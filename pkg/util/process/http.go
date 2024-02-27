/*
Copyright 2022 The Katalyst Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package process

import (
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/klog/v2"

	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/credential"
	"github.com/kubewharf/katalyst-core/pkg/util/credential/authorization"
)

const (
	httpDefaultTimeout     = time.Second * 10
	httpDefaultConnTimeout = time.Second * 3
)

const (
	HTTPChainCredential  = "credential"
	HTTPChainRateLimiter = "rateLimiter"
	HTTPChainMonitor     = "monitor"
)

const (
	HTTPRequestCount       = "http_request_count"
	HTTPAuthenticateFailed = "http_request_authenticate_failed"
	HTTPNoPermission       = "http_request_no_permission"
	HTTPThrottled          = "http_request_throttled"

	UserUnknown = "unknown"
)

type contextKey string

const (
	KeyAuthInfo contextKey = "auth"
)

var (
	httpCleanupVisitorPeriod = time.Minute * 3
)

type visitor struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

type HTTPHandler struct {
	mux       sync.Mutex
	cred      credential.Credential
	accessCtl authorization.AccessControl

	enabled              sets.String
	visitors             map[string]*visitor
	authInfo             map[string]string
	skipAuthURLPrefix    []string
	strictAuthentication bool

	emitter metrics.MetricEmitter
}

func NewHTTPHandler(enabled []string, skipAuthURLPrefix []string, strictAuthentication bool, emitter metrics.MetricEmitter) *HTTPHandler {

	return &HTTPHandler{
		visitors: make(map[string]*visitor),
		enabled:  sets.NewString(enabled...),
		// no credential by default
		cred: credential.DefaultCredential(),
		// no authorization check by default
		accessCtl:            authorization.DefaultAccessControl(),
		skipAuthURLPrefix:    skipAuthURLPrefix,
		strictAuthentication: strictAuthentication,
		emitter:              emitter,
	}
}

func (h *HTTPHandler) Run(ctx context.Context) {
	if h.enabled.Has(HTTPChainRateLimiter) {
		go wait.Until(h.cleanupVisitor, httpCleanupVisitorPeriod, ctx.Done())
	}

	if h.enabled.Has(HTTPChainCredential) {
		h.cred.Run(ctx)
		h.accessCtl.Run(ctx)
	}
}

func (h *HTTPHandler) getHTTPVisitor(subject string) *rate.Limiter {
	h.mux.Lock()
	defer h.mux.Unlock()

	v, exists := h.visitors[subject]
	if !exists {
		limiter := rate.NewLimiter(0.5, 1)
		h.visitors[subject] = &visitor{limiter, time.Now()}
		return limiter
	}

	v.lastSeen = time.Now()
	return v.limiter
}

// cleanupVisitor periodically cleanups visitors if they are not called for a long time
func (h *HTTPHandler) cleanupVisitor() {
	h.mux.Lock()
	defer h.mux.Unlock()

	for addr, v := range h.visitors {
		if time.Since(v.lastSeen) > httpCleanupVisitorPeriod {
			delete(h.visitors, addr)
		}
	}
}

// withBasicAuth is used to verify the requests and bind authInfo to request.
func (h *HTTPHandler) withCredential(f http.HandlerFunc) http.HandlerFunc {

	skipAuth := func(r *http.Request) bool {
		for _, prefix := range h.skipAuthURLPrefix {
			if strings.HasPrefix(r.URL.Path, prefix) {
				return true
			}
		}
		return false
	}

	return func(w http.ResponseWriter, r *http.Request) {
		if r == nil {
			klog.Warningf("request is nil")
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		var authInfo credential.AuthInfo
		shouldSkipAuth := skipAuth(r)
		if shouldSkipAuth {
			f(w, r)
			return
		}

		var err error
		authInfo, err = h.cred.Auth(r)
		if err != nil {
			if h.strictAuthentication {
				klog.Warningf("request %+v doesn't have proper auth", r.URL)
				w.Header().Set("Katalyst-Authenticate", `Basic realm="Restricted"`)
				w.WriteHeader(http.StatusUnauthorized)
				_ = h.emitter.StoreInt64(HTTPAuthenticateFailed, 1, metrics.MetricTypeNameCount,
					metrics.MetricTag{Key: "path", Val: r.URL.Path})
				return
			}
		} else {
			r = attachAuthInfo(r, authInfo)
			klog.V(4).Infof("user %v request %+v  with auth type %v", authInfo.SubjectName(), r.URL, authInfo.AuthType())
			if verifyErr := h.accessCtl.Verify(authInfo, authorization.PermissionTypeHttpEndpoint); verifyErr != nil &&
				h.strictAuthentication {
				klog.Warningf("request %+v with user %v doesn't have permission, msg: %v", r.URL, authInfo.SubjectName(), verifyErr)
				w.Header().Set("Katalyst-Authenticate", `Basic realm="Restricted"`)
				w.WriteHeader(http.StatusUnauthorized)
				_ = h.emitter.StoreInt64(HTTPNoPermission, 1, metrics.MetricTypeNameCount,
					metrics.MetricTag{Key: "path", Val: r.URL.Path},
					metrics.MetricTag{Key: "user", Val: authInfo.SubjectName()})
				return
			}
		}

		if authInfo != nil {
			klog.V(4).Infof("user %v request %+v is valid", authInfo.SubjectName(), r.URL)
		}
		f(w, r)
	}
}

// withRateLimiter is used to limit user-requests to protect server
func (h *HTTPHandler) withRateLimiter(f http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r != nil {
			rateLimiterKey := r.RemoteAddr
			authInfo, err := getAuthInfo(r)
			if err != nil {
				klog.Warningf("request %+v has no valid auth info bound to it, using Remote address %v as RateLimiter key, err: %v", r.URL, r.RemoteAddr, err)
			} else {
				rateLimiterKey = authInfo.SubjectName()
			}

			limiter := h.getHTTPVisitor(rateLimiterKey)
			if !limiter.Allow() {
				klog.Warningf("request %+v has too many requests from %v", r.URL, rateLimiterKey)
				w.Header().Set("Katalyst-Limit", `too many requests`)
				w.WriteHeader(http.StatusTooManyRequests)
				_ = h.emitter.StoreInt64(HTTPThrottled, 1, metrics.MetricTypeNameCount,
					metrics.MetricTag{Key: "path", Val: r.URL.Path},
					metrics.MetricTag{Key: "rateLimiterKey", Val: rateLimiterKey})
				return
			}
		}

		f(w, r)
	}
}

func (h *HTTPHandler) withMonitor(f http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		f(w, r)

		user := UserUnknown
		if authInfo, err := getAuthInfo(r); err == nil {
			user = authInfo.SubjectName()
		}
		_ = h.emitter.StoreInt64(HTTPRequestCount, 1, metrics.MetricTypeNameCount,
			metrics.MetricTag{Key: "path", Val: r.URL.Path},
			metrics.MetricTag{Key: "user", Val: user})
	}
}

func (h *HTTPHandler) WithCredential(cred credential.Credential) error {
	if cred == nil {
		return fmt.Errorf("nil Credential is not allowed")
	}

	h.cred = cred
	return nil
}

func (h *HTTPHandler) WithAuthorization(auth authorization.AccessControl) error {
	if auth == nil {
		return fmt.Errorf("nil AccessControl is not allowed")
	}

	h.accessCtl = auth
	return nil
}

// WithHandleChain builds handler chains for http.Handler
func (h *HTTPHandler) WithHandleChain(f http.Handler) http.Handler {
	// build orders for http chains
	chains := []string{HTTPChainMonitor, HTTPChainRateLimiter, HTTPChainCredential}
	funcs := map[string]func(http.HandlerFunc) http.HandlerFunc{
		HTTPChainRateLimiter: h.withRateLimiter,
		HTTPChainCredential:  h.withCredential,
		HTTPChainMonitor:     h.withMonitor,
	}

	var handler http.Handler = f
	for _, c := range chains {
		if h.enabled.Has(c) {
			tmpHandler := handler
			handler = funcs[c](func(w http.ResponseWriter, r *http.Request) {
				tmpHandler.ServeHTTP(w, r)
			})
		}
	}
	return handler
}

// NewDefaultHTTPClient returns a raw HTTP client.
func NewDefaultHTTPClient() *http.Client {
	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   httpDefaultConnTimeout,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}

	client := &http.Client{
		Timeout:   httpDefaultTimeout,
		Transport: transport,
	}
	return client
}

// GetAndUnmarshal gets data from the given url and unmarshal it into the given struct.
func GetAndUnmarshal(url string, v interface{}) error {
	resp, err := http.Get(url)
	if err != nil {
		return err
	}

	defer resp.Body.Close()
	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("invalid response status code %d, url: %s", resp.StatusCode, url)
	}

	err = json.Unmarshal(body, v)
	if err != nil {
		return err
	}

	return nil
}

func attachAuthInfo(r *http.Request, authInfo credential.AuthInfo) *http.Request {
	if authInfo == nil {
		return r
	}
	newCtx := context.WithValue(r.Context(), KeyAuthInfo, &authInfo)
	return r.WithContext(newCtx)
}

func getAuthInfo(r *http.Request) (credential.AuthInfo, error) {
	value := r.Context().Value(KeyAuthInfo)
	if value == nil {
		return nil, fmt.Errorf("no auth info bound to this request")
	}

	authInfo, ok := value.(*credential.AuthInfo)
	if !ok {
		return nil, fmt.Errorf("invalid auth info bound to this request")
	}

	return *authInfo, nil
}

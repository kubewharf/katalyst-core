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
	"io/ioutil"
	"net"
	"net/http"
	"sync"
	"time"

	"golang.org/x/time/rate"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/klog/v2"
)

const (
	httpDefaultTimeout     = time.Second * 10
	httpDefaultConnTimeout = time.Second * 3
)

const (
	HTTPChainAuth        = "auth"
	HTTPChainRateLimiter = "rateLimiter"
)

var (
	httpCleanupVisitorPeriod = time.Minute * 3
	httpSyncPasswdPeriod     = time.Minute * 3
)

// GetAuthPair is a uniformed helper function to get auth pair maps (user:passwd)
type GetAuthPair func() (map[string]string, error)

type visitor struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

type HTTPHandler struct {
	mux     sync.Mutex
	authFun GetAuthPair

	enabled  sets.String
	visitors map[string]*visitor
	authInfo map[string]string
}

func NewHTTPHandler(authFun GetAuthPair, enabled []string) *HTTPHandler {
	return &HTTPHandler{
		visitors: make(map[string]*visitor),
		authFun:  authFun,
		enabled:  sets.NewString(enabled...),
	}
}

func (h *HTTPHandler) Run(ctx context.Context) {
	if h.enabled.Has(HTTPChainRateLimiter) {
		go wait.Until(h.cleanupVisitor, httpCleanupVisitorPeriod, ctx.Done())
	}

	if h.enabled.Has(HTTPChainAuth) {
		go wait.Until(h.syncPasswd, httpSyncPasswdPeriod, ctx.Done())
	}
}

func (h *HTTPHandler) getHTTPVisitor(addr string) *rate.Limiter {
	h.mux.Lock()
	defer h.mux.Unlock()

	v, exists := h.visitors[addr]
	if !exists {
		limiter := rate.NewLimiter(0.2, 1)
		h.visitors[addr] = &visitor{limiter, time.Now()}
		return limiter
	}

	v.lastSeen = time.Now()
	return v.limiter
}

func (h *HTTPHandler) getAuthPair(user string) (string, bool) {
	h.mux.Lock()
	defer h.mux.Unlock()

	passwd, ok := h.authInfo[user]
	return passwd, ok
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

// syncPasswd periodically syncs user and passwd since it may change dynamically
func (h *HTTPHandler) syncPasswd() {
	if pairs, err := h.authFun(); err != nil {
		klog.Errorf("failed to get auth pairs: %v", err)
	} else {
		h.mux.Lock()
		h.authInfo = pairs
		h.mux.Unlock()
	}
}

// withBasicAuth is used to verify the requests with basic-auth
// the common use cases are: `curl -i --user "user-name:user-passwd"`
func (h *HTTPHandler) withBasicAuth(f http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r != nil {
			user, passwd, ok := r.BasicAuth()
			if ok {
				klog.V(4).Infof("user %v request %+v ", user, r.URL)
				expectedPasswd, ok := h.getAuthPair(user)
				if ok && passwd == expectedPasswd {
					klog.V(4).Infof("user %v request %+v is valid", user, r.URL)
					f(w, r)
					return
				}
				klog.Warningf("request %+v with user %v doesn't have proper auth", r.URL, user)
			}
		}

		klog.Warningf("request %+v doesn't have proper auth", r.URL)
		w.Header().Set("Katalyst-Authenticate", `Basic realm="Restricted"`)
		w.WriteHeader(http.StatusUnauthorized)
	}
}

// withRateLimiter is used to limit user-requests to protect server
// todo, actually, we'd better to build different limiter rate for each user
func (h *HTTPHandler) withRateLimiter(f http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r != nil {
			limiter := h.getHTTPVisitor(r.RemoteAddr)
			if !limiter.Allow() {
				klog.Warningf("request %+v has too many requests from addr %v", r.URL, r.RemoteAddr)
				w.Header().Set("Katalyst-Limit", `too many requests`)
				w.WriteHeader(http.StatusTooManyRequests)
				return
			}
		}

		f(w, r)
	}
}

// WithHandleChain builds handler chains for http.Handler
func (h *HTTPHandler) WithHandleChain(f http.Handler) http.Handler {
	// build orders for http chains
	chains := []string{HTTPChainRateLimiter, HTTPChainAuth}
	funcs := map[string]func(http.HandlerFunc) http.HandlerFunc{
		HTTPChainRateLimiter: h.withRateLimiter,
		HTTPChainAuth:        h.withBasicAuth,
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

	err = json.Unmarshal(body, v)
	if err != nil {
		return err
	}

	return nil
}

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
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func emptyAuthPair() (map[string]string, error) {
	return map[string]string{}, nil
}

func dummyAuthPair() (map[string]string, error) {
	return map[string]string{
		"t-user-1": "t-passwd",
	}, nil
}

type dummyHandler struct {
	success int
}

func (d *dummyHandler) ServeHTTP(_ http.ResponseWriter, _ *http.Request) {
	d.success++
}

type dummyResponseWriter struct{}

func (d dummyResponseWriter) Header() http.Header       { return make(http.Header) }
func (d dummyResponseWriter) Write([]byte) (int, error) { return 0, nil }
func (d dummyResponseWriter) WriteHeader(_ int)         {}

func TestHTTPHandler(t *testing.T) {
	t.Parallel()

	httpCleanupVisitorPeriod = time.Second
	httpSyncPasswdPeriod = time.Second

	type req struct {
		burst   int
		success int
		user    string
		passwd  string
	}

	for _, tc := range []struct {
		comment  string
		pairFunc GetAuthPair
		enabled  []string
		reqs     []req
		visitCnt int
	}{
		{
			comment:  "empty chain to pass all",
			pairFunc: emptyAuthPair,
			enabled:  []string{},
			reqs: []req{
				{
					burst:   2,
					success: 2,
					user:    "t-user-1",
					passwd:  "t-passwd",
				},
			},
			visitCnt: 0,
		},
		{
			comment:  "rate limiter chain to limit requests",
			pairFunc: emptyAuthPair,
			enabled:  []string{HTTPChainRateLimiter},
			reqs: []req{
				{
					burst:   1,
					success: 1,
					user:    "t-user-1",
					passwd:  "t-passwd",
				},
				{
					burst:   2,
					success: 1,
					user:    "t-user-2",
					passwd:  "t-passwd",
				},
			},
			visitCnt: 2,
		},
		{
			comment:  "auth chain to build auth for user",
			pairFunc: dummyAuthPair,
			enabled:  []string{HTTPChainAuth},
			reqs: []req{
				{
					burst:   2,
					success: 2,
					user:    "t-user-1",
					passwd:  "t-passwd",
				},
				{
					burst:   1,
					success: 0,
					user:    "t-user-2",
					passwd:  "t-passwd",
				},
			},
			visitCnt: 0,
		},
		{
			comment:  "auth chain to limit requests",
			pairFunc: dummyAuthPair,
			enabled:  []string{HTTPChainAuth, HTTPChainRateLimiter},
			reqs: []req{
				{
					burst:   2,
					success: 1,
					user:    "t-user-1",
					passwd:  "t-passwd",
				},
				{
					burst:   2,
					success: 0,
					user:    "t-user-2",
					passwd:  "t-passwd",
				},
			},
			visitCnt: 1,
		},
	} {
		t.Logf("test case: %v", tc.comment)
		h := NewHTTPHandler(tc.pairFunc, tc.enabled)

		ctx, cancel := context.WithCancel(context.Background())
		h.Run(ctx)
		time.Sleep(time.Second)

		for _, r := range tc.reqs {
			f := &dummyHandler{}
			hf := h.WithHandleChain(f)

			for j := 0; j < r.burst; j++ {
				hr := &http.Request{
					Header:     make(http.Header),
					RemoteAddr: fmt.Sprintf("%v", r.user),
					Method:     fmt.Sprintf("%v-order-%v", r.user, j),
				}
				hr.SetBasicAuth(r.user, r.passwd)

				hf.ServeHTTP(dummyResponseWriter{}, hr)
			}

			assert.Equal(t, r.success, f.success)
		}

		h.mux.Lock()
		assert.Equal(t, tc.visitCnt, len(h.visitors))
		h.mux.Unlock()

		time.Sleep(time.Second * 3)

		h.mux.Lock()
		assert.Equal(t, 0, len(h.visitors))
		h.mux.Unlock()

		cancel()
	}
}

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

	type req struct {
		burst      int
		success    int
		remoteAddr string
	}

	for _, tc := range []struct {
		comment  string
		enabled  []string
		reqs     []req
		visitCnt int
	}{
		{
			comment: "empty chain to pass all",
			enabled: []string{},
			reqs: []req{
				{
					burst:      2,
					success:    2,
					remoteAddr: "addr-1",
				},
			},
			visitCnt: 0,
		},
		{
			comment: "rate limiter chain to limit requests",
			enabled: []string{HTTPChainRateLimiter},
			reqs: []req{
				{
					burst:      1,
					success:    1,
					remoteAddr: "addr-1",
				},
				{
					burst:      2,
					success:    1,
					remoteAddr: "addr-2",
				},
			},
			visitCnt: 2,
		},
		{
			comment: "auth chain to build auth for user",
			enabled: []string{HTTPChainCredential},
			reqs: []req{
				{
					burst:      2,
					success:    2,
					remoteAddr: "addr-1",
				},
				{
					burst:      1,
					success:    1,
					remoteAddr: "addr-2",
				},
			},
			visitCnt: 0,
		},
		{
			comment: "auth chain to limit requests",
			enabled: []string{HTTPChainCredential, HTTPChainRateLimiter},
			reqs: []req{
				{
					burst:      2,
					success:    1,
					remoteAddr: "addr-1",
				},
				{
					burst:      2,
					success:    0,
					remoteAddr: "addr-2",
				},
			},
			visitCnt: 1,
		},
	} {
		t.Logf("test case: %v", tc.comment)
		h := NewHTTPHandler(tc.enabled, []string{})

		ctx, cancel := context.WithCancel(context.Background())
		h.Run(ctx)
		time.Sleep(time.Second)

		for _, r := range tc.reqs {
			f := &dummyHandler{}
			hf := h.WithHandleChain(f)

			for j := 0; j < r.burst; j++ {
				hr := &http.Request{
					Header:     make(http.Header),
					RemoteAddr: fmt.Sprintf("%v", r.remoteAddr),
					Method:     fmt.Sprintf("%v-order-%v", r.remoteAddr, j),
				}

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

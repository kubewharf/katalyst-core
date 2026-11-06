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

package pod

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRuntimeCgroupPathHandlerInitializerIgnoresNil(t *testing.T) {
	t.Parallel()
	r := runtimeCgroupPathHandlerInitializerRegistry{}
	r.register(nil)
	require.Empty(t, r.snapshot())
}

func TestRuntimeCgroupPathHandlerInitializerSnapshotIsStable(t *testing.T) {
	t.Parallel()
	r := runtimeCgroupPathHandlerInitializerRegistry{}
	r.register(func(RuntimePodFetcher) {})
	snap := r.snapshot()
	r.register(func(RuntimePodFetcher) {})
	require.Len(t, snap, 1)
	require.Len(t, r.snapshot(), 2)
}

func TestRuntimeCgroupPathHandlerInitializerPreservesOrderAndFetcher(t *testing.T) {
	t.Parallel()
	r := runtimeCgroupPathHandlerInitializerRegistry{}
	fetcher := &runtimePodFetcherStub{}
	order := make([]int, 0, 2)
	fetchers := make([]RuntimePodFetcher, 0, 2)
	r.register(func(f RuntimePodFetcher) { order = append(order, 1); fetchers = append(fetchers, f) })
	r.register(func(f RuntimePodFetcher) { order = append(order, 2); fetchers = append(fetchers, f) })
	r.initialize(fetcher)
	require.Equal(t, []int{1, 2}, order)
	require.Len(t, fetchers, 2)
	require.Equal(t, fetcher, fetchers[0])
	require.Equal(t, fetcher, fetchers[1])
}

func TestRuntimeCgroupPathHandlerInitializerRegistryConcurrentAccess(t *testing.T) {
	t.Parallel()
	r := runtimeCgroupPathHandlerInitializerRegistry{}
	const n = 32
	var wg sync.WaitGroup
	wg.Add(n + 1)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			r.register(func(RuntimePodFetcher) {})
		}()
	}
	go func() {
		defer wg.Done()
		for i := 0; i < n; i++ {
			_ = r.snapshot()
		}
	}()
	wg.Wait()
	require.Len(t, r.snapshot(), n)
}

// TestRuntimeCgroupPathHandlerLateRegistrationGetsLiveFetcher ensures an
// initializer registered after initialize has already run is handed the live
// fetcher immediately, rather than being dropped.
func TestRuntimeCgroupPathHandlerLateRegistrationGetsLiveFetcher(t *testing.T) {
	t.Parallel()
	r := runtimeCgroupPathHandlerInitializerRegistry{}
	fetcher := &runtimePodFetcherStub{}
	r.initialize(fetcher)

	got := make(chan RuntimePodFetcher, 1)
	r.register(func(f RuntimePodFetcher) { got <- f })
	require.Equal(t, fetcher, <-got)
}

// TestKataInitializerOnLocalRegistry wires the real production Kata
// initializer (RegisterKataContainerFetcher) into a LOCAL registry and runs the
// fan-out through initializeRuntimeCgroupPathHandlersOn. It never touches the
// package-global registry, so it is safe to run in parallel.
func TestKataInitializerOnLocalRegistry(t *testing.T) {
	t.Parallel()
	r := runtimeCgroupPathHandlerInitializerRegistry{}
	r.register(RegisterKataContainerFetcher)
	require.Len(t, r.snapshot(), 1)
	// Fan-out must not panic even with a nil stub fetcher; the Kata initializer
	// tolerates nil fetcher via sync.Once and returns early on lookup.
	initializeRuntimeCgroupPathHandlersOn(&r, nil)
}

/*
Copyright 2026 The Katalyst Authors.

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

package common

import (
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestRegisterRelativeCgroupPathHandlerWithUnregisterPreservesOrderAndRegistrationIdentity(t *testing.T) {
	t.Parallel()

	handler := RelativeCgroupPathHandler{
		Name: "same-handler-instance",
		Handler: func(_, _ string) (string, bool, error) {
			return "", true, nil
		},
	}
	unregisterFirst := RegisterRelativeCgroupPathHandlerWithUnregister(handler)
	t.Cleanup(unregisterFirst)
	unregisterSecond := RegisterRelativeCgroupPathHandlerWithUnregister(handler)
	t.Cleanup(unregisterSecond)

	handlers := snapshotRelativeCgroupPathHandlers()
	require.NotEmpty(t, handlers)
	require.Equal(t, defaultCgroupPathHandlerName, handlers[0].Name)
	indices := relativeHandlerIndicesByName(handlers, handler.Name)
	require.Len(t, indices, 2)
	require.Less(t, indices[0], indices[1])
	ids := relativeHandlerRegistrationIDsByName(handler.Name)
	require.Len(t, ids, 2)
	require.NotZero(t, ids[0])
	require.NotZero(t, ids[1])
	require.NotEqual(t, ids[0], ids[1])

	unregisterFirst()
	unregisterFirst()
	handlers = snapshotRelativeCgroupPathHandlers()
	require.Equal(t, defaultCgroupPathHandlerName, handlers[0].Name)
	require.Equal(t, 1, countRelativeHandlersByName(handlers, handler.Name))

	unregisterSecond()
	handlers = snapshotRelativeCgroupPathHandlers()
	require.Equal(t, defaultCgroupPathHandlerName, handlers[0].Name)
	require.Zero(t, countRelativeHandlersByName(handlers, handler.Name))
}

func TestRelativeCgroupPathHandlerConcurrentSnapshotAndUnregister(t *testing.T) {
	t.Parallel()

	const workers = 32
	errCh := make(chan error, workers)
	var wg sync.WaitGroup
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		i := i
		go func() {
			defer wg.Done()
			name := fmt.Sprintf("concurrent-relative-handler-%d", i)
			unregister := RegisterRelativeCgroupPathHandlerWithUnregister(RelativeCgroupPathHandler{
				Name: name,
				Handler: func(_, _ string) (string, bool, error) {
					return "", true, nil
				},
			})
			handlers := snapshotRelativeCgroupPathHandlers()
			if len(handlers) == 0 || handlers[0].Name != defaultCgroupPathHandlerName {
				errCh <- fmt.Errorf("snapshot lost default-first invariant: %#v", handlers)
			}
			unregister()
			unregister()
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		require.NoError(t, err)
	}

	handlers := snapshotRelativeCgroupPathHandlers()
	require.NotEmpty(t, handlers)
	require.Equal(t, defaultCgroupPathHandlerName, handlers[0].Name)
	for i := 0; i < workers; i++ {
		require.Zero(t, countRelativeHandlersByName(handlers, fmt.Sprintf("concurrent-relative-handler-%d", i)))
	}
}

func relativeHandlerIndicesByName(handlers []RelativeCgroupPathHandler, name string) []int {
	var indices []int
	for i, handler := range handlers {
		if handler.Name == name {
			indices = append(indices, i)
		}
	}
	return indices
}

func countRelativeHandlersByName(handlers []RelativeCgroupPathHandler, name string) int {
	return len(relativeHandlerIndicesByName(handlers, name))
}

func relativeHandlerRegistrationIDsByName(name string) []uint64 {
	relativeCgroupPathHandlerLock.RLock()
	defer relativeCgroupPathHandlerLock.RUnlock()

	var ids []uint64
	for _, registered := range relativeCgroupPathHandlerList {
		if registered.handler.Name == name {
			ids = append(ids, registered.id)
		}
	}
	return ids
}

// handlerAggregationOutcome models what one registered handler returns for a
// synthetic lookup: skip mirrors a runtime detector deciding the container is
// out of its runtime's scope, err is what it returns when it did not skip.
type handlerAggregationOutcome struct {
	name string
	skip bool
	err  error
}

// TestResolveContainerCgroupPathHandlerAggregation is the regression test for
// the multi-handler aggregation contract shared by GetContainerAbsCgroupPath and
// GetContainerRelativeCgroupPath. It calls the unexported resolvers directly
// with constructed handler lists, because the production registries carry the
// real kata/duma/default handlers and cannot be isolated per-case.
//
// Semantics under test:
//   - a runtime detector turning gRPC NotFound into skip must let the aggregate
//     fall through to typed os.ErrNotExist once no handler resolves the
//     container (failure to find, not an operational outage);
//   - any non-absence operational error (e.g. CRI Internal/Unavailable) must
//     dominate absence so lifecycle callers fail closed instead of mistaking a
//     broken runtime for a retired container.
//
// The kata handler's single-layer classification (NotFound->skip,
// Internal/Unavailable->error) lives in
// pkg/metaserver/agent/pod/kata_test.go
// (TestKataContainerFetcher_CRIErrorSingleHandlerClassification). The duma-like
// detector reuses this exact aggregator; its own NotFound->skip mapping is
// covered in the katalyst-adapter repository (commit 7d83297b), so this package
// only pins the aggregation semantics both detectors rely on.
func TestResolveContainerCgroupPathHandlerAggregation(t *testing.T) {
	t.Parallel()

	// notFound wraps os.ErrNotExist the same way the cgroupfs default probe does.
	notFound := fmt.Errorf("failed to find cgroup path: %w", os.ErrNotExist)
	internal := status.Error(codes.Internal, "runtime backend exploded")
	unavailable := status.Error(codes.Unavailable, "runtime backend down")

	cases := []struct {
		name string
		// detectors stand in for the runtime detectors (kata-like, duma-like)
		// registered ahead of the cgroupfs default handler.
		detectors []handlerAggregationOutcome
		default_  handlerAggregationOutcome
		// wantAbsence means the aggregate must classify as typed os.ErrNotExist.
		// When false it must surface an operational error that is NOT os.ErrNotExist.
		wantAbsence bool
	}{
		{
			name: "a all detectors skip NotFound, default absent -> os.ErrNotExist",
			detectors: []handlerAggregationOutcome{
				{name: "kata-mock", skip: true},
				{name: "duma-mock", skip: true},
			},
			default_:    handlerAggregationOutcome{name: "default", skip: false, err: notFound},
			wantAbsence: true,
		},
		{
			name: "b kata Internal fails closed instead of absence",
			detectors: []handlerAggregationOutcome{
				{name: "kata-mock", skip: false, err: internal},
				{name: "duma-mock", skip: true},
			},
			default_:    handlerAggregationOutcome{name: "default", skip: false, err: notFound},
			wantAbsence: false,
		},
		{
			name: "c duma Unavailable fails closed instead of absence",
			detectors: []handlerAggregationOutcome{
				{name: "kata-mock", skip: true},
				{name: "duma-mock", skip: false, err: unavailable},
			},
			default_:    handlerAggregationOutcome{name: "default", skip: false, err: notFound},
			wantAbsence: false,
		},
		{
			name: "d kata skip, duma operational -> operational error dominates",
			detectors: []handlerAggregationOutcome{
				{name: "kata-mock", skip: true},
				{name: "duma-mock", skip: false, err: internal},
			},
			default_:    handlerAggregationOutcome{name: "default", skip: false, err: notFound},
			wantAbsence: false,
		},
		{
			name: "e every handler skips -> os.ErrNotExist",
			detectors: []handlerAggregationOutcome{
				{name: "kata-mock", skip: true},
				{name: "duma-mock", skip: true},
			},
			default_:    handlerAggregationOutcome{name: "default", skip: true},
			wantAbsence: true,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			absHandlers := make([]AbsoluteCgroupPathHandler, 0, len(tc.detectors)+1)
			for _, d := range tc.detectors {
				d := d
				absHandlers = append(absHandlers, AbsoluteCgroupPathHandler{
					Name: d.name,
					Handler: func(_, _, _ string) (string, bool, error) {
						return "", d.skip, d.err
					},
				})
			}
			absHandlers = append(absHandlers, AbsoluteCgroupPathHandler{
				Name: tc.default_.name,
				Handler: func(_, _, _ string) (string, bool, error) {
					return "", tc.default_.skip, tc.default_.err
				},
			})

			relHandlers := make([]RelativeCgroupPathHandler, 0, len(tc.detectors)+1)
			for _, d := range tc.detectors {
				d := d
				relHandlers = append(relHandlers, RelativeCgroupPathHandler{
					Name: d.name,
					Handler: func(_, _ string) (string, bool, error) {
						return "", d.skip, d.err
					},
				})
			}
			relHandlers = append(relHandlers, RelativeCgroupPathHandler{
				Name: tc.default_.name,
				Handler: func(_, _ string) (string, bool, error) {
					return "", tc.default_.skip, tc.default_.err
				},
			})

			absPath, absErr := resolveContainerAbsCgroupPath(absHandlers, "cpu", "pod", "container")
			relPath, relErr := resolveContainerRelativeCgroupPath(relHandlers, "pod", "container")

			if tc.wantAbsence {
				require.Empty(t, absPath)
				require.Empty(t, relPath)
				require.ErrorIs(t, absErr, os.ErrNotExist,
					"absolute aggregate must be typed absence, not operational: %v", absErr)
				require.ErrorIs(t, relErr, os.ErrNotExist,
					"relative aggregate must be typed absence, not operational: %v", relErr)
			} else {
				require.Error(t, absErr)
				require.Error(t, relErr)
				require.False(t, errors.Is(absErr, os.ErrNotExist),
					"operational error must fail closed, not absence: %v", absErr)
				require.False(t, errors.Is(relErr, os.ErrNotExist),
					"operational error must fail closed, not absence: %v", relErr)
			}
		})
	}
}

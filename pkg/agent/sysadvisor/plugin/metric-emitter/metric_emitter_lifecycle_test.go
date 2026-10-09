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

// Copyright 2022 The Katalyst Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package metric_emitter

import (
	"context"
	"errors"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/metric-emitter/syncer"
)

// testTimeout bounds every synchronization point so a broken lifecycle fails the
// test instead of hanging it.
const testTimeout = 2 * time.Second

var errFakeMissingDependency = errors.New("fake syncer: missing dependency")

// fakeSyncer is a syncer.CustomMetricSyncer used to drive the emitter lifecycle.
// It exposes channels for synchronization control so tests can observe exactly
// when a syncer starts, is stopped (ctx canceled), and returns from Run.
type fakeSyncer struct {
	name string

	// startErr, when non-nil, is returned by Run immediately after started is
	// closed, simulating a syncer whose dependency validation fails.
	startErr error

	started  chan struct{} // closed exactly once Run is entered
	stopped  chan struct{} // closed after ctx is canceled and Run is about to return
	returned chan struct{} // closed when Run has returned
}

func newFakeSyncer(name string, startErr error) *fakeSyncer {
	return &fakeSyncer{
		name:     name,
		startErr: startErr,
		started:  make(chan struct{}),
		stopped:  make(chan struct{}),
		returned: make(chan struct{}),
	}
}

func (f *fakeSyncer) Name() string { return f.name }

func (f *fakeSyncer) Run(ctx context.Context) (runErr error) {
	close(f.started)
	if f.startErr != nil {
		return f.startErr
	}

	<-ctx.Done()
	close(f.stopped)
	return nil
}

// runEmitter starts the emitter in a goroutine and returns a channel that is
// closed when emitter.Run returns.
func runEmitter(cme *CustomMetricEmitter, ctx context.Context) (done chan struct{}) {
	done = make(chan struct{})
	go func() {
		defer close(done)
		cme.Run(ctx)
	}()
	return done
}

func requireClosed(t *testing.T, ch chan struct{}, msgAndArgs ...interface{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(testTimeout):
		t.Fatalf("timed out waiting for channel: %v", msgAndArgs...)
	}
}

func requireNotClosed(t *testing.T, ch chan struct{}, msgAndArgs ...interface{}) {
	t.Helper()
	select {
	case <-ch:
		t.Fatalf("channel should not be closed yet: %v", msgAndArgs...)
	case <-time.After(100 * time.Millisecond):
	}
}

// TestCustomMetricEmitter_NormalCancelNoLeak ensures that when every syncer
// starts successfully, canceling the context stops every syncer and makes
// Run return, without leaving managed goroutines behind.
func TestCustomMetricEmitter_NormalCancelNoLeak(t *testing.T) {
	t.Parallel()

	healthyA := newFakeSyncer("healthy-a", nil)
	healthyB := newFakeSyncer("healthy-b", nil)

	cme := &CustomMetricEmitter{
		name:    PluginNameCustomMetricEmitter,
		syncers: []syncer.CustomMetricSyncer{healthyA, healthyB},
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := runEmitter(cme, ctx)

	// both syncers must have started
	requireClosed(t, healthyA.started, "healthy-a started")
	requireClosed(t, healthyB.started, "healthy-b started")

	// emitter must still be running (nothing canceled yet)
	requireNotClosed(t, done, "emitter running before cancel")

	cancel()

	// cancellation must propagate to every syncer and Run must return
	requireClosed(t, healthyA.stopped, "healthy-a stopped after cancel")
	requireClosed(t, healthyB.stopped, "healthy-b stopped after cancel")
	requireClosed(t, done, "emitter.Run returned after cancel")
}

// TestCustomMetricEmitter_PartialStartFailureCancelsOthers ensures that when one
// syncer fails to start, the emitter cancels the already-started syncers, waits
// for them, and returns -- instead of panicking inside a detached goroutine or
// leaking the started syncers.
func TestCustomMetricEmitter_PartialStartFailureCancelsOthers(t *testing.T) {
	t.Parallel()

	healthy := newFakeSyncer("healthy", nil)
	failing := newFakeSyncer("failing", errFakeMissingDependency)

	cme := &CustomMetricEmitter{
		name:    PluginNameCustomMetricEmitter,
		syncers: []syncer.CustomMetricSyncer{healthy, failing},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := runEmitter(cme, ctx)

	// both syncers have been entered; the failing one returns an error.
	requireClosed(t, healthy.started, "healthy started")
	requireClosed(t, failing.started, "failing started")

	// the failing syncer's error must tear down the healthy one: its ctx must be
	// canceled and it must stop.
	requireClosed(t, healthy.stopped, "healthy stopped after sibling failure")
	// and the emitter itself must return without us canceling the parent ctx.
	requireClosed(t, done, "emitter.Run returned after partial start failure")
}

// TestCustomMetricEmitter_AllSyncersFail ensures that when every syncer fails to
// start, the emitter still returns cleanly (no hang, no panic).
func TestCustomMetricEmitter_AllSyncersFail(t *testing.T) {
	t.Parallel()

	failingA := newFakeSyncer("failing-a", errFakeMissingDependency)
	failingB := newFakeSyncer("failing-b", errFakeMissingDependency)

	cme := &CustomMetricEmitter{
		name:    PluginNameCustomMetricEmitter,
		syncers: []syncer.CustomMetricSyncer{failingA, failingB},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := runEmitter(cme, ctx)

	requireClosed(t, failingA.started, "failing-a started")
	requireClosed(t, failingB.started, "failing-b started")
	requireClosed(t, done, "emitter.Run returned when all syncers failed")
}

// TestCustomMetricEmitter_EmptySyncersReturnsOnCancel ensures that with no
// syncers registered, Run still behaves correctly and returns on cancel.
func TestCustomMetricEmitter_EmptySyncersReturnsOnCancel(t *testing.T) {
	t.Parallel()

	cme := &CustomMetricEmitter{
		name:    PluginNameCustomMetricEmitter,
		syncers: nil,
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := runEmitter(cme, ctx)

	// with no syncers, Run immediately blocks on <-ctx.Done(); it must return
	// once we cancel.
	requireNotClosed(t, done, "emitter running before cancel")
	cancel()
	requireClosed(t, done, "emitter.Run returned after cancel")
}

// TestCustomMetricEmitter_NoGoroutineLeak is a coarse leak check: after a normal
// start/cancel cycle settles, the number of goroutines must not have grown.
func TestCustomMetricEmitter_NoGoroutineLeak(t *testing.T) {
	t.Parallel()

	// warm up so runtime goroutine bookkeeping settles
	runtime.Gosched()
	before := runtime.NumGoroutine()

	healthy := newFakeSyncer("healthy", nil)
	cme := &CustomMetricEmitter{
		name:    PluginNameCustomMetricEmitter,
		syncers: []syncer.CustomMetricSyncer{healthy},
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := runEmitter(cme, ctx)
	requireClosed(t, healthy.started, "healthy started")

	cancel()
	requireClosed(t, healthy.stopped, "healthy stopped")
	requireClosed(t, done, "emitter.Run returned")

	// settle and allow any stray managed goroutine to finish
	require.Eventually(t, func() bool {
		return runtime.NumGoroutine() <= before
	}, testTimeout, 5*time.Millisecond, "goroutine count should not grow after emitter shutdown")
}

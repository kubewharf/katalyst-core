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

package advisor

import (
	"context"
	"testing"

	"github.com/pkg/errors"

	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/poweraware/capper"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/poweraware/evictor"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/poweraware/reader"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/poweraware/spec"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	metricspool "github.com/kubewharf/katalyst-core/pkg/metrics/metrics-pool"
)

type dummySpecFetcher struct {
	spec.SpecFetcher
	called bool
}

type dummyPowerReader struct {
	reader.PowerReader
	calledInit    bool
	calledGet     bool
	calledCleanup bool
}

func (dsr *dummyPowerReader) Init() error {
	dsr.calledInit = true
	return nil
}

func (dsr *dummyPowerReader) Get(ctx context.Context) (int, error) {
	dsr.calledGet = true
	return 100, nil
}

func (dsr *dummyPowerReader) Cleanup() {
	dsr.calledCleanup = true
}

type dummyPowerReconciler struct {
	PowerReconciler
	called bool
}

type dummyPodEvictor struct {
	evictor.PodEvictor
	calledReset bool
	calledInit  bool
}

func (d *dummyPodEvictor) Reset(ctx context.Context) {
	d.calledReset = true
}

func (d *dummyPodEvictor) Init() error {
	d.calledInit = true
	return nil
}

type dummyPowerCapper struct {
	capper.PowerCapper
	resetCalled bool
	initCalled  bool
	capCalled   bool
}

func (d *dummyPowerCapper) Reset() {
	d.resetCalled = true
}

func (d *dummyPowerCapper) Init() error {
	d.initCalled = true
	return nil
}

func (d *dummyPowerCapper) Cap(ctx context.Context, targetWatts, currWatt int) {
	d.capCalled = true
}

func (re *dummyPowerReconciler) Reconcile(ctx context.Context, desired *spec.PowerSpec, actual int) (bool, error) {
	re.called = true
	return false, nil
}

type ctxKey string

const ctxKeyTest ctxKey = "test"

func (d *dummySpecFetcher) GetPowerSpec(ctx context.Context) (*spec.PowerSpec, error) {
	d.called = true
	switch ctx.Value(ctxKeyTest) {
	case "run_normal":
		return &spec.PowerSpec{
			Alert: spec.PowerAlertP1,
		}, nil
	case "run_abort_on_spec_fetcher_error":
		return nil, errors.New("dummy spec fetcher error")
	case "run_return_on_None_alert":
		return &spec.PowerSpec{
			Alert: spec.PowerAlertOK,
		}, nil
	case "run_return_on_Pause_op":
		return &spec.PowerSpec{
			Alert:      spec.PowerAlertS0,
			InternalOp: spec.InternalOpNoop,
		}, nil
	default:
		return nil, errors.New("unknown test")
	}
}

func Test_powerAwareAdvisor_run_normal(t *testing.T) {
	t.Parallel()
	// make sure advisor run gets power spec, gets actual power status, and takes action in order

	dummyEmitter := metricspool.DummyMetricsEmitterPool{}.GetDefaultMetricsEmitter().WithTags("advisor-poweraware")
	depSpecFetcher := dummySpecFetcher{}
	depPowerReader := &dummyPowerReader{}
	depPowerCapper := &dummyPowerReconciler{}

	advisor := powerAwareAdvisor{
		emitter:     dummyEmitter,
		specFetcher: &depSpecFetcher,
		powerReader: depPowerReader,
		reconciler:  depPowerCapper,
	}

	ctx := context.TODO()
	ctx = context.WithValue(ctx, ctxKeyTest, "run_normal")
	advisor.run(ctx)

	if !depSpecFetcher.called {
		t.Errorf("expected spec fetcher been calledGet, got %v", depSpecFetcher.called)
	}

	if !depPowerReader.calledGet {
		t.Errorf("expected power reader been calledGet, got %v", depPowerReader.calledGet)
	}

	if !depPowerCapper.called {
		t.Errorf("expected power reconciler been calledGet, got %v", depPowerReader.calledGet)
	}
}

func Test_powerAwareAdvisor_run_abort_on_spec_fetcher_error(t *testing.T) {
	t.Parallel()
	// make sure advisor run gets power spec, aborts on error

	depSpecFetcher := dummySpecFetcher{}
	depPowerReader := &dummyPowerReader{}

	advisor := powerAwareAdvisor{
		emitter:     &metrics.DummyMetrics{},
		specFetcher: &depSpecFetcher,
		powerReader: depPowerReader,
	}

	ctx := context.TODO()
	ctx = context.WithValue(ctx, ctxKeyTest, "run_abort_on_spec_fetcher_error")
	advisor.run(ctx)

	if !depSpecFetcher.called {
		t.Errorf("expected spec fetcher been calledGet, got %v", depSpecFetcher.called)
	}

	if depPowerReader.calledGet {
		t.Errorf("expected power reader not calledGet, got %v", depPowerReader.calledGet)
	}
}

func Test_powerAwareAdvisor_run_return_on_None_alert(t *testing.T) {
	t.Parallel()
	depSpecFetcher := dummySpecFetcher{}
	depPowerReader := &dummyPowerReader{}
	depInitResetter := &dummyPowerCapper{}

	advisor := powerAwareAdvisor{
		specFetcher: &depSpecFetcher,
		powerReader: depPowerReader,
		powerCapper: depInitResetter,
	}

	ctx := context.TODO()
	ctx = context.WithValue(ctx, ctxKeyTest, "run_return_on_None_alert")
	advisor.run(ctx)

	if !depSpecFetcher.called {
		t.Errorf("expected spec fetcher been calledGet, got %v", depSpecFetcher.called)
	}

	if depPowerReader.calledGet {
		t.Errorf("expected power reader not calledGet, got %v", depPowerReader.calledGet)
	}

	if depInitResetter.resetCalled {
		t.Errorf("unexpected power capper to reset")
	}
}

func Test_powerAwareAdvisor_run_return_on_Pause_op(t *testing.T) {
	t.Parallel()
	depSpecFetcher := dummySpecFetcher{}
	depPowerReader := &dummyPowerReader{}
	depInitResetter := &dummyPowerCapper{}

	advisor := powerAwareAdvisor{
		specFetcher: &depSpecFetcher,
		powerReader: depPowerReader,
		powerCapper: depInitResetter,
	}

	ctx := context.TODO()
	ctx = context.WithValue(ctx, ctxKeyTest, "run_return_on_Pause_op")
	advisor.run(ctx)

	if !depSpecFetcher.called {
		t.Errorf("expected spec fetcher been calledGet, got %v", depSpecFetcher.called)
	}

	if depPowerReader.calledGet {
		t.Errorf("expected power reader not calledGet, got %v", depPowerReader.calledGet)
	}

	if depInitResetter.resetCalled {
		t.Errorf("expected power capper not to reset, got %v", depInitResetter.resetCalled)
	}
}

func Test_powerAwareAdvisor_Run_does_Init_Cleanup(t *testing.T) {
	t.Parallel()

	depPowerReader := &dummyPowerReader{}
	depPodEvictor := &dummyPodEvictor{}

	advisor := powerAwareAdvisor{
		emitter:     &metrics.DummyMetrics{},
		powerReader: depPowerReader,
		podEvictor:  depPodEvictor,
	}

	err := advisor.Init()

	if err == nil || err.Error() != "no power capping server is provided" {
		t.Errorf("expected error 'no power capping server is provided', got %v", err)
	}
	if !depPowerReader.calledInit {
		t.Errorf("expected power reader init called called; got %v", depPowerReader.calledInit)
	}
	if !depPodEvictor.calledInit {
		t.Errorf("expected power evict init called called; got %v", depPodEvictor.calledInit)
	}
}

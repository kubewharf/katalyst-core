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

package component

import (
	"context"
	"testing"

	"github.com/pkg/errors"

	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/poweraware/component/reader"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	metricspool "github.com/kubewharf/katalyst-core/pkg/metrics/metrics-pool"
	"github.com/kubewharf/katalyst-core/pkg/util/external/power"
)

type dummySpecFetcher struct {
	SpecFetcher
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

type dummyPowerLimitResetter struct {
	power.InitResetter
	resetCalled bool
	initCalled  bool
}

func (ca *dummyPowerLimitResetter) Reset() {
	ca.resetCalled = true
}

func (dsr *dummyPowerLimitResetter) Init() error {
	dsr.initCalled = true
	return nil
}

func (re *dummyPowerReconciler) Reconcile(ctx context.Context, desired *types.PowerSpec, actual int) {
	re.called = true
}

type ctxKey string

const ctxKeyTest ctxKey = "test"

func (d *dummySpecFetcher) GetPowerSpec(ctx context.Context) (*types.PowerSpec, error) {
	d.called = true
	switch ctx.Value(ctxKeyTest) {
	case "run_normal":
		return &types.PowerSpec{
			Alert: types.PowerAlertF1,
		}, nil
	case "run_abort_on_spec_fetcher_error":
		return nil, errors.New("dummy spec fetcher error")
	case "run_return_on_None_alert":
		return &types.PowerSpec{
			Alert: types.PowerAlertOK,
		}, nil
	case "run_return_on_Pause_op":
		return &types.PowerSpec{
			Alert:      types.PowerAlertS0,
			InternalOp: types.InternalOpPause,
		}, nil
	default:
		return nil, errors.New("unknown test")
	}
}

func Test_powerAwareController_run_normal(t *testing.T) {
	t.Parallel()
	// make sure controller run gets power spec, gets actual power status, and takes action in order

	dummyEmitter := metricspool.DummyMetricsEmitterPool{}.GetDefaultMetricsEmitter().WithTags("advisor-poweraware")
	depSpecFetcher := dummySpecFetcher{}
	depPowerReader := &dummyPowerReader{}
	depPowerReconciler := &dummyPowerReconciler{}

	controller := powerAwareController{
		emitter:     dummyEmitter,
		specFetcher: &depSpecFetcher,
		powerReader: depPowerReader,
		reconciler:  depPowerReconciler,
	}

	ctx := context.TODO()
	ctx = context.WithValue(ctx, ctxKeyTest, "run_normal")
	controller.run(ctx)

	if !depSpecFetcher.called {
		t.Errorf("expected spec fetcher been calledGet, got %v", depSpecFetcher.called)
	}

	if !depPowerReader.calledGet {
		t.Errorf("expected power reader been calledGet, got %v", depPowerReader.calledGet)
	}

	if !depPowerReconciler.called {
		t.Errorf("expected power reconciler been calledGet, got %v", depPowerReader.calledGet)
	}
}

func Test_powerAwareController_run_abort_on_spec_fetcher_error(t *testing.T) {
	t.Parallel()
	// make sure controller run gets power spec, aborts on error

	depSpecFetcher := dummySpecFetcher{}
	depPowerReader := &dummyPowerReader{}

	controller := powerAwareController{
		specFetcher: &depSpecFetcher,
		powerReader: depPowerReader,
	}

	ctx := context.TODO()
	ctx = context.WithValue(ctx, ctxKeyTest, "run_abort_on_spec_fetcher_error")
	controller.run(ctx)

	if !depSpecFetcher.called {
		t.Errorf("expected spec fetcher been calledGet, got %v", depSpecFetcher.called)
	}

	if depPowerReader.calledGet {
		t.Errorf("expected power reader not calledGet, got %v", depPowerReader.calledGet)
	}
}

func Test_powerAwareController_run_return_on_None_alert(t *testing.T) {
	t.Parallel()
	depSpecFetcher := dummySpecFetcher{}
	depPowerReader := &dummyPowerReader{}
	depInitResetter := &dummyPowerLimitResetter{}

	controller := powerAwareController{
		specFetcher:            &depSpecFetcher,
		powerReader:            depPowerReader,
		powerLimitInitResetter: depInitResetter,
	}

	ctx := context.TODO()
	ctx = context.WithValue(ctx, ctxKeyTest, "run_return_on_None_alert")
	controller.run(ctx)

	if !depSpecFetcher.called {
		t.Errorf("expected spec fetcher been calledGet, got %v", depSpecFetcher.called)
	}

	if depPowerReader.calledGet {
		t.Errorf("expected power reader not calledGet, got %v", depPowerReader.calledGet)
	}

	if !depInitResetter.resetCalled {
		t.Errorf("expected power capper to reset, got %v", depInitResetter.resetCalled)
	}
}

func Test_powerAwareController_run_return_on_Pause_op(t *testing.T) {
	t.Parallel()
	depSpecFetcher := dummySpecFetcher{}
	depPowerReader := &dummyPowerReader{}
	depInitResetter := &dummyPowerLimitResetter{}

	controller := powerAwareController{
		specFetcher:            &depSpecFetcher,
		powerReader:            depPowerReader,
		powerLimitInitResetter: depInitResetter,
	}

	ctx := context.TODO()
	ctx = context.WithValue(ctx, ctxKeyTest, "run_return_on_Pause_op")
	controller.run(ctx)

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

type dummyErrorPowerReader struct {
	reader.PowerReader
	calledCleanup bool
}

func (dsr *dummyErrorPowerReader) Init() error {
	return errors.New("test dummy error")
}

func Test_powerAwareController_Run_does_Init_Cleanup(t *testing.T) {
	t.Parallel()

	depSpecFetcher := dummySpecFetcher{}
	depPowerReader := &dummyPowerReader{}
	depInitResetter := &dummyPowerLimitResetter{}

	controller := powerAwareController{
		specFetcher:            &depSpecFetcher,
		powerReader:            depPowerReader,
		powerLimitInitResetter: depInitResetter,
	}

	ctx := context.Background()
	ctx, cancel := context.WithCancel(ctx)

	// call cancel before Run to ensure the loop body is bypassed in test
	cancel()
	controller.Run(ctx)

	if !depPowerReader.calledInit {
		t.Errorf("expected power reader init called called; got %v", depPowerReader.calledInit)
	}
	if !depInitResetter.initCalled {
		t.Errorf("expected power capper init called; got %v", depInitResetter.initCalled)
	}
	if !depPowerReader.calledCleanup {
		t.Errorf("expected power reader cleanup called; got %v", depPowerReader.calledCleanup)
	}
	if !depInitResetter.resetCalled {
		t.Errorf("expected reset called; got %v", depInitResetter.resetCalled)
	}
}

func Test_powerAwareController_Run_Exit_on_Init_error(t *testing.T) {
	t.Parallel()

	depPowerReader := &dummyErrorPowerReader{}

	controller := powerAwareController{
		powerReader: depPowerReader,
	}

	controller.Run(context.TODO())

	if depPowerReader.calledCleanup {
		t.Errorf("expected power reader cleanup not called; got %v", depPowerReader.calledCleanup)
	}
}

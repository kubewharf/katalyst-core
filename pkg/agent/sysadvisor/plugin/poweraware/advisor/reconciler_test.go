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
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/poweraware/advisor/action"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/poweraware/advisor/action/strategy"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/poweraware/capper"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/poweraware/evictor"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/poweraware/spec"
	metricspool "github.com/kubewharf/katalyst-core/pkg/metrics/metrics-pool"
)

type dummyStrategy struct {
	strategy.PowerActionStrategy
}

func (p dummyStrategy) RecommendAction(_, _ int, _ spec.PowerAlert, op spec.InternalOp, _ time.Duration,
) action.PowerAction {
	opPlan := spec.InternalOpFreqCap
	if op != spec.InternalOpAuto {
		opPlan = op
	}
	return action.PowerAction{
		Op:  opPlan,
		Arg: 127,
	}
}

type dummyPercentageEvictor struct {
	called bool
}

func (d *dummyPercentageEvictor) Evict(ctx context.Context, targetPercent int) {
	d.called = true
}

func Test_powerReconciler_Engages_Capper_Evictor(t *testing.T) {
	t.Parallel()

	type fields struct {
		evictor *dummyPercentageEvictor
		capper  *dummyPowerCapper
	}
	type args struct {
		desired *spec.PowerSpec
		actual  int
	}
	tests := []struct {
		name              string
		fields            fields
		args              args
		wantCapperCalled  bool
		wantEvictorCalled bool
		wantFreqCapped    bool
	}{
		{
			name: "internal op freqCap should involve capper only",
			fields: fields{
				evictor: &dummyPercentageEvictor{},
				capper:  &dummyPowerCapper{},
			},
			args: args{
				desired: &spec.PowerSpec{
					Alert:      spec.PowerAlertP0,
					Budget:     127,
					InternalOp: spec.InternalOpFreqCap,
					AlertTime:  time.Time{},
				},
				actual: 100,
			},
			wantCapperCalled:  true,
			wantEvictorCalled: false,
			wantFreqCapped:    true,
		},
		{
			name: "internal op Evict should involve evictor only",
			fields: fields{
				evictor: &dummyPercentageEvictor{},
				capper:  &dummyPowerCapper{},
			},
			args: args{
				desired: &spec.PowerSpec{
					Alert:      spec.PowerAlertP0,
					Budget:     127,
					InternalOp: spec.InternalOpEvict,
					AlertTime:  time.Time{},
				},
				actual: 100,
			},
			wantCapperCalled:  false,
			wantEvictorCalled: true,
			wantFreqCapped:    false,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			p := &powerReconciler{
				evictor:  tt.fields.evictor,
				capper:   tt.fields.capper,
				strategy: &dummyStrategy{},
				emitter:  metricspool.DummyMetricsEmitterPool{}.GetDefaultMetricsEmitter().WithTags("test"),
			}

			freqCapped, err := p.Reconcile(context.TODO(), tt.args.desired, tt.args.actual)
			assert.NoError(t, err)
			assert.Equalf(t, tt.wantFreqCapped, freqCapped, "unexpected freqCapped returned")
			assert.Equalf(t, tt.wantCapperCalled, tt.fields.capper.capCalled, "unexpected behavior calling capper")
			assert.Equalf(t, tt.wantEvictorCalled, tt.fields.evictor.called, "unexpected behavior calling evictor")
		})
	}
}

func Test_powerReconciler_Reconcile_DryRun(t *testing.T) {
	t.Parallel()

	mockStrategy := &dummyStrategy{}
	dummyEmitter := metricspool.DummyMetricsEmitterPool{}.GetDefaultMetricsEmitter().WithTags("advisor-poweraware")

	type fields struct {
		dryRun      bool
		priorAction action.PowerAction
		evictor     evictor.PercentageEvictor
		capper      capper.PowerCapper
		strategy    strategy.PowerActionStrategy
	}
	type args struct {
		ctx     context.Context
		desired *spec.PowerSpec
		actual  int
	}
	tests := []struct {
		name   string
		fields fields
		args   args
	}{
		{
			name: "happy path dry run prints action",
			fields: fields{
				dryRun: true,
				priorAction: action.PowerAction{
					Op:  spec.InternalOpEvict,
					Arg: 127,
				},
				evictor:  nil,
				capper:   nil,
				strategy: mockStrategy,
			},
			args: args{
				ctx: context.TODO(),
				desired: &spec.PowerSpec{
					Alert:      spec.PowerAlertP0,
					Budget:     127,
					InternalOp: spec.InternalOpAuto,
					AlertTime:  time.Time{},
				},
				actual: 135,
			},
		},
		{
			name: "happy path dry run suppress duplicate logs",
			fields: fields{
				dryRun: true,
				priorAction: action.PowerAction{
					Op:  spec.InternalOpFreqCap,
					Arg: 127,
				},
				evictor:  nil,
				capper:   nil,
				strategy: mockStrategy,
			},
			args: args{
				ctx: context.TODO(),
				desired: &spec.PowerSpec{
					Alert:      spec.PowerAlertP0,
					Budget:     127,
					InternalOp: spec.InternalOpAuto,
					AlertTime:  time.Time{},
				},
				actual: 135,
			},
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			p := &powerReconciler{
				dryRun:      tt.fields.dryRun,
				priorAction: tt.fields.priorAction,
				evictor:     tt.fields.evictor,
				capper:      tt.fields.capper,
				strategy:    tt.fields.strategy,
				emitter:     dummyEmitter,
			}
			_, err := p.Reconcile(tt.args.ctx, tt.args.desired, tt.args.actual)
			assert.NoError(t, err)
		})
	}
}

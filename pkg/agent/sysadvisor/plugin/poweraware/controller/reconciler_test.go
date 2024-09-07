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

package controller

import (
	"context"
	"testing"
	"time"

	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/poweraware/capper"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/poweraware/controller/action"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/poweraware/controller/action/strategy"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/poweraware/evictor"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/poweraware/spec"
)

type dummyStrategy struct {
	strategy.PowerActionStrategy
}

func (p dummyStrategy) RecommendAction(_, _ int, _ spec.PowerAlert, _ spec.InternalOp, _ time.Duration,
) action.PowerAction {
	return action.PowerAction{
		Op:  spec.InternalOpFreqCap,
		Arg: 127,
	}
}

func Test_powerReconciler_Reconcile_DryRun(t *testing.T) {
	t.Parallel()

	mockStrategy := &dummyStrategy{}

	type fields struct {
		dryRun      bool
		priorAction action.PowerAction
		evictor     evictor.LoadEvictor
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
			}
			p.Reconcile(tt.args.ctx, tt.args.desired, tt.args.actual)
		})
	}
}

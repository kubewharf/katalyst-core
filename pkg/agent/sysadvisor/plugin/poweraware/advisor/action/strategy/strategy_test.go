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

package strategy

import (
	"math"
	"reflect"
	"testing"
	"time"

	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/poweraware/advisor/action"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/poweraware/spec"
)

func Test_linearDecay_calcExcessiveInPercent(t *testing.T) {
	t.Parallel()
	type args struct {
		target int
		curr   int
		ttl    time.Duration
	}
	tests := []struct {
		name string
		args args
		want int
	}{
		{
			name: "timely gets off all extra",
			args: args{
				target: 100,
				curr:   120,
				ttl:    time.Minute * 2,
			},
			want: 17,
		},
		{
			name: "having some time gets some fractional",
			args: args{
				target: 100,
				curr:   120,
				ttl:    time.Minute * 20,
			},
			want: 9,
		},
		{
			name: "having more time unloads less",
			args: args{
				target: 100,
				curr:   120,
				ttl:    time.Minute * 30,
			},
			want: 6,
		},
		{
			name: "having quite a lot of time unloads little",
			args: args{
				target: 100,
				curr:   120,
				ttl:    time.Minute * 60,
			},
			want: 2,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			d := exponentialDecay{b: math.E / 2}
			if got := d.calcExcessiveInPercent(tt.args.target, tt.args.curr, tt.args.ttl); got != tt.want {
				t.Errorf("calcExcessiveInPercent() = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_ruleBasedPowerStrategy_RecommendAction(t *testing.T) {
	t.Parallel()
	type fields struct {
		coefficient exponentialDecay
	}
	type args struct {
		actualWatt  int
		desiredWatt int
		alert       spec.PowerAlert
		internalOp  spec.InternalOp
		ttl         time.Duration
	}
	tests := []struct {
		name   string
		fields fields
		args   args
		want   action.PowerAction
	}{
		{
			name: "approaching deadline leads to freq capping",
			fields: fields{
				coefficient: exponentialDecay{},
			},
			args: args{
				actualWatt:  99,
				desiredWatt: 88,
				alert:       spec.PowerAlertP1,
				internalOp:  spec.InternalOpThrottle,
				ttl:         time.Second * 30,
			},
			want: action.PowerAction{
				Op:  spec.InternalOpFreqCap,
				Arg: 88,
			},
		},
		{
			name:   "having a lot of time usually leads to evict a very little portion",
			fields: fields{coefficient: exponentialDecay{b: math.E / 2}},
			args: args{
				actualWatt:  99,
				desiredWatt: 88,
				alert:       spec.PowerAlertP1,
				internalOp:  spec.InternalOpAuto,
				ttl:         time.Minute * 60,
			},
			want: action.PowerAction{
				Op:  spec.InternalOpEvict,
				Arg: 1,
			},
		},
		{
			name:   "actual not more than desired, so no op",
			fields: fields{coefficient: exponentialDecay{}},
			args: args{
				actualWatt:  88,
				desiredWatt: 88,
				alert:       spec.PowerAlertP1,
				internalOp:  spec.InternalOpAuto,
				ttl:         time.Second * 60,
			},
			want: action.PowerAction{
				Op:  spec.InternalOpNoop,
				Arg: 0,
			},
		},
		{
			name:   "stale request, no op too",
			fields: fields{coefficient: exponentialDecay{}},
			args: args{
				actualWatt:  100,
				desiredWatt: 88,
				alert:       spec.PowerAlertP1,
				internalOp:  spec.InternalOpAuto,
				ttl:         -time.Minute * 5,
			},
			want: action.PowerAction{
				Op:  spec.InternalOpNoop,
				Arg: 0,
			},
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			p := ruleBasedPowerStrategy{
				coefficient: tt.fields.coefficient,
			}
			if got := p.RecommendAction(tt.args.actualWatt, tt.args.desiredWatt, tt.args.alert, tt.args.internalOp, tt.args.ttl); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("RecommendAction() = %v, want %v", got, tt.want)
			}
		})
	}
}

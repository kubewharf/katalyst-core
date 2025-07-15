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
	"reflect"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/poweraware/advisor/action"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/poweraware/advisor/action/strategy/assess"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/poweraware/spec"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
)

type mockEvicableProber struct {
	mock.Mock
}

func (m *mockEvicableProber) HasEvictablePods() bool {
	args := m.Called()
	return args.Bool(0)
}

func Test_evictFirstStrategy_RecommendAction(t *testing.T) {
	t.Parallel()

	mockPorberTrue := new(mockEvicableProber)
	mockPorberTrue.On("HasEvictablePods").Return(true)

	mockPorberFalse := new(mockEvicableProber)
	mockPorberFalse.On("HasEvictablePods").Return(false)

	type fields struct {
		coefficient     exponentialDecay
		evictableProber EvictableProber
		dvfsUsed        int
		effectCurrent   bool
		prevPower       int
		inDVFS          bool
	}
	type args struct {
		actualWatt  int
		desiredWatt int
		alert       spec.PowerAlert
		internalOp  spec.InternalOp
		ttl         time.Duration
	}
	tests := []struct {
		name       string
		fields     fields
		args       args
		want       action.PowerAction
		wantInDVFS bool
	}{
		{
			name: "plan of s0 always targets full range",
			fields: fields{
				coefficient:     exponentialDecay{},
				evictableProber: nil,
				dvfsUsed:        0,
			},
			args: args{
				alert:       spec.PowerAlertS0,
				actualWatt:  100,
				desiredWatt: 80,
			},
			want: action.PowerAction{
				Op:  spec.InternalOpFreqCap,
				Arg: 80,
			},
			wantInDVFS: true,
		},
		{
			name: "plan of p0 is constraint when allowing dvfs only",
			fields: fields{
				dvfsUsed:      0,
				effectCurrent: true,
			},
			args: args{
				alert:       spec.PowerAlertP0,
				actualWatt:  100,
				desiredWatt: 80,
				internalOp:  spec.InternalOpFreqCap,
			},
			want: action.PowerAction{
				Op:  spec.InternalOpFreqCap,
				Arg: 90,
			},
			wantInDVFS: true,
		},
		{
			name:   "p1 is noop",
			fields: fields{},
			args: args{
				actualWatt:  100,
				desiredWatt: 80,
				alert:       spec.PowerAlertP1,
			},
			want: action.PowerAction{
				Op:  spec.InternalOpNoop,
				Arg: 0,
			},
		},
		{
			name:   "p2 is noop",
			fields: fields{},
			args: args{
				actualWatt:  100,
				desiredWatt: 80,
				alert:       spec.PowerAlertP2,
			},
			want: action.PowerAction{
				Op:  spec.InternalOpNoop,
				Arg: 0,
			},
		},
		{
			name: "evict first if possible",
			fields: fields{
				coefficient:     exponentialDecay{b: defaultDecayB},
				evictableProber: mockPorberTrue,
				dvfsUsed:        0,
			},
			args: args{
				actualWatt:  100,
				desiredWatt: 80,
				alert:       spec.PowerAlertP0,
				ttl:         time.Minute * 15,
			},
			want: action.PowerAction{
				Op:  spec.InternalOpEvict,
				Arg: 14,
			},
		},
		{
			name: "if no evictable and there is room, go for dvfs",
			fields: fields{
				evictableProber: mockPorberFalse,
				dvfsUsed:        0,
				effectCurrent:   true,
			},
			args: args{
				actualWatt:  100,
				desiredWatt: 95,
				alert:       spec.PowerAlertP0,
			},
			want: action.PowerAction{
				Op:  spec.InternalOpFreqCap,
				Arg: 95,
			},
			wantInDVFS: true,
		},
		{
			name: "if no evictable and there is partial room, go for constrint dvfs",
			fields: fields{
				evictableProber: mockPorberFalse,
				dvfsUsed:        8,
				effectCurrent:   true,
			},
			args: args{
				actualWatt:  100,
				desiredWatt: 95,
				alert:       spec.PowerAlertP0,
			},
			want: action.PowerAction{
				Op:  spec.InternalOpFreqCap,
				Arg: 98,
			},
			wantInDVFS: true,
		},
		{
			name: "if no evictable and there is no room, noop",
			fields: fields{
				evictableProber: mockPorberFalse,
				dvfsUsed:        10,
				inDVFS:          true,
			},
			args: args{
				actualWatt:  100,
				desiredWatt: 95,
				alert:       spec.PowerAlertP0,
			},
			want: action.PowerAction{
				Op:  spec.InternalOpNoop,
				Arg: 0,
			},
			wantInDVFS: false,
		},
		{
			name: "being previously not current effect should be refreshed by current anyway",
			fields: fields{
				evictableProber: mockPorberFalse,
				dvfsUsed:        8,
				effectCurrent:   false,
				prevPower:       100,
				inDVFS:          true,
			},
			args: args{
				actualWatt:  100,
				desiredWatt: 80,
				alert:       spec.PowerAlertP0,
			},
			want: action.PowerAction{
				Op:  spec.InternalOpFreqCap,
				Arg: 98,
			},
			wantInDVFS: true,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			e := &evictFirstStrategy{
				emitter:         &metrics.DummyMetrics{},
				coefficient:     tt.fields.coefficient,
				evictableProber: tt.fields.evictableProber,
				dvfsTracker: dvfsTracker{
					dvfsAccumEffect: tt.fields.dvfsUsed,
					isEffectCurrent: tt.fields.effectCurrent,
					assessor:        assess.NewPowerChangeAssessor(tt.fields.dvfsUsed, 0),
				},
				metricsReader: nil,
			}
			if got := e.RecommendAction(tt.args.actualWatt, tt.args.desiredWatt, tt.args.alert, tt.args.internalOp, tt.args.ttl); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("RecommendAction() = %v, want %v", got, tt.want)
			}
			assert.Equal(t, tt.wantInDVFS, e.dvfsTracker.inDVFS)
			if tt.fields.evictableProber != nil {
				mock.AssertExpectationsForObjects(t, tt.fields.evictableProber)
			}
		})
	}
}

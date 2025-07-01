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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/poweraware/advisor/action/strategy/assess"
)

type mockCapperProber struct {
	mock.Mock
}

func (m *mockCapperProber) IsCapperReady() bool {
	args := m.Called()
	return args.Bool(0)
}

func Test_dvfsTracker_update(t *testing.T) {
	t.Parallel()

	mockProber := new(mockCapperProber)
	mockProber.On("IsCapperReady").Return(true)

	type fields struct {
		dvfsUsed  int
		indvfs    bool
		prevPower int
	}
	type args struct {
		actualWatt  int
		desiredWatt int
	}
	tests := []struct {
		name            string
		fields          fields
		args            args
		wantDVFSTracker dvfsTracker
	}{
		{
			name: "not in dvfs, not to accumulate, and effect is refreshed anyway",
			fields: fields{
				dvfsUsed:  3,
				indvfs:    false,
				prevPower: 100,
			},
			args: args{
				actualWatt:  90,
				desiredWatt: 85,
			},
			wantDVFSTracker: dvfsTracker{
				capperProber:    mockProber,
				dvfsAccumEffect: 3,
				isEffectCurrent: true,
				inDVFS:          false,
				assessor:        assess.NewPowerChangeAssessor(3, 90),
			},
		},
		{
			name: "in dvfs, accumulate if lower power is observed",
			fields: fields{
				dvfsUsed:  3,
				indvfs:    true,
				prevPower: 100,
			},
			args: args{
				actualWatt:  90,
				desiredWatt: 85,
			},
			wantDVFSTracker: dvfsTracker{
				capperProber:    mockProber,
				dvfsAccumEffect: 13,
				isEffectCurrent: true,
				inDVFS:          true,
				assessor:        assess.NewPowerChangeAssessor(13, 90),
			},
		},
		{
			name: "in dvfs, not to accumulate if higher power is observed",
			fields: fields{
				dvfsUsed:  3,
				indvfs:    true,
				prevPower: 100,
			},
			args: args{
				actualWatt:  101,
				desiredWatt: 85,
			},
			wantDVFSTracker: dvfsTracker{
				capperProber:    mockProber,
				dvfsAccumEffect: 3,
				isEffectCurrent: true,
				inDVFS:          true,
				assessor:        assess.NewPowerChangeAssessor(3, 101),
			},
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			d := &dvfsTracker{
				capperProber:    mockProber,
				dvfsAccumEffect: tt.fields.dvfsUsed,
				inDVFS:          tt.fields.indvfs,
				assessor:        assess.NewPowerChangeAssessor(3, tt.fields.prevPower),
			}
			d.update(tt.args.actualWatt)
			assert.Equal(t, &tt.wantDVFSTracker, d)
		})
	}
}

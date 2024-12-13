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
)

func Test_dvfsTracker_update(t *testing.T) {
	t.Parallel()
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
			name: "not in dvfs, not to accumulate",
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
				dvfsAccumEffect: 3,
				inDVFS:          false,
				prevPower:       90,
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
				dvfsAccumEffect: 13,
				inDVFS:          true,
				prevPower:       90,
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
				dvfsAccumEffect: 3,
				inDVFS:          true,
				prevPower:       101,
			},
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			d := &dvfsTracker{
				dvfsAccumEffect: tt.fields.dvfsUsed,
				inDVFS:          tt.fields.indvfs,
				prevPower:       tt.fields.prevPower,
			}
			d.update(tt.args.actualWatt, tt.args.desiredWatt)
			assert.Equal(t, &tt.wantDVFSTracker, d)
		})
	}
}

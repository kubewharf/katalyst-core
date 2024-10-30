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

package resctrl

import (
	"reflect"
	"testing"
	"time"

	"github.com/spf13/afero"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/resctrl/state"
)

func Test_getMB(t *testing.T) {
	t.Parallel()

	fs := afero.NewMemMapFs()
	_ = afero.WriteFile(fs, "/foo/mon_data/mon_L3_04/mbm_total_bytes", []byte("2000000000"), 0644)

	dateKeeper, _ := state.NewMBRawDataKeeper()
	dateKeeper.Set("/foo/mon_data/mon_L3_04/mbm_total_bytes",
		1_000_000_000,
		time.Date(2024, 9, 18, 19, 57, 45, 0, time.UTC),
	)

	type args struct {
		fs         afero.Fs
		monGroup   string
		ccd        int
		ts         time.Time
		dataKeeper state.MBRawDataKeeper
	}
	tests := []struct {
		name string
		args args
		want int
	}{
		{
			name: "happy path",
			args: args{
				fs:         fs,
				monGroup:   "/foo",
				ccd:        4,
				ts:         time.Date(2024, 9, 18, 19, 57, 46, 0, time.UTC),
				dataKeeper: dateKeeper,
			},
			want: 953,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := calcMB(tt.args.fs, tt.args.monGroup, tt.args.ccd, tt.args.ts, tt.args.dataKeeper); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("getMB() = %v, want %v", got, tt.want)
			}
		})
	}
}

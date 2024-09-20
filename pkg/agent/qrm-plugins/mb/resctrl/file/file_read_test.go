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

package file

import (
	"reflect"
	"testing"

	"github.com/spf13/afero"
)

func Test_readRawData(t *testing.T) {
	t.Parallel()

	fs := afero.NewMemMapFs()
	_ = afero.WriteFile(fs, "/sys/fs/resctrl/node_1/mon_data/mon_L3_02", []byte("1234567890123456789"), 0644)
	_ = afero.WriteFile(fs, "/sys/fs/resctrl/node_1/mon_data/mon_L3_03", []byte("Unavailable"), 0644)

	type args struct {
		fs   afero.Fs
		path string
	}
	tests := []struct {
		name string
		args args
		want int64
	}{
		{
			name: "happy path to get byte count",
			args: args{
				fs:   fs,
				path: "/sys/fs/resctrl/node_1/mon_data/mon_L3_02",
			},
			want: 1234567890123456789,
		},
		{
			name: "Unavailable should be ignored",
			args: args{
				fs:   fs,
				path: "/sys/fs/resctrl/node_1/mon_data/mon_L3_03",
			},
			want: -1,
		},
		{
			name: "file not exist should be ignore",
			args: args{
				fs:   fs,
				path: "/sys/fs/resctrl/node_100/mon_data/mon_L3_02",
			},
			want: -1,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := ReadValueFromFile(tt.args.fs, tt.args.path); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("ReadValueFromFile() = %v, want %v", got, tt.want)
			}
		})
	}
}

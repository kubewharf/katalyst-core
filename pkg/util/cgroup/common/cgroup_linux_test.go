//go:build linux
// +build linux

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

package common

import (
	"reflect"
	"testing"
)

func TestParseCgroupNumaValue(t *testing.T) {
	t.Parallel()

	type args struct {
		content string
	}
	tests := []struct {
		name    string
		args    args
		want    map[string]map[int]uint64
		wantErr bool
	}{
		{
			name: "cgroupv1 format",
			args: args{
				content: `total=7587426 N0=92184 N1=21339 N2=104047 N3=7374122
file=70686 N0=5353 N1=3096 N2=12817 N3=51844
anon=7516740 N0=86831 N1=18243 N2=91230 N3=7322278
unevictable=0 N0=0 N1=0 N2=0 N3=0`,
			},
			want: map[string]map[int]uint64{
				"total": {
					0: 92184,
					1: 21339,
					2: 104047,
					3: 7374122,
				},
				"file": {
					0: 5353,
					1: 3096,
					2: 12817,
					3: 51844,
				},
				"anon": {
					0: 86831,
					1: 18243,
					2: 91230,
					3: 7322278,
				},
				"unevictable": {
					0: 0,
					1: 0,
					2: 0,
					3: 0,
				},
			},
			wantErr: false,
		},
		{
			name: "cgroupv2 format",
			args: args{
				content: `anon N0=1629990912 N1=65225723904
file N0=1892352 N1=37441536
unevictable N0=0 N1=0`,
			},
			want: map[string]map[int]uint64{
				"anon": {
					0: 1629990912,
					1: 65225723904,
				},
				"file": {
					0: 1892352,
					1: 37441536,
				},
				"unevictable": {
					0: 0,
					1: 0,
				},
			},
			wantErr: false,
		},
		{
			name: "wrong separator",
			args: args{
				content: `anon N0:1629990912 N1:65225723904
file N0:1892352 N1:37441536
unevictable N0:0 N1:0`,
			},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := ParseCgroupNumaValue(tt.args.content)
			if (err != nil) != tt.wantErr {
				t.Errorf("ParseCgroupNumaValue() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("ParseCgroupNumaValue() = %v, want %v", got, tt.want)
				return
			}
		})
	}
}

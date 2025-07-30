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

package resource

import (
	"reflect"
	"testing"
)

func TestGetGroupedDomainSetting(t *testing.T) {
	t.Parallel()
	type args struct {
		domsGroupSetting map[int]GroupSettings
	}
	tests := []struct {
		name string
		args args
		want map[string][]int
	}{
		{
			name: "happy path",
			args: args{
				domsGroupSetting: map[int]GroupSettings{
					0: {
						"dedicated": 30_000,
						"shared-50": 20_000,
					},
					1: {
						"dedicated": 35_000,
						"shared-60": 25_000,
					},
				},
			},
			want: map[string][]int{
				"dedicated": {30_000, 35_000},
				"shared-60": {0, 25_000},
				"shared-50": {20_000, 0},
			},
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := GetGroupedDomainSetting(tt.args.domsGroupSetting); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("GetGroupedDomainSetting() = %v, want %v", got, tt.want)
			}
		})
	}
}

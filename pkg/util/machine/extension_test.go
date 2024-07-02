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

package machine

import (
	"testing"

	info "github.com/google/cadvisor/info/v1"
	"github.com/stretchr/testify/assert"
	"k8s.io/apimachinery/pkg/util/sets"
)

func TestKatalystMachineInfo_GetPackageMap(t *testing.T) {
	t.Parallel()
	type fields struct {
		MachineInfo       *info.MachineInfo
		CPUTopology       *CPUTopology
		MemoryTopology    *MemoryTopology
		DieTopology       *DieTopology
		ExtraCPUInfo      *ExtraCPUInfo
		ExtraNetworkInfo  *ExtraNetworkInfo
		ExtraTopologyInfo *ExtraTopologyInfo
	}
	type args struct{}
	tests := []struct {
		name   string
		fields fields
		args   args
		want   map[int][]int
	}{
		{
			name: "happy path of 2 packages 3 numa nodes each",
			fields: fields{
				DieTopology: &DieTopology{
					NumPackages: 2,
				},
				ExtraTopologyInfo: &ExtraTopologyInfo{
					NumaDistanceMap: nil,
					SiblingNumaInfo: &SiblingNumaInfo{
						SiblingNumaMap: map[int]sets.Int{
							// package 0, numa nodes 0,1,2
							0: {1: {}, 2: {}},
							1: {0: {}, 2: {}},
							2: {0: {}, 1: {}},
							// package 1, numa nodes 3,4,5
							3: {4: {}, 5: {}},
							4: {3: {}, 5: {}},
							5: {3: {}, 4: {}},
						},
					},
				},
			},
			args: args{},
			want: map[int][]int{
				0: {0, 1, 2},
				1: {3, 4, 5},
			},
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s := &KatalystMachineInfo{
				MachineInfo:       tt.fields.MachineInfo,
				CPUTopology:       tt.fields.CPUTopology,
				MemoryTopology:    tt.fields.MemoryTopology,
				DieTopology:       tt.fields.DieTopology,
				ExtraCPUInfo:      tt.fields.ExtraCPUInfo,
				ExtraNetworkInfo:  tt.fields.ExtraNetworkInfo,
				ExtraTopologyInfo: tt.fields.ExtraTopologyInfo,
			}
			assert.Equalf(t, tt.want, s.GetPackageMap(), "GetPackageMap()")
		})
	}
}

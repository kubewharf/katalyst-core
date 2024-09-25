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

package mbdomain

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"k8s.io/apimachinery/pkg/util/sets"

	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

func TestNewMBDomainManager(t *testing.T) {
	t.Parallel()

	type args struct {
		dieTopology *machine.DieTopology
	}
	tests := []struct {
		name string
		args args
		want *MBDomainManager
	}{
		{
			name: "happy path",
			args: args{
				dieTopology: &machine.DieTopology{
					Packages:       1,
					NUMAsInPackage: map[int][]int{0: {0, 1, 2, 3}},
					DiesInNuma: map[int]sets.Int{
						0: {0: sets.Empty{}, 1: sets.Empty{}},
						1: {2: sets.Empty{}, 3: sets.Empty{}},
						2: {4: sets.Empty{}, 5: sets.Empty{}},
						3: {6: sets.Empty{}, 7: sets.Empty{}},
					},
				},
			},
			want: &MBDomainManager{
				Domains: map[int]*MBDomain{
					0: {
						ID:        0,
						NumaNodes: []int{0, 1, 2, 3},
						CCDNode: map[int]int{
							0: 0, 1: 0, 2: 1, 3: 1, 4: 2, 5: 2, 6: 3, 7: 3,
						},
						NodeCCDs: map[int][]int{
							0: {0, 1},
							1: {2, 3},
							2: {4, 5},
							3: {6, 7},
						},
						CCDs:          []int{0, 1, 2, 3, 4, 5, 6, 7},
						PreemptyNodes: make(sets.Int),
					},
				},
			},
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := NewMBDomainManager(tt.args.dieTopology)
			assert.Equal(t, tt.want, got)
		})
	}
}

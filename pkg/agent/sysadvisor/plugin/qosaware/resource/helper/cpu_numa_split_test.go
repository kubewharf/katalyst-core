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

package helper

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

func TestSplitCPUByBindingNUMAs(t *testing.T) {
	t.Parallel()

	mkCI := func(assignments map[int]machine.CPUSet) *types.ContainerInfo {
		return &types.ContainerInfo{
			PodUID:                   "pod",
			ContainerName:            "container",
			TopologyAwareAssignments: assignments,
		}
	}

	tests := []struct {
		name       string
		estimation float64
		binding    machine.CPUSet
		ci         *types.ContainerInfo
		want       float64
		zeroAssign bool
	}{
		{
			name:       "no binding numas keeps estimation unchanged",
			estimation: 10,
			binding:    machine.NewCPUSet(),
			ci: mkCI(map[int]machine.CPUSet{
				0: machine.NewCPUSet(0, 1, 2, 3),
			}),
			want: 10,
		},
		{
			name:       "splits estimation by binding numa cpu share",
			estimation: 10,
			binding:    machine.NewCPUSet(0),
			ci: mkCI(map[int]machine.CPUSet{
				0: machine.NewCPUSet(0, 1, 2, 3),
				1: machine.NewCPUSet(4, 5, 6, 7),
			}),
			want: 5,
		},
		{
			name:       "binding numa absent from assignment contributes zero",
			estimation: 10,
			binding:    machine.NewCPUSet(0),
			ci: mkCI(map[int]machine.CPUSet{
				1: machine.NewCPUSet(4, 5, 6, 7),
			}),
			want:       0,
			zeroAssign: false,
		},
		{
			name:       "no cross-numa assignment reports zero assignment",
			estimation: 10,
			binding:    machine.NewCPUSet(0),
			ci:         mkCI(map[int]machine.CPUSet{}),
			want:       0,
			zeroAssign: true,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, zeroAssign := SplitCPUByBindingNUMAs(tc.estimation, tc.binding, tc.ci)
			assert.Equal(t, tc.want, got)
			assert.Equal(t, tc.zeroAssign, zeroAssign)
		})
	}
}

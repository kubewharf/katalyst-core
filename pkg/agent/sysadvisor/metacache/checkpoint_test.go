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

package metacache

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"k8s.io/apimachinery/pkg/util/sets"

	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

func TestCheckpoint(t *testing.T) {
	t.Parallel()

	cp := NewMetaCacheCheckpoint()
	cp.PoolEntries = map[string]*types.PoolInfo{
		"p1": {
			PoolName: "p1",
			TopologyAwareAssignments: map[int]machine.CPUSet{
				0: machine.MustParse("0"),
				1: machine.MustParse("24"),
			},
			OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
				0: machine.MustParse("0"),
				1: machine.MustParse("24"),
			},
			RegionNames: sets.NewString("r1"),
		},
	}
	cp.PodEntries = map[string]types.ContainerEntries{
		"pod1": {
			"c1": {
				PodUID:        "pod1",
				ContainerName: "c1",
				RegionNames:   sets.NewString("r1"),
			},
		},
	}
	cp.RegionEntries = map[string]*types.RegionInfo{
		"r1": {
			RegionType:                 types.QoSRegionTypeShare,
			BindingNumas:               machine.NewCPUSet(0, 1),
			HeadroomPolicyTopPriority:  "",
			HeadroomPolicyInUse:        "",
			Headroom:                   1.002,
			ControlKnobMap:             nil,
			ProvisionPolicyTopPriority: "",
			ProvisionPolicyInUse:       "",
		},
	}

	checkpoint, err := cp.MarshalCheckpoint()
	assert.NoError(t, err)

	cp = NewMetaCacheCheckpoint()
	err = cp.UnmarshalCheckpoint(checkpoint)
	assert.NoError(t, err)

	err = cp.VerifyChecksum()
	assert.NoError(t, err)
}

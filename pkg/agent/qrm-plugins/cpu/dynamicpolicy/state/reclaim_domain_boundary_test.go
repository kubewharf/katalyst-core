/*
Copyright 2026 The Katalyst Authors.

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

package state

import (
	"testing"

	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/util/sets"

	apiconsts "github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	cpuconsts "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/consts"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

func TestReclaimBoundaryBindingRequiresTopology(t *testing.T) {
	t.Parallel()

	ai := &AllocationInfo{
		AllocationMeta: commonstate.AllocationMeta{
			QoSLevel: apiconsts.PodAnnotationQoSLevelDedicatedCores,
			Annotations: map[string]string{
				apiconsts.PodAnnotationMemoryEnhancementNumaBinding: apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable,
			},
		},
		TopologyAwareAssignments: map[int]machine.CPUSet{0: machine.NewCPUSet(0)},
	}
	var err error
	require.NotPanics(t, func() {
		_, err = ai.RampUpReclaimDomains(nil)
	})
	require.ErrorContains(t, err, "topology")

	ai.Annotations = nil
	domains, err := ai.RampUpReclaimDomains(nil)
	require.NoError(t, err)
	require.Equal(t, sets.NewInt(commonstate.FakedNUMAID), domains)
}

func TestReclaimBoundaryDistributedHintRejectsPartialParse(t *testing.T) {
	t.Parallel()

	topology := &machine.CPUTopology{
		CPUDetails: machine.CPUDetails{
			0: {NUMANodeID: 0},
			1: {NUMANodeID: 1},
		},
	}
	for _, hint := range []string{"0,1-2-3", "0,2-1", "0,", "0-1", "0,1"} {
		hint := hint
		t.Run(hint, func(t *testing.T) {
			t.Parallel()
			ai := &AllocationInfo{
				AllocationMeta: commonstate.AllocationMeta{
					QoSLevel: apiconsts.PodAnnotationQoSLevelDedicatedCores,
					Annotations: map[string]string{
						apiconsts.PodAnnotationMemoryEnhancementNumaBinding: apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable,
						cpuconsts.CPUStateAnnotationKeyNUMAHint:             hint,
					},
				},
				TopologyAwareAssignments: map[int]machine.CPUSet{0: machine.NewCPUSet(0), 1: machine.NewCPUSet(1)},
			}
			domains, err := ai.RampUpReclaimDomains(topology)
			if hint == "0-1" || hint == "0,1" {
				require.NoError(t, err)
				require.Equal(t, sets.NewInt(0, 1), domains)
			} else {
				require.Error(t, err)
			}
		})
	}
}

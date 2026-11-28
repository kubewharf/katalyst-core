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

package dynamicpolicy

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

// newGlobalDomainRampUpAllocation builds a non-pool AllocationInfo for the
// global-domain exemption test.
func newGlobalDomainRampUpAllocation(podUID, containerName string, rampUp bool,
	qosLevel string, numaBinding bool, result machine.CPUSet,
) *state.AllocationInfo {
	annotations := map[string]string{
		consts.PodAnnotationQoSLevelKey: qosLevel,
	}
	if numaBinding {
		annotations[consts.PodAnnotationMemoryEnhancementNumaBinding] = consts.PodAnnotationMemoryEnhancementNumaBindingEnable
	}
	return &state.AllocationInfo{
		AllocationMeta: commonstate.AllocationMeta{
			PodUid:        podUID,
			PodNamespace:  "ns",
			PodName:       podUID,
			ContainerName: containerName,
			QoSLevel:      qosLevel,
			Annotations:   annotations,
		},
		RampUp:           rampUp,
		AllocationResult: result,
	}
}

func TestGlobalDomainActiveRampUpCPUSet(t *testing.T) {
	t.Parallel()

	shared := consts.PodAnnotationQoSLevelSharedCores
	dedicated := consts.PodAnnotationQoSLevelDedicatedCores

	t.Run("empty entries returns empty cpuset", func(t *testing.T) {
		t.Parallel()
		require.True(t, globalDomainActiveRampUpCPUSet(state.PodEntries{}).IsEmpty())
	})

	t.Run("pool entries are ignored", func(t *testing.T) {
		t.Parallel()
		entries := state.PodEntries{
			commonstate.PoolNameShare: state.ContainerEntries{
				commonstate.FakedContainerName: newGlobalDomainRampUpAllocation(
					"pool", commonstate.FakedContainerName, true, shared, false, machine.MustParse("0-3")),
			},
		}
		require.True(t, globalDomainActiveRampUpCPUSet(entries).IsEmpty())
	})

	t.Run("non-binding shared ramp-up cpus are included", func(t *testing.T) {
		t.Parallel()
		entries := state.PodEntries{
			"pod-1": state.ContainerEntries{
				"main": newGlobalDomainRampUpAllocation(
					"pod-1", "main", true, shared, false, machine.MustParse("0-3")),
			},
		}
		require.Equal(t, machine.MustParse("0-3"), globalDomainActiveRampUpCPUSet(entries))
	})

	t.Run("steady (non-ramp-up) shared allocations are excluded", func(t *testing.T) {
		t.Parallel()
		entries := state.PodEntries{
			"pod-1": state.ContainerEntries{
				"main": newGlobalDomainRampUpAllocation(
					"pod-1", "main", false, shared, false, machine.MustParse("0-3")),
			},
		}
		require.True(t, globalDomainActiveRampUpCPUSet(entries).IsEmpty())
	})

	t.Run("dedicated ramp-up allocations are excluded", func(t *testing.T) {
		t.Parallel()
		entries := state.PodEntries{
			"pod-1": state.ContainerEntries{
				"main": newGlobalDomainRampUpAllocation(
					"pod-1", "main", true, dedicated, false, machine.MustParse("4-7")),
			},
		}
		require.True(t, globalDomainActiveRampUpCPUSet(entries).IsEmpty())
	})

	t.Run("numa-binding shared ramp-up allocations are excluded", func(t *testing.T) {
		t.Parallel()
		entries := state.PodEntries{
			"pod-1": state.ContainerEntries{
				"main": newGlobalDomainRampUpAllocation(
					"pod-1", "main", true, shared, true, machine.MustParse("8-11")),
			},
		}
		require.True(t, globalDomainActiveRampUpCPUSet(entries).IsEmpty())
	})
}

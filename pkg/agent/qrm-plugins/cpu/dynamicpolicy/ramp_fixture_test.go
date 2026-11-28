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

	"github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

func rampTestEntries(t *testing.T, uid string) state.PodEntries {
	t.Helper()
	entries := state.PodEntries{}
	for numa, name := range []string{"n0", "n1"} {
		cpus := machine.NewCPUSet(0)
		if numa == 1 {
			cpus = machine.NewCPUSet(24)
		}
		entries[uid+"-"+name] = state.ContainerEntries{"main": &state.AllocationInfo{
			AllocationMeta: commonstate.AllocationMeta{PodUid: uid + "-" + name, ContainerName: "main", QoSLevel: consts.PodAnnotationQoSLevelSharedCores, Annotations: map[string]string{consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable}, OwnerPoolName: commonstate.PoolNameShare},
			RampUp:         true, RequestQuantity: 1, AllocationResult: cpus, OriginalAllocationResult: cpus,
			TopologyAwareAssignments: map[int]machine.CPUSet{numa: cpus}, OriginalTopologyAwareAssignments: map[int]machine.CPUSet{numa: cpus},
		}}
	}
	return entries
}

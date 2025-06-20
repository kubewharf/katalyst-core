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

package state

import (
	"fmt"

	"github.com/kubewharf/katalyst-core/pkg/config/agent/qrm"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

func GenerateMachineState(conf *qrm.QRMPluginsConfiguration, gpuTopologyProvider machine.GPUTopologyProvider) (GPUMap, error) {
	if gpuTopologyProvider == nil {
		return nil, fmt.Errorf("gpu topology provider must not be nil")
	}

	gpuTopology, err := gpuTopologyProvider.GetGPUTopology()
	if err != nil {
		return nil, fmt.Errorf("gpu topology provider failed with error: %v", err)
	}

	gpuMap := make(GPUMap)
	for deviceID := range gpuTopology.GPUs {
		gpuMap[deviceID] = &GPUState{
			GPUMemoryAllocatable: uint64(conf.GPUQRMPluginConfig.GPUMemoryAllocatablePerGPU.Value()),
			GPUMemoryAllocated:   0,
		}
	}

	return gpuMap, nil
}

// GenerateMachineStateFromPodEntries returns GPUMap for gpu memory based on
// machine info along with existed pod entries
func GenerateMachineStateFromPodEntries(
	conf *qrm.QRMPluginsConfiguration,
	podEntries PodEntries,
	gpuTopologyProvider machine.GPUTopologyProvider,
) (GPUMap, error) {
	machineState, err := GenerateMachineState(conf, gpuTopologyProvider)
	if err != nil {
		return nil, fmt.Errorf("GenerateMachineState failed with error: %v", err)
	}

	for gpuID, gpuState := range machineState {
		var gpuMemoryAllocated uint64
		for podUID, containerEntries := range podEntries {
			for containerName, allocationInfo := range containerEntries {
				if containerName != "" && allocationInfo != nil {
					gpuAllocation, ok := allocationInfo.TopologyAwareAllocations[gpuID]
					if !ok {
						continue
					}
					alloc := allocationInfo.Clone()
					alloc.AllocatedAllocation = gpuAllocation.Clone()
					alloc.TopologyAwareAllocations = map[string]GPUAllocation{gpuID: gpuAllocation}
					gpuMemoryAllocated += gpuAllocation.GPUMemoryQuantity
					gpuState.SetAllocationInfo(podUID, containerName, alloc)
				}
			}
		}
		gpuState.GPUMemoryAllocated = gpuMemoryAllocated

		generalLog := general.LoggerWithPrefix("GenerateMachineStateFromPodEntries", general.LoggingPKGFull)
		if gpuState.GPUMemoryAllocatable < gpuState.GPUMemoryAllocated {
			generalLog.Warningf("invalid allocated GPU memory: %d on GPU: %s"+
				" with allocatable GPU memory size: %d", gpuState.GPUMemoryAllocated, gpuID, gpuState.GPUMemoryAllocatable)
		}
		machineState[gpuID] = gpuState
	}

	return machineState, nil
}

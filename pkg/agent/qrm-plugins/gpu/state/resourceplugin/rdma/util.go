package rdma

import (
	"fmt"

	gpuconsts "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/consts"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

func GenerateMachineState(topologyRegistry *machine.DeviceTopologyRegistry) (RDMAMap, error) {
	if topologyRegistry == nil {
		return nil, fmt.Errorf("topology provider registry must not be nil")
	}

	rdmaTopology, _, err := topologyRegistry.GetDeviceTopology(gpuconsts.RDMADeviceName)
	if err != nil {
		return nil, fmt.Errorf("rdma topology provider failed with error: %v", err)
	}

	rdmaMap := make(RDMAMap)
	for deviceID := range rdmaTopology.Devices {
		rdmaMap[deviceID] = &RDMAState{}
	}

	return rdmaMap, nil
}

func GenerateMachineStateFromPodEntries(
	podEntries PodEntries, topologyRegistry *machine.DeviceTopologyRegistry,
) (RDMAMap, error) {
	machineState, err := GenerateMachineState(topologyRegistry)
	if err != nil {
		return nil, fmt.Errorf("GenerateMachineState failed with error: %v", err)
	}

	for rdmaID, rdmaState := range machineState {
		for podUID, containerEntries := range podEntries {
			for containerName, allocationInfo := range containerEntries {
				if containerName != "" && allocationInfo != nil {
					rdmaAllocation, ok := allocationInfo.TopologyAwareAllocations[rdmaID]
					if !ok {
						continue
					}
					alloc := allocationInfo.Clone()
					alloc.AllocatedAllocation = rdmaAllocation.Clone()
					alloc.TopologyAwareAllocations = map[string]RDMAAllocation{rdmaID: rdmaAllocation}
					rdmaState.SetAllocationInfo(podUID, containerName, alloc)
				}
			}
		}

		machineState[rdmaID] = rdmaState
	}

	return machineState, nil
}

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

package staticpolicy

import (
	"fmt"

	"k8s.io/apimachinery/pkg/util/sets"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/state"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/native"
)

func (p *StaticPolicy) updateAllocatedDevice() error {
	podDevicesMap, err := p.getPodDevicesMap()
	if err != nil {
		return fmt.Errorf("error getting pod devices map: %v", err)
	}

	p.Lock()
	defer p.Unlock()

	podEntries := p.state.GetPodEntries()
	for pod, devices := range podDevicesMap {
		if len(devices) == 0 {
			general.ErrorS(err, "pod has no devices", "podUID", pod.PodUID, "containerName", pod.ContainerName)
			continue
		}

		allocationInfo := podEntries.GetAllocationInfo(pod.PodUID, pod.ContainerName)
		if allocationInfo == nil {
			continue
		}

		gpuMemory := allocationInfo.AllocatedAllocation.GPUMemoryQuantity / uint64(devices.Len())
		topologyAwareAllocations := make(map[string]state.GPUAllocation)
		for _, deviceID := range devices.UnsortedList() {
			topologyAwareAllocations[deviceID] = state.GPUAllocation{
				GPUMemoryQuantity: gpuMemory,
			}
		}
		allocationInfo.TopologyAwareAllocations = topologyAwareAllocations

		podEntries.SetAllocationInfo(pod.PodUID, pod.ContainerName, allocationInfo)
	}

	machineState, err := state.GenerateMachineStateFromPodEntries(p.qrmConfig, podEntries, p.gpuTopologyProvider)
	if err != nil {
		return err
	}

	p.state.SetPodEntries(podEntries, false)
	p.state.SetMachineState(machineState, true)

	return nil
}

type podUIDContainerName struct {
	PodUID        string
	ContainerName string
}

func (p *StaticPolicy) getPodDevicesMap() (map[podUIDContainerName]sets.String, error) {
	kubeletCheckpoint, err := native.GetKubeletCheckpoint()
	if err != nil {
		return nil, fmt.Errorf("could not get kubelet checkpoint: %v", err)
	}

	podDevices, _ := kubeletCheckpoint.GetDataInLatestFormat()
	podDevicesMap := make(map[podUIDContainerName]sets.String)
	for _, podDevice := range podDevices {
		if !p.resourceNames.Has(podDevice.ResourceName) {
			continue
		}

		key := podUIDContainerName{
			PodUID:        podDevice.PodUID,
			ContainerName: podDevice.ContainerName,
		}

		_, ok := podDevicesMap[key]
		if !ok {
			podDevicesMap[key] = sets.NewString()
		}
		for _, deviceList := range podDevice.DeviceIDs {
			for _, deviceID := range deviceList {
				podDevicesMap[key].Insert(deviceID)
			}
		}
	}

	return podDevicesMap, nil
}

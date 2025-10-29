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

package gpu_memory

import (
	"fmt"

	"k8s.io/apimachinery/pkg/util/sets"

	"github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/strategy/allocate"
	gpuutil "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/util"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/util"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

// Filter filters the available GPU devices based on available GPU memory
// It returns devices that have enough available memory for the request
func (s *GPUMemoryStrategy) Filter(ctx *allocate.AllocationContext, allAvailableDevices []string) ([]string, error) {
	if ctx.DeviceTopology == nil {
		return nil, fmt.Errorf("GPU topology is nil")
	}

	_, gpuMemory, err := util.GetQuantityFromResourceRequests(ctx.ResourceReq.ResourceRequests, string(consts.ResourceGPUMemory), false)
	if err != nil {
		general.Warningf("getReqQuantityFromResourceReq failed with error: %v, use default available devices", err)
		return allAvailableDevices, nil
	}

	if gpuMemory == 0 {
		general.Infof("GPU Memory is 0, use default available devices")
		return allAvailableDevices, nil
	}

	filteredDevices, err := s.filterGPUDevices(ctx, gpuMemory, allAvailableDevices)
	if err != nil {
		return nil, err
	}

	return filteredDevices, nil
}

func (s *GPUMemoryStrategy) filterGPUDevices(
	ctx *allocate.AllocationContext,
	gpuMemoryRequest float64,
	allAvailableDevices []string,
) ([]string, error) {
	gpuRequest := ctx.DeviceReq.GetDeviceRequest()
	gpuMemoryPerGPU := gpuMemoryRequest / float64(gpuRequest)
	gpuMemoryAllocatablePerGPU := float64(ctx.GPUQRMPluginConfig.GPUMemoryAllocatablePerGPU.Value())

	machineState, ok := ctx.MachineState[consts.ResourceGPUMemory]
	if !ok {
		return nil, fmt.Errorf("machine state for %s is not available", consts.ResourceGPUMemory)
	}
	filteredDevices := sets.NewString()
	for _, device := range allAvailableDevices {
		if !ctx.HintNodes.IsEmpty() && !gpuutil.IsNUMAAffinityDevice(device, ctx.DeviceTopology, ctx.HintNodes) {
			continue
		}

		if !machineState.IsRequestSatisfied(device, gpuMemoryPerGPU, gpuMemoryAllocatablePerGPU) {
			general.Warningf("must include gpu %s has enough memory to allocate, gpuMemoryAllocatable: %f, gpuMemoryAllocated: %f, gpuMemoryPerGPU: %f",
				device, gpuMemoryAllocatablePerGPU, machineState.GetQuantityAllocated(device), gpuMemoryPerGPU)
			continue
		}

		filteredDevices.Insert(device)
	}

	return filteredDevices.UnsortedList(), nil
}

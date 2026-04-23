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

package gpu_compute

import (
	"fmt"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/sets"

	"github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/strategy/allocate"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/util"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

// Filter filters the available GPU devices based on available GPU resources (memory & milligpu)
// It returns devices that have enough available resources for all the requests
func (s *GPUComputeStrategy) Filter(ctx *allocate.AllocationContext, allAvailableDevices []string) ([]string, error) {
	if ctx.DeviceTopology == nil {
		return nil, fmt.Errorf("GPU topology is nil")
	}

	allowedResources := sets.NewString(string(consts.ResourceGPUMemory), string(consts.ResourceMilliGPU))
	_, resourceRequests, err := util.GetQuantityMapFromResourceReq(ctx.ResourceReq, allowedResources)
	if err != nil {
		general.Warningf("getQuantityMapFromResourceReq failed with error: %v, use default available devices", err)
		return allAvailableDevices, nil
	}

	if len(resourceRequests) == 0 {
		general.Infof("No non-zero GPU resources requested, use default available devices")
		return allAvailableDevices, nil
	}

	filteredDevices, err := s.filterGPUDevices(ctx, resourceRequests, allAvailableDevices)
	if err != nil {
		return nil, err
	}

	return filteredDevices, nil
}

func (s *GPUComputeStrategy) filterGPUDevices(
	ctx *allocate.AllocationContext,
	resourceRequests map[v1.ResourceName]float64,
	allAvailableDevices []string,
) ([]string, error) {
	gpuRequest := ctx.DeviceReq.GetDeviceRequest()

	requestPerGPU := make(map[v1.ResourceName]float64)
	for res, req := range resourceRequests {
		requestPerGPU[res] = req / float64(gpuRequest)
	}

	allocatablePerGPU := map[v1.ResourceName]float64{
		consts.ResourceGPUMemory: float64(ctx.GPUQRMPluginConfig.GPUMemoryAllocatablePerGPU.Value()),
		consts.ResourceMilliGPU:  float64(ctx.GPUQRMPluginConfig.MilliGPUAllocatablePerGPU.Value()),
	}

	filteredDevices := sets.NewString()
	for _, device := range allAvailableDevices {
		if !ctx.MachineState.IsResourceRequestSatisfied(device, requestPerGPU, allocatablePerGPU) {
			general.Warningf("must include gpu %s has enough resources to allocate", device)
			continue
		}

		filteredDevices.Insert(device)
	}

	return filteredDevices.UnsortedList(), nil
}

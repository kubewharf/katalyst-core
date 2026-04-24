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

package virtual_gpu

import (
	"fmt"
	"sort"

	"github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/strategy/allocate"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/util"
	qrmutil "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/util"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

// Sort sorts the filtered GPU devices based on available GPU compute
// It prioritizes devices with less available memory and considers NUMA affinity
// TODO: support multiple resources, prioritize virtual_gpu for sorting, then milligpu
func (s *VirtualGPUStrategy) Sort(ctx *allocate.AllocationContext, filteredDevices []string) ([]string, error) {
	if ctx.DeviceTopology == nil {
		return nil, fmt.Errorf("GPU topology is nil")
	}

	_, gpuCompute, err := qrmutil.GetQuantityFromResourceRequests(ctx.ResourceReq.ResourceRequests, string(consts.ResourceGPUMemory), nil)
	if err != nil {
		general.Warningf("getReqQuantityFromResourceReq failed with error: %v, use default filtered devices", err)
		return filteredDevices, nil
	}

	if gpuCompute == 0 {
		general.Infof("GPU Compute is 0, use default available devices")
		return filteredDevices, nil
	}

	gpuComputeAllocatablePerGPU := float64(ctx.GPUQRMPluginConfig.GPUMemoryAllocatablePerGPU.Value())
	milliGPUAllocatablePerGPU := float64(ctx.GPUQRMPluginConfig.MilliGPUAllocatablePerGPU.Value())
	gpuComputeMachineState := ctx.MachineState[consts.ResourceGPUMemory]
	milliGPUMachineState := ctx.MachineState[consts.ResourceMilliGPU]

	// Create a slice of device info with available memory and available milligpu
	type deviceInfo struct {
		ID                string
		AvailableMemory   float64
		AvailableMilliGPU float64
		NUMAAffinity      bool
	}

	devices := make([]deviceInfo, 0, len(filteredDevices))

	for _, deviceID := range filteredDevices {
		availableMemory := gpuComputeAllocatablePerGPU - gpuComputeMachineState.GetQuantityAllocated(deviceID)
		availableMilliGPU := milliGPUAllocatablePerGPU - milliGPUMachineState.GetQuantityAllocated(deviceID)
		devices = append(devices, deviceInfo{
			ID:                deviceID,
			AvailableMemory:   availableMemory,
			AvailableMilliGPU: availableMilliGPU,
			NUMAAffinity:      util.IsNUMAAffinityDevice(deviceID, ctx.DeviceTopology, ctx.HintNodes),
		})
	}

	// Get config flag for spreading/packing
	spreading := ctx.GPUQRMPluginConfig.FractionalGPUPrefersSpreading

	// Sort devices: first by NUMA affinity (preferred), then by available memory (ascending for packing, descending for spreading), then by available milligpu (same order)
	sort.Slice(devices, func(i, j int) bool {
		// First, check NUMA affinity
		if devices[i].NUMAAffinity != devices[j].NUMAAffinity {
			return devices[i].NUMAAffinity && !devices[j].NUMAAffinity
		}
		// Next, check available GPU memory
		if devices[i].AvailableMemory != devices[j].AvailableMemory {
			if spreading {
				return devices[i].AvailableMemory > devices[j].AvailableMemory
			}
			return devices[i].AvailableMemory < devices[j].AvailableMemory
		}
		// Finally, check available milligpu
		if spreading {
			return devices[i].AvailableMilliGPU > devices[j].AvailableMilliGPU
		}
		return devices[i].AvailableMilliGPU < devices[j].AvailableMilliGPU
	})

	// Extract sorted device IDs
	sortedDevices := make([]string, len(devices))
	for i, device := range devices {
		sortedDevices[i] = device.ID
	}

	general.InfoS("Sorted devices", "count", len(sortedDevices), "devices", sortedDevices)
	return sortedDevices, nil
}

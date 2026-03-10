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
	"sort"

	"github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/strategy/allocate"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/util"
	qrmutil "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/util"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

// Sort sorts the filtered GPU devices based on available GPU memory
// It prioritizes devices with less available memory and considers NUMA affinity
func (s *GPUMemoryStrategy) Sort(ctx *allocate.AllocationContext, filteredDevices []string) ([]string, error) {
	if ctx.DeviceTopology == nil {
		return nil, fmt.Errorf("GPU topology is nil")
	}

	_, gpuMemory, err := qrmutil.GetQuantityFromResourceRequests(ctx.ResourceReq.ResourceRequests, string(consts.ResourceGPUMemory), false)
	if err != nil {
		general.Warningf("getReqQuantityFromResourceReq failed with error: %v, use default filtered devices", err)
		return filteredDevices, nil
	}

	if gpuMemory == 0 {
		general.Infof("GPU Memory is 0, use default available devices")
		return filteredDevices, nil
	}

	gpuMemoryAllocatablePerGPU := float64(ctx.GPUQRMPluginConfig.GPUMemoryAllocatablePerGPU.Value())
	machineState := ctx.MachineState[consts.ResourceGPUMemory]

	// Create a slice of device info with available memory
	type deviceInfo struct {
		ID              string
		AvailableMemory float64
		NUMAAffinity    bool
	}

	devices := make([]deviceInfo, 0, len(filteredDevices))

	for _, deviceID := range filteredDevices {
		availableMemory := gpuMemoryAllocatablePerGPU - machineState.GetQuantityAllocated(deviceID)
		devices = append(devices, deviceInfo{
			ID:              deviceID,
			AvailableMemory: availableMemory,
			NUMAAffinity:    util.IsNUMAAffinityDevice(deviceID, ctx.DeviceTopology, ctx.HintNodes),
		})
	}

	// Sort devices: first by NUMA affinity (preferred), then by available memory (ascending)
	sort.Slice(devices, func(i, j int) bool {
		// If both devices have NUMA affinity or both don't, sort by available memory
		if devices[i].NUMAAffinity == devices[j].NUMAAffinity {
			return devices[i].AvailableMemory < devices[j].AvailableMemory
		}

		// Prefer devices with NUMA affinity
		return devices[i].NUMAAffinity && !devices[j].NUMAAffinity
	})

	// Extract sorted device IDs
	sortedDevices := make([]string, len(devices))
	for i, device := range devices {
		sortedDevices[i] = device.ID
	}

	general.InfoS("Sorted devices", "count", len(sortedDevices), "devices", sortedDevices)
	return sortedDevices, nil
}

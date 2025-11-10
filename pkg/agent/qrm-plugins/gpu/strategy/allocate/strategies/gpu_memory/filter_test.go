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
	"testing"

	"github.com/stretchr/testify/assert"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	"github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/state"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/strategy/allocate"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/qrm"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

func TestGPUMemoryStrategy_Filter(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                    string
		ctx                     *allocate.AllocationContext
		availableDevices        []string
		expectedFilteredDevices []string
		expectedErr             bool
	}{
		{
			name: "gpu topology is nil",
			ctx: &allocate.AllocationContext{
				ResourceReq: &v1alpha1.ResourceRequest{
					ResourceRequests: map[string]float64{
						string(consts.ResourceGPUMemory): 4,
					},
				},
				DeviceReq: &v1alpha1.DeviceRequest{
					DeviceRequest: 2,
				},
				GPUQRMPluginConfig: &qrm.GPUQRMPluginConfig{
					GPUMemoryAllocatablePerGPU: *resource.NewQuantity(2, resource.DecimalSI),
				},
			},
			expectedErr: true,
		},
		{
			name: "gpu memory does not exist, just allocate every device",
			ctx: &allocate.AllocationContext{
				DeviceTopology: &machine.DeviceTopology{
					Devices: map[string]machine.DeviceInfo{
						"gpu-0": {},
						"gpu-1": {},
						"gpu-2": {},
					},
				},
				ResourceReq: &v1alpha1.ResourceRequest{
					ResourceRequests: map[string]float64{
						string(consts.ResourceMemoryBandwidth): 4,
					},
				},
				DeviceReq: &v1alpha1.DeviceRequest{
					DeviceRequest: 2,
				},
				GPUQRMPluginConfig: &qrm.GPUQRMPluginConfig{
					GPUMemoryAllocatablePerGPU: *resource.NewQuantity(2, resource.DecimalSI),
				},
			},
			availableDevices:        []string{"gpu-0", "gpu-1", "gpu-2"},
			expectedFilteredDevices: []string{"gpu-0", "gpu-1", "gpu-2"},
		},
		{
			name: "gpu memory is 0, so we use all the available devices",
			ctx: &allocate.AllocationContext{
				DeviceTopology: &machine.DeviceTopology{
					Devices: map[string]machine.DeviceInfo{
						"gpu-0": {},
						"gpu-1": {},
						"gpu-2": {},
					},
				},
				ResourceReq: &v1alpha1.ResourceRequest{
					ResourceRequests: map[string]float64{
						string(consts.ResourceGPUMemory): 0,
					},
				},
				DeviceReq: &v1alpha1.DeviceRequest{
					DeviceRequest: 2,
				},
				GPUQRMPluginConfig: &qrm.GPUQRMPluginConfig{
					GPUMemoryAllocatablePerGPU: *resource.NewQuantity(2, resource.DecimalSI),
				},
			},
			availableDevices:        []string{"gpu-0", "gpu-1", "gpu-2"},
			expectedFilteredDevices: []string{"gpu-0", "gpu-1", "gpu-2"},
		},
		{
			name: "allocate available devices with available gpu memory",
			ctx: &allocate.AllocationContext{
				DeviceTopology: &machine.DeviceTopology{
					Devices: map[string]machine.DeviceInfo{
						"gpu-0": {},
						"gpu-1": {},
						"gpu-2": {},
					},
				},
				ResourceReq: &v1alpha1.ResourceRequest{
					ResourceRequests: map[string]float64{
						string(consts.ResourceGPUMemory): 4,
					},
				},
				DeviceReq: &v1alpha1.DeviceRequest{
					DeviceRequest: 2,
				},
				GPUQRMPluginConfig: &qrm.GPUQRMPluginConfig{
					GPUMemoryAllocatablePerGPU: *resource.NewQuantity(2, resource.DecimalSI),
				},
				MachineState: map[v1.ResourceName]state.AllocationMap{
					consts.ResourceGPUMemory: {
						"gpu-0": {},
						"gpu-1": {},
						"gpu-2": {},
					},
				},
			},
			availableDevices:        []string{"gpu-0", "gpu-1", "gpu-2"},
			expectedFilteredDevices: []string{"gpu-0", "gpu-1", "gpu-2"},
		},
		{
			name: "exclude devices with not enough gpu memory",
			ctx: &allocate.AllocationContext{
				DeviceTopology: &machine.DeviceTopology{
					Devices: map[string]machine.DeviceInfo{
						"gpu-0": {},
						"gpu-1": {},
						"gpu-2": {},
						"gpu-3": {},
					},
				},
				ResourceReq: &v1alpha1.ResourceRequest{
					ResourceRequests: map[string]float64{
						string(consts.ResourceGPUMemory): 4,
					},
				},
				DeviceReq: &v1alpha1.DeviceRequest{
					DeviceRequest: 2,
				},
				GPUQRMPluginConfig: &qrm.GPUQRMPluginConfig{
					GPUMemoryAllocatablePerGPU: *resource.NewQuantity(2, resource.DecimalSI),
				},
				MachineState: map[v1.ResourceName]state.AllocationMap{
					consts.ResourceGPUMemory: {
						// 2 GB allocated
						"gpu-0": {
							PodEntries: map[string]state.ContainerEntries{
								"pod-0": {
									"container-0": &state.AllocationInfo{
										AllocatedAllocation: state.Allocation{
											Quantity: 2,
										},
									},
								},
							},
						},
						"gpu-1": {},
						"gpu-2": {},
						"gpu-3": {
							// 1 GB allocated
							PodEntries: map[string]state.ContainerEntries{
								"pod-1": {
									"container-0": &state.AllocationInfo{
										AllocatedAllocation: state.Allocation{
											Quantity: 0.5,
										},
									},
									"container-1": &state.AllocationInfo{
										AllocatedAllocation: state.Allocation{
											Quantity: 0.5,
										},
									},
								},
							},
						},
					},
				},
			},
			availableDevices:        []string{"gpu-0", "gpu-1", "gpu-2", "gpu-3"},
			expectedFilteredDevices: []string{"gpu-1", "gpu-2"},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			strategy := NewGPUMemoryStrategy()
			filteredDevices, err := strategy.Filter(tt.ctx, tt.availableDevices)
			if tt.expectedErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.ElementsMatch(t, tt.expectedFilteredDevices, filteredDevices)
			}
		})
	}
}

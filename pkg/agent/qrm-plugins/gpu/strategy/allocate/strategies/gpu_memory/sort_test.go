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

func TestGPUMemoryStrategy_Sort(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                  string
		ctx                   *allocate.AllocationContext
		filteredDevices       []string
		expectedSortedDevices []string
		expectedErr           bool
	}{
		{
			name: "nil gpu topology",
			ctx: &allocate.AllocationContext{
				ResourceReq: &v1alpha1.ResourceRequest{
					ResourceRequests: map[string]float64{
						string(consts.ResourceGPUMemory): 1,
					},
				},
			},
			filteredDevices: []string{"gpu-1", "gpu-2"},
			expectedErr:     true,
		},
		{
			name: "gpu memory is 0 returns all available devices without sorting",
			ctx: &allocate.AllocationContext{
				DeviceTopology: &machine.DeviceTopology{
					Devices: map[string]machine.DeviceInfo{
						"gpu-1": {},
						"gpu-2": {},
					},
				},
				ResourceReq: &v1alpha1.ResourceRequest{
					ResourceRequests: map[string]float64{
						string(consts.ResourceGPUMemory): 0,
					},
				},
			},
			filteredDevices:       []string{"gpu-1", "gpu-2"},
			expectedSortedDevices: []string{"gpu-1", "gpu-2"},
		},
		{
			name: "devices are sorted by NUMA affinity first",
			ctx: &allocate.AllocationContext{
				DeviceTopology: &machine.DeviceTopology{
					// gpu-1 has NUMA affinity but gpu-2 does not
					Devices: map[string]machine.DeviceInfo{
						"gpu-1": {
							NumaNodes: []int{1},
						},
						"gpu-2": {
							NumaNodes: []int{0},
						},
					},
				},
				ResourceReq: &v1alpha1.ResourceRequest{
					ResourceRequests: map[string]float64{
						string(consts.ResourceGPUMemory): 1,
					},
				},
				GPUQRMPluginConfig: &qrm.GPUQRMPluginConfig{
					GPUMemoryAllocatablePerGPU: *resource.NewQuantity(2, resource.DecimalSI),
				},
				HintNodes: machine.NewCPUSet(0),
			},
			filteredDevices:       []string{"gpu-1", "gpu-2"},
			expectedSortedDevices: []string{"gpu-2", "gpu-1"},
		},
		{
			name: "for devices with NUMA affinity, they are sorted by available memory in ascending order",
			ctx: &allocate.AllocationContext{
				DeviceTopology: &machine.DeviceTopology{
					Devices: map[string]machine.DeviceInfo{
						"gpu-1": {
							NumaNodes: []int{0},
						},
						"gpu-2": {
							NumaNodes: []int{1},
						},
						"gpu-3": {
							NumaNodes: []int{2},
						},
					},
				},
				ResourceReq: &v1alpha1.ResourceRequest{
					ResourceRequests: map[string]float64{
						string(consts.ResourceGPUMemory): 1,
					},
				},
				GPUQRMPluginConfig: &qrm.GPUQRMPluginConfig{
					GPUMemoryAllocatablePerGPU: *resource.NewQuantity(4, resource.DecimalSI),
				},
				HintNodes: machine.NewCPUSet(0, 1),
				MachineState: map[v1.ResourceName]state.AllocationMap{
					consts.ResourceGPUMemory: {
						"gpu-1": {
							PodEntries: map[string]state.ContainerEntries{
								"pod-0": {
									"container-0": {
										AllocatedAllocation: state.Allocation{
											Quantity: 1,
										},
									},
								},
							},
						},
						"gpu-2": {
							PodEntries: map[string]state.ContainerEntries{
								"pod-1": {
									"container-1": {
										AllocatedAllocation: state.Allocation{
											Quantity: 1,
										},
									},
									"container-2": {
										AllocatedAllocation: state.Allocation{
											Quantity: 1,
										},
									},
								},
							},
						},
					},
				},
			},
			filteredDevices:       []string{"gpu-1", "gpu-2", "gpu-3"},
			expectedSortedDevices: []string{"gpu-2", "gpu-1", "gpu-3"},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			strategy := NewGPUMemoryStrategy()
			sortedDevices, err := strategy.Sort(tt.ctx, tt.filteredDevices)

			if tt.expectedErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expectedSortedDevices, sortedDevices)
			}
		})
	}
}

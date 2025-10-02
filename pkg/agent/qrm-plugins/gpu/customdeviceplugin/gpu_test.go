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

package customdeviceplugin

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	"github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/baseplugin"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/state"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

func generateTestGPUCustomDevicePlugin(
	stateDirectory string, numGPUs, numNUMAs, numCPUs, socketNum int,
) (*GPUDevicePlugin, error) {
	gpuTopology, err := machine.GenerateDummyGPUTopology(numGPUs, numNUMAs)
	if err != nil {
		return nil, fmt.Errorf("GenerateDummyGPUTopology failed: %v", err)
	}
	providerStub := machine.NewGPUTopologyProviderStub(gpuTopology)

	basePlugin, err := baseplugin.GenerateTestBasePlugin(stateDirectory, providerStub, numCPUs, numNUMAs, socketNum)
	if err != nil {
		return nil, fmt.Errorf("generateTestBasePlugin failed: %v", err)
	}

	p := NewGPUDevicePlugin(basePlugin)

	machineState := make(state.GPUMap)
	// Initialize machine state to not have any gpu memory allocated
	for i := 0; i < numGPUs; i++ {
		machineState[fmt.Sprintf("%d", i)] = &state.GPUState{
			GPUMemoryAllocatable: 16 * 1024 * 1024 * 1024,
			GPUMemoryAllocated:   0,
		}
	}

	gpuDevicePlugin := p.(*GPUDevicePlugin)
	gpuDevicePlugin.State.SetMachineState(machineState, true)

	return gpuDevicePlugin, nil
}

func TestGPUDevicePlugin_UpdateAllocatableAssociatedDevices(t *testing.T) {
	t.Parallel()

	req1 := &pluginapi.UpdateAllocatableAssociatedDevicesRequest{
		DeviceName: "nvidia.com/gpu",
		Devices: []*pluginapi.AssociatedDevice{
			{
				ID: "0",
				Topology: &pluginapi.TopologyInfo{
					Nodes: []*pluginapi.NUMANode{
						{
							ID: 0,
						},
					},
				},
				Health: pluginapi.Healthy,
			},
			{
				ID: "1",
				Topology: &pluginapi.TopologyInfo{
					Nodes: []*pluginapi.NUMANode{
						{
							ID: 1,
						},
					},
				},
				Health: pluginapi.Healthy,
			},
		},
	}

	req2 := &pluginapi.UpdateAllocatableAssociatedDevicesRequest{
		DeviceName: "nvidia.com/gpu",
		Devices: []*pluginapi.AssociatedDevice{
			{
				ID:     "0",
				Health: pluginapi.Healthy,
			},
			{
				ID:     "1",
				Health: pluginapi.Healthy,
			},
		},
	}

	p1, err := generateTestGPUCustomDevicePlugin(t.TempDir(), 2, 2, 4, 2)
	assert.NoError(t, err)
	// For this test case, we override the dummy gpu topology to nil
	err = p1.GpuTopologyProvider.SetGPUTopology(nil)
	assert.NoError(t, err)

	p2, err := generateTestGPUCustomDevicePlugin(t.TempDir(), 2, 2, 4, 2)
	assert.NoError(t, err)
	// For this test case, we override the dummy gpu topology to nil
	err = p2.GpuTopologyProvider.SetGPUTopology(nil)
	assert.NoError(t, err)

	testCases := []struct {
		name         string
		plugin       *GPUDevicePlugin
		req          *pluginapi.UpdateAllocatableAssociatedDevicesRequest
		expectedGPUs map[string]machine.GPUInfo
	}{
		{
			name:   "Normal case",
			plugin: p1,
			req:    req1,
			expectedGPUs: map[string]machine.GPUInfo{
				"0": {
					Health:   pluginapi.Healthy,
					NUMANode: []int{0},
				},
				"1": {
					Health:   pluginapi.Healthy,
					NUMANode: []int{1},
				},
			},
		},
		{
			name:   "Empty topology",
			plugin: p2,
			req:    req2,
			expectedGPUs: map[string]machine.GPUInfo{
				"0": {
					Health:   pluginapi.Healthy,
					NUMANode: nil,
				},
				"1": {
					Health:   pluginapi.Healthy,
					NUMANode: nil,
				},
			},
		},
	}
	for _, tt := range testCases {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := tt.plugin.UpdateAllocatableAssociatedDevices(tt.req)
			assert.NoError(t, err)

			gpuTopology, _, err := tt.plugin.GpuTopologyProvider.GetGPUTopology()
			assert.NoError(t, err)
			assert.Equal(t, tt.expectedGPUs, gpuTopology.GPUs)
		})
	}
}

func TestGPUDevicePlugin_AllocateAssociatedDevice(t *testing.T) {
	t.Parallel()

	p1, err := generateTestGPUCustomDevicePlugin(t.TempDir(), 4, 2, 4, 2)
	assert.NoError(t, err)

	p2, err := generateTestGPUCustomDevicePlugin(t.TempDir(), 4, 2, 4, 2)
	assert.NoError(t, err)

	p3, err := generateTestGPUCustomDevicePlugin(t.TempDir(), 2, 1, 4, 2)
	assert.NoError(t, err)
	// Fill up one of the GPUs memory completely
	p3.State.SetMachineState(map[string]*state.GPUState{
		"0": {
			GPUMemoryAllocatable: 16 * 1024 * 1024 * 1024,
			GPUMemoryAllocated:   16 * 1024 * 1024 * 1024,
		},
		"1": {
			GPUMemoryAllocatable: 16 * 1024 * 1024 * 1024,
			GPUMemoryAllocated:   0 * 1024 * 1024 * 1024,
		},
	}, false)

	p4, err := generateTestGPUCustomDevicePlugin(t.TempDir(), 4, 2, 4, 2)
	assert.NoError(t, err)

	p5, err := generateTestGPUCustomDevicePlugin(t.TempDir(), 2, 1, 4, 2)
	assert.NoError(t, err)
	// Pod is already allocated
	p5.State.SetAllocationInfo("pod5", "container5", &state.AllocationInfo{
		AllocatedAllocation: state.GPUAllocation{
			GPUMemoryQuantity: 8 * 1024 * 1024 * 1024,
			NUMANodes:         []int{0},
		},
		TopologyAwareAllocations: map[string]state.GPUAllocation{
			"1": {
				GPUMemoryQuantity: 8 * 1024 * 1024 * 1024,
				NUMANodes:         []int{0},
			},
		},
	}, false)

	req1 := &pluginapi.AssociatedDeviceRequest{
		ResourceRequest: &pluginapi.ResourceRequest{
			ResourceName:  string(consts.ResourceGPUMemory),
			PodUid:        "pod1",
			ContainerName: "container1",
			ResourceRequests: map[string]float64{
				string(consts.ResourceGPUMemory): 8 * 1024 * 1024 * 1024,
			},
		},
		DeviceRequest: &pluginapi.DeviceRequest{
			DeviceName:       p1.DeviceName(),
			AvailableDevices: []string{"2", "3"},
			ReusableDevices:  []string{"0", "1"},
			DeviceRequest:    2,
		},
	}

	req2 := &pluginapi.AssociatedDeviceRequest{
		ResourceRequest: &pluginapi.ResourceRequest{
			ResourceName:  string(consts.ResourceGPUMemory),
			PodUid:        "pod2",
			ContainerName: "container2",
			ResourceRequests: map[string]float64{
				string(consts.ResourceGPUMemory): 32 * 1024 * 1024 * 1024,
			},
		},
		DeviceRequest: &pluginapi.DeviceRequest{
			DeviceName:       p2.DeviceName(),
			ReusableDevices:  []string{"0"},
			AvailableDevices: []string{"1", "2"},
			DeviceRequest:    2,
			Hint: &pluginapi.TopologyHint{
				Nodes:     []uint64{0},
				Preferred: true,
			},
		},
	}

	req3 := &pluginapi.AssociatedDeviceRequest{
		ResourceRequest: &pluginapi.ResourceRequest{
			ResourceName:  string(consts.ResourceGPUMemory),
			PodUid:        "pod3",
			ContainerName: "container3",
			ResourceRequests: map[string]float64{
				string(consts.ResourceGPUMemory): 8 * 1024 * 1024 * 1024,
			},
		},
		DeviceRequest: &pluginapi.DeviceRequest{
			DeviceName:       p3.DeviceName(),
			AvailableDevices: []string{"0", "1"},
			DeviceRequest:    1,
			Hint: &pluginapi.TopologyHint{
				Nodes:     []uint64{0},
				Preferred: true,
			},
		},
	}

	req4 := &pluginapi.AssociatedDeviceRequest{
		ResourceRequest: &pluginapi.ResourceRequest{
			ResourceName:  string(consts.ResourceGPUMemory),
			PodUid:        "pod4",
			ContainerName: "container4",
			ResourceRequests: map[string]float64{
				string(consts.ResourceGPUMemory): 8 * 1024 * 1024 * 1024,
			},
		},
		DeviceRequest: &pluginapi.DeviceRequest{
			DeviceName:       p4.DeviceName(),
			AvailableDevices: []string{"0", "1"},
			DeviceRequest:    1,
		},
	}

	req5 := &pluginapi.AssociatedDeviceRequest{
		ResourceRequest: &pluginapi.ResourceRequest{
			ResourceName:  string(consts.ResourceGPUMemory),
			PodUid:        "pod5",
			ContainerName: "container5",
			ResourceRequests: map[string]float64{
				string(consts.ResourceGPUMemory): 16 * 1024 * 1024 * 1024,
			},
		},
		DeviceRequest: &pluginapi.DeviceRequest{
			DeviceName:       p5.DeviceName(),
			AvailableDevices: []string{"0", "1"},
			DeviceRequest:    1,
		},
	}

	testCases := []struct {
		name        string
		plugin      *GPUDevicePlugin
		req         *pluginapi.AssociatedDeviceRequest
		expectedErr bool
		checkResp   func(t *testing.T, resp *pluginapi.AssociatedDeviceAllocationResponse)
	}{
		{
			name:        "Use all reusable devices first",
			plugin:      p1,
			req:         req1,
			expectedErr: false,
			checkResp: func(t *testing.T, resp *pluginapi.AssociatedDeviceAllocationResponse) {
				assert.NotNil(t, resp)
				assert.NotNil(t, resp.AllocationResult)
				assert.Equal(t, []string{"0", "1"}, resp.AllocationResult.GetAllocatedDevices())
			},
		},
		{
			name:        "Use available devices after reusable devices are used up",
			plugin:      p2,
			req:         req2,
			expectedErr: false,
			checkResp: func(t *testing.T, resp *pluginapi.AssociatedDeviceAllocationResponse) {
				assert.NotNil(t, resp)
				assert.NotNil(t, resp.AllocationResult)
				assert.Equal(t, []string{"0", "1"}, resp.AllocationResult.GetAllocatedDevices())
			},
		},
		{
			name:        "A GPU device is completely used up",
			plugin:      p3,
			req:         req3,
			expectedErr: false,
			checkResp: func(t *testing.T, resp *pluginapi.AssociatedDeviceAllocationResponse) {
				assert.NotNil(t, resp)
				assert.NotNil(t, resp.AllocationResult)
				assert.Equal(t, []string{"1"}, resp.AllocationResult.GetAllocatedDevices())
			},
		},
		{
			name:        "Request has no hints",
			plugin:      p4,
			req:         req4,
			expectedErr: true,
		},
		{
			name:        "Pod is already allocated in state",
			plugin:      p5,
			req:         req5,
			expectedErr: false,
			checkResp: func(t *testing.T, resp *pluginapi.AssociatedDeviceAllocationResponse) {
				assert.NotNil(t, resp)
				assert.NotNil(t, resp.AllocationResult)
				assert.Equal(t, []string{"1"}, resp.AllocationResult.GetAllocatedDevices())
			},
		},
	}

	for _, tt := range testCases {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			resp, err := tt.plugin.AllocateAssociatedDevice(tt.req)

			if tt.expectedErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}

			if tt.checkResp != nil {
				tt.checkResp(t, resp)
			}
		})
	}
}

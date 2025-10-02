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

package resourceplugin

import (
	"fmt"
	"testing"

	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	"github.com/stretchr/testify/assert"

	"github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/baseplugin"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/state"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

func generateTestGPUMemPlugin(stateDirectory string, numGPUs, numNUMAs, numCPUs, socketNum int) (*GPUMemPlugin, error) {
	gpuTopology, err := machine.GenerateDummyGPUTopology(numGPUs, numNUMAs)
	if err != nil {
		return nil, fmt.Errorf("GenerateDummyGPUTopology failed: %v", err)
	}
	providerStub := machine.NewGPUTopologyProviderStub(gpuTopology)

	basePlugin, err := baseplugin.GenerateTestBasePlugin(stateDirectory, providerStub, numCPUs, numNUMAs, socketNum)
	if err != nil {
		return nil, fmt.Errorf("generateTestBasePlugin failed: %v", err)
	}

	p := NewGPUMemPlugin(basePlugin)

	machineState := make(state.GPUMap)
	// Initialize machine state to not have any gpu memory allocated
	for i := 0; i < numGPUs; i++ {
		machineState[fmt.Sprintf("%d", i)] = &state.GPUState{
			GPUMemoryAllocatable: 16 * 1024 * 1024 * 1024,
			GPUMemoryAllocated:   0,
		}
	}

	gpuMemPlugin := p.(*GPUMemPlugin)
	gpuMemPlugin.State.SetMachineState(machineState, true)

	return gpuMemPlugin, nil
}

func TestGPUMemPlugin_GetTopologyHints(t *testing.T) {
	t.Parallel()

	req1 := &pluginapi.ResourceRequest{
		PodUid:         "pod1",
		PodNamespace:   "default",
		PodName:        "pod-1",
		ContainerName:  "container-1",
		ContainerType:  pluginapi.ContainerType_MAIN,
		ContainerIndex: 0,
		ResourceRequests: map[string]float64{
			string(consts.ResourceGPUMemory): 8 * 1024 * 1024 * 1024,
			"nvidia.com/gpu":                 2,
		},
		Annotations: map[string]string{
			consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
		},
	}

	req2 := &pluginapi.ResourceRequest{
		PodUid:         "pod2",
		PodNamespace:   "default",
		PodName:        "pod-2",
		ContainerName:  "container-2",
		ContainerType:  pluginapi.ContainerType_MAIN,
		ContainerIndex: 0,
		ResourceRequests: map[string]float64{
			string(consts.ResourceGPUMemory): 8 * 1024 * 1024 * 1024,
			"nvidia.com/gpu":                 2,
		},
		Annotations: map[string]string{
			consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
		},
	}

	reqWithInvalidQoS := &pluginapi.ResourceRequest{
		PodUid:         "pod3",
		PodNamespace:   "default",
		PodName:        "pod-3",
		ContainerName:  "container-3",
		ContainerType:  pluginapi.ContainerType_MAIN,
		ContainerIndex: 0,
		Annotations:    map[string]string{},
	}

	p1, err := generateTestGPUMemPlugin(t.TempDir(), 4, 2, 8, 2)
	assert.NoError(t, err)

	p2, err := generateTestGPUMemPlugin(t.TempDir(), 4, 2, 8, 2)
	assert.NoError(t, err)

	// pod1 is already allocated to numa node 1
	p2.State.SetAllocationInfo("pod1", "container-1", &state.AllocationInfo{
		AllocationMeta: commonstate.AllocationMeta{
			PodUid:        "pod1",
			PodNamespace:  "default",
			PodName:       "pod-1",
			ContainerName: "container-1",
		},
		AllocatedAllocation: state.GPUAllocation{
			NUMANodes: []int{1},
		},
	}, false)

	p3, err := generateTestGPUMemPlugin(t.TempDir(), 2, 1, 4, 1)
	assert.NoError(t, err)

	// All GPU memory have been used up
	p3.State.SetMachineState(map[string]*state.GPUState{
		"0": {
			GPUMemoryAllocatable: 16 * 1024 * 1024 * 1024,
			GPUMemoryAllocated:   16 * 1024 * 1024 * 1024,
		},
		"1": {
			GPUMemoryAllocatable: 16 * 1024 * 1024 * 1024,
			GPUMemoryAllocated:   16 * 1024 * 1024 * 1024,
		},
	}, false)

	testCases := []struct {
		name        string
		plugin      *GPUMemPlugin
		req         *pluginapi.ResourceRequest
		expectedErr bool
		checkResp   func(t *testing.T, resp *pluginapi.ResourceHintsResponse)
	}{
		{
			name:        "normal case",
			plugin:      p1,
			req:         req1,
			expectedErr: false,
			checkResp: func(t *testing.T, resp *pluginapi.ResourceHintsResponse) {
				assert.NotNil(t, resp)
				assert.NotNil(t, resp.ResourceHints)
				assert.Contains(t, resp.ResourceHints, p1.ResourceName())
				hints := resp.ResourceHints[p1.ResourceName()]
				assert.NotNil(t, hints)
				assert.Equal(t, 3, len(hints.Hints))
			},
		},
		{
			name:        "get hints for another pod",
			plugin:      p1,
			req:         req2,
			expectedErr: false,
			checkResp: func(t *testing.T, resp *pluginapi.ResourceHintsResponse) {
				assert.NotNil(t, resp)
				assert.NotNil(t, resp.ResourceHints)
				assert.Contains(t, resp.ResourceHints, p1.ResourceName())
				hints := resp.ResourceHints[p1.ResourceName()]
				assert.NotNil(t, hints)
				assert.Equal(t, 3, len(hints.Hints))
			},
		},
		{
			name:        "regenerate hints for existing pod",
			plugin:      p2,
			req:         req1,
			expectedErr: false,
			checkResp: func(t *testing.T, resp *pluginapi.ResourceHintsResponse) {
				assert.NotNil(t, resp)
				assert.NotNil(t, resp.ResourceHints)
				assert.Contains(t, resp.ResourceHints, p2.ResourceName())
				hints := resp.ResourceHints[p2.ResourceName()]
				assert.NotNil(t, hints)
				assert.Equal(t, 1, len(hints.Hints))
				assert.Equal(t, hints.Hints[0].Nodes, []uint64{1})
				assert.Equal(t, hints.Hints[0].Preferred, true)
			},
		},
		{
			name:        "insufficient gpu memory",
			plugin:      p3,
			req:         req1,
			expectedErr: true,
			checkResp:   nil,
		},
		{
			name:        "request with invalid QoS",
			plugin:      p1,
			req:         reqWithInvalidQoS,
			expectedErr: true,
			checkResp:   nil,
		},
	}

	for _, tt := range testCases {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			resp, err := tt.plugin.GetTopologyHints(tt.req)
			fmt.Println(resp)

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

func TestGPUMemPlugin_Allocate(t *testing.T) {
	t.Parallel()

	req1 := &pluginapi.ResourceRequest{
		PodUid:         "pod1",
		PodNamespace:   "default",
		PodName:        "pod-1",
		ContainerName:  "container-1",
		ContainerType:  pluginapi.ContainerType_MAIN,
		ContainerIndex: 0,
		ResourceRequests: map[string]float64{
			string(consts.ResourceGPUMemory): 8 * 1024 * 1024 * 1024,
			"nvidia.com/gpu":                 1,
		},
		Annotations: map[string]string{
			consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
		},
		Hint: &pluginapi.TopologyHint{
			Nodes:     []uint64{0},
			Preferred: true,
		},
	}

	reqForInitContainer := &pluginapi.ResourceRequest{
		PodUid:         "pod2",
		PodNamespace:   "default",
		PodName:        "pod-2",
		ContainerName:  "container-2",
		ContainerType:  pluginapi.ContainerType_INIT,
		ContainerIndex: 0,
		ResourceRequests: map[string]float64{
			string(consts.ResourceGPUMemory): 8 * 1024 * 1024 * 1024,
			"nvidia.com/gpu":                 1,
		},
		Annotations: map[string]string{
			consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
		},
	}

	reqForSidecarContainer := &pluginapi.ResourceRequest{
		PodUid:         "pod3",
		PodNamespace:   "default",
		PodName:        "pod-3",
		ContainerName:  "container-3",
		ContainerType:  pluginapi.ContainerType_SIDECAR,
		ContainerIndex: 0,
		ResourceRequests: map[string]float64{
			string(consts.ResourceGPUMemory): 8 * 1024 * 1024 * 1024,
			"nvidia.com/gpu":                 1,
		},
		Annotations: map[string]string{
			consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
		},
	}

	p1, err := generateTestGPUMemPlugin(t.TempDir(), 4, 2, 8, 2)
	assert.NoError(t, err)

	p2, err := generateTestGPUMemPlugin(t.TempDir(), 4, 2, 8, 2)
	assert.NoError(t, err)

	p3, err := generateTestGPUMemPlugin(t.TempDir(), 4, 2, 8, 2)
	assert.NoError(t, err)

	testCases := []struct {
		name        string
		plugin      *GPUMemPlugin
		req         *pluginapi.ResourceRequest
		expectedErr bool
		checkResp   func(t *testing.T, resp *pluginapi.ResourceAllocationResponse)
	}{
		{
			name:        "normal case",
			plugin:      p1,
			req:         req1,
			expectedErr: false,
			checkResp: func(t *testing.T, resp *pluginapi.ResourceAllocationResponse) {
				assert.NotNil(t, resp)
				assert.NotNil(t, resp.AllocationResult)
				assert.NotNil(t, resp.AllocationResult.ResourceAllocation)
				assert.Equal(t, resp.AllocationResult.ResourceAllocation[string(consts.ResourceGPUMemory)].AllocatedQuantity, float64(8*1024*1024*1024))
				assert.Equal(t, resp.AllocationResult.ResourceAllocation[string(consts.ResourceGPUMemory)].ResourceHints.Hints[0], req1.Hint)
			},
		},
		{
			name:        "allocate for sidecar container",
			plugin:      p2,
			req:         reqForSidecarContainer,
			expectedErr: false,
			checkResp: func(t *testing.T, resp *pluginapi.ResourceAllocationResponse) {
				assert.NotNil(t, resp)
				assert.NotNil(t, resp.AllocationResult)
				assert.NotNil(t, resp.AllocationResult.ResourceAllocation)
				assert.Equal(t, resp.AllocationResult.ResourceAllocation[string(consts.ResourceGPUMemory)].AllocatedQuantity, float64(0))
			},
		},
		{
			name:        "allocate for init containers",
			plugin:      p3,
			req:         reqForInitContainer,
			expectedErr: false,
			checkResp: func(t *testing.T, resp *pluginapi.ResourceAllocationResponse) {
				assert.NotNil(t, resp)
				assert.Nil(t, resp.AllocationResult)
			},
		},
	}
	for _, tt := range testCases {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			resp, err := tt.plugin.Allocate(tt.req)
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

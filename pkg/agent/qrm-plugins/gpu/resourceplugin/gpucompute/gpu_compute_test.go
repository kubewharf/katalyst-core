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

package gpucompute

import (
	"context"
	"testing"

	cadvisorapi "github.com/google/cadvisor/info/v1"
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/stretchr/testify/assert"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/runtime"
	deviceplugin "k8s.io/kubelet/pkg/apis/deviceplugin/v1alpha"
	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	"github.com/kubewharf/katalyst-api/pkg/apis/node/v1alpha1"
	"github.com/kubewharf/katalyst-api/pkg/consts"
	katalyst_base "github.com/kubewharf/katalyst-core/cmd/base"
	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/agent"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/baseplugin"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/customdeviceplugin/gpu"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/state"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/qrm/statedirectory"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

func generateTestConfiguration(t *testing.T) *config.Configuration {
	conf := config.NewConfiguration()
	tmpDir := t.TempDir()
	conf.QRMPluginSocketDirs = []string{tmpDir}
	conf.CheckpointManagerDir = tmpDir

	return conf
}

func generateTestGenericContext(t *testing.T, conf *config.Configuration) *agent.GenericContext {
	genericCtx, err := katalyst_base.GenerateFakeGenericContext([]runtime.Object{})
	if err != nil {
		t.Fatalf("unable to generate test generic context: %v", err)
	}

	metaServer, err := metaserver.NewMetaServer(genericCtx.Client, metrics.DummyMetrics{}, conf)
	if err != nil {
		t.Fatalf("unable to generate test meta server: %v", err)
	}

	metaServer.CPUTopology = &machine.CPUTopology{
		NumNUMANodes: 2,
		NumSockets:   2,
		NUMAToCPUs: map[int]machine.CPUSet{
			0: machine.NewCPUSet(0, 1),
			1: machine.NewCPUSet(2, 3),
		},
		CPUDetails: map[int]machine.CPUTopoInfo{
			0: {NUMANodeID: 0},
			1: {NUMANodeID: 0},
			2: {NUMANodeID: 1},
			3: {NUMANodeID: 1},
		},
	}

	metaServer.MachineInfo = &cadvisorapi.MachineInfo{
		Topology: []cadvisorapi.Node{
			{
				Id: 0,
				Cores: []cadvisorapi.Core{
					{SocketID: 0, Id: 0, Threads: []int{0, 4}},
					{SocketID: 0, Id: 1, Threads: []int{1, 5}},
					{SocketID: 0, Id: 2, Threads: []int{2, 6}},
					{SocketID: 0, Id: 3, Threads: []int{3, 7}},
				},
			},
			{
				Id: 1,
				Cores: []cadvisorapi.Core{
					{SocketID: 1, Id: 4, Threads: []int{8, 12}},
					{SocketID: 1, Id: 5, Threads: []int{9, 13}},
					{SocketID: 1, Id: 6, Threads: []int{10, 14}},
					{SocketID: 1, Id: 7, Threads: []int{11, 15}},
				},
			},
		},
	}

	agentCtx := &agent.GenericContext{
		GenericContext: genericCtx,
		MetaServer:     metaServer,
		PluginManager:  nil,
	}

	agentCtx.MetaServer = metaServer
	return agentCtx
}

func makeTestBasePlugin(t *testing.T) *baseplugin.BasePlugin {
	conf := generateTestConfiguration(t)
	agentCtx := generateTestGenericContext(t, conf)

	tmpDir := t.TempDir()
	conf.StateDirectoryConfiguration = &statedirectory.StateDirectoryConfiguration{
		StateFileDirectory: tmpDir,
	}
	conf.GPUDeviceNames = []string{"test-gpu"}
	conf.GPUMemoryAllocatablePerGPU = *resource.NewQuantity(10, resource.DecimalSI)
	conf.MilliGPUAllocatablePerGPU = *resource.NewQuantity(1000, resource.DecimalSI)

	basePlugin, err := baseplugin.NewBasePlugin(agentCtx, conf, metrics.DummyMetrics{})
	assert.NoError(t, err)

	gpu.NewGPUDevicePlugin(basePlugin)

	stateImpl, err := state.NewCheckpointState(conf.StateDirectoryConfiguration, conf.QRMPluginsConfiguration, "test", "test-policy", state.NewDefaultResourceStateGeneratorRegistry(), true, metrics.DummyMetrics{})
	assert.NoError(t, err)

	basePlugin.SetState(stateImpl)

	return basePlugin
}

func TestGPUComputePlugin_GetTopologyHints(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                   string
		podUID                 string
		containerName          string
		req                    *pluginapi.ResourceRequest
		allocationInfo         *state.AllocationInfo
		allocationInfoMap      map[v1.ResourceName]*state.AllocationInfo // add allocationInfoMap for multiple resources
		allocationResourcesMap *state.AllocationResourcesMap
		deviceTopology         *machine.DeviceTopology
		expectedErr            bool
		expectedResp           *pluginapi.ResourceHintsResponse
	}{
		{
			name:          "cannot allocate to the same device name",
			podUID:        "test-pod",
			containerName: "test-container",
			req: &pluginapi.ResourceRequest{
				PodUid:        "test-pod",
				ContainerName: "test-container",
				ResourceRequests: map[string]float64{
					string(consts.ResourceGPUMemory): 8,
					"test-gpu":                       2,
				},
			},
			allocationResourcesMap: &state.AllocationResourcesMap{
				"gpu_device": {
					"gpu-0": {
						PodEntries: map[string]state.ContainerEntries{
							"pod1": {
								"container1": {
									DeviceName: "test-gpu",
									AllocatedAllocation: state.Allocation{
										Quantity: 1,
									},
								},
							},
						},
					},
				},
			},
			deviceTopology: &machine.DeviceTopology{
				Devices: map[string]machine.DeviceInfo{
					"gpu-0": {
						NumaNodes: []int{0},
						Health:    deviceplugin.Healthy,
					},
					"gpu-1": {
						NumaNodes: []int{1},
						Health:    deviceplugin.Healthy,
					},
					"gpu-2": {
						NumaNodes: []int{0},
						Health:    deviceplugin.Healthy,
					},
					"gpu-3": {
						NumaNodes: []int{1},
						Health:    deviceplugin.Healthy,
					},
				},
			},
			expectedResp: &pluginapi.ResourceHintsResponse{
				PodUid:        "test-pod",
				ContainerName: "test-container",
				ResourceName:  string(consts.ResourceGPUMemory),
				ResourceHints: map[string]*pluginapi.ListOfTopologyHints{
					string(consts.ResourceGPUMemory): {
						Hints: []*pluginapi.TopologyHint{
							{
								Nodes:     []uint64{1}, // numa node 1 is preferred as numa node 0 has same device that is already occupied
								Preferred: true,
							},
						},
					},
				},
				Labels: map[string]string{
					"katalyst.kubewharf.io/qos_level": "shared_cores",
				},
				Annotations: map[string]string{
					"katalyst.kubewharf.io/qos_level": "shared_cores",
				},
			},
		},
		{
			name:          "existing pod allocation",
			podUID:        "test-pod-1",
			containerName: "test-container-1",
			req: &pluginapi.ResourceRequest{
				PodUid:        "test-pod-1",
				ContainerName: "test-container-1",
				Annotations: map[string]string{
					"numa_binding": "true",
				},
				ResourceRequests: map[string]float64{
					string(consts.ResourceGPUMemory): 10,
					"test-gpu":                       2,
				},
			},
			allocationInfo: &state.AllocationInfo{
				AllocatedAllocation: state.Allocation{
					Quantity:  100,
					NUMANodes: []int{0, 1},
				},
			},
			expectedResp: &pluginapi.ResourceHintsResponse{
				PodUid:        "test-pod-1",
				ContainerName: "test-container-1",
				ResourceName:  string(consts.ResourceGPUMemory),
				ResourceHints: map[string]*pluginapi.ListOfTopologyHints{
					string(consts.ResourceGPUMemory): {
						Hints: []*pluginapi.TopologyHint{
							{
								Nodes:     []uint64{0, 1},
								Preferred: true,
							},
						},
					},
				},
				Labels: map[string]string{
					"katalyst.kubewharf.io/qos_level": "shared_cores",
				},
				Annotations: map[string]string{
					"katalyst.kubewharf.io/qos_level": "shared_cores",
				},
			},
		},
		{
			name:          "calculate a new hint when there is no existing allocation",
			podUID:        "test-pod-2",
			containerName: "test-container-2",
			req: &pluginapi.ResourceRequest{
				PodUid:        "test-pod-2",
				ContainerName: "test-container-2",
				Annotations: map[string]string{
					"numa_binding": "true",
				},
				ResourceRequests: map[string]float64{
					string(consts.ResourceGPUMemory): 4,
					"test-gpu":                       2,
				},
			},
			allocationResourcesMap: &state.AllocationResourcesMap{
				consts.ResourceGPUMemory: {
					"gpu-3": {
						PodEntries: map[string]state.ContainerEntries{
							"pod-3": {
								"container-3": {
									DeviceName: "another-gpu", // can be in the same gpu if the gpu is of a different device name
									AllocatedAllocation: state.Allocation{
										Quantity: 2,
									},
								},
							},
						},
					},
				},
			},
			deviceTopology: &machine.DeviceTopology{
				Devices: map[string]machine.DeviceInfo{
					"gpu-0": {
						NumaNodes: []int{0},
						Health:    deviceplugin.Healthy,
					},
					"gpu-1": {
						NumaNodes: []int{1},
						Health:    deviceplugin.Healthy,
					},
					"gpu-2": {
						NumaNodes: []int{0},
						Health:    deviceplugin.Healthy,
					},
					"gpu-3": {
						NumaNodes: []int{1},
						Health:    deviceplugin.Healthy,
					},
				},
			},
			expectedResp: &pluginapi.ResourceHintsResponse{
				PodUid:        "test-pod-2",
				ContainerName: "test-container-2",
				ResourceName:  string(consts.ResourceGPUMemory),
				ResourceHints: map[string]*pluginapi.ListOfTopologyHints{
					string(consts.ResourceGPUMemory): {
						Hints: []*pluginapi.TopologyHint{
							{
								Nodes:     []uint64{0},
								Preferred: false,
							},
							{
								Nodes:     []uint64{1}, // numa node 1 is preferred because it has already been allocated to another pod
								Preferred: true,
							},
						},
					},
				},
				Labels: map[string]string{
					"katalyst.kubewharf.io/qos_level": "shared_cores",
				},
				Annotations: map[string]string{
					"katalyst.kubewharf.io/qos_level": "shared_cores",
				},
			},
		},
		{
			name:          "not enough resources to allocate, no hints",
			podUID:        "test-pod-3",
			containerName: "test-container-3",
			req: &pluginapi.ResourceRequest{
				PodUid:        "test-pod-3",
				ContainerName: "test-container-3",
				Annotations: map[string]string{
					"numa_binding": "true",
				},
				ResourceRequests: map[string]float64{
					string(consts.ResourceGPUMemory): 100,
					"test-gpu":                       2,
				},
			},
			allocationResourcesMap: &state.AllocationResourcesMap{
				consts.ResourceGPUMemory: {
					"gpu-0": {
						PodEntries: map[string]state.ContainerEntries{
							"pod-2": {
								"container-2": {
									AllocatedAllocation: state.Allocation{
										Quantity: 4,
									},
								},
							},
						},
					},
					"gpu-1": {
						PodEntries: map[string]state.ContainerEntries{
							"pod-2": {
								"container-2": {
									AllocatedAllocation: state.Allocation{
										Quantity: 4,
									},
								},
							},
						},
					},
					"gpu-2": {
						PodEntries: map[string]state.ContainerEntries{
							"pod-2": {
								"container-2": {
									AllocatedAllocation: state.Allocation{
										Quantity: 4,
									},
								},
							},
						},
					},
					"gpu-3": {
						PodEntries: map[string]state.ContainerEntries{
							"pod-2": {
								"container-2": {
									AllocatedAllocation: state.Allocation{
										Quantity: 4,
									},
								},
							},
						},
					},
				},
			},
			deviceTopology: &machine.DeviceTopology{
				Devices: map[string]machine.DeviceInfo{
					"gpu-0": {
						NumaNodes: []int{0},
						Health:    deviceplugin.Healthy,
					},
					"gpu-1": {
						NumaNodes: []int{1},
						Health:    deviceplugin.Healthy,
					},
					"gpu-2": {
						NumaNodes: []int{0},
						Health:    deviceplugin.Healthy,
					},
					"gpu-3": {
						NumaNodes: []int{1},
						Health:    deviceplugin.Healthy,
					},
				},
			},
			expectedErr: true,
		},
		{
			name:          "multiple gpu resource names requested",
			podUID:        "test-pod-4",
			containerName: "test-container-4",
			req: &pluginapi.ResourceRequest{
				PodUid:        "test-pod-4",
				ContainerName: "test-container-4",
				ResourceRequests: map[string]float64{
					string(consts.ResourceGPUMemory): 4,
					"test-gpu-1":                     1,
					"test-gpu-2":                     1,
				},
			},
			expectedErr: true,
		},
		{
			name:          "packing mode - all resources agree on same numa",
			podUID:        "test-pod-pack",
			containerName: "test-container-pack",
			req: &pluginapi.ResourceRequest{
				PodUid:        "test-pod-pack",
				ContainerName: "test-container-pack",
				ResourceRequests: map[string]float64{
					string(consts.ResourceGPUMemory): 4,
					string(consts.ResourceMilliGPU):  500,
				},
			},
			allocationResourcesMap: &state.AllocationResourcesMap{
				consts.ResourceGPUMemory: {
					"gpu-0": {
						PodEntries: map[string]state.ContainerEntries{
							"pod-1": {
								"container-1": {
									AllocatedAllocation: state.Allocation{
										Quantity:  2,
										NUMANodes: []int{0},
									},
								},
							},
						},
					},
					"gpu-1": {
						PodEntries: map[string]state.ContainerEntries{},
					},
					"gpu-2": {
						PodEntries: map[string]state.ContainerEntries{},
					},
					"gpu-3": {
						PodEntries: map[string]state.ContainerEntries{},
					},
				},
				consts.ResourceMilliGPU: {
					"gpu-0": {
						PodEntries: map[string]state.ContainerEntries{
							"pod-1": {
								"container-1": {
									AllocatedAllocation: state.Allocation{
										Quantity:  500,
										NUMANodes: []int{0},
									},
								},
							},
						},
					},
					"gpu-1": {
						PodEntries: map[string]state.ContainerEntries{},
					},
					"gpu-2": {
						PodEntries: map[string]state.ContainerEntries{},
					},
					"gpu-3": {
						PodEntries: map[string]state.ContainerEntries{},
					},
				},
			},
			deviceTopology: &machine.DeviceTopology{
				Devices: map[string]machine.DeviceInfo{
					"gpu-0": {
						NumaNodes: []int{0},
						Health:    deviceplugin.Healthy,
					},
					"gpu-1": {
						NumaNodes: []int{1},
						Health:    deviceplugin.Healthy,
					},
					"gpu-2": {
						NumaNodes: []int{0},
						Health:    deviceplugin.Healthy,
					},
					"gpu-3": {
						NumaNodes: []int{1},
						Health:    deviceplugin.Healthy,
					},
				},
			},
			expectedResp: &pluginapi.ResourceHintsResponse{
				PodUid:        "test-pod-pack",
				ContainerName: "test-container-pack",
				ResourceName:  string(consts.ResourceGPUMemory),
				ResourceHints: map[string]*pluginapi.ListOfTopologyHints{
					string(consts.ResourceGPUMemory): {
						Hints: []*pluginapi.TopologyHint{
							{
								Nodes:     []uint64{0},
								Preferred: true,
							},
							{
								Nodes:     []uint64{1},
								Preferred: false,
							},
						},
					},
					string(consts.ResourceMilliGPU): {
						Hints: []*pluginapi.TopologyHint{
							{
								Nodes:     []uint64{0},
								Preferred: true,
							},
							{
								Nodes:     []uint64{1},
								Preferred: false,
							},
						},
					},
				},
				Labels: map[string]string{
					"katalyst.kubewharf.io/qos_level": "shared_cores",
				},
				Annotations: map[string]string{
					"katalyst.kubewharf.io/qos_level": "shared_cores",
				},
			},
		},
		{
			name:          "allocation info length mismatch (6a)",
			podUID:        "test-pod-mismatch",
			containerName: "test-container-mismatch",
			req: &pluginapi.ResourceRequest{
				PodUid:        "test-pod-mismatch",
				ContainerName: "test-container-mismatch",
				ResourceRequests: map[string]float64{
					string(consts.ResourceGPUMemory): 4,
					string(consts.ResourceMilliGPU):  500,
				},
			},
			allocationInfoMap: map[v1.ResourceName]*state.AllocationInfo{
				consts.ResourceGPUMemory: {
					AllocatedAllocation: state.Allocation{
						Quantity:  4,
						NUMANodes: []int{0},
					},
				}, // no ResourceMilliGPU allocation info!
			},
			expectedErr: true,
		},
		{
			name:          "all requested resources have allocation info (6c)",
			podUID:        "test-pod-all-ai",
			containerName: "test-container-all-ai",
			req: &pluginapi.ResourceRequest{
				PodUid:        "test-pod-all-ai",
				ContainerName: "test-container-all-ai",
				ResourceRequests: map[string]float64{
					string(consts.ResourceGPUMemory): 4,
					string(consts.ResourceMilliGPU):  500,
				},
			},
			allocationInfoMap: map[v1.ResourceName]*state.AllocationInfo{
				consts.ResourceGPUMemory: {
					AllocatedAllocation: state.Allocation{
						Quantity:  4,
						NUMANodes: []int{0},
					},
				},
				consts.ResourceMilliGPU: {
					AllocatedAllocation: state.Allocation{
						Quantity:  500,
						NUMANodes: []int{0},
					},
				},
			},
			expectedResp: &pluginapi.ResourceHintsResponse{
				PodUid:        "test-pod-all-ai",
				ContainerName: "test-container-all-ai",
				ResourceName:  string(consts.ResourceGPUMemory),
				ResourceHints: map[string]*pluginapi.ListOfTopologyHints{
					string(consts.ResourceGPUMemory): {
						Hints: []*pluginapi.TopologyHint{
							{
								Nodes:     []uint64{0},
								Preferred: true,
							},
						},
					},
					string(consts.ResourceMilliGPU): {
						Hints: []*pluginapi.TopologyHint{
							{
								Nodes:     []uint64{0},
								Preferred: true,
							},
						},
					},
				},
				Labels: map[string]string{
					"katalyst.kubewharf.io/qos_level": "shared_cores",
				},
				Annotations: map[string]string{
					"katalyst.kubewharf.io/qos_level": "shared_cores",
				},
			},
		},
		{
			name:          "no clear cut best numa across different resources",
			podUID:        "test-pod-no-clear-best",
			containerName: "test-container-no-clear-best",
			req: &pluginapi.ResourceRequest{
				PodUid:        "test-pod-no-clear-best",
				ContainerName: "test-container-no-clear-best",
				ResourceRequests: map[string]float64{
					string(consts.ResourceGPUMemory): 4,
					string(consts.ResourceMilliGPU):  500,
				},
			},
			allocationResourcesMap: &state.AllocationResourcesMap{
				"gpu_device": {},
				consts.ResourceGPUMemory: {
					"gpu-0": {
						PodEntries: map[string]state.ContainerEntries{
							"pod-1": {
								"container-1": {
									AllocatedAllocation: state.Allocation{
										Quantity:  2,
										NUMANodes: []int{0},
									},
								},
							},
						},
					},
					"gpu-1": {
						PodEntries: map[string]state.ContainerEntries{},
					},
					"gpu-2": {
						PodEntries: map[string]state.ContainerEntries{},
					},
					"gpu-3": {
						PodEntries: map[string]state.ContainerEntries{},
					},
				},
				consts.ResourceMilliGPU: {
					"gpu-0": {
						PodEntries: map[string]state.ContainerEntries{},
					},
					"gpu-1": {
						PodEntries: map[string]state.ContainerEntries{
							"pod-1": {
								"container-1": {
									AllocatedAllocation: state.Allocation{
										Quantity:  500,
										NUMANodes: []int{1},
									},
								},
							},
						},
					},
					"gpu-2": {
						PodEntries: map[string]state.ContainerEntries{},
					},
					"gpu-3": {
						PodEntries: map[string]state.ContainerEntries{},
					},
				},
			},
			deviceTopology: &machine.DeviceTopology{
				Devices: map[string]machine.DeviceInfo{
					"gpu-0": {
						NumaNodes: []int{0},
						Health:    deviceplugin.Healthy,
					},
					"gpu-1": {
						NumaNodes: []int{1},
						Health:    deviceplugin.Healthy,
					},
					"gpu-2": {
						NumaNodes: []int{0},
						Health:    deviceplugin.Healthy,
					},
					"gpu-3": {
						NumaNodes: []int{1},
						Health:    deviceplugin.Healthy,
					},
				},
			},
			expectedResp: &pluginapi.ResourceHintsResponse{
				PodUid:        "test-pod-no-clear-best",
				ContainerName: "test-container-no-clear-best",
				ResourceName:  string(consts.ResourceGPUMemory),
				ResourceHints: map[string]*pluginapi.ListOfTopologyHints{
					string(consts.ResourceGPUMemory): {
						Hints: []*pluginapi.TopologyHint{
							{
								Nodes:     []uint64{0},
								Preferred: true,
							},
							{
								Nodes:     []uint64{1},
								Preferred: true,
							},
						},
					},
					string(consts.ResourceMilliGPU): {
						Hints: []*pluginapi.TopologyHint{
							{
								Nodes:     []uint64{0},
								Preferred: true,
							},
							{
								Nodes:     []uint64{1},
								Preferred: true,
							},
						},
					},
				},
				Labels: map[string]string{
					"katalyst.kubewharf.io/qos_level": "shared_cores",
				},
				Annotations: map[string]string{
					"katalyst.kubewharf.io/qos_level": "shared_cores",
				},
			},
		},
		{
			name:          "milligpu requested with physical gpu (error)",
			podUID:        "test-pod-milligpu-with-gpu",
			containerName: "test-container-milligpu-with-gpu",
			req: &pluginapi.ResourceRequest{
				PodUid:        "test-pod-milligpu-with-gpu",
				ContainerName: "test-container-milligpu-with-gpu",
				ResourceRequests: map[string]float64{
					"test-gpu":                       1,
					string(consts.ResourceGPUMemory): 4,
					string(consts.ResourceMilliGPU):  500,
				},
			},
			deviceTopology: &machine.DeviceTopology{
				Devices: map[string]machine.DeviceInfo{
					"gpu-0": {
						NumaNodes: []int{0},
						Health:    deviceplugin.Healthy,
					},
					"gpu-1": {
						NumaNodes: []int{1},
						Health:    deviceplugin.Healthy,
					},
				},
			},
			expectedErr: true,
		},
		{
			name:          "per GPU requested quantity exceeds allocatable (error)",
			podUID:        "test-pod-too-much",
			containerName: "test-container-too-much",
			req: &pluginapi.ResourceRequest{
				PodUid:        "test-pod-too-much",
				ContainerName: "test-container-too-much",
				ResourceRequests: map[string]float64{
					"test-gpu":                       1,
					string(consts.ResourceGPUMemory): 20, // allocatable per GPU is 8
				},
			},
			deviceTopology: &machine.DeviceTopology{
				Devices: map[string]machine.DeviceInfo{
					"gpu-0": {
						NumaNodes: []int{0},
						Health:    deviceplugin.Healthy,
					},
				},
			},
			expectedErr: true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			basePlugin := makeTestBasePlugin(t)
			resourcePlugin := NewGPUComputePlugin(basePlugin)

			gpuComputePlugin, ok := resourcePlugin.(*GPUComputePlugin)
			assert.True(t, ok)

			if tt.allocationInfo != nil {
				basePlugin.GetState().SetAllocationInfo(consts.ResourceGPUMemory, tt.podUID, tt.containerName, tt.allocationInfo, false)
			}
			if tt.allocationInfoMap != nil {
				for resName, ai := range tt.allocationInfoMap {
					basePlugin.GetState().SetAllocationInfo(resName, tt.podUID, tt.containerName, ai, false)
				}
			}

			if tt.allocationResourcesMap != nil {
				basePlugin.GetState().SetMachineState(*tt.allocationResourcesMap, false)
			}

			if tt.deviceTopology != nil {
				err := basePlugin.DeviceTopologyRegistry.SetDeviceTopology("test-gpu", tt.deviceTopology)
				assert.NoError(t, err)
			}

			resp, err := gpuComputePlugin.GetTopologyHints(context.Background(), tt.req)
			if tt.expectedErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expectedResp, resp)
			}
		})
	}
}

func TestGPUComputePlugin_Allocate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                          string
		resourceReq                   *pluginapi.ResourceRequest
		deviceReq                     *pluginapi.DeviceRequest
		allocationInfo                *state.AllocationInfo
		allocationResourcesMap        *state.AllocationResourcesMap
		deviceTopology                *machine.DeviceTopology
		fractionalGPUPrefersSpreading bool
		expectedResp                  *pluginapi.ResourceAllocationResponse
		expectedErr                   bool
		expectedSelectedGPUID         string
	}{
		{
			name: "nil device request returns empty response",
			resourceReq: &pluginapi.ResourceRequest{
				PodUid:        "test-pod",
				ContainerName: "test-container",
				ResourceRequests: map[string]float64{
					string(consts.ResourceGPUMemory): 4,
					"test-gpu":                       2,
				},
				ContainerType: pluginapi.ContainerType_MAIN,
			},
			deviceReq: nil,
			expectedResp: &pluginapi.ResourceAllocationResponse{
				PodUid:        "test-pod",
				ContainerName: "test-container",
				ContainerType: pluginapi.ContainerType_MAIN,
				ResourceName:  string(consts.ResourceGPUMemory),
				Labels: map[string]string{
					"katalyst.kubewharf.io/qos_level": "shared_cores",
				},
				Annotations: map[string]string{
					"katalyst.kubewharf.io/qos_level": "shared_cores",
				},
			},
		},
		{
			name: "existing allocation",
			resourceReq: &pluginapi.ResourceRequest{
				ResourceName:  string(consts.ResourceGPUMemory),
				PodUid:        "test-pod1",
				ContainerName: "test-container1",
				ResourceRequests: map[string]float64{
					string(consts.ResourceGPUMemory): 4,
					"test-gpu":                       2,
				},
				ContainerType: pluginapi.ContainerType_MAIN,
			},
			deviceReq: nil,
			allocationInfo: &state.AllocationInfo{
				AllocatedAllocation: state.Allocation{
					Quantity: 4,
				},
			},
			expectedResp: &pluginapi.ResourceAllocationResponse{
				PodUid:        "test-pod1",
				ContainerName: "test-container1",
				ContainerType: pluginapi.ContainerType_MAIN,
				ResourceName:  string(consts.ResourceGPUMemory),
				AllocationResult: &pluginapi.ResourceAllocation{
					ResourceAllocation: map[string]*pluginapi.ResourceAllocationInfo{
						string(consts.ResourceGPUMemory): {
							IsNodeResource:    true,
							IsScalarResource:  true,
							AllocatedQuantity: 4,
							ResourceHints: &pluginapi.ListOfTopologyHints{
								Hints: []*pluginapi.TopologyHint{nil},
							},
						},
					},
				},
				Labels: map[string]string{
					"katalyst.kubewharf.io/qos_level": "shared_cores",
				},
				Annotations: map[string]string{
					"katalyst.kubewharf.io/qos_level": "shared_cores",
				},
			},
		},
		{
			name: "gpu topology does not exist",
			resourceReq: &pluginapi.ResourceRequest{
				ResourceName:  string(consts.ResourceGPUMemory),
				PodUid:        "test-pod1",
				ContainerName: "test-container1",
				ResourceRequests: map[string]float64{
					string(consts.ResourceGPUMemory): 4,
					"test-gpu":                       2,
				},
				ContainerType: pluginapi.ContainerType_MAIN,
			},
			deviceReq: &pluginapi.DeviceRequest{
				DeviceName:       "test-gpu",
				ReusableDevices:  nil,
				AvailableDevices: []string{"gpu-0", "gpu-1"},
			},
			expectedErr: true,
		},
		{
			name: "allocate with both gpu compute and milligpu request - spreading mode",
			resourceReq: &pluginapi.ResourceRequest{
				PodUid:        "test-pod-milligpu-spread",
				ContainerName: "test-container-milligpu-spread",
				ResourceRequests: map[string]float64{
					string(consts.ResourceGPUMemory): 2,
					string(consts.ResourceMilliGPU):  500,
				},
				ContainerType: pluginapi.ContainerType_MAIN,
			},
			deviceReq: nil,
			allocationResourcesMap: &state.AllocationResourcesMap{
				"gpu_device": {},
				consts.ResourceGPUMemory: {
					"gpu-0": {
						Allocatable: 4,
						PodEntries: map[string]state.ContainerEntries{
							"existing-pod": {
								"existing-container": &state.AllocationInfo{
									AllocationMeta: commonstate.AllocationMeta{
										PodUid:        "existing-pod",
										ContainerName: "existing-container",
									},
									AllocatedAllocation: state.Allocation{
										Quantity: 1,
									},
								},
							},
						},
					},
					"gpu-1": {
						Allocatable: 4,
						PodEntries:  map[string]state.ContainerEntries{},
					},
				},
				consts.ResourceMilliGPU: {
					"gpu-0": {
						Allocatable: 1000,
						PodEntries: map[string]state.ContainerEntries{
							"existing-pod": {
								"existing-container": &state.AllocationInfo{
									AllocationMeta: commonstate.AllocationMeta{
										PodUid:        "existing-pod",
										ContainerName: "existing-container",
									},
									AllocatedAllocation: state.Allocation{
										Quantity: 300,
									},
								},
							},
						},
					},
					"gpu-1": {
						Allocatable: 1000,
						PodEntries:  map[string]state.ContainerEntries{},
					},
				},
			},
			deviceTopology: &machine.DeviceTopology{
				Devices: map[string]machine.DeviceInfo{
					"gpu-0": {
						NumaNodes: []int{0},
						Health:    deviceplugin.Healthy,
					},
					"gpu-1": {
						NumaNodes: []int{1},
						Health:    deviceplugin.Healthy,
					},
				},
			},
			fractionalGPUPrefersSpreading: true,
			expectedResp: &pluginapi.ResourceAllocationResponse{
				PodUid:        "test-pod-milligpu-spread",
				PodNamespace:  "",
				PodName:       "",
				ContainerName: "test-container-milligpu-spread",
				ContainerType: pluginapi.ContainerType_MAIN,
				ResourceName:  string(consts.ResourceGPUMemory),
				AllocationResult: &pluginapi.ResourceAllocation{
					ResourceAllocation: map[string]*pluginapi.ResourceAllocationInfo{
						string(consts.ResourceGPUMemory): {
							IsNodeResource:    true,
							IsScalarResource:  true,
							AllocatedQuantity: 2,
							ResourceHints: &pluginapi.ListOfTopologyHints{
								Hints: []*pluginapi.TopologyHint{nil},
							},
						},
						string(consts.ResourceMilliGPU): {
							IsNodeResource:    true,
							IsScalarResource:  true,
							AllocatedQuantity: 500,
							ResourceHints: &pluginapi.ListOfTopologyHints{
								Hints: []*pluginapi.TopologyHint{nil},
							},
						},
					},
				},
				Labels: map[string]string{
					"katalyst.kubewharf.io/qos_level": "shared_cores",
				},
				Annotations: map[string]string{
					"katalyst.kubewharf.io/qos_level": "shared_cores",
				},
			},
			expectedSelectedGPUID: "gpu-1", // gpu-1 is chosen here
			expectedErr:           false,
		},
		{
			name: "allocate with both gpu compute and milligpu request - packing mode",
			resourceReq: &pluginapi.ResourceRequest{
				PodUid:        "test-pod-milligpu-pack",
				ContainerName: "test-container-milligpu-pack",
				ResourceRequests: map[string]float64{
					string(consts.ResourceGPUMemory): 2,
					string(consts.ResourceMilliGPU):  500,
				},
				ContainerType: pluginapi.ContainerType_MAIN,
			},
			deviceReq: nil,
			allocationResourcesMap: &state.AllocationResourcesMap{
				"gpu_device": {},
				consts.ResourceGPUMemory: {
					"gpu-0": {
						Allocatable: 4,
						PodEntries: map[string]state.ContainerEntries{
							"existing-pod": {
								"existing-container": &state.AllocationInfo{
									AllocationMeta: commonstate.AllocationMeta{
										PodUid:        "existing-pod",
										ContainerName: "existing-container",
									},
									AllocatedAllocation: state.Allocation{
										Quantity: 1,
									},
								},
							},
						},
					},
					"gpu-1": {
						Allocatable: 4,
						PodEntries:  map[string]state.ContainerEntries{},
					},
				},
				consts.ResourceMilliGPU: {
					"gpu-0": {
						Allocatable: 1000,
						PodEntries: map[string]state.ContainerEntries{
							"existing-pod": {
								"existing-container": &state.AllocationInfo{
									AllocationMeta: commonstate.AllocationMeta{
										PodUid:        "existing-pod",
										ContainerName: "existing-container",
									},
									AllocatedAllocation: state.Allocation{
										Quantity: 300,
									},
								},
							},
						},
					},
					"gpu-1": {
						Allocatable: 1000,
						PodEntries:  map[string]state.ContainerEntries{},
					},
				},
			},
			deviceTopology: &machine.DeviceTopology{
				Devices: map[string]machine.DeviceInfo{
					"gpu-0": {
						NumaNodes: []int{0},
						Health:    deviceplugin.Healthy,
					},
					"gpu-1": {
						NumaNodes: []int{1},
						Health:    deviceplugin.Healthy,
					},
				},
			},
			fractionalGPUPrefersSpreading: false,
			expectedResp: &pluginapi.ResourceAllocationResponse{
				PodUid:        "test-pod-milligpu-pack",
				PodNamespace:  "",
				PodName:       "",
				ContainerName: "test-container-milligpu-pack",
				ContainerType: pluginapi.ContainerType_MAIN,
				ResourceName:  string(consts.ResourceGPUMemory),
				AllocationResult: &pluginapi.ResourceAllocation{
					ResourceAllocation: map[string]*pluginapi.ResourceAllocationInfo{
						string(consts.ResourceGPUMemory): {
							IsNodeResource:    true,
							IsScalarResource:  true,
							AllocatedQuantity: 2,
							ResourceHints: &pluginapi.ListOfTopologyHints{
								Hints: []*pluginapi.TopologyHint{nil},
							},
						},
						string(consts.ResourceMilliGPU): {
							IsNodeResource:    true,
							IsScalarResource:  true,
							AllocatedQuantity: 500,
							ResourceHints: &pluginapi.ListOfTopologyHints{
								Hints: []*pluginapi.TopologyHint{nil},
							},
						},
					},
				},
				Labels: map[string]string{
					"katalyst.kubewharf.io/qos_level": "shared_cores",
				},
				Annotations: map[string]string{
					"katalyst.kubewharf.io/qos_level": "shared_cores",
				},
			},
			expectedSelectedGPUID: "gpu-0",
			expectedErr:           false,
		},
		{
			name: "allocate with no clear-cut GPU - no common GPU",
			resourceReq: &pluginapi.ResourceRequest{
				PodUid:        "test-pod-no-common",
				ContainerName: "test-container-no-common",
				ResourceRequests: map[string]float64{
					string(consts.ResourceGPUMemory): 1,
					string(consts.ResourceMilliGPU):  300,
				},
				ContainerType: pluginapi.ContainerType_MAIN,
			},
			deviceReq: nil,
			allocationResourcesMap: &state.AllocationResourcesMap{
				"gpu_device": {},
				consts.ResourceGPUMemory: {
					"gpu-0": {
						Allocatable: 4,
						PodEntries:  map[string]state.ContainerEntries{}, // most unallocated for GPU compute here
					},
					"gpu-1": {
						Allocatable: 4,
						PodEntries: map[string]state.ContainerEntries{
							"existing-pod": {
								"existing-container": &state.AllocationInfo{
									AllocationMeta: commonstate.AllocationMeta{
										PodUid:        "existing-pod",
										ContainerName: "existing-container",
									},
									AllocatedAllocation: state.Allocation{
										Quantity: 3,
									},
								},
							},
						},
					},
				},
				consts.ResourceMilliGPU: {
					"gpu-0": {
						Allocatable: 1000,
						PodEntries: map[string]state.ContainerEntries{
							"existing-pod": {
								"existing-container": &state.AllocationInfo{
									AllocationMeta: commonstate.AllocationMeta{
										PodUid:        "existing-pod",
										ContainerName: "existing-container",
									},
									AllocatedAllocation: state.Allocation{
										Quantity: 700,
									},
								},
							},
						},
					},
					"gpu-1": {
						Allocatable: 1000,
						PodEntries:  map[string]state.ContainerEntries{}, // most unallocated for milligpu here
					},
				},
			},
			deviceTopology: &machine.DeviceTopology{
				Devices: map[string]machine.DeviceInfo{
					"gpu-0": {
						NumaNodes: []int{0},
						Health:    deviceplugin.Healthy,
					},
					"gpu-1": {
						NumaNodes: []int{1},
						Health:    deviceplugin.Healthy,
					},
				},
			},
			fractionalGPUPrefersSpreading: true,
			expectedResp: &pluginapi.ResourceAllocationResponse{
				PodUid:        "test-pod-no-common",
				PodNamespace:  "",
				PodName:       "",
				ContainerName: "test-container-no-common",
				ContainerType: pluginapi.ContainerType_MAIN,
				ResourceName:  string(consts.ResourceGPUMemory),
				AllocationResult: &pluginapi.ResourceAllocation{
					ResourceAllocation: map[string]*pluginapi.ResourceAllocationInfo{
						string(consts.ResourceGPUMemory): {
							IsNodeResource:    true,
							IsScalarResource:  true,
							AllocatedQuantity: 1,
							ResourceHints: &pluginapi.ListOfTopologyHints{
								Hints: []*pluginapi.TopologyHint{nil},
							},
						},
						string(consts.ResourceMilliGPU): {
							IsNodeResource:    true,
							IsScalarResource:  true,
							AllocatedQuantity: 300,
							ResourceHints: &pluginapi.ListOfTopologyHints{
								Hints: []*pluginapi.TopologyHint{nil},
							},
						},
					},
				},
				Labels: map[string]string{
					"katalyst.kubewharf.io/qos_level": "shared_cores",
				},
				Annotations: map[string]string{
					"katalyst.kubewharf.io/qos_level": "shared_cores",
				},
			},
			expectedSelectedGPUID: "gpu-0", // should pick first from first resource's list!
			expectedErr:           false,
		},
		{
			name: "allocate filters out gpus with insufficient resources",
			resourceReq: &pluginapi.ResourceRequest{
				PodUid:        "test-pod-insufficient-filter",
				ContainerName: "test-container-insufficient-filter",
				ResourceRequests: map[string]float64{
					string(consts.ResourceGPUMemory): 3,
					string(consts.ResourceMilliGPU):  600,
				},
				ContainerType: pluginapi.ContainerType_MAIN,
			},
			deviceReq: nil,
			allocationResourcesMap: &state.AllocationResourcesMap{
				"gpu_device": {},
				consts.ResourceGPUMemory: {
					"gpu-0": {
						Allocatable: 10,
						PodEntries: map[string]state.ContainerEntries{
							"existing-pod": {
								"existing-container": &state.AllocationInfo{
									AllocationMeta: commonstate.AllocationMeta{
										PodUid:        "existing-pod",
										ContainerName: "existing-container",
									},
									AllocatedAllocation: state.Allocation{
										Quantity: 8,
									},
								},
							},
						},
					},
					"gpu-1": {
						Allocatable: 10,
						PodEntries:  map[string]state.ContainerEntries{},
					},
					"gpu-2": {
						Allocatable: 10,
						PodEntries:  map[string]state.ContainerEntries{},
					},
				},
				consts.ResourceMilliGPU: {
					"gpu-0": {
						Allocatable: 1000,
						PodEntries:  map[string]state.ContainerEntries{},
					},
					"gpu-1": {
						Allocatable: 1000,
						PodEntries: map[string]state.ContainerEntries{
							"existing-pod": {
								"existing-container": &state.AllocationInfo{
									AllocationMeta: commonstate.AllocationMeta{
										PodUid:        "existing-pod",
										ContainerName: "existing-container",
									},
									AllocatedAllocation: state.Allocation{
										Quantity: 500,
									},
								},
							},
						},
					},
					"gpu-2": {
						Allocatable: 1000,
						PodEntries:  map[string]state.ContainerEntries{},
					},
				},
			},
			deviceTopology: &machine.DeviceTopology{
				Devices: map[string]machine.DeviceInfo{
					"gpu-0": {
						NumaNodes: []int{0},
						Health:    deviceplugin.Healthy,
					},
					"gpu-1": {
						NumaNodes: []int{0},
						Health:    deviceplugin.Healthy,
					},
					"gpu-2": {
						NumaNodes: []int{1},
						Health:    deviceplugin.Healthy,
					},
				},
			},
			fractionalGPUPrefersSpreading: false,
			expectedSelectedGPUID:         "gpu-2", // gpu-0 lacks memory, gpu-1 lacks milligpu
			expectedErr:                   false,
		},
		{
			name: "allocate fails when all gpus have insufficient resources",
			resourceReq: &pluginapi.ResourceRequest{
				PodUid:        "test-pod-insufficient-fail",
				ContainerName: "test-container-insufficient-fail",
				ResourceRequests: map[string]float64{
					string(consts.ResourceGPUMemory): 3,
					string(consts.ResourceMilliGPU):  600,
				},
				ContainerType: pluginapi.ContainerType_MAIN,
			},
			deviceReq: nil,
			allocationResourcesMap: &state.AllocationResourcesMap{
				"gpu_device": {},
				consts.ResourceGPUMemory: {
					"gpu-0": {
						Allocatable: 10,
						PodEntries: map[string]state.ContainerEntries{
							"existing-pod": {
								"existing-container": &state.AllocationInfo{
									AllocationMeta: commonstate.AllocationMeta{
										PodUid:        "existing-pod",
										ContainerName: "existing-container",
									},
									AllocatedAllocation: state.Allocation{
										Quantity: 8,
									},
								},
							},
						},
					},
					"gpu-1": {
						Allocatable: 10,
						PodEntries:  map[string]state.ContainerEntries{},
					},
				},
				consts.ResourceMilliGPU: {
					"gpu-0": {
						Allocatable: 1000,
						PodEntries:  map[string]state.ContainerEntries{},
					},
					"gpu-1": {
						Allocatable: 1000,
						PodEntries: map[string]state.ContainerEntries{
							"existing-pod": {
								"existing-container": &state.AllocationInfo{
									AllocationMeta: commonstate.AllocationMeta{
										PodUid:        "existing-pod",
										ContainerName: "existing-container",
									},
									AllocatedAllocation: state.Allocation{
										Quantity: 500,
									},
								},
							},
						},
					},
				},
			},
			deviceTopology: &machine.DeviceTopology{
				Devices: map[string]machine.DeviceInfo{
					"gpu-0": {
						NumaNodes: []int{0},
						Health:    deviceplugin.Healthy,
					},
					"gpu-1": {
						NumaNodes: []int{0},
						Health:    deviceplugin.Healthy,
					},
				},
			},
			fractionalGPUPrefersSpreading: false,
			expectedErr:                   true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			basePlugin := makeTestBasePlugin(t)
			basePlugin.Conf.FractionalGPUPrefersSpreading = tt.fractionalGPUPrefersSpreading

			if tt.deviceTopology != nil {
				basePlugin.DeviceTopologyRegistry.SetDeviceTopology("test-gpu", tt.deviceTopology)
			}

			if tt.allocationResourcesMap != nil {
				basePlugin.GetState().SetMachineState(*tt.allocationResourcesMap, true)
			}

			resourcePlugin := NewGPUComputePlugin(basePlugin)

			gpuComputePlugin, ok := resourcePlugin.(*GPUComputePlugin)
			assert.True(t, ok)

			if tt.allocationInfo != nil {
				basePlugin.GetState().SetAllocationInfo(consts.ResourceGPUMemory, tt.resourceReq.PodUid, tt.resourceReq.ContainerName, tt.allocationInfo, false)
			}

			resp, err := gpuComputePlugin.Allocate(context.Background(), tt.resourceReq, tt.deviceReq)
			if tt.expectedErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				// For allocateWithoutGPUDevices test case with expectedResp, check the response
				if tt.expectedResp != nil {
					assert.Equal(t, tt.expectedResp, resp)
				} else if _, ok := tt.resourceReq.ResourceRequests[string(consts.ResourceMilliGPU)]; ok && tt.deviceReq == nil {
					// For other allocateWithoutGPUDevices test cases, check allocation info in state
					allocationInfoGPU := basePlugin.GetState().GetAllocationInfo(consts.ResourceGPUMemory, tt.resourceReq.PodUid, tt.resourceReq.ContainerName)
					allocationInfoMilliGPU := basePlugin.GetState().GetAllocationInfo(consts.ResourceMilliGPU, tt.resourceReq.PodUid, tt.resourceReq.ContainerName)
					assert.NotNil(t, allocationInfoGPU)
					assert.NotNil(t, allocationInfoMilliGPU)
				} else {
					// For regular test cases, check expected response
					assert.Equal(t, tt.expectedResp, resp)
				}

				// Check expected selected GPU ID if specified
				if tt.expectedSelectedGPUID != "" {
					allocationInfoGPU := basePlugin.GetState().GetAllocationInfo(consts.ResourceGPUMemory, tt.resourceReq.PodUid, tt.resourceReq.ContainerName)
					if assert.NotNil(t, allocationInfoGPU) {
						assert.Equal(t, 1, len(allocationInfoGPU.TopologyAwareAllocations))
						for gpuID := range allocationInfoGPU.TopologyAwareAllocations {
							assert.Equal(t, tt.expectedSelectedGPUID, gpuID)
						}
					}
				}
			}
		})
	}
}

func TestGPUComputePlugin_GetTopologyAwareResources(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		podUID         string
		containerName  string
		allocationInfo *state.AllocationInfo
		expectedResp   map[string]*pluginapi.GetTopologyAwareResourcesResponse
	}{
		{
			name:          "no existing allocation info",
			podUID:        "test-pod",
			containerName: "test-container",
			expectedResp:  nil,
		},
		{
			name:          "existing allocation info",
			podUID:        "test-pod1",
			containerName: "test-container1",
			allocationInfo: &state.AllocationInfo{
				AllocatedAllocation: state.Allocation{
					Quantity:  16,
					NUMANodes: []int{0, 1},
				},
				TopologyAwareAllocations: map[string]state.Allocation{
					"gpu-0": {
						Quantity:  4,
						NUMANodes: []int{0},
					},
					"gpu-1": {
						Quantity:  8,
						NUMANodes: []int{1},
					},
				},
			},
			expectedResp: map[string]*pluginapi.GetTopologyAwareResourcesResponse{
				string(consts.ResourceGPUMemory): {
					PodUid: "test-pod1",
					ContainerTopologyAwareResources: &pluginapi.ContainerTopologyAwareResources{
						ContainerName: "test-container1",
						AllocatedResources: map[string]*pluginapi.TopologyAwareResource{
							string(consts.ResourceGPUMemory): {
								IsNodeResource:             true,
								IsScalarResource:           true,
								AggregatedQuantity:         16,
								OriginalAggregatedQuantity: 16,
								TopologyAwareQuantityList: []*pluginapi.TopologyAwareQuantity{
									{
										ResourceValue: 4,
										Name:          "gpu-0",
										Type:          string(v1alpha1.TopologyTypeGPU),
										Annotations: map[string]string{
											consts.ResourceAnnotationKeyResourceIdentifier: "",
										},
										Node: 0,
									},
									{
										ResourceValue: 8,
										Name:          "gpu-1",
										Type:          string(v1alpha1.TopologyTypeGPU),
										Annotations: map[string]string{
											consts.ResourceAnnotationKeyResourceIdentifier: "",
										},
										Node: 1,
									},
								},
								OriginalTopologyAwareQuantityList: []*pluginapi.TopologyAwareQuantity{
									{
										ResourceValue: 4,
										Name:          "gpu-0",
										Type:          string(v1alpha1.TopologyTypeGPU),
										Annotations: map[string]string{
											consts.ResourceAnnotationKeyResourceIdentifier: "",
										},
										Node: 0,
									},
									{
										ResourceValue: 8,
										Name:          "gpu-1",
										Type:          string(v1alpha1.TopologyTypeGPU),
										Annotations: map[string]string{
											consts.ResourceAnnotationKeyResourceIdentifier: "",
										},
										Node: 1,
									},
								},
							},
						},
					},
				},
				string(consts.ResourceMilliGPU): {
					PodUid: "test-pod1",
					ContainerTopologyAwareResources: &pluginapi.ContainerTopologyAwareResources{
						ContainerName: "test-container1",
						AllocatedResources: map[string]*pluginapi.TopologyAwareResource{
							string(consts.ResourceMilliGPU): {
								IsNodeResource:             true,
								IsScalarResource:           true,
								AggregatedQuantity:         16,
								OriginalAggregatedQuantity: 16,
								TopologyAwareQuantityList: []*pluginapi.TopologyAwareQuantity{
									{
										ResourceValue: 4,
										Name:          "gpu-0",
										Type:          string(v1alpha1.TopologyTypeGPU),
										Annotations: map[string]string{
											consts.ResourceAnnotationKeyResourceIdentifier: "",
										},
										Node: 0,
									},
									{
										ResourceValue: 8,
										Name:          "gpu-1",
										Type:          string(v1alpha1.TopologyTypeGPU),
										Annotations: map[string]string{
											consts.ResourceAnnotationKeyResourceIdentifier: "",
										},
										Node: 1,
									},
								},
								OriginalTopologyAwareQuantityList: []*pluginapi.TopologyAwareQuantity{
									{
										ResourceValue: 4,
										Name:          "gpu-0",
										Type:          string(v1alpha1.TopologyTypeGPU),
										Annotations: map[string]string{
											consts.ResourceAnnotationKeyResourceIdentifier: "",
										},
										Node: 0,
									},
									{
										ResourceValue: 8,
										Name:          "gpu-1",
										Type:          string(v1alpha1.TopologyTypeGPU),
										Annotations: map[string]string{
											consts.ResourceAnnotationKeyResourceIdentifier: "",
										},
										Node: 1,
									},
								},
							},
						},
					},
				},
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			basePlugin := makeTestBasePlugin(t)
			resourcePlugin := NewGPUComputePlugin(basePlugin)

			gpuComputePlugin, ok := resourcePlugin.(*GPUComputePlugin)
			assert.True(t, ok)

			if tt.allocationInfo != nil {
				basePlugin.GetState().SetAllocationInfo(consts.ResourceGPUMemory, tt.podUID, tt.containerName, tt.allocationInfo, false)
				basePlugin.GetState().SetAllocationInfo(consts.ResourceMilliGPU, tt.podUID, tt.containerName, tt.allocationInfo, false)
			}

			resp, err := gpuComputePlugin.GetTopologyAwareResources(context.Background(), tt.podUID, tt.containerName)
			assert.NoError(t, err)
			assert.Empty(t,
				cmp.Diff(tt.expectedResp, resp,
					cmpopts.SortSlices(func(a, b *pluginapi.TopologyAwareQuantity) bool {
						return a.Name < b.Name
					}),
				),
			)
		})
	}
}

func TestGPUComputePlugin_GetTopologyAwareAllocatableResources(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		deviceTopology *machine.DeviceTopology
		expectedResp   map[string]*pluginapi.AllocatableTopologyAwareResource
		expectedErr    bool
	}{
		{
			name:        "gpu topology does not exist",
			expectedErr: true,
		},
		{
			name: "devices are numa aware",
			deviceTopology: &machine.DeviceTopology{
				Devices: map[string]machine.DeviceInfo{
					"gpu-0": {
						Health:    deviceplugin.Healthy,
						NumaNodes: []int{0},
					},
					"gpu-1": {
						Health:    deviceplugin.Healthy,
						NumaNodes: []int{1},
					},
				},
			},
			expectedResp: map[string]*pluginapi.AllocatableTopologyAwareResource{
				string(consts.ResourceGPUMemory): {
					IsNodeResource:                true,
					IsScalarResource:              true,
					AggregatedAllocatableQuantity: 20,
					TopologyAwareAllocatableQuantityList: []*pluginapi.TopologyAwareQuantity{
						{
							ResourceValue: 10,
							Name:          "gpu-0",
							Type:          string(v1alpha1.TopologyTypeGPU),
							Node:          0,
							Annotations: map[string]string{
								consts.ResourceAnnotationKeyResourceIdentifier: "",
							},
						},
						{
							ResourceValue: 10,
							Name:          "gpu-1",
							Type:          string(v1alpha1.TopologyTypeGPU),
							Node:          1,
							Annotations: map[string]string{
								consts.ResourceAnnotationKeyResourceIdentifier: "",
							},
						},
					},
					AggregatedCapacityQuantity: 20,
					TopologyAwareCapacityQuantityList: []*pluginapi.TopologyAwareQuantity{
						{
							ResourceValue: 10,
							Name:          "gpu-0",
							Type:          string(v1alpha1.TopologyTypeGPU),
							Node:          0,
							Annotations: map[string]string{
								consts.ResourceAnnotationKeyResourceIdentifier: "",
							},
						},
						{
							ResourceValue: 10,
							Name:          "gpu-1",
							Type:          string(v1alpha1.TopologyTypeGPU),
							Node:          1,
							Annotations: map[string]string{
								consts.ResourceAnnotationKeyResourceIdentifier: "",
							},
						},
					},
				},
				string(consts.ResourceMilliGPU): {
					IsNodeResource:                true,
					IsScalarResource:              true,
					AggregatedAllocatableQuantity: 2000, // 2 GPUs × 1000 milliGPU each
					TopologyAwareAllocatableQuantityList: []*pluginapi.TopologyAwareQuantity{
						{
							ResourceValue: 1000,
							Name:          "gpu-0",
							Type:          string(v1alpha1.TopologyTypeGPU),
							Node:          0,
							Annotations: map[string]string{
								consts.ResourceAnnotationKeyResourceIdentifier: "",
							},
						},
						{
							ResourceValue: 1000,
							Name:          "gpu-1",
							Type:          string(v1alpha1.TopologyTypeGPU),
							Node:          1,
							Annotations: map[string]string{
								consts.ResourceAnnotationKeyResourceIdentifier: "",
							},
						},
					},
					AggregatedCapacityQuantity: 2000,
					TopologyAwareCapacityQuantityList: []*pluginapi.TopologyAwareQuantity{
						{
							ResourceValue: 1000,
							Name:          "gpu-0",
							Type:          string(v1alpha1.TopologyTypeGPU),
							Node:          0,
							Annotations: map[string]string{
								consts.ResourceAnnotationKeyResourceIdentifier: "",
							},
						},
						{
							ResourceValue: 1000,
							Name:          "gpu-1",
							Type:          string(v1alpha1.TopologyTypeGPU),
							Node:          1,
							Annotations: map[string]string{
								consts.ResourceAnnotationKeyResourceIdentifier: "",
							},
						},
					},
				},
			},
		},
		{
			name: "devices are not numa aware",
			deviceTopology: &machine.DeviceTopology{
				Devices: map[string]machine.DeviceInfo{
					"gpu-0": {
						Health:    deviceplugin.Healthy,
						NumaNodes: []int{},
					},
					"gpu-1": {
						Health:    deviceplugin.Healthy,
						NumaNodes: []int{},
					},
				},
			},
			expectedResp: map[string]*pluginapi.AllocatableTopologyAwareResource{
				string(consts.ResourceGPUMemory): {
					IsNodeResource:                true,
					IsScalarResource:              true,
					AggregatedAllocatableQuantity: 20,
					TopologyAwareAllocatableQuantityList: []*pluginapi.TopologyAwareQuantity{
						{
							ResourceValue: 10,
							Name:          "gpu-0",
							Type:          string(v1alpha1.TopologyTypeGPU),
							Annotations: map[string]string{
								consts.ResourceAnnotationKeyResourceIdentifier: "",
							},
						},
						{
							ResourceValue: 10,
							Name:          "gpu-1",
							Type:          string(v1alpha1.TopologyTypeGPU),
							Annotations: map[string]string{
								consts.ResourceAnnotationKeyResourceIdentifier: "",
							},
						},
					},
					AggregatedCapacityQuantity: 20,
					TopologyAwareCapacityQuantityList: []*pluginapi.TopologyAwareQuantity{
						{
							ResourceValue: 10,
							Name:          "gpu-0",
							Type:          string(v1alpha1.TopologyTypeGPU),
							Annotations: map[string]string{
								consts.ResourceAnnotationKeyResourceIdentifier: "",
							},
						},
						{
							ResourceValue: 10,
							Name:          "gpu-1",
							Type:          string(v1alpha1.TopologyTypeGPU),
							Annotations: map[string]string{
								consts.ResourceAnnotationKeyResourceIdentifier: "",
							},
						},
					},
				},
				string(consts.ResourceMilliGPU): {
					IsNodeResource:                true,
					IsScalarResource:              true,
					AggregatedAllocatableQuantity: 2000, // 2 GPUs × 1000 milliGPU each
					TopologyAwareAllocatableQuantityList: []*pluginapi.TopologyAwareQuantity{
						{
							ResourceValue: 1000,
							Name:          "gpu-0",
							Type:          string(v1alpha1.TopologyTypeGPU),
							Annotations: map[string]string{
								consts.ResourceAnnotationKeyResourceIdentifier: "",
							},
						},
						{
							ResourceValue: 1000,
							Name:          "gpu-1",
							Type:          string(v1alpha1.TopologyTypeGPU),
							Annotations: map[string]string{
								consts.ResourceAnnotationKeyResourceIdentifier: "",
							},
						},
					},
					AggregatedCapacityQuantity: 2000,
					TopologyAwareCapacityQuantityList: []*pluginapi.TopologyAwareQuantity{
						{
							ResourceValue: 1000,
							Name:          "gpu-0",
							Type:          string(v1alpha1.TopologyTypeGPU),
							Annotations: map[string]string{
								consts.ResourceAnnotationKeyResourceIdentifier: "",
							},
						},
						{
							ResourceValue: 1000,
							Name:          "gpu-1",
							Type:          string(v1alpha1.TopologyTypeGPU),
							Annotations: map[string]string{
								consts.ResourceAnnotationKeyResourceIdentifier: "",
							},
						},
					},
				},
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			basePlugin := makeTestBasePlugin(t)
			resourcePlugin := NewGPUComputePlugin(basePlugin)

			gpuComputePlugin, ok := resourcePlugin.(*GPUComputePlugin)
			assert.True(t, ok)

			if tt.deviceTopology != nil {
				err := basePlugin.DeviceTopologyRegistry.SetDeviceTopology("test-gpu", tt.deviceTopology)
				assert.NoError(t, err)
			}

			resp, err := gpuComputePlugin.GetTopologyAwareAllocatableResources(context.Background())
			if tt.expectedErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Empty(t,
					cmp.Diff(tt.expectedResp, resp,
						cmpopts.SortSlices(func(a, b *pluginapi.TopologyAwareQuantity) bool {
							return a.Name < b.Name
						}),
					),
				)
			}
		})
	}
}

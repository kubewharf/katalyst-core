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

package gpumemory

import (
	"context"
	"testing"

	cadvisorapi "github.com/google/cadvisor/info/v1"
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/stretchr/testify/assert"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/runtime"
	deviceplugin "k8s.io/kubelet/pkg/apis/deviceplugin/v1alpha"
	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	"github.com/kubewharf/katalyst-api/pkg/apis/node/v1alpha1"
	"github.com/kubewharf/katalyst-api/pkg/consts"
	katalyst_base "github.com/kubewharf/katalyst-core/cmd/base"
	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/agent"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/baseplugin"
	gpuconsts "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/consts"
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
	conf.GPUMemoryAllocatablePerGPU = *resource.NewQuantity(4, resource.DecimalSI)

	basePlugin, err := baseplugin.NewBasePlugin(agentCtx, conf, metrics.DummyMetrics{})
	assert.NoError(t, err)

	gpu.NewGPUDevicePlugin(basePlugin)

	stateImpl, err := state.NewCheckpointState(conf.StateDirectoryConfiguration, conf.QRMPluginsConfiguration, "test", "test-policy", state.NewDefaultResourceStateGeneratorRegistry(), true, metrics.DummyMetrics{})
	assert.NoError(t, err)

	basePlugin.SetState(stateImpl)

	return basePlugin
}

func TestGPUMemPlugin_GetTopologyHints(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                   string
		podUID                 string
		containerName          string
		req                    *pluginapi.ResourceRequest
		allocationInfo         *state.AllocationInfo
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
					string(consts.ResourceGPUMemory): 4,
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
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			basePlugin := makeTestBasePlugin(t)
			resourcePlugin := NewGPUMemPlugin(basePlugin)

			gpuMemPlugin, ok := resourcePlugin.(*GPUMemPlugin)
			assert.True(t, ok)

			if tt.allocationInfo != nil {
				basePlugin.GetState().SetAllocationInfo(consts.ResourceGPUMemory, tt.podUID, tt.containerName, tt.allocationInfo, false)
			}

			if tt.allocationResourcesMap != nil {
				basePlugin.GetState().SetMachineState(*tt.allocationResourcesMap, false)
			}

			if tt.deviceTopology != nil {
				err := basePlugin.DeviceTopologyRegistry.SetDeviceTopology(gpuconsts.GPUDeviceType, tt.deviceTopology)
				assert.NoError(t, err)
			}

			resp, err := gpuMemPlugin.GetTopologyHints(context.Background(), tt.req)
			if tt.expectedErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expectedResp, resp)
			}
		})
	}
}

func TestGPUMemPlugin_Allocate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		resourceReq    *pluginapi.ResourceRequest
		deviceReq      *pluginapi.DeviceRequest
		allocationInfo *state.AllocationInfo
		expectedResp   *pluginapi.ResourceAllocationResponse
		expectedErr    bool
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
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			basePlugin := makeTestBasePlugin(t)
			resourcePlugin := NewGPUMemPlugin(basePlugin)

			gpuMemPlugin, ok := resourcePlugin.(*GPUMemPlugin)
			assert.True(t, ok)

			if tt.allocationInfo != nil {
				basePlugin.GetState().SetAllocationInfo(consts.ResourceGPUMemory, tt.resourceReq.PodUid, tt.resourceReq.ContainerName, tt.allocationInfo, false)
			}

			resp, err := gpuMemPlugin.Allocate(context.Background(), tt.resourceReq, tt.deviceReq)
			if tt.expectedErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expectedResp, resp)
			}
		})
	}
}

func TestGPUMemPlugin_GetTopologyAwareResources(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		podUID         string
		containerName  string
		allocationInfo *state.AllocationInfo
		expectedResp   *pluginapi.GetTopologyAwareResourcesResponse
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
			expectedResp: &pluginapi.GetTopologyAwareResourcesResponse{
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
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			basePlugin := makeTestBasePlugin(t)
			resourcePlugin := NewGPUMemPlugin(basePlugin)

			gpuMemPlugin, ok := resourcePlugin.(*GPUMemPlugin)
			assert.True(t, ok)

			if tt.allocationInfo != nil {
				basePlugin.GetState().SetAllocationInfo(consts.ResourceGPUMemory, tt.podUID, tt.containerName, tt.allocationInfo, false)
			}

			resp, err := gpuMemPlugin.GetTopologyAwareResources(context.Background(), tt.podUID, tt.containerName)
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

func TestGPUMemPlugin_GetTopologyAwareAllocatableResources(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		deviceTopology *machine.DeviceTopology
		expectedResp   *gpuconsts.AllocatableResource
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
			expectedResp: &gpuconsts.AllocatableResource{
				ResourceName: string(consts.ResourceGPUMemory),
				AllocatableTopologyAwareResource: &pluginapi.AllocatableTopologyAwareResource{
					IsNodeResource:                true,
					IsScalarResource:              true,
					AggregatedAllocatableQuantity: 8,
					TopologyAwareAllocatableQuantityList: []*pluginapi.TopologyAwareQuantity{
						{
							ResourceValue: 4,
							Name:          "gpu-0",
							Type:          string(v1alpha1.TopologyTypeGPU),
							Node:          0,
							Annotations: map[string]string{
								consts.ResourceAnnotationKeyResourceIdentifier: "",
							},
						},
						{
							ResourceValue: 4,
							Name:          "gpu-1",
							Type:          string(v1alpha1.TopologyTypeGPU),
							Node:          1,
							Annotations: map[string]string{
								consts.ResourceAnnotationKeyResourceIdentifier: "",
							},
						},
					},
					AggregatedCapacityQuantity: 8,
					TopologyAwareCapacityQuantityList: []*pluginapi.TopologyAwareQuantity{
						{
							ResourceValue: 4,
							Name:          "gpu-0",
							Type:          string(v1alpha1.TopologyTypeGPU),
							Node:          0,
							Annotations: map[string]string{
								consts.ResourceAnnotationKeyResourceIdentifier: "",
							},
						},
						{
							ResourceValue: 4,
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
			expectedResp: &gpuconsts.AllocatableResource{
				ResourceName: string(consts.ResourceGPUMemory),
				AllocatableTopologyAwareResource: &pluginapi.AllocatableTopologyAwareResource{
					IsNodeResource:                true,
					IsScalarResource:              true,
					AggregatedAllocatableQuantity: 8,
					TopologyAwareAllocatableQuantityList: []*pluginapi.TopologyAwareQuantity{
						{
							ResourceValue: 4,
							Name:          "gpu-0",
							Type:          string(v1alpha1.TopologyTypeGPU),
							Annotations: map[string]string{
								consts.ResourceAnnotationKeyResourceIdentifier: "",
							},
						},
						{
							ResourceValue: 4,
							Name:          "gpu-1",
							Type:          string(v1alpha1.TopologyTypeGPU),
							Annotations: map[string]string{
								consts.ResourceAnnotationKeyResourceIdentifier: "",
							},
						},
					},
					AggregatedCapacityQuantity: 8,
					TopologyAwareCapacityQuantityList: []*pluginapi.TopologyAwareQuantity{
						{
							ResourceValue: 4,
							Name:          "gpu-0",
							Type:          string(v1alpha1.TopologyTypeGPU),
							Annotations: map[string]string{
								consts.ResourceAnnotationKeyResourceIdentifier: "",
							},
						},
						{
							ResourceValue: 4,
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
			resourcePlugin := NewGPUMemPlugin(basePlugin)

			gpuMemPlugin, ok := resourcePlugin.(*GPUMemPlugin)
			assert.True(t, ok)

			if tt.deviceTopology != nil {
				err := basePlugin.DeviceTopologyRegistry.SetDeviceTopology(gpuconsts.GPUDeviceType, tt.deviceTopology)
				assert.NoError(t, err)
			}

			resp, err := gpuMemPlugin.GetTopologyAwareAllocatableResources(context.Background())
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

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

package rdma

import (
	"context"
	"fmt"
	"sort"
	"testing"

	cadvisorapi "github.com/google/cadvisor/info/v1"
	"github.com/stretchr/testify/assert"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/uuid"
	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	katalyst_base "github.com/kubewharf/katalyst-core/cmd/base"
	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/agent"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/baseplugin"
	gpuconsts "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/state"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/strategy/allocate/manager"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/strategy/allocate/strategies/accompanyresource"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/strategy/allocate/strategies/canonical"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/strategy/allocate/strategies/noop"
	gpuutil "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/util"
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
	conf.KubeletDevicePluginPath = tmpDir

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
	// Ensure both GPU and RDMA device names are configured so that
	// gpuutil.ResolveResourceName can resolve logical types in tests
	conf.GPUDeviceNames = []string{"test-gpu"}
	conf.RDMADeviceNames = []string{"test-rdma"}

	basePlugin, err := baseplugin.NewBasePlugin(agentCtx, conf, metrics.DummyMetrics{})
	assert.NoError(t, err)

	stateImpl, err := state.NewCheckpointState(conf.StateDirectoryConfiguration, conf.QRMPluginsConfiguration, "test", "test-policy", state.NewDefaultResourceStateGeneratorRegistry(), true, metrics.DummyMetrics{})
	assert.NoError(t, err)

	basePlugin.SetState(stateImpl)

	gpuTopologyProvider := machine.NewDeviceTopologyProvider()
	basePlugin.DeviceTopologyRegistry.RegisterDeviceTopologyProvider(gpuconsts.GPUDeviceType, gpuTopologyProvider)
	// Also register a provider for rdma device type to align with plugin lookup by type
	basePlugin.DeviceTopologyRegistry.RegisterDeviceTopologyProvider(gpuconsts.RDMADeviceType, machine.NewDeviceTopologyProvider())

	// Register GPU device names for resource name resolution in tests.
	basePlugin.RegisterDeviceNames(conf.GPUDeviceNames, gpuconsts.GPUDeviceType)

	return basePlugin
}

func TestRDMADevicePlugin_UpdateAllocatableAssociatedDevices(t *testing.T) {
	t.Parallel()

	basePlugin := makeTestBasePlugin(t)
	devicePlugin := NewRDMADevicePlugin(basePlugin)

	// Update topology with associated devices
	req := &pluginapi.UpdateAllocatableAssociatedDevicesRequest{
		DeviceName: "test-rdma",
		Devices: []*pluginapi.AssociatedDevice{
			{
				ID: "test-rdma-0",
				Topology: &pluginapi.TopologyInfo{
					Nodes: []*pluginapi.NUMANode{
						{
							ID: 0,
						},
					},
				},
			},
			{
				ID: "test-rdma-1",
				Topology: &pluginapi.TopologyInfo{
					Nodes: []*pluginapi.NUMANode{
						{
							ID: 1,
						},
					},
				},
			},
		},
	}

	resp, err := devicePlugin.UpdateAllocatableAssociatedDevices(context.Background(), req)
	assert.NoError(t, err)
	assert.NotNil(t, resp)

	// Verify device topology is updated
	gpuDevicePlugin := devicePlugin.(*RDMADevicePlugin)
	deviceTopology, err := gpuDevicePlugin.DeviceTopologyRegistry.GetDeviceTopology(basePlugin.Conf.RDMADeviceNames[0])
	assert.NoError(t, err)
	assert.NotNil(t, deviceTopology)

	expectedDeviceTopology := &machine.DeviceTopology{
		Devices: map[string]machine.DeviceInfo{
			"test-rdma-0": {
				NumaNodes:  []int{0},
				Dimensions: make(map[string]string),
			},
			"test-rdma-1": {
				NumaNodes:  []int{1},
				Dimensions: make(map[string]string),
			},
		},
	}

	// Compare device contents; ignore dynamic UpdateTime field
	assert.Equal(t, expectedDeviceTopology.Devices, deviceTopology.Devices)

	// An empty update clears the previous device topology.
	resp, err = devicePlugin.UpdateAllocatableAssociatedDevices(context.Background(), &pluginapi.UpdateAllocatableAssociatedDevicesRequest{
		DeviceName: "test-rdma",
	})
	assert.NoError(t, err)
	assert.NotNil(t, resp)

	deviceTopology, err = gpuDevicePlugin.DeviceTopologyRegistry.GetDeviceTopology(basePlugin.Conf.RDMADeviceNames[0])
	assert.NoError(t, err)
	assert.Empty(t, deviceTopology.Devices)
}

func TestRDMADevicePlugin_AllocateAssociatedDevices(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                            string
		podUID                          string
		containerName                   string
		allocationInfo                  *state.AllocationInfo
		accompanyResourceAllocationInfo *state.AllocationInfo
		accompanyResourceName           string
		deviceReq                       *pluginapi.DeviceRequest
		deviceTopology                  *machine.DeviceTopology
		accompanyDeviceTopology         *machine.DeviceTopology
		machineState                    state.AllocationResourcesMap
		expectedErr                     bool
		expectedResp                    *pluginapi.AssociatedDeviceAllocationResponse
		requestAnnotations              map[string]string
		keptAnnotationKeys              []string
		expectedAnnotations             map[string]string
		// isRandomAllocation is true if there is more than one possible allocation
		isRandomAllocation   bool
		expectedNumAllocated int
	}{
		{
			name: "Allocation already exists",
			allocationInfo: &state.AllocationInfo{
				AllocatedAllocation: state.Allocation{
					Quantity:  2,
					NUMANodes: []int{0, 1},
				},
				TopologyAwareAllocations: map[string]state.Allocation{
					"test-rdma-0": {
						Quantity:  1,
						NUMANodes: []int{0},
					},
					"test-rdma-1": {
						Quantity:  1,
						NUMANodes: []int{1},
					},
				},
			},
			podUID:        string(uuid.NewUUID()),
			containerName: "test-container",
			deviceReq: &pluginapi.DeviceRequest{
				DeviceName:       "test-rdma",
				AvailableDevices: []string{"test-rdma-2", "test-rdma-3"},
				ReusableDevices:  []string{"test-rdma-2", "test-rdma-3"},
				DeviceRequest:    2,
			},
			expectedResp: &pluginapi.AssociatedDeviceAllocationResponse{
				AllocationResult: &pluginapi.AssociatedDeviceAllocation{
					AllocatedDevices: []string{"test-rdma-0", "test-rdma-1"},
				},
			},
		},
		{
			name:          "No accompany resource allocates by best effort, allocate reusable devices first",
			podUID:        string(uuid.NewUUID()),
			containerName: "test-container",
			deviceTopology: &machine.DeviceTopology{
				Devices: map[string]machine.DeviceInfo{
					"test-rdma-0": {
						NumaNodes: []int{0},
					},
					"test-rdma-1": {
						NumaNodes: []int{1},
					},
					"test-rdma-2": {
						NumaNodes: []int{0},
					},
					"test-rdma-3": {
						NumaNodes: []int{1},
					},
				},
			},
			machineState: state.AllocationResourcesMap{
				gpuconsts.RDMADeviceType: {
					"test-rdma-0": {},
					"test-rdma-1": {},
					"test-rdma-2": {},
					"test-rdma-3": {},
				},
			},
			deviceReq: &pluginapi.DeviceRequest{
				DeviceName:       "test-rdma",
				ReusableDevices:  []string{"test-rdma-2", "test-rdma-3"},
				AvailableDevices: []string{"test-rdma-0", "test-rdma-1", "test-rdma-2", "test-rdma-3"},
				DeviceRequest:    2,
			},
			expectedResp: &pluginapi.AssociatedDeviceAllocationResponse{
				AllocationResult: &pluginapi.AssociatedDeviceAllocation{
					AllocatedDevices: []string{"test-rdma-2", "test-rdma-3"},
				},
			},
			requestAnnotations: map[string]string{"example.com/rdma-shared": "true", "example.com/ignored": "value"},
			keptAnnotationKeys: []string{"example.com/rdma-shared"},
			expectedAnnotations: map[string]string{
				"example.com/rdma-shared":         "true",
				"katalyst.kubewharf.io/qos_level": "shared_cores",
			},
		},
		{
			name:          "No accompany resource allocates by best effort, no reusable devices, only allocate available devices with NUMA affinity",
			podUID:        string(uuid.NewUUID()),
			containerName: "test-container",
			deviceTopology: &machine.DeviceTopology{
				Devices: map[string]machine.DeviceInfo{
					"test-rdma-0": {
						NumaNodes: []int{0},
					},
					"test-rdma-1": {
						NumaNodes: []int{1},
					},
					"test-rdma-2": {
						NumaNodes: []int{0},
					},
					"test-rdma-3": {
						NumaNodes: []int{1},
					},
				},
			},
			machineState: state.AllocationResourcesMap{
				gpuconsts.RDMADeviceType: {
					"test-rdma-0": {},
					"test-rdma-1": {},
					"test-rdma-2": {},
					"test-rdma-3": {},
				},
			},
			deviceReq: &pluginapi.DeviceRequest{
				DeviceName:       "test-rdma",
				ReusableDevices:  nil,
				AvailableDevices: []string{"test-rdma-0", "test-rdma-1", "test-rdma-2", "test-rdma-3"},
				DeviceRequest:    2,
				Hint: &pluginapi.TopologyHint{
					Nodes: []uint64{0},
				},
			},
			isRandomAllocation:   true,
			expectedNumAllocated: 2,
		},
		{
			name:          "Accompany resource has been allocated",
			podUID:        "test-pod",
			containerName: "test-container",
			deviceTopology: &machine.DeviceTopology{
				Devices: map[string]machine.DeviceInfo{
					"test-rdma-0": {
						NumaNodes: []int{0},
					},
					"test-rdma-1": {
						NumaNodes: []int{1},
					},
					"test-rdma-2": {
						NumaNodes: []int{0},
					},
					"test-rdma-3": {
						NumaNodes: []int{1},
					},
				},
			},
			// Ratio of 1 rdma device per 2 gpu devices
			accompanyDeviceTopology: &machine.DeviceTopology{
				Devices: map[string]machine.DeviceInfo{
					"test-gpu-0": {
						NumaNodes: []int{0},
					},
					"test-gpu-1": {
						NumaNodes: []int{1},
					},
					"test-gpu-2": {
						NumaNodes: []int{0},
					},
					"test-gpu-3": {
						NumaNodes: []int{1},
					},
					"test-gpu-4": {
						NumaNodes: []int{0},
					},
					"test-gpu-5": {
						NumaNodes: []int{1},
					},
					"test-gpu-6": {
						NumaNodes: []int{0},
					},
					"test-gpu-7": {
						NumaNodes: []int{1},
					},
				},
			},
			accompanyResourceName: "test-gpu",
			accompanyResourceAllocationInfo: &state.AllocationInfo{
				AllocatedAllocation: state.Allocation{
					Quantity: 4,
				},
				TopologyAwareAllocations: map[string]state.Allocation{
					"test-gpu-0": {
						Quantity: 1,
					},
					"test-gpu-2": {
						Quantity: 1,
					},
					"test-gpu-4": {
						Quantity: 1,
					},
					"test-gpu-6": {
						Quantity: 1,
					},
				},
			},
			machineState: state.AllocationResourcesMap{
				gpuconsts.RDMADeviceType: {
					"test-rdma-0": {},
					"test-rdma-1": {},
					"test-rdma-2": {},
					"test-rdma-3": {},
				},
				gpuconsts.GPUDeviceType: {
					"test-gpu-0": {
						PodEntries: map[string]state.ContainerEntries{
							"test-pod": {
								"test-container": {
									AllocatedAllocation: state.Allocation{
										Quantity: 1,
									},
								},
							},
						},
					},
					"test-gpu-1": {},
					"test-gpu-2": {
						PodEntries: map[string]state.ContainerEntries{
							"test-pod": {
								"test-container": {
									AllocatedAllocation: state.Allocation{
										Quantity: 1,
									},
								},
							},
						},
					},
					"test-gpu-3": {},
					"test-gpu-4": {
						PodEntries: map[string]state.ContainerEntries{
							"test-pod": {
								"test-container": {
									AllocatedAllocation: state.Allocation{
										Quantity: 1,
									},
								},
							},
						},
					},
					"test-gpu-5": {},
					"test-gpu-6": {
						PodEntries: map[string]state.ContainerEntries{
							"test-pod": {
								"test-container": {
									AllocatedAllocation: state.Allocation{
										Quantity: 1,
									},
								},
							},
						},
					},
					"test-gpu-7": {},
				},
			},
			deviceReq: &pluginapi.DeviceRequest{
				DeviceName:       "test-rdma",
				ReusableDevices:  nil,
				AvailableDevices: []string{"test-rdma-0", "test-rdma-1", "test-rdma-2", "test-rdma-3"},
				DeviceRequest:    2,
				Hint: &pluginapi.TopologyHint{
					Nodes: []uint64{0, 1},
				},
			},
			isRandomAllocation:   true,
			expectedNumAllocated: 2,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			basePlugin := makeTestBasePlugin(t)
			basePlugin.PodAnnotationKeptKeys = append(basePlugin.PodAnnotationKeptKeys, tt.keptAnnotationKeys...)
			devicePlugin := NewRDMADevicePlugin(basePlugin)

			if tt.allocationInfo != nil {
				basePlugin.GetState().SetAllocationInfo(gpuconsts.RDMADeviceType, tt.podUID, tt.containerName, tt.allocationInfo, false)
			}

			if tt.accompanyResourceAllocationInfo != nil && tt.accompanyResourceName != "" {
				accompanyResourceType := gpuutil.ResolveResourceName(basePlugin.GetDeviceNameToTypeMap(), tt.accompanyResourceName, false)
				assert.NotEmpty(t, accompanyResourceType)
				basePlugin.GetState().SetAllocationInfo(v1.ResourceName(accompanyResourceType), tt.podUID, tt.containerName, tt.accompanyResourceAllocationInfo, false)
			}

			if tt.deviceTopology != nil {
				// Set topology under both type and specific device name to satisfy plugin + generator lookups
				err := basePlugin.DeviceTopologyRegistry.SetDeviceTopology(gpuconsts.RDMADeviceType, tt.deviceTopology)
				assert.NoError(t, err)
				err = basePlugin.DeviceTopologyRegistry.SetDeviceTopology(basePlugin.Conf.RDMADeviceNames[0], tt.deviceTopology)
				assert.NoError(t, err)
			}

			if tt.accompanyResourceName != "" && tt.accompanyDeviceTopology != nil {
				accompanyResourceType := gpuutil.ResolveResourceName(basePlugin.GetDeviceNameToTypeMap(), tt.accompanyResourceName, false)
				assert.NotEmpty(t, accompanyResourceType)
				err := basePlugin.DeviceTopologyRegistry.SetDeviceTopology(accompanyResourceType, tt.accompanyDeviceTopology)
				assert.NoError(t, err)
			}

			if tt.machineState != nil {
				basePlugin.GetState().SetMachineState(tt.machineState, false)
			}

			resourceReq := &pluginapi.ResourceRequest{
				PodUid:        tt.podUID,
				ContainerName: tt.containerName,
				Annotations:   tt.requestAnnotations,
			}

			resp, err := devicePlugin.AllocateAssociatedDevice(context.Background(), resourceReq, tt.deviceReq, tt.accompanyResourceName)
			if tt.expectedErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				if tt.isRandomAllocation {
					assert.Equal(t, tt.expectedNumAllocated, len(resp.AllocationResult.AllocatedDevices))
				} else {
					evaluateAllocatedDevicesResult(t, tt.expectedResp, resp)
				}

				// Verify state is updated
				allocationInfo := basePlugin.GetState().GetAllocationInfo(gpuconsts.RDMADeviceType, tt.podUID, tt.containerName)
				assert.NotNil(t, allocationInfo)
				assert.Equal(t, float64(len(resp.AllocationResult.AllocatedDevices)), allocationInfo.AllocatedAllocation.Quantity)
				if tt.expectedAnnotations != nil {
					assert.Equal(t, tt.expectedAnnotations, allocationInfo.Annotations)
				}
			}
		})
	}
}

func TestRDMADevicePlugin_HybridClassicTopologyMixedAllocations(t *testing.T) {
	t.Parallel()

	const sharedAnnotationKey = "example.com/rdma-shared"
	type allocationStep struct {
		name          string
		gpus          []int
		shared        bool
		expectedRDMAs []int
		expectError   bool
	}
	tests := []struct {
		name      string
		gpuCount  int
		rdmaCount int
		steps     []allocationStep
	}{
		{
			name:      "8-gpu-2-rdma",
			gpuCount:  8,
			rdmaCount: 2,
			steps: []allocationStep{
				{name: "shared-free", gpus: []int{0}, shared: true, expectedRDMAs: []int{0}},
				{name: "exclusive-free", gpus: []int{4, 5}, expectedRDMAs: []int{1}},
				{name: "shared-reuse", gpus: []int{1}, shared: true, expectedRDMAs: []int{0}},
				{name: "exclusive-exhausted", gpus: []int{2}, expectError: true},
				{name: "shared-rejects-exclusive", gpus: []int{6}, shared: true, expectedRDMAs: []int{}},
			},
		},
		{
			name:      "8-gpu-4-rdma",
			gpuCount:  8,
			rdmaCount: 4,
			steps: []allocationStep{
				{name: "shared-free", gpus: []int{0}, shared: true, expectedRDMAs: []int{0}},
				{name: "exclusive-first", gpus: []int{2, 3}, expectedRDMAs: []int{1}},
				{name: "shared-reuse", gpus: []int{1}, shared: true, expectedRDMAs: []int{0}},
				{name: "exclusive-second", gpus: []int{4}, expectedRDMAs: []int{2}},
				{name: "exclusive-third", gpus: []int{6}, expectedRDMAs: []int{3}},
				{name: "exclusive-exhausted", gpus: []int{5}, expectError: true},
				{name: "shared-rejects-exclusive", gpus: []int{7}, shared: true, expectedRDMAs: []int{}},
			},
		},
		{
			name:      "8-gpu-8-rdma",
			gpuCount:  8,
			rdmaCount: 8,
			steps: []allocationStep{
				{name: "shared-free", gpus: []int{0}, shared: true, expectedRDMAs: []int{0}},
				{name: "exclusive-pair", gpus: []int{1, 2}, expectedRDMAs: []int{1, 2}},
				{name: "shared-free-fallback", gpus: []int{3}, shared: true, expectedRDMAs: []int{3}},
				{name: "exclusive-fill", gpus: []int{4, 5, 6, 7}, expectedRDMAs: []int{4, 5, 6, 7}},
			},
		},
		{
			name:      "16-gpu-4-rdma",
			gpuCount:  16,
			rdmaCount: 4,
			steps: []allocationStep{
				{name: "shared-free", gpus: []int{0}, shared: true, expectedRDMAs: []int{0}},
				{name: "exclusive-first", gpus: []int{4, 5}, expectedRDMAs: []int{1}},
				{name: "shared-reuse", gpus: []int{1}, shared: true, expectedRDMAs: []int{0}},
				{name: "exclusive-fill", gpus: []int{8, 9, 10, 11, 12, 13, 14, 15}, expectedRDMAs: []int{2, 3}},
				{name: "exclusive-exhausted", gpus: []int{2}, expectError: true},
				{name: "shared-rejects-exclusive", gpus: []int{6}, shared: true, expectedRDMAs: []int{}},
			},
		},
		{
			name:      "16-gpu-8-rdma",
			gpuCount:  16,
			rdmaCount: 8,
			steps: []allocationStep{
				{name: "shared-free", gpus: []int{0}, shared: true, expectedRDMAs: []int{0}},
				{name: "exclusive-first", gpus: []int{2, 3}, expectedRDMAs: []int{1}},
				{name: "shared-reuse", gpus: []int{1}, shared: true, expectedRDMAs: []int{0}},
				{name: "exclusive-middle", gpus: []int{4, 5, 6, 7, 8, 9, 10, 11}, expectedRDMAs: []int{2, 3, 4, 5}},
				{name: "exclusive-sixth", gpus: []int{12}, expectedRDMAs: []int{6}},
				{name: "exclusive-seventh", gpus: []int{14}, expectedRDMAs: []int{7}},
				{name: "exclusive-exhausted", gpus: []int{13}, expectError: true},
				{name: "shared-rejects-exclusive", gpus: []int{15}, shared: true, expectedRDMAs: []int{}},
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			basePlugin := makeTestBasePlugin(t)
			basePlugin.Conf.EnableHybridAccompanyResourceAllocation = true
			basePlugin.Conf.AccompanyResourceSharedAnnotationKey = sharedAnnotationKey
			basePlugin.PodAnnotationKeptKeys = append(basePlugin.PodAnnotationKeptKeys, sharedAnnotationKey)
			devicePlugin := NewRDMADevicePlugin(basePlugin).(*RDMADevicePlugin)
			registerTestRDMAAccompanyStrategy(t, basePlugin)
			installClassicPluginTopology(t, basePlugin, tt.gpuCount, tt.rdmaCount)

			for i, step := range tt.steps {
				step := step
				t.Run(step.name, func(t *testing.T) {
					podUID := fmt.Sprintf("%s-pod-%d", tt.name, i)
					containerName := "container"
					seedGPUAllocation(t, basePlugin, podUID, containerName, step.gpus)
					available := availableRDMADevices(t, basePlugin, tt.rdmaCount)
					expected := rdmaDeviceIDs(step.expectedRDMAs)
					annotations := map[string]string{}
					if step.shared {
						annotations[sharedAnnotationKey] = "true"
					}
					requestedRDMAs := (len(step.gpus)*tt.rdmaCount + tt.gpuCount - 1) / tt.gpuCount
					previouslyOwned := make(map[string]bool, len(expected))

					for _, id := range expected {
						if rdmaHasOwners(basePlugin.GetState().GetMachineState()[gpuconsts.RDMADeviceType][id]) {
							previouslyOwned[id] = true
							assert.NotContains(t, available, id)
						}
					}

					resp, err := devicePlugin.AllocateAssociatedDevice(
						context.Background(),
						&pluginapi.ResourceRequest{
							PodUid:        podUID,
							ContainerName: containerName,
							Annotations:   annotations,
						},
						&pluginapi.DeviceRequest{
							DeviceName:       "test-rdma",
							AvailableDevices: available,
							DeviceRequest:    uint64(requestedRDMAs),
						},
						"test-gpu",
					)
					if step.expectError {
						assert.Error(t, err)
						assert.Nil(t, resp)
						assert.Nil(t, basePlugin.GetState().GetAllocationInfo(
							gpuconsts.RDMADeviceType, podUID, containerName))
						assertRDMAOwnership(t, basePlugin, sharedAnnotationKey)
						return
					}

					assert.NoError(t, err)
					if !assert.NotNil(t, resp) {
						return
					}
					assert.ElementsMatch(t, expected, resp.AllocationResult.AllocatedDevices)
					allocationInfo := basePlugin.GetState().GetAllocationInfo(
						gpuconsts.RDMADeviceType, podUID, containerName)
					assert.NotNil(t, allocationInfo)
					assert.Equal(t, float64(len(expected)), allocationInfo.AllocatedAllocation.Quantity)
					assert.ElementsMatch(t, expected, allocationKeys(allocationInfo.TopologyAwareAllocations))
					if step.shared {
						assert.Equal(t, "true", allocationInfo.Annotations[sharedAnnotationKey])
					} else {
						assert.NotEqual(t, "true", allocationInfo.Annotations[sharedAnnotationKey])
					}
					assertRDMAOwnership(t, basePlugin, sharedAnnotationKey)
					for id := range previouslyOwned {
						assert.True(t, step.shared)
						assert.GreaterOrEqual(t, rdmaOwnerCount(
							basePlugin.GetState().GetMachineState()[gpuconsts.RDMADeviceType][id]), 2)
					}
				})
			}
		})
	}
}

func evaluateAllocatedDevicesResult(t *testing.T, expectedResp, actualResp *pluginapi.AssociatedDeviceAllocationResponse) {
	if expectedResp.AllocationResult == nil && actualResp.AllocationResult == nil {
		return
	}

	if expectedResp.AllocationResult != nil && actualResp.AllocationResult == nil {
		t.Errorf("expected allocation result %v, but got nil", expectedResp.AllocationResult)
		return
	}

	if actualResp.AllocationResult != nil && expectedResp.AllocationResult == nil {
		t.Errorf("expected nil allocation result, but got %v", actualResp.AllocationResult)
		return
	}

	assert.ElementsMatch(t, expectedResp.AllocationResult.AllocatedDevices, actualResp.AllocationResult.AllocatedDevices)
}

func installClassicPluginTopology(t *testing.T, basePlugin *baseplugin.BasePlugin, gpuCount, rdmaCount int) {
	t.Helper()

	assert.Zero(t, gpuCount%2)
	assert.Zero(t, rdmaCount%2)
	assert.Zero(t, gpuCount%rdmaCount)

	priorityDimensions := []string{"pcie", machine.DimensionNuma}
	gpuTopology := &machine.DeviceTopology{
		PriorityDimensions: append([]string(nil), priorityDimensions...),
		Devices:            make(map[string]machine.DeviceInfo, gpuCount),
	}
	rdmaTopology := &machine.DeviceTopology{
		PriorityDimensions: append([]string(nil), priorityDimensions...),
		Devices:            make(map[string]machine.DeviceInfo, rdmaCount),
	}
	machineState := state.AllocationResourcesMap{
		gpuconsts.GPUDeviceType:  make(state.AllocationMap, gpuCount),
		gpuconsts.RDMADeviceType: make(state.AllocationMap, rdmaCount),
	}
	gpusPerRDMA := gpuCount / rdmaCount
	gpusByPCIe := make(map[string]int, rdmaCount)
	gpusByNUMA := make(map[int]int, 2)
	rdmasByNUMA := make(map[int]int, 2)

	for i := 0; i < gpuCount; i++ {
		id := fmt.Sprintf("test-gpu-%d", i)
		numa := i / (gpuCount / 2)
		pcie := fmt.Sprintf("pcie-%d", i/gpusPerRDMA)
		gpuTopology.Devices[id] = machine.DeviceInfo{
			NumaNodes: []int{numa},
			Dimensions: map[string]string{
				"pcie":                pcie,
				machine.DimensionNuma: fmt.Sprintf("numa-%d", numa),
			},
		}
		gpusByPCIe[pcie]++
		gpusByNUMA[numa]++
		machineState[gpuconsts.GPUDeviceType][id] = &state.AllocationState{PodEntries: state.PodEntries{}}
	}
	for i := 0; i < rdmaCount; i++ {
		id := fmt.Sprintf("test-rdma-%d", i)
		numa := i / (rdmaCount / 2)
		rdmaTopology.Devices[id] = machine.DeviceInfo{
			NumaNodes: []int{numa},
			Dimensions: map[string]string{
				"pcie":                fmt.Sprintf("pcie-%d", i),
				machine.DimensionNuma: fmt.Sprintf("numa-%d", numa),
			},
		}
		rdmasByNUMA[numa]++
		machineState[gpuconsts.RDMADeviceType][id] = &state.AllocationState{PodEntries: state.PodEntries{}}
	}

	assert.Len(t, gpuTopology.Devices, gpuCount)
	assert.Len(t, rdmaTopology.Devices, rdmaCount)
	assert.Equal(t, priorityDimensions, gpuTopology.PriorityDimensions)
	assert.Equal(t, priorityDimensions, rdmaTopology.PriorityDimensions)
	assert.Equal(t, map[int]int{0: gpuCount / 2, 1: gpuCount / 2}, gpusByNUMA)
	assert.Equal(t, map[int]int{0: rdmaCount / 2, 1: rdmaCount / 2}, rdmasByNUMA)
	assert.Len(t, gpusByPCIe, rdmaCount)
	for _, count := range gpusByPCIe {
		assert.Equal(t, gpusPerRDMA, count)
	}

	basePlugin.DeviceTopologyRegistry.RegisterDeviceTopologyProvider("test-gpu", machine.NewDeviceTopologyProvider())
	assert.NoError(t, basePlugin.DeviceTopologyRegistry.SetDeviceTopology("test-gpu", gpuTopology))
	assert.NoError(t, basePlugin.DeviceTopologyRegistry.SetDeviceTopology(gpuconsts.GPUDeviceType, gpuTopology))
	assert.NoError(t, basePlugin.DeviceTopologyRegistry.SetDeviceTopology("test-rdma", rdmaTopology))
	assert.NoError(t, basePlugin.DeviceTopologyRegistry.SetDeviceTopology(gpuconsts.RDMADeviceType, rdmaTopology))
	basePlugin.GetState().SetMachineState(machineState, false)
}

func registerTestRDMAAccompanyStrategy(t *testing.T, basePlugin *baseplugin.BasePlugin) {
	t.Helper()

	const strategyName = "test-rdma-accompany"
	strategyManager := manager.GetGlobalStrategyManager(basePlugin.Conf.GPUQRMPluginConfig)
	err := strategyManager.RegisterGenericAllocationStrategy(
		strategyName,
		[]string{canonical.StrategyNameCanonical},
		noop.StrategyNameNoop,
		accompanyresource.StrategyNameAccompanyResource,
	)
	if err != nil {
		assert.ErrorContains(t, err, "allocation strategy with name "+strategyName+" already registered")
	}
	basePlugin.Conf.CustomAllocationStrategy["test-rdma"] = strategyName
}

func seedGPUAllocation(
	t *testing.T,
	basePlugin *baseplugin.BasePlugin,
	podUID, containerName string,
	gpus []int,
) {
	t.Helper()

	machineState := basePlugin.GetState().GetMachineState()
	for _, gpu := range gpus {
		id := fmt.Sprintf("test-gpu-%d", gpu)
		deviceState := machineState[gpuconsts.GPUDeviceType][id]
		assert.NotNil(t, deviceState)
		assert.Empty(t, deviceState.PodEntries)
		deviceState.PodEntries = state.PodEntries{
			podUID: state.ContainerEntries{
				containerName: &state.AllocationInfo{
					AllocatedAllocation: state.Allocation{Quantity: 1},
					TopologyAwareAllocations: map[string]state.Allocation{
						id: {Quantity: 1},
					},
				},
			},
		}
	}
	basePlugin.GetState().SetMachineState(machineState, false)
}

func availableRDMADevices(t *testing.T, basePlugin *baseplugin.BasePlugin, rdmaCount int) []string {
	t.Helper()

	machineState := basePlugin.GetState().GetMachineState()
	available := make([]string, 0, rdmaCount)
	for i := 0; i < rdmaCount; i++ {
		id := fmt.Sprintf("test-rdma-%d", i)
		deviceState, ok := machineState[gpuconsts.RDMADeviceType][id]
		assert.True(t, ok)
		if rdmaHasOwners(deviceState) {
			assert.NotContains(t, available, id)
		} else {
			available = append(available, id)
		}
	}
	return available
}

func rdmaHasOwners(deviceState *state.AllocationState) bool {
	return deviceState != nil && len(deviceState.PodEntries) > 0
}

func rdmaOwnerCount(deviceState *state.AllocationState) int {
	if deviceState == nil {
		return 0
	}

	var owners int
	for _, containers := range deviceState.PodEntries {
		owners += len(containers)
	}
	return owners
}

func rdmaDeviceIDs(indices []int) []string {
	ids := make([]string, len(indices))
	for i, index := range indices {
		ids[i] = fmt.Sprintf("test-rdma-%d", index)
	}
	return ids
}

func allocationKeys(allocations map[string]state.Allocation) []string {
	keys := make([]string, 0, len(allocations))
	for id := range allocations {
		keys = append(keys, id)
	}
	sort.Strings(keys)
	return keys
}

func assertRDMAOwnership(t *testing.T, basePlugin *baseplugin.BasePlugin, sharedAnnotationKey string) {
	t.Helper()

	for id, deviceState := range basePlugin.GetState().GetMachineState()[gpuconsts.RDMADeviceType] {
		if !rdmaHasOwners(deviceState) {
			continue
		}

		var sharedOwners, exclusiveOwners int
		for _, containers := range deviceState.PodEntries {
			for _, allocationInfo := range containers {
				if allocationInfo.Annotations[sharedAnnotationKey] == "true" {
					sharedOwners++
				} else {
					exclusiveOwners++
				}
			}
		}
		assert.False(t, sharedOwners > 0 && exclusiveOwners > 0, "mixed ownership on %s", id)
		if exclusiveOwners > 0 {
			assert.Equal(t, 1, exclusiveOwners, "multiple exclusive owners on %s", id)
		}
	}
}

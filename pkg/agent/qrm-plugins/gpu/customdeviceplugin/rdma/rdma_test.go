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
	"testing"

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
	"github.com/kubewharf/katalyst-core/pkg/config"
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
	conf.GenericQRMPluginConfiguration.StateFileDirectory = tmpDir
	conf.RDMADeviceNames = []string{"test-rdma"}

	basePlugin, err := baseplugin.NewBasePlugin(agentCtx, conf, metrics.DummyMetrics{})
	assert.NoError(t, err)

	stateImpl, err := state.NewCheckpointState(conf.QRMPluginsConfiguration, tmpDir, "test", "test-policy", state.NewDefaultResourceStateGeneratorRegistry(), true, metrics.DummyMetrics{})
	assert.NoError(t, err)

	basePlugin.State = stateImpl

	// Register gpu device type and gpu device topology provider as it is an accompany resource for rdma
	basePlugin.RegisterDeviceNameToType([]string{"test-gpu"}, gpuconsts.GPUDeviceType)
	gpuTopologyProvider := machine.NewDeviceTopologyProvider([]string{"test-gpu"})
	basePlugin.DeviceTopologyRegistry.RegisterDeviceTopologyProvider(gpuconsts.GPUDeviceType, gpuTopologyProvider)

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
	deviceTopology, numaTopologyReady, err := gpuDevicePlugin.DeviceTopologyRegistry.GetDeviceTopology(gpuconsts.RDMADeviceType)
	assert.NoError(t, err)
	assert.True(t, numaTopologyReady)
	assert.NotNil(t, deviceTopology)

	expectedDeviceTopology := &machine.DeviceTopology{
		Devices: map[string]machine.DeviceInfo{
			"test-rdma-0": {
				NumaNodes:      []int{0},
				DeviceAffinity: make(map[machine.AffinityPriority]machine.DeviceIDs),
			},
			"test-rdma-1": {
				NumaNodes:      []int{1},
				DeviceAffinity: make(map[machine.AffinityPriority]machine.DeviceIDs),
			},
		},
	}

	assert.Equal(t, expectedDeviceTopology, deviceTopology)
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
			expectedResp: &pluginapi.AssociatedDeviceAllocationResponse{
				AllocationResult: &pluginapi.AssociatedDeviceAllocation{
					AllocatedDevices: []string{"test-rdma-0", "test-rdma-2"},
				},
			},
		},
		{
			name:          "No accompany resource allocates by best effort, no reusable devices, skip devices that are already allocated",
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
					"test-rdma-0": {
						PodEntries: map[string]state.ContainerEntries{
							"pod-uid2": {
								"test-container": {
									AllocatedAllocation: state.Allocation{
										Quantity: 1,
									},
								},
							},
						},
					},
					"test-rdma-1": {
						PodEntries: map[string]state.ContainerEntries{
							"pod-uid3": {
								"test-container": {
									AllocatedAllocation: state.Allocation{
										Quantity: 1,
									},
								},
							},
						},
					},
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
					Nodes: []uint64{0, 1},
				},
			},
			expectedResp: &pluginapi.AssociatedDeviceAllocationResponse{
				AllocationResult: &pluginapi.AssociatedDeviceAllocation{
					AllocatedDevices: []string{"test-rdma-2", "test-rdma-3"},
				},
			},
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
			expectedResp: &pluginapi.AssociatedDeviceAllocationResponse{
				AllocationResult: &pluginapi.AssociatedDeviceAllocation{
					AllocatedDevices: []string{"test-rdma-0", "test-rdma-2"},
				},
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			basePlugin := makeTestBasePlugin(t)
			devicePlugin := NewRDMADevicePlugin(basePlugin)

			if tt.allocationInfo != nil {
				basePlugin.State.SetAllocationInfo(gpuconsts.RDMADeviceType, tt.podUID, tt.containerName, tt.allocationInfo, false)
			}

			if tt.accompanyResourceAllocationInfo != nil && tt.accompanyResourceName != "" {
				accompanyResourceType, err := basePlugin.GetResourceTypeFromDeviceName(tt.accompanyResourceName)
				assert.NoError(t, err)
				basePlugin.State.SetAllocationInfo(v1.ResourceName(accompanyResourceType), tt.podUID, tt.containerName, tt.accompanyResourceAllocationInfo, false)
			}

			if tt.deviceTopology != nil {
				err := basePlugin.DeviceTopologyRegistry.SetDeviceTopology(gpuconsts.RDMADeviceType, tt.deviceTopology)
				assert.NoError(t, err)
			}

			if tt.accompanyResourceName != "" && tt.accompanyDeviceTopology != nil {
				accompanyResourceType, err := basePlugin.GetResourceTypeFromDeviceName(tt.accompanyResourceName)
				assert.NoError(t, err)
				err = basePlugin.DeviceTopologyRegistry.SetDeviceTopology(accompanyResourceType, tt.accompanyDeviceTopology)
				assert.NoError(t, err)
			}

			if tt.machineState != nil {
				basePlugin.State.SetMachineState(tt.machineState, false)
			}

			resourceReq := &pluginapi.ResourceRequest{
				PodUid:        tt.podUID,
				ContainerName: tt.containerName,
			}

			resp, err := devicePlugin.AllocateAssociatedDevice(context.Background(), resourceReq, tt.deviceReq, tt.accompanyResourceName)
			if tt.expectedErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				evaluateAllocatedDevicesResult(t, tt.expectedResp, resp)

				// Verify state is updated
				allocationInfo := basePlugin.State.GetAllocationInfo(gpuconsts.RDMADeviceType, tt.podUID, tt.containerName)
				assert.NotNil(t, allocationInfo)
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

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

package gpu

import (
	"context"
	"testing"

	cadvisorapi "github.com/google/cadvisor/info/v1"
	"github.com/stretchr/testify/assert"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/uuid"
	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	"github.com/kubewharf/katalyst-api/pkg/consts"
	katalyst_base "github.com/kubewharf/katalyst-core/cmd/base"
	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/agent"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/baseplugin"
	gpuconsts "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/state"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/strategy/allocate/manager"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/strategy/allocate/strategies/canonical"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/strategy/allocate/strategies/deviceaffinity"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/strategy/allocate/strategies/gpu_memory"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/qrm/statedirectory"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

const (
	testDeviceAffinityAllocation = "deviceAffinityAllocation"
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

	basePlugin, err := baseplugin.NewBasePlugin(agentCtx, conf, metrics.DummyMetrics{})
	assert.NoError(t, err)

	stateImpl, err := state.NewCheckpointState(conf.StateDirectoryConfiguration, conf.QRMPluginsConfiguration, "test", "test-policy", state.NewDefaultResourceStateGeneratorRegistry(), true, metrics.DummyMetrics{})
	assert.NoError(t, err)

	basePlugin.SetState(stateImpl)

	return basePlugin
}

func TestGPUDevicePlugin_UpdateAllocatableAssociatedDevices(t *testing.T) {
	t.Parallel()

	basePlugin := makeTestBasePlugin(t)
	devicePlugin := NewGPUDevicePlugin(basePlugin)

	// Update topology with associated devices
	req := &pluginapi.UpdateAllocatableAssociatedDevicesRequest{
		DeviceName: "test-gpu",
		Devices: []*pluginapi.AssociatedDevice{
			{
				ID: "test-gpu-0",
				Topology: &pluginapi.TopologyInfo{
					Nodes: []*pluginapi.NUMANode{
						{
							ID: 0,
						},
					},
				},
			},
			{
				ID: "test-gpu-1",
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
	gpuDevicePlugin := devicePlugin.(*GPUDevicePlugin)
	deviceTopology, err := gpuDevicePlugin.DeviceTopologyRegistry.GetDeviceTopology("test-gpu")
	assert.NoError(t, err)
	assert.NotNil(t, deviceTopology)

	expectedDeviceTopology := &machine.DeviceTopology{
		Devices: map[string]machine.DeviceInfo{
			"test-gpu-0": {
				NumaNodes:  []int{0},
				Dimensions: make(map[string]string),
			},
			"test-gpu-1": {
				NumaNodes:  []int{1},
				Dimensions: make(map[string]string),
			},
		},
	}
	expectedDeviceTopology.UpdateTime = deviceTopology.UpdateTime

	assert.Equal(t, expectedDeviceTopology, deviceTopology)
}

func TestGPUDevicePlugin_AllocateAssociatedDevice(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                              string
		podUID                            string
		containerName                     string
		allocationInfo                    *state.AllocationInfo
		preAllocateResourceAllocationInfo *state.AllocationInfo
		deviceReq                         *pluginapi.DeviceRequest
		deviceTopology                    *machine.DeviceTopology
		expectedErr                       bool
		expectedResp                      *pluginapi.AssociatedDeviceAllocationResponse
	}{
		{
			name: "Allocation already exists",
			allocationInfo: &state.AllocationInfo{
				AllocatedAllocation: state.Allocation{
					Quantity:  2,
					NUMANodes: []int{0, 1},
				},
				TopologyAwareAllocations: map[string]state.Allocation{
					"test-gpu-0": {
						Quantity:  1,
						NUMANodes: []int{0},
					},
					"test-gpu-1": {
						Quantity:  1,
						NUMANodes: []int{1},
					},
				},
			},
			podUID:        string(uuid.NewUUID()),
			containerName: "test-container",
			deviceReq: &pluginapi.DeviceRequest{
				DeviceName:       "test-gpu",
				AvailableDevices: []string{"test-gpu-2", "test-gpu-3"},
				ReusableDevices:  []string{"test-gpu-2", "test-gpu-3"},
				DeviceRequest:    2,
			},
			expectedResp: &pluginapi.AssociatedDeviceAllocationResponse{
				AllocationResult: &pluginapi.AssociatedDeviceAllocation{
					AllocatedDevices: []string{"test-gpu-0", "test-gpu-1"},
				},
			},
		},
		{
			name: "gpu memory allocation exists",
			preAllocateResourceAllocationInfo: &state.AllocationInfo{
				AllocatedAllocation: state.Allocation{
					Quantity:  4,
					NUMANodes: []int{0, 1},
				},
				TopologyAwareAllocations: map[string]state.Allocation{
					"test-gpu-0": {
						Quantity:  2,
						NUMANodes: []int{0},
					},
					"test-gpu-1": {
						Quantity:  2,
						NUMANodes: []int{1},
					},
				},
			},
			podUID:        string(uuid.NewUUID()),
			containerName: "test-container",
			deviceReq: &pluginapi.DeviceRequest{
				DeviceName:       "test-gpu",
				AvailableDevices: []string{"test-gpu-2", "test-gpu-3"},
				ReusableDevices:  []string{"test-gpu-2", "test-gpu-3"},
				DeviceRequest:    2,
			},
			deviceTopology: &machine.DeviceTopology{
				Devices: map[string]machine.DeviceInfo{
					"test-gpu-0": {
						NumaNodes: []int{0}, Health: pluginapi.Healthy,
					},
					"test-gpu-1": {
						NumaNodes: []int{1}, Health: pluginapi.Healthy,
					},
					"test-gpu-2": {
						NumaNodes: []int{0}, Health: pluginapi.Healthy,
					},
					"test-gpu-3": {
						NumaNodes: []int{1}, Health: pluginapi.Healthy,
					},
				},
			},
			expectedResp: &pluginapi.AssociatedDeviceAllocationResponse{
				AllocationResult: &pluginapi.AssociatedDeviceAllocation{
					AllocatedDevices: []string{"test-gpu-0", "test-gpu-1"},
				},
			},
		},
		{
			name: "device topology does not exist",
			preAllocateResourceAllocationInfo: &state.AllocationInfo{
				AllocatedAllocation: state.Allocation{
					Quantity:  4,
					NUMANodes: []int{0, 1},
				},
				TopologyAwareAllocations: map[string]state.Allocation{
					"test-gpu-0": {
						Quantity:  2,
						NUMANodes: []int{0},
					},
					"test-gpu-1": {
						Quantity:  2,
						NUMANodes: []int{1},
					},
				},
			},
			podUID:        string(uuid.NewUUID()),
			containerName: "test-container",
			deviceReq: &pluginapi.DeviceRequest{
				DeviceName:       "test-gpu",
				AvailableDevices: []string{"test-gpu-2", "test-gpu-3"},
				ReusableDevices:  []string{"test-gpu-2", "test-gpu-3"},
				DeviceRequest:    2,
			},
			expectedErr: true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			basePlugin := makeTestBasePlugin(t)
			devicePlugin := NewGPUDevicePlugin(basePlugin)

			if tt.allocationInfo != nil {
				basePlugin.GetState().SetAllocationInfo(gpuconsts.GPUDeviceType, tt.podUID, tt.containerName, tt.allocationInfo, false)
			}

			if tt.preAllocateResourceAllocationInfo != nil {
				basePlugin.GetState().SetAllocationInfo(v1.ResourceName(defaultPreAllocateResourceName), tt.podUID, tt.containerName, tt.preAllocateResourceAllocationInfo, false)
			}

			if tt.deviceTopology != nil {
				err := basePlugin.DeviceTopologyRegistry.SetDeviceTopology("test-gpu", tt.deviceTopology)
				assert.NoError(t, err)
			}

			resourceReq := &pluginapi.ResourceRequest{
				PodUid:        tt.podUID,
				ContainerName: tt.containerName,
			}

			resp, err := devicePlugin.AllocateAssociatedDevice(context.Background(), resourceReq, tt.deviceReq, "test")
			if tt.expectedErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				evaluateAllocatedDevicesResult(t, tt.expectedResp, resp)

				// Verify state is updated
				allocationInfo := basePlugin.GetState().GetAllocationInfo(gpuconsts.GPUDeviceType, tt.podUID, tt.containerName)
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

func TestGPUDevicePlugin_GetAssociatedDeviceTopologyHints(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                              string
		podUID                            string
		containerName                     string
		qosLevel                          string
		allocationInfo                    *state.AllocationInfo
		preAllocateResourceAllocationInfo *state.AllocationInfo
		deviceReq                         *pluginapi.AssociatedDeviceRequest
		deviceTopology                    *machine.DeviceTopology
		requiredDeviceAffinity            bool
		expectedErr                       bool
		expectedResp                      *pluginapi.AssociatedDeviceHintsResponse
	}{
		{
			name:        "Invalid Request",
			expectedErr: true,
		},
		{
			name:          "GPU Allocation exists",
			podUID:        "test-uid",
			containerName: "test-container",
			allocationInfo: &state.AllocationInfo{
				TopologyAwareAllocations: map[string]state.Allocation{
					"test-gpu-0": {NUMANodes: []int{0}},
				},
			},
			deviceReq: &pluginapi.AssociatedDeviceRequest{
				ResourceRequest: &pluginapi.ResourceRequest{
					PodUid:        "test-uid",
					ContainerName: "test-container",
					Annotations:   map[string]string{consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores},
					Labels:        map[string]string{consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores},
				},
				DeviceName: "test-gpu",
				DeviceRequest: []*pluginapi.DeviceRequest{
					{DeviceName: "test-gpu", DeviceRequest: 1},
				},
			},
			expectedResp: &pluginapi.AssociatedDeviceHintsResponse{
				PodUid:        "test-uid",
				ContainerName: "test-container",
				DeviceName:    "test-gpu",
				Annotations:   map[string]string{consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores},
				Labels:        map[string]string{consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores},
				DeviceHints: &pluginapi.ListOfTopologyHints{
					Hints: []*pluginapi.TopologyHint{
						{
							Nodes:     []uint64{0},
							Preferred: true,
						},
					},
				},
			},
		},
		{
			name:          "GPU pre-allocate resource allocation exists",
			podUID:        "test-uid",
			containerName: "test-container",
			preAllocateResourceAllocationInfo: &state.AllocationInfo{
				TopologyAwareAllocations: map[string]state.Allocation{
					"test-gpu-1": {NUMANodes: []int{1}},
				},
			},
			deviceReq: &pluginapi.AssociatedDeviceRequest{
				ResourceRequest: &pluginapi.ResourceRequest{
					PodUid:        "test-uid",
					ContainerName: "test-container",
					Annotations:   map[string]string{consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores},
					Labels:        map[string]string{consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores},
				},
				DeviceName: "test-gpu",
				DeviceRequest: []*pluginapi.DeviceRequest{
					{DeviceName: "test-gpu", DeviceRequest: 1},
				},
			},
			expectedResp: &pluginapi.AssociatedDeviceHintsResponse{
				PodUid:        "test-uid",
				ContainerName: "test-container",
				DeviceName:    "test-gpu",
				Annotations:   map[string]string{consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores},
				Labels:        map[string]string{consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores},
				DeviceHints: &pluginapi.ListOfTopologyHints{
					Hints: []*pluginapi.TopologyHint{
						{
							Nodes:     []uint64{1},
							Preferred: true,
						},
					},
				},
			},
		},
		{
			name:          "device topology does not exist",
			podUID:        "test-uid",
			containerName: "test-container",
			deviceReq: &pluginapi.AssociatedDeviceRequest{
				ResourceRequest: &pluginapi.ResourceRequest{
					PodUid:        "test-uid",
					ContainerName: "test-container",
					Annotations:   map[string]string{consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores},
					Labels:        map[string]string{consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores},
				},
				DeviceName: "test-gpu",
				DeviceRequest: []*pluginapi.DeviceRequest{
					{
						DeviceName:       "test-gpu",
						AvailableDevices: []string{"test-gpu-0", "test-gpu-1"},
						DeviceRequest:    1,
					},
				},
			},
			expectedErr: true,
		},
		{
			name:          "requested devices more than available",
			podUID:        "test-uid",
			containerName: "test-container",
			deviceTopology: &machine.DeviceTopology{
				Devices: map[string]machine.DeviceInfo{
					"test-gpu-0": {NumaNodes: []int{0}, Health: pluginapi.Healthy},
				},
			},
			deviceReq: &pluginapi.AssociatedDeviceRequest{
				ResourceRequest: &pluginapi.ResourceRequest{
					PodUid:        "test-uid",
					ContainerName: "test-container",
					Annotations:   map[string]string{consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores},
					Labels:        map[string]string{consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores},
				},
				DeviceName: "test-gpu",
				DeviceRequest: []*pluginapi.DeviceRequest{
					{
						DeviceName:       "test-gpu",
						AvailableDevices: []string{"test-gpu-0"},
						DeviceRequest:    2,
					},
				},
			},
			expectedErr: true,
		},
		{
			name:          "generate from available devices without allocation info",
			podUID:        "test-uid",
			containerName: "test-container",
			deviceTopology: &machine.DeviceTopology{
				Devices: map[string]machine.DeviceInfo{
					"test-gpu-0": {NumaNodes: []int{0}, Health: pluginapi.Healthy},
					"test-gpu-1": {NumaNodes: []int{1}, Health: pluginapi.Healthy},
				},
			},
			deviceReq: &pluginapi.AssociatedDeviceRequest{
				ResourceRequest: &pluginapi.ResourceRequest{
					PodUid:        "test-uid",
					ContainerName: "test-container",
					Annotations:   map[string]string{consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores},
					Labels:        map[string]string{consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores},
				},
				DeviceName: "test-gpu",
				DeviceRequest: []*pluginapi.DeviceRequest{
					{
						DeviceName:       "test-gpu",
						AvailableDevices: []string{"test-gpu-0", "test-gpu-1"},
						DeviceRequest:    1,
					},
				},
			},
			expectedResp: &pluginapi.AssociatedDeviceHintsResponse{
				PodUid:        "test-uid",
				ContainerName: "test-container",
				DeviceName:    "test-gpu",
				Annotations:   map[string]string{consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores},
				Labels:        map[string]string{consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores},
				DeviceHints: &pluginapi.ListOfTopologyHints{
					Hints: []*pluginapi.TopologyHint{
						{Nodes: []uint64{0}, Preferred: true},
						{Nodes: []uint64{1}, Preferred: true},
						{Nodes: []uint64{0, 1}, Preferred: false},
					},
				},
			},
		},
		{
			name:          "strategy framework filters NVLink mesh",
			podUID:        "test-uid",
			containerName: "test-container",
			deviceTopology: &machine.DeviceTopology{
				PriorityDimensions: []string{"NVLINK"},
				Devices: map[string]machine.DeviceInfo{
					"test-gpu-0": {NumaNodes: []int{0}, Health: pluginapi.Healthy, Dimensions: map[string]string{"NVLINK": "group-1"}},
					"test-gpu-1": {NumaNodes: []int{1}, Health: pluginapi.Healthy, Dimensions: map[string]string{"NVLINK": "group-1"}},
					"test-gpu-2": {NumaNodes: []int{0}, Health: pluginapi.Healthy, Dimensions: map[string]string{"NVLINK": "group-2"}},
					"test-gpu-3": {NumaNodes: []int{1}, Health: pluginapi.Healthy, Dimensions: map[string]string{"NVLINK": "group-2"}},
				},
			},
			requiredDeviceAffinity: true,
			deviceReq: &pluginapi.AssociatedDeviceRequest{
				ResourceRequest: &pluginapi.ResourceRequest{
					PodUid:        "test-uid",
					ContainerName: "test-container",
					Annotations:   map[string]string{consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores},
					Labels:        map[string]string{consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores},
				},
				DeviceName: "test-gpu",
				DeviceRequest: []*pluginapi.DeviceRequest{
					{
						DeviceName:       "test-gpu",
						AvailableDevices: []string{"test-gpu-0", "test-gpu-1", "test-gpu-2", "test-gpu-3"},
						DeviceRequest:    2,
					},
				},
			},
			expectedResp: &pluginapi.AssociatedDeviceHintsResponse{
				PodUid:        "test-uid",
				ContainerName: "test-container",
				DeviceName:    "test-gpu",
				Annotations:   map[string]string{consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores},
				Labels:        map[string]string{consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores},
				DeviceHints: &pluginapi.ListOfTopologyHints{
					Hints: []*pluginapi.TopologyHint{
						// {0} and {1} will fail Strategy verification because each only contains GPUs from different NVLink groups.
						// The only valid combination is {0, 1} which has all 4 GPUs, allowing the Strategy to pick 2 GPUs from the same NVLink group.
						{Nodes: []uint64{0, 1}, Preferred: true},
					},
				},
			},
		},
		{
			name:          "strict affinity: request 4 devices, each NUMA has 4 devices in 2 groups",
			podUID:        "test-uid",
			containerName: "test-container",
			deviceTopology: &machine.DeviceTopology{
				PriorityDimensions: []string{"0"},
				Devices: map[string]machine.DeviceInfo{
					"test-gpu-1": {NumaNodes: []int{0}, Health: pluginapi.Healthy, Dimensions: map[string]string{"0": "g12"}},
					"test-gpu-2": {NumaNodes: []int{0}, Health: pluginapi.Healthy, Dimensions: map[string]string{"0": "g12"}},
					"test-gpu-3": {NumaNodes: []int{0}, Health: pluginapi.Healthy, Dimensions: map[string]string{"0": "g34"}},
					"test-gpu-4": {NumaNodes: []int{0}, Health: pluginapi.Healthy, Dimensions: map[string]string{"0": "g34"}},
					"test-gpu-5": {NumaNodes: []int{1}, Health: pluginapi.Healthy, Dimensions: map[string]string{"0": "g56"}},
					"test-gpu-6": {NumaNodes: []int{1}, Health: pluginapi.Healthy, Dimensions: map[string]string{"0": "g56"}},
					"test-gpu-7": {NumaNodes: []int{1}, Health: pluginapi.Healthy, Dimensions: map[string]string{"0": "g78"}},
					"test-gpu-8": {NumaNodes: []int{1}, Health: pluginapi.Healthy, Dimensions: map[string]string{"0": "g78"}},
				},
			},
			requiredDeviceAffinity: true,
			deviceReq: &pluginapi.AssociatedDeviceRequest{
				ResourceRequest: &pluginapi.ResourceRequest{
					PodUid:        "test-uid",
					ContainerName: "test-container",
					Annotations:   map[string]string{consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores},
					Labels:        map[string]string{consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores},
				},
				DeviceName: "test-gpu",
				DeviceRequest: []*pluginapi.DeviceRequest{
					{
						DeviceName:       "test-gpu",
						AvailableDevices: []string{"test-gpu-1", "test-gpu-2", "test-gpu-3", "test-gpu-4", "test-gpu-5", "test-gpu-6", "test-gpu-7", "test-gpu-8"},
						DeviceRequest:    4,
					},
				},
			},
			expectedResp: &pluginapi.AssociatedDeviceHintsResponse{
				PodUid:        "test-uid",
				ContainerName: "test-container",
				DeviceName:    "test-gpu",
				Annotations:   map[string]string{consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores},
				Labels:        map[string]string{consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores},
				DeviceHints: &pluginapi.ListOfTopologyHints{
					Hints: []*pluginapi.TopologyHint{
						// {0} has 4 devices (two full groups), satisfies request 4
						// {1} has 4 devices (two full groups), satisfies request 4
						{Nodes: []uint64{0}, Preferred: true},
						{Nodes: []uint64{1}, Preferred: true},
						{Nodes: []uint64{0, 1}, Preferred: false},
					},
				},
			},
		},
		{
			name:          "strict affinity: request 5 devices from two NUMA nodes with 4 devices each",
			podUID:        "test-uid",
			containerName: "test-container",
			deviceTopology: &machine.DeviceTopology{
				PriorityDimensions: []string{"0"},
				Devices: map[string]machine.DeviceInfo{
					"test-gpu-1": {NumaNodes: []int{0}, Health: pluginapi.Healthy, Dimensions: map[string]string{"0": "g12"}},
					"test-gpu-2": {NumaNodes: []int{0}, Health: pluginapi.Healthy, Dimensions: map[string]string{"0": "g12"}},
					"test-gpu-3": {NumaNodes: []int{0}, Health: pluginapi.Healthy, Dimensions: map[string]string{"0": "g34"}},
					"test-gpu-4": {NumaNodes: []int{0}, Health: pluginapi.Healthy, Dimensions: map[string]string{"0": "g34"}},
					"test-gpu-5": {NumaNodes: []int{1}, Health: pluginapi.Healthy, Dimensions: map[string]string{"0": "g56"}},
					"test-gpu-6": {NumaNodes: []int{1}, Health: pluginapi.Healthy, Dimensions: map[string]string{"0": "g56"}},
					"test-gpu-7": {NumaNodes: []int{1}, Health: pluginapi.Healthy, Dimensions: map[string]string{"0": "g78"}},
					"test-gpu-8": {NumaNodes: []int{1}, Health: pluginapi.Healthy, Dimensions: map[string]string{"0": "g78"}},
				},
			},
			requiredDeviceAffinity: true,
			deviceReq: &pluginapi.AssociatedDeviceRequest{
				ResourceRequest: &pluginapi.ResourceRequest{
					PodUid:        "test-uid",
					ContainerName: "test-container",
					Annotations:   map[string]string{consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores},
					Labels:        map[string]string{consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores},
				},
				DeviceName: "test-gpu",
				DeviceRequest: []*pluginapi.DeviceRequest{
					{
						DeviceName:       "test-gpu",
						AvailableDevices: []string{"test-gpu-1", "test-gpu-2", "test-gpu-3", "test-gpu-4", "test-gpu-5", "test-gpu-6", "test-gpu-7", "test-gpu-8"},
						DeviceRequest:    5,
					},
				},
			},
			expectedResp: &pluginapi.AssociatedDeviceHintsResponse{
				PodUid:        "test-uid",
				ContainerName: "test-container",
				DeviceName:    "test-gpu",
				Annotations:   map[string]string{consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores},
				Labels:        map[string]string{consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores},
				DeviceHints: &pluginapi.ListOfTopologyHints{
					Hints: []*pluginapi.TopologyHint{
						// {0} only has 4 devices, fails
						// {1} only has 4 devices, fails
						// {0, 1} has 8 devices. We request 5 devices, which needs ceil(5/2)=3 groups.
						// This can be satisfied, so {0, 1} is preferred.
						{Nodes: []uint64{0, 1}, Preferred: true},
					},
				},
			},
		},
		{
			name:          "strict affinity: multi-level affinity, fragmented NUMA fails, unfragmented NUMA succeeds",
			podUID:        "test-uid",
			containerName: "test-container",
			deviceTopology: &machine.DeviceTopology{
				PriorityDimensions: []string{"NVLINK", "SOCKET"},
				Devices: map[string]machine.DeviceInfo{
					"test-gpu-1": {NumaNodes: []int{0}, Health: pluginapi.Healthy, Dimensions: map[string]string{"NVLINK": "g1", "SOCKET": "s1"}},
					"test-gpu-2": {NumaNodes: []int{0}, Health: pluginapi.Healthy, Dimensions: map[string]string{"NVLINK": "g1", "SOCKET": "s1"}},
					"test-gpu-3": {NumaNodes: []int{0}, Health: pluginapi.Healthy, Dimensions: map[string]string{"NVLINK": "g2", "SOCKET": "s1"}},
					"test-gpu-4": {NumaNodes: []int{0}, Health: pluginapi.Healthy, Dimensions: map[string]string{"NVLINK": "g2", "SOCKET": "s1"}},
					"test-gpu-5": {NumaNodes: []int{1}, Health: pluginapi.Healthy, Dimensions: map[string]string{"NVLINK": "g3", "SOCKET": "s2"}},
					"test-gpu-6": {NumaNodes: []int{1}, Health: pluginapi.Healthy, Dimensions: map[string]string{"NVLINK": "g3", "SOCKET": "s2"}},
					"test-gpu-7": {NumaNodes: []int{1}, Health: pluginapi.Healthy, Dimensions: map[string]string{"NVLINK": "g4", "SOCKET": "s2"}},
					"test-gpu-8": {NumaNodes: []int{1}, Health: pluginapi.Healthy, Dimensions: map[string]string{"NVLINK": "g4", "SOCKET": "s2"}},
				},
			},
			requiredDeviceAffinity: true,
			deviceReq: &pluginapi.AssociatedDeviceRequest{
				ResourceRequest: &pluginapi.ResourceRequest{
					PodUid:        "test-uid",
					ContainerName: "test-container",
					Annotations:   map[string]string{consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores},
					Labels:        map[string]string{consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores},
				},
				DeviceName: "test-gpu",
				DeviceRequest: []*pluginapi.DeviceRequest{
					{
						DeviceName:       "test-gpu",
						AvailableDevices: []string{"test-gpu-1", "test-gpu-3", "test-gpu-5", "test-gpu-6"},
						DeviceRequest:    2,
					},
				},
			},
			expectedResp: &pluginapi.AssociatedDeviceHintsResponse{
				PodUid:        "test-uid",
				ContainerName: "test-container",
				DeviceName:    "test-gpu",
				Annotations:   map[string]string{consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores},
				Labels:        map[string]string{consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores},
				DeviceHints: &pluginapi.ListOfTopologyHints{
					Hints: []*pluginapi.TopologyHint{
						// {0} has fragmented available devices (1 and 3) that belong to different NVLINK groups, fails strategy.
						// {1} has unfragmented available devices (5 and 6) that belong to the same NVLINK group, passes strategy.
						{Nodes: []uint64{1}, Preferred: true},
						{Nodes: []uint64{0, 1}, Preferred: false},
					},
				},
			},
		},
		{
			name:          "strategy allocation always succeeds, hint generation aligns with kubelet Device Manager",
			podUID:        "test-uid",
			containerName: "test-container",
			deviceTopology: &machine.DeviceTopology{
				Devices: map[string]machine.DeviceInfo{
					"test-gpu-0": {NumaNodes: []int{0}, Health: pluginapi.Healthy},
					"test-gpu-1": {NumaNodes: []int{0}, Health: pluginapi.Healthy},
					"test-gpu-2": {NumaNodes: []int{1}, Health: pluginapi.Healthy},
					"test-gpu-3": {NumaNodes: []int{1}, Health: pluginapi.Healthy},
				},
			},
			deviceReq: &pluginapi.AssociatedDeviceRequest{
				ResourceRequest: &pluginapi.ResourceRequest{
					PodUid:        "test-uid",
					ContainerName: "test-container",
					Annotations:   map[string]string{consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores},
					Labels:        map[string]string{consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores},
				},
				DeviceName: "test-gpu",
				DeviceRequest: []*pluginapi.DeviceRequest{
					{
						DeviceName:       "test-gpu",
						AvailableDevices: []string{"test-gpu-0", "test-gpu-1", "test-gpu-2", "test-gpu-3"},
						DeviceRequest:    2,
					},
				},
			},
			expectedResp: &pluginapi.AssociatedDeviceHintsResponse{
				PodUid:        "test-uid",
				ContainerName: "test-container",
				DeviceName:    "test-gpu",
				Annotations:   map[string]string{consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores},
				Labels:        map[string]string{consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores},
				DeviceHints: &pluginapi.ListOfTopologyHints{
					Hints: []*pluginapi.TopologyHint{
						// When allocation always succeeds (no strict affinity required),
						// all combinations that have enough devices are returned.
						// {0} has 2 devices, satisfies request 2. Size 1 -> Preferred: true
						// {1} has 2 devices, satisfies request 2. Size 1 -> Preferred: true
						// {0, 1} has 4 devices, satisfies request 2. Size 2 -> Preferred: false
						{Nodes: []uint64{0}, Preferred: true},
						{Nodes: []uint64{1}, Preferred: true},
						{Nodes: []uint64{0, 1}, Preferred: false},
					},
				},
			},
		},
		{
			name:          "strategy allocation always succeeds, request 3 devices, hint generation aligns with kubelet Device Manager",
			podUID:        "test-uid",
			containerName: "test-container",
			deviceTopology: &machine.DeviceTopology{
				Devices: map[string]machine.DeviceInfo{
					"test-gpu-0": {NumaNodes: []int{0}, Health: pluginapi.Healthy},
					"test-gpu-1": {NumaNodes: []int{0}, Health: pluginapi.Healthy},
					"test-gpu-2": {NumaNodes: []int{1}, Health: pluginapi.Healthy},
					"test-gpu-3": {NumaNodes: []int{1}, Health: pluginapi.Healthy},
				},
			},
			deviceReq: &pluginapi.AssociatedDeviceRequest{
				ResourceRequest: &pluginapi.ResourceRequest{
					PodUid:        "test-uid",
					ContainerName: "test-container",
					Annotations:   map[string]string{consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores},
					Labels:        map[string]string{consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores},
				},
				DeviceName: "test-gpu",
				DeviceRequest: []*pluginapi.DeviceRequest{
					{
						DeviceName:       "test-gpu",
						AvailableDevices: []string{"test-gpu-0", "test-gpu-1", "test-gpu-2", "test-gpu-3"},
						DeviceRequest:    3,
					},
				},
			},
			expectedResp: &pluginapi.AssociatedDeviceHintsResponse{
				PodUid:        "test-uid",
				ContainerName: "test-container",
				DeviceName:    "test-gpu",
				Annotations:   map[string]string{consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores},
				Labels:        map[string]string{consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores},
				DeviceHints: &pluginapi.ListOfTopologyHints{
					Hints: []*pluginapi.TopologyHint{
						// {0} has 2 devices, does not satisfy request 3.
						// {1} has 2 devices, does not satisfy request 3.
						// {0, 1} has 4 devices, satisfies request 3. Size 2 -> Preferred: true (because it's the minimum size)
						{Nodes: []uint64{0, 1}, Preferred: true},
					},
				},
			},
		},
		{
			name:          "generate from available devices with unhealthy GPUs filtered out",
			podUID:        "test-uid",
			containerName: "test-container",
			deviceTopology: &machine.DeviceTopology{
				Devices: map[string]machine.DeviceInfo{
					"test-gpu-0": {NumaNodes: []int{0}, Health: pluginapi.Unhealthy},
					"test-gpu-1": {NumaNodes: []int{1}, Health: pluginapi.Healthy},
				},
			},
			deviceReq: &pluginapi.AssociatedDeviceRequest{
				ResourceRequest: &pluginapi.ResourceRequest{
					PodUid:        "test-uid",
					ContainerName: "test-container",
					Annotations:   map[string]string{consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores},
					Labels:        map[string]string{consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores},
				},
				DeviceName: "test-gpu",
				DeviceRequest: []*pluginapi.DeviceRequest{
					{
						DeviceName:       "test-gpu",
						AvailableDevices: []string{"test-gpu-0", "test-gpu-1"},
						DeviceRequest:    1,
					},
				},
			},
			expectedResp: &pluginapi.AssociatedDeviceHintsResponse{
				PodUid:        "test-uid",
				ContainerName: "test-container",
				DeviceName:    "test-gpu",
				Annotations:   map[string]string{consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores},
				Labels:        map[string]string{consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores},
				DeviceHints: &pluginapi.ListOfTopologyHints{
					Hints: []*pluginapi.TopologyHint{
						// Only NUMA node 1 has healthy GPUs, so only 1 should be generated.
						{Nodes: []uint64{1}, Preferred: true},
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
			devicePlugin := NewGPUDevicePlugin(basePlugin)

			if tt.allocationInfo != nil {
				basePlugin.GetState().SetAllocationInfo(gpuconsts.GPUDeviceType, tt.podUID, tt.containerName, tt.allocationInfo, false)
			}
			if tt.preAllocateResourceAllocationInfo != nil {
				basePlugin.GetState().SetAllocationInfo(v1.ResourceName(defaultPreAllocateResourceName), tt.podUID, tt.containerName, tt.preAllocateResourceAllocationInfo, false)
			}
			if tt.deviceTopology != nil {
				err := basePlugin.DeviceTopologyRegistry.SetDeviceTopology("test-gpu", tt.deviceTopology)
				assert.NoError(t, err)

				if len(tt.deviceTopology.PriorityDimensions) > 0 {
					basePlugin.Conf.GPUQRMPluginConfig.RequiredDeviceAffinity = tt.requiredDeviceAffinity

					err = manager.GetGlobalStrategyManager().RegisterGenericAllocationStrategy(
						testDeviceAffinityAllocation,
						[]string{canonical.StrategyNameCanonical, gpu_memory.StrategyNameGPUMemory},
						gpu_memory.StrategyNameGPUMemory,
						deviceaffinity.StrategyNameDeviceAffinity,
					)
					// Ignore already registered error
					if err != nil && err.Error() != "strategy "+testDeviceAffinityAllocation+" already exists" {
						t.Logf("register strategy err: %v", err)
					}

					if basePlugin.Conf.GPUQRMPluginConfig.CustomAllocationStrategy == nil {
						basePlugin.Conf.GPUQRMPluginConfig.CustomAllocationStrategy = make(map[string]string)
					}
					basePlugin.Conf.GPUQRMPluginConfig.CustomAllocationStrategy["test-gpu"] = testDeviceAffinityAllocation
				}
			}

			resp, err := devicePlugin.GetAssociatedDeviceTopologyHints(context.Background(), tt.deviceReq)
			if tt.expectedErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				// Order of hints doesn't matter, we can use ElementsMatch
				if tt.expectedResp != nil && tt.expectedResp.DeviceHints != nil {
					assert.ElementsMatch(t, tt.expectedResp.DeviceHints.Hints, resp.DeviceHints.Hints)
					resp.DeviceHints.Hints = nil
					tt.expectedResp.DeviceHints.Hints = nil
				}
				assert.Equal(t, tt.expectedResp, resp)
			}
		})
	}
}

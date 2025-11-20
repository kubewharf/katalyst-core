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

package staticpolicy

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/uuid"
	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	katalyst_base "github.com/kubewharf/katalyst-core/cmd/base"
	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/agent"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/baseplugin"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/customdeviceplugin"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/resourceplugin"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/state"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

const (
	testResourcePluginName      = "resource-plugin-stub"
	testCustomDevicePluginName  = "custom-device-plugin-stub"
	testCustomDevicePluginName2 = "custom-device-plugin-stub-2"
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

func TestNewStaticPolicy(t *testing.T) {
	t.Parallel()

	conf := generateTestConfiguration(t)
	agentCtx := generateTestGenericContext(t, conf)

	tmpDir := t.TempDir()
	conf.GenericQRMPluginConfiguration.StateFileDirectory = tmpDir

	_, policy, err := NewStaticPolicy(agentCtx, conf, nil, "test")
	assert.NoError(t, err)
	assert.NotNil(t, policy)
}

func makeTestStaticPolicy(t *testing.T) *StaticPolicy {
	conf := generateTestConfiguration(t)
	agentCtx := generateTestGenericContext(t, conf)

	tmpDir := t.TempDir()
	conf.GenericQRMPluginConfiguration.StateFileDirectory = tmpDir

	stateImpl, err := state.NewCheckpointState(conf.QRMPluginsConfiguration, tmpDir, "test", "test-policy", state.NewDefaultResourceStateGeneratorRegistry(), true, metrics.DummyMetrics{})
	assert.NoError(t, err)

	deviceTopologyRegistry := machine.NewDeviceTopologyRegistry()

	basePlugin := &baseplugin.BasePlugin{
		Conf:                                  conf,
		Emitter:                               metrics.DummyMetrics{},
		MetaServer:                            agentCtx.MetaServer,
		AgentCtx:                              agentCtx,
		PodAnnotationKeptKeys:                 []string{},
		PodLabelKeptKeys:                      []string{},
		State:                                 stateImpl,
		DeviceTopologyRegistry:                deviceTopologyRegistry,
		DefaultResourceStateGeneratorRegistry: state.NewDefaultResourceStateGeneratorRegistry(),
	}

	staticPolicy := &StaticPolicy{
		BasePlugin:            basePlugin,
		resourcePlugins:       make(map[string]resourceplugin.ResourcePlugin),
		customDevicePlugins:   make(map[string]customdeviceplugin.CustomDevicePlugin),
		associatedDeviceNames: sets.NewString(),
	}

	err = staticPolicy.registerDefaultResourcePlugins()
	assert.NoError(t, err)

	err = staticPolicy.registerDefaultCustomDevicePlugins()
	assert.NoError(t, err)

	return staticPolicy
}

func TestStaticPolicy_Allocate(t *testing.T) {
	t.Parallel()

	policy := makeTestStaticPolicy(t)

	// Register stubbed resource plugin
	policy.RegisterResourcePlugin(resourceplugin.NewResourcePluginStub(policy.BasePlugin))

	testName := "test"
	podUID := string(uuid.NewUUID())

	req := &pluginapi.ResourceRequest{
		PodUid:         podUID,
		PodNamespace:   testName,
		PodName:        testName,
		ContainerName:  testName,
		ContainerType:  pluginapi.ContainerType_MAIN,
		ContainerIndex: 0,
		ResourceName:   testResourcePluginName,
		ResourceRequests: map[string]float64{
			testResourcePluginName: 2,
		},
		Labels:      map[string]string{},
		Annotations: map[string]string{},
	}

	_, err := policy.Allocate(context.Background(), req)
	assert.NoError(t, err)

	// Check state
	stateImpl := policy.State
	allocationInfo := stateImpl.GetAllocationInfo(testResourcePluginName, podUID, testName)
	fmt.Println(allocationInfo)
	assert.NotNil(t, allocationInfo)

	// Allocating to an invalid resource plugin returns error
	invalidReq := &pluginapi.ResourceRequest{
		PodUid:         string(uuid.NewUUID()),
		PodNamespace:   testName,
		PodName:        testName,
		ContainerName:  testName,
		ContainerType:  pluginapi.ContainerType_MAIN,
		ContainerIndex: 0,
		ResourceName:   "invalid-plugin",
		ResourceRequests: map[string]float64{
			"invalid-plugin": 2,
		},
		Labels:      map[string]string{},
		Annotations: map[string]string{},
	}

	_, err = policy.Allocate(context.Background(), invalidReq)
	assert.Error(t, err)
}

func TestStaticPolicy_RemovePod(t *testing.T) {
	t.Parallel()
	policy := makeTestStaticPolicy(t)

	// Register stubbed resource plugin
	policy.RegisterResourcePlugin(resourceplugin.NewResourcePluginStub(policy.BasePlugin))

	deviceTopologyProviderStub := machine.NewDeviceTopologyProviderStub()

	testDeviceTopology := &machine.DeviceTopology{
		Devices: map[string]machine.DeviceInfo{
			"test-1": {},
		},
	}
	err := deviceTopologyProviderStub.SetDeviceTopology(testDeviceTopology)
	assert.NoError(t, err)

	policy.DeviceTopologyRegistry.RegisterDeviceTopologyProvider(testResourcePluginName, deviceTopologyProviderStub)

	policy.DefaultResourceStateGeneratorRegistry.RegisterResourceStateGenerator(testResourcePluginName,
		state.NewGenericDefaultResourceStateGenerator(testResourcePluginName, policy.DeviceTopologyRegistry))

	testName := "test"
	podUID := string(uuid.NewUUID())

	req := &pluginapi.ResourceRequest{
		PodUid:         podUID,
		PodNamespace:   testName,
		PodName:        testName,
		ContainerName:  testName,
		ContainerType:  pluginapi.ContainerType_MAIN,
		ContainerIndex: 0,
		ResourceName:   testResourcePluginName,
		ResourceRequests: map[string]float64{
			testResourcePluginName: 2,
		},
		Labels:      map[string]string{},
		Annotations: map[string]string{},
	}

	_, err = policy.Allocate(context.Background(), req)
	assert.NoError(t, err)

	// Remove pod
	_, err = policy.RemovePod(context.Background(), &pluginapi.RemovePodRequest{
		PodUid: podUID,
	})
	assert.NoError(t, err)

	// Check state
	stateImpl := policy.State
	allocationInfo := stateImpl.GetAllocationInfo(testResourcePluginName, podUID, testName)
	assert.Nil(t, allocationInfo)
}

func TestStaticPolicy_GetTopologyHints(t *testing.T) {
	t.Parallel()

	policy := makeTestStaticPolicy(t)

	policy.RegisterResourcePlugin(resourceplugin.NewResourcePluginStub(policy.BasePlugin))

	testName := "test"
	podUID := string(uuid.NewUUID())

	req := &pluginapi.ResourceRequest{
		PodUid:         podUID,
		PodNamespace:   testName,
		PodName:        testName,
		ContainerName:  testName,
		ContainerType:  pluginapi.ContainerType_MAIN,
		ContainerIndex: 0,
		ResourceName:   testResourcePluginName,
		ResourceRequests: map[string]float64{
			testResourcePluginName: 2,
		},
		Labels:      map[string]string{},
		Annotations: map[string]string{},
	}

	resp, err := policy.GetTopologyHints(context.Background(), req)
	assert.NoError(t, err)
	assert.NotNil(t, resp)

	// Getting topology hints from an invalid resource plugin returns error
	invalidReq := &pluginapi.ResourceRequest{
		PodUid:         podUID,
		PodNamespace:   testName,
		PodName:        testName,
		ContainerName:  testName,
		ContainerType:  pluginapi.ContainerType_MAIN,
		ContainerIndex: 0,
		ResourceName:   "invalid-plugin",
		ResourceRequests: map[string]float64{
			"invalid-plugin": 2,
		},
		Labels:      map[string]string{},
		Annotations: map[string]string{},
	}

	_, err = policy.GetTopologyHints(context.Background(), invalidReq)
	assert.Error(t, err)
}

func TestStaticPolicy_GetTopologyAwareResources(t *testing.T) {
	t.Parallel()

	policy := makeTestStaticPolicy(t)

	policy.RegisterResourcePlugin(resourceplugin.NewResourcePluginStub(policy.BasePlugin))
	testName := "test"
	podUID := string(uuid.NewUUID())

	// Allocate first
	req := &pluginapi.ResourceRequest{
		PodUid:         podUID,
		PodNamespace:   testName,
		PodName:        testName,
		ContainerName:  testName,
		ContainerType:  pluginapi.ContainerType_MAIN,
		ContainerIndex: 0,
		ResourceName:   testResourcePluginName,
		ResourceRequests: map[string]float64{
			testResourcePluginName: 2,
		},
		Labels:      map[string]string{},
		Annotations: map[string]string{},
	}

	_, err := policy.Allocate(context.Background(), req)
	assert.NoError(t, err)

	// Get topology aware resources
	getTopologyAwareResourcesReq := &pluginapi.GetTopologyAwareResourcesRequest{
		PodUid:        podUID,
		ContainerName: testName,
	}

	resp, err := policy.GetTopologyAwareResources(context.Background(), getTopologyAwareResourcesReq)
	assert.NoError(t, err)
	assert.NotNil(t, resp)

	// Invalid request does not return error
	invalidReq := &pluginapi.GetTopologyAwareResourcesRequest{
		PodUid:        podUID,
		ContainerName: "invalid-container",
	}

	resp, err = policy.GetTopologyAwareResources(context.Background(), invalidReq)
	assert.NoError(t, err)
	assert.NotNil(t, resp)
}

func TestStaticPolicy_mergeTopologyAwareResourcesResponse(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		podUID        string
		containerName string
		respList      []*pluginapi.GetTopologyAwareResourcesResponse
		expectedResp  *pluginapi.GetTopologyAwareResourcesResponse
		expectedErr   bool
	}{
		{
			name:          "test merging of response",
			podUID:        "test-pod",
			containerName: "test-container",
			respList: []*pluginapi.GetTopologyAwareResourcesResponse{
				{
					PodUid:       "test-pod",
					PodName:      "test-pod-name",
					PodNamespace: "test-pod-namespace",
					ContainerTopologyAwareResources: &pluginapi.ContainerTopologyAwareResources{
						ContainerName: "test-container",
						AllocatedResources: map[string]*pluginapi.TopologyAwareResource{
							"test-resource-1": {},
						},
					},
				},
				{
					PodUid:       "test-pod",
					PodName:      "test-pod-name",
					PodNamespace: "test-pod-namespace",
					ContainerTopologyAwareResources: &pluginapi.ContainerTopologyAwareResources{
						ContainerName: "test-container",
						AllocatedResources: map[string]*pluginapi.TopologyAwareResource{
							"test-resource-2": {},
						},
					},
				},
			},
			expectedResp: &pluginapi.GetTopologyAwareResourcesResponse{
				PodUid:       "test-pod",
				PodName:      "test-pod-name",
				PodNamespace: "test-pod-namespace",
				ContainerTopologyAwareResources: &pluginapi.ContainerTopologyAwareResources{
					ContainerName: "test-container",
					AllocatedResources: map[string]*pluginapi.TopologyAwareResource{
						"test-resource-1": {},
						"test-resource-2": {},
					},
				},
			},
		},
		{
			name:          "pod name is not the same, return an error",
			podUID:        "test-pod",
			containerName: "test-container",
			respList: []*pluginapi.GetTopologyAwareResourcesResponse{
				{
					PodUid:       "test-pod",
					PodName:      "test-pod-name",
					PodNamespace: "test-pod-namespace",
					ContainerTopologyAwareResources: &pluginapi.ContainerTopologyAwareResources{
						ContainerName: "test-container",
						AllocatedResources: map[string]*pluginapi.TopologyAwareResource{
							"test-resource-1": {},
						},
					},
				},
				{
					PodUid:       "test-pod",
					PodName:      "test-pod-name-2",
					PodNamespace: "test-pod-namespace",
					ContainerTopologyAwareResources: &pluginapi.ContainerTopologyAwareResources{
						ContainerName: "test-container",
						AllocatedResources: map[string]*pluginapi.TopologyAwareResource{
							"test-resource-2": {},
						},
					},
				},
			},
			expectedErr: true,
		},
		{
			name:          "pod namespace is not the same, return an error",
			podUID:        "test-pod",
			containerName: "test-container",
			respList: []*pluginapi.GetTopologyAwareResourcesResponse{
				{
					PodUid:       "test-pod",
					PodName:      "test-pod-name",
					PodNamespace: "test-pod-namespace",
					ContainerTopologyAwareResources: &pluginapi.ContainerTopologyAwareResources{
						ContainerName: "test-container",
						AllocatedResources: map[string]*pluginapi.TopologyAwareResource{
							"test-resource-1": {},
						},
					},
				},
				{
					PodUid:       "test-pod",
					PodName:      "test-pod-name",
					PodNamespace: "test-pod-namespace-2",
					ContainerTopologyAwareResources: &pluginapi.ContainerTopologyAwareResources{
						ContainerName: "test-container",
						AllocatedResources: map[string]*pluginapi.TopologyAwareResource{
							"test-resource-2": {},
						},
					},
				},
			},
			expectedErr: true,
		},
		{
			name:          "container topology aware resources is nil, return an error",
			podUID:        "test-pod",
			containerName: "test-container",
			respList: []*pluginapi.GetTopologyAwareResourcesResponse{
				{
					PodUid:       "test-pod",
					PodName:      "test-pod-name",
					PodNamespace: "test-pod-namespace",
					ContainerTopologyAwareResources: &pluginapi.ContainerTopologyAwareResources{
						ContainerName: "test-container",
						AllocatedResources: map[string]*pluginapi.TopologyAwareResource{
							"test-resource-1": {},
						},
					},
				},
				{
					PodUid:                          "test-pod",
					PodName:                         "test-pod-name",
					PodNamespace:                    "test-pod-namespace",
					ContainerTopologyAwareResources: nil,
				},
			},
			expectedErr: true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			policy := makeTestStaticPolicy(t)

			resp, err := policy.mergeTopologyAwareResourcesResponse(tt.podUID, tt.containerName, tt.respList)
			if tt.expectedErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expectedResp, resp)
			}
		})
	}
}

func TestStaticPolicy_GetTopologyAwareAllocatableResources(t *testing.T) {
	t.Parallel()

	policy := makeTestStaticPolicy(t)

	policy.RegisterResourcePlugin(resourceplugin.NewResourcePluginStub(policy.BasePlugin))
	testName := "test"
	podUID := string(uuid.NewUUID())

	// Allocate first
	req := &pluginapi.ResourceRequest{
		PodUid:         podUID,
		PodNamespace:   testName,
		PodName:        testName,
		ContainerName:  testName,
		ContainerType:  pluginapi.ContainerType_MAIN,
		ContainerIndex: 0,
		ResourceName:   testResourcePluginName,
		ResourceRequests: map[string]float64{
			testResourcePluginName: 2,
		},
		Labels:      map[string]string{},
		Annotations: map[string]string{},
	}

	_, err := policy.Allocate(context.Background(), req)
	assert.NoError(t, err)

	getTopologyAwareAllocatableResourcesReq := &pluginapi.GetTopologyAwareAllocatableResourcesRequest{}
	resp, err := policy.GetTopologyAwareAllocatableResources(context.Background(), getTopologyAwareAllocatableResourcesReq)
	assert.NoError(t, err)
	assert.NotNil(t, resp)
}

func TestStaticPolicy_UpdateAllocatableAssociatedDevices(t *testing.T) {
	t.Parallel()

	policy := makeTestStaticPolicy(t)

	policy.RegisterResourcePlugin(resourceplugin.NewResourcePluginStub(policy.BasePlugin))
	policy.RegisterCustomDevicePlugin(customdeviceplugin.NewCustomDevicePluginStub(policy.BasePlugin))

	req := &pluginapi.UpdateAllocatableAssociatedDevicesRequest{
		DeviceName: testCustomDevicePluginName,
		Devices: []*pluginapi.AssociatedDevice{
			{
				ID: "test-device-1",
			},
		},
	}

	resp, err := policy.UpdateAllocatableAssociatedDevices(context.Background(), req)
	assert.NoError(t, err)
	assert.NotNil(t, resp)

	// Test error handling for no device request
	noDeviceRequest := &pluginapi.UpdateAllocatableAssociatedDevicesRequest{
		DeviceName: testCustomDevicePluginName,
	}

	resp, err = policy.UpdateAllocatableAssociatedDevices(context.Background(), noDeviceRequest)
	assert.Error(t, err)
	assert.Nil(t, resp)

	// Test error handling for non-existent custom device plugin
	invalidReq := &pluginapi.UpdateAllocatableAssociatedDevicesRequest{
		DeviceName: "non-existent-device",
	}

	resp, err = policy.UpdateAllocatableAssociatedDevices(context.Background(), invalidReq)
	assert.Error(t, err)
	assert.Nil(t, resp)
}

func TestStaticPolicy_AllocateAssociatedDevices(t *testing.T) {
	t.Parallel()

	policy := makeTestStaticPolicy(t)
	policy.RegisterResourcePlugin(resourceplugin.NewResourcePluginStub(policy.BasePlugin))
	policy.RegisterCustomDevicePlugin(customdeviceplugin.NewCustomDevicePluginStub(policy.BasePlugin))
	policy.RegisterCustomDevicePlugin(customdeviceplugin.NewCustomDevicePluginStub2(policy.BasePlugin))

	podUID := string(uuid.NewUUID())

	testName := "test"

	req := &pluginapi.AssociatedDeviceRequest{
		ResourceRequest: &pluginapi.ResourceRequest{
			PodUid:         podUID,
			PodNamespace:   testName,
			PodName:        testName,
			ContainerName:  testName,
			ContainerType:  pluginapi.ContainerType_MAIN,
			ContainerIndex: 0,
			ResourceName:   testResourcePluginName,
			ResourceRequests: map[string]float64{
				testResourcePluginName: 2,
			},
			Labels:      map[string]string{},
			Annotations: map[string]string{},
		},
		DeviceRequest: []*pluginapi.DeviceRequest{
			{
				DeviceName: testCustomDevicePluginName,
			},
		},
		DeviceName:            testCustomDevicePluginName,
		AccompanyResourceName: testResourcePluginName,
	}

	resp, err := policy.AllocateAssociatedDevice(context.Background(), req)

	assert.NoError(t, err)
	assert.NotNil(t, resp)

	// Verify in state
	stateImpl := policy.State
	allocationInfo := stateImpl.GetAllocationInfo(testCustomDevicePluginName, podUID, testName)
	assert.NotNil(t, allocationInfo)

	podUID = string(uuid.NewUUID())
	// Error handling if there is no target device
	noTargetDeviceReq := &pluginapi.AssociatedDeviceRequest{
		ResourceRequest: &pluginapi.ResourceRequest{
			PodUid:        podUID,
			PodNamespace:  testName,
			PodName:       testName,
			ContainerName: testName,
			ContainerType: pluginapi.ContainerType_MAIN,
			ResourceName:  testResourcePluginName,
			ResourceRequests: map[string]float64{
				testResourcePluginName: 2,
			},
			Labels:      map[string]string{},
			Annotations: map[string]string{},
		},
		DeviceRequest: []*pluginapi.DeviceRequest{
			{
				DeviceName: testCustomDevicePluginName,
			},
		},
		DeviceName:            "invalid-device",
		AccompanyResourceName: testResourcePluginName,
	}

	resp, err = policy.AllocateAssociatedDevice(context.Background(), noTargetDeviceReq)

	assert.Error(t, err)
	assert.Nil(t, resp)

	// Accompany resource is another custom device plugin
	req = &pluginapi.AssociatedDeviceRequest{
		ResourceRequest: &pluginapi.ResourceRequest{
			PodUid:        podUID,
			PodNamespace:  testName,
			PodName:       testName,
			ContainerName: testName,
			ContainerType: pluginapi.ContainerType_MAIN,
			ResourceName:  testResourcePluginName,
			ResourceRequests: map[string]float64{
				testResourcePluginName: 2,
			},
			Labels:      map[string]string{},
			Annotations: map[string]string{},
		},
		DeviceRequest: []*pluginapi.DeviceRequest{
			{
				DeviceName: testCustomDevicePluginName,
			},
			{
				DeviceName: testCustomDevicePluginName2,
			},
		},
		DeviceName:            testCustomDevicePluginName,
		AccompanyResourceName: testCustomDevicePluginName2,
	}

	resp, err = policy.AllocateAssociatedDevice(context.Background(), req)
	assert.NoError(t, err)
	assert.NotNil(t, resp)

	// Verify state
	allocationInfo = stateImpl.GetAllocationInfo(testCustomDevicePluginName, podUID, testName)
	assert.NotNil(t, allocationInfo)

	allocationInfo = stateImpl.GetAllocationInfo(testCustomDevicePluginName2, podUID, testName)
	assert.NotNil(t, allocationInfo)
}

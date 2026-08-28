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

package baseplugin

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"
	"k8s.io/kubernetes/pkg/kubelet/checkpointmanager"
	"k8s.io/kubernetes/pkg/kubelet/cm/devicemanager/checkpoint"

	apiconsts "github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	gpuconsts "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/state"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/qrm/statedirectory"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	metaagent "github.com/kubewharf/katalyst-core/pkg/metaserver/agent"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/pod"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
	"github.com/kubewharf/katalyst-core/pkg/util/native"
)

type mockMetricEmitter struct {
	metrics.DummyMetrics
	storedInt64 map[string][]int64
	storedTags  map[string][][]metrics.MetricTag
}

func newMockMetricEmitter() *mockMetricEmitter {
	return &mockMetricEmitter{
		storedInt64: make(map[string][]int64),
		storedTags:  make(map[string][][]metrics.MetricTag),
	}
}

func (m *mockMetricEmitter) StoreInt64(key string, val int64, _ metrics.MetricTypeName, tags ...metrics.MetricTag) error {
	m.storedInt64[key] = append(m.storedInt64[key], val)
	m.storedTags[key] = append(m.storedTags[key], tags)
	return nil
}

type trackingGPUState struct {
	state.State

	setAllocationInfoCalls    int
	setPodResourceEntriesCall int
	getAllocationInfoCalls    int
}

func (s *trackingGPUState) SetAllocationInfo(
	resourceName v1.ResourceName, podUID, containerName string, allocationInfo *state.AllocationInfo, persist bool,
) {
	s.setAllocationInfoCalls++
	s.State.SetAllocationInfo(resourceName, podUID, containerName, allocationInfo, persist)
}

func (s *trackingGPUState) SetPodResourceEntries(podResourceEntries state.PodResourceEntries, persist bool) {
	s.setPodResourceEntriesCall++
	s.State.SetPodResourceEntries(podResourceEntries, persist)
}

func (s *trackingGPUState) GetAllocationInfo(
	resourceName v1.ResourceName, podUID, containerName string,
) *state.AllocationInfo {
	s.getAllocationInfoCalls++
	return s.State.GetAllocationInfo(resourceName, podUID, containerName)
}

func TestHydrateKubeletGPUAllocations(t *testing.T) {
	t.Parallel()

	const (
		keptLabelKey               = "kept-label"
		mainContainerAnnotationKey = "main-container"
	)

	tests := []struct {
		name                            string
		enableKubeletCheckpointFallback bool
		existingState                   *state.AllocationInfo
		deviceNames                     []string
		checkpointEntries               []checkpoint.PodDevicesEntry
		wantImported                    bool
	}{
		{
			name:                            "imports kubelet-only allocation",
			enableKubeletCheckpointFallback: true,
			wantImported:                    true,
		},
		{
			name: "skips kubelet allocation when fallback is disabled",
		},
		{
			name:                            "keeps existing QRM allocation",
			enableKubeletCheckpointFallback: true,
			existingState: &state.AllocationInfo{
				AllocationMeta: commonstate.AllocationMeta{
					PodUid:        "pod-uid",
					PodNamespace:  "default",
					PodName:       "pod",
					ContainerName: "container",
				},
				TopologyAwareAllocations: map[string]state.Allocation{
					"gpu-0": {Quantity: 1, NUMANodes: []int{0}},
				},
				AllocatedAllocation: state.Allocation{Quantity: 1, NUMANodes: []int{0}},
			},
			wantImported: false,
		},
		{
			name:                            "imports allocations from multiple resources",
			enableKubeletCheckpointFallback: true,
			deviceNames:                     []string{"test-gpu-0", "test-gpu-1"},
			checkpointEntries: []checkpoint.PodDevicesEntry{
				{
					PodUID: "pod-uid", ContainerName: "container", ResourceName: "test-gpu-0",
					DeviceIDs: checkpoint.DevicesPerNUMA{0: {"gpu-0"}},
				},
				{
					PodUID: "pod-uid", ContainerName: "container", ResourceName: "test-gpu-1",
					DeviceIDs: checkpoint.DevicesPerNUMA{1: {"gpu-1"}},
				},
			},
			wantImported: true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			conf := generateTestConfiguration(t)
			if len(tt.deviceNames) == 0 {
				tt.deviceNames = []string{"test-gpu"}
			}
			conf.GPUDeviceNames = tt.deviceNames
			conf.EnableKubeletCheckpointFallback = tt.enableKubeletCheckpointFallback
			conf.PodLabelKeptKeys = []string{keptLabelKey}
			conf.MainContainerAnnotationKey = mainContainerAnnotationKey
			base := &BasePlugin{
				Conf:                                  conf,
				Emitter:                               metrics.DummyMetrics{},
				MetaServer:                            &metaserver.MetaServer{},
				DeviceTopologyRegistry:                machine.NewDeviceTopologyRegistry(metrics.DummyMetrics{}),
				DefaultResourceStateGeneratorRegistry: state.NewDefaultResourceStateGeneratorRegistry(),
				PodAnnotationKeptKeys:                 conf.PodAnnotationKeptKeys,
				PodLabelKeptKeys:                      conf.PodLabelKeptKeys,
			}
			base.MetaServer.MetaAgent = &metaagent.MetaAgent{
				PodFetcher: &pod.PodFetcherStub{PodList: []*v1.Pod{{
					ObjectMeta: metav1.ObjectMeta{
						UID:       "pod-uid",
						Namespace: "default",
						Name:      "pod",
						Annotations: map[string]string{
							apiconsts.PodAnnotationQoSLevelKey:           apiconsts.PodAnnotationQoSLevelSharedCores,
							apiconsts.PodAnnotationAggregatedRequestsKey: "kept-annotation-value",
							mainContainerAnnotationKey:                   "container",
							"ignored-annotation":                         "ignored",
						},
						Labels: map[string]string{
							apiconsts.PodAnnotationQoSLevelKey: apiconsts.PodAnnotationQoSLevelSharedCores,
							keptLabelKey:                       "kept-label-value",
							"ignored-label":                    "ignored",
						},
					},
					Spec: v1.PodSpec{
						Containers: []v1.Container{
							{Name: "sidecar"},
							{Name: "container"},
						},
					},
				}}},
			}
			for index, deviceName := range conf.GPUDeviceNames {
				base.DeviceTopologyRegistry.RegisterDeviceTopologyProvider(deviceName, machine.NewDeviceTopologyProvider())
				require.NoError(t, base.DeviceTopologyRegistry.SetDeviceTopology(deviceName, &machine.DeviceTopology{
					Devices: map[string]machine.DeviceInfo{
						fmt.Sprintf("gpu-%d", index): {NumaNodes: []int{index}},
					},
				}))
			}
			base.DefaultResourceStateGeneratorRegistry.RegisterResourceStateGenerator(
				gpuconsts.GPUDeviceType,
				state.NewGenericDefaultResourceStateGenerator(
					conf.GPUDeviceNames, base.DeviceTopologyRegistry, 1, true,
				),
			)
			stateImpl, err := state.NewGPUPluginState(conf.QRMPluginsConfiguration, base.DefaultResourceStateGeneratorRegistry)
			require.NoError(t, err)
			if len(conf.GPUDeviceNames) > 1 {
				stateImpl.SetResourceState(gpuconsts.GPUDeviceType, state.AllocationMap{
					"gpu-0": {Allocatable: 1},
					"gpu-1": {Allocatable: 1},
				}, false)
			}
			if tt.existingState != nil {
				stateImpl.SetAllocationInfo(gpuconsts.GPUDeviceType, "pod-uid", "container", tt.existingState, false)
			}
			trackingState := &trackingGPUState{State: stateImpl}

			manager, err := checkpointmanager.NewCheckpointManager(conf.KubeletDevicePluginPath)
			require.NoError(t, err)
			base.kubeletCheckpointManager = manager
			checkpointEntries := tt.checkpointEntries
			if len(checkpointEntries) == 0 {
				checkpointEntries = []checkpoint.PodDevicesEntry{{
					PodUID: "pod-uid", ContainerName: "container", ResourceName: "test-gpu",
					DeviceIDs: checkpoint.DevicesPerNUMA{0: {"gpu-0"}},
				}}
			}
			require.NoError(t, manager.CreateCheckpoint(native.KubeletDeviceManagerCheckpoint, checkpoint.New(
				checkpointEntries, nil,
			)))

			require.NoError(t, base.hydrateKubeletGPUAllocations(trackingState))
			allocation := stateImpl.GetAllocationInfo(gpuconsts.GPUDeviceType, "pod-uid", "container")
			assert.Zero(t, trackingState.getAllocationInfoCalls)
			if tt.wantImported {
				require.NotNil(t, allocation)
				if len(tt.deviceNames) == 1 {
					assert.Equal(t, "test-gpu", allocation.DeviceName)
				}
				assert.Equal(t, state.Allocation{Quantity: 1, NUMANodes: []int{0}},
					allocation.TopologyAwareAllocations["gpu-0"])
				if len(tt.deviceNames) > 1 {
					assert.Equal(t, state.Allocation{Quantity: 1, NUMANodes: []int{1}},
						allocation.TopologyAwareAllocations["gpu-1"])
					assert.Equal(t, float64(2), allocation.AllocatedAllocation.Quantity)
				}
				assert.Equal(t, commonstate.AllocationMeta{
					PodUid:         "pod-uid",
					PodNamespace:   "default",
					PodName:        "pod",
					ContainerName:  "container",
					ContainerType:  pluginapi.ContainerType_MAIN.String(),
					ContainerIndex: 1,
					Annotations: map[string]string{
						apiconsts.PodAnnotationQoSLevelKey:           apiconsts.PodAnnotationQoSLevelSharedCores,
						apiconsts.PodAnnotationAggregatedRequestsKey: "kept-annotation-value",
					},
					Labels: map[string]string{
						apiconsts.PodAnnotationQoSLevelKey: apiconsts.PodAnnotationQoSLevelSharedCores,
						keptLabelKey:                       "kept-label-value",
					},
					QoSLevel: apiconsts.PodAnnotationQoSLevelSharedCores,
				}, allocation.AllocationMeta)
				assert.Zero(t, trackingState.setAllocationInfoCalls)
				assert.Equal(t, 1, trackingState.setPodResourceEntriesCall)
			} else if tt.existingState != nil {
				assert.Equal(t, tt.existingState, allocation)
			} else {
				assert.Nil(t, allocation)
			}
		})
	}
}

func TestReadKubeletGPUAllocationsWithoutCheckpointManager(t *testing.T) {
	t.Parallel()

	emitter := newMockMetricEmitter()
	base := &BasePlugin{
		Conf:    generateTestConfiguration(t),
		Emitter: emitter,
	}

	allocations, err := base.readKubeletGPUAllocations()

	assert.Nil(t, allocations)
	require.ErrorContains(t, err, "kubelet checkpoint manager is nil")
	assert.Equal(t, []int64{1}, emitter.storedInt64[metricKubeletGPUCheckpointReadFailed])
}

func TestBasePlugin_InitStateEmitsMetricWhenKubeletHydrationFails(t *testing.T) {
	t.Parallel()

	conf := generateTestConfiguration(t)
	conf.EnableKubeletCheckpointFallback = true
	conf.StateDirectoryConfiguration = &statedirectory.StateDirectoryConfiguration{
		StateFileDirectory: t.TempDir(),
	}
	conf.KubeletDevicePluginPath = filepath.Join(t.TempDir(), "not-a-directory")
	require.NoError(t, os.WriteFile(conf.KubeletDevicePluginPath, []byte("file"), 0o644))

	emitter := newMockMetricEmitter()
	base := &BasePlugin{
		Conf:                                  conf,
		Emitter:                               emitter,
		DefaultResourceStateGeneratorRegistry: state.NewDefaultResourceStateGeneratorRegistry(),
	}

	require.Error(t, base.InitState())
	assert.Equal(t, []int64{1}, emitter.storedInt64[metricKubeletGPUHydrateFailed])
	require.Len(t, emitter.storedTags[metricKubeletGPUHydrateFailed], 1)
	assert.Equal(t, "error_message", emitter.storedTags[metricKubeletGPUHydrateFailed][0][0].Key)
}

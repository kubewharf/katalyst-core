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
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/kubernetes/pkg/kubelet/checkpointmanager"
	"k8s.io/kubernetes/pkg/kubelet/cm/devicemanager/checkpoint"

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

func TestHydrateKubeletGPUAllocations(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                            string
		enableKubeletCheckpointFallback bool
		existingState                   *state.AllocationInfo
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
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			conf := generateTestConfiguration(t)
			conf.GPUDeviceNames = []string{"test-gpu"}
			conf.EnableKubeletCheckpointFallback = tt.enableKubeletCheckpointFallback
			base := &BasePlugin{
				Conf:                                  conf,
				Emitter:                               metrics.DummyMetrics{},
				MetaServer:                            &metaserver.MetaServer{},
				DeviceTopologyRegistry:                machine.NewDeviceTopologyRegistry(metrics.DummyMetrics{}),
				DefaultResourceStateGeneratorRegistry: state.NewDefaultResourceStateGeneratorRegistry(),
			}
			base.MetaServer.MetaAgent = &metaagent.MetaAgent{
				PodFetcher: &pod.PodFetcherStub{PodList: []*v1.Pod{{
					ObjectMeta: metav1.ObjectMeta{
						UID: "pod-uid", Namespace: "default", Name: "pod",
					},
				}}},
			}
			base.DeviceTopologyRegistry.RegisterDeviceTopologyProvider("test-gpu", machine.NewDeviceTopologyProvider())
			require.NoError(t, base.DeviceTopologyRegistry.SetDeviceTopology("test-gpu", &machine.DeviceTopology{
				Devices: map[string]machine.DeviceInfo{"gpu-0": {NumaNodes: []int{0}}},
			}))
			base.DefaultResourceStateGeneratorRegistry.RegisterResourceStateGenerator(
				gpuconsts.GPUDeviceType,
				state.NewGenericDefaultResourceStateGenerator(
					conf.GPUDeviceNames, base.DeviceTopologyRegistry, 1, true,
				),
			)
			stateImpl, err := state.NewGPUPluginState(conf.QRMPluginsConfiguration, base.DefaultResourceStateGeneratorRegistry)
			require.NoError(t, err)
			if tt.existingState != nil {
				stateImpl.SetAllocationInfo(gpuconsts.GPUDeviceType, "pod-uid", "container", tt.existingState, false)
			}

			manager, err := checkpointmanager.NewCheckpointManager(conf.KubeletDevicePluginPath)
			require.NoError(t, err)
			require.NoError(t, manager.CreateCheckpoint(native.KubeletDeviceManagerCheckpoint, checkpoint.New(
				[]checkpoint.PodDevicesEntry{{
					PodUID: "pod-uid", ContainerName: "container", ResourceName: "test-gpu",
					DeviceIDs: checkpoint.DevicesPerNUMA{0: {"gpu-0"}},
				}}, nil,
			)))

			require.NoError(t, base.hydrateKubeletGPUAllocations(stateImpl))
			allocation := stateImpl.GetAllocationInfo(gpuconsts.GPUDeviceType, "pod-uid", "container")
			if tt.wantImported {
				require.NotNil(t, allocation)
				assert.Equal(t, "test-gpu", allocation.DeviceName)
				assert.Equal(t, state.Allocation{Quantity: 1, NUMANodes: []int{0}},
					allocation.TopologyAwareAllocations["gpu-0"])
			} else if tt.existingState != nil {
				assert.Equal(t, tt.existingState, allocation)
			} else {
				assert.Nil(t, allocation)
			}
		})
	}
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

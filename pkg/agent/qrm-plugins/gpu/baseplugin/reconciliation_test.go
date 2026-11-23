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
	"testing"

	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/kubernetes/pkg/kubelet/checkpointmanager"
	"k8s.io/kubernetes/pkg/kubelet/cm/devicemanager/checkpoint"

	"github.com/kubewharf/katalyst-api/pkg/consts"
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

func TestInitStateHydrationRetainsRestoredMissingGPU(t *testing.T) {
	t.Parallel()
	conf := generateTestConfiguration(t)
	conf.GPUDeviceNames = []string{"test-gpu"}
	conf.EnableKubeletCheckpointFallback = true
	conf.StateDirectoryConfiguration = &statedirectory.StateDirectoryConfiguration{StateFileDirectory: t.TempDir()}
	registry := machine.NewDeviceTopologyRegistry(metrics.DummyMetrics{})
	registry.RegisterDeviceTopologyProvider("test-gpu", machine.NewDeviceTopologyProvider())
	require.NoError(t, registry.SetDeviceTopology("test-gpu", &machine.DeviceTopology{
		Devices: map[string]machine.DeviceInfo{"gpu-0": {NumaNodes: []int{0}}, "gpu-lost": {NumaNodes: []int{1}}},
	}))
	generators := state.NewDefaultResourceStateGeneratorRegistry()
	generator := state.NewGenericDefaultResourceStateGenerator(conf.GPUDeviceNames, registry, 1, true)
	generators.RegisterResourceStateGenerator(gpuconsts.GPUDeviceType, generator)

	// Persist an allocation before a device disappears; this is a real QRM checkpoint.
	oldOwner := &state.AllocationInfo{
		AllocationMeta:           commonstate.AllocationMeta{PodUid: "restored-pod", ContainerName: "c"},
		DeviceName:               "test-gpu",
		AllocatedAllocation:      state.Allocation{Quantity: 1, NUMANodes: []int{1}},
		TopologyAwareAllocations: map[string]state.Allocation{"gpu-lost": {Quantity: 1, NUMANodes: []int{1}}},
	}
	oldState, err := state.NewCheckpointState(conf.StateDirectoryConfiguration, conf.QRMPluginsConfiguration,
		GPUPluginStateFileName, gpuconsts.GPUResourcePluginPolicyNameStatic, generators, false, metrics.DummyMetrics{})
	require.NoError(t, err)
	oldState.SetAllocationInfo(gpuconsts.GPUDeviceType, "restored-pod", "c", oldOwner, false)
	devices, err := state.GenerateResourceStateFromPodEntries(oldState.GetPodEntries(gpuconsts.GPUDeviceType), generator, nil)
	require.NoError(t, err)
	oldState.SetResourceState(gpuconsts.GPUDeviceType, devices, false)
	require.NoError(t, oldState.StoreState())
	require.NoError(t, registry.SetDeviceTopology("test-gpu", &machine.DeviceTopology{
		Devices: map[string]machine.DeviceInfo{"gpu-0": {NumaNodes: []int{0}}},
	}))

	// Kubelet knows another live pod on the remaining GPU. Include the old pod's
	// checkpoint entry too, to verify deduplication does not discard its QRM owner.
	kubeletManager, err := checkpointmanager.NewCheckpointManager(conf.KubeletDevicePluginPath)
	require.NoError(t, err)
	require.NoError(t, kubeletManager.CreateCheckpoint(native.KubeletDeviceManagerCheckpoint, checkpoint.New([]checkpoint.PodDevicesEntry{
		{PodUID: "new-pod", ContainerName: "c", ResourceName: "test-gpu", DeviceIDs: checkpoint.DevicesPerNUMA{0: {"gpu-0"}}},
		{PodUID: "restored-pod", ContainerName: "c", ResourceName: "test-gpu", DeviceIDs: checkpoint.DevicesPerNUMA{1: {"gpu-lost"}}},
	}, nil)))
	activePods := make([]*v1.Pod, 0, 2)
	for _, uid := range []string{"restored-pod", "new-pod"} {
		activePods = append(activePods, &v1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				UID: types.UID(uid), Name: uid, Namespace: "default",
				Annotations: map[string]string{consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores},
			},
			Spec: v1.PodSpec{Containers: []v1.Container{{Name: "c"}}},
		})
	}
	base := &BasePlugin{
		Conf: conf, Emitter: metrics.DummyMetrics{},
		MetaServer:             &metaserver.MetaServer{MetaAgent: &metaagent.MetaAgent{PodFetcher: &pod.PodFetcherStub{PodList: activePods}}},
		DeviceTopologyRegistry: registry, DefaultResourceStateGeneratorRegistry: generators,
		kubeletCheckpointManager: kubeletManager, stateInitializedCh: make(chan struct{}),
	}
	require.Nil(t, base.GetState()) // InitState must use the restored stateImpl before installing p.state.
	require.NoError(t, base.InitState())
	current := base.GetState()
	require.NotNil(t, current)
	require.Equal(t, oldOwner, current.GetAllocationInfo(gpuconsts.GPUDeviceType, "restored-pod", "c"))
	imported := current.GetAllocationInfo(gpuconsts.GPUDeviceType, "new-pod", "c")
	require.NotNil(t, imported)
	require.Equal(t, state.Allocation{Quantity: 1, NUMANodes: []int{0}}, imported.TopologyAwareAllocations["gpu-0"])
	machineState := current.GetMachineState()[gpuconsts.GPUDeviceType]
	require.Len(t, machineState, 2)
	require.Equal(t, float64(1), machineState["gpu-lost"].Allocatable)
	require.Equal(t, float64(1), machineState["gpu-lost"].GetQuantityAllocated())
	require.Equal(t, float64(1), machineState["gpu-0"].GetQuantityAllocated())

	// Confirm hydration persisted both owners; a subsequent restore loses neither.
	restored, err := state.NewCheckpointState(conf.StateDirectoryConfiguration, conf.QRMPluginsConfiguration,
		GPUPluginStateFileName, gpuconsts.GPUResourcePluginPolicyNameStatic, generators, false, metrics.DummyMetrics{})
	require.NoError(t, err)
	require.Equal(t, current.GetMachineState(), restored.GetMachineState())
	require.Equal(t, current.GetPodResourceEntries(), restored.GetPodResourceEntries())
}

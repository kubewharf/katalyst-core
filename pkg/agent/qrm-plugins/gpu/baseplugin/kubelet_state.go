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
	"context"
	"fmt"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/kubernetes/pkg/kubelet/checkpointmanager"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	gpuconsts "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/kubelet"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/state"
	metaserverpod "github.com/kubewharf/katalyst-core/pkg/metaserver/agent/pod"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
	"github.com/kubewharf/katalyst-core/pkg/util/native"
)

const (
	metricKubeletGPUCheckpointReadFailed = "qrm_gpu_init_kubelet_checkpoint_read_failed"
	metricKubeletGPUHydrateFailed        = "qrm_gpu_init_kubelet_hydrate_failed"
)

type kubeletGPUAllocationKey struct {
	podUID        string
	containerName string
}

// hydrateKubeletGPUAllocations updates katalyst GPU state with allocations from the kubelet device manager.
// Allocations that are present in both the kubelet device manager and the katalyst GPU state are deduplicated.
func (p *BasePlugin) hydrateKubeletGPUAllocations(stateImpl state.State) error {
	if !p.Conf.EnableKubeletCheckpointFallback || p.Conf.KubeletDevicePluginPath == "" {
		return nil
	}

	checkpointManager, err := checkpointmanager.NewCheckpointManager(p.Conf.KubeletDevicePluginPath)
	if err != nil {
		return fmt.Errorf("new kubelet checkpoint manager failed: %w", err)
	}

	// Read allocations from the kubelet device manager.
	allocations, err := kubelet.ReadAllocations(checkpointManager, sets.NewString(p.Conf.GPUDeviceNames...))
	if err != nil {
		p.storeKubeletGPUInitMetric(metricKubeletGPUCheckpointReadFailed)
		return err
	}
	if len(allocations) == 0 {
		return nil
	}

	activePods, err := p.MetaServer.GetPodList(
		context.WithValue(context.Background(), metaserverpod.BypassCacheKey, metaserverpod.BypassCacheTrue),
		native.PodIsActive,
	)
	if err != nil {
		general.Warningf("failed to get active pod list for kubelet GPU state hydration: %v", err)
		return nil
	}
	activePodMap := native.GetPodKeyMap(activePods, func(obj metav1.Object) string {
		return string(obj.GetUID())
	})

	machineState := stateImpl.GetMachineState()
	gpuState, ok := machineState[v1.ResourceName(gpuconsts.GPUDeviceType)]
	if !ok {
		return fmt.Errorf("GPU device state %q is not initialized", gpuconsts.GPUDeviceType)
	}

	gpuTopology, _, err := p.DeviceTopologyRegistry.GetLatestDeviceTopology(p.Conf.GPUDeviceNames)
	if err != nil {
		return fmt.Errorf("get GPU topology for kubelet state hydration failed: %w", err)
	}

	// Convert kubelet allocations to be stored in katalyst GPU state.
	imported := make(map[kubeletGPUAllocationKey]*state.AllocationInfo)
	for _, entry := range allocations {
		key := kubeletGPUAllocationKey{podUID: entry.PodUID, containerName: entry.ContainerName}
		// Do nothing if the allocation is already in the katalyst GPU state.
		if stateImpl.GetAllocationInfo(v1.ResourceName(gpuconsts.GPUDeviceType), entry.PodUID, entry.ContainerName) != nil {
			continue
		}

		pod, ok := activePodMap[entry.PodUID]
		if !ok {
			general.Infof("pod %s is inactive, skipping", entry.PodUID)
			continue
		}

		allocationInfo := imported[key]
		if allocationInfo == nil {
			allocationInfo = &state.AllocationInfo{
				AllocationMeta: commonstate.AllocationMeta{
					PodUid:        entry.PodUID,
					PodNamespace:  pod.Namespace,
					PodName:       pod.Name,
					ContainerName: entry.ContainerName,
				},
				DeviceName:               entry.ResourceName,
				TopologyAwareAllocations: make(map[string]state.Allocation),
			}
			imported[key] = allocationInfo
		}

		for _, deviceID := range entry.DeviceIDs {
			if _, exists := allocationInfo.TopologyAwareAllocations[deviceID]; exists {
				continue
			}
			if _, exists := gpuState[deviceID]; !exists {
				general.Infof("GPU state does not contain device %s, skipping", deviceID)
				continue
			}
			device, exists := gpuTopology.Devices[deviceID]
			if !exists {
				general.Infof("GPU topology does not contain device %s, skipping", deviceID)
				continue
			}
			allocationInfo.TopologyAwareAllocations[deviceID] = state.Allocation{
				Quantity:  1,
				NUMANodes: append([]int(nil), device.NumaNodes...),
			}
			allocationInfo.AllocatedAllocation.Quantity++
		}
	}

	importedCount := 0
	for key, allocationInfo := range imported {
		if len(allocationInfo.TopologyAwareAllocations) == 0 {
			continue
		}
		numaNodes := machine.NewCPUSet()
		for _, allocation := range allocationInfo.TopologyAwareAllocations {
			numaNodes.Add(allocation.NUMANodes...)
		}
		allocationInfo.AllocatedAllocation.NUMANodes = numaNodes.ToSliceInt()
		stateImpl.SetAllocationInfo(
			v1.ResourceName(gpuconsts.GPUDeviceType),
			key.podUID,
			key.containerName,
			allocationInfo,
			false,
		)

		general.InfoS("Successfully imported allocation %s/%s", key.podUID, key.containerName)

		importedCount++
	}
	if importedCount == 0 {
		return nil
	}

	// Rebuild and store state using the newly imported kubelet allocations.
	podEntries := stateImpl.GetPodEntries(v1.ResourceName(gpuconsts.GPUDeviceType))
	rebuiltState, err := p.GenerateResourceStateFromPodEntries(gpuconsts.GPUDeviceType, podEntries)
	if err != nil {
		return fmt.Errorf("rebuild GPU state after kubelet hydration failed: %w", err)
	}
	stateImpl.SetResourceState(v1.ResourceName(gpuconsts.GPUDeviceType), rebuiltState, false)
	if err := stateImpl.StoreState(); err != nil {
		return fmt.Errorf("persist GPU state after kubelet hydration failed: %w", err)
	}
	return nil
}

func (p *BasePlugin) storeKubeletGPUInitMetric(name string) {
	if p.Emitter != nil {
		_ = p.Emitter.StoreInt64(name, 1, metrics.MetricTypeNameCount)
	}
}

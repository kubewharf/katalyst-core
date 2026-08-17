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

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	gpuconsts "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/kubelet"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/state"
	qrmutil "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/util"
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

	allocations, err := p.readKubeletGPUAllocations()
	if err != nil {
		return err
	}
	if len(allocations) == 0 {
		return nil
	}

	activePodMap, err := p.getActivePodMapForKubeletGPUHydration()
	if err != nil {
		general.Errorf("failed to get active pod list for kubelet GPU state hydration: %v", err)
		return fmt.Errorf("failed to get active pod list for kubelet GPU state hydration: %w", err)
	}

	gpuState, gpuTopologies, err := p.getGPUStateAndTopologyForKubeletHydration(stateImpl)
	if err != nil {
		return err
	}

	podResourceEntries, podEntries := getGPUPodEntriesForKubeletHydration(stateImpl)
	imported := p.collectKubeletGPUAllocations(allocations, activePodMap, podEntries, gpuState, gpuTopologies)
	if applyKubeletGPUAllocations(podEntries, imported) == 0 {
		return nil
	}

	return p.rebuildAndStoreGPUState(stateImpl, podResourceEntries, podEntries)
}

// readKubeletGPUAllocations reads GPU allocations from the kubelet device manager checkpoint.
// It records the checkpoint-read failure metric when checkpoint parsing fails.
func (p *BasePlugin) readKubeletGPUAllocations() ([]kubelet.Allocation, error) {
	if p.kubeletCheckpointManager == nil {
		p.storeKubeletGPUInitMetric(metricKubeletGPUCheckpointReadFailed)
		return nil, fmt.Errorf("kubelet checkpoint manager is nil")
	}

	allocations, err := kubelet.ReadAllocations(p.kubeletCheckpointManager, sets.NewString(p.Conf.GPUDeviceNames...))
	if err != nil {
		p.storeKubeletGPUInitMetric(metricKubeletGPUCheckpointReadFailed)
		return nil, err
	}
	return allocations, nil
}

// getActivePodMapForKubeletGPUHydration fetches active pods directly from metaserver.
// It returns the pods keyed by UID so checkpoint allocations can be matched to live pods.
func (p *BasePlugin) getActivePodMapForKubeletGPUHydration() (map[string]*v1.Pod, error) {
	activePods, err := p.MetaServer.GetPodList(
		context.WithValue(context.Background(), metaserverpod.BypassCacheKey, metaserverpod.BypassCacheTrue),
		native.PodIsActive,
	)
	if err != nil {
		return nil, err
	}
	return native.GetPodKeyMap(activePods, func(obj metav1.Object) string {
		return string(obj.GetUID())
	}), nil
}

// getGPUStateAndTopologyForKubeletHydration loads the current GPU resource state and each configured GPU topology.
// These are used to validate checkpoint device IDs before importing them into Katalyst state.
func (p *BasePlugin) getGPUStateAndTopologyForKubeletHydration(
	stateImpl state.State,
) (state.AllocationMap, map[string]*machine.DeviceTopology, error) {
	machineState := stateImpl.GetMachineState()
	gpuState, ok := machineState[v1.ResourceName(gpuconsts.GPUDeviceType)]
	if !ok {
		return nil, nil, fmt.Errorf("GPU device state %q is not initialized", gpuconsts.GPUDeviceType)
	}

	gpuTopologies, ok := p.DeviceTopologyRegistry.GetDeviceTopologies(p.Conf.GPUDeviceNames)
	if !ok {
		return nil, nil, fmt.Errorf("get GPU topologies for kubelet state hydration failed")
	}
	return gpuState, gpuTopologies, nil
}

// getGPUPodEntriesForKubeletHydration returns cloned pod-resource entries and the GPU pod entries within them.
// It initializes missing maps so callers can safely add imported kubelet allocations.
func getGPUPodEntriesForKubeletHydration(stateImpl state.State) (state.PodResourceEntries, state.PodEntries) {
	resourceName := v1.ResourceName(gpuconsts.GPUDeviceType)
	podResourceEntries := stateImpl.GetPodResourceEntries()
	if podResourceEntries == nil {
		podResourceEntries = make(state.PodResourceEntries)
	}
	podEntries := podResourceEntries[resourceName]
	if podEntries == nil {
		podEntries = make(state.PodEntries)
		podResourceEntries[resourceName] = podEntries
	}
	return podResourceEntries, podEntries
}

// collectKubeletGPUAllocations converts kubelet checkpoint allocations into GPU allocation infos.
// It skips inactive pods, existing Katalyst allocations, duplicate devices, and devices missing from current state or topology.
func (p *BasePlugin) collectKubeletGPUAllocations(
	allocations []kubelet.Allocation,
	activePodMap map[string]*v1.Pod,
	podEntries state.PodEntries,
	gpuState state.AllocationMap,
	gpuTopologies map[string]*machine.DeviceTopology,
) map[kubeletGPUAllocationKey]*state.AllocationInfo {
	imported := make(map[kubeletGPUAllocationKey]*state.AllocationInfo)
	for _, entry := range allocations {
		key := kubeletGPUAllocationKey{podUID: entry.PodUID, containerName: entry.ContainerName}
		// Do nothing if the allocation is already in the katalyst GPU state.
		if podEntries.GetAllocationInfo(entry.PodUID, entry.ContainerName) != nil {
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
				AllocationMeta:           p.generateKubeletGPUAllocationMeta(pod, entry.ContainerName),
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
			gpuTopology, ok := gpuTopologies[entry.ResourceName]
			if !ok {
				general.Infof("GPU topology does not exist for resource %s, skipping device %s",
					entry.ResourceName, deviceID)
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
	return imported
}

// applyKubeletGPUAllocations writes validated kubelet allocations into the cloned GPU pod entries.
// It finalizes each allocation's aggregated NUMA nodes and returns the number of imported allocations.
func applyKubeletGPUAllocations(
	podEntries state.PodEntries,
	imported map[kubeletGPUAllocationKey]*state.AllocationInfo,
) int {
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
		podEntries.SetAllocationInfo(key.podUID, key.containerName, allocationInfo)

		general.Infof("Successfully imported allocation %s/%s", key.podUID, key.containerName)

		importedCount++
	}
	return importedCount
}

// rebuildAndStoreGPUState rebuilds GPU resource state from updated pod entries and stores both state views.
// It persists the checkpoint once after pod entries and resource state have been updated together.
func (p *BasePlugin) rebuildAndStoreGPUState(
	stateImpl state.State,
	podResourceEntries state.PodResourceEntries,
	podEntries state.PodEntries,
) error {
	rebuiltState, err := p.GenerateResourceStateFromPodEntries(gpuconsts.GPUDeviceType, podEntries)
	if err != nil {
		return fmt.Errorf("rebuild GPU state after kubelet hydration failed: %w", err)
	}
	stateImpl.SetPodResourceEntries(podResourceEntries, false)
	stateImpl.SetResourceState(v1.ResourceName(gpuconsts.GPUDeviceType), rebuiltState, false)
	if err := stateImpl.StoreState(); err != nil {
		return fmt.Errorf("persist GPU state after kubelet hydration failed: %w", err)
	}

	general.Infof("Successfully restored GPU state after kubelet hydration")
	return nil
}

// generateKubeletGPUAllocationMeta generates the important metadata for a kubelet GPU allocation.
// This includes whitelisted pod annotations and labels and container types and container indexes.
func (p *BasePlugin) generateKubeletGPUAllocationMeta(pod *v1.Pod, containerName string) commonstate.AllocationMeta {
	qosLevel, err := p.Conf.QoSConfiguration.GetQoSLevelForPod(pod)
	if err != nil {
		general.Warningf("failed to get QoS level for pod %s/%s during kubelet GPU state hydration: %v",
			pod.Namespace, pod.Name, err)
	}

	annotations, labels := qrmutil.FilterQoSRelatedLabelsAndAnnotations(
		p.Conf.QoSConfiguration,
		general.DeepCopyMap(pod.Annotations),
		general.DeepCopyMap(pod.Labels),
		qosLevel,
		p.PodAnnotationKeptKeys,
		p.PodLabelKeptKeys,
	)

	containerType := ""
	var containerIndex uint64
	resolvedContainerType, resolvedContainerIndex, err := qrmutil.GetContainerTypeAndIndex(
		pod, containerName, p.Conf.MainContainerAnnotationKey,
	)
	if err != nil {
		general.Warningf("failed to get container type and index for pod %s/%s container %s: %v",
			pod.Namespace, pod.Name, containerName, err)
	} else {
		containerType = resolvedContainerType.String()
		containerIndex = resolvedContainerIndex
	}

	return commonstate.AllocationMeta{
		PodUid:         string(pod.UID),
		PodNamespace:   pod.Namespace,
		PodName:        pod.Name,
		ContainerName:  containerName,
		ContainerType:  containerType,
		ContainerIndex: containerIndex,
		Labels:         labels,
		Annotations:    annotations,
		QoSLevel:       qosLevel,
	}
}

func (p *BasePlugin) storeKubeletGPUInitMetric(name string) {
	if p.Emitter != nil {
		_ = p.Emitter.StoreInt64(name, 1, metrics.MetricTypeNameCount)
	}
}

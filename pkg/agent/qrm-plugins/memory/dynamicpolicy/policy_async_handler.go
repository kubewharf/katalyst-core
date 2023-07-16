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

package dynamicpolicy

import (
	"context"
	"fmt"
	"time"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/sets"

	"github.com/kubewharf/katalyst-api/pkg/consts"
	apiconsts "github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/memory/dynamicpolicy/state"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/util"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/cgroup/common"
	cgroupmgr "github.com/kubewharf/katalyst-core/pkg/util/cgroup/manager"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

// setExtraControlKnobByConfigForAllocationInfo sets control knob entry for container,
// if the container doesn't have the entry in the checkpoint.
// pod specified value has higher priority than config value.
func setExtraControlKnobByConfigForAllocationInfo(allocationInfo *state.AllocationInfo, extraControlKnobConfigs commonstate.ExtraControlKnobConfigs, pod *v1.Pod) {
	if allocationInfo == nil {
		general.Errorf("nil allocationInfo")
		return
	} else if pod == nil {
		general.Errorf("nil allocationInfo")
		return
	}

	// v1.ResourceMemory is legacy control knob name for cpuset.mems,
	// it shouldn't be configured in extraControlKnobConfigs
	legacyControlKnobNames := sets.NewString(string(v1.ResourceMemory))

	for _, legacyControlKnobName := range legacyControlKnobNames.List() {
		if _, found := extraControlKnobConfigs[legacyControlKnobName]; found {
			general.Errorf("legacy control knob name: %s is configured", legacyControlKnobName)
			return
		}
	}

	if allocationInfo.ExtraControlKnobInfo == nil {
		allocationInfo.ExtraControlKnobInfo = make(map[string]commonstate.ControlKnobInfo)
	}

	for controlKnobName, configEntry := range extraControlKnobConfigs {

		if _, found := allocationInfo.ExtraControlKnobInfo[controlKnobName]; found {
			continue
		}

		clonedControlKnobInfo := configEntry.ControlKnobInfo.Clone()

		if specifiedValue, ok := pod.Annotations[configEntry.PodExplicitlyAnnotationKey]; ok &&
			configEntry.PodExplicitlyAnnotationKey != "" {
			clonedControlKnobInfo.ControlKnobValue = specifiedValue
		} else if qosLevelDefaultValue, ok := configEntry.QoSLevelToDefaultValue[allocationInfo.QoSLevel]; ok {
			clonedControlKnobInfo.ControlKnobValue = qosLevelDefaultValue
		}

		general.Infof("set extral control knob: %s by configs: %#v  for pod: %s/%s, container: %s",
			controlKnobName, clonedControlKnobInfo,
			allocationInfo.PodNamespace, allocationInfo.PodName,
			allocationInfo.ContainerName)

		allocationInfo.ExtraControlKnobInfo[controlKnobName] = clonedControlKnobInfo
	}
}

func (p *DynamicPolicy) setExtraControlKnobByConfigs() {
	general.Infof("called")

	if p.metaServer == nil {
		general.Errorf("nil metaServer")
		return
	} else if len(p.extraControlKnobConfigs) == 0 {
		general.Errorf("empty extraControlKnobConfigs, skip setExtraControlKnobByConfigs")
		return
	}

	podList, err := p.metaServer.GetPodList(context.Background(), nil)
	if err != nil {
		general.Errorf("get pod list failed, err: %v", err)
		return
	}

	p.Lock()
	defer p.Unlock()

	podResourceEntries := p.state.GetPodResourceEntries()
	podEntries := podResourceEntries[v1.ResourceMemory]

	for _, pod := range podList {
		if pod == nil {
			general.Errorf("get nil pod from metaServer")
			continue
		}

		podUID := string(pod.UID)

		for _, container := range pod.Spec.Containers {
			containerName := container.Name
			allocationInfo := podEntries[podUID][containerName]

			if allocationInfo == nil {
				general.Warningf("no entry for pod: %s/%s, container: %s", pod.Namespace, pod.Name, containerName)
				continue
			}

			setExtraControlKnobByConfigForAllocationInfo(allocationInfo, p.extraControlKnobConfigs, pod)
		}
	}

	resourcesMachineState, err := state.GenerateMachineStateFromPodEntries(p.state.GetMachineInfo(), podResourceEntries, p.state.GetReservedMemory())
	if err != nil {
		general.Errorf("GenerateMachineStateFromPodEntries failed with error: %v", err)
		return
	}

	p.state.SetPodResourceEntries(podResourceEntries)
	p.state.SetMachineState(resourcesMachineState)
}

func (p *DynamicPolicy) applyExternalCgroupParams() {
	general.Infof("called")

	podEntries := p.state.GetPodResourceEntries()[v1.ResourceMemory]

	for podUID, containerEntries := range podEntries {
		for containerName, allocationInfo := range containerEntries {
			if allocationInfo == nil {
				continue
			}

			containerID, err := p.metaServer.GetContainerID(podUID, containerName)
			if err != nil {
				general.Errorf("get container id of pod: %s/%s container: %s failed with error: %v",
					allocationInfo.PodNamespace, allocationInfo.PodName,
					allocationInfo.ContainerName, err)
				continue
			}

			for controlKnobName, entry := range allocationInfo.ExtraControlKnobInfo {
				if entry.CgroupSubsysName == "" || len(entry.CgroupVersionToIfaceName) == 0 {
					continue
				}

				var cgroupIfaceName string
				if common.CheckCgroup2UnifiedMode() {
					cgroupIfaceName = entry.CgroupVersionToIfaceName[apiconsts.CgroupV2]
				} else {
					cgroupIfaceName = entry.CgroupVersionToIfaceName[apiconsts.CgroupV1]
				}

				if cgroupIfaceName == "" {
					general.Errorf("pod: %s/%s container: %s control knob name: %s with empty cgroupIfaceName",
						allocationInfo.PodNamespace, allocationInfo.PodName,
						allocationInfo.ContainerName, controlKnobName)
					continue
				}

				general.InfoS("ApplyUnifiedDataForContainer",
					"podNamespace", allocationInfo.PodNamespace,
					"podName", allocationInfo.PodName,
					"containerName", allocationInfo.ContainerName,
					"controlKnobName", controlKnobName,
					"controlKnobValue", entry.ControlKnobValue,
					"cgroupSubsysName", entry.CgroupSubsysName,
					"cgroupIfaceName", cgroupIfaceName)

				err := cgroupmgr.ApplyUnifiedDataForContainer(podUID, containerID, entry.CgroupSubsysName, cgroupIfaceName, entry.ControlKnobValue)

				if err != nil {
					general.ErrorS(err, "ApplyUnifiedDataForContainer failed",
						"podNamespace", allocationInfo.PodNamespace,
						"podName", allocationInfo.PodName,
						"containerName", allocationInfo.ContainerName,
						"controlKnobName", controlKnobName,
						"controlKnobValue", entry.ControlKnobValue,
						"cgroupSubsysName", entry.CgroupSubsysName,
						"cgroupIfaceName", cgroupIfaceName)
				}
			}
		}
	}
}

// checkMemorySet emit errors if the memory allocation falls into unexpected results
func (p *DynamicPolicy) checkMemorySet() {
	general.Infof("called")

	podEntries := p.state.GetPodResourceEntries()[v1.ResourceMemory]
	actualMemorySets := make(map[string]map[string]machine.CPUSet)

	unionNUMABindingStateMemorySet := machine.NewCPUSet()
	for podUID, containerEntries := range podEntries {
		for containerName, allocationInfo := range containerEntries {
			if allocationInfo == nil || !allocationInfo.CheckMainContainer() {
				continue
			} else if allocationInfo.QoSLevel == consts.PodAnnotationQoSLevelSharedCores &&
				p.getContainerRequestedMemoryBytes(allocationInfo) == 0 {
				general.Warningf("skip memset checking for pod: %s/%s container: %s with zero cpu request",
					allocationInfo.PodNamespace, allocationInfo.PodName, containerName)
				continue
			} else if allocationInfo.CheckNumaBinding() {
				unionNUMABindingStateMemorySet = unionNUMABindingStateMemorySet.Union(allocationInfo.NumaAllocationResult)
			}

			tags := metrics.ConvertMapToTags(map[string]string{
				"podNamespace":  allocationInfo.PodNamespace,
				"podName":       allocationInfo.PodName,
				"containerName": allocationInfo.ContainerName,
			})

			containerID, err := p.metaServer.GetContainerID(podUID, containerName)
			if err != nil {
				general.Errorf("get container id of pod: %s container: %s failed with error: %v", podUID, containerName, err)
				continue
			}

			cpusetStats, err := cgroupmgr.GetCPUSetForContainer(podUID, containerID)
			if err != nil {
				general.Errorf("GetMemorySet of pod: %s container: name(%s), id(%s) failed with error: %v",
					podUID, containerName, containerID, err)
				_ = p.emitter.StoreInt64(util.MetricNameRealStateInvalid, 1, metrics.MetricTypeNameRaw, tags...)
				continue
			}

			if actualMemorySets[podUID] == nil {
				actualMemorySets[podUID] = make(map[string]machine.CPUSet)
			}
			actualMemorySets[podUID][containerName] = machine.MustParse(cpusetStats.Mems)

			general.Infof("pod: %s/%s, container: %s, state MemorySet: %s, actual MemorySet: %s",
				allocationInfo.PodNamespace, allocationInfo.PodName, allocationInfo.ContainerName,
				allocationInfo.NumaAllocationResult.String(), actualMemorySets[podUID][containerName].String())

			// only do comparison for dedicated_cores with numa_biding to avoid effect of adjustment for shared_cores
			if !allocationInfo.CheckNumaBinding() {
				continue
			}

			if !actualMemorySets[podUID][containerName].Equals(allocationInfo.NumaAllocationResult) {
				general.Errorf("pod: %s/%s, container: %s, memset invalid",
					allocationInfo.PodNamespace, allocationInfo.PodName, allocationInfo.ContainerName)
				_ = p.emitter.StoreInt64(util.MetricNameMemSetInvalid, 1, metrics.MetricTypeNameRaw, tags...)
			}
		}
	}

	unionNUMABindingActualMemorySet := machine.NewCPUSet()
	unionDedicatedActualMemorySet := machine.NewCPUSet()
	unionSharedActualMemorySet := machine.NewCPUSet()

	var memorySetOverlap bool
	for podUID, containerEntries := range actualMemorySets {
		for containerName, cset := range containerEntries {
			allocationInfo := podEntries[podUID][containerName]
			if allocationInfo == nil {
				continue
			}

			switch allocationInfo.QoSLevel {
			case consts.PodAnnotationQoSLevelDedicatedCores:
				if allocationInfo.CheckNumaBinding() {
					if !memorySetOverlap && cset.Intersection(unionNUMABindingActualMemorySet).Size() != 0 {
						memorySetOverlap = true
						general.Errorf("pod: %s/%s, container: %s memset: %s overlaps with others",
							allocationInfo.PodNamespace, allocationInfo.PodName, allocationInfo.ContainerName, cset.String())
					}
					unionNUMABindingActualMemorySet = unionNUMABindingActualMemorySet.Union(cset)
				} else {
					unionDedicatedActualMemorySet = unionDedicatedActualMemorySet.Union(cset)
				}
			case consts.PodAnnotationQoSLevelSharedCores:
				unionSharedActualMemorySet = unionSharedActualMemorySet.Union(cset)
			}
		}
	}

	regionOverlap := unionNUMABindingActualMemorySet.Intersection(unionSharedActualMemorySet).Size() != 0 ||
		unionNUMABindingActualMemorySet.Intersection(unionDedicatedActualMemorySet).Size() != 0
	if regionOverlap {
		general.Errorf("shared_cores union memset: %s, dedicated_cores union memset: %s overlap with numa_binding union memset: %s",
			unionSharedActualMemorySet.String(), unionDedicatedActualMemorySet.String(), unionNUMABindingActualMemorySet.String())
	}

	if !memorySetOverlap {
		memorySetOverlap = regionOverlap
	}
	if memorySetOverlap {
		general.Errorf("found memset overlap. actualMemorySets: %+v", actualMemorySets)
		_ = p.emitter.StoreInt64(util.MetricNameMemSetOverlap, 1, metrics.MetricTypeNameRaw)
	}

	machineState := p.state.GetMachineState()[v1.ResourceMemory]
	notAssignedMemSet := machineState.GetNUMANodesWithoutNUMABindingPods()
	if !unionNUMABindingStateMemorySet.Union(notAssignedMemSet).Equals(p.topology.CPUDetails.NUMANodes()) {
		general.Infof("found node memset invalid. unionNUMABindingStateMemorySet: %s, notAssignedMemSet: %s, topology: %s",
			unionNUMABindingStateMemorySet.String(), notAssignedMemSet.String(), p.topology.CPUDetails.NUMANodes().String())
		_ = p.emitter.StoreInt64(util.MetricNameNodeMemsetInvalid, 1, metrics.MetricTypeNameRaw)
	}

	general.Infof("finish checkMemorySet")
}

// clearResidualState is used to clean residual pods in local state
func (p *DynamicPolicy) clearResidualState() {
	general.Infof("called")
	residualSet := make(map[string]bool)

	ctx := context.Background()
	podList, err := p.metaServer.GetPodList(ctx, nil)
	if err != nil {
		general.Infof("get pod list failed: %v", err)
		return
	}

	podSet := sets.NewString()
	for _, pod := range podList {
		podSet.Insert(fmt.Sprintf("%v", pod.UID))
	}

	p.Lock()
	defer p.Unlock()

	podResourceEntries := p.state.GetPodResourceEntries()
	for _, podEntries := range podResourceEntries {
		for podUID, containerEntries := range podEntries {
			if len(containerEntries) == 1 && containerEntries[""] != nil {
				continue
			}

			if !podSet.Has(podUID) && !residualSet[podUID] {
				residualSet[podUID] = true
				p.residualHitMap[podUID] += 1
				general.Infof("found pod: %s with state but doesn't show up in pod watcher, hit count: %d", podUID, p.residualHitMap[podUID])
			}
		}
	}

	podsToDelete := sets.NewString()
	for podUID, hitCount := range p.residualHitMap {
		if !residualSet[podUID] {
			general.Infof("already found pod: %s in pod watcher or its state is cleared, delete it from residualHitMap", podUID)
			delete(p.residualHitMap, podUID)
			continue
		}

		if time.Duration(hitCount)*stateCheckPeriod >= maxResidualTime {
			podsToDelete.Insert(podUID)
		}
	}

	if podsToDelete.Len() > 0 {
		for {
			podUID, found := podsToDelete.PopAny()
			if !found {
				break
			}

			// todo: if the sysadvisor memory plugin is supported in the future, we need to call
			//  the memory plugin to remove the pod before deleting the pod entry
			general.Infof("clear residual pod: %s in state", podUID)
			for _, podEntries := range podResourceEntries {
				delete(podEntries, podUID)
			}
		}

		resourcesMachineState, err := state.GenerateMachineStateFromPodEntries(p.state.GetMachineInfo(), podResourceEntries, p.state.GetReservedMemory())
		if err != nil {
			general.Errorf("GenerateMachineStateFromPodEntries failed with error: %v", err)
			return
		}

		p.state.SetPodResourceEntries(podResourceEntries)
		p.state.SetMachineState(resourcesMachineState)

		err = p.adjustAllocationEntries()
		if err != nil {
			general.ErrorS(err, "adjustAllocationEntries failed")
		}
	}
}

// setMemoryMigrate is used to calculate and set memory migrate configuration, notice that
// 1. not to set memory migrate for NUMA binding containers
// 2. for a certain given pod/container, only one setting action is on the flight
// 3. the setting action is done asynchronously to avoid hang
func (p *DynamicPolicy) setMemoryMigrate() {
	general.Infof("called")
	p.RLock()

	podResourceEntries := p.state.GetPodResourceEntries()
	podEntries := podResourceEntries[v1.ResourceMemory]

	migrateCGData := make(map[string]map[string]*common.CPUSetData)
	for podUID, containerEntries := range podEntries {
		for containerName, allocationInfo := range containerEntries {
			if allocationInfo == nil {
				general.Errorf("pod: %s, container: %s has nil allocationInfo", podUID, containerName)
				continue
			} else if containerName == "" {
				general.Errorf("pod: %s has empty containerName entry", podUID)
				continue
			} else if allocationInfo.CheckNumaBinding() {
				continue
			}

			if migrateCGData[podUID] == nil {
				migrateCGData[podUID] = make(map[string]*common.CPUSetData)
			}
			migrateCGData[podUID][containerName] = &common.CPUSetData{Migrate: "1"}
		}
	}

	p.RUnlock()
	p.migrateMemoryLock.Lock()

	for podUID, containersData := range migrateCGData {
		for containerName, cgData := range containersData {
			if !p.migratingMemory[podUID][containerName] {
				if p.migratingMemory[podUID] == nil {
					p.migratingMemory[podUID] = make(map[string]bool)
				}

				p.migratingMemory[podUID][containerName] = true
				go func(podUID, containerName string, cgData *common.CPUSetData) {
					defer func() {
						p.migrateMemoryLock.Lock()
						delete(p.migratingMemory[podUID], containerName)
						if len(p.migratingMemory[podUID]) == 0 {
							delete(p.migratingMemory, podUID)
						}
						p.migrateMemoryLock.Unlock()
					}()

					containerID, err := p.metaServer.GetContainerID(podUID, containerName)
					if err != nil {
						general.Errorf("get container id of pod: %s container: %s failed with error: %v",
							podUID, containerName, err)
						return
					}
					general.Infof("start to set cgroup memory migrate for pod: %s, container: %s(%s) and pin memory",
						podUID, containerName, containerID)

					err = cgroupmgr.ApplyCPUSetForContainer(podUID, containerID, cgData)
					general.Infof("end to set cgroup memory migrate for pod: %s, container: %s(%s) and pin memory",
						podUID, containerName, containerID)
					if err != nil {
						general.Errorf("set cgroup memory migrate for pod: %s, container: %s(%s) failed with error: %v",
							podUID, containerName, containerID, err)
						return
					}

					general.Infof("set cgroup memory migrate for pod: %s, container: %s(%s) successfully",
						podUID, containerName, containerID)
				}(podUID, containerName, cgData)
			}
		}
	}
	p.migrateMemoryLock.Unlock()
}

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
	"strconv"
	"sync"
	"time"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/sets"

	"github.com/kubewharf/katalyst-api/pkg/consts"
	apiconsts "github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	memconsts "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/memory/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/memory/dynamicpolicy/oom"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/memory/dynamicpolicy/state"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/util"
	coreconfig "github.com/kubewharf/katalyst-core/pkg/config"
	dynamicconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/cgroup/common"
	cgroupmgr "github.com/kubewharf/katalyst-core/pkg/util/cgroup/manager"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
	"github.com/kubewharf/katalyst-core/pkg/util/native"
	"github.com/kubewharf/katalyst-core/pkg/util/qos"
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

		if clonedControlKnobInfo.ControlKnobValue == "" {
			general.Infof("skip set extral control knob: %s by configs: %#v  for pod: %s/%s, container: %s",
				controlKnobName, clonedControlKnobInfo,
				allocationInfo.PodNamespace, allocationInfo.PodName,
				allocationInfo.ContainerName)
			continue
		}

		general.Infof("set extral control knob: %s by configs: %#v  for pod: %s/%s, container: %s",
			controlKnobName, clonedControlKnobInfo,
			allocationInfo.PodNamespace, allocationInfo.PodName,
			allocationInfo.ContainerName)
		allocationInfo.ExtraControlKnobInfo[controlKnobName] = clonedControlKnobInfo
	}
}

func (p *DynamicPolicy) setExtraControlKnobByConfigs(_ *coreconfig.Configuration,
	_ interface{},
	_ *dynamicconfig.DynamicAgentConfiguration,
	_ metrics.MetricEmitter,
	_ *metaserver.MetaServer,
) {
	general.Infof("called")
	var (
		err     error
		podList []*v1.Pod
	)
	defer func() {
		_ = general.UpdateHealthzStateByError(memconsts.SetExtraControlKnob, err)
	}()

	if p.metaServer == nil {
		general.Errorf("nil metaServer")
		return
	} else if len(p.extraControlKnobConfigs) == 0 {
		general.Errorf("empty extraControlKnobConfigs, skip setExtraControlKnobByConfigs")
		return
	}

	podList, err = p.metaServer.GetPodList(context.Background(), nil)
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

	var resourcesMachineState state.NUMANodeResourcesMap
	resourcesMachineState, err = state.GenerateMachineStateFromPodEntries(p.state.GetMachineInfo(), podResourceEntries, p.state.GetMachineState(), p.state.GetReservedMemory())
	if err != nil {
		general.Errorf("GenerateMachineStateFromPodEntries failed with error: %v", err)
		return
	}

	p.state.SetPodResourceEntries(podResourceEntries, false)
	p.state.SetMachineState(resourcesMachineState, false)
	if err := p.state.StoreState(); err != nil {
		general.ErrorS(err, "store state failed")
	}
}

func (p *DynamicPolicy) applyExternalCgroupParams(_ *coreconfig.Configuration,
	_ interface{},
	_ *dynamicconfig.DynamicAgentConfiguration,
	_ metrics.MetricEmitter,
	_ *metaserver.MetaServer,
) {
	general.Infof("called")

	var errList []error
	defer func() {
		_ = general.UpdateHealthzStateByError(memconsts.ApplyExternalCGParams, errors.NewAggregate(errList))
	}()

	podEntries := p.state.GetPodResourceEntries()[v1.ResourceMemory]

	for podUID, containerEntries := range podEntries {
		for containerName, allocationInfo := range containerEntries {
			if allocationInfo == nil {
				continue
			}

			containerID, err := p.metaServer.GetContainerID(podUID, containerName)
			if err != nil {
				general.Warningf("get container id of pod: %s/%s container: %s failed with error: %v",
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
					general.Warningf("pod: %s/%s container: %s control knob name: %s with empty cgroupIfaceName",
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

				exist, err := common.IsContainerCgroupFileExist(entry.CgroupSubsysName, podUID, containerID, cgroupIfaceName)
				if err != nil {
					general.Warningf("check %v/%v cgroup file's existence of pod: %s/%s container: %s failed with error: %v",
						cgroupIfaceName, entry.CgroupSubsysName,
						allocationInfo.PodNamespace, allocationInfo.PodName,
						allocationInfo.ContainerName, err)
					continue
				}
				if !exist {
					general.Warningf("%v/%v cgroup file not exist of pod: %s/%s container: %v,skip applyExternalCgroupParams",
						cgroupIfaceName, entry.CgroupSubsysName,
						allocationInfo.PodNamespace, allocationInfo.PodName,
						allocationInfo.ContainerName)
					continue
				}

				err = cgroupmgr.ApplyUnifiedDataForContainer(podUID, containerID, entry.CgroupSubsysName, cgroupIfaceName, entry.ControlKnobValue)
				if err != nil {
					errList = append(errList, err)
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
func (p *DynamicPolicy) checkMemorySet(_ *coreconfig.Configuration,
	_ interface{},
	_ *dynamicconfig.DynamicAgentConfiguration,
	_ metrics.MetricEmitter,
	_ *metaserver.MetaServer,
) {
	general.Infof("called")
	var (
		errList          []error
		invalidMemSet    = false
		memorySetOverlap = false
	)

	defer func() {
		if len(errList) > 0 {
			_ = general.UpdateHealthzStateByError(memconsts.CheckMemSet, errors.NewAggregate(errList))
		} else if invalidMemSet {
			_ = general.UpdateHealthzState(memconsts.CheckMemSet, general.HealthzCheckStateNotReady, "invalid mem set exists")
		} else if memorySetOverlap {
			_ = general.UpdateHealthzState(memconsts.CheckMemSet, general.HealthzCheckStateNotReady, "mem set overlap")
		} else {
			_ = general.UpdateHealthzState(memconsts.CheckMemSet, general.HealthzCheckStateReady, "")
		}
	}()

	podEntries := p.state.GetPodResourceEntries()[v1.ResourceMemory]
	actualMemorySets := make(map[string]map[string]machine.CPUSet)

	unionNUMABindingStateMemorySet := machine.NewCPUSet()
	for podUID, containerEntries := range podEntries {
		for containerName, allocationInfo := range containerEntries {
			if allocationInfo == nil || !allocationInfo.CheckMainContainer() {
				continue
			} else if allocationInfo.CheckShared() &&
				p.getContainerRequestedMemoryBytes(allocationInfo) == 0 {
				general.Warningf("skip memset checking for pod: %s/%s container: %s with zero memory request",
					allocationInfo.PodNamespace, allocationInfo.PodName, containerName)
				continue
			} else if allocationInfo.CheckSharedOrDedicatedNUMABinding() {
				unionNUMABindingStateMemorySet = unionNUMABindingStateMemorySet.Union(allocationInfo.NumaAllocationResult)
			}

			tags := metrics.ConvertMapToTags(map[string]string{
				"podNamespace":  allocationInfo.PodNamespace,
				"podName":       allocationInfo.PodName,
				"containerName": allocationInfo.ContainerName,
			})

			var (
				containerID string
				cpusetStats *common.CPUSetStats
			)
			containerID, err := p.metaServer.GetContainerID(podUID, containerName)
			if err != nil {
				general.Errorf("get container id of pod: %s container: %s failed with error: %v", podUID, containerName, err)
				continue
			}
			cpusetAbsCGPath, err := common.GetContainerAbsCgroupPath(common.CgroupSubsysCPUSet, podUID, containerID)
			if err != nil {
				general.Errorf("get container abs cgroup path of pod: %s container: %s failed with error: %v", podUID, containerName, err)
				_ = p.emitter.StoreInt64(util.MetricNameCgroupPathNotFound, 1, metrics.MetricTypeNameRaw, tags...)
				continue
			}

			cpusetStats, err = cgroupmgr.GetCPUSetWithAbsolutePath(cpusetAbsCGPath)
			if err != nil {
				general.Errorf("GetMemorySet of pod: %s container: name(%s), id(%s) failed with error: %v",
					podUID, containerName, containerID, err)
				_ = p.emitter.StoreInt64(util.MetricNameRealStateInvalid, 1, metrics.MetricTypeNameRaw, tags...)
				errList = append(errList, err)
				continue
			}

			if actualMemorySets[podUID] == nil {
				actualMemorySets[podUID] = make(map[string]machine.CPUSet)
			}
			actualMemorySets[podUID][containerName] = machine.MustParse(cpusetStats.Mems)

			general.Infof("pod: %s/%s, container: %s, state MemorySet: %s, actual MemorySet: %s",
				allocationInfo.PodNamespace, allocationInfo.PodName, allocationInfo.ContainerName,
				allocationInfo.NumaAllocationResult.String(), actualMemorySets[podUID][containerName].String())

			// only do comparison for dedicated_cores with numa_binding to avoid effect of adjustment for shared_cores
			if !allocationInfo.CheckNUMABinding() {
				continue
			}

			if !actualMemorySets[podUID][containerName].Equals(allocationInfo.NumaAllocationResult) {
				invalidMemSet = true
				general.Errorf("pod: %s/%s, container: %s, memset invalid",
					allocationInfo.PodNamespace, allocationInfo.PodName, allocationInfo.ContainerName)
				_ = p.emitter.StoreInt64(util.MetricNameMemSetInvalid, 1, metrics.MetricTypeNameRaw, tags...)
			}
		}
	}

	unionDedicatedNUMABindingExclusiveActualMemorySet := machine.NewCPUSet()
	unionDedicatedActualMemorySet := machine.NewCPUSet()
	unionSharedActualMemorySet := machine.NewCPUSet()

	for podUID, containerEntries := range actualMemorySets {
		for containerName, cset := range containerEntries {
			allocationInfo := podEntries[podUID][containerName]
			if allocationInfo == nil {
				continue
			}

			switch allocationInfo.QoSLevel {
			case consts.PodAnnotationQoSLevelDedicatedCores:
				if allocationInfo.CheckNUMABinding() && allocationInfo.CheckNumaExclusive() {
					if !memorySetOverlap && cset.Intersection(unionDedicatedNUMABindingExclusiveActualMemorySet).Size() != 0 {
						memorySetOverlap = true
						general.Errorf("pod: %s/%s, container: %s memset: %s overlaps with others",
							allocationInfo.PodNamespace, allocationInfo.PodName, allocationInfo.ContainerName, cset.String())
					}
					unionDedicatedNUMABindingExclusiveActualMemorySet = unionDedicatedNUMABindingExclusiveActualMemorySet.Union(cset)
				} else {
					unionDedicatedActualMemorySet = unionDedicatedActualMemorySet.Union(cset)
				}
			case consts.PodAnnotationQoSLevelSharedCores:
				unionSharedActualMemorySet = unionSharedActualMemorySet.Union(cset)
			}
		}
	}

	regionOverlap := unionDedicatedNUMABindingExclusiveActualMemorySet.Intersection(unionSharedActualMemorySet).Size() != 0 ||
		unionDedicatedNUMABindingExclusiveActualMemorySet.Intersection(unionDedicatedActualMemorySet).Size() != 0
	if regionOverlap {
		general.Errorf("shared_cores union memset: %s, dedicated_cores union memset: %s overlap with numa_binding and exclusive union memset: %s",
			unionSharedActualMemorySet.String(), unionDedicatedActualMemorySet.String(), unionDedicatedNUMABindingExclusiveActualMemorySet.String())
	}

	if !memorySetOverlap {
		memorySetOverlap = regionOverlap
	}
	if memorySetOverlap {
		general.Errorf("found memset overlap. actualMemorySets: %+v", actualMemorySets)
		_ = p.emitter.StoreInt64(util.MetricNameMemSetOverlap, 1, metrics.MetricTypeNameRaw)
	}

	machineState := p.state.GetMachineState()[v1.ResourceMemory]
	notAssignedMemSet := machineState.GetNUMANodesWithoutSharedOrDedicatedNUMABindingPods()
	if !unionNUMABindingStateMemorySet.Union(notAssignedMemSet).Equals(p.topology.CPUDetails.NUMANodes()) {
		general.Infof("found node memset invalid. unionNUMABindingStateMemorySet: %s, notAssignedMemSet: %s, topology: %s",
			unionNUMABindingStateMemorySet.String(), notAssignedMemSet.String(), p.topology.CPUDetails.NUMANodes().String())
		_ = p.emitter.StoreInt64(util.MetricNameNodeMemsetInvalid, 1, metrics.MetricTypeNameRaw)
	}

	general.Infof("finish checkMemorySet")
}

// clearResidualState is used to clean residual pods in local state
func (p *DynamicPolicy) clearResidualState(_ *coreconfig.Configuration,
	_ interface{},
	_ *dynamicconfig.DynamicAgentConfiguration,
	_ metrics.MetricEmitter,
	_ *metaserver.MetaServer,
) {
	general.Infof("called")
	var (
		err     error
		podList []*v1.Pod
	)
	residualSet := make(map[string]bool)

	defer func() {
		_ = general.UpdateHealthzStateByError(memconsts.ClearResidualState, err)
	}()

	ctx := context.Background()
	podList, err = p.metaServer.GetPodList(ctx, nil)
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

		resourcesMachineState, err := state.GenerateMachineStateFromPodEntries(p.state.GetMachineInfo(), podResourceEntries, p.state.GetMachineState(), p.state.GetReservedMemory())
		if err != nil {
			general.Errorf("GenerateMachineStateFromPodEntries failed with error: %v", err)
			return
		}

		p.state.SetPodResourceEntries(podResourceEntries, false)
		p.state.SetMachineState(resourcesMachineState, false)

		err = p.adjustAllocationEntries(false)
		if err != nil {
			general.ErrorS(err, "adjustAllocationEntries failed")
		}
		if err := p.state.StoreState(); err != nil {
			general.ErrorS(err, "store state failed")
		}
	}
}

// setMemoryMigrate is used to calculate and set memory migrate configuration, notice that
// 1. not to set memory migrate for shared or dedicated NUMA binding containers
// 2. for a certain given pod/container, only one setting action is on the flight
// 3. the setting action is done asynchronously to avoid hang
func (p *DynamicPolicy) setMemoryMigrate() {
	general.Infof("called")
	p.RLock()
	// TODO update healthz check

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
			} else if allocationInfo.CheckSharedOrDedicatedNUMABinding() {
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

// clearResidualOOMPriority is used to clean residual oom pinned map entries
func (p *DynamicPolicy) clearResidualOOMPriority(conf *coreconfig.Configuration,
	_ interface{}, _ *dynamicconfig.DynamicAgentConfiguration,
	emitter metrics.MetricEmitter, metaServer *metaserver.MetaServer,
) {
	if p.oomPriorityMap == nil {
		general.Infof("oom priority bpf has not been initialized yet")
		return
	}
	general.Infof("called")

	ctx := context.Background()
	activePods, err := metaServer.GetPodList(ctx, native.PodIsActive)
	if err != nil {
		general.Errorf("failed to list active pods from metaServer: %v", err)
		return
	}

	activeCgroupIDsMap := make(map[uint64]struct{})
	for _, pod := range activePods {
		if pod == nil {
			general.Errorf("get nil pod from metaServer")
			continue
		}

		podUID := string(pod.UID)

		for _, container := range pod.Spec.Containers {
			containerName := container.Name
			containerID, err := metaServer.GetContainerID(podUID, containerName)
			if err != nil {
				general.Errorf("get container id of pod: %s container: %s failed with error: %v",
					podUID, containerName, err)
				continue
			}

			if exist, err := common.IsContainerCgroupExist(podUID, containerID); err != nil {
				general.Errorf("check if container cgroup exists failed, pod: %s, container: %s(%s), err: %v",
					podUID, containerName, containerID, err)
				continue
			} else if !exist {
				general.Infof("container cgroup does not exist, pod: %s, container: %s(%s)", podUID, containerName, containerID)
				continue
			}

			cgID, err := metaServer.ExternalManager.GetCgroupIDForContainer(podUID, containerID)
			if err != nil {
				general.Errorf("get cgroup id failed, pod: %s, container: %s(%s), err: %v",
					podUID, containerName, containerID, err)
				continue
			}

			activeCgroupIDsMap[cgID] = struct{}{}
		}
	}

	p.oomPriorityMapLock.Lock()
	defer p.oomPriorityMapLock.Unlock()

	// delete the keys in OOM pinned map which do not exist in activeCgroupIDsMap
	var key uint64
	var value int64
	var toDeleteKeys []uint64
	iter := p.oomPriorityMap.Iterate()
	for iter.Next(&key, &value) {
		if _, ok := activeCgroupIDsMap[key]; !ok {
			toDeleteKeys = append(toDeleteKeys, key)
		}
	}

	var wg sync.WaitGroup
	for _, key := range toDeleteKeys {
		wg.Add(1)
		go func(cgID uint64) {
			err := p.oomPriorityMap.Delete(cgID)
			if err != nil && !general.IsErrKeyNotExist(err) {
				general.Errorf("[clearResidualOOMPriority] delete oom pinned map failed: %v", err)
				_ = emitter.StoreInt64(util.MetricNameMemoryOOMPriorityDeleteFailed, 1,
					metrics.MetricTypeNameRaw, metrics.ConvertMapToTags(map[string]string{
						"cg_id": strconv.FormatUint(cgID, 10),
					})...)
			}
			wg.Done()
		}(key)
	}
	wg.Wait()
}

// syncOOMPriority is used to sync the OOM pinned map according to the current pod list
func (p *DynamicPolicy) syncOOMPriority(conf *coreconfig.Configuration,
	_ interface{}, _ *dynamicconfig.DynamicAgentConfiguration,
	emitter metrics.MetricEmitter, metaServer *metaserver.MetaServer,
) {
	var (
		updateBPFMapErr []error
		err             error
	)

	defer func() {
		_ = general.UpdateHealthzStateByError(memconsts.OOMPriority, errors.NewAggregate(append(updateBPFMapErr, err)))
	}()

	if p.oomPriorityMap == nil {
		general.Infof("oom priority bpf has not been initialized yet")
		return
	}
	general.Infof("called")

	if metaServer == nil {
		general.Errorf("nil metaServer")
		return
	}

	ctx := context.Background()
	var podList []*v1.Pod
	podList, err = metaServer.GetPodList(ctx, native.PodIsActive)
	if err != nil {
		general.Infof("get pod list failed: %v", err)
		return
	}

	podOOMMaps := make(map[string]map[string]int)
	var toUpdateKeys []uint64
	var toUpdateValues []int

	for _, pod := range podList {
		if pod == nil {
			general.Errorf("get nil pod from metaServer")
			continue
		}

		oomPriority, err := oom.GetOOMPriority(conf.QoSConfiguration, pod)
		if err != nil {
			general.Errorf("get oom priority failed for pod: %s, err: %v", string(pod.UID), err)
			continue
		}

		if oomPriority == qos.IgnoreOOMPriorityScore {
			general.Infof("ignore oom priority for pod: %s", string(pod.UID))
			continue
		}

		podUID := string(pod.UID)
		podOOMMaps[podUID] = make(map[string]int)

		for _, container := range pod.Spec.Containers {
			containerName := container.Name
			containerID, err := metaServer.GetContainerID(podUID, containerName)
			if err != nil {
				general.Errorf("get container id failed, pod: %s, container: %s(%s), err: %v",
					podUID, containerName, containerID, err)
				continue
			}

			if exist, err := common.IsContainerCgroupExist(podUID, containerID); err != nil {
				general.Errorf("check if container cgroup exists failed, pod: %s, container: %s(%s), err: %v",
					podUID, containerName, containerID, err)
				continue
			} else if !exist {
				general.Infof("container cgroup does not exist, pod: %s, container: %s(%s)", podUID, containerName, containerID)
				continue
			}

			cgID, err := metaServer.ExternalManager.GetCgroupIDForContainer(podUID, containerID)
			if err != nil {
				general.Errorf("get cgroup id failed, pod: %s, container: %s(%s), err: %v",
					podUID, containerName, containerID, err)
				continue
			}

			toUpdateKeys = append(toUpdateKeys, cgID)
			toUpdateValues = append(toUpdateValues, oomPriority)
			podOOMMaps[podUID][containerName] = oomPriority
		}
	}

	// update the OOM pinned map according to the current pod list
	p.oomPriorityMapLock.Lock()
	var wg sync.WaitGroup

	for i := 0; i < len(toUpdateKeys); i++ {
		toUpdateKey, toUpdateValue := toUpdateKeys[i], toUpdateValues[i]
		wg.Add(1)
		go func(cgID uint64, oomPriority int) {
			err := p.oomPriorityMap.Put(cgID, int64(oomPriority))
			if err != nil {
				general.Errorf("update oom pinned map failed: %v", err)
				updateBPFMapErr = append(updateBPFMapErr, err)
				_ = emitter.StoreInt64(util.MetricNameMemoryOOMPriorityUpdateFailed, 1,
					metrics.MetricTypeNameRaw, metrics.ConvertMapToTags(map[string]string{
						"cg_id": strconv.FormatUint(cgID, 10),
					})...)
			}
			wg.Done()
		}(toUpdateKey, toUpdateValue)
	}
	wg.Wait()
	p.oomPriorityMapLock.Unlock()

	// update oom priority annotation in local state
	p.Lock()
	defer p.Unlock()

	podResourceEntries := p.state.GetPodResourceEntries()
	podEntries := podResourceEntries[v1.ResourceMemory]

	for podUID, containerEntries := range podEntries {
		if podOOMMaps[podUID] == nil {
			continue
		}

		for containerName, allocationInfo := range containerEntries {
			if allocationInfo == nil {
				continue
			}

			if oomPriority, ok := podOOMMaps[podUID][containerName]; !ok ||
				allocationInfo.Annotations[apiconsts.PodAnnotationMemoryEnhancementOOMPriority] == strconv.Itoa(oomPriority) {
				continue
			}

			allocationInfo.Annotations[apiconsts.PodAnnotationMemoryEnhancementOOMPriority] = strconv.Itoa(podOOMMaps[podUID][containerName])
		}
	}

	var resourcesMachineState state.NUMANodeResourcesMap
	resourcesMachineState, err = state.GenerateMachineStateFromPodEntries(p.state.GetMachineInfo(), podResourceEntries, p.state.GetMachineState(), p.state.GetReservedMemory())
	if err != nil {
		general.Errorf("GenerateMachineStateFromPodEntries failed with error: %v", err)
		return
	}

	p.state.SetPodResourceEntries(podResourceEntries, false)
	p.state.SetMachineState(resourcesMachineState, false)
	if err := p.state.StoreState(); err != nil {
		general.Errorf("store state failed with error: %v", err)
	}
}

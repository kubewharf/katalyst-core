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
	"time"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/sets"

	"github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/advisorsvc"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	cpuconsts "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/util"
	coreconfig "github.com/kubewharf/katalyst-core/pkg/config"
	dynamicconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/cgroup/common"
	cgroupcm "github.com/kubewharf/katalyst-core/pkg/util/cgroup/common"
	cgroupcmutils "github.com/kubewharf/katalyst-core/pkg/util/cgroup/manager"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
	"github.com/kubewharf/katalyst-core/pkg/util/native"
)

const (
	metricsNamePodTotalRequestLargerThanBindingCPUSet = "pod_total_request_larger_than_cpu_set"
)

// checkCPUSet emit errors if the memory allocation falls into unexpected results
func (p *DynamicPolicy) checkCPUSet(_ *coreconfig.Configuration,
	_ interface{},
	_ *dynamicconfig.DynamicAgentConfiguration,
	_ metrics.MetricEmitter,
	_ *metaserver.MetaServer,
) {
	general.Infof("exec checkCPUSet")
	var (
		errList       []error
		invalidCPUSet = false
		cpuSetOverlap = false
	)

	defer func() {
		if len(errList) > 0 {
			_ = general.UpdateHealthzStateByError(cpuconsts.CheckCPUSet, errors.NewAggregate(errList))
		} else if invalidCPUSet {
			_ = general.UpdateHealthzState(cpuconsts.CheckCPUSet, general.HealthzCheckStateNotReady, "invalid cpuset exists")
		} else if cpuSetOverlap {
			_ = general.UpdateHealthzState(cpuconsts.CheckCPUSet, general.HealthzCheckStateNotReady, "cpuset overlap")
		} else {
			_ = general.UpdateHealthzState(cpuconsts.CheckCPUSet, general.HealthzCheckStateReady, "")
		}
	}()

	podEntries := p.state.GetPodEntries()
	actualCPUSets := make(map[string]map[string]machine.CPUSet)
	for podUID, containerEntries := range podEntries {
		if containerEntries.IsPoolEntry() {
			continue
		}

		for containerName, allocationInfo := range containerEntries {
			if allocationInfo == nil || !allocationInfo.CheckMainContainer() {
				continue
			} else if allocationInfo.CheckShared() && p.getContainerRequestedCores(allocationInfo) == 0 {
				general.Warningf("skip cpuset checking for pod: %s/%s container: %s with zero cpu request",
					allocationInfo.PodNamespace, allocationInfo.PodName, containerName)
				continue
			}

			tags := metrics.ConvertMapToTags(map[string]string{
				"podNamespace":  allocationInfo.PodNamespace,
				"podName":       allocationInfo.PodName,
				"containerName": allocationInfo.ContainerName,
			})
			var (
				containerId string
				cpuSetStats *cgroupcm.CPUSetStats
			)

			containerId, err := p.metaServer.GetContainerID(podUID, containerName)
			if err != nil {
				general.Errorf("get container id of pod: %s container: %s failed with error: %v", podUID, containerName, err)
				continue
			}

			cpusetAbsCGPath, err := common.GetContainerAbsCgroupPath(common.CgroupSubsysCPUSet, podUID, containerId)
			if err != nil {
				general.Errorf("get container abs cgroup path of pod: %s container: %s failed with error: %v", podUID, containerName, err)
				_ = p.emitter.StoreInt64(util.MetricNameCgroupPathNotFound, 1, metrics.MetricTypeNameRaw, tags...)
				continue
			}

			cpuSetStats, err = cgroupcmutils.GetCPUSetWithAbsolutePath(cpusetAbsCGPath)
			if err != nil {
				general.Errorf("GetCPUSet of pod: %s container: name(%s), id(%s) failed with error: %v",
					podUID, containerName, containerId, err)
				_ = p.emitter.StoreInt64(util.MetricNameRealStateInvalid, 1, metrics.MetricTypeNameRaw, tags...)
				errList = append(errList, err)
				continue
			}

			if actualCPUSets[podUID] == nil {
				actualCPUSets[podUID] = make(map[string]machine.CPUSet)
			}
			actualCPUSets[podUID][containerName] = machine.MustParse(cpuSetStats.CPUs)

			general.Infof("pod: %s/%s, container: %s, state CPUSet: %s, actual CPUSet: %s",
				allocationInfo.PodNamespace, allocationInfo.PodName, allocationInfo.ContainerName,
				allocationInfo.AllocationResult.String(), actualCPUSets[podUID][containerName].String())

			// only do comparison for dedicated_cores with numa_biding to avoid effect of adjustment for shared_cores
			if !allocationInfo.CheckDedicated() {
				continue
			}

			if !actualCPUSets[podUID][containerName].Equals(allocationInfo.OriginalAllocationResult) {
				invalidCPUSet = true
				general.Errorf("pod: %s/%s, container: %s, cpuset invalid",
					allocationInfo.PodNamespace, allocationInfo.PodName, allocationInfo.ContainerName)
				_ = p.emitter.StoreInt64(util.MetricNameCPUSetInvalid, 1, metrics.MetricTypeNameRaw, tags...)
			}
		}
	}

	unionDedicatedCPUSet := machine.NewCPUSet()
	unionSharedCPUSet := machine.NewCPUSet()

	for podUID, containerEntries := range actualCPUSets {
		for containerName, cset := range containerEntries {
			allocationInfo := podEntries[podUID][containerName]
			if allocationInfo == nil {
				continue
			}

			switch allocationInfo.QoSLevel {
			case consts.PodAnnotationQoSLevelDedicatedCores:
				if !cpuSetOverlap && cset.Intersection(unionDedicatedCPUSet).Size() != 0 {
					cpuSetOverlap = true
					general.Errorf("pod: %s/%s, container: %s cpuset: %s overlaps with others",
						allocationInfo.PodNamespace, allocationInfo.PodName, allocationInfo.ContainerName, cset.String())
				}
				unionDedicatedCPUSet = unionDedicatedCPUSet.Union(cset)
			case consts.PodAnnotationQoSLevelSharedCores:
				unionSharedCPUSet = unionSharedCPUSet.Union(cset)
			}
		}
	}

	regionOverlap := unionSharedCPUSet.Intersection(unionDedicatedCPUSet).Size() != 0
	if regionOverlap {
		general.Errorf("shared_cores union cpuset: %s overlaps with dedicated_cores union cpuset: %s",
			unionSharedCPUSet.String(), unionDedicatedCPUSet.String())
	}

	if !cpuSetOverlap {
		cpuSetOverlap = regionOverlap
	}
	if cpuSetOverlap {
		general.Errorf("found cpuset overlap. actualCPUSets: %+v", actualCPUSets)
		_ = p.emitter.StoreInt64(util.MetricNameCPUSetOverlap, 1, metrics.MetricTypeNameRaw)
	}

	p.checkCPUSetWithPodTotalRequest(podEntries, actualCPUSets)
	general.Infof("finish checkCPUSet")
}

type cpusetPodState struct {
	cpuset               machine.CPUSet
	totalMilliCPURequest int64
	podUIDs              sets.String
	podMap               map[string]*v1.Pod
}

func (p *DynamicPolicy) checkCPUSetWithPodTotalRequest(
	podEntries state.PodEntries,
	actualCPUSets map[string]map[string]machine.CPUSet,
) {
	ctx := context.Background()
	cpusetPodStateMap := p.buildCPUSetPodStateMap(ctx, podEntries, actualCPUSets)
	p.checkAndEmitMetrics(podEntries, cpusetPodStateMap)
	general.Infof("finish checkCPUSetWithPodTotalRequest")
}

func (p *DynamicPolicy) buildCPUSetPodStateMap(ctx context.Context, podEntries state.PodEntries, actualCPUSets map[string]map[string]machine.CPUSet) map[string]*cpusetPodState {
	cpusetPodStateMap := make(map[string]*cpusetPodState)

	// Build initial cpuset state map
	for podUID, containerCPUSets := range actualCPUSets {
		for _, cset := range containerCPUSets {
			mainContainerEntry := podEntries[podUID].GetMainContainerEntry()
			if mainContainerEntry == nil || mainContainerEntry.CheckReclaimed() {
				continue
			}

			csetStr := cset.String()
			if _, ok := cpusetPodStateMap[csetStr]; !ok {
				cpusetPodStateMap[csetStr] = &cpusetPodState{
					cpuset:  cset,
					podUIDs: sets.NewString(),
				}
			}
			cpusetPodStateMap[csetStr].podUIDs.Insert(podUID)
		}
	}

	// Populate pod info and calculate total CPU requests
	for csetStr, cs := range cpusetPodStateMap {
		podMap := make(map[string]*v1.Pod, cs.podUIDs.Len())
		totalMilliCPURequest := int64(0)

		for _, podUID := range cs.podUIDs.List() {
			pod, err := p.metaServer.GetPod(ctx, podUID)
			if err != nil {
				general.Errorf("get pod: %s failed with error: %v", podUID, err)
				continue
			}

			if !native.PodIsActive(pod) {
				continue
			}

			resources := native.SumUpPodRequestResources(pod)
			totalMilliCPURequest += resources.Cpu().MilliValue()
			podMap[podUID] = pod
		}

		cs.podMap = podMap
		cs.totalMilliCPURequest = totalMilliCPURequest
		general.Infof("cpuset: %s, size: %d, totalMilliCPURequest: %d, podUIDs: %v", csetStr, cs.cpuset.Size(),
			totalMilliCPURequest, cs.podUIDs.List())
	}

	return cpusetPodStateMap
}

func (p *DynamicPolicy) checkAndEmitMetrics(podEntries state.PodEntries, cpusetPodStateMap map[string]*cpusetPodState) {
	allowSharedCoresOverlapReclaimedCores := p.state.GetAllowSharedCoresOverlapReclaimedCores()

	for cpuset, cs := range cpusetPodStateMap {
		totalMilliCPURequest := p.calculateTotalCPURequest(cpuset, cs, cpusetPodStateMap)
		if cs.cpuset.Size() == 0 || totalMilliCPURequest == 0 {
			continue
		}
		exceededRatio := float64(totalMilliCPURequest-int64(cs.cpuset.Size()*1000)) / float64(totalMilliCPURequest)

		if exceededRatio > 0 {
			p.emitExceededMetrics(podEntries, cpuset, cs, exceededRatio, allowSharedCoresOverlapReclaimedCores)
		}
	}
}

func (p *DynamicPolicy) calculateTotalCPURequest(cpuset string, cs *cpusetPodState, cpusetPodStateMap map[string]*cpusetPodState) int64 {
	totalMilliCPURequest := cs.totalMilliCPURequest

	// Add requests from subsets
	for otherCPUSet, otherState := range cpusetPodStateMap {
		if cpuset == otherCPUSet {
			continue
		}

		if otherState.cpuset.IsSubsetOf(cs.cpuset) {
			totalMilliCPURequest += otherState.totalMilliCPURequest
		}
	}

	return totalMilliCPURequest
}

func (p *DynamicPolicy) emitExceededMetrics(
	podEntries state.PodEntries,
	cpuset string,
	cs *cpusetPodState,
	exceededRatio float64,
	allowSharedCoresOverlapReclaimedCores bool,
) {
	enableReclaim := p.dynamicConfig.GetDynamicConfiguration().EnableReclaim
	for podUID, pod := range cs.podMap {
		mainContainerEntry := podEntries[podUID].GetMainContainerEntry()
		if mainContainerEntry == nil ||
			(mainContainerEntry.CheckShared() && enableReclaim && !allowSharedCoresOverlapReclaimedCores) {
			continue
		}

		// check if the pod exceeds the binding cpuset by more than 1 core
		outOfTolerance := int64(cs.cpuset.Size()+1) < (cs.totalMilliCPURequest / 1000)

		general.Errorf("pod: %s/%s, ownerPoolName: %s, qosLevel: %s, cpuset: %s, size %d, exceeds total cpu request: %.3f, exceeded ratio: %.3f, outOfTolerance: %v",
			pod.Namespace, pod.Name, mainContainerEntry.OwnerPoolName, mainContainerEntry.QoSLevel, cpuset, cs.cpuset.Size(),
			float64(cs.totalMilliCPURequest)/1000, exceededRatio, outOfTolerance)

		_ = p.emitter.StoreFloat64(metricsNamePodTotalRequestLargerThanBindingCPUSet, exceededRatio, metrics.MetricTypeNameRaw, []metrics.MetricTag{
			{Key: "podNamespace", Val: pod.Namespace},
			{Key: "podName", Val: pod.Name},
			{Key: "qosLevel", Val: mainContainerEntry.QoSLevel},
			{Key: "ownerPoolName", Val: mainContainerEntry.OwnerPoolName},
			{Key: "poolType", Val: commonstate.GetPoolType(mainContainerEntry.OwnerPoolName)},
			{Key: "outOfTolerance", Val: strconv.FormatBool(outOfTolerance)},
			{Key: "cpuset", Val: cpuset},
		}...)
	}
}

// clearResidualState is used to clean residual pods in local state
func (p *DynamicPolicy) clearResidualState(_ *coreconfig.Configuration,
	_ interface{},
	_ *dynamicconfig.DynamicAgentConfiguration,
	_ metrics.MetricEmitter,
	_ *metaserver.MetaServer,
) {
	general.Infof("exec clearResidualState")
	var (
		err     error
		podList []*v1.Pod
	)
	residualSet := make(map[string]bool)

	defer func() {
		_ = general.UpdateHealthzStateByError(cpuconsts.ClearResidualState, err)
	}()

	if p.metaServer == nil {
		general.Errorf("nil metaServer")
		return
	}

	ctx := context.Background()
	podList, err = p.metaServer.GetPodList(ctx, nil)
	if err != nil {
		general.Errorf("get pod list failed: %v", err)
		return
	}

	podSet := sets.NewString()
	for _, pod := range podList {
		podSet.Insert(fmt.Sprintf("%v", pod.UID))
	}

	p.Lock()
	defer p.Unlock()

	podEntries := p.state.GetPodEntries()
	for podUID, containerEntries := range podEntries {
		if containerEntries.IsPoolEntry() {
			continue
		}

		if !podSet.Has(podUID) {
			residualSet[podUID] = true
			p.residualHitMap[podUID] += 1
			general.Infof("found pod: %s with state but doesn't show up in pod watcher, hit count: %d", podUID, p.residualHitMap[podUID])
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

			var rErr error
			if p.enableCPUAdvisor {
				if p.advisorClient == nil {
					general.Errorf("remove residual pod: %s in sys advisor failed due to nil cpu advisor client, remain it in state", podUID)
					continue
				}
				_, rErr = p.advisorClient.RemovePod(ctx, &advisorsvc.RemovePodRequest{
					PodUid: podUID,
				})
			}
			if rErr != nil {
				general.Errorf("remove residual pod: %s in sys advisor failed with error: %v, remain it in state", podUID, rErr)
				continue
			}

			general.Infof("clear residual pod: %s in state", podUID)
			delete(podEntries, podUID)
		}

		var updatedMachineState state.NUMANodeMap
		updatedMachineState, err = generateMachineStateFromPodEntries(p.machineInfo.CPUTopology, podEntries, p.state.GetMachineState())
		if err != nil {
			general.Errorf("GenerateMachineStateFromPodEntries failed with error: %v", err)
			return
		}

		p.state.SetPodEntries(podEntries, false)
		p.state.SetMachineState(updatedMachineState, false)

		err = p.adjustAllocationEntries(false)
		if err != nil {
			general.ErrorS(err, "adjustAllocationEntries failed")
		}
		if err := p.state.StoreState(); err != nil {
			general.ErrorS(err, "store state failed")
		}
	}
}

// syncCPUIdle is used to set cpu idle for reclaimed cores
func (p *DynamicPolicy) syncCPUIdle(_ *coreconfig.Configuration,
	_ interface{},
	_ *dynamicconfig.DynamicAgentConfiguration,
	_ metrics.MetricEmitter,
	_ *metaserver.MetaServer,
) {
	general.Infof("exec syncCPUIdle")
	var err error
	defer func() {
		_ = general.UpdateHealthzStateByError(cpuconsts.SyncCPUIdle, err)
	}()

	if !cgroupcm.IsCPUIdleSupported() {
		general.Warningf("cpu idle isn't unsupported, skip syncing")
		return
	}

	err = cgroupcmutils.ApplyCPUWithRelativePath(p.reclaimRelativeRootCgroupPath, &cgroupcm.CPUData{CpuIdlePtr: &p.enableCPUIdle})
	if err != nil {
		general.Errorf("ApplyCPUWithRelativePath in %s with enableCPUIdle: %v in failed with error: %v",
			p.reclaimRelativeRootCgroupPath, p.enableCPUIdle, err)
	}

	// sync numa binding reclaim cgroup
	for _, cgroupPath := range p.numaBindingReclaimRelativeRootCgroupPaths {
		if !general.IsPathExists(cgroupcm.GetAbsCgroupPath(cgroupcm.DefaultSelectedSubsys, cgroupPath)) {
			continue
		}

		err = cgroupcmutils.ApplyCPUWithRelativePath(cgroupPath, &cgroupcm.CPUData{CpuIdlePtr: &p.enableCPUIdle})
		if err != nil {
			general.Errorf("ApplyCPUWithRelativePath in %s with enableCPUIdle: %v in failed with error: %v",
				cgroupPath, p.enableCPUIdle, err)
		}
	}
}

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

	"k8s.io/apimachinery/pkg/util/sets"
	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	"github.com/kubewharf/katalyst-api/pkg/consts"
	advisorapi "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/cpuadvisor"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/util"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	cgroupcm "github.com/kubewharf/katalyst-core/pkg/util/cgroup/common"
	cgroupcmutils "github.com/kubewharf/katalyst-core/pkg/util/cgroup/manager"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

// checkCPUSet emit errors if the memory allocation falls into unexpected results
func (p *DynamicPolicy) checkCPUSet() {
	general.Infof("exec checkCPUSet")

	podEntries := p.state.GetPodEntries()
	actualCPUSets := make(map[string]map[string]machine.CPUSet)
	for podUID, containerEntries := range podEntries {
		if containerEntries.IsPoolEntry() {
			continue
		}

		for containerName, allocationInfo := range containerEntries {
			if allocationInfo == nil ||
				allocationInfo.ContainerType != pluginapi.ContainerType_MAIN.String() {
				continue
			} else if state.CheckShared(allocationInfo) && allocationInfo.RequestQuantity == 0 {
				general.Warningf("skip cpuset checking for pod: %s/%s container: %s with zero cpu request",
					allocationInfo.PodNamespace, allocationInfo.PodName, containerName)
				continue
			}

			tags := metrics.ConvertMapToTags(map[string]string{
				"podNamespace":  allocationInfo.PodNamespace,
				"podName":       allocationInfo.PodName,
				"containerName": allocationInfo.ContainerName,
			})

			containerId, err := p.metaServer.GetContainerID(podUID, containerName)
			if err != nil {
				general.Errorf("get container id of pod: %s container: %s failed with error: %v", podUID, containerName, err)
				continue
			}

			cpusetStats, err := cgroupcmutils.GetCPUSetForContainer(podUID, containerId)
			if err != nil {
				general.Errorf("GetCPUSet of pod: %s container: name(%s), id(%s) failed with error: %v",
					podUID, containerName, containerId, err)
				_ = p.emitter.StoreInt64(util.MetricNameRealStateInvalid, 1, metrics.MetricTypeNameRaw, tags...)
				continue
			}

			if actualCPUSets[podUID] == nil {
				actualCPUSets[podUID] = make(map[string]machine.CPUSet)
			}
			actualCPUSets[podUID][containerName] = machine.MustParse(cpusetStats.CPUs)

			general.Infof("pod: %s/%s, container: %s, state CPUSet: %s, actual CPUSet: %s",
				allocationInfo.PodNamespace, allocationInfo.PodName, allocationInfo.ContainerName,
				allocationInfo.AllocationResult.String(), actualCPUSets[podUID][containerName].String())

			// only do comparison for dedicated_cores with numa_biding to avoid effect of adjustment for shared_cores
			if !state.CheckDedicated(allocationInfo) {
				continue
			}

			if !actualCPUSets[podUID][containerName].Equals(allocationInfo.OriginalAllocationResult) {
				general.Errorf("pod: %s/%s, container: %s, cpuset invalid",
					allocationInfo.PodNamespace, allocationInfo.PodName, allocationInfo.ContainerName)
				_ = p.emitter.StoreInt64(util.MetricNameCPUSetInvalid, 1, metrics.MetricTypeNameRaw, tags...)
			}
		}
	}

	unionDedicatedCPUSet := machine.NewCPUSet()
	unionSharedCPUSet := machine.NewCPUSet()

	var cpuSetOverlap bool
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

	general.Infof("finish checkCPUSet")
}

// clearResidualState is used to clean residual pods in local state
func (p *DynamicPolicy) clearResidualState() {
	general.Infof("exec clearResidualState")
	residualSet := make(map[string]bool)

	if p.metaServer == nil {
		general.Errorf("nil metaServer")
		return
	}

	ctx := context.Background()
	podList, err := p.metaServer.GetPodList(ctx, nil)
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
			if p.enableCPUSysAdvisor {
				_, rErr = p.advisorClient.RemovePod(ctx, &advisorapi.RemovePodRequest{
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

		updatedMachineState, err := state.GenerateMachineStateFromPodEntries(p.machineInfo.CPUTopology, podEntries)
		if err != nil {
			general.Errorf("GenerateMachineStateFromPodEntries failed with error: %v", err)
			return
		}

		p.state.SetPodEntries(podEntries)
		p.state.SetMachineState(updatedMachineState)

		err = p.adjustAllocationEntries()
		if err != nil {
			general.ErrorS(err, "adjustAllocationEntries failed")
		}
	}
}

// syncCPUIdle is used to set cpu idle for reclaimed cores
func (p *DynamicPolicy) syncCPUIdle() {
	general.Infof("exec syncCPUIdle")
	if !cgroupcm.IsCPUIdleSupported() {
		general.Warningf("cpu idle isn't unsupported, skip syncing")
		return
	}

	err := cgroupcmutils.ApplyCPUWithRelativePath(p.reclaimRelativeRootCgroupPath, &cgroupcm.CPUData{CpuIdlePtr: &p.enableCPUIdle})
	if err != nil {
		general.Errorf("ApplyCPUWithRelativePath in %s with enableCPUIdle: %v in failed with error: %v",
			p.reclaimRelativeRootCgroupPath, p.enableCPUIdle, err)
	}
}

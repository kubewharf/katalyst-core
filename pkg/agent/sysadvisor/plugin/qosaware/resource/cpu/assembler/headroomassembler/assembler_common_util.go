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

package headroomassembler

import (
	"context"
	"fmt"
	"strconv"

	v1 "k8s.io/api/core/v1"

	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/klog/v2"

	"github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/qosaware/resource/helper"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	metaserverHelper "github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric/helper"
	"github.com/kubewharf/katalyst-core/pkg/util"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/native"
)

const (
	FakedNUMAID = "-1"
)

func (ha *HeadroomAssemblerCommon) getUtilBasedHeadroom(options helper.UtilBasedCapacityOptions,
	reclaimMetrics *metaserverHelper.ReclaimMetrics,
	lastReclaimedCPUPerNumaForCalculate map[int]float64,
) (resource.Quantity, error) {
	if reclaimMetrics == nil {
		return resource.Quantity{}, fmt.Errorf("reclaimMetrics is nil")
	}

	if reclaimMetrics.ReclaimedCoresSupply == 0 {
		return *resource.NewQuantity(0, resource.DecimalSI), nil
	}

	lastReclaimedCPU := 0.0
	for _, cpu := range lastReclaimedCPUPerNumaForCalculate {
		lastReclaimedCPU += cpu
	}

	lastOverload := ha.overloadState[reclaimMetrics.CgroupPath]

	headroom, overload, err := helper.EstimateUtilBasedCapacity(options, reclaimMetrics, lastReclaimedCPU, lastOverload)
	if err != nil {
		return resource.Quantity{}, err
	}
	ha.overloadState[reclaimMetrics.CgroupPath] = overload

	general.InfoS("getUtilBasedHeadroom", "reclaimMetrics", reclaimMetrics,
		"lastReclaimedCPUPerNumaForCalculate", lastReclaimedCPUPerNumaForCalculate, "headroom", headroom)

	return *resource.NewMilliQuantity(int64(headroom*1000), resource.DecimalSI), nil
}

func (ha *HeadroomAssemblerCommon) getLastReclaimedCPUPerNUMA() (map[int]float64, error) {
	cnr, err := ha.metaServer.CNRFetcher.GetCNR(context.Background())
	if err != nil {
		return nil, err
	}

	return util.GetReclaimedCPUPerNUMA(cnr.Status.TopologyZone), nil
}

func (ha *HeadroomAssemblerCommon) getReclaimNUMABindingTopo(reclaimPool *types.PoolInfo) (bindingNUMAs, nonBindingNumas []int, err error) {
	if ha.metaServer == nil {
		err = fmt.Errorf("invalid metaserver")
		return
	}

	if ha.metaServer.MachineInfo == nil {
		err = fmt.Errorf("invalid machaine info")
		return
	}

	if len(ha.metaServer.MachineInfo.Topology) == 0 {
		err = fmt.Errorf("invalid machaine topo")
		return
	}

	availNUMAs, _, e := helper.GetAvailableNUMAsAndReclaimedCores(ha.conf, ha.metaReader, ha.metaServer)
	if e != nil {
		err = fmt.Errorf("get available numa failed: %v", e)
		return
	}

	numaMap := make(map[int]bool)
	for numaID := range reclaimPool.TopologyAwareAssignments {
		if !availNUMAs.Contains(numaID) {
			continue
		}

		numaMap[numaID] = false
	}

	pods, e := ha.metaServer.GetPodList(context.TODO(), func(pod *v1.Pod) bool {
		if !native.PodIsActive(pod) {
			return false
		}

		if ok, err := ha.conf.CheckSystemQoSForPod(pod); err != nil || ok {
			klog.Errorf("filter system core pod %v err: %v", pod.Name, err)
			return false
		}

		return true
	})
	if e != nil {
		err = fmt.Errorf("get pod list failed: %v", e)
		return
	}

	for _, pod := range pods {
		qos, e := ha.conf.GetQoSLevel(pod, map[string]string{})
		if e != nil {
			err = fmt.Errorf("get qos level failed: %s, %v", pod.Name, e)
			return
		}

		switch qos {
		case consts.PodAnnotationQoSLevelReclaimedCores, consts.PodAnnotationQoSLevelSharedCores:
			numaRet, ok := pod.Annotations[consts.PodAnnotationNUMABindResultKey]
			if !ok || numaRet == FakedNUMAID {
				continue
			}

			numaID, err := strconv.Atoi(numaRet)
			if err != nil {
				klog.Errorf("invalid numa binding result: %s, %s, %v", pod.Name, numaRet, err)
				continue
			}

			if _, ok := numaMap[numaID]; !ok {
				continue
			}

			numaMap[numaID] = true
		case consts.PodAnnotationQoSLevelDedicatedCores:
			containers, ok := ha.metaReader.GetContainerEntries(string(pod.UID))
			if !ok {
				klog.Warningf("get container entries failed: %s, %s, not found", pod.Name, pod.UID)
				continue
			}

			for _, ci := range containers {
				for numaID := range ci.TopologyAwareAssignments {
					if _, ok := numaMap[numaID]; !ok {
						continue
					}
					numaMap[numaID] = true
				}
			}
		}
	}

	for numaID, bound := range numaMap {
		if bound {
			bindingNUMAs = append(bindingNUMAs, numaID)
		} else {
			nonBindingNumas = append(nonBindingNumas, numaID)
		}
	}

	return
}

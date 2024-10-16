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
	"math"
	"strconv"

	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/klog/v2"

	"github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/qosaware/resource/helper"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	metaserverHelper "github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric/helper"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

func (ha *HeadroomAssemblerCommon) getUtilBasedHeadroom(options helper.UtilBasedCapacityOptions,
	reclaimMetrics *metaserverHelper.ReclaimMetrics,
) (resource.Quantity, error) {
	lastReclaimedCPU, err := ha.getLastReclaimedCPU()
	if err != nil {
		return resource.Quantity{}, err
	}
	if reclaimMetrics == nil {
		return resource.Quantity{}, fmt.Errorf("reclaimMetrics is nil")
	}

	if reclaimMetrics.ReclaimedCoresSupply == 0 {
		return *resource.NewQuantity(0, resource.DecimalSI), nil
	}

	util := reclaimMetrics.CgroupCPUUsage / reclaimMetrics.ReclaimedCoresSupply

	general.InfoS("getUtilBasedHeadroom", "reclaimedCoresSupply", reclaimMetrics.ReclaimedCoresSupply,
		"util", util, "reclaim PoolCPUUsage", reclaimMetrics.PoolCPUUsage, "reclaim CgroupCPUUsage", reclaimMetrics.CgroupCPUUsage,
		"lastReclaimedCPU", lastReclaimedCPU)

	headroom, err := helper.EstimateUtilBasedCapacity(
		options,
		reclaimMetrics.ReclaimedCoresSupply,
		util,
		lastReclaimedCPU,
	)
	if err != nil {
		return resource.Quantity{}, err
	}

	return *resource.NewQuantity(int64(math.Ceil(headroom)), resource.DecimalSI), nil
}

func (ha *HeadroomAssemblerCommon) getLastReclaimedCPU() (float64, error) {
	cnr, err := ha.metaServer.CNRFetcher.GetCNR(context.Background())
	if err != nil {
		return 0, err
	}

	if cnr.Status.Resources.Allocatable != nil {
		if reclaimedMilliCPU, ok := (*cnr.Status.Resources.Allocatable)[consts.ReclaimedResourceMilliCPU]; ok {
			return float64(reclaimedMilliCPU.Value()) / 1000, nil
		}
	}

	klog.Errorf("cnr status resource allocatable reclaimed milli cpu not found")
	return 0, nil
}

func (ha *HeadroomAssemblerCommon) getReclaimNUMABindingTopo(reclaimPool *types.PoolInfo) (bindingNUMAs, nonBindingNumas []int, err error) {
	if ha.metaServer == nil || ha.metaServer.MachineInfo == nil || len(ha.metaServer.MachineInfo.Topology) == 0 {
		err = fmt.Errorf("invalid machaine topo")
		return
	}

	numaMap := make(map[int]bool)

	for numaID := range reclaimPool.TopologyAwareAssignments {
		numaMap[numaID] = false
	}

	f := func(podUID string, containerName string, ci *types.ContainerInfo) bool {
		if ci == nil {
			return true
		}

		if ci.QoSLevel != consts.PodAnnotationQoSLevelReclaimedCores {
			return true
		}

		numaRet, ok := ci.Annotations[consts.PodAnnotationNUMABindResultKey]
		if !ok || numaRet == "-1" {
			return true
		}

		numaID, err := strconv.Atoi(numaRet)
		if err != nil {
			klog.Errorf("invalid numa binding result: %s, %v\n", numaRet, err)
			return true
		}

		numaMap[numaID] = true
		return true
	}
	ha.metaReader.RangeContainer(f)

	for numaID, bound := range numaMap {
		if bound {
			bindingNUMAs = append(bindingNUMAs, numaID)
		} else {
			nonBindingNumas = append(nonBindingNumas, numaID)
		}
	}

	return
}

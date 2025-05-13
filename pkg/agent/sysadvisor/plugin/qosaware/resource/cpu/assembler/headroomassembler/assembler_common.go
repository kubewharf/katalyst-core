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
	"fmt"
	"math"

	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/qosaware/resource/cpu/region"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/qosaware/resource/helper"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	metricHelper "github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric/helper"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/cgroup/common"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

type HeadroomAssemblerCommon struct {
	conf               *config.Configuration
	regionMap          *map[string]region.QoSRegion
	reservedForReclaim *map[int]int
	numaAvailable      *map[int]int
	nonBindingNumas    *machine.CPUSet

	metaReader metacache.MetaReader
	metaServer *metaserver.MetaServer
	emitter    metrics.MetricEmitter
}

func NewHeadroomAssemblerCommon(conf *config.Configuration, _ interface{}, regionMap *map[string]region.QoSRegion,
	reservedForReclaim *map[int]int, numaAvailable *map[int]int, nonBindingNumas *machine.CPUSet, metaReader metacache.MetaReader,
	metaServer *metaserver.MetaServer, emitter metrics.MetricEmitter,
) HeadroomAssembler {
	return &HeadroomAssemblerCommon{
		conf:               conf,
		regionMap:          regionMap,
		reservedForReclaim: reservedForReclaim,
		numaAvailable:      numaAvailable,
		nonBindingNumas:    nonBindingNumas,

		metaReader: metaReader,
		metaServer: metaServer,
		emitter:    emitter,
	}
}

func (ha *HeadroomAssemblerCommon) GetHeadroom() (resource.Quantity, map[int]resource.Quantity, error) {
	dynamicConfig := ha.conf.GetDynamicConfiguration()

	// return zero when reclaim is disabled
	if !dynamicConfig.EnableReclaim {
		general.Infof("reclaim is NOT enabled")
		return *resource.NewQuantity(0, resource.DecimalSI), nil, nil
	}

	if !dynamicConfig.CPUUtilBasedConfiguration.Enable {
		reclaimPoolInfo, reclaimPoolExist := ha.metaReader.GetPoolInfo(commonstate.PoolNameReclaim)
		if !reclaimPoolExist || reclaimPoolInfo == nil {
			return resource.Quantity{}, nil, fmt.Errorf("get headroom failed: reclaim pool not found")
		}

		bindingNUMAs, nonBindingNumas, err := ha.getReclaimNUMABindingTopo(reclaimPoolInfo)
		if err != nil {
			general.Errorf("getReclaimNUMABindingTop failed: %v", err)
			return resource.Quantity{}, nil, err
		}

		general.Infof("RNB NUMA topo: %v, %v", bindingNUMAs, nonBindingNumas)

		numaHeadroom := make(map[int]resource.Quantity, ha.metaServer.NumNUMANodes)
		totalHeadroom := resource.Quantity{}

		// get headroom per NUMA
		for _, numaID := range bindingNUMAs {
			cpuSet, ok := reclaimPoolInfo.TopologyAwareAssignments[numaID]
			if !ok {
				return resource.Quantity{}, nil, fmt.Errorf("reclaim pool NOT found TopologyAwareAssignments with numaID: %v", numaID)
			}

			reclaimPath := common.GetReclaimRelativeRootCgroupPath(ha.conf.ReclaimRelativeRootCgroupPath, numaID)
			reclaimMetrics, err := metricHelper.GetReclaimMetrics(cpuSet, reclaimPath, ha.metaServer.MetricsFetcher)
			if err != nil {
				return resource.Quantity{}, nil, fmt.Errorf("get reclaim Metrics failed with numa %d: %v", numaID, err)
			}

			headroom := *resource.NewQuantity(int64(math.Ceil(reclaimMetrics.ReclaimedCoresSupply)), resource.DecimalSI)
			numaHeadroom[numaID] = headroom
			totalHeadroom.Add(headroom)
		}

		// get global reclaim headroom
		if len(nonBindingNumas) > 0 {
			cpuSets := machine.NewCPUSet()
			for _, numaID := range nonBindingNumas {
				cpuSet, ok := reclaimPoolInfo.TopologyAwareAssignments[numaID]
				if !ok {
					return resource.Quantity{}, nil, fmt.Errorf("reclaim pool NOT found TopologyAwareAssignments with numaID: %v", numaID)
				}

				cpuSets = cpuSets.Union(cpuSet)
			}

			reclaimMetrics, err := metricHelper.GetReclaimMetrics(cpuSets, common.GetReclaimRelativeRootCgroupPath(ha.conf.ReclaimRelativeRootCgroupPath, commonstate.FakedNUMAID), ha.metaServer.MetricsFetcher)
			if err != nil {
				return resource.Quantity{}, nil, fmt.Errorf("get reclaim Metrics failed: %v", err)
			}

			headroomPerNUMA := reclaimMetrics.ReclaimedCoresSupply / float64(len(nonBindingNumas))
			for _, numaID := range nonBindingNumas {
				q := *resource.NewQuantity(int64(headroomPerNUMA), resource.DecimalSI)
				numaHeadroom[numaID] = q
				totalHeadroom.Add(q)
			}
		}

		general.InfoS("[qosaware-cpu] get headroom ret", "total", totalHeadroom.Value())
		for numaID, headroom := range numaHeadroom {
			general.InfoS("[qosaware-cpu] get headroom per numa", "NUMA-ID", numaID, "headroom", headroom.Value())
		}

		allNUMAs := ha.metaServer.CPUDetails.NUMANodes()
		for _, numaID := range allNUMAs.ToSliceInt() {
			if _, ok := numaHeadroom[numaID]; !ok {
				general.InfoS("set non-reclaim NUMA cpu headroom as empty", "NUMA-ID", numaID)
				numaHeadroom[numaID] = *resource.NewQuantity(0, resource.BinarySI)
			}
		}

		return totalHeadroom, numaHeadroom, nil
	}

	return ha.getHeadroomByUtil()
}

func (ha *HeadroomAssemblerCommon) getHeadroomByUtil() (resource.Quantity, map[int]resource.Quantity, error) {
	reclaimPoolInfo, reclaimPoolExist := ha.metaReader.GetPoolInfo(commonstate.PoolNameReclaim)
	if !reclaimPoolExist || reclaimPoolInfo == nil {
		return resource.Quantity{}, nil, fmt.Errorf("get headroom by util failed: reclaim pool not found")
	}

	bindingNUMAs, nonBindingNumas, err := ha.getReclaimNUMABindingTopo(reclaimPoolInfo)
	if err != nil {
		general.Errorf("getReclaimNUMABindingTop failed: %v", err)
		return resource.Quantity{}, nil, err
	}
	general.Infof("RNB NUMA topo: %v, %v", bindingNUMAs, nonBindingNumas)

	numaHeadroom := make(map[int]resource.Quantity, ha.metaServer.NumNUMANodes)
	totalHeadroom := resource.Quantity{}
	dynamicConfig := ha.conf.GetDynamicConfiguration()
	options := helper.UtilBasedCapacityOptions{
		TargetUtilization: dynamicConfig.TargetReclaimedCoreUtilization,
		MaxUtilization:    dynamicConfig.MaxReclaimedCoreUtilization,
		MaxOversoldRate:   dynamicConfig.CPUUtilBasedConfiguration.MaxOversoldRate,
		MaxCapacity:       dynamicConfig.MaxHeadroomCapacityRate * float64(ha.metaServer.MachineInfo.NumCores/ha.metaServer.NumNUMANodes),
	}

	reclaimedCPUs, err := ha.getLastReclaimedCPUPerNUMA()
	if err != nil {
		general.Errorf("getLastReclaimedCPUPerNUMA failed: %v", err)
		return resource.Quantity{}, nil, err
	}

	// get headroom per NUMA
	for _, numaID := range bindingNUMAs {
		cpuSet, ok := reclaimPoolInfo.TopologyAwareAssignments[numaID]
		if !ok {
			return resource.Quantity{}, nil, fmt.Errorf("reclaim pool NOT found TopologyAwareAssignments with numaID: %v", numaID)
		}

		reclaimPath := common.GetReclaimRelativeRootCgroupPath(ha.conf.ReclaimRelativeRootCgroupPath, numaID)
		reclaimMetrics, err := metricHelper.GetReclaimMetrics(cpuSet, reclaimPath, ha.metaServer.MetricsFetcher)
		if err != nil {
			return resource.Quantity{}, nil, fmt.Errorf("get reclaim Metrics failed with numa %d: %v", numaID, err)
		}

		lastReclaimedCPUPerNumaForCalculate := make(map[int]float64)
		lastReclaimedCPUPerNumaForCalculate[numaID] = reclaimedCPUs[numaID]
		headroom, err := ha.getUtilBasedHeadroom(options, reclaimMetrics, lastReclaimedCPUPerNumaForCalculate)
		if err != nil {
			return resource.Quantity{}, nil, fmt.Errorf("get util-based headroom failed with numa %d: %v", numaID, err)
		}

		numaHeadroom[numaID] = headroom
		totalHeadroom.Add(headroom)
	}

	// get global reclaim headroom
	if len(nonBindingNumas) > 0 {
		cpusets := machine.NewCPUSet()
		lastReclaimedCPUPerNumaForCalculate := make(map[int]float64)
		for _, numaID := range nonBindingNumas {
			cpuSet, ok := reclaimPoolInfo.TopologyAwareAssignments[numaID]
			if !ok {
				return resource.Quantity{}, nil, fmt.Errorf("reclaim pool NOT found TopologyAwareAssignments with numaID: %v", numaID)
			}

			cpusets = cpusets.Union(cpuSet)
			lastReclaimedCPUPerNumaForCalculate[numaID] = reclaimedCPUs[numaID]
		}

		reclaimMetrics, err := metricHelper.GetReclaimMetrics(cpusets, common.GetReclaimRelativeRootCgroupPath(ha.conf.ReclaimRelativeRootCgroupPath, commonstate.FakedNUMAID), ha.metaServer.MetricsFetcher)
		if err != nil {
			return resource.Quantity{}, nil, fmt.Errorf("get reclaim Metrics failed: %v", err)
		}

		options.MaxCapacity *= float64(len(nonBindingNumas))
		headroom, err := ha.getUtilBasedHeadroom(options, reclaimMetrics, lastReclaimedCPUPerNumaForCalculate)
		if err != nil {
			return resource.Quantity{}, nil, fmt.Errorf("get util-based headroom failed: %v", err)
		}

		headroomPerNUMA := float64(headroom.Value()) / float64(len(nonBindingNumas))
		for _, numaID := range nonBindingNumas {
			q := *resource.NewQuantity(int64(headroomPerNUMA), resource.DecimalSI)
			numaHeadroom[numaID] = q
			totalHeadroom.Add(q)
		}
	}

	general.InfoS("[qosaware-cpu] headroom by utilization", "total", totalHeadroom.Value())
	for numaID, headroom := range numaHeadroom {
		general.InfoS("[qosaware-cpu] NUMA headroom by utilization", "NUMA-ID", numaID, "headroom", headroom.Value())
	}

	allNUMAs := ha.metaServer.CPUDetails.NUMANodes()
	for _, numaID := range allNUMAs.ToSliceInt() {
		if _, ok := numaHeadroom[numaID]; !ok {
			general.InfoS("set non-reclaim NUMA cpu headroom as empty", "NUMA-ID", numaID)
			numaHeadroom[numaID] = *resource.NewQuantity(0, resource.BinarySI)
		}
	}
	return totalHeadroom, numaHeadroom, nil
}

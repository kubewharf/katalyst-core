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

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/klog/v2"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/qosaware/resource/cpu/region"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/qosaware/resource/helper"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	metricHelper "github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric/helper"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
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

	reserved := ha.conf.GetDynamicConfiguration().ReservedResourceForAllocate[v1.ResourceCPU]
	headroomTotal := 0.0
	headroomNuma := make(map[int]float64)
	emptyNUMAs := ha.metaServer.CPUDetails.NUMANodes()

	reclaimPoolInfo, reclaimPoolExist := ha.metaReader.GetPoolInfo(commonstate.PoolNameReclaim)

	// sum up dedicated region headroom
	for _, r := range *ha.regionMap {
		if r.EnableReclaim() && reclaimPoolExist && reclaimPoolInfo != nil {
			for _, numaID := range r.GetBindingNumas().ToSliceInt() {
				headroomNuma[numaID] = float64(reclaimPoolInfo.TopologyAwareAssignments[numaID].Size())
				general.InfoS("region headroom", "region", r.Name(), "numaID", numaID, "headroom", headroomNuma[numaID])
			}
		}

		emptyNUMAs = emptyNUMAs.Difference(r.GetBindingNumas())
	}

	// add empty numa headroom
	for _, numaID := range emptyNUMAs.ToSliceInt() {
		available, _ := (*ha.numaAvailable)[numaID]
		reservedForAllocate := float64(reserved.Value()*int64(emptyNUMAs.Size())) / float64(ha.metaServer.NumNUMANodes)
		reservedForReclaim, _ := (*ha.reservedForReclaim)[numaID]
		headroom := float64(available) - reservedForAllocate + float64(reservedForReclaim)
		headroomNuma[numaID] = headroom

		klog.InfoS("empty NUMA headroom", "headroom", headroom)
	}

	for numaID, headroom := range headroomNuma {
		general.InfoS("[qosaware-cpu] NUMA headroom assembled", "NUMA-ID", numaID, "headroom", headroom)
		headroomTotal += headroom
	}
	general.InfoS("[qosaware-cpu] headroom assembled", "headroomTotal", headroomTotal,
		"util based enabled", dynamicConfig.CPUUtilBasedConfiguration.Enable)

	// if util based cpu headroom disable or reclaim pool not existed, just return total reclaim pool size as headroom
	if !dynamicConfig.CPUUtilBasedConfiguration.Enable || !reclaimPoolExist || reclaimPoolInfo == nil {
		headroomNumaRet := make(map[int]resource.Quantity)
		for numaID, headroom := range headroomNuma {
			headroomNumaRet[numaID] = *resource.NewMilliQuantity(int64(headroom*1000), resource.DecimalSI)
		}

		allNUMAs := ha.metaServer.CPUDetails.NUMANodes()
		for _, numaID := range allNUMAs.ToSliceInt() {
			if _, ok := headroomNumaRet[numaID]; !ok {
				general.InfoS("set non-reclaim NUMA cpu headroom as empty", "NUMA-ID", numaID)
				headroomNumaRet[numaID] = *resource.NewQuantity(0, resource.BinarySI)
			}
		}
		return *resource.NewQuantity(int64(headroomTotal), resource.DecimalSI), headroomNumaRet, nil
	}

	return ha.getHeadroomByUtil()
}

func (ha *HeadroomAssemblerCommon) getReclaimCgroupPathByNUMA(numaID int) string {
	return fmt.Sprintf("%s-%d", ha.conf.ReclaimRelativeRootCgroupPath, numaID)
}

func (ha *HeadroomAssemblerCommon) getReclaimCgroupPath() string {
	return ha.conf.ReclaimRelativeRootCgroupPath
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
		MaxOversoldRate:   dynamicConfig.MaxOversoldRate,
		MaxCapacity:       dynamicConfig.MaxHeadroomCapacityRate * float64(ha.metaServer.MachineInfo.NumCores/ha.metaServer.NumNUMANodes),
	}

	// get headroom per NUMA
	for _, numaID := range bindingNUMAs {
		cpuSet, ok := reclaimPoolInfo.TopologyAwareAssignments[numaID]
		if !ok {
			return resource.Quantity{}, nil, fmt.Errorf("reclaim pool NOT found TopologyAwareAssignments with numaID: %v", numaID)
		}

		reclaimMetrics, err := metricHelper.GetReclaimMetrics(cpuSet, ha.getReclaimCgroupPathByNUMA(numaID), ha.metaServer.MetricsFetcher)
		if err != nil {
			return resource.Quantity{}, nil, fmt.Errorf("get reclaim Metrics failed with numa %d: %v", numaID, err)
		}

		headroom, err := ha.getUtilBasedHeadroom(options, reclaimMetrics)
		if err != nil {
			return resource.Quantity{}, nil, fmt.Errorf("get util-based headroom failed with numa %d: %v", numaID, err)
		}

		numaHeadroom[numaID] = headroom
		totalHeadroom.Add(headroom)
	}

	// get global reclaim headroom
	if len(nonBindingNumas) > 0 {
		cpusets := machine.NewCPUSet()
		for _, numaID := range nonBindingNumas {
			cpuSet, ok := reclaimPoolInfo.TopologyAwareAssignments[numaID]
			if !ok {
				return resource.Quantity{}, nil, fmt.Errorf("reclaim pool NOT found TopologyAwareAssignments with numaID: %v", numaID)
			}

			cpusets = cpusets.Union(cpuSet)
		}

		reclaimMetrics, err := metricHelper.GetReclaimMetrics(cpusets, ha.getReclaimCgroupPath(), ha.metaServer.MetricsFetcher)
		if err != nil {
			return resource.Quantity{}, nil, fmt.Errorf("get reclaim Metrics failed: %v", err)
		}

		options.MaxCapacity *= float64(len(nonBindingNumas))
		headroom, err := ha.getUtilBasedHeadroom(options, reclaimMetrics)
		if err != nil {
			return resource.Quantity{}, nil, fmt.Errorf("get util-based headroom failed: %v", err)
		}

		totalHeadroom.Add(headroom)
		headroomPerNUMA := float64(headroom.Value()) / float64(len(nonBindingNumas))
		for _, numaID := range nonBindingNumas {
			numaHeadroom[numaID] = *resource.NewQuantity(int64(headroomPerNUMA), resource.DecimalSI)
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

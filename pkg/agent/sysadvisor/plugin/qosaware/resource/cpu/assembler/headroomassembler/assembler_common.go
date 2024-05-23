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
	"k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/klog/v2"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/qosaware/resource/cpu/region"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/config"
	metric_consts "github.com/kubewharf/katalyst-core/pkg/consts"
	pkgconsts "github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
	"github.com/kubewharf/katalyst-core/pkg/util/metric"
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

	backoffRetries int
	backoffStep    int
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

		metaReader:  metaReader,
		metaServer:  metaServer,
		emitter:     emitter,
		backoffStep: 2,
	}
}

func (ha *HeadroomAssemblerCommon) GetHeadroom() (resource.Quantity, error) {
	dynamicConfig := ha.conf.GetDynamicConfiguration()
	reserved := ha.conf.GetDynamicConfiguration().ReservedResourceForAllocate[v1.ResourceCPU]

	// return zero when reclaim is disabled
	if !dynamicConfig.EnableReclaim {
		return *resource.NewQuantity(0, resource.DecimalSI), nil
	}

	headroomTotal := 0.0
	emptyNUMAs := ha.metaServer.CPUDetails.NUMANodes()
	exclusiveNUMAs := machine.NewCPUSet()

	hasUpperBound := false

	// sum up dedicated region headroom
	for _, r := range *ha.regionMap {
		if r.Type() == types.QoSRegionTypeDedicatedNumaExclusive {
			regionInfo, ok := ha.metaReader.GetRegionInfo(r.Name())
			if !ok || regionInfo == nil || regionInfo.Headroom < 0 {
				return resource.Quantity{}, fmt.Errorf("failed to get headroom for %v", r.Name())
			}
			if regionInfo.RegionStatus.BoundType == types.BoundUpper {
				general.Infof("region %v is in status of upper bound", regionInfo.RegionName)
				hasUpperBound = true
			}
			headroomTotal += regionInfo.Headroom
			exclusiveNUMAs = exclusiveNUMAs.Union(r.GetBindingNumas())

			klog.InfoS("dedicated_cores NUMA headroom", "headroom", regionInfo.Headroom, "NUMAs", r.GetBindingNumas().String())
		}
		emptyNUMAs = emptyNUMAs.Difference(r.GetBindingNumas())
	}

	reclaimPoolUtil := 0.0

	// add non binding reclaim pool size
	reclaimPoolInfo, ok := ha.metaReader.GetPoolInfo(state.PoolNameReclaim)
	if ok && reclaimPoolInfo != nil {

		reclaimedMetrics, err := ha.getPoolMetrics(state.PoolNameReclaim)
		if err != nil {
			return resource.Quantity{}, err
		}
		reclaimPoolUtil = reclaimedMetrics.coreAvgUtil

		reclaimPoolNUMAs := machine.GetCPUAssignmentNUMAs(reclaimPoolInfo.TopologyAwareAssignments)

		sharedCoresHeadroom := 0.0
		if dynamicConfig.AllowSharedCoresOverlapReclaimedCores {
			regionMetrics, err := ha.getShareRegionMetrics()
			if err != nil {
				return *resource.NewQuantity(0, resource.DecimalSI), err
			}

			for _, regionMetric := range regionMetrics {
				sharedCoresHeadroom += general.MaxFloat64(float64(regionMetric.cpus)-regionMetric.cpuUsage, 0)
				general.InfoS("shared_cores region headroom", "regionName", regionMetric.regionInfo.RegionName, "cpus", regionMetric.cpus, "cpu usage", regionMetric.cpuUsage)
				if regionMetric.regionInfo.RegionStatus.BoundType == types.BoundUpper {
					general.Infof("region %v is in status of upper bound", regionMetric.regionInfo.RegionName)
					hasUpperBound = true
				}
			}

			klog.InfoS("shared_cores headroom", "headroom", sharedCoresHeadroom)
		} else {
			for _, numaID := range reclaimPoolNUMAs.Difference(exclusiveNUMAs).Difference(emptyNUMAs).ToSliceInt() {
				headroom := float64(reclaimPoolInfo.TopologyAwareAssignments[numaID].Size())
				sharedCoresHeadroom += headroom

				klog.InfoS("shared_cores headroom", "headroom", headroom, "numaID", numaID)
			}
		}

		headroomTotal += sharedCoresHeadroom
	}

	// add empty numa headroom
	for _, numaID := range emptyNUMAs.ToSliceInt() {
		available, _ := (*ha.numaAvailable)[numaID]
		reservedForAllocate := float64(reserved.Value()*int64(emptyNUMAs.Size())) / float64(ha.metaServer.NumNUMANodes)
		reservedForReclaim, _ := (*ha.reservedForReclaim)[numaID]
		headroom := float64(available) - reservedForAllocate + float64(reservedForReclaim)
		headroomTotal += headroom

		klog.InfoS("empty NUMA headroom", "headroom", headroom)
	}

	if hasUpperBound {
		ha.backoffRetries++
		headroomTotal = general.MaxFloat64(0, headroomTotal-float64(ha.backoffRetries*ha.backoffStep))
	} else {
		ha.backoffRetries = 0
	}

	general.InfoS("[qosaware-cpu] headroom assembled", "headroomTotal", headroomTotal, "backoffRetries",
		ha.backoffRetries, "util based enabled", dynamicConfig.CPUUtilBasedConfiguration.Enable)

	// if util based cpu headroom disable, just return total reclaim pool size as headroom
	if !dynamicConfig.CPUUtilBasedConfiguration.Enable {
		return *resource.NewQuantity(int64(headroomTotal), resource.DecimalSI), nil
	}

	return ha.getUtilBasedHeadroom(dynamicConfig, int(headroomTotal), reclaimPoolUtil)
}

type poolMetrics struct {
	coreAvgUtil float64
	poolSize    int
}

type RegionMetrics struct {
	cpuUsage   float64
	cpus       int
	regionInfo *types.RegionInfo
}

// getPoolMetrics get reclaimed pool metrics, including the average utilization of each core in
// the reclaimed pool and the size of the pool
func (ha *HeadroomAssemblerCommon) getPoolMetrics(poolName string) (*poolMetrics, error) {
	reclaimedInfo, ok := ha.metaReader.GetPoolInfo(poolName)
	if !ok {
		return nil, fmt.Errorf("failed get reclaim pool info")
	}

	cpuSet := reclaimedInfo.TopologyAwareAssignments.MergeCPUSet()
	m := ha.metaServer.AggregateCoreMetric(cpuSet, pkgconsts.MetricCPUUsageRatio, metric.AggregatorAvg)
	return &poolMetrics{
		coreAvgUtil: m.Value,
		poolSize:    cpuSet.Size(),
	}, nil
}

func (ha *HeadroomAssemblerCommon) getShareRegionMetrics() ([]*RegionMetrics, error) {
	var errList []error
	metrics := make([]*RegionMetrics, 0)
	ha.metaReader.RangeRegionInfo(func(regionName string, regionInfo *types.RegionInfo) bool {
		if regionInfo.RegionType == types.QoSRegionTypeShare {

			size, exist := ha.metaReader.GetPoolSize(regionInfo.OwnerPoolName)
			if !exist {
				errList = append(errList, fmt.Errorf("pool %v not exists", regionInfo.OwnerPoolName))
				return true
			}
			regionMetric := &RegionMetrics{
				cpuUsage:   0,
				cpus:       size,
				regionInfo: regionInfo,
			}

			for podName, containers := range regionInfo.Pods {
				for containerName := range containers {
					data, err := ha.metaServer.GetContainerMetric(podName, containerName, metric_consts.MetricCPUUsageContainer)
					if err != nil {
						errList = append(errList, err)
						return true
					}
					regionMetric.cpuUsage += data.Value
				}
			}

			metrics = append(metrics, regionMetric)
		}
		return true
	})
	return metrics, errors.NewAggregate(errList)
}

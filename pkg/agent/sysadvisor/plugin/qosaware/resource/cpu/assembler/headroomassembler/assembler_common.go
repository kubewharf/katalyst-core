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

	configapi "github.com/kubewharf/katalyst-api/pkg/apis/config/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/qosaware/resource/cpu/region"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric/helper"
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

	// return zero when reclaim is disabled
	if !dynamicConfig.EnableReclaim {
		return *resource.NewQuantity(0, resource.DecimalSI), nil
	}

	reserved := ha.conf.GetDynamicConfiguration().ReservedResourceForAllocate[v1.ResourceCPU]
	headroomTotal := 0.0
	emptyNUMAs := ha.metaServer.CPUDetails.NUMANodes()
	exclusiveNUMAs := machine.NewCPUSet()

	hasUpperBound := false

	// sum up dedicated region headroom
	for _, r := range *ha.regionMap {
		if r.Type() == configapi.QoSRegionTypeDedicatedNumaExclusive {
			regionInfo, ok := ha.metaReader.GetRegionInfo(r.Name())
			if !ok || regionInfo == nil || regionInfo.Headroom < 0 {
				return resource.Quantity{}, fmt.Errorf("failed to get headroom for %v", r.Name())
			}
			if regionInfo.RegionStatus.BoundType == types.BoundUpper && r.EnableReclaim() {
				general.Infof("region %v is in status of upper bound", regionInfo.RegionName)
				hasUpperBound = true
			}
			headroomTotal += regionInfo.Headroom
			exclusiveNUMAs = exclusiveNUMAs.Union(r.GetBindingNumas())

			klog.InfoS("dedicated_cores NUMA headroom", "headroom", regionInfo.Headroom, "NUMAs", r.GetBindingNumas().String())
		}
		emptyNUMAs = emptyNUMAs.Difference(r.GetBindingNumas())
	}

	// add non binding reclaim pool size
	reclaimPoolInfo, reclaimPoolExist := ha.metaReader.GetPoolInfo(commonstate.PoolNameReclaim)
	if reclaimPoolExist && reclaimPoolInfo != nil {
		reclaimPoolNUMAs := machine.GetCPUAssignmentNUMAs(reclaimPoolInfo.TopologyAwareAssignments)

		sharedCoresHeadroom := 0.0
		for _, numaID := range reclaimPoolNUMAs.Difference(exclusiveNUMAs).Difference(emptyNUMAs).ToSliceInt() {
			headroom := float64(reclaimPoolInfo.TopologyAwareAssignments[numaID].Size())
			sharedCoresHeadroom += headroom

			klog.InfoS("shared_cores headroom", "headroom", headroom, "numaID", numaID)
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

	// if util based cpu headroom disable or reclaim pool not existed, just return total reclaim pool size as headroom
	if !dynamicConfig.CPUUtilBasedConfiguration.Enable || !reclaimPoolExist || reclaimPoolInfo == nil {
		return *resource.NewQuantity(int64(headroomTotal), resource.DecimalSI), nil
	}

	reclaimMetrics, err := helper.GetReclaimMetrics(reclaimPoolInfo.TopologyAwareAssignments.MergeCPUSet(), ha.conf.ReclaimRelativeRootCgroupPath, ha.metaServer.MetricsFetcher)
	if err != nil {
		return resource.Quantity{}, err
	}

	return ha.getUtilBasedHeadroom(dynamicConfig, reclaimMetrics)
}

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

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/qosaware/resource/cpu/region"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

type HeadroomAssemblerDedicated struct {
	conf               *config.Configuration
	regionMap          *map[string]region.QoSRegion
	reservedForReclaim *map[int]int
	numaAvailable      *map[int]int
	nonBindingNumas    *machine.CPUSet

	metaReader metacache.MetaReader
	metaServer *metaserver.MetaServer
	emitter    metrics.MetricEmitter
}

func NewHeadroomAssemblerDedicated(conf *config.Configuration, _ interface{}, regionMap *map[string]region.QoSRegion,
	reservedForReclaim *map[int]int, numaAvailable *map[int]int, nonBindingNumas *machine.CPUSet,
	metaReader metacache.MetaReader, metaServer *metaserver.MetaServer, emitter metrics.MetricEmitter) HeadroomAssembler {
	return &HeadroomAssemblerDedicated{
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

func (ha *HeadroomAssemblerDedicated) GetHeadroom() (resource.Quantity, error) {
	dynamicConfig := ha.conf.GetDynamicConfiguration()
	reserved := ha.conf.GetDynamicConfiguration().ReservedResourceForAllocate[v1.ResourceCPU]

	// return zero when reclaim is disabled
	if !dynamicConfig.EnableReclaim {
		return *resource.NewQuantity(0, resource.DecimalSI), nil
	}

	headroomTotal := 0.0
	emptyNUMAs := ha.metaServer.CPUDetails.NUMANodes()
	exclusiveNUMAs := machine.NewCPUSet()

	// sum up dedicated region headroom
	for _, r := range *ha.regionMap {
		if r.Type() == types.QoSRegionTypeDedicatedNumaExclusive {
			regionInfo, ok := ha.metaReader.GetRegionInfo(r.Name())
			if !ok || regionInfo == nil || regionInfo.Headroom < 0 {
				return resource.Quantity{}, fmt.Errorf("failed to get headroom for %v", r.Name())
			}
			headroomTotal += regionInfo.Headroom
			exclusiveNUMAs = exclusiveNUMAs.Union(r.GetBindingNumas())

			klog.InfoS("dedicated NUMA headroom", "headroom", regionInfo.Headroom, "NUMAs", r.GetBindingNumas().String())
		}
		emptyNUMAs = emptyNUMAs.Difference(r.GetBindingNumas())
	}

	// add non binding reclaim pool size
	reclaimPoolInfo, ok := ha.metaReader.GetPoolInfo(state.PoolNameReclaim)
	if ok && reclaimPoolInfo != nil {
		reclaimPoolNUMAs := machine.GetCPUAssignmentNUMAs(reclaimPoolInfo.TopologyAwareAssignments)
		for _, numaID := range reclaimPoolNUMAs.Difference(exclusiveNUMAs).Difference(emptyNUMAs).ToSliceInt() {
			headroom := float64(reclaimPoolInfo.TopologyAwareAssignments[numaID].Size())
			headroomTotal += headroom

			klog.InfoS("reclaim pool headroom", "headroom", headroom, "numaID", numaID)
		}
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

	klog.Infof("[qosaware-cpu] total headroom assembled %.2f", headroomTotal)

	return *resource.NewQuantity(int64(headroomTotal), resource.DecimalSI), nil
}

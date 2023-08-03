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

package provisionassembler

import (
	"time"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/cpuadvisor"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/qosaware/resource/cpu/region"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

type ProvisionAssemblerCommon struct {
	conf               *config.Configuration
	regionMap          *map[string]region.QoSRegion
	reservedForReclaim *map[int]int
	numaAvailable      *map[int]int
	nonBindingNumas    *machine.CPUSet

	metaReader metacache.MetaReader
	metaServer *metaserver.MetaServer
	emitter    metrics.MetricEmitter
}

func NewProvisionAssemblerCommon(conf *config.Configuration, _ interface{}, regionMap *map[string]region.QoSRegion,
	reservedForReclaim *map[int]int, numaAvailable *map[int]int, nonBindingNumas *machine.CPUSet,
	metaReader metacache.MetaReader, metaServer *metaserver.MetaServer, emitter metrics.MetricEmitter) ProvisionAssembler {
	return &ProvisionAssemblerCommon{
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

func (pa *ProvisionAssemblerCommon) AssembleProvision() (types.InternalCPUCalculationResult, bool, error) {
	enableReclaim := pa.conf.GetDynamicConfiguration().EnableReclaim

	calculationResult := types.InternalCPUCalculationResult{
		PoolEntries: make(map[string]map[int]int),
		TimeStamp:   time.Now(),
	}

	// fill in reserve pool entry
	reservePoolSize, _ := pa.metaReader.GetPoolSize(state.PoolNameReserve)
	calculationResult.SetPoolEntry(state.PoolNameReserve, cpuadvisor.FakedNUMAID, reservePoolSize)

	shares := 0
	isolationUppers := 0

	sharePoolSizes := make(map[string]int)
	isolationUpperSizes := make(map[string]int)
	isolationLowerSizes := make(map[string]int)

	for _, r := range *pa.regionMap {
		controlKnob, err := r.GetProvision()
		if err != nil {
			return types.InternalCPUCalculationResult{}, false, err
		}

		switch r.Type() {
		case types.QoSRegionTypeShare:
			// save raw share pool sizes
			sharePoolSizes[r.OwnerPoolName()] = int(controlKnob[types.ControlKnobNonReclaimedCPUSize].Value)

			shares += sharePoolSizes[r.OwnerPoolName()]

		case types.QoSRegionTypeIsolation:
			// save limits and requests for isolated region
			isolationUpperSizes[r.Name()] = int(controlKnob[types.ControlKnobNonReclaimedCPUSizeUpper].Value)
			isolationLowerSizes[r.Name()] = int(controlKnob[types.ControlKnobNonReclaimedCPUSizeLower].Value)

			isolationUppers += isolationUpperSizes[r.Name()]

		case types.QoSRegionTypeDedicatedNumaExclusive:
			regionNuma := r.GetBindingNumas().ToSliceInt()[0] // always one binding numa for this type of region
			reservedForReclaim := pa.getNumasReservedForReclaim(r.GetBindingNumas())

			// fill in reclaim pool entry for dedicated numa exclusive regions
			if !enableReclaim {
				if reservedForReclaim > 0 {
					calculationResult.SetPoolEntry(state.PoolNameReclaim, regionNuma, reservedForReclaim)
				}
			} else {
				available := getNumasAvailableResource(*pa.numaAvailable, r.GetBindingNumas())
				nonReclaimRequirement := int(controlKnob[types.ControlKnobNonReclaimedCPUSize].Value)
				reclaimed := available - nonReclaimRequirement + reservedForReclaim

				calculationResult.SetPoolEntry(state.PoolNameReclaim, regionNuma, reclaimed)
			}
		}
	}

	general.Infof("share size: %v", sharePoolSizes)
	general.Infof("isolate upper-size: %v", isolationUpperSizes)
	general.Infof("isolate lower-size: %v", isolationLowerSizes)

	shareAndIsolatedPoolAvailable := getNumasAvailableResource(*pa.numaAvailable, *pa.nonBindingNumas)
	shareAndIsolatePoolSizes := general.MergeMapInt(sharePoolSizes, isolationUpperSizes)
	if shares+isolationUppers > shareAndIsolatedPoolAvailable {
		shareAndIsolatePoolSizes = general.MergeMapInt(sharePoolSizes, isolationLowerSizes)
	}
	boundUpper := regulatePoolSizes(shareAndIsolatePoolSizes, shareAndIsolatedPoolAvailable, enableReclaim)

	// fill in regulated share-and-isolated pool entries
	for poolName, poolSize := range shareAndIsolatePoolSizes {
		calculationResult.SetPoolEntry(poolName, cpuadvisor.FakedNUMAID, poolSize)
	}

	reclaimPoolSizeOfNonBindingNumas := shareAndIsolatedPoolAvailable - general.SumUpMapValues(shareAndIsolatePoolSizes) + pa.getNumasReservedForReclaim(*pa.nonBindingNumas)
	calculationResult.SetPoolEntry(state.PoolNameReclaim, cpuadvisor.FakedNUMAID, reclaimPoolSizeOfNonBindingNumas)

	return calculationResult, boundUpper, nil
}

func (pa *ProvisionAssemblerCommon) getNumasReservedForReclaim(numas machine.CPUSet) int {
	res := 0
	for _, id := range numas.ToSliceInt() {
		if v, ok := (*pa.reservedForReclaim)[id]; ok {
			res += v
		}
	}
	return res
}

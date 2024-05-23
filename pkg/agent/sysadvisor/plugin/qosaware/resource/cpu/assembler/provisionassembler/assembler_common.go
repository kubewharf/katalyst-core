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
	"context"
	"fmt"
	"time"

	"k8s.io/klog/v2"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/cpuadvisor"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/qosaware/resource/cpu/region"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/qosaware/resource/helper"
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

	metaReader   metacache.MetaReader
	metaServer   *metaserver.MetaServer
	emitter      metrics.MetricEmitter
	regionHelper *RegionMapHelper
}

func NewProvisionAssemblerCommon(conf *config.Configuration, _ interface{}, regionMap *map[string]region.QoSRegion,
	reservedForReclaim *map[int]int, numaAvailable *map[int]int, nonBindingNumas *machine.CPUSet,
	metaReader metacache.MetaReader, metaServer *metaserver.MetaServer, emitter metrics.MetricEmitter,
) ProvisionAssembler {
	return &ProvisionAssemblerCommon{
		conf:               conf,
		regionMap:          regionMap,
		reservedForReclaim: reservedForReclaim,
		numaAvailable:      numaAvailable,
		nonBindingNumas:    nonBindingNumas,

		metaReader:   metaReader,
		metaServer:   metaServer,
		emitter:      emitter,
		regionHelper: NewRegionMap(*regionMap),
	}
}

func (pa *ProvisionAssemblerCommon) AssembleProvision() (types.InternalCPUCalculationResult, error) {
	nodeEnableReclaim := pa.conf.GetDynamicConfiguration().EnableReclaim

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
			return types.InternalCPUCalculationResult{}, err
		}

		switch r.Type() {
		case types.QoSRegionTypeShare:
			if r.IsNumaBinding() {
				regionNuma := r.GetBindingNumas().ToSliceInt()[0] // always one binding numa for this type of region
				reservedForReclaim := pa.getNumasReservedForReclaim(r.GetBindingNumas())

				nonReclaimRequirement := int(controlKnob[types.ControlKnobNonReclaimedCPUSize].Value)
				// available = NUMA Size - Reserved - ReservedForReclaimed
				available := getNumasAvailableResource(*pa.numaAvailable, r.GetBindingNumas())

				// calc isolation pool size
				isolationPoolSizeSum := 0
				isolationRegions := pa.regionHelper.GetRegions(regionNuma, types.QoSRegionTypeIsolation)

				isolationRegionControlKnobs := map[string]types.ControlKnob{}
				isolationRegionControlKnobKey := types.ControlKnobNonReclaimedCPUSizeUpper
				if len(isolationRegions) > 0 {
					isolationUpperSum := 0
					for _, isolationRegion := range isolationRegions {
						isolationControlKnob, err := isolationRegion.GetProvision()
						if err != nil {
							return types.InternalCPUCalculationResult{}, err
						}
						isolationRegionControlKnobs[isolationRegion.Name()] = isolationControlKnob
						isolationUpperSum += int(isolationControlKnob[types.ControlKnobNonReclaimedCPUSizeUpper].Value)
					}

					if nonReclaimRequirement+isolationUpperSum > available {
						isolationRegionControlKnobKey = types.ControlKnobNonReclaimedCPUSizeLower
					}
				}

				numaPoolSize := map[string]int{r.OwnerPoolName(): nonReclaimRequirement}
				for isolationRegionName, isolationRegionControlKnob := range isolationRegionControlKnobs {
					numaPoolSize[isolationRegionName] = int(isolationRegionControlKnob[isolationRegionControlKnobKey].Value)
				}
				poolThrottled := regulatePoolSizes(numaPoolSize, available, nodeEnableReclaim)
				r.SetThrottled(poolThrottled)

				nonReclaimRequirement = numaPoolSize[r.OwnerPoolName()]
				for isolationRegionName := range isolationRegionControlKnobs {
					isolationPoolSize := numaPoolSize[isolationRegionName]
					calculationResult.SetPoolEntry(isolationRegionName, regionNuma, isolationPoolSize)
					isolationPoolSizeSum += isolationPoolSize
				}

				// calc share and reclaimed pool size
				sharePoolSize := 0
				reclaimed := 0
				if nodeEnableReclaim {
					reclaimed = available - nonReclaimRequirement - isolationPoolSizeSum + reservedForReclaim
					sharePoolSize = nonReclaimRequirement
				} else {
					reclaimed = reservedForReclaim
					sharePoolSize = available - isolationPoolSizeSum
				}

				calculationResult.SetPoolEntry(state.PoolNameReclaim, regionNuma, reclaimed)
				calculationResult.SetPoolEntry(r.OwnerPoolName(), regionNuma, sharePoolSize)
			} else {
				// save raw share pool sizes
				sharePoolSizes[r.OwnerPoolName()] = int(controlKnob[types.ControlKnobNonReclaimedCPUSize].Value)
				shares += sharePoolSizes[r.OwnerPoolName()]
			}
		case types.QoSRegionTypeIsolation:
			if r.IsNumaBinding() {
				regionNuma := r.GetBindingNumas().ToSliceInt()[0] // always one binding numa for this type of region
				// If there is a SNB pool with the same NUMA ID, it will be calculated while processing the SNB pool.
				if shareRegions := pa.regionHelper.GetRegions(regionNuma, types.QoSRegionTypeShare); len(shareRegions) == 0 {
					calculationResult.SetPoolEntry(r.Name(), regionNuma, int(controlKnob[types.ControlKnobNonReclaimedCPUSizeUpper].Value))
				}
			} else {
				// save limits and requests for isolated region
				isolationUpperSizes[r.Name()] = int(controlKnob[types.ControlKnobNonReclaimedCPUSizeUpper].Value)
				isolationLowerSizes[r.Name()] = int(controlKnob[types.ControlKnobNonReclaimedCPUSizeLower].Value)

				isolationUppers += isolationUpperSizes[r.Name()]
			}
		case types.QoSRegionTypeDedicatedNumaExclusive:
			regionNuma := r.GetBindingNumas().ToSliceInt()[0] // always one binding numa for this type of region
			reservedForReclaim := pa.getNumasReservedForReclaim(r.GetBindingNumas())

			podSet := r.GetPods()
			if podSet.Pods() != 1 {
				return types.InternalCPUCalculationResult{}, fmt.Errorf("more than one pod are assgined to numa exclusive region: %v", podSet)
			}
			podUID, _, _ := podSet.PopAny()

			enableReclaim, err := helper.PodEnableReclaim(context.Background(), pa.metaServer, podUID, nodeEnableReclaim)
			if err != nil {
				return types.InternalCPUCalculationResult{}, err
			}

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

				klog.InfoS("assemble info", "regionName", r.Name(), "reclaimed", reclaimed,
					"available", available, "nonReclaimRequirement", nonReclaimRequirement, "reservedForReclaim", reservedForReclaim)
			}
		}
	}

	shareAndIsolatedPoolAvailable := getNumasAvailableResource(*pa.numaAvailable, *pa.nonBindingNumas)
	shareAndIsolatePoolSizes := general.MergeMapInt(sharePoolSizes, isolationUpperSizes)
	if shares+isolationUppers > shareAndIsolatedPoolAvailable {
		shareAndIsolatePoolSizes = general.MergeMapInt(sharePoolSizes, isolationLowerSizes)
	}
	poolThrottled := regulatePoolSizes(shareAndIsolatePoolSizes, shareAndIsolatedPoolAvailable, nodeEnableReclaim)
	for _, r := range *pa.regionMap {
		if r.Type() == types.QoSRegionTypeShare && !r.IsNumaBinding() {
			r.SetThrottled(poolThrottled)
		}
	}

	klog.InfoS("pool sizes", "share size", sharePoolSizes,
		"isolate upper-size", isolationUpperSizes, "isolate lower-size", isolationLowerSizes,
		"shareAndIsolatePoolSizes", shareAndIsolatePoolSizes,
		"shareAndIsolatedPoolAvailable", shareAndIsolatedPoolAvailable)

	// fill in regulated share-and-isolated pool entries
	for poolName, poolSize := range shareAndIsolatePoolSizes {
		calculationResult.SetPoolEntry(poolName, cpuadvisor.FakedNUMAID, poolSize)
	}

	var reclaimPoolSizeOfNonBindingNumas int

	// fill in reclaim pool entries of non binding numas
	if nodeEnableReclaim {
		// generate based on share pool requirement on non binding numas
		reclaimPoolSizeOfNonBindingNumas = shareAndIsolatedPoolAvailable - general.SumUpMapValues(shareAndIsolatePoolSizes) + pa.getNumasReservedForReclaim(*pa.nonBindingNumas)
	} else {
		// generate by reserved value on non binding numas
		reclaimPoolSizeOfNonBindingNumas = pa.getNumasReservedForReclaim(*pa.nonBindingNumas)
	}
	calculationResult.SetPoolEntry(state.PoolNameReclaim, cpuadvisor.FakedNUMAID, reclaimPoolSizeOfNonBindingNumas)

	return calculationResult, nil
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

type RegionMapHelper struct {
	regions map[int]map[types.QoSRegionType][]region.QoSRegion
}

func NewRegionMap(regions map[string]region.QoSRegion) *RegionMapHelper {
	helper := &RegionMapHelper{
		regions: map[int]map[types.QoSRegionType][]region.QoSRegion{},
	}

	helper.preProcessRegions(regions)

	return helper
}

func (rm *RegionMapHelper) GetRegions(numaID int, regionType types.QoSRegionType) []region.QoSRegion {
	numaRecords, ok := rm.regions[numaID]
	if !ok {
		return nil
	}

	return numaRecords[regionType]
}

func (rm *RegionMapHelper) preProcessRegions(regions map[string]region.QoSRegion) {
	for _, r := range regions {
		for _, numaID := range r.GetBindingNumas().ToSliceInt() {
			numaRecords, ok := rm.regions[numaID]
			if !ok {
				numaRecords = map[types.QoSRegionType][]region.QoSRegion{}
			}
			numaRegions := numaRecords[r.Type()]
			numaRegions = append(numaRegions, r)
			numaRecords[r.Type()] = numaRegions
			rm.regions[numaID] = numaRecords
		}
	}
}

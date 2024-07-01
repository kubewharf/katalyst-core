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
	"math"
	"time"

	"k8s.io/klog/v2"

	configapi "github.com/kubewharf/katalyst-api/pkg/apis/config/v1alpha1"
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
	conf                                  *config.Configuration
	regionMap                             *map[string]region.QoSRegion
	reservedForReclaim                    *map[int]int
	numaAvailable                         *map[int]int
	nonBindingNumas                       *machine.CPUSet
	allowSharedCoresOverlapReclaimedCores *bool

	metaReader   metacache.MetaReader
	metaServer   *metaserver.MetaServer
	emitter      metrics.MetricEmitter
	regionHelper *RegionMapHelper
}

func NewProvisionAssemblerCommon(conf *config.Configuration, _ interface{}, regionMap *map[string]region.QoSRegion,
	reservedForReclaim *map[int]int, numaAvailable *map[int]int, nonBindingNumas *machine.CPUSet, allowSharedCoresOverlapReclaimedCores *bool,
	metaReader metacache.MetaReader, metaServer *metaserver.MetaServer, emitter metrics.MetricEmitter,
) ProvisionAssembler {
	return &ProvisionAssemblerCommon{
		conf:                                  conf,
		regionMap:                             regionMap,
		reservedForReclaim:                    reservedForReclaim,
		numaAvailable:                         numaAvailable,
		nonBindingNumas:                       nonBindingNumas,
		allowSharedCoresOverlapReclaimedCores: allowSharedCoresOverlapReclaimedCores,

		metaReader:   metaReader,
		metaServer:   metaServer,
		emitter:      emitter,
		regionHelper: NewRegionMap(*regionMap),
	}
}

func (pa *ProvisionAssemblerCommon) AssembleProvision() (types.InternalCPUCalculationResult, error) {
	nodeEnableReclaim := pa.conf.GetDynamicConfiguration().EnableReclaim

	calculationResult := types.InternalCPUCalculationResult{
		PoolEntries:                           make(map[string]map[int]int),
		PoolOverlapInfo:                       map[string]map[int]map[string]int{},
		TimeStamp:                             time.Now(),
		AllowSharedCoresOverlapReclaimedCores: *pa.allowSharedCoresOverlapReclaimedCores,
	}

	// fill in reserve pool entry
	reservePoolSize, _ := pa.metaReader.GetPoolSize(state.PoolNameReserve)
	calculationResult.SetPoolEntry(state.PoolNameReserve, state.FakedNUMAID, reservePoolSize)

	shares := 0
	isolationUppers := 0

	sharePoolRequirements := make(map[string]int)
	isolationUpperSizes := make(map[string]int)
	isolationLowerSizes := make(map[string]int)

	for _, r := range *pa.regionMap {
		controlKnob, err := r.GetProvision()
		if err != nil {
			return types.InternalCPUCalculationResult{}, err
		}

		switch r.Type() {
		case configapi.QoSRegionTypeShare:
			if r.IsNumaBinding() {
				regionNuma := r.GetBindingNumas().ToSliceInt()[0] // always one binding numa for this type of region
				reservedForReclaim := pa.getNumasReservedForReclaim(r.GetBindingNumas())

				nonReclaimRequirement := int(controlKnob[configapi.ControlKnobNonReclaimedCPURequirement].Value)
				// available = NUMA Size - Reserved - ReservedForReclaimed when allowSharedCoresOverlapReclaimedCores == false
				// available = NUMA Size - Reserved when allowSharedCoresOverlapReclaimedCores == true
				available := getNUMAsResource(*pa.numaAvailable, r.GetBindingNumas())
				if *pa.allowSharedCoresOverlapReclaimedCores {
					available += getNUMAsResource(*pa.reservedForReclaim, r.GetBindingNumas())
				}

				// calc isolation pool size
				isolationRegions := pa.regionHelper.GetRegions(regionNuma, configapi.QoSRegionTypeIsolation)

				isolationRegionControlKnobs := map[string]types.ControlKnob{}
				isolationRegionControlKnobKey := configapi.ControlKnobNonReclaimedCPURequirementUpper
				if len(isolationRegions) > 0 {
					isolationUpperSum := 0
					for _, isolationRegion := range isolationRegions {
						isolationControlKnob, err := isolationRegion.GetProvision()
						if err != nil {
							return types.InternalCPUCalculationResult{}, err
						}
						isolationRegionControlKnobs[isolationRegion.Name()] = isolationControlKnob
						isolationUpperSum += int(isolationControlKnob[configapi.ControlKnobNonReclaimedCPURequirementUpper].Value)
					}

					if nonReclaimRequirement+isolationUpperSum > available {
						isolationRegionControlKnobKey = configapi.ControlKnobNonReclaimedCPURequirementLower
					}
				}

				poolRequirements := map[string]int{r.OwnerPoolName(): nonReclaimRequirement}
				isolationRequirements := map[string]int{}
				for isolationRegionName, isolationRegionControlKnob := range isolationRegionControlKnobs {
					isolationRequirements[isolationRegionName] = int(isolationRegionControlKnob[isolationRegionControlKnobKey].Value)
				}
				poolSizes, poolThrottled := regulatePoolSizes(poolRequirements, isolationRequirements, available, nodeEnableReclaim, *pa.allowSharedCoresOverlapReclaimedCores)
				r.SetThrottled(poolThrottled)

				for poolName, size := range poolSizes {
					calculationResult.SetPoolEntry(poolName, regionNuma, size)
				}

				reclaimedCoresSize := available - general.SumUpMapValues(poolSizes) + reservedForReclaim
				if *pa.allowSharedCoresOverlapReclaimedCores {
					reclaimedCoresAvail := poolSizes[r.OwnerPoolName()] - poolRequirements[r.OwnerPoolName()]
					if !nodeEnableReclaim {
						reclaimedCoresAvail = 0
					}
					reclaimedCoresSize = general.Max(pa.getNumasReservedForReclaim(r.GetBindingNumas()), reclaimedCoresAvail)
					reclaimedCoresSize = general.Min(reclaimedCoresSize, poolSizes[r.OwnerPoolName()])
					calculationResult.SetPoolOverlapInfo(state.PoolNameReclaim, regionNuma, r.OwnerPoolName(), reclaimedCoresSize)
				}

				calculationResult.SetPoolEntry(state.PoolNameReclaim, regionNuma, reclaimedCoresSize)
			} else {
				// save raw share pool sizes
				sharePoolRequirements[r.OwnerPoolName()] = int(controlKnob[configapi.ControlKnobNonReclaimedCPURequirement].Value)
				shares += sharePoolRequirements[r.OwnerPoolName()]
			}
		case configapi.QoSRegionTypeIsolation:
			if r.IsNumaBinding() {
				regionNuma := r.GetBindingNumas().ToSliceInt()[0] // always one binding numa for this type of region
				// If there is a SNB pool with the same NUMA ID, it will be calculated while processing the SNB pool.
				if shareRegions := pa.regionHelper.GetRegions(regionNuma, configapi.QoSRegionTypeShare); len(shareRegions) == 0 {
					calculationResult.SetPoolEntry(r.Name(), regionNuma, int(controlKnob[configapi.ControlKnobNonReclaimedCPURequirementUpper].Value))
				}
			} else {
				// save limits and requests for isolated region
				isolationUpperSizes[r.Name()] = int(controlKnob[configapi.ControlKnobNonReclaimedCPURequirementUpper].Value)
				isolationLowerSizes[r.Name()] = int(controlKnob[configapi.ControlKnobNonReclaimedCPURequirementLower].Value)

				isolationUppers += isolationUpperSizes[r.Name()]
			}
		case configapi.QoSRegionTypeDedicatedNumaExclusive:
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
				available := getNUMAsResource(*pa.numaAvailable, r.GetBindingNumas())
				nonReclaimRequirement := int(controlKnob[configapi.ControlKnobNonReclaimedCPURequirement].Value)
				reclaimed := available - nonReclaimRequirement + reservedForReclaim

				calculationResult.SetPoolEntry(state.PoolNameReclaim, regionNuma, reclaimed)

				klog.InfoS("assemble info", "regionName", r.Name(), "reclaimed", reclaimed,
					"available", available, "nonReclaimRequirement", nonReclaimRequirement,
					"reservedForReclaim", reservedForReclaim)
			}
		}
	}

	shareAndIsolatedPoolAvailable := getNUMAsResource(*pa.numaAvailable, *pa.nonBindingNumas)
	if *pa.allowSharedCoresOverlapReclaimedCores {
		shareAndIsolatedPoolAvailable += getNUMAsResource(*pa.reservedForReclaim, *pa.nonBindingNumas)
	}

	isolationPoolSizes := isolationUpperSizes
	if shares+isolationUppers > shareAndIsolatedPoolAvailable {
		isolationPoolSizes = isolationLowerSizes
	}

	shareAndIsolatePoolSizes, poolThrottled := regulatePoolSizes(sharePoolRequirements, isolationPoolSizes, shareAndIsolatedPoolAvailable, nodeEnableReclaim, *pa.allowSharedCoresOverlapReclaimedCores)
	for _, r := range *pa.regionMap {
		if r.Type() == configapi.QoSRegionTypeShare && !r.IsNumaBinding() {
			r.SetThrottled(poolThrottled)
		}
	}

	klog.InfoS("pool sizes", "share pool requirement", sharePoolRequirements,
		"isolate upper-size", isolationUpperSizes, "isolate lower-size", isolationLowerSizes,
		"shareAndIsolatePoolSizes", shareAndIsolatePoolSizes,
		"shareAndIsolatedPoolAvailable", shareAndIsolatedPoolAvailable)

	// fill in regulated share-and-isolated pool entries
	for poolName, poolSize := range shareAndIsolatePoolSizes {
		calculationResult.SetPoolEntry(poolName, state.FakedNUMAID, poolSize)
	}

	reclaimPoolSizeOfNonBindingNUMAs := shareAndIsolatedPoolAvailable - general.SumUpMapValues(shareAndIsolatePoolSizes) + pa.getNumasReservedForReclaim(*pa.nonBindingNumas)
	if *pa.allowSharedCoresOverlapReclaimedCores {
		isolated := shareAndIsolatedPoolAvailable
		sharePoolSizes := make(map[string]int)
		for poolName := range sharePoolRequirements {
			sharePoolSizes[poolName] = shareAndIsolatePoolSizes[poolName]
			isolated -= shareAndIsolatePoolSizes[poolName]
		}
		reclaimPoolSizeOfNonBindingNUMAs = general.Max(pa.getNumasReservedForReclaim(*pa.nonBindingNumas), shareAndIsolatedPoolAvailable-isolated-general.SumUpMapValues(sharePoolRequirements))
		if !nodeEnableReclaim {
			reclaimPoolSizeOfNonBindingNUMAs = pa.getNumasReservedForReclaim(*pa.nonBindingNumas)
		}

		if len(sharePoolSizes) > 0 {
			reclaimPoolSizeOfNonBindingNUMAs = general.Min(reclaimPoolSizeOfNonBindingNUMAs, general.SumUpMapValues(sharePoolSizes))
			sharedOverlapReclaimSize := make(map[string]int) // sharedPoolName -> reclaimedSize
			sharePoolSum := general.SumUpMapValues(sharePoolSizes)
			ps := general.SortedByValue(sharePoolSizes)
			for _, p := range ps {
				sharePoolSize := p.Value
				sharePoolName := p.Key
				size := int(math.Floor(float64(reclaimPoolSizeOfNonBindingNUMAs*sharePoolSize) / float64(sharePoolSum)))
				if size > 0 {
					sharedOverlapReclaimSize[sharePoolName] = size
				}
			}

			diff := reclaimPoolSizeOfNonBindingNUMAs - general.SumUpMapValues(sharedOverlapReclaimSize)
			if diff > 0 {
				sharedOverlapReclaimSize[ps[0].Key] += diff
			} else if diff < 0 {
				return types.InternalCPUCalculationResult{}, fmt.Errorf("failed to calculate sharedOverlapReclaimSize")
			}

			for overlapPoolName, size := range sharedOverlapReclaimSize {
				calculationResult.SetPoolOverlapInfo(state.PoolNameReclaim, state.FakedNUMAID, overlapPoolName, size)
			}
		}
	}

	calculationResult.SetPoolEntry(state.PoolNameReclaim, state.FakedNUMAID, reclaimPoolSizeOfNonBindingNUMAs)

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
	regions map[int]map[configapi.QoSRegionType][]region.QoSRegion
}

func NewRegionMap(regions map[string]region.QoSRegion) *RegionMapHelper {
	helper := &RegionMapHelper{
		regions: map[int]map[configapi.QoSRegionType][]region.QoSRegion{},
	}

	helper.preProcessRegions(regions)

	return helper
}

func (rm *RegionMapHelper) GetRegions(numaID int, regionType configapi.QoSRegionType) []region.QoSRegion {
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
				numaRecords = map[configapi.QoSRegionType][]region.QoSRegion{}
			}
			numaRegions := numaRecords[r.Type()]
			numaRegions = append(numaRegions, r)
			numaRecords[r.Type()] = numaRegions
			rm.regions[numaID] = numaRecords
		}
	}
}

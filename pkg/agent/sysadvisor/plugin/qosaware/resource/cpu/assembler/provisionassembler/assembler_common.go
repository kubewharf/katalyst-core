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
	"fmt"
	"math"
	"time"

	"k8s.io/klog/v2"

	configapi "github.com/kubewharf/katalyst-api/pkg/apis/config/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
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
	conf                                  *config.Configuration
	regionMap                             *map[string]region.QoSRegion
	reservedForReclaim                    *map[int]int
	numaAvailable                         *map[int]int
	nonBindingNumas                       *machine.CPUSet
	allowSharedCoresOverlapReclaimedCores *bool

	metaReader metacache.MetaReader
	metaServer *metaserver.MetaServer
	emitter    metrics.MetricEmitter
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

		metaReader: metaReader,
		metaServer: metaServer,
		emitter:    emitter,
	}
}

func (pa *ProvisionAssemblerCommon) getIsolationRequirements(r region.QoSRegion) (map[string]int, error) {
	reservedForReclaim := getNUMAsResource(*pa.reservedForReclaim, r.GetBindingNumas())

	available := getNUMAsResource(*pa.numaAvailable, r.GetBindingNumas())
	if *pa.allowSharedCoresOverlapReclaimedCores {
		available += reservedForReclaim
	}

	numaID := r.GetBindingNumas().ToSliceInt()[0] // always one binding numa for this type of region

	regionHelper := NewRegionMapHelper(*pa.regionMap)
	podsRequests := general.Max(1, int(math.Ceil(r.GetPodsRequest())))

	// calc isolation pool size
	isolationRegions := regionHelper.GetRegions(numaID, configapi.QoSRegionTypeIsolation)

	isolationRegionControlKnobs := map[string]types.ControlKnob{}
	isolationRegionControlKnobKey := configapi.ControlKnobNonReclaimedCPURequirementUpper
	if len(isolationRegions) > 0 {
		isolationUpperSum := 0
		for _, isolationRegion := range isolationRegions {
			isolationControlKnob, err := isolationRegion.GetProvision()
			if err != nil {
				return nil, err
			}
			isolationRegionControlKnobs[isolationRegion.Name()] = isolationControlKnob
			isolationUpperSum += int(isolationControlKnob[configapi.ControlKnobNonReclaimedCPURequirementUpper].Value)
		}

		if podsRequests+isolationUpperSum > available {
			isolationRegionControlKnobKey = configapi.ControlKnobNonReclaimedCPURequirementLower
		}
	}

	isolationRequirements := map[string]int{}
	for isolationRegionName, isolationRegionControlKnob := range isolationRegionControlKnobs {
		isolationRequirements[isolationRegionName] = int(isolationRegionControlKnob[isolationRegionControlKnobKey].Value)
	}
	return isolationRequirements, nil
}

func (pa *ProvisionAssemblerCommon) AssembleProvision() (types.InternalCPUCalculationResult, error) {
	nodeEnableReclaim := pa.conf.GetDynamicConfiguration().EnableReclaim

	calculationResult := types.InternalCPUCalculationResult{
		PoolEntries:                           make(map[string]map[int]types.CPUResource),
		PoolOverlapInfo:                       map[string]map[int]map[string]int{},
		TimeStamp:                             time.Now(),
		AllowSharedCoresOverlapReclaimedCores: *pa.allowSharedCoresOverlapReclaimedCores,
	}

	// fill in reserve pool entry
	reservePoolSize, _ := pa.metaReader.GetPoolSize(commonstate.PoolNameReserve)
	calculationResult.SetPoolEntry(commonstate.PoolNameReserve, commonstate.FakedNUMAID, reservePoolSize, -1)

	isolationUppers := 0

	sharePoolRequirements := make(map[string]int)
	sharePoolRequests := make(map[string]int)
	isolationUpperSizes := make(map[string]int)
	isolationLowerSizes := make(map[string]int)

	regionHelper := NewRegionMapHelper(*pa.regionMap)

	for _, r := range *pa.regionMap {
		controlKnob, err := r.GetProvision()
		if err != nil {
			return types.InternalCPUCalculationResult{}, err
		}

		switch r.Type() {
		case configapi.QoSRegionTypeShare:
			if r.IsNumaBinding() {
				regionNuma := r.GetBindingNumas().ToSliceInt()[0] // always one binding numa for this type of region
				reservedForReclaim := getNUMAsResource(*pa.reservedForReclaim, r.GetBindingNumas())
				podsRequests := general.Max(1, int(math.Ceil(r.GetPodsRequest())))
				// available = NUMA Size - Reserved - ReservedForReclaimed when allowSharedCoresOverlapReclaimedCores == false
				// available = NUMA Size - Reserved when allowSharedCoresOverlapReclaimedCores == true
				available := getNUMAsResource(*pa.numaAvailable, r.GetBindingNumas())
				if *pa.allowSharedCoresOverlapReclaimedCores {
					available += reservedForReclaim
				}

				isolationRequirements, err := pa.getIsolationRequirements(r)
				if err != nil {
					return types.InternalCPUCalculationResult{}, err
				}

				shareRequirements := map[string]int{r.OwnerPoolName(): podsRequests}

				poolSizes, poolThrottled := regulatePoolSizes(shareRequirements, isolationRequirements, available, !nodeEnableReclaim || *pa.allowSharedCoresOverlapReclaimedCores)
				r.SetThrottled(poolThrottled)

				for poolName, size := range poolSizes {
					calculationResult.SetPoolEntry(poolName, regionNuma, size, -1)
				}

				var reclaimedCoresSize int
				if *pa.allowSharedCoresOverlapReclaimedCores {
					reclaimedCoresAvail := poolSizes[r.OwnerPoolName()] - int(controlKnob[configapi.ControlKnobNonReclaimedCPURequirement].Value)

					if !nodeEnableReclaim {
						reclaimedCoresAvail = 0
					}
					reclaimedCoresSize = general.Max(reservedForReclaim, reclaimedCoresAvail)
					reclaimedCoresSize = general.Min(reclaimedCoresSize, poolSizes[r.OwnerPoolName()])
					calculationResult.SetPoolOverlapInfo(commonstate.PoolNameReclaim, regionNuma, r.OwnerPoolName(), reclaimedCoresSize)
				} else {
					reclaimedCoresSize = available - general.SumUpMapValues(poolSizes) + reservedForReclaim
					if !nodeEnableReclaim {
						reclaimedCoresSize = reservedForReclaim
					}
				}

				calculationResult.SetPoolEntry(commonstate.PoolNameReclaim, regionNuma, reclaimedCoresSize, -1)
			} else {
				// save raw share pool sizes
				sharePoolRequirements[r.OwnerPoolName()] = general.Max(1, int(controlKnob[configapi.ControlKnobNonReclaimedCPURequirement].Value))
				sharePoolRequests[r.OwnerPoolName()] = general.Max(1, int(math.Ceil(r.GetPodsRequest())))
			}
		case configapi.QoSRegionTypeIsolation:
			if r.IsNumaBinding() {
				regionNuma := r.GetBindingNumas().ToSliceInt()[0] // always one binding numa for this type of region
				// If there is a SNB pool with the same NUMA ID, it will be calculated while processing the SNB pool.
				if shareRegions := regionHelper.GetRegions(regionNuma, configapi.QoSRegionTypeShare); len(shareRegions) == 0 {
					calculationResult.SetPoolEntry(r.Name(), regionNuma, int(controlKnob[configapi.ControlKnobNonReclaimedCPURequirementUpper].Value), -1)

					_, ok := calculationResult.GetPoolEntry(commonstate.PoolNameReclaim, regionNuma)
					if !ok {
						available := getNUMAsResource(*pa.numaAvailable, r.GetBindingNumas())
						reservedForReclaim := getNUMAsResource(*pa.reservedForReclaim, r.GetBindingNumas())

						isolationRegions := regionHelper.GetRegions(regionNuma, configapi.QoSRegionTypeIsolation)
						isolationSizes := 0
						for _, ir := range isolationRegions {
							ck, err := ir.GetProvision()
							if err != nil {
								return types.InternalCPUCalculationResult{}, err
							}
							isolationSizes += int(ck[configapi.ControlKnobNonReclaimedCPURequirementUpper].Value)
						}
						reclaimedCoresSize := general.Max(available-isolationSizes, 0) + reservedForReclaim
						calculationResult.SetPoolEntry(commonstate.PoolNameReclaim, regionNuma, reclaimedCoresSize, -1)
					}
				}
			} else {
				// save limits and requests for isolated region
				isolationUpperSizes[r.Name()] = int(controlKnob[configapi.ControlKnobNonReclaimedCPURequirementUpper].Value)
				isolationLowerSizes[r.Name()] = int(controlKnob[configapi.ControlKnobNonReclaimedCPURequirementLower].Value)

				isolationUppers += isolationUpperSizes[r.Name()]
			}
		case configapi.QoSRegionTypeDedicatedNumaExclusive:
			regionNuma := r.GetBindingNumas().ToSliceInt()[0] // always one binding numa for this type of region
			reservedForReclaim := getNUMAsResource(*pa.reservedForReclaim, r.GetBindingNumas())

			// fill in reclaim pool entry for dedicated numa exclusive regions
			if !r.EnableReclaim() {
				if reservedForReclaim > 0 {
					calculationResult.SetPoolEntry(commonstate.PoolNameReclaim, regionNuma, reservedForReclaim, -1)
				}
			} else {
				available := getNUMAsResource(*pa.numaAvailable, r.GetBindingNumas())
				nonReclaimRequirement := int(controlKnob[configapi.ControlKnobNonReclaimedCPURequirement].Value)
				reclaimed := available - nonReclaimRequirement + reservedForReclaim

				calculationResult.SetPoolEntry(commonstate.PoolNameReclaim, regionNuma, reclaimed, -1)

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

	allowExpand := !nodeEnableReclaim || *pa.allowSharedCoresOverlapReclaimedCores
	var requirements map[string]int
	if allowExpand {
		requirements = sharePoolRequests
	} else {
		requirements = sharePoolRequirements
	}

	if general.SumUpMapValues(requirements)+isolationUppers > shareAndIsolatedPoolAvailable {
		isolationPoolSizes = isolationLowerSizes
	}
	shareAndIsolatePoolSizes, poolThrottled := regulatePoolSizes(requirements, isolationPoolSizes, shareAndIsolatedPoolAvailable, allowExpand)

	for _, r := range *pa.regionMap {
		if r.Type() == configapi.QoSRegionTypeShare && !r.IsNumaBinding() {
			r.SetThrottled(poolThrottled)
		}
	}

	klog.InfoS("pool info", "share pool requirement", sharePoolRequirements,
		"share pool requests", sharePoolRequests,
		"isolate upper-size", isolationUpperSizes, "isolate lower-size", isolationLowerSizes,
		"shareAndIsolatePoolSizes", shareAndIsolatePoolSizes,
		"shareAndIsolatedPoolAvailable", shareAndIsolatedPoolAvailable)

	// fill in regulated share-and-isolated pool entries
	for poolName, poolSize := range shareAndIsolatePoolSizes {
		calculationResult.SetPoolEntry(poolName, commonstate.FakedNUMAID, poolSize, -1)
	}

	var reclaimPoolSizeOfNonBindingNUMAs int
	nonBindingNUMAsReservedForReclaim := getNUMAsResource(*pa.reservedForReclaim, *pa.nonBindingNumas)
	if *pa.allowSharedCoresOverlapReclaimedCores {
		isolated := 0
		sharePoolSizes := make(map[string]int)
		for poolName, size := range shareAndIsolatePoolSizes {
			_, ok := sharePoolRequirements[poolName]
			if ok {
				sharePoolSizes[poolName] = shareAndIsolatePoolSizes[poolName]
			} else {
				isolated += size
			}
		}

		reclaimPoolSizeOfNonBindingNUMAs = general.Max(nonBindingNUMAsReservedForReclaim, shareAndIsolatedPoolAvailable-isolated-general.SumUpMapValues(sharePoolRequirements))
		if !nodeEnableReclaim {
			reclaimPoolSizeOfNonBindingNUMAs = nonBindingNUMAsReservedForReclaim
		}

		if len(sharePoolSizes) > 0 {
			sharedOverlapReclaimSize := make(map[string]int)
			if !nodeEnableReclaim {
				reclaimPoolSizeOfNonBindingNUMAs = general.Min(reclaimPoolSizeOfNonBindingNUMAs, general.SumUpMapValues(sharePoolSizes))
				reclaimSizes, err := regulateOverlapReclaimPoolSize(sharePoolSizes, reclaimPoolSizeOfNonBindingNUMAs)
				if err != nil {
					return types.InternalCPUCalculationResult{}, fmt.Errorf("failed to calculate sharedOverlapReclaimSize")
				}
				sharedOverlapReclaimSize = reclaimSizes
			} else {
				for poolName, size := range sharePoolSizes {
					reclaimSize := size - sharePoolRequirements[poolName]
					if reclaimSize > 0 {
						sharedOverlapReclaimSize[poolName] = reclaimSize
					} else {
						sharedOverlapReclaimSize[poolName] = 1
					}
				}

				reclaimPoolSizeOfNonBindingNUMAs = general.SumUpMapValues(sharedOverlapReclaimSize)
				if reclaimPoolSizeOfNonBindingNUMAs < nonBindingNUMAsReservedForReclaim {
					reclaimPoolSizeOfNonBindingNUMAs = nonBindingNUMAsReservedForReclaim
					regulatedOverlapReclaimPoolSize, err := regulateOverlapReclaimPoolSize(sharePoolSizes, reclaimPoolSizeOfNonBindingNUMAs)
					if err != nil {
						return types.InternalCPUCalculationResult{}, fmt.Errorf("failed to regulateOverlapReclaimPoolSize for non-binding NUMAs reserved for reclaim")
					}
					sharedOverlapReclaimSize = regulatedOverlapReclaimPoolSize
				}
			}

			for overlapPoolName, size := range sharedOverlapReclaimSize {
				calculationResult.SetPoolOverlapInfo(commonstate.PoolNameReclaim, commonstate.FakedNUMAID, overlapPoolName, size)
			}
		}
	} else {
		reclaimPoolSizeOfNonBindingNUMAs = shareAndIsolatedPoolAvailable - general.SumUpMapValues(shareAndIsolatePoolSizes) + nonBindingNUMAsReservedForReclaim
		if !nodeEnableReclaim {
			reclaimPoolSizeOfNonBindingNUMAs = nonBindingNUMAsReservedForReclaim
		}
	}

	calculationResult.SetPoolEntry(commonstate.PoolNameReclaim, commonstate.FakedNUMAID, reclaimPoolSizeOfNonBindingNUMAs, -1)

	return calculationResult, nil
}

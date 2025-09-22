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

func (pa *ProvisionAssemblerCommon) assembleDedicatedNUMAExclusiveRegion(r region.QoSRegion, result *types.InternalCPUCalculationResult) error {
	if r.Type() != configapi.QoSRegionTypeDedicatedNumaExclusive {
		return fmt.Errorf("region %v is not a DedicatedNUMAExclusive region", r.Name())
	}

	controlKnob, err := r.GetProvision()
	if err != nil {
		return err
	}

	regionNuma := r.GetBindingNumas().ToSliceInt()[0] // always one binding numa for this type of region
	reservedForReclaim := getNUMAsResource(*pa.reservedForReclaim, r.GetBindingNumas())
	available := getNUMAsResource(*pa.numaAvailable, r.GetBindingNumas())
	var reclaimedCoresSize int
	reclaimedCoresLimit := float64(-1)

	// fill in reclaim pool entry for dedicated numa exclusive regions
	nonReclaimRequirement := int(controlKnob[configapi.ControlKnobNonReclaimedCPURequirement].Value)
	if !r.EnableReclaim() {
		nonReclaimRequirement = available
	}

	quotaCtrlKnobEnabled, err := metacache.IsQuotaCtrlKnobEnabled(pa.metaReader)
	if err != nil {
		return err
	}

	if quotaCtrlKnobEnabled {
		reclaimedCoresSize = available
		reclaimedCoresLimit = general.MaxFloat64(float64(reservedForReclaim), float64(available-nonReclaimRequirement))

		if quota, ok := controlKnob[configapi.ControlKnobReclaimedCoresCPUQuota]; ok {
			reclaimedCoresLimit = general.MinFloat64(reclaimedCoresLimit, quota.Value)
		}
	} else {
		reclaimedCoresSize = general.Max(reservedForReclaim, available-nonReclaimRequirement)
	}

	klog.InfoS("assembleDedicatedNUMAExclusive info", "regionName", r.Name(), "reclaimedCoresSize", reclaimedCoresSize,
		"reclaimedCoresLimit", reclaimedCoresLimit,
		"available", available, "nonReclaimRequirement", nonReclaimRequirement,
		"reservedForReclaim", reservedForReclaim, "controlKnob", controlKnob)

	result.SetPoolEntry(commonstate.PoolNameReclaim, regionNuma, reclaimedCoresSize, reclaimedCoresLimit)
	return nil
}

func (pa *ProvisionAssemblerCommon) assembleReserve(result *types.InternalCPUCalculationResult) {
	// fill in reserve pool entry
	reservePoolSize, _ := pa.metaReader.GetPoolSize(commonstate.PoolNameReserve)
	result.SetPoolEntry(commonstate.PoolNameReserve, commonstate.FakedNUMAID, reservePoolSize, -1)
}

func (pa *ProvisionAssemblerCommon) AssembleProvision() (types.InternalCPUCalculationResult, error) {
	calculationResult := types.InternalCPUCalculationResult{
		PoolEntries:                           make(map[string]map[int]types.CPUResource),
		PoolOverlapInfo:                       map[string]map[int]map[string]int{},
		TimeStamp:                             time.Now(),
		AllowSharedCoresOverlapReclaimedCores: *pa.allowSharedCoresOverlapReclaimedCores,
	}

	pa.assembleReserve(&calculationResult)

	regionHelper := NewRegionMapHelper(*pa.regionMap)

	err := pa.assembleWithNUMABinding(regionHelper, &calculationResult)
	if err != nil {
		general.Errorf("assembleWithNUMABinding failed with error: %v", err)
		return types.InternalCPUCalculationResult{}, err
	}

	err = pa.assembleWithoutNUMABinding(regionHelper, &calculationResult)
	if err != nil {
		general.Errorf("assembleWithoutNUMABinding failed with error: %v", err)
		return types.InternalCPUCalculationResult{}, err
	}

	err = pa.assembleNUMABindingNUMAExclusive(regionHelper, &calculationResult)
	if err != nil {
		general.Errorf("assembleNUMABindingNUMAExclusive failed with error: %v", err)
		return types.InternalCPUCalculationResult{}, err
	}

	return calculationResult, nil
}

func (pa *ProvisionAssemblerCommon) assembleWithoutNUMABinding(regionHelper *RegionMapHelper, result *types.InternalCPUCalculationResult) error {
	return pa.assembleWithoutNUMAExclusivePool(regionHelper, commonstate.FakedNUMAID, result)
}

func (pa *ProvisionAssemblerCommon) assembleWithNUMABinding(regionHelper *RegionMapHelper, result *types.InternalCPUCalculationResult) error {
	for numaID := range *pa.numaAvailable {
		err := pa.assembleWithoutNUMAExclusivePool(regionHelper, numaID, result)
		if err != nil {
			return err
		}
	}

	return nil
}

func (pa *ProvisionAssemblerCommon) assembleNUMABindingNUMAExclusive(regionHelper *RegionMapHelper, result *types.InternalCPUCalculationResult) error {
	for numaID := range *pa.numaAvailable {
		dedicatedNUMAExclusiveRegions := regionHelper.GetRegions(numaID, configapi.QoSRegionTypeDedicatedNumaExclusive)
		for _, r := range dedicatedNUMAExclusiveRegions {
			if err := pa.assembleDedicatedNUMAExclusiveRegion(r, result); err != nil {
				return fmt.Errorf("failed to assemble dedicatedNUMAExclusiveRegion: %v", err)
			}
		}
	}

	return nil
}

func (pa *ProvisionAssemblerCommon) assembleWithoutNUMAExclusivePool(
	regionHelper *RegionMapHelper,
	numaID int,
	result *types.InternalCPUCalculationResult,
) error {
	shareRegions := regionHelper.GetRegions(numaID, configapi.QoSRegionTypeShare)
	shareInfo, err := extractShareRegionInfo(shareRegions)
	if err != nil {
		return err
	}

	isolationRegions := regionHelper.GetRegions(numaID, configapi.QoSRegionTypeIsolation)
	isolationInfo, err := extractIsolationRegionInfo(isolationRegions)
	if err != nil {
		return err
	}

	// todo support dedicated without numa exclusive region

	// skip empty numa binding region
	if len(shareRegions) == 0 && len(isolationRegions) == 0 && numaID != commonstate.FakedNUMAID {
		return nil
	}

	nodeEnableReclaim := pa.conf.GetDynamicConfiguration().EnableReclaim

	var numaSet machine.CPUSet
	if numaID == commonstate.FakedNUMAID {
		numaSet = *pa.nonBindingNumas
	} else {
		numaSet = machine.NewCPUSet(numaID)
	}

	shareAndIsolatedPoolAvailable := getNUMAsResource(*pa.numaAvailable, numaSet)
	if !*pa.allowSharedCoresOverlapReclaimedCores {
		shareAndIsolatedPoolAvailable -= getNUMAsResource(*pa.reservedForReclaim, numaSet)
	}

	allowExpand := !nodeEnableReclaim || *pa.allowSharedCoresOverlapReclaimedCores
	poolSizeRequirements := getPoolSizeRequirements(shareInfo, allowExpand)

	isolationUppers := general.SumUpMapValues(isolationInfo.isolationUpperSizes)
	isolationPoolSizes := isolationInfo.isolationUpperSizes
	// if the maximum of share poolSizeRequirements and share requests adds up with isolation upper sizes is larger than
	// the available cores of share and isolated pool, we should shrink the isolation pool sizes to lower sizes
	if general.Max(general.SumUpMapValues(shareInfo.shareRequests), general.SumUpMapValues(shareInfo.shareRequirements))+isolationUppers > shareAndIsolatedPoolAvailable {
		isolationPoolSizes = isolationInfo.isolationLowerSizes
	}

	shareAndIsolatePoolSizes, poolThrottled := regulatePoolSizes(poolSizeRequirements, isolationPoolSizes, shareAndIsolatedPoolAvailable, allowExpand)
	for _, r := range shareRegions {
		r.SetThrottled(poolThrottled)
	}

	general.InfoS("pool info",
		"numaID", numaID,
		"shareRequirements", shareInfo.shareRequirements,
		"shareRequests", shareInfo.shareRequests,
		"shareReclaimEnable", shareInfo.shareReclaimEnable,
		"minReclaimedCoresCPUQuota", shareInfo.minReclaimedCoresCPUQuota,
		"poolSizeRequirements", poolSizeRequirements,
		"isolationUpperSizes", isolationInfo.isolationUpperSizes,
		"isolationLowerSizes", isolationInfo.isolationLowerSizes,
		"shareAndIsolatePoolSizes", shareAndIsolatePoolSizes,
		"shareAndIsolatedPoolAvailable", shareAndIsolatedPoolAvailable)

	// fill in regulated share-and-isolated pool entries
	for poolName, poolSize := range shareAndIsolatePoolSizes {
		result.SetPoolEntry(poolName, numaID, poolSize, -1)
	}

	// assemble reclaim pool
	var reclaimedCoresSize int
	reclaimedCoresQuota := float64(-1)
	reservedForReclaim := getNUMAsResource(*pa.reservedForReclaim, numaSet)
	if *pa.allowSharedCoresOverlapReclaimedCores {
		isolated := 0
		sharePoolSizes := make(map[string]int)
		reclaimableSharePoolSizes := make(map[string]int)
		for poolName, size := range shareAndIsolatePoolSizes {
			_, ok := poolSizeRequirements[poolName]
			if ok {
				if shareInfo.shareReclaimEnable[poolName] {
					reclaimableSharePoolSizes[poolName] = shareAndIsolatePoolSizes[poolName]
				}
				sharePoolSizes[poolName] = shareAndIsolatePoolSizes[poolName]
			} else {
				isolated += size
			}
		}

		if nodeEnableReclaim {
			reclaimedCoresSize = general.Max(reservedForReclaim, shareAndIsolatedPoolAvailable-isolated-general.SumUpMapValues(poolSizeRequirements))
		} else {
			reclaimedCoresSize = reservedForReclaim
		}

		if len(sharePoolSizes) > 0 {
			sharedOverlapReclaimSize := make(map[string]int)
			if !nodeEnableReclaim {
				reclaimedCoresSize = general.Min(reclaimedCoresSize, general.SumUpMapValues(sharePoolSizes))
				var overlapSharePoolSizes map[string]int
				if reclaimedCoresSize <= general.SumUpMapValues(reclaimableSharePoolSizes) {
					overlapSharePoolSizes = reclaimableSharePoolSizes
				} else {
					overlapSharePoolSizes = sharePoolSizes
				}

				reclaimSizes, err := regulateOverlapReclaimPoolSize(overlapSharePoolSizes, reclaimedCoresSize)
				if err != nil {
					return fmt.Errorf("failed to regulateOverlapReclaimPoolSize: %w", err)
				}
				sharedOverlapReclaimSize = reclaimSizes
			} else {
				for poolName, size := range reclaimableSharePoolSizes {
					// calculate the reclaim size for each share pool by subtracting the share requirement from the share pool size
					reclaimSize := size - shareInfo.shareRequirements[poolName]
					if reclaimSize > 0 {
						sharedOverlapReclaimSize[poolName] = reclaimSize
					} else {
						sharedOverlapReclaimSize[poolName] = 1
					}
				}

				reclaimedCoresSize = general.SumUpMapValues(sharedOverlapReclaimSize)
				if reclaimedCoresSize < reservedForReclaim {
					reclaimedCoresSize = reservedForReclaim
					regulatedOverlapReclaimPoolSize, err := regulateOverlapReclaimPoolSize(sharePoolSizes, reclaimedCoresSize)
					if err != nil {
						return fmt.Errorf("failed to regulateOverlapReclaimPoolSize for NUMAs reserved for reclaim: %w", err)
					}
					sharedOverlapReclaimSize = regulatedOverlapReclaimPoolSize
				}
			}

			quotaCtrlKnobEnabled, err := metacache.IsQuotaCtrlKnobEnabled(pa.metaReader)
			if err != nil {
				return err
			}

			if quotaCtrlKnobEnabled && numaID != commonstate.FakedNUMAID {
				reclaimedCoresQuota = float64(general.Max(reservedForReclaim, reclaimedCoresSize))
				if shareInfo.minReclaimedCoresCPUQuota != -1 {
					reclaimedCoresQuota = general.MaxFloat64(float64(reservedForReclaim), shareInfo.minReclaimedCoresCPUQuota)
				}

				// if cpu quota enabled, set all reclaimable share pool size to reclaimableSharePoolSizes
				for poolName := range sharedOverlapReclaimSize {
					sharedOverlapReclaimSize[poolName] = general.Max(sharedOverlapReclaimSize[poolName], reclaimableSharePoolSizes[poolName])
				}
				reclaimedCoresSize = general.SumUpMapValues(sharedOverlapReclaimSize)
			}

			for overlapPoolName, size := range sharedOverlapReclaimSize {
				result.SetPoolOverlapInfo(commonstate.PoolNameReclaim, numaID, overlapPoolName, size)
			}
		}
	} else {
		if nodeEnableReclaim {
			reclaimedCoresSize = shareAndIsolatedPoolAvailable - general.SumUpMapValues(shareAndIsolatePoolSizes) + reservedForReclaim
		} else {
			reclaimedCoresSize = reservedForReclaim
		}
	}

	general.InfoS("assemble reclaim pool entry",
		"reservedForReclaim", reservedForReclaim,
		"reclaimedCoresSize", reclaimedCoresSize,
		"reclaimedCoresQuota", reclaimedCoresQuota,
		"numaID", numaID)

	result.SetPoolEntry(commonstate.PoolNameReclaim, numaID, reclaimedCoresSize, reclaimedCoresQuota)

	return nil
}

func getPoolSizeRequirements(info shareRegionInfo, expand bool) map[string]int {
	result := make(map[string]int)
	for name, reclaimEnable := range info.shareReclaimEnable {
		if !reclaimEnable || expand {
			result[name] = info.shareRequests[name]
		} else {
			result[name] = info.shareRequirements[name]
		}
	}
	return result
}

type shareRegionInfo struct {
	shareRequirements         map[string]int
	shareRequests             map[string]int
	shareReclaimEnable        map[string]bool
	minReclaimedCoresCPUQuota float64
}

func extractShareRegionInfo(shareRegions []region.QoSRegion) (shareRegionInfo, error) {
	shareRequirements := make(map[string]int)
	shareRequests := make(map[string]int)
	shareReclaimEnable := make(map[string]bool)
	minReclaimedCoresCPUQuota := float64(-1)

	for _, r := range shareRegions {
		controlKnob, err := r.GetProvision()
		if err != nil {
			return shareRegionInfo{}, err
		}
		shareRequirements[r.OwnerPoolName()] = general.Max(1, int(controlKnob[configapi.ControlKnobNonReclaimedCPURequirement].Value))
		shareRequests[r.OwnerPoolName()] = general.Max(1, int(math.Ceil(r.GetPodsRequest())))
		shareReclaimEnable[r.OwnerPoolName()] = r.EnableReclaim()
		if quota, ok := controlKnob[configapi.ControlKnobReclaimedCoresCPUQuota]; ok {
			if minReclaimedCoresCPUQuota == -1 || quota.Value < minReclaimedCoresCPUQuota {
				minReclaimedCoresCPUQuota = quota.Value
			}
		}
	}

	return shareRegionInfo{
		shareRequirements:         shareRequirements,
		shareRequests:             shareRequests,
		shareReclaimEnable:        shareReclaimEnable,
		minReclaimedCoresCPUQuota: minReclaimedCoresCPUQuota,
	}, nil
}

type isolationRegionInfo struct {
	isolationUpperSizes map[string]int
	isolationLowerSizes map[string]int
}

func extractIsolationRegionInfo(isolationRegions []region.QoSRegion) (isolationRegionInfo, error) {
	isolationUpperSizes := make(map[string]int)
	isolationLowerSizes := make(map[string]int)

	for _, r := range isolationRegions {
		controlKnob, err := r.GetProvision()
		if err != nil {
			return isolationRegionInfo{}, err
		}
		// save limits and requests for isolated region
		isolationUpperSizes[r.Name()] = int(controlKnob[configapi.ControlKnobNonIsolatedUpperCPUSize].Value)
		isolationLowerSizes[r.Name()] = int(controlKnob[configapi.ControlKnobNonIsolatedLowerCPUSize].Value)
	}

	return isolationRegionInfo{
		isolationUpperSizes: isolationUpperSizes,
		isolationLowerSizes: isolationLowerSizes,
	}, nil
}

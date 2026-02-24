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

	// set pool overlap info for dedicated pool
	for podUID, containerSet := range r.GetPods() {
		for containerName := range containerSet {
			general.InfoS("set pool overlap pod container info",
				"poolName", commonstate.PoolNameReclaim,
				"numaID", regionNuma,
				"podUID", podUID,
				"containerName", containerName,
				"reclaimSize", reclaimedCoresSize)
			result.SetPoolOverlapPodContainerInfo(commonstate.PoolNameReclaim, regionNuma, podUID, containerName, reclaimedCoresSize)
		}
	}

	// set reclaim pool cpu limit
	result.SetPoolEntry(commonstate.PoolNameReclaim, regionNuma, 0, reclaimedCoresLimit)
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
		PoolOverlapPodContainerInfo:           map[string]map[int]map[string]map[string]int{},
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
		dedicatedNUMAExclusiveRegions := regionHelper.GetRegions(numaID, configapi.QoSRegionTypeDedicated)
		for _, r := range dedicatedNUMAExclusiveRegions {
			if !r.IsNumaBinding() || !r.IsNumaExclusive() {
				continue
			}

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
	var numaSet machine.CPUSet
	if numaID == commonstate.FakedNUMAID {
		numaSet = *pa.nonBindingNumas
	} else {
		numaSet = machine.NewCPUSet(numaID)
	}

	pinnedCPUSizeByPkg := pa.getPinnedCPUSizeByPackage(numaSet)
	totalPinnedCPUSize := general.SumUpMapValues(pinnedCPUSizeByPkg)

	unpinnedShareRegionInfo, pinnedShareRegionInfos, err := extractShareRegionInfo(shareRegions, pinnedCPUSizeByPkg)
	if err != nil {
		return err
	}

	isolationRegions := regionHelper.GetRegions(numaID, configapi.QoSRegionTypeIsolation)
	unpinnedIsolationInfo, pinnedIsolationInfo, err := extractIsolationRegionInfo(isolationRegions, pinnedCPUSizeByPkg)
	if err != nil {
		return err
	}

	dedicatedRegions := regionHelper.GetRegions(numaID, configapi.QoSRegionTypeDedicated)
	unpinnedDedicatedInfo, pinnedDedicatedInfo, err := extractDedicatedRegionInfo(dedicatedRegions, pinnedCPUSizeByPkg)
	if err != nil {
		return err
	}

	// skip empty numa binding region
	if len(shareRegions) == 0 && len(isolationRegions) == 0 && len(dedicatedRegions) == 0 && numaID != commonstate.FakedNUMAID {
		return nil
	}

	nodeEnableReclaim := pa.conf.GetDynamicConfiguration().EnableReclaim

	reservedForReclaim := getNUMAsResource(*pa.reservedForReclaim, numaSet)
	shareAndIsolatedDedicatedPoolAvailable := getNUMAsResource(*pa.numaAvailable, numaSet)
	if !*pa.allowSharedCoresOverlapReclaimedCores {
		shareAndIsolatedDedicatedPoolAvailable -= reservedForReclaim
	}

	getShareAndIsolateDedicatedPoolSizesFunc := func(
		shareAndIsolatedDedicatedPoolAvailable int,
		shareRegionInfo, dedicatedRegionInfo regionInfo,
		isolationRegionInfo isolationRegionInfo,
	) map[string]int {
		sharePoolSizeRequirements := getPoolSizeRequirements(shareRegionInfo)

		isolationUppers := general.SumUpMapValues(isolationRegionInfo.isolationUpperSizes)
		isolationPoolSizes := isolationRegionInfo.isolationUpperSizes
		// if the maximum of share sharePoolSizeRequirements and share requests adds up with isolation upper sizes is larger than
		// the available cores of share and isolated pool, we should shrink the isolation pool sizes to lower sizes
		if general.Max(general.SumUpMapValues(shareRegionInfo.requests), general.SumUpMapValues(shareRegionInfo.requirements))+isolationUppers >
			shareAndIsolatedDedicatedPoolAvailable-general.SumUpMapValues(dedicatedRegionInfo.requests) {
			isolationPoolSizes = isolationRegionInfo.isolationLowerSizes
		}

		allowExpand := !nodeEnableReclaim || *pa.allowSharedCoresOverlapReclaimedCores
		var regulateSharePoolSizes map[string]int
		if allowExpand {
			regulateSharePoolSizes = shareRegionInfo.requests
		} else {
			regulateSharePoolSizes = sharePoolSizeRequirements
		}
		unexpandableRequirements := general.MergeMapInt(isolationPoolSizes, dedicatedRegionInfo.requests)
		shareAndIsolateDedicatedPoolSizes, poolThrottled := regulatePoolSizes(regulateSharePoolSizes, unexpandableRequirements, shareAndIsolatedDedicatedPoolAvailable, allowExpand)
		for _, r := range shareRegionInfo.regionMap {
			r.SetThrottled(poolThrottled)
		}

		return shareAndIsolateDedicatedPoolSizes
	}

	shareInfo := initRegionInfo()
	isolationInfo := initIsolationRegionInfo()
	dedicatedInfo := initRegionInfo()
	shareAndIsolateDedicatedPoolSizes := make(map[string]int)
	unpinnedShareAndIsolatedDedicatedPoolAvailable := general.Max(0, shareAndIsolatedDedicatedPoolAvailable-totalPinnedCPUSize)
	pinnedCPUSetAllInfo := getPinnedCPUSetAllRegionInfo(pinnedShareRegionInfos, pinnedIsolationInfo, pinnedDedicatedInfo)

	// first calculate share and isolate dedicated pool sizes for each pinned region
	for pkgName, allInfo := range pinnedCPUSetAllInfo {
		pinnedCPUSize := pinnedCPUSizeByPkg[pkgName]
		poolSizes := getShareAndIsolateDedicatedPoolSizesFunc(pinnedCPUSize, allInfo.shareRegionInfo, allInfo.dedicatedRegionInfos, allInfo.isolationRegionInfo)
		for poolName, size := range poolSizes {
			shareAndIsolateDedicatedPoolSizes[poolName] = size
		}

		shareInfo.merge(allInfo.shareRegionInfo)
		isolationInfo.merge(allInfo.isolationRegionInfo)
		dedicatedInfo.merge(allInfo.dedicatedRegionInfos)

		general.InfoS("pinned pool info",
			"numaID", numaID,
			"pkgName", pkgName,
			"shareRegionInfo", allInfo.shareRegionInfo,
			"isolationRegionInfo", allInfo.isolationRegionInfo,
			"dedicatedRegionInfos", allInfo.dedicatedRegionInfos,
			"pinnedCPUSize", pinnedCPUSize,
			"poolSizes", poolSizes)
	}

	unpinnedPoolSizes := getShareAndIsolateDedicatedPoolSizesFunc(unpinnedShareAndIsolatedDedicatedPoolAvailable, unpinnedShareRegionInfo, unpinnedDedicatedInfo, unpinnedIsolationInfo)
	for poolName, size := range unpinnedPoolSizes {
		shareAndIsolateDedicatedPoolSizes[poolName] = size
	}

	shareInfo.merge(unpinnedShareRegionInfo)
	isolationInfo.merge(unpinnedIsolationInfo)
	dedicatedInfo.merge(unpinnedDedicatedInfo)

	general.InfoS("unpinned pool info",
		"numaID", numaID,
		"unpinnedShareRegionInfo", unpinnedShareRegionInfo,
		"unpinnedIsolationRegionInfo", unpinnedIsolationInfo,
		"unpinnedDedicatedRegionInfos", unpinnedDedicatedInfo,
		"unpinnedShareAndIsolatedDedicatedPoolAvailable", unpinnedShareAndIsolatedDedicatedPoolAvailable,
		"poolSizes", unpinnedPoolSizes)

	dedicatedPoolSizes := make(map[string]int)
	for poolName := range dedicatedInfo.requests {
		if size, ok := shareAndIsolateDedicatedPoolSizes[poolName]; ok {
			dedicatedPoolSizes[poolName] = size
		}
	}
	dedicatedPoolAvailable := general.SumUpMapValues(dedicatedPoolSizes)
	dedicatedPoolSizeRequirements := getPoolSizeRequirements(dedicatedInfo)
	dedicatedReclaimCoresSize := dedicatedPoolAvailable - general.SumUpMapValues(dedicatedPoolSizeRequirements)

	general.InfoS("pool info",
		"numaID", numaID,
		"reservedForReclaim", reservedForReclaim,
		"shareRequirements", shareInfo.requirements,
		"shareRequests", shareInfo.requests,
		"shareReclaimEnable", shareInfo.reclaimEnable,
		"shareMinReclaimedCoresCPUQuota", shareInfo.minReclaimedCoresCPUQuota,
		"dedicatedRequirements", dedicatedInfo.requirements,
		"dedicatedRequests", dedicatedInfo.requests,
		"dedicatedReclaimEnable", dedicatedInfo.reclaimEnable,
		"dedicatedMinReclaimedCoresCPUQuota", dedicatedInfo.minReclaimedCoresCPUQuota,
		"dedicatedPoolAvailable", dedicatedPoolAvailable,
		"dedicatedPoolSizeRequirements", dedicatedPoolSizeRequirements,
		"dedicatedReclaimCoresSize", dedicatedReclaimCoresSize,
		"sharePoolSizeRequirements", getPoolSizeRequirements(shareInfo),
		"isolationUpperSizes", isolationInfo.isolationUpperSizes,
		"isolationLowerSizes", isolationInfo.isolationLowerSizes,
		"shareAndIsolateDedicatedPoolSizes", shareAndIsolateDedicatedPoolSizes,
		"shareAndIsolatedDedicatedPoolAvailable", shareAndIsolatedDedicatedPoolAvailable)

	// fill in regulated share-and-isolated pool entries
	for poolName, poolSize := range shareAndIsolateDedicatedPoolSizes {
		if podSet, ok := dedicatedInfo.podSet[poolName]; ok {
			// fill in dedicated pool entries with pod uid for each pod
			for uid := range podSet {
				result.SetPoolEntry(uid, numaID, poolSize, -1)
			}
		} else {
			// fill in share pool or isolation pool entries with pool name for each pod
			result.SetPoolEntry(poolName, numaID, poolSize, -1)
		}
	}

	reclaimPoolData := &reclaimPoolCalculationData{
		shareInfo:                              shareInfo,
		isolationInfo:                          isolationInfo,
		dedicatedInfo:                          dedicatedInfo,
		shareAndIsolateDedicatedPoolSizes:      shareAndIsolateDedicatedPoolSizes,
		dedicatedPoolSizes:                     dedicatedPoolSizes,
		dedicatedReclaimCoresSize:              dedicatedReclaimCoresSize,
		shareAndIsolatedDedicatedPoolAvailable: shareAndIsolatedDedicatedPoolAvailable,
		reservedForReclaim:                     reservedForReclaim,
		nodeEnableReclaim:                      nodeEnableReclaim,
		numaID:                                 numaID,
	}

	reclaimedCoresSize, overlapReclaimedCoresSize, reclaimedCoresQuota, err := pa.calculateReclaimPool(reclaimPoolData, result)
	if err != nil {
		return err
	}

	nonOverlapReclaimedCoresSize := general.Max(reclaimedCoresSize-overlapReclaimedCoresSize, 0)
	result.SetPoolEntry(commonstate.PoolNameReclaim, numaID, nonOverlapReclaimedCoresSize, reclaimedCoresQuota)

	general.InfoS("assemble reclaim pool entry",
		"numaID", numaID,
		"reservedForReclaim", reservedForReclaim,
		"reclaimedCoresSize", reclaimedCoresSize,
		"overlapReclaimedCoresSize", overlapReclaimedCoresSize,
		"nonOverlapReclaimedCoresSize", nonOverlapReclaimedCoresSize,
		"reclaimedCoresQuota", reclaimedCoresQuota)

	return nil
}

type reclaimPoolCalculationData struct {
	shareInfo                              regionInfo
	isolationInfo                          isolationRegionInfo
	dedicatedInfo                          regionInfo
	shareAndIsolateDedicatedPoolSizes      map[string]int
	dedicatedPoolSizes                     map[string]int
	dedicatedReclaimCoresSize              int
	shareAndIsolatedDedicatedPoolAvailable int
	reservedForReclaim                     int
	nodeEnableReclaim                      bool
	numaID                                 int
}

func (pa *ProvisionAssemblerCommon) calculateReclaimPool(
	data *reclaimPoolCalculationData,
	result *types.InternalCPUCalculationResult,
) (int, int, float64, error) {
	if *pa.allowSharedCoresOverlapReclaimedCores {
		return pa.calculateOverlapReclaimPool(data, result)
	}
	return pa.calculateNonOverlapReclaimPool(data, result)
}

func (pa *ProvisionAssemblerCommon) calculateOverlapReclaimPool(
	data *reclaimPoolCalculationData,
	result *types.InternalCPUCalculationResult,
) (int, int, float64, error) {
	var reclaimedCoresSize, overlapReclaimedCoresSize int
	reclaimedCoresQuota := float64(-1)

	isolated := 0
	poolSizes := make(map[string]int)
	sharePoolSizes := make(map[string]int)
	reclaimablePoolSizes := make(map[string]int)
	nonReclaimableSharePoolSizes := make(map[string]int)
	reclaimableShareRequirements := make(map[string]int)
	reclaimableRequirements := make(map[string]int)

	for poolName, size := range data.shareAndIsolateDedicatedPoolSizes {
		_, ok := data.shareInfo.requirements[poolName]
		if ok {
			if data.shareInfo.reclaimEnable[poolName] {
				reclaimablePoolSizes[poolName] = size
				reclaimableShareRequirements[poolName] = data.shareInfo.requirements[poolName]
				reclaimableRequirements[poolName] = data.shareInfo.requirements[poolName]
			} else {
				nonReclaimableSharePoolSizes[poolName] = size
			}
			poolSizes[poolName] = size
			sharePoolSizes[poolName] = size
		}

		_, ok = data.isolationInfo.isolationUpperSizes[poolName]
		if ok {
			isolated += size
		}

		_, ok = data.dedicatedInfo.requests[poolName]
		if ok {
			if data.dedicatedInfo.reclaimEnable[poolName] {
				reclaimablePoolSizes[poolName] = size
				reclaimableRequirements[poolName] = data.dedicatedInfo.requirements[poolName]
			}
			poolSizes[poolName] = size
		}
	}

	overlapReclaimSize := make(map[string]int)
	shareReclaimCoresSize := data.shareAndIsolatedDedicatedPoolAvailable - isolated -
		general.SumUpMapValues(nonReclaimableSharePoolSizes) - general.SumUpMapValues(reclaimableShareRequirements) -
		general.SumUpMapValues(data.dedicatedPoolSizes)

	if data.nodeEnableReclaim {
		reclaimedCoresSize = shareReclaimCoresSize + data.dedicatedReclaimCoresSize
		if reclaimedCoresSize < data.reservedForReclaim {
			reclaimedCoresSize = data.reservedForReclaim
			regulatedOverlapReclaimPoolSize, err := regulateOverlapReclaimPoolSize(poolSizes, reclaimedCoresSize)
			if err != nil {
				return 0, 0, 0, fmt.Errorf("failed to regulateOverlapReclaimPoolSize for NUMAs reserved for reclaim: %w", err)
			}
			overlapReclaimSize = regulatedOverlapReclaimPoolSize
		} else {
			for poolName, size := range reclaimablePoolSizes {
				requirement, ok := reclaimableRequirements[poolName]
				if !ok {
					continue
				}
				reclaimSize := size - requirement
				if reclaimSize > 0 {
					overlapReclaimSize[poolName] = reclaimSize
				} else {
					overlapReclaimSize[poolName] = 1
				}
			}
		}
	} else {
		reclaimedCoresSize = data.reservedForReclaim
		if len(poolSizes) > 0 && reclaimedCoresSize > shareReclaimCoresSize {
			reclaimedCoresSize = general.Min(reclaimedCoresSize, general.SumUpMapValues(poolSizes))
			var overlapSharePoolSizes map[string]int
			if reclaimedCoresSize <= general.SumUpMapValues(reclaimablePoolSizes) {
				overlapSharePoolSizes = reclaimablePoolSizes
			} else {
				overlapSharePoolSizes = poolSizes
			}

			reclaimSizes, err := regulateOverlapReclaimPoolSize(overlapSharePoolSizes, reclaimedCoresSize)
			if err != nil {
				return 0, 0, 0, fmt.Errorf("failed to regulateOverlapReclaimPoolSize: %w", err)
			}
			overlapReclaimSize = reclaimSizes
		} else if len(sharePoolSizes) > 0 && reclaimedCoresSize <= general.SumUpMapValues(sharePoolSizes) {
			reclaimSizes, err := regulateOverlapReclaimPoolSize(sharePoolSizes, reclaimedCoresSize)
			if err != nil {
				return 0, 0, 0, fmt.Errorf("failed to regulateOverlapReclaimPoolSize: %w", err)
			}
			overlapReclaimSize = reclaimSizes
		}
	}

	quotaCtrlKnobEnabled, err := metacache.IsQuotaCtrlKnobEnabled(pa.metaReader)
	if err != nil {
		return 0, 0, 0, err
	}

	if quotaCtrlKnobEnabled && data.numaID != commonstate.FakedNUMAID && len(poolSizes) > 0 {
		reclaimedCoresQuota = float64(general.Max(data.reservedForReclaim, reclaimedCoresSize))
		if data.shareInfo.minReclaimedCoresCPUQuota != -1 || data.dedicatedInfo.minReclaimedCoresCPUQuota != -1 {
			if data.shareInfo.minReclaimedCoresCPUQuota != -1 {
				reclaimedCoresQuota = data.shareInfo.minReclaimedCoresCPUQuota
			}

			if data.dedicatedInfo.minReclaimedCoresCPUQuota != -1 {
				reclaimedCoresQuota = general.MinFloat64(reclaimedCoresQuota, data.dedicatedInfo.minReclaimedCoresCPUQuota)
			}

			reclaimedCoresQuota = general.MaxFloat64(reclaimedCoresQuota, float64(data.reservedForReclaim))
		}

		// if cpu quota enabled, set all reclaimable share pool size to reclaimablePoolSizes
		for poolName := range overlapReclaimSize {
			overlapReclaimSize[poolName] = general.Max(overlapReclaimSize[poolName], reclaimablePoolSizes[poolName])
		}
	}

	for overlapPoolName, reclaimSize := range overlapReclaimSize {
		if _, ok := data.shareInfo.requests[overlapPoolName]; ok {
			general.InfoS("set pool overlap info",
				"poolName", commonstate.PoolNameReclaim,
				"numaID", data.numaID,
				"poolName", overlapPoolName,
				"reclaimSize", reclaimSize)
			result.SetPoolOverlapInfo(commonstate.PoolNameReclaim, data.numaID, overlapPoolName, reclaimSize)
			overlapReclaimedCoresSize += reclaimSize
			continue
		}

		if podSet, ok := data.dedicatedInfo.podSet[overlapPoolName]; ok {
			// set pool overlap info for dedicated pool
			for podUID, containerSet := range podSet {
				for containerName := range containerSet {
					general.InfoS("set pool overlap pod container info",
						"poolName", commonstate.PoolNameReclaim,
						"numaID", data.numaID,
						"podUID", podUID,
						"containerName", containerName,
						"reclaimSize", reclaimSize)
					result.SetPoolOverlapPodContainerInfo(commonstate.PoolNameReclaim, data.numaID, podUID, containerName, reclaimSize)
				}
			}
			overlapReclaimedCoresSize += reclaimSize
			continue
		}
	}

	return reclaimedCoresSize, overlapReclaimedCoresSize, reclaimedCoresQuota, nil
}

func (pa *ProvisionAssemblerCommon) calculateNonOverlapReclaimPool(
	data *reclaimPoolCalculationData,
	result *types.InternalCPUCalculationResult,
) (int, int, float64, error) {
	var reclaimedCoresSize, overlapReclaimedCoresSize int
	reclaimedCoresQuota := float64(-1)

	if data.nodeEnableReclaim {
		for poolName, size := range data.dedicatedInfo.requests {
			if data.dedicatedInfo.reclaimEnable[poolName] {
				reclaimSize := size - data.dedicatedInfo.requirements[poolName]
				if reclaimSize <= 0 {
					continue
				}
				if podSet, ok := data.dedicatedInfo.podSet[poolName]; ok {
					for podUID, containerSet := range podSet {
						for containerName := range containerSet {
							general.InfoS("set pool overlap pod container info",
								"poolName", commonstate.PoolNameReclaim,
								"numaID", data.numaID,
								"podUID", podUID,
								"containerName", containerName,
								"reclaimSize", reclaimSize)
							result.SetPoolOverlapPodContainerInfo(commonstate.PoolNameReclaim, data.numaID, podUID, containerName, reclaimSize)
						}
					}
					overlapReclaimedCoresSize += reclaimSize
					continue
				}
			}
		}

		shareReclaimedCoresSize := data.shareAndIsolatedDedicatedPoolAvailable - general.SumUpMapValues(data.shareAndIsolateDedicatedPoolSizes)
		reclaimedCoresSize = shareReclaimedCoresSize + data.dedicatedReclaimCoresSize + data.reservedForReclaim
	} else {
		reclaimedCoresSize = data.reservedForReclaim
	}

	return reclaimedCoresSize, overlapReclaimedCoresSize, reclaimedCoresQuota, nil
}

// regionInfo is a struct that contains region information
// for share region the key of map is owner pool name
// for dedicated region the key of map is region name
type regionInfo struct {
	requirements              map[string]int
	requests                  map[string]int
	reclaimEnable             map[string]bool
	podSet                    map[string]types.PodSet
	minReclaimedCoresCPUQuota float64
	regionMap                 map[string]region.QoSRegion
}

func (r *regionInfo) merge(other regionInfo) {
	for poolName, size := range other.requirements {
		r.requirements[poolName] = size
	}

	for poolName, size := range other.requests {
		r.requests[poolName] = size
	}

	for poolName, enable := range other.reclaimEnable {
		r.reclaimEnable[poolName] = enable
	}

	for poolName, podSet := range other.podSet {
		r.podSet[poolName] = podSet
	}

	if r.minReclaimedCoresCPUQuota == -1 || other.minReclaimedCoresCPUQuota < r.minReclaimedCoresCPUQuota {
		r.minReclaimedCoresCPUQuota = other.minReclaimedCoresCPUQuota
	}

	for poolName, reg := range other.regionMap {
		r.regionMap[poolName] = reg
	}
}

func initRegionInfo() regionInfo {
	return regionInfo{
		requirements:              make(map[string]int),
		requests:                  make(map[string]int),
		reclaimEnable:             make(map[string]bool),
		podSet:                    make(map[string]types.PodSet),
		minReclaimedCoresCPUQuota: -1,
		regionMap:                 make(map[string]region.QoSRegion),
	}
}

func (pa *ProvisionAssemblerCommon) getPinnedCPUSizeByPackage(numaSet machine.CPUSet) map[string]int {
	pinnedCPUSizeByPkg := make(map[string]int)

	cfg := pa.metaReader.GetResourcePackageConfig()
	if len(cfg) > 0 {
		for _, numaID := range numaSet.ToSliceInt() {
			pkgMap, ok := cfg[numaID]
			if !ok {
				continue
			}
			for pkgName, pinnedCPUSet := range pkgMap {
				size := pinnedCPUSet.Size()
				if size <= 0 {
					continue
				}
				pinnedCPUSizeByPkg[pkgName] += size
			}
		}
		return pinnedCPUSizeByPkg
	}
	return pinnedCPUSizeByPkg
}

func extractShareRegionInfo(shareRegions []region.QoSRegion, pinnedCPUSizeByPkg map[string]int) (regionInfo, map[string]*regionInfo, error) {
	unpinnedRegionInfo := initRegionInfo()
	pinnedRegionInfos := make(map[string]*regionInfo)

	for _, r := range shareRegions {
		controlKnob, err := r.GetProvision()
		if err != nil {
			return regionInfo{}, nil, err
		}

		ri := &unpinnedRegionInfo
		pkgName := r.GetResourcePackageName()
		if pkgName != "" {
			if _, ok := pinnedCPUSizeByPkg[pkgName]; ok {
				if _, exists := pinnedRegionInfos[pkgName]; !exists {
					info := initRegionInfo()
					pinnedRegionInfos[pkgName] = &info
				}
				ri = pinnedRegionInfos[pkgName]
			}
		}

		ri.requirements[r.OwnerPoolName()] = general.Max(1, int(controlKnob[configapi.ControlKnobNonReclaimedCPURequirement].Value))
		ri.requests[r.OwnerPoolName()] = general.Max(1, int(math.Ceil(r.GetPodsRequest())))
		ri.reclaimEnable[r.OwnerPoolName()] = r.EnableReclaim()
		if quota, ok := controlKnob[configapi.ControlKnobReclaimedCoresCPUQuota]; ok {
			if ri.minReclaimedCoresCPUQuota == -1 || quota.Value < ri.minReclaimedCoresCPUQuota {
				ri.minReclaimedCoresCPUQuota = quota.Value
			}
		}
		ri.regionMap[r.OwnerPoolName()] = r
	}

	return unpinnedRegionInfo, pinnedRegionInfos, nil
}

func getPoolSizeRequirements(info regionInfo) map[string]int {
	result := make(map[string]int)
	for name, reclaimEnable := range info.reclaimEnable {
		if !reclaimEnable {
			result[name] = info.requests[name]
		} else {
			result[name] = info.requirements[name]
		}
	}
	return result
}

type isolationRegionInfo struct {
	isolationUpperSizes map[string]int
	isolationLowerSizes map[string]int
}

func (r *isolationRegionInfo) merge(other isolationRegionInfo) {
	for poolName, size := range other.isolationUpperSizes {
		r.isolationUpperSizes[poolName] = size
	}

	for poolName, size := range other.isolationLowerSizes {
		r.isolationLowerSizes[poolName] = size
	}
}

func initIsolationRegionInfo() isolationRegionInfo {
	return isolationRegionInfo{
		isolationUpperSizes: make(map[string]int),
		isolationLowerSizes: make(map[string]int),
	}
}

func extractIsolationRegionInfo(isolationRegions []region.QoSRegion, pinnedCPUSizeByPkg map[string]int) (isolationRegionInfo, map[string]*isolationRegionInfo, error) {
	unpinnedRegionInfo := initIsolationRegionInfo()
	pinnedRegionInfos := make(map[string]*isolationRegionInfo)

	for _, r := range isolationRegions {
		controlKnob, err := r.GetProvision()
		if err != nil {
			return isolationRegionInfo{}, nil, err
		}

		ri := &unpinnedRegionInfo
		pkgName := r.GetResourcePackageName()
		if pkgName != "" {
			if _, ok := pinnedCPUSizeByPkg[pkgName]; ok {
				if _, exists := pinnedRegionInfos[pkgName]; !exists {
					info := initIsolationRegionInfo()
					pinnedRegionInfos[pkgName] = &info
				}
				ri = pinnedRegionInfos[pkgName]
			}
		}

		ri.isolationUpperSizes[r.Name()] = int(controlKnob[configapi.ControlKnobNonIsolatedUpperCPUSize].Value)
		ri.isolationLowerSizes[r.Name()] = int(controlKnob[configapi.ControlKnobNonIsolatedLowerCPUSize].Value)
	}

	return unpinnedRegionInfo, pinnedRegionInfos, nil
}

func extractDedicatedRegionInfo(regions []region.QoSRegion, pinnedCPUSizeByPkg map[string]int) (regionInfo, map[string]*regionInfo, error) {
	unpinnedRegionInfo := initRegionInfo()
	pinnedRegionInfos := make(map[string]*regionInfo)

	for _, r := range regions {
		if r.IsNumaExclusive() {
			continue
		}

		controlKnob, err := r.GetProvision()
		if err != nil {
			return regionInfo{}, nil, err
		}

		ri := &unpinnedRegionInfo
		pkgName := r.GetResourcePackageName()
		if pkgName != "" {
			if _, ok := pinnedCPUSizeByPkg[pkgName]; ok {
				if _, exists := pinnedRegionInfos[pkgName]; !exists {
					info := initRegionInfo()
					pinnedRegionInfos[pkgName] = &info
				}
				ri = pinnedRegionInfos[pkgName]
			}
		}

		regionName := r.Name()
		ri.requirements[regionName] = general.Max(1, int(controlKnob[configapi.ControlKnobNonReclaimedCPURequirement].Value))
		if r.IsNumaBinding() {
			numaBindingSize := r.GetBindingNumas().Size()
			if numaBindingSize == 0 {
				return regionInfo{}, nil, fmt.Errorf("numa binding size is zero, region name: %s", r.Name())
			}
			ri.requests[regionName] = int(math.Ceil(r.GetPodsRequest() / float64(numaBindingSize)))
		} else {
			ri.requests[regionName] = int(math.Ceil(r.GetPodsRequest()))
		}
		ri.reclaimEnable[regionName] = r.EnableReclaim()
		ri.podSet[regionName] = r.GetPods()
		if quota, ok := controlKnob[configapi.ControlKnobReclaimedCoresCPUQuota]; ok {
			if ri.minReclaimedCoresCPUQuota == -1 || quota.Value < ri.minReclaimedCoresCPUQuota {
				ri.minReclaimedCoresCPUQuota = quota.Value
			}
		}
		ri.regionMap[regionName] = r
	}

	return unpinnedRegionInfo, pinnedRegionInfos, nil
}

type pinnedCPUSetAllRegionInfo struct {
	shareRegionInfo      regionInfo
	isolationRegionInfo  isolationRegionInfo
	dedicatedRegionInfos regionInfo
}

func initPinnedCPUSetAllRegionInfo() *pinnedCPUSetAllRegionInfo {
	return &pinnedCPUSetAllRegionInfo{
		shareRegionInfo:      initRegionInfo(),
		isolationRegionInfo:  initIsolationRegionInfo(),
		dedicatedRegionInfos: initRegionInfo(),
	}
}

func getPinnedCPUSetAllRegionInfo(
	shareRegionInfo map[string]*regionInfo,
	isolationRegionInfo map[string]*isolationRegionInfo,
	dedicatedRegionInfos map[string]*regionInfo,
) map[string]*pinnedCPUSetAllRegionInfo {
	res := make(map[string]*pinnedCPUSetAllRegionInfo)
	for pkgName, info := range shareRegionInfo {
		_, ok := res[pkgName]
		if !ok {
			res[pkgName] = initPinnedCPUSetAllRegionInfo()
		}
		res[pkgName].shareRegionInfo = *info
	}

	for regionName, info := range isolationRegionInfo {
		_, ok := res[regionName]
		if !ok {
			res[regionName] = initPinnedCPUSetAllRegionInfo()
		}
		res[regionName].isolationRegionInfo = *info
	}

	for regionName, info := range dedicatedRegionInfos {
		_, ok := res[regionName]
		if !ok {
			res[regionName] = initPinnedCPUSetAllRegionInfo()
		}
		res[regionName].dedicatedRegionInfos = *info
	}

	return res
}

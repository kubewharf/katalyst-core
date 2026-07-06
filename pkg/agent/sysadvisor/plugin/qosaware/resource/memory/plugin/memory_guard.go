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

package plugin

import (
	"math"
	"strconv"

	"go.uber.org/atomic"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/memory/dynamicpolicy/memoryadvisor"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/qosaware/resource/helper"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/global"
	"github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/cgroup/common"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

const (
	MemoryGuard = "memory-guard"

	reconcileStatusSucceeded = "succeeded"
	reconcileStatusFailed    = "failed"

	reclaimMemoryUnlimited = -1

	defaultProcZoneInfoFile = "/proc/zoneinfo"

	// Factor for calculating memory.high based on memory.max, available for cgroup v2 only
	memoryHighScaleFactor = 0.95
)

type memoryGuard struct {
	metaReader                         metacache.MetaReader
	metaServer                         *metaserver.MetaServer
	emitter                            metrics.MetricEmitter
	reclaimRelativeRootCgroupPaths     []string
	numaBindingRelativeRootCgroupPaths map[int][]string
	reclaimMemoryLimit                 *atomic.Int64
	numaBindingReclaimMemoryLimit      *atomic.Value
	reconcileStatus                    *atomic.String
	minCriticalWatermark               int64
	conf                               *config.Configuration
}

func NewMemoryGuard(conf *config.Configuration, extraConfig interface{}, metaReader metacache.MetaReader, metaServer *metaserver.MetaServer, emitter metrics.MetricEmitter) MemoryAdvisorPlugin {
	entries := conf.ReclaimRelativeRootCgroupPaths
	if len(entries) == 0 {
		entries = []global.ReclaimRelativeRootCgroupPathEntry{{
			Path:          conf.ReclaimRelativeRootCgroupPath,
			NUMASeparator: "-",
		}}
	}

	parentPaths := make([]string, 0, len(entries))
	for _, e := range entries {
		parentPaths = append(parentPaths, e.Path)
	}

	return &memoryGuard{
		metaReader:                     metaReader,
		metaServer:                     metaServer,
		emitter:                        emitter,
		reclaimRelativeRootCgroupPaths: parentPaths,
		numaBindingRelativeRootCgroupPaths: common.GetNUMABindingReclaimRelativeRootCgroupPathsMulti(
			entries, metaServer.CPUDetails.NUMANodes().ToSliceNoSortInt()),
		reclaimMemoryLimit:            atomic.NewInt64(-1),
		numaBindingReclaimMemoryLimit: &atomic.Value{},
		reconcileStatus:               atomic.NewString(reconcileStatusFailed),
		minCriticalWatermark:          conf.MinCriticalWatermark,
		conf:                          conf,
	}
}

func (mg *memoryGuard) Reconcile(status *types.MemoryPressureStatus) error {
	dynamicConfig := mg.conf.GetDynamicConfiguration()
	if !dynamicConfig.MemoryGuardConfiguration.Enable {
		mg.reclaimMemoryLimit.Store(int64(reclaimMemoryUnlimited))
		mg.reconcileStatus.Store(reconcileStatusSucceeded)
		general.InfoS("memory guard is disabled")
		return nil
	}

	mg.reconcileStatus.Store(reconcileStatusFailed)

	zoneInfos := machine.GetNormalZoneInfo(defaultProcZoneInfoFile)

	err := mg.updateNonActualNUMABindingReclaimMemoryLimit(zoneInfos)
	if err != nil {
		general.ErrorS(err, "Update non-actual numa binding reclaim memory limit failed")
		return err
	}

	err = mg.updateActualNUMABindingReclaimMemoryLimit(zoneInfos)
	if err != nil {
		general.ErrorS(err, "Update actual numa binding reclaim memory limit failed")
		return err
	}

	mg.reconcileStatus.Store(reconcileStatusSucceeded)

	return nil
}

func (mg *memoryGuard) GetAdvices() types.InternalMemoryCalculationResult {
	if mg.reconcileStatus.Load() == reconcileStatusFailed {
		general.Errorf("failed to get last reconcile result")
		return types.InternalMemoryCalculationResult{}
	}
	memoryMax := mg.reclaimMemoryLimit.Load()
	memoryHigh := int64(float64(memoryMax) * memoryHighScaleFactor)
	if memoryMax == reclaimMemoryUnlimited {
		memoryHigh = reclaimMemoryUnlimited
	}
	result := types.InternalMemoryCalculationResult{}
	for _, cgroupPath := range mg.reclaimRelativeRootCgroupPaths {
		result.ExtraEntries = append(result.ExtraEntries, types.ExtraMemoryAdvices{
			CgroupPath: cgroupPath,
			Values: map[string]string{
				string(memoryadvisor.ControlKnobKeyMemoryLimitInBytes): strconv.FormatInt(memoryMax, 10),
				string(memoryadvisor.ControlKnobKeyMemoryHigh):         strconv.FormatInt(memoryHigh, 10),
			},
		})
	}

	numaBindingReclaimMemoryLimitValue := mg.numaBindingReclaimMemoryLimit.Load()
	if numaBindingReclaimMemoryLimitValue != nil {
		perNUMA := numaBindingReclaimMemoryLimitValue.(map[int]map[string]int64)
		for numaID, cgroupPaths := range mg.numaBindingRelativeRootCgroupPaths {
			perPath, ok := perNUMA[numaID]
			if !ok {
				continue
			}
			for _, cgroupPath := range cgroupPaths {
				numaMemoryMax, ok := perPath[cgroupPath]
				if !ok {
					continue
				}
				numaMemoryHigh := int64(float64(numaMemoryMax) * memoryHighScaleFactor)
				if numaMemoryMax == reclaimMemoryUnlimited {
					numaMemoryHigh = reclaimMemoryUnlimited
				}
				result.ExtraEntries = append(result.ExtraEntries, types.ExtraMemoryAdvices{
					CgroupPath: cgroupPath,
					Values: map[string]string{
						string(memoryadvisor.ControlKnobKeyMemoryLimitInBytes): strconv.FormatInt(numaMemoryMax, 10),
						string(memoryadvisor.ControlKnobKeyMemoryHigh):         strconv.FormatInt(numaMemoryHigh, 10),
					},
				})
			}
		}
	}

	return result
}

func (mg *memoryGuard) calculateReclaimedMemoryLimitFor(numaID int, reclaimedCgroupPaths []string, zoneInfos []machine.NormalZoneInfo) (float64, error) {
	watermarkScaleFactor, err := mg.metaServer.GetNodeMetric(consts.MetricMemScaleFactorSystem)
	if err != nil {
		general.ErrorS(err, "Can not get system watermark scale factor")
		return 0, err
	}

	reclaimedCoresUsed := .0
	for _, p := range reclaimedCgroupPaths {
		if !general.IsPathExists(common.GetAbsCgroupPath(common.DefaultSelectedSubsys, p)) {
			continue
		}
		m, err := mg.metaServer.GetCgroupNumaMetric(p, numaID, consts.MetricsMemTotalPerNumaCgroup)
		if err != nil {
			return 0, err
		}
		reclaimedCoresUsed += m.Value
	}

	tmp, err := mg.metaServer.GetNumaMetric(numaID, consts.MetricMemTotalNuma)
	if err != nil {
		return 0, err
	}
	numaTotal := tmp.Value

	tmp, err = mg.metaServer.GetNumaMetric(numaID, consts.MetricMemFreeNuma)
	if err != nil {
		return 0, err
	}
	numaFree := tmp.Value

	criticalWatermark := numaTotal * watermarkScaleFactor.Value / float64(10000)

	var zoneInfo machine.NormalZoneInfo
	found := false
	for _, z := range zoneInfos {
		if z.Node == int64(numaID) {
			zoneInfo = z
			found = true
			break
		}
	}
	if found {
		numaFree = float64(zoneInfo.Free) * float64(mg.metaServer.KatalystMachineInfo.PageSize)
		watermarkPages := zoneInfo.Low
		if mg.conf.CriticalWatermarkSource == "high" {
			watermarkPages = zoneInfo.High
		}
		criticalWatermark = float64(watermarkPages) * float64(mg.metaServer.KatalystMachineInfo.PageSize)
	}

	criticalWatermarkScaleFactor := mg.conf.GetDynamicConfiguration().CriticalWatermarkScaleFactor
	criticalWatermark *= criticalWatermarkScaleFactor

	criticalWatermark = math.Max(float64(mg.minCriticalWatermark), criticalWatermark)
	reclaimMemoryLimit := reclaimedCoresUsed +
		math.Max(numaFree-criticalWatermark, 0)

	reclaimedMemoryMaxRatio := mg.conf.GetDynamicConfiguration().ReclaimedMemoryMaxRatio
	if reclaimedMemoryMaxRatio > 0 {
		reclaimMemoryLimit = math.Min(reclaimMemoryLimit, reclaimedMemoryMaxRatio*numaTotal)
	}

	general.InfoS("NUMA memory info", "numaID", numaID,
		"criticalWatermark", general.FormatMemoryQuantity(criticalWatermark),
		"reclaimedCoresUsed", general.FormatMemoryQuantity(reclaimedCoresUsed),
		"numaFree", general.FormatMemoryQuantity(numaFree),
		"criticalWatermarkScaleFactor", criticalWatermarkScaleFactor,
		"reclaimedMemoryMaxRatio", reclaimedMemoryMaxRatio,
		"reclaimMemoryLimit", general.FormatMemoryQuantity(reclaimMemoryLimit),
		"zoneInfo", zoneInfo, "found", found)
	return reclaimMemoryLimit, nil
}

func (mg *memoryGuard) updateNonActualNUMABindingReclaimMemoryLimit(zoneInfos []machine.NormalZoneInfo) error {
	reclaimMemoryLimit := .0
	availNUMAs, _, err := helper.GetAvailableNUMAsAndReclaimedCores(mg.conf, mg.metaReader, mg.metaServer)
	if err != nil {
		return err
	}

	actualNUMABindingNUMAs, err := helper.GetActualNUMABindingNUMAsForReclaimedCores(mg.metaReader)
	if err != nil {
		return err
	}

	// Charge the reclaimed_cores usage-baseline for every configured parent
	// cgroup on this NUMA; the NUMA-level free-memory cushion is shared
	// across parents and counted once per NUMA by the helper.
	for _, numaID := range availNUMAs.Difference(actualNUMABindingNUMAs).ToSliceInt() {
		limit, err := mg.calculateReclaimedMemoryLimitFor(numaID, mg.reclaimRelativeRootCgroupPaths, zoneInfos)
		if err != nil {
			return err
		}

		reclaimMemoryLimit += limit
	}

	mg.reclaimMemoryLimit.Store(int64(reclaimMemoryLimit))
	return nil
}

func (mg *memoryGuard) updateActualNUMABindingReclaimMemoryLimit(zoneInfos []machine.NormalZoneInfo) error {
	limits := make(map[int]map[string]int64, len(mg.metaServer.Topology))

	for _, numaID := range mg.metaServer.CPUDetails.NUMANodes().ToSliceNoSortInt() {
		perPath := make(map[string]int64, len(mg.numaBindingRelativeRootCgroupPaths[numaID]))
		for _, cgroupPath := range mg.numaBindingRelativeRootCgroupPaths[numaID] {
			if !general.IsPathExists(common.GetAbsCgroupPath(common.DefaultSelectedSubsys, cgroupPath)) {
				continue
			}

			limit, err := mg.calculateReclaimedMemoryLimitFor(numaID, []string{cgroupPath}, zoneInfos)
			if err != nil {
				return err
			}

			perPath[cgroupPath] = int64(limit)
		}
		if len(perPath) > 0 {
			limits[numaID] = perPath
		}
	}

	mg.numaBindingReclaimMemoryLimit.Store(limits)
	return nil
}

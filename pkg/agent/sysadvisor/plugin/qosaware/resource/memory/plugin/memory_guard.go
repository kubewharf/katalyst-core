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
	"fmt"
	"math"
	"strconv"

	"go.uber.org/atomic"

	configv1alpha1 "github.com/kubewharf/katalyst-api/pkg/apis/config/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/memory/dynamicpolicy/memoryadvisor"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/qosaware/resource/helper"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/config"
	dynamicconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/cgroup/common"
	cgroupmgr "github.com/kubewharf/katalyst-core/pkg/util/cgroup/manager"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
	"github.com/kubewharf/katalyst-core/pkg/util/reclaim"
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

	// existingCgroupPathsFn filters reclaimed cgroup paths to those materialized
	// on the host. When nil, common.GetExistingRelativeCgroupPaths is used.
	// Tests inject a fake to avoid patching the global cgroupfs probe.
	existingCgroupPathsFn func(paths ...string) []string
	// zoneInfoFn reads /proc/zoneinfo. When nil, machine.GetNormalZoneInfo is
	// used. Tests inject a fake to avoid patching the global host probe.
	zoneInfoFn func(file string) []machine.NormalZoneInfo
	// numaMemoryUsageFn reads hierarchical per-NUMA usage in bytes. When nil,
	// cgroupmgr.GetHierarchicalNumaMemoryUsageWithRelativePath is used.
	numaMemoryUsageFn func(path string, pageSize uint64) (map[int]uint64, error)
}

func (mg *memoryGuard) materializedCgroupPaths(paths ...string) []string {
	if mg.existingCgroupPathsFn != nil {
		return mg.existingCgroupPathsFn(paths...)
	}
	return common.GetExistingRelativeCgroupPaths(paths...)
}

func (mg *memoryGuard) getHierarchicalNumaMemoryUsage(path string) (map[int]uint64, error) {
	fn := mg.numaMemoryUsageFn
	if fn == nil {
		fn = cgroupmgr.GetHierarchicalNumaMemoryUsageWithRelativePath
	}
	return fn(path, uint64(mg.metaServer.KatalystMachineInfo.PageSize))
}

// SetNumaMemoryUsageFnForTest overrides the direct cgroupfs usage reader. Test-only.
func (mg *memoryGuard) SetNumaMemoryUsageFnForTest(fn func(string, uint64) (map[int]uint64, error)) {
	mg.numaMemoryUsageFn = fn
}

// SetReclaimPathsForTest overrides the reclaim cgroup paths the guard iterates.
// Test-only: production paths come from reclaim.AggregateCgroupPaths at NewMemoryGuard.
func (mg *memoryGuard) SetReclaimPathsForTest(parent []string, numaBinding map[int][]string) {
	mg.reclaimRelativeRootCgroupPaths = parent
	mg.numaBindingRelativeRootCgroupPaths = numaBinding
}

// SetZoneInfoFnForTest overrides the /proc/zoneinfo probe. Test-only.
func (mg *memoryGuard) SetZoneInfoFnForTest(fn func(string) []machine.NormalZoneInfo) {
	mg.zoneInfoFn = fn
}

// SetExistingCgroupPathsFnForTest overrides the cgroupfs existence probe. Test-only.
func (mg *memoryGuard) SetExistingCgroupPathsFnForTest(fn func(...string) []string) {
	mg.existingCgroupPathsFn = fn
}

func NewMemoryGuard(conf *config.Configuration, extraConfig interface{}, metaReader metacache.MetaReader, metaServer *metaserver.MetaServer, emitter metrics.MetricEmitter) MemoryAdvisorPlugin {
	return &memoryGuard{
		metaReader:                         metaReader,
		metaServer:                         metaServer,
		emitter:                            emitter,
		reclaimRelativeRootCgroupPaths:     reclaim.AggregateCgroupPaths(),
		numaBindingRelativeRootCgroupPaths: reclaim.AggregateNumaBindingCgroupPaths(),
		reclaimMemoryLimit:                 atomic.NewInt64(-1),
		numaBindingReclaimMemoryLimit:      &atomic.Value{},
		reconcileStatus:                    atomic.NewString(reconcileStatusFailed),
		minCriticalWatermark:               conf.MinCriticalWatermark,
		conf:                               conf,
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

	zoneInfoFn := mg.zoneInfoFn
	if zoneInfoFn == nil {
		zoneInfoFn = machine.GetNormalZoneInfo
	}
	zoneInfos := zoneInfoFn(defaultProcZoneInfoFile)

	err := mg.updateNonActualNUMABindingReclaimMemoryLimit(dynamicConfig, zoneInfos)
	if err != nil {
		general.ErrorS(err, "Update non-actual numa binding reclaim memory limit failed")
		return err
	}

	err = mg.updateActualNUMABindingReclaimMemoryLimit(dynamicConfig, zoneInfos)
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

	result := types.InternalMemoryCalculationResult{}

	memoryMax := mg.reclaimMemoryLimit.Load()
	memoryLimits := calculateCgroupMemoryLimits(mg.reclaimRelativeRootCgroupPaths, memoryMax)
	for _, path := range mg.reclaimRelativeRootCgroupPaths {
		max := memoryLimits[path]
		high := calculateMemoryHigh(max)
		result.ExtraEntries = append(result.ExtraEntries, types.ExtraMemoryAdvices{
			CgroupPath: path,
			Values: map[string]string{
				string(memoryadvisor.ControlKnobKeyMemoryLimitInBytes): strconv.FormatInt(max, 10),
				string(memoryadvisor.ControlKnobKeyMemoryHigh):         strconv.FormatInt(high, 10),
			},
		})
	}

	numaBindingReclaimMemoryLimitValue := mg.numaBindingReclaimMemoryLimit.Load()
	if numaBindingReclaimMemoryLimitValue != nil {
		perNUMA := numaBindingReclaimMemoryLimitValue.(map[int]int64)
		for numaID, paths := range mg.numaBindingRelativeRootCgroupPaths {
			numaMemoryMax, ok := perNUMA[numaID]
			if !ok {
				continue
			}
			numaMemoryLimits := calculateCgroupMemoryLimits(paths, numaMemoryMax)
			for _, path := range paths {
				max := numaMemoryLimits[path]
				high := calculateMemoryHigh(max)
				result.ExtraEntries = append(result.ExtraEntries, types.ExtraMemoryAdvices{
					CgroupPath: path,
					Values: map[string]string{
						string(memoryadvisor.ControlKnobKeyMemoryLimitInBytes): strconv.FormatInt(max, 10),
						string(memoryadvisor.ControlKnobKeyMemoryHigh):         strconv.FormatInt(high, 10),
					},
				})
			}
		}
	}

	return result
}

func calculateMemoryHigh(limit int64) int64 {
	if limit == reclaimMemoryUnlimited {
		return reclaimMemoryUnlimited
	}
	// memory.high should track the final memory.max advice instead of the raw
	// percentage share, otherwise the soft threshold could diverge from the
	// hard limit after usage-floor adjustment.
	return int64(float64(limit) * memoryHighScaleFactor)
}

// calculateCgroupMemoryLimits applies the same memory-guard budget to every
// reclaim cgroup path in the group. Consumer percentage is intentionally not
// applied here; headroom policies account for it when aggregating the advised
// limits back into a single reclaim headroom cap.
// TODO: a node can currently safely support only one reclaim consumer. Multiple
// reclaim consumers may overuse memory because each consumer root receives the
// full memory-guard budget and there is no shared enforcement boundary.
func calculateCgroupMemoryLimits(cgroupPaths []string, totalLimit int64) map[string]int64 {
	limits := make(map[string]int64, len(cgroupPaths))
	for _, path := range cgroupPaths {
		limits[path] = totalLimit
	}

	return limits
}

func (mg *memoryGuard) calculateReclaimedMemoryLimitFor(dynamicConfig *dynamicconfig.Configuration, numaID int, reclaimedCgroupPaths []string, zoneInfos []machine.NormalZoneInfo) (float64, error) {
	if mg.metaServer == nil || mg.metaServer.KatalystMachineInfo == nil || mg.metaServer.KatalystMachineInfo.MemoryTopology == nil {
		return 0, fmt.Errorf("machine memory topology is unavailable")
	}

	pageSize := mg.metaServer.KatalystMachineInfo.PageSize
	if pageSize <= 0 {
		return 0, fmt.Errorf("invalid page size %d", pageSize)
	}

	numaTotalBytes, ok := mg.metaServer.KatalystMachineInfo.NormalMemoryDetails[numaID]
	if !ok || numaTotalBytes == 0 {
		return 0, fmt.Errorf("normal memory capacity is missing for numa %d", numaID)
	}
	numaTotal := float64(numaTotalBytes)

	var zoneInfo machine.NormalZoneInfo
	found := false
	for _, z := range zoneInfos {
		if z.Node == int64(numaID) {
			zoneInfo = z
			found = true
			break
		}
	}
	if !found {
		return 0, fmt.Errorf("normal zone info is missing for numa %d", numaID)
	}

	// filter out reclaimed cgroup paths that are not materialized on this node.
	// their per-numa counters are absent from the store, so keeping them would
	// abort the whole reconcile; the remaining parents still contribute their
	// usage baseline.
	existingCgroupPaths := mg.materializedCgroupPaths(reclaimedCgroupPaths...)

	reclaimedMemoryUsed := .0
	for _, p := range existingCgroupPaths {
		perNumaUsage, err := mg.getHierarchicalNumaMemoryUsage(p)
		if err != nil {
			return 0, err
		}
		used, ok := perNumaUsage[numaID]
		if !ok {
			return 0, fmt.Errorf("numa %d usage is missing for cgroup %s", numaID, p)
		}
		reclaimedMemoryUsed += float64(used)
	}

	numaFree := float64(zoneInfo.Free) * float64(pageSize)
	watermarkPages := getCriticalWatermarkPages(&zoneInfo, dynamicConfig.CriticalWatermarkSource)
	criticalWatermark := float64(watermarkPages) * float64(pageSize)

	criticalWatermarkScaleFactor := dynamicConfig.CriticalWatermarkScaleFactor
	criticalWatermark *= criticalWatermarkScaleFactor

	criticalWatermark = math.Max(float64(mg.minCriticalWatermark), criticalWatermark)
	rawReclaimMemoryLimit := reclaimedMemoryUsed +
		math.Max(numaFree-criticalWatermark, 0)
	reclaimMemoryLimit := rawReclaimMemoryLimit

	// clamp the reclaimMemory limit to the max ratio of total memory in this NUMA
	reclaimedMemoryMaxRatio := dynamicConfig.ReclaimedMemoryMaxRatio
	maxRatioLimit := .0
	if reclaimedMemoryMaxRatio > 0 {
		maxRatioLimit = reclaimedMemoryMaxRatio * numaTotal
		reclaimMemoryLimit = math.Min(reclaimMemoryLimit, maxRatioLimit)
	}

	usageFloor := .0
	usageFloorApplied := false
	if mg.conf.ReclaimedMemoryLimitMargin > 0 && reclaimedMemoryUsed > 0 {
		// Apply the floor after the ratio cap so active usage keeps its growth
		// margin under pressure; empty pools must not gain budget from the margin.
		usageFloor = math.Min(reclaimedMemoryUsed+float64(mg.conf.ReclaimedMemoryLimitMargin), numaTotal)
		usageFloorApplied = usageFloor > reclaimMemoryLimit
		reclaimMemoryLimit = math.Max(reclaimMemoryLimit, usageFloor)
	}

	general.InfoS("NUMA memory info", "numaID", numaID,
		"criticalWatermark", general.FormatMemoryQuantity(criticalWatermark),
		"reclaimedCoresUsed", general.FormatMemoryQuantity(reclaimedMemoryUsed),
		"numaFree", general.FormatMemoryQuantity(numaFree),
		"criticalWatermarkScaleFactor", criticalWatermarkScaleFactor,
		"reclaimedMemoryMaxRatio", reclaimedMemoryMaxRatio,
		"rawReclaimMemoryLimit", general.FormatMemoryQuantity(rawReclaimMemoryLimit),
		"maxRatioLimit", general.FormatMemoryQuantity(maxRatioLimit),
		"reclaimedMemoryLimitMargin", mg.conf.ReclaimedMemoryLimitMargin,
		"usageFloor", general.FormatMemoryQuantity(usageFloor),
		"usageFloorApplied", usageFloorApplied,
		"maxRatioOverridden", reclaimedMemoryMaxRatio > 0 && usageFloor > maxRatioLimit,
		"reclaimMemoryLimit", general.FormatMemoryQuantity(reclaimMemoryLimit),
		"zoneInfo", zoneInfo, "found", found)
	return reclaimMemoryLimit, nil
}

func getCriticalWatermarkPages(zoneInfo *machine.NormalZoneInfo, source configv1alpha1.CriticalWatermarkSource) uint64 {
	if source == configv1alpha1.CriticalWatermarkSourceHigh {
		return zoneInfo.High
	}
	return zoneInfo.Low
}

func (mg *memoryGuard) updateNonActualNUMABindingReclaimMemoryLimit(dynamicConfig *dynamicconfig.Configuration, zoneInfos []machine.NormalZoneInfo) error {
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
		limit, err := mg.calculateReclaimedMemoryLimitFor(dynamicConfig, numaID, mg.reclaimRelativeRootCgroupPaths, zoneInfos)
		if err != nil {
			return err
		}

		reclaimMemoryLimit += limit
	}

	mg.reclaimMemoryLimit.Store(int64(reclaimMemoryLimit))
	return nil
}

func (mg *memoryGuard) updateActualNUMABindingReclaimMemoryLimit(dynamicConfig *dynamicconfig.Configuration, zoneInfos []machine.NormalZoneInfo) error {
	limits := make(map[int]int64, len(mg.metaServer.Topology))

	for _, numaID := range mg.metaServer.CPUDetails.NUMANodes().ToSliceNoSortInt() {
		cgroupPaths := mg.numaBindingRelativeRootCgroupPaths[numaID]
		if len(cgroupPaths) == 0 {
			continue
		}

		limit, err := mg.calculateReclaimedMemoryLimitFor(dynamicConfig, numaID, cgroupPaths, zoneInfos)
		if err != nil {
			return err
		}

		limits[numaID] = int64(limit)
	}

	mg.numaBindingReclaimMemoryLimit.Store(limits)
	return nil
}

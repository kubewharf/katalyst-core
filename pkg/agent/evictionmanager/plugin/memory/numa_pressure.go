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

package memory

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"sync"
	"time"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/client-go/tools/events"

	pluginapi "github.com/kubewharf/katalyst-api/pkg/protocol/evictionplugin/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/agent/evictionmanager/plugin"
	"github.com/kubewharf/katalyst-core/pkg/client"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic"
	"github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric/helper"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
	"github.com/kubewharf/katalyst-core/pkg/util/native"
	"github.com/kubewharf/katalyst-core/pkg/util/process"
)

const (
	EvictionPluginNameNumaMemoryPressure = "numa-memory-pressure-eviction-plugin"
	EvictionScopeNumaMemory              = "NumaMemory"
)

const (
	memLowReservedSize = 4 * 1024 * 1024 * 1024 // 4G
	memGapSize         = 1 * 1024 * 1024 * 1024 // 1G
	memGuardSize       = 256 * 1024 * 1024      // 256M

	minDuration = 2
)

var hostZoneInfoFile = "/proc/zoneinfo"

// NewNumaMemoryPressureEvictionPlugin returns a new MemoryPressureEvictionPlugin
func NewNumaMemoryPressureEvictionPlugin(_ *client.GenericClientSet, _ events.EventRecorder,
	metaServer *metaserver.MetaServer, emitter metrics.MetricEmitter, conf *config.Configuration,
) plugin.EvictionPlugin {
	return &NumaMemoryPressurePlugin{
		pluginName:                     EvictionPluginNameNumaMemoryPressure,
		emitter:                        emitter,
		StopControl:                    process.NewStopControl(time.Time{}),
		metaServer:                     metaServer,
		dynamicConfig:                  conf.DynamicAgentConfiguration,
		reclaimedPodFilter:             conf.CheckReclaimedQoSForPod,
		numaActionMap:                  make(map[int]int),
		numaFreeBelowWatermarkTimesMap: make(map[int]int),
		numaWatermarkSnapshotMap:       make(map[int]*numaWatermarkSnapshot),
		evictionHelper:                 NewEvictionHelper(emitter, metaServer, conf),

		memLowReservePages: uint64(metaServer.KatalystMachineInfo.MemoryTopology.AlignToPageSize(memLowReservedSize)),
		memGapPages:        uint64(metaServer.KatalystMachineInfo.MemoryTopology.AlignToPageSize(memGapSize)),
		memGuardPages:      uint64(metaServer.KatalystMachineInfo.MemoryTopology.AlignToPageSize(memGuardSize)),
	}
}

// numaWatermarkSnapshot captures, per NUMA, the watermark values (in pages) read
// during the most recent ThresholdMet detection. It is shared with
// GetTopEvictionPods (under the same lock) so the reclaimed-memory safe-zone gate
// can apply the danger bypass against a consistent snapshot instead of re-reading
// /proc/zoneinfo on a later call.
type numaWatermarkSnapshot struct {
	// valid reports whether free/min/low were obtained for this NUMA this cycle.
	// When a NUMA has no snapshot (e.g. watermark metric fetch failed) the gate
	// fails closed.
	valid bool
	free  int
	min   int
	low   int
}

// NumaMemoryPressurePlugin implements the EvictionPlugin interface
// It triggers pod eviction based on the numa node pressure
type NumaMemoryPressurePlugin struct {
	*process.StopControl
	sync.Mutex

	emitter            metrics.MetricEmitter
	reclaimedPodFilter func(pod *v1.Pod) (bool, error)
	pluginName         string
	metaServer         *metaserver.MetaServer
	evictionHelper     *EvictionHelper

	dynamicConfig *dynamic.DynamicAgentConfiguration

	numaActionMap                  map[int]int
	numaFreeBelowWatermarkTimesMap map[int]int
	numaWatermarkSnapshotMap       map[int]*numaWatermarkSnapshot
	isUnderNumaPressure            bool

	// guard pages for numa.mem.pressure detection
	memLowReservePages uint64 // minimum allowed mem.low
	memGapPages        uint64 // the depth of the probe, used to judge the pressure
	memGuardPages      uint64 // a buffer depth to prevent kswpad ping-pong
}

func (n *NumaMemoryPressurePlugin) Start() {
	general.RegisterHeartbeatCheck(EvictionPluginNameNumaMemoryPressure, healthCheckTimeout, general.HealthzCheckStateNotReady, healthCheckTimeout)
	return
}

func (n *NumaMemoryPressurePlugin) Name() string {
	if n == nil {
		return ""
	}

	return n.pluginName
}

func (n *NumaMemoryPressurePlugin) ThresholdMet(_ context.Context, _ *pluginapi.GetThresholdMetRequest) (*pluginapi.ThresholdMetResponse, error) {
	var err error
	defer func() {
		_ = general.UpdateHealthzStateByError(EvictionPluginNameNumaMemoryPressure, err)
	}()

	resp := &pluginapi.ThresholdMetResponse{
		MetType: pluginapi.ThresholdMetType_NOT_MET,
	}

	if !n.dynamicConfig.GetDynamicConfiguration().EnableNumaLevelEviction {
		return resp, nil
	}

	// ThresholdMet and GetTopEvictionPods read/write the same action/snapshot
	// state; guard both so the per-NUMA watermark snapshot stays consistent.
	n.Lock()
	defer n.Unlock()

	err = n.detectNumaPressures()
	if n.isUnderNumaPressure {
		resp = &pluginapi.ThresholdMetResponse{
			MetType:       pluginapi.ThresholdMetType_HARD_MET,
			EvictionScope: EvictionScopeNumaMemory,
		}
	}

	return resp, nil
}

func (n *NumaMemoryPressurePlugin) detectNumaPressures() error {
	var errList []error
	n.isUnderNumaPressure = false
	// Rebuild the watermark snapshot each detection cycle so stale entries from a
	// NUMA that vanished or failed to fetch this round cannot leak into the gate.
	n.numaWatermarkSnapshotMap = make(map[int]*numaWatermarkSnapshot)

	// step1, check zoneinfo
	zoneinfo := machine.GetNormalZoneInfo(hostZoneInfoFile)
	for _, numaID := range n.metaServer.CPUDetails.NUMANodes().ToSliceNoSortInt() {
		n.numaActionMap[numaID] = actionNoop
		if _, ok := n.numaFreeBelowWatermarkTimesMap[numaID]; !ok {
			n.numaFreeBelowWatermarkTimesMap[numaID] = 0
		}

		// Notice: the unit of free/min/low from zoneinfo is pages.
		if numaID < len(zoneinfo) && zoneinfo[numaID].Node == int64(numaID) {
			low := zoneinfo[numaID].Low
			min := zoneinfo[numaID].Min
			fileInactive := zoneinfo[numaID].FileInactive

			// Notice: adding a buffer range for mem.low/mem.min to avoid kswapd ping-pong
			low += n.memGuardPages
			min += n.memGapPages

			// step2, add a compensation mechanism to prevent system thrashing due to insufficient file memory
			fileReserved := general.Max(int(n.memLowReservePages), int(low))
			if fileInactive < uint64(fileReserved) {
				tmp := low + n.memGapPages
				low = uint64(general.Max(int(n.memLowReservePages), int(tmp)))
			}

			// step3, compare mem.free, mem.low, mem.min
			if err := n.detectNumaWatermarkPressure(numaID, int(zoneinfo[numaID].Free), int(min), int(low)); err != nil {
				errList = append(errList, err)
				continue
			}
		} else {
			// If there is no watermark info in zoneinfo, fall back to applying info from metaServer
			if err := n.detectNumaWatermarkPressure(numaID, 0, 0, 0); err != nil {
				errList = append(errList, err)
				continue
			}
		}
	}

	return errors.NewAggregate(errList)
}

func (n *NumaMemoryPressurePlugin) isUnderAdditionalPressure(free, min, low int) bool {
	return free < (min+low)/2 || (low > int(n.memGapPages) && free < (low-int(n.memGapPages)))
}

func (n *NumaMemoryPressurePlugin) detectNumaWatermarkPressure(numaID, free, min, low int) error {
	if low <= 0 {
		// If there is no watermark info in zoneinfo, fall back to applying info from metaServer
		// Notice: the unit of freeMem here is bytes.
		freeBytes, totalBytes, scaleFactor, err := helper.GetWatermarkMetrics(n.metaServer.MetricsFetcher, n.emitter, numaID)
		if err != nil {
			general.Errorf("failed to getWatermarkMetrics for numa %d, err: %v", numaID, err)
			_ = n.emitter.StoreInt64(metricsNameFetchMetricError, 1, metrics.MetricTypeNameCount,
				metrics.ConvertMapToTags(map[string]string{
					metricsTagKeyNumaID: strconv.Itoa(numaID),
				})...)
			return err
		}
		free = general.ConvertBytesToPages(int(freeBytes))
		low = general.ConvertBytesToPages(int(totalBytes * scaleFactor / 10000))
	}

	// Snapshot the finalized watermarks (pages) so the reclaimed-memory
	// safe-zone gate in GetTopEvictionPods can apply the danger bypass on a
	// consistent view. A fetch failure above returns before this point, leaving
	// NUMA without a snapshot so the gate fails closed.
	n.numaWatermarkSnapshotMap[numaID] = &numaWatermarkSnapshot{
		valid: true,
		free:  free,
		min:   min,
		low:   low,
	}

	dynamicConfig := n.dynamicConfig.GetDynamicConfiguration()
	_ = n.emitter.StoreFloat64(metricsNameNumaMetric, float64(n.numaFreeBelowWatermarkTimesMap[numaID]), metrics.MetricTypeNameRaw,
		metrics.ConvertMapToTags(map[string]string{
			metricsTagKeyNumaID:     strconv.Itoa(numaID),
			metricsTagKeyMetricName: metricsTagValueNumaFreeBelowWatermarkTimes,
		})...)

	// Notice:
	// numaFreeBelowWatermarkTimes > NumaFreeBelowWatermarkTimesReclaimedThreshold, trigger actionReclaimedEviction
	// numaFreeBelowWatermarkTimes > NumaFreeBelowWatermarkTimesThreshold, trigger actionEviction
	// By default, the reclaimed threshold is half non-reclaimed.
	actionReclaimedThreshold := dynamicConfig.NumaFreeBelowWatermarkTimesReclaimedThreshold
	actionNonReclaimedThreshold := dynamicConfig.NumaFreeBelowWatermarkTimesThreshold

	if free < min {
		// We are under a DANGEROUS situation, NEED A EVICTION NOW! IMMEDIATELY!
		n.isUnderNumaPressure = true
		n.numaActionMap[numaID] = actionReclaimedEviction
		if n.numaFreeBelowWatermarkTimesMap[numaID] < actionNonReclaimedThreshold {
			n.numaFreeBelowWatermarkTimesMap[numaID] = actionNonReclaimedThreshold - dynamicConfig.NumaFreeConstraintFastEvictionWaitCycle
		}
	} else if free < low {
		n.numaFreeBelowWatermarkTimesMap[numaID]++

		// avoid excessive pressure on LRU spinlock in kswapd
		if n.numaFreeBelowWatermarkTimesMap[numaID] > minDuration && n.isUnderAdditionalPressure(free, min, low) {
			n.numaFreeBelowWatermarkTimesMap[numaID]++
		}

		// Currently, the default value for NumaFreeBelowWatermarkTimesThreshold is 20,
		// with a default periodic check interval of 5 seconds.
		// This means if the free memory remains below the low watermark for 50 seconds, actionReclaimedEviction will be triggered.
		// If the condition persists for 100 seconds, actionEviction will be triggered.
		if n.numaFreeBelowWatermarkTimesMap[numaID] > actionReclaimedThreshold {
			n.isUnderNumaPressure = true
			n.numaActionMap[numaID] = actionReclaimedEviction
		}
	} else {
		// added cooling mechanism to avoid kswapd ping-pong
		if n.numaFreeBelowWatermarkTimesMap[numaID] > 0 {
			n.numaFreeBelowWatermarkTimesMap[numaID]--
			if n.numaFreeBelowWatermarkTimesMap[numaID] > actionReclaimedThreshold {
				n.numaFreeBelowWatermarkTimesMap[numaID] = actionReclaimedThreshold
			}
		}
	}
	if n.numaFreeBelowWatermarkTimesMap[numaID] >= actionNonReclaimedThreshold {
		n.numaActionMap[numaID] = actionEviction
	}

	general.Infof("numa ID: %d, "+
		"free pages: %+v, min: %+v, low: %+v, belowTimes: %+v, numaReclaimedThreshold: %+v, numaNonReclaimedThreshold: %+v",
		numaID, free, min, low, n.numaFreeBelowWatermarkTimesMap[numaID],
		actionReclaimedThreshold, actionNonReclaimedThreshold)

	switch n.numaActionMap[numaID] {
	case actionReclaimedEviction:
		_ = n.emitter.StoreInt64(metricsNameThresholdMet, 1, metrics.MetricTypeNameCount,
			metrics.ConvertMapToTags(map[string]string{
				metricsTagKeyEvictionScope:  EvictionScopeNumaMemory,
				metricsTagKeyDetectionLevel: metricsTagValueDetectionLevelNuma,
				metricsTagKeyNumaID:         strconv.Itoa(numaID),
				metricsTagKeyAction:         metricsTagValueActionReclaimedEviction,
			})...)
	case actionEviction:
		_ = n.emitter.StoreInt64(metricsNameThresholdMet, 1, metrics.MetricTypeNameCount,
			metrics.ConvertMapToTags(map[string]string{
				metricsTagKeyEvictionScope:  EvictionScopeNumaMemory,
				metricsTagKeyDetectionLevel: metricsTagValueDetectionLevelNuma,
				metricsTagKeyNumaID:         strconv.Itoa(numaID),
				metricsTagKeyAction:         metricsTagValueActionEviction,
			})...)
	}

	return nil
}

func (n *NumaMemoryPressurePlugin) GetTopEvictionPods(_ context.Context, request *pluginapi.GetTopEvictionPodsRequest) (*pluginapi.GetTopEvictionPodsResponse, error) {
	if request == nil {
		return nil, fmt.Errorf("GetTopEvictionPods got nil request")
	}

	if len(request.ActivePods) == 0 {
		general.Warningf("GetTopEvictionPods got empty active pods list")
		return &pluginapi.GetTopEvictionPodsResponse{}, nil
	}

	dynamicConfig := n.dynamicConfig.GetDynamicConfiguration()
	targetPods := make([]*v1.Pod, 0, len(request.ActivePods))
	podToEvictMap := make(map[string]*v1.Pod)

	// Snapshot the per-NUMA action and watermark state under lock, then release
	// before computing the reclaimed-usage ratio / selecting pods (which call
	// into the MetricsFetcher). This keeps ThresholdMet and GetTopEvictionPods
	// consistent without holding the lock across slow fetcher calls.
	n.Lock()
	type numaPlanItem struct {
		action int
		snap   *numaWatermarkSnapshot
	}
	plan := make(map[int]numaPlanItem, len(n.numaActionMap))
	for numaID, action := range n.numaActionMap {
		item := numaPlanItem{action: action}
		if action == actionReclaimedEviction {
			if s, ok := n.numaWatermarkSnapshotMap[numaID]; ok && s != nil {
				copied := *s
				item.snap = &copied
			}
		}
		plan[numaID] = item
	}
	underPressure := n.isUnderNumaPressure
	n.Unlock()

	general.Infof("GetTopEvictionPods condition, isUnderNumaPressure: %+v, plan: %+v",
		underPressure, plan)

	if dynamicConfig.EnableNumaLevelEviction && underPressure {
		for numaID, item := range plan {
			action := item.action
			// The reclaimed-memory safe-zone gate only applies to
			// actionReclaimedEviction. actionEviction (evicting all pods) and
			// actionNoop are unaffected.
			if action == actionReclaimedEviction {
				skip, reason := n.decideReclaimedMemorySafeZoneGate(numaID, item.snap, request.ActivePods)
				if skip {
					general.Infof("reclaimed-memory safe-zone gate skipped actionReclaimedEviction on numa %d: %s", numaID, reason)
					continue
				}
			}

			candidates := n.evictionHelper.getCandidates(request.ActivePods, numaID, dynamicConfig.NumaVictimMinimumUtilizationThreshold)
			n.evictionHelper.selectTopNPodsToEvictByMetrics(candidates, request.TopN, numaID, action,
				dynamicConfig.NumaEvictionRankingMetrics, podToEvictMap)
		}
	}

	for uid := range podToEvictMap {
		targetPods = append(targetPods, podToEvictMap[uid])
	}

	_ = n.emitter.StoreInt64(metricsNameNumberOfTargetPods, int64(len(targetPods)), metrics.MetricTypeNameRaw)
	general.Infof("[numa-memory-pressure-eviction-plugin] GetTopEvictionPods result, targetPods: %+v", native.GetNamespacedNameListFromSlice(targetPods))

	resp := &pluginapi.GetTopEvictionPodsResponse{
		TargetPods: targetPods,
	}

	// TODO: different grace time for different evictions.
	// Currently, grace time is aligned with actionEviction in the case of mixed evictions.
	gracePeriod := dynamicConfig.MemoryPressureEvictionConfiguration.ReclaimedGracePeriod
	for _, item := range plan {
		if item.action == actionEviction {
			gracePeriod = dynamicConfig.MemoryPressureEvictionConfiguration.GracePeriod
			break
		}
	}

	if gracePeriod > 0 {
		resp.DeletionOptions = &pluginapi.DeletionOptions{
			GracePeriodSeconds: gracePeriod,
		}
	}
	return resp, nil
}

// validSafeZoneThreshold reports whether r is a finite fraction within the
// documented [0,1] range. NaN, Inf and out-of-range values are invalid and make
// the gate fail closed rather than being silently armed.
func validSafeZoneThreshold(r float64) bool {
	return !math.IsNaN(r) && !math.IsInf(r, 0) && r >= 0 && r <= 1
}

// decideReclaimedMemorySafeZoneGate evaluates the reclaimed-memory safe-zone
// gate for a NUMA whose raw action is actionReclaimedEviction. It returns
// whether the reclaimed eviction must be skipped (i.e. deferred to the regular
// path), together with a fixed low-cardinality reason used for logging and
// metrics.
//
// The state machine keys off (EnableReclaimedMemorySafeZoneGate,
// ReclaimedMemoryUsageRatioThreshold) and maps to a fixed low-cardinality
// gate_state tag:
//   - invalid threshold (NaN / Inf / outside [0,1]): closed. The gate fails
//     closed, no skip, gate_state=closed, reason=invalid_ratio. This is a
//     defensive re-check at evaluation; ApplyConfiguration already normalizes
//     invalid values to 0.0.
//   - ratioThreshold <= 0:                 closed. The safe zone is not even
//     evaluated: no auto-shadow observation, no would-skip decision, and no
//     skip. gate_state=closed, reason=closed_zero_ratio. This is the default
//     (dynamic ratio defaults to 0.0) and preserves legacy behavior for old
//     CRs that do not opt in.
//   - ratioThreshold > 0 && !enable:      auto_shadow. The safe-zone decision is
//     evaluated but never applied; a would-skip low-ratio hit is recorded as
//     auto_shadow_would_skip, skip=false. A nil CR field resolves to false, so
//     an omitted / old CR lands here (observe-only).
//   - ratioThreshold > 0 && enable:        enabled. The only arm that really
//     skips, on a low reclaimed-usage ratio (low_reclaimed_usage).
//
// There is no "unknown" arm: the arm is a plain bool, so there is no mode
// string to misparse. Regardless of arm, the following always fall back to the
// regular eviction (skip=false): missing watermark snapshot
// (watermark_unavailable), danger watermarks (free<min /
// additional_pressure), and metric / QoS errors (metric_unavailable /
// qos_classification_failed). belowTimes keeps advancing in ThresholdMet
// regardless, so a skip is bounded and the actionEviction backstop is
// untouched.
func (n *NumaMemoryPressurePlugin) decideReclaimedMemorySafeZoneGate(numaID int, snap *numaWatermarkSnapshot, activePods []*v1.Pod) (skip bool, reason string) {
	memConfig := n.dynamicConfig.GetDynamicConfiguration().MemoryPressureEvictionConfiguration
	enable := memConfig.EnableReclaimedMemorySafeZoneGate
	ratioThreshold := memConfig.ReclaimedMemoryUsageRatioThreshold

	// Defensive fail-closed re-validation at evaluation: a non-finite or
	// out-of-[0,1] threshold must never arm the gate. This cannot panic on NaN
	// comparisons and always returns skip=false.
	if !validSafeZoneThreshold(ratioThreshold) {
		n.emitSafeZoneGateDecision(numaID, metricsTagValueGateStateClosed, false, "invalid_ratio")
		return false, "invalid_ratio"
	}

	// ratioThreshold <= 0 closes the gate completely. There is no safe-zone
	// evaluation, no would-skip observation, and no actual skip; this is the
	// no-opt-in / explicit-zero path.
	if ratioThreshold == 0 {
		n.emitSafeZoneGateDecision(numaID, metricsTagValueGateStateClosed, false, "closed_zero_ratio")
		return false, "closed_zero_ratio"
	}

	// Pick the arm: a positive ratio arms either auto-shadow (observe-only) or
	// enabled (may really skip). enable defaults to false (nil CR -> false).
	state := metricsTagValueGateStateAutoShadow
	if enable {
		state = metricsTagValueGateStateEnabled
	}

	wouldSkip, reason := n.evaluateSafeZoneSkip(numaID, snap, activePods, ratioThreshold)
	if !enable {
		// Auto-shadow: evaluate but never skip. Rename a would-skip low-ratio hit
		// so the metric/reason reflects that it was only observed; every other
		// outcome (danger bypass, metric/QoS failure) keeps its own reason.
		if wouldSkip {
			reason = "auto_shadow_would_skip"
		}
		n.emitSafeZoneGateDecision(numaID, state, false, reason)
		return false, reason
	}

	n.emitSafeZoneGateDecision(numaID, state, wouldSkip, reason)
	return wouldSkip, reason
}

// evaluateSafeZoneSkip performs the pure safe-zone attribution evaluation shared
// by the enabled and auto-shadow (disabled) arms. Every failure path returns
// skip=false.
func (n *NumaMemoryPressurePlugin) evaluateSafeZoneSkip(numaID int, snap *numaWatermarkSnapshot, activePods []*v1.Pod, ratioThreshold float64) (skip bool, reason string) {
	// Without a valid watermark snapshot we cannot attribute; fall back to eviction.
	if snap == nil || !snap.valid {
		return false, "watermark_unavailable"
	}

	// Danger watermarks always bypass the skip: an immediate eviction must not be delayed.
	if snap.free < snap.min {
		return false, "free_below_min"
	}
	if n.isUnderAdditionalPressure(snap.free, snap.min, snap.low) {
		return false, "additional_pressure"
	}

	ratio, errReason, err := n.getReclaimedNumaUsageRatio(numaID, activePods)
	if err != nil {
		general.Errorf("failed to get reclaimed numa usage ratio for numa %d, err: %v", numaID, err)
		// Gate fetch / QoS errors are recorded on the dedicated bounded gate
		// metric by the caller's emitSafeZoneGateDecision; they are NOT routed
		// to the shared generic fetch_metric_error_count.
		return false, errReason
	}

	if ratio < ratioThreshold {
		return true, "low_reclaimed_usage"
	}
	return false, "reclaimed_usage_not_low"
}

// getReclaimedNumaUsageRatio returns the fraction of a NUMA's total memory
// (mem.total.numa) currently held by reclaimed_cores pods on that NUMA (sum of
// mem.total.numa.container over reclaimed pods), computed from the existing
// MetricsFetcher. The denominator is NUMA total memory (mem.total.numa), not a
// free/available-memory estimate.
//
// Any failure makes the whole attribution fail (so the caller falls back to the
// regular eviction rather than guessing): an unreadable NUMA total or a
// reclaimed pod missing its per-NUMA metric yields errReason "metric_unavailable";
// a QoS classification error yields "qos_classification_failed".
func (n *NumaMemoryPressurePlugin) getReclaimedNumaUsageRatio(numaID int, activePods []*v1.Pod) (ratio float64, errReason string, err error) {
	total, err := helper.GetNumaMetric(n.metaServer.MetricsFetcher, n.emitter, consts.MetricMemTotalNuma, numaID)
	if err != nil {
		return 0, "metric_unavailable", err
	}
	if total <= 0 {
		return 0, "metric_unavailable", fmt.Errorf("numa %d total memory (mem.total.numa) is non-positive: %v", numaID, total)
	}

	var reclaimedUsed float64
	for _, pod := range activePods {
		if pod == nil {
			continue
		}
		isReclaimed, err := n.reclaimedPodFilter(pod)
		if err != nil {
			return 0, "qos_classification_failed", fmt.Errorf("classify reclaimed qos for pod %s/%s: %w", pod.Namespace, pod.Name, err)
		}
		if !isReclaimed {
			continue
		}
		used, err := helper.GetPodMetric(n.metaServer.MetricsFetcher, n.emitter, pod, consts.MetricsMemTotalPerNumaContainer, numaID)
		if err != nil {
			// A reclaimed pod missing its per-NUMA metric makes the attribution
			// fail rather than being silently treated as zero.
			return 0, "metric_unavailable", err
		}
		reclaimedUsed += used
	}

	return reclaimedUsed / total, "", nil
}

// emitSafeZoneGateDecision records one data point for the reclaimed-memory
// safe-zone gate on its dedicated bounded metric. State, decision and reason
// values are deliberately from small fixed sets to keep metric tags low
// cardinality. This metric only describes the safe-zone gate's skip/evict
// decision; it never implies the whole eviction is being suppressed.
func (n *NumaMemoryPressurePlugin) emitSafeZoneGateDecision(numaID int, state string, skip bool, reason string) {
	decision := metricsTagValueGateDecisionEvict
	if skip {
		decision = metricsTagValueGateDecisionSkip
	}
	_ = n.emitter.StoreInt64(metricsNameReclaimedMemorySafeZoneGateDecision, 1, metrics.MetricTypeNameCount,
		metrics.ConvertMapToTags(map[string]string{
			metricsTagKeyNumaID:       strconv.Itoa(numaID),
			metricsTagKeyGateState:    state,
			metricsTagKeyGateDecision: decision,
			metricsTagKeyGateReason:   reason,
		})...)
	general.Infof("reclaimed-memory safe-zone gate: numa=%d state=%s skip=%v reason=%s", numaID, state, skip, reason)
}

func (n *NumaMemoryPressurePlugin) GetEvictPods(_ context.Context, request *pluginapi.GetEvictPodsRequest) (*pluginapi.GetEvictPodsResponse, error) {
	if request == nil {
		return nil, fmt.Errorf("GetEvictPods got nil request")
	}

	return &pluginapi.GetEvictPodsResponse{}, nil
}

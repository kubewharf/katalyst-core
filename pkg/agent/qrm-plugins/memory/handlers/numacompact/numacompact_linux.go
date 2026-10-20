//go:build linux
// +build linux

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

package numacompact

import (
	"strconv"
	"sync"
	"time"

	v1 "k8s.io/api/core/v1"

	memconsts "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/memory/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/memory/dynamicpolicy/state"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/memory/handlers/compaction"
	coreconfig "github.com/kubewharf/katalyst-core/pkg/config"
	dynamicconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic"
	dynamicqrm "github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/adminqos/qrm"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

var (
	// numaLastCompact records, per NUMA node, the compaction state in the current idle period. A
	// NUMA node absent from the map has not been compacted since it last became idle (or has never
	// been observed idle), so it is eligible for the falling-edge compaction. Once compacted, the
	// entry enables periodic THP-order readiness checks while the node stays idle (see
	// doNumaMemCompact). When a business pod is present on a node, its entry is removed so the next
	// idle period triggers a fresh falling-edge compaction.
	numaLastCompact = make(map[int]*numaCompactState)

	numaLastCompactMu sync.RWMutex

	// A non-nil task remains active until its worker actually returns.
	numaMemCompactTask   *numaCompactTaskState
	numaMemCompactTaskMu sync.Mutex

	// nowFn returns the current time. It is a variable so tests can control the clock.
	nowFn = time.Now
)

// All fields are guarded by numaMemCompactTaskMu.
type numaCompactTaskState struct {
	startedAt time.Time
	reset     bool
}

type numaCompactState struct {
	compactAt                   time.Time
	postCompactTHPUnusableIndex float64
}

type numaCompactResult struct {
	costMs                               int64
	preCompactUnusableIndex              float64
	postCompactUnusableIndex             float64
	preCompactTHPOrderPlusFreeSize       uint64
	postCompactTHPOrderPlusFreeSize      uint64
	thpOrderPlusFreeSizeMetricsAvailable bool
}

// getNumaLastCompactState returns the idle-compaction state of numaID and whether it has one.
func getNumaLastCompactState(numaID int) (*numaCompactState, bool) {
	numaLastCompactMu.RLock()
	defer numaLastCompactMu.RUnlock()
	state, ok := numaLastCompact[numaID]
	if !ok || state == nil {
		return nil, false
	}
	return state, true
}

// setNumaCompactState records the latest compaction time and its post-compaction THP-order unusable
// index. An invalid index keeps the compaction cooldown effective without reusing an older baseline.
func setNumaCompactState(numaID int, unusableIndex float64) {
	numaLastCompactMu.Lock()
	defer numaLastCompactMu.Unlock()
	numaLastCompact[numaID] = &numaCompactState{
		compactAt:                   nowFn(),
		postCompactTHPUnusableIndex: unusableIndex,
	}
}

// clearNumaCompactState drops the idle-compaction record of numaID (called when a business pod is
// present), so the next idle period triggers a fresh falling-edge compaction.
func clearNumaCompactState(numaID int) {
	numaLastCompactMu.Lock()
	defer numaLastCompactMu.Unlock()
	delete(numaLastCompact, numaID)
}

// clearNumaCompactStates drops all idle-compaction records. This is used while the feature is
// disabled so workload transitions during that period cannot leave stale idle-cycle state behind.
func clearNumaCompactStates() {
	numaLastCompactMu.Lock()
	defer numaLastCompactMu.Unlock()
	numaLastCompact = make(map[int]*numaCompactState)
}

// emitNumaMemCompactError emits a runtime-error metric tagged with the given reason. It is a no-op
// when the emitter is nil.
func emitNumaMemCompactError(emitter metrics.MetricEmitter, reason string) {
	if emitter == nil {
		return
	}
	_ = emitter.StoreInt64(metricNameNumaMemCompactError, 1, metrics.MetricTypeNameRaw,
		metrics.MetricTag{Key: "reason", Val: reason})
}

func reportNumaMemCompactResult(emitter metrics.MetricEmitter, numaID int, result numaCompactResult) {
	numaIDTag := metrics.MetricTag{Key: "numa_id", Val: strconv.Itoa(numaID)}
	_ = emitter.StoreInt64(metricNameMemoryCompact, 1, metrics.MetricTypeNameRaw, numaIDTag)
	_ = emitter.StoreInt64(metricNameMemoryCompactCost, result.costMs, metrics.MetricTypeNameRaw, numaIDTag)

	if result.preCompactUnusableIndex != invalidUnusableIndex &&
		result.postCompactUnusableIndex != invalidUnusableIndex {
		unusableIndexDiff := result.preCompactUnusableIndex - result.postCompactUnusableIndex
		_ = emitter.StoreFloat64(metricNameNumaMemCompactTHPUnusableIndexDiff,
			unusableIndexDiff,
			metrics.MetricTypeNameRaw, numaIDTag)
		general.Infof("NUMA %d memory compaction completed, cost=%dms, pre_thp_unusable_index=%.1f, post_thp_unusable_index=%.1f, diff=%.1f",
			numaID, result.costMs, result.preCompactUnusableIndex, result.postCompactUnusableIndex,
			unusableIndexDiff)
	} else {
		general.Infof("NUMA %d memory compaction completed, cost=%dms", numaID, result.costMs)
	}

	if result.thpOrderPlusFreeSizeMetricsAvailable {
		freeSizeDiff := int64(result.postCompactTHPOrderPlusFreeSize) -
			int64(result.preCompactTHPOrderPlusFreeSize)
		_ = emitter.StoreInt64(metricNameNumaMemCompactTHPOrderPlusFreeSizeDiffBytes,
			freeSizeDiff,
			metrics.MetricTypeNameRaw, numaIDTag)
		general.Infof("NUMA %d THP-order+ free memory size after compaction: pre=%dB, post=%dB, diff=%dB",
			numaID, result.preCompactTHPOrderPlusFreeSize, result.postCompactTHPOrderPlusFreeSize,
			freeSizeDiff)
	}
}

// getNumaMemCompactConfiguration returns the dynamic NumaMemCompactConfiguration, or nil if the
// dynamic configuration is not available.
func getNumaMemCompactConfiguration(dynamicConf *dynamicconfig.DynamicAgentConfiguration) *dynamicqrm.NumaMemCompactConfiguration {
	if dynamicConf == nil {
		return nil
	}
	conf := dynamicConf.GetDynamicConfiguration()
	if conf == nil {
		return nil
	}
	return conf.NumaMemCompactConfiguration
}

// getMemoryNUMAState returns the memory NUMANodeMap from the memory plugin's readonly state,
// together with whether it was read successfully.
func getMemoryNUMAState() (state.NUMANodeMap, bool) {
	readonlyState, err := state.GetReadonlyState()
	if err != nil || readonlyState == nil {
		general.Errorf("failed to get readonly memory state: %v", err)
		return nil, false
	}
	return readonlyState.GetMachineState()[v1.ResourceMemory], true
}

// numaHasOnlineBusinessPods reports whether the given NUMA node currently hosts any online
// business pod (shared_cores/dedicated_cores) according to the provided memory NUMANodeMap.
func numaHasOnlineBusinessPods(machineState state.NUMANodeMap, numaID int) bool {
	return machineState[numaID].HasSharedOrDedicatedPods()
}

// doNumaMemCompact proactively compacts memory only on NUMA nodes that carry no online business
// (shared_cores/dedicated_cores) pods. The initial idle transition compacts immediately; subsequent
// attempts are gated by degradation from the NUMA node's post-compaction THP-order unusable index.
//
// The policy is edge-triggered with readiness-based maintenance: a NUMA node is compacted once
// right after it becomes idle (falling edge). When periodic compaction is enabled, interval is the
// minimum delay between actual compactions. Once it has elapsed, THP-order readiness is checked on
// every handler cycle and compaction is triggered only when the unusable-index increase from the
// post-compaction baseline exceeds the threshold. When periodic compaction is disabled, the node
// is compacted only once until a business pod is scheduled onto it and leaves again. The memory
// NUMA state is refreshed for each NUMA node so decisions do not rely on a snapshot taken before
// earlier nodes were handled.
// It returns whether at least one NUMA node was actually compacted.
func doNumaMemCompact(metaServer *metaserver.MetaServer, emitter metrics.MetricEmitter,
	enablePeriodic bool, interval time.Duration, degradedThreshold float64,
) bool {
	thpOrder, err := compaction.ReadTHPOrder()
	if err != nil {
		general.Errorf("failed to determine THP order: %v", err)
		emitNumaMemCompactError(emitter, errReasonReadTHPOrder)
		return false
	}

	didCompact := false
	for _, numaID := range metaServer.CPUDetails.NUMANodes().ToSliceNoSortInt() {
		machineState, stateOK := getMemoryNUMAState()
		if !stateOK {
			emitNumaMemCompactError(emitter, errReasonReadStateFailed)
			return didCompact
		}

		// A NUMA node hosting online business pods is dirtied: drop its compaction record so
		// the next idle period triggers a fresh falling-edge compaction, and never compact here.
		if numaHasOnlineBusinessPods(machineState, numaID) {
			general.Infof("skip NUMA %d: online business pods present", numaID)
			clearNumaCompactState(numaID)
			continue
		}

		// The NUMA node is idle now. The first idle observation triggers compaction immediately.
		// Afterwards, interval is the minimum delay before another compaction. Once it expires,
		// THP-order readiness is checked on each handler cycle until compaction is needed.
		preCompactUnusableIndex := invalidUnusableIndex
		if lastCompact, ok := getNumaLastCompactState(numaID); ok {
			if !enablePeriodic || nowFn().Sub(lastCompact.compactAt) < interval {
				continue
			}

			if lastCompact.postCompactTHPUnusableIndex != invalidUnusableIndex {
				unusableIndex, err := compaction.ReadNumaUnusableIndex(numaID, thpOrder)
				if err != nil {
					general.Errorf("failed to check NUMA %d THP-order unusable-index degradation: %v", numaID, err)
					emitNumaMemCompactError(emitter, errReasonReadTHPUnusableIndex)
					continue
				}
				preCompactUnusableIndex = unusableIndex
				unusableIndexDiff := unusableIndex - lastCompact.postCompactTHPUnusableIndex
				degraded := unusableIndexDiff > degradedThreshold
				degradedValue := int64(0)
				if degraded {
					degradedValue = 1
				}
				_ = emitter.StoreInt64(metricNameNumaMemCompactTHPUnusableIndexDegraded, degradedValue,
					metrics.MetricTypeNameRaw,
					metrics.MetricTag{Key: "numa_id", Val: strconv.Itoa(numaID)})
				general.Infof("NUMA %d THP-order unusable index degradation checked: order=%d, index=%.1f, baseline=%.1f, diff=%.1f, threshold=%.1f, degraded=%t",
					numaID, thpOrder, unusableIndex, lastCompact.postCompactTHPUnusableIndex, unusableIndexDiff,
					degradedThreshold, degraded)
				if !degraded {
					continue
				}
			}
		}

		if preCompactUnusableIndex == invalidUnusableIndex {
			unusableIndex, err := compaction.ReadNumaUnusableIndex(numaID, thpOrder)
			if err != nil {
				general.Errorf("failed to read NUMA %d pre-compaction THP-order unusable index: %v", numaID, err)
				emitNumaMemCompactError(emitter, errReasonReadTHPUnusableIndex)
			} else {
				preCompactUnusableIndex = unusableIndex
			}
		}

		preCompactTHPOrderPlusFreeSize, preCompactTHPOrderPlusFreeSizeErr := compaction.ReadNumaFreeMemorySizeAtOrAboveOrder(numaID, thpOrder)
		if preCompactTHPOrderPlusFreeSizeErr != nil {
			general.Errorf("failed to read NUMA %d pre-compaction THP-order+ free memory size: %v",
				numaID, preCompactTHPOrderPlusFreeSizeErr)
			emitNumaMemCompactError(emitter, errReasonReadTHPOrderPlusFreeSize)
		}

		// Compact this NUMA node unless its own kcompactd is busy (R/D state). Emit the
		// per-compaction metric (tagged with the numa_id) and record the post-compaction state. Also
		// emit how long the compaction took.
		start := nowFn()
		if compaction.TryCompactNUMANode(numaID) {
			didCompact = true
			costMs := nowFn().Sub(start).Milliseconds()

			postCompactUnusableIndex, err := compaction.ReadNumaUnusableIndex(numaID, thpOrder)
			if err != nil {
				general.Errorf("failed to read NUMA %d post-compaction THP-order unusable index: %v", numaID, err)
				emitNumaMemCompactError(emitter, errReasonReadTHPUnusableIndex)
				postCompactUnusableIndex = invalidUnusableIndex
			}

			setNumaCompactState(numaID, postCompactUnusableIndex)

			if postCompactUnusableIndex != invalidUnusableIndex {
				_ = emitter.StoreInt64(metricNameNumaMemCompactTHPUnusableIndexDegraded, 0,
					metrics.MetricTypeNameRaw,
					metrics.MetricTag{Key: "numa_id", Val: strconv.Itoa(numaID)})
			}

			postCompactTHPOrderPlusFreeSize, postCompactTHPOrderPlusFreeSizeErr := compaction.ReadNumaFreeMemorySizeAtOrAboveOrder(numaID, thpOrder)
			if postCompactTHPOrderPlusFreeSizeErr != nil {
				general.Errorf("failed to read NUMA %d post-compaction THP-order+ free memory size: %v",
					numaID, postCompactTHPOrderPlusFreeSizeErr)
				emitNumaMemCompactError(emitter, errReasonReadTHPOrderPlusFreeSize)
			}

			reportNumaMemCompactResult(emitter, numaID, numaCompactResult{
				costMs:                          costMs,
				preCompactUnusableIndex:         preCompactUnusableIndex,
				postCompactUnusableIndex:        postCompactUnusableIndex,
				preCompactTHPOrderPlusFreeSize:  preCompactTHPOrderPlusFreeSize,
				postCompactTHPOrderPlusFreeSize: postCompactTHPOrderPlusFreeSize,
				thpOrderPlusFreeSizeMetricsAvailable: preCompactTHPOrderPlusFreeSizeErr == nil &&
					postCompactTHPOrderPlusFreeSizeErr == nil,
			})
		}
	}
	return didCompact
}

// reportNumaMemCompactTaskDuration reports the elapsed or final duration of a background scan.
func reportNumaMemCompactTaskDuration(emitter metrics.MetricEmitter, duration time.Duration, status string) {
	_ = emitter.StoreInt64(metricNameNumaMemCompactTaskDurationSeconds,
		int64(duration.Seconds()), metrics.MetricTypeNameRaw,
		metrics.MetricTag{Key: "status", Val: status})
}

// NumaMemCompact is the periodical handler that proactively compacts memory on idle NUMA nodes.
// It is a standalone feature, independent of the fragmem handler, and is gated only by the
// dynamically configured EnableNumaMemCompact switch (per machine-type via AdminQoSConfiguration).
// It schedules at most one background scan so synchronous compaction never blocks its heartbeat.
func NumaMemCompact(conf *coreconfig.Configuration,
	_ interface{}, dynamicConf *dynamicconfig.DynamicAgentConfiguration,
	emitter metrics.MetricEmitter, metaServer *metaserver.MetaServer,
) {
	general.Infof("NumaMemCompact was called")

	defer func() {
		_ = general.UpdateHealthzStateByError(memconsts.NumaMemCompact, nil)
	}()

	if conf == nil || emitter == nil || metaServer == nil {
		general.Errorf("nil input, conf:%v, emitter:%v, metaServer:%v", conf, emitter, metaServer)
		emitNumaMemCompactError(emitter, errReasonNilInput)
		return
	}

	numaMemCompactConf := getNumaMemCompactConfiguration(dynamicConf)
	if numaMemCompactConf == nil {
		general.Infof("dynamic numa mem compact configuration not found")
		emitNumaMemCompactError(emitter, errReasonDynamicConfMissing)
		return
	}

	// Report whether the feature is currently enabled so it can be observed regardless of the
	// switch state.
	enabledValue := int64(0)
	if numaMemCompactConf.EnableNumaMemCompact {
		enabledValue = 1
	}
	_ = emitter.StoreInt64(metricNameNumaMemCompactEnabled, enabledValue, metrics.MetricTypeNameRaw)

	numaMemCompactTaskMu.Lock()
	defer numaMemCompactTaskMu.Unlock()

	if numaMemCompactTask != nil {
		reportNumaMemCompactTaskDuration(emitter, time.Since(numaMemCompactTask.startedAt), "ongoing")
	}

	if !numaMemCompactConf.EnableNumaMemCompact {
		general.Infof("NumaMemCompact skipped: EnableNumaMemCompact disabled")
		if numaMemCompactTask != nil {
			numaMemCompactTask.reset = true
		}
		clearNumaCompactStates()
		return
	}

	if numaMemCompactTask != nil {
		return
	}

	numaMemCompactTask = &numaCompactTaskState{
		startedAt: time.Now(),
	}
	interval := numaMemCompactConf.NumaMemCompactInterval
	enablePeriodic := numaMemCompactConf.EnablePeriodicNumaMemCompact
	degradedThreshold := numaMemCompactConf.THPUnusableIndexDegradedThreshold
	go func() {
		didCompact := false
		defer func() {
			numaMemCompactTaskMu.Lock()
			defer numaMemCompactTaskMu.Unlock()
			if didCompact {
				reportNumaMemCompactTaskDuration(emitter, time.Since(numaMemCompactTask.startedAt), "completed")
			}
			// A write finishing after disable must not restore the previous idle-cycle baseline,
			// even if the feature has already been re-enabled while that write was in flight.
			if numaMemCompactTask.reset {
				clearNumaCompactStates()
			}
			numaMemCompactTask = nil
		}()
		didCompact = doNumaMemCompact(metaServer, emitter, enablePeriodic, interval, degradedThreshold)
	}()
}

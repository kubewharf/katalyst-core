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
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"

	apiconsts "github.com/kubewharf/katalyst-api/pkg/consts"
	pluginapi "github.com/kubewharf/katalyst-api/pkg/protocol/evictionplugin/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	utilMetric "github.com/kubewharf/katalyst-core/pkg/util/metric"
)

const (
	safeZoneGateTestNumaID    = 0
	safeZoneGateTestNumaTotal = 50 * 1024 * 1024 * 1024 // 50GB per numa (mem.total.numa)
	safeZoneGateTestRatioThr  = 0.30
)

// watermark snapshots in pages chosen so that:
//   - safeZoneGateSafeSnap:       free >= min and not under additional pressure
//   - safeZoneGateDangerMinSnap:  free < min (immediate danger)
//   - safeZoneGateDangerAddlSnap: free >= min but under additional pressure
var (
	safeZoneGateSafeSnap       = &numaWatermarkSnapshot{valid: true, free: 900, min: 100, low: 1000}
	safeZoneGateDangerMinSnap  = &numaWatermarkSnapshot{valid: true, free: 50, min: 100, low: 1000}
	safeZoneGateDangerAddlSnap = &numaWatermarkSnapshot{valid: true, free: 400, min: 100, low: 1000}
)

// gateEmission captures one dedicated gate-decision emission so the fixed
// low-cardinality gate_state / gate_decision / gate_reason tags can be asserted.
type gateEmission struct {
	state    string
	decision string
	reason   string
}

// recordingEmitter records only the dedicated safe-zone gate metric; every other
// emitter call is ignored.
type recordingEmitter struct {
	metrics.DummyMetrics
	mu        sync.Mutex
	emissions []gateEmission
}

func (r *recordingEmitter) StoreInt64(key string, _ int64, _ metrics.MetricTypeName, tags ...metrics.MetricTag) error {
	if key != metricsNameReclaimedMemorySafeZoneGateDecision {
		return nil
	}
	em := gateEmission{}
	for _, t := range tags {
		switch t.Key {
		case metricsTagKeyGateState:
			em.state = t.Val
		case metricsTagKeyGateDecision:
			em.decision = t.Val
		case metricsTagKeyGateReason:
			em.reason = t.Val
		}
	}
	r.mu.Lock()
	r.emissions = append(r.emissions, em)
	r.mu.Unlock()
	return nil
}

func (r *recordingEmitter) last() gateEmission {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.emissions) == 0 {
		return gateEmission{}
	}
	return r.emissions[len(r.emissions)-1]
}

func makeSafeZoneGateReclaimedPod(uid, name string, usageByNuma map[int]float64, fetcher *metric.FakeMetricsFetcher) *v1.Pod {
	pod := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			UID:  types.UID(uid),
			Name: name,
			Annotations: map[string]string{
				apiconsts.PodAnnotationQoSLevelKey: apiconsts.PodAnnotationQoSLevelReclaimedCores,
			},
		},
		Spec: v1.PodSpec{
			Containers: []v1.Container{{Name: "c"}},
		},
	}
	now := time.Now()
	for numaID, usage := range usageByNuma {
		fetcher.SetContainerNumaMetric(uid, "c", numaID, consts.MetricsMemTotalPerNumaContainer,
			utilMetric.MetricData{Value: usage, Time: &now})
	}
	return pod
}

func makeSafeZoneGateNormalPod(uid, name string, usageByNuma map[int]float64, fetcher *metric.FakeMetricsFetcher) *v1.Pod {
	pod := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			UID:  types.UID(uid),
			Name: name,
			Annotations: map[string]string{
				apiconsts.PodAnnotationQoSLevelKey: apiconsts.PodAnnotationQoSLevelDedicatedCores,
			},
		},
		Spec: v1.PodSpec{
			Containers: []v1.Container{{Name: "c"}},
		},
	}
	now := time.Now()
	for numaID, usage := range usageByNuma {
		fetcher.SetContainerNumaMetric(uid, "c", numaID, consts.MetricsMemTotalPerNumaContainer,
			utilMetric.MetricData{Value: usage, Time: &now})
	}
	return pod
}

func setSafeZoneGateNumaTotal(t *testing.T, fetcher *metric.FakeMetricsFetcher, numaID int, total float64) {
	t.Helper()
	now := time.Now()
	fetcher.SetNumaMetric(numaID, consts.MetricMemTotalNuma, utilMetric.MetricData{Value: total, Time: &now})
}

func TestNumaMemoryPressurePlugin_decideReclaimedMemorySafeZoneGate(t *testing.T) {
	t.Parallel()

	// 1GB on the gate numa => 0.02 ratio (below the 0.30 threshold).
	lowPods := func(f *metric.FakeMetricsFetcher) []*v1.Pod {
		return []*v1.Pod{makeSafeZoneGateReclaimedPod("uid-low", "reclaimed-low",
			map[int]float64{safeZoneGateTestNumaID: 1 * 1024 * 1024 * 1024}, f)}
	}
	// 15GB on the gate numa => exactly the 0.30 boundary, must NOT be skipped.
	boundaryPods := func(f *metric.FakeMetricsFetcher) []*v1.Pod {
		return []*v1.Pod{makeSafeZoneGateReclaimedPod("uid-boundary", "reclaimed-boundary",
			map[int]float64{safeZoneGateTestNumaID: 15 * 1024 * 1024 * 1024}, f)}
	}
	// 25GB on the gate numa => 0.5 ratio (above threshold).
	highPods := func(f *metric.FakeMetricsFetcher) []*v1.Pod {
		return []*v1.Pod{makeSafeZoneGateReclaimedPod("uid-high", "reclaimed-high",
			map[int]float64{safeZoneGateTestNumaID: 25 * 1024 * 1024 * 1024}, f)}
	}
	// A reclaimed pod with NO per-NUMA metric registered (metric_unavailable path).
	missingMetricPods := func(f *metric.FakeMetricsFetcher) []*v1.Pod {
		return []*v1.Pod{makeSafeZoneGateReclaimedPod("uid-missing", "reclaimed-missing", nil, f)}
	}
	// Two reclaimed pods: 10GB + 10GB = 20GB => 0.4 ratio (above threshold).
	// Each alone would be 0.2 (below threshold), so the NOT-skip outcome
	// proves the numerator accumulates across reclaimed pods.
	multiReclaimedPods := func(f *metric.FakeMetricsFetcher) []*v1.Pod {
		return []*v1.Pod{
			makeSafeZoneGateReclaimedPod("uid-r1", "reclaimed-r1", map[int]float64{safeZoneGateTestNumaID: 10 * 1024 * 1024 * 1024}, f),
			makeSafeZoneGateReclaimedPod("uid-r2", "reclaimed-r2", map[int]float64{safeZoneGateTestNumaID: 10 * 1024 * 1024 * 1024}, f),
		}
	}
	// A small reclaimed pod (1GB => 0.02) plus a huge non-reclaimed pod (40GB).
	// The skip outcome proves non-reclaimed usage is excluded from the numerator;
	// if it were counted the ratio would be 0.82 and the gate would not skip.
	mixedPods := func(f *metric.FakeMetricsFetcher) []*v1.Pod {
		return []*v1.Pod{
			makeSafeZoneGateReclaimedPod("uid-recl", "reclaimed-small", map[int]float64{safeZoneGateTestNumaID: 1 * 1024 * 1024 * 1024}, f),
			makeSafeZoneGateNormalPod("uid-normal", "normal-huge", map[int]float64{safeZoneGateTestNumaID: 40 * 1024 * 1024 * 1024}, f),
		}
	}

	tests := []struct {
		name         string
		enable       bool
		ratioThr     float64
		snap         *numaWatermarkSnapshot
		pods         func(f *metric.FakeMetricsFetcher) []*v1.Pod
		skipTotal    bool
		totalZero    bool
		filterErr    bool
		wantSkip     bool
		wantReason   string
		wantState    string
		wantDecision string
	}{
		// --- closed arm: ratio <= 0 never evaluates the safe zone ---
		{
			name:         "unset construction defaults are fully closed",
			enable:       false,
			ratioThr:     0.0,
			snap:         safeZoneGateSafeSnap,
			pods:         lowPods,
			wantSkip:     false,
			wantReason:   "closed_zero_ratio",
			wantState:    metricsTagValueGateStateClosed,
			wantDecision: metricsTagValueGateDecisionEvict,
		},
		{
			name:         "enable=false + zero ratio stays closed",
			enable:       false,
			ratioThr:     0,
			snap:         safeZoneGateSafeSnap,
			pods:         lowPods,
			wantSkip:     false,
			wantReason:   "closed_zero_ratio",
			wantState:    metricsTagValueGateStateClosed,
			wantDecision: metricsTagValueGateDecisionEvict,
		},
		{
			name:         "enable=true + zero ratio still stays closed",
			enable:       true,
			ratioThr:     0,
			snap:         safeZoneGateSafeSnap,
			pods:         lowPods,
			wantSkip:     false,
			wantReason:   "closed_zero_ratio",
			wantState:    metricsTagValueGateStateClosed,
			wantDecision: metricsTagValueGateDecisionEvict,
		},

		// --- invalid threshold: fail closed, observable, no panic ---
		{
			name:         "NaN ratio fails closed as invalid_ratio",
			enable:       true,
			ratioThr:     math.NaN(),
			snap:         safeZoneGateSafeSnap,
			pods:         lowPods,
			wantSkip:     false,
			wantReason:   "invalid_ratio",
			wantState:    metricsTagValueGateStateClosed,
			wantDecision: metricsTagValueGateDecisionEvict,
		},
		{
			name:         "+Inf ratio fails closed as invalid_ratio",
			enable:       true,
			ratioThr:     math.Inf(1),
			snap:         safeZoneGateSafeSnap,
			pods:         lowPods,
			wantSkip:     false,
			wantReason:   "invalid_ratio",
			wantState:    metricsTagValueGateStateClosed,
			wantDecision: metricsTagValueGateDecisionEvict,
		},
		{
			name:         "-Inf ratio fails closed as invalid_ratio",
			enable:       true,
			ratioThr:     math.Inf(-1),
			snap:         safeZoneGateSafeSnap,
			pods:         lowPods,
			wantSkip:     false,
			wantReason:   "invalid_ratio",
			wantState:    metricsTagValueGateStateClosed,
			wantDecision: metricsTagValueGateDecisionEvict,
		},
		{
			name:         "ratio above 1 fails closed as invalid_ratio",
			enable:       true,
			ratioThr:     1.5,
			snap:         safeZoneGateSafeSnap,
			pods:         lowPods,
			wantSkip:     false,
			wantReason:   "invalid_ratio",
			wantState:    metricsTagValueGateStateClosed,
			wantDecision: metricsTagValueGateDecisionEvict,
		},
		{
			// Direct eval-level negative threshold: bypasses ApplyConfiguration
			// (which would reset it to 0.0) to exercise the defensive
			// invalid_ratio fail-closed re-check inside decide().
			name:         "negative ratio fails closed as invalid_ratio at evaluation",
			enable:       true,
			ratioThr:     -0.2,
			snap:         safeZoneGateSafeSnap,
			pods:         lowPods,
			wantSkip:     false,
			wantReason:   "invalid_ratio",
			wantState:    metricsTagValueGateStateClosed,
			wantDecision: metricsTagValueGateDecisionEvict,
		},

		// --- auto-shadow arm: ratio > 0 + enable=false observes but never skips ---
		{
			name:         "auto-shadow positive ratio records auto_shadow_would_skip",
			enable:       false,
			ratioThr:     safeZoneGateTestRatioThr,
			snap:         safeZoneGateSafeSnap,
			pods:         lowPods,
			wantSkip:     false,
			wantReason:   "auto_shadow_would_skip",
			wantState:    metricsTagValueGateStateAutoShadow,
			wantDecision: metricsTagValueGateDecisionEvict,
		},
		{
			name:         "auto-shadow observes danger free<min bypass as evict",
			enable:       false,
			ratioThr:     safeZoneGateTestRatioThr,
			snap:         safeZoneGateDangerMinSnap,
			pods:         lowPods,
			wantSkip:     false,
			wantReason:   "free_below_min",
			wantState:    metricsTagValueGateStateAutoShadow,
			wantDecision: metricsTagValueGateDecisionEvict,
		},
		{
			name:         "auto-shadow observes additional-pressure bypass as evict",
			enable:       false,
			ratioThr:     safeZoneGateTestRatioThr,
			snap:         safeZoneGateDangerAddlSnap,
			pods:         lowPods,
			wantSkip:     false,
			wantReason:   "additional_pressure",
			wantState:    metricsTagValueGateStateAutoShadow,
			wantDecision: metricsTagValueGateDecisionEvict,
		},
		{
			name:         "auto-shadow boundary ratio does not rename reason",
			enable:       false,
			ratioThr:     safeZoneGateTestRatioThr,
			snap:         safeZoneGateSafeSnap,
			pods:         boundaryPods,
			wantSkip:     false,
			wantReason:   "reclaimed_usage_not_low",
			wantState:    metricsTagValueGateStateAutoShadow,
			wantDecision: metricsTagValueGateDecisionEvict,
		},

		// --- enabled arm: the only arm that really skips ---
		{
			name:         "danger free<min always bypasses skip",
			enable:       true,
			ratioThr:     safeZoneGateTestRatioThr,
			snap:         safeZoneGateDangerMinSnap,
			pods:         lowPods,
			wantSkip:     false,
			wantReason:   "free_below_min",
			wantState:    metricsTagValueGateStateEnabled,
			wantDecision: metricsTagValueGateDecisionEvict,
		},
		{
			name:         "additional-pressure watermark bypasses skip",
			enable:       true,
			ratioThr:     safeZoneGateTestRatioThr,
			snap:         safeZoneGateDangerAddlSnap,
			pods:         lowPods,
			wantSkip:     false,
			wantReason:   "additional_pressure",
			wantState:    metricsTagValueGateStateEnabled,
			wantDecision: metricsTagValueGateDecisionEvict,
		},
		{
			name:         "enabled safe zone low ratio skips reclaimed eviction",
			enable:       true,
			ratioThr:     safeZoneGateTestRatioThr,
			snap:         safeZoneGateSafeSnap,
			pods:         lowPods,
			wantSkip:     true,
			wantReason:   "low_reclaimed_usage",
			wantState:    metricsTagValueGateStateEnabled,
			wantDecision: metricsTagValueGateDecisionSkip,
		},
		{
			name:         "enabled ratio exactly at threshold is boundary and not skipped",
			enable:       true,
			ratioThr:     safeZoneGateTestRatioThr,
			snap:         safeZoneGateSafeSnap,
			pods:         boundaryPods,
			wantSkip:     false,
			wantReason:   "reclaimed_usage_not_low",
			wantState:    metricsTagValueGateStateEnabled,
			wantDecision: metricsTagValueGateDecisionEvict,
		},
		{
			name:         "enabled safe zone high ratio still evicts",
			enable:       true,
			ratioThr:     safeZoneGateTestRatioThr,
			snap:         safeZoneGateSafeSnap,
			pods:         highPods,
			wantSkip:     false,
			wantReason:   "reclaimed_usage_not_low",
			wantState:    metricsTagValueGateStateEnabled,
			wantDecision: metricsTagValueGateDecisionEvict,
		},
		{
			name:         "enabled invalid snapshot fails safe (does not skip)",
			enable:       true,
			ratioThr:     safeZoneGateTestRatioThr,
			snap:         nil,
			pods:         lowPods,
			wantSkip:     false,
			wantReason:   "watermark_unavailable",
			wantState:    metricsTagValueGateStateEnabled,
			wantDecision: metricsTagValueGateDecisionEvict,
		},
		{
			// A non-nil snapshot whose valid flag is false (watermark fetch
			// failed this cycle) must also fail closed, distinct from a nil map
			// entry.
			name:         "enabled snapshot valid=false fails safe as watermark_unavailable",
			enable:       true,
			ratioThr:     safeZoneGateTestRatioThr,
			snap:         &numaWatermarkSnapshot{valid: false, free: 0, min: 100, low: 1000},
			pods:         lowPods,
			wantSkip:     false,
			wantReason:   "watermark_unavailable",
			wantState:    metricsTagValueGateStateEnabled,
			wantDecision: metricsTagValueGateDecisionEvict,
		},
		{
			name:         "enabled missing numa total metric fails safe",
			enable:       true,
			ratioThr:     safeZoneGateTestRatioThr,
			snap:         safeZoneGateSafeSnap,
			pods:         lowPods,
			skipTotal:    true,
			wantSkip:     false,
			wantReason:   "metric_unavailable",
			wantState:    metricsTagValueGateStateEnabled,
			wantDecision: metricsTagValueGateDecisionEvict,
		},
		{
			name:         "enabled reclaimed pod missing per-numa metric fails safe",
			enable:       true,
			ratioThr:     safeZoneGateTestRatioThr,
			snap:         safeZoneGateSafeSnap,
			pods:         missingMetricPods,
			wantSkip:     false,
			wantReason:   "metric_unavailable",
			wantState:    metricsTagValueGateStateEnabled,
			wantDecision: metricsTagValueGateDecisionEvict,
		},
		{
			name:         "reclaimed usage accumulates across multiple reclaimed pods",
			enable:       true,
			ratioThr:     safeZoneGateTestRatioThr,
			snap:         safeZoneGateSafeSnap,
			pods:         multiReclaimedPods,
			wantSkip:     false,
			wantReason:   "reclaimed_usage_not_low",
			wantState:    metricsTagValueGateStateEnabled,
			wantDecision: metricsTagValueGateDecisionEvict,
		},
		{
			name:         "non-reclaimed pod usage is excluded from the numerator",
			enable:       true,
			ratioThr:     safeZoneGateTestRatioThr,
			snap:         safeZoneGateSafeSnap,
			pods:         mixedPods,
			wantSkip:     true,
			wantReason:   "low_reclaimed_usage",
			wantState:    metricsTagValueGateStateEnabled,
			wantDecision: metricsTagValueGateDecisionSkip,
		},
		{
			name:         "numa total <= 0 fails safe as metric_unavailable",
			enable:       true,
			ratioThr:     safeZoneGateTestRatioThr,
			snap:         safeZoneGateSafeSnap,
			pods:         lowPods,
			totalZero:    true,
			wantSkip:     false,
			wantReason:   "metric_unavailable",
			wantState:    metricsTagValueGateStateEnabled,
			wantDecision: metricsTagValueGateDecisionEvict,
		},
		{
			name:         "qos classification error fails safe as qos_classification_failed",
			enable:       true,
			ratioThr:     safeZoneGateTestRatioThr,
			snap:         safeZoneGateSafeSnap,
			pods:         lowPods,
			filterErr:    true,
			wantSkip:     false,
			wantReason:   "qos_classification_failed",
			wantState:    metricsTagValueGateStateEnabled,
			wantDecision: metricsTagValueGateDecisionEvict,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			plugin, err := makeNumaPressureEvictionPlugin(makeConf())
			assert.NoError(t, err)
			fetcher := plugin.metaServer.MetricsFetcher.(*metric.FakeMetricsFetcher)

			// Swap in a recording emitter to assert the low-cardinality gate tags.
			em := &recordingEmitter{}
			plugin.emitter = em

			dc := plugin.dynamicConfig.GetDynamicConfiguration().MemoryPressureEvictionConfiguration
			dc.EnableReclaimedMemorySafeZoneGate = tt.enable
			dc.ReclaimedMemoryUsageRatioThreshold = tt.ratioThr

			if tt.totalZero {
				setSafeZoneGateNumaTotal(t, fetcher, safeZoneGateTestNumaID, 0)
			} else if !tt.skipTotal {
				setSafeZoneGateNumaTotal(t, fetcher, safeZoneGateTestNumaID, safeZoneGateTestNumaTotal)
			}
			if tt.filterErr {
				plugin.reclaimedPodFilter = func(pod *v1.Pod) (bool, error) {
					return false, fmt.Errorf("injected qos classification error")
				}
			}
			pods := tt.pods(fetcher)

			skip, reason := plugin.decideReclaimedMemorySafeZoneGate(safeZoneGateTestNumaID, tt.snap, pods)
			assert.Equal(t, tt.wantSkip, skip)
			assert.Equal(t, tt.wantReason, reason)

			// Exactly one gate decision is emitted on the dedicated metric.
			assert.Len(t, em.emissions, 1, "expected exactly one gate decision emission")
			got := em.last()
			assert.Equal(t, tt.wantState, got.state)
			assert.Equal(t, tt.wantDecision, got.decision)
			assert.Equal(t, tt.wantReason, got.reason)
		})
	}
}

// TestNumaMemoryPressurePlugin_SafeZoneGateStatelessDefault proves a freshly
// constructed plugin (restart / no prior state) ships fully closed without any
// watermark snapshot or opt-in, and never skips.
func TestNumaMemoryPressurePlugin_SafeZoneGateStatelessDefault(t *testing.T) {
	t.Parallel()

	plugin, err := makeNumaPressureEvictionPlugin(makeConf())
	require.NoError(t, err)
	em := &recordingEmitter{}
	plugin.emitter = em

	// No ThresholdMet ran: the snapshot map is empty (stateless after restart).
	assert.Empty(t, plugin.numaWatermarkSnapshotMap)

	// Default dynamic config: disabled, zero ratio -> closed, no skip.
	skip, reason := plugin.decideReclaimedMemorySafeZoneGate(safeZoneGateTestNumaID, nil, nil)
	assert.False(t, skip)
	assert.Equal(t, "closed_zero_ratio", reason)
	got := em.last()
	assert.Equal(t, metricsTagValueGateStateClosed, got.state)
}

func TestNumaMemoryPressurePlugin_GetTopEvictionPods_SafeZoneGate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		action    int
		snap      *numaWatermarkSnapshot
		wantEvict bool
	}{
		{
			name:      "safe-zone reclaimed eviction is skipped",
			action:    actionReclaimedEviction,
			snap:      safeZoneGateSafeSnap,
			wantEvict: false,
		},
		{
			name:      "danger reclaimed eviction bypasses gate",
			action:    actionReclaimedEviction,
			snap:      safeZoneGateDangerMinSnap,
			wantEvict: true,
		},
		{
			name:      "actionEviction backstop is unaffected by gate",
			action:    actionEviction,
			snap:      safeZoneGateSafeSnap,
			wantEvict: true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// Each subtest owns an isolated plugin/fetcher/pod so parallel
			// cases never mutate shared state.
			plugin, err := makeNumaPressureEvictionPlugin(makeConf())
			assert.NoError(t, err)
			fetcher := plugin.metaServer.MetricsFetcher.(*metric.FakeMetricsFetcher)

			dc := plugin.dynamicConfig.GetDynamicConfiguration().MemoryPressureEvictionConfiguration
			dc.EnableReclaimedMemorySafeZoneGate = true
			dc.ReclaimedMemoryUsageRatioThreshold = safeZoneGateTestRatioThr
			setSafeZoneGateNumaTotal(t, fetcher, safeZoneGateTestNumaID, safeZoneGateTestNumaTotal)

			reclaimedPod := makeSafeZoneGateReclaimedPod("uid-e2e", "reclaimed-e2e",
				map[int]float64{safeZoneGateTestNumaID: 1 * 1024 * 1024 * 1024}, fetcher)

			// Set this case's action and watermark snapshot on the isolated plugin.
			plugin.Lock()
			plugin.isUnderNumaPressure = true
			plugin.numaActionMap = map[int]int{safeZoneGateTestNumaID: tt.action}
			plugin.numaWatermarkSnapshotMap = map[int]*numaWatermarkSnapshot{safeZoneGateTestNumaID: tt.snap}
			plugin.Unlock()

			resp, err := plugin.GetTopEvictionPods(context.TODO(), &pluginapi.GetTopEvictionPodsRequest{
				ActivePods:    []*v1.Pod{reclaimedPod},
				TopN:          1,
				EvictionScope: EvictionScopeNumaMemory,
			})
			assert.NoError(t, err)

			got := sets.NewString()
			for _, p := range resp.TargetPods {
				got.Insert(p.Name)
			}
			if tt.wantEvict {
				assert.True(t, got.Has("reclaimed-e2e"), "expected pod to be evicted, got %v", got)
			} else {
				assert.False(t, got.Has("reclaimed-e2e"), "expected pod to be skipped, got %v", got)
			}
		})
	}
}

// TestNumaMemoryPressurePlugin_SafeZoneGateConcurrent exercises concurrent
// decide() and GetTopEvictionPods calls under the race detector; the gate must
// stay consistent and never panic or data-race.
func TestNumaMemoryPressurePlugin_SafeZoneGateConcurrent(t *testing.T) {
	t.Parallel()

	plugin, err := makeNumaPressureEvictionPlugin(makeConf())
	require.NoError(t, err)
	fetcher := plugin.metaServer.MetricsFetcher.(*metric.FakeMetricsFetcher)

	dc := plugin.dynamicConfig.GetDynamicConfiguration().MemoryPressureEvictionConfiguration
	dc.EnableReclaimedMemorySafeZoneGate = true
	dc.ReclaimedMemoryUsageRatioThreshold = safeZoneGateTestRatioThr
	setSafeZoneGateNumaTotal(t, fetcher, safeZoneGateTestNumaID, safeZoneGateTestNumaTotal)

	pod := makeSafeZoneGateReclaimedPod("uid-conc", "reclaimed-conc",
		map[int]float64{safeZoneGateTestNumaID: 1 * 1024 * 1024 * 1024}, fetcher)

	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				// Safe snap + low ratio: enabled arm deterministically skips.
				skip, reason := plugin.decideReclaimedMemorySafeZoneGate(safeZoneGateTestNumaID, safeZoneGateSafeSnap, []*v1.Pod{pod})
				assert.True(t, skip)
				assert.Equal(t, "low_reclaimed_usage", reason)
			}
		}()
	}
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				plugin.Lock()
				plugin.isUnderNumaPressure = true
				plugin.numaActionMap = map[int]int{safeZoneGateTestNumaID: actionReclaimedEviction}
				plugin.numaWatermarkSnapshotMap = map[int]*numaWatermarkSnapshot{safeZoneGateTestNumaID: safeZoneGateSafeSnap}
				plugin.Unlock()

				_, _ = plugin.GetTopEvictionPods(context.TODO(), &pluginapi.GetTopEvictionPodsRequest{
					ActivePods:    []*v1.Pod{pod},
					TopN:          1,
					EvictionScope: EvictionScopeNumaMemory,
				})
			}
		}()
	}
	wg.Wait()
}

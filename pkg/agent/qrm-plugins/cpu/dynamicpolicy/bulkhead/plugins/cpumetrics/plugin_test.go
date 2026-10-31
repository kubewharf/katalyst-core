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

package cpumetrics

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	bulkheadapi "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/bulkhead/api"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/bulkhead/model"
	pkgconsts "github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent"
	metricfetcher "github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric"
	metrictypes "github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric/types"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
	utilmetric "github.com/kubewharf/katalyst-core/pkg/util/metric"
)

type capturedMetric struct {
	key   string
	value float64
	tags  map[string]string
}

type captureEmitter struct {
	sync.Mutex
	stored  []capturedMetric
	failKey string
}

func (e *captureEmitter) StoreInt64(key string, value int64, _ metrics.MetricTypeName, tags ...metrics.MetricTag) error {
	return e.store(key, float64(value), tags)
}

func (e *captureEmitter) StoreFloat64(key string, value float64, _ metrics.MetricTypeName, tags ...metrics.MetricTag) error {
	return e.store(key, value, tags)
}

func (e *captureEmitter) store(key string, value float64, tags []metrics.MetricTag) error {
	e.Lock()
	defer e.Unlock()
	tagMap := make(map[string]string, len(tags))
	for _, tag := range tags {
		tagMap[tag.Key] = tag.Val
	}
	e.stored = append(e.stored, capturedMetric{key: key, value: value, tags: tagMap})
	if key == e.failKey {
		return errors.New("injected emitter failure")
	}
	return nil
}

func (e *captureEmitter) WithTags(string, ...metrics.MetricTag) metrics.MetricEmitter { return e }
func (e *captureEmitter) Run(context.Context)                                         {}

func (e *captureEmitter) snapshot() []capturedMetric {
	e.Lock()
	defer e.Unlock()
	result := append([]capturedMetric(nil), e.stored...)
	sort.Slice(result, func(i, j int) bool {
		left := result[i].key + fmt.Sprint(result[i].tags)
		right := result[j].key + fmt.Sprint(result[j].tags)
		return left < right
	})
	return result
}

func (e *captureEmitter) reset() {
	e.Lock()
	defer e.Unlock()
	e.stored = nil
}

type perCPUMetric struct {
	cpu        int
	metricName string
	value      float64
}

func newFetcherWithMetrics(samples []perCPUMetric) metrictypes.MetricsFetcher {
	fetcher := metricfetcher.NewFakeMetricsFetcher(metrics.DummyMetrics{}).(*metricfetcher.FakeMetricsFetcher)
	now := time.Now()
	for _, sample := range samples {
		fetcher.SetCPUMetric(sample.cpu, sample.metricName,
			utilmetric.MetricData{Value: sample.value, Time: &now})
	}
	return fetcher
}

func completeSamples(cpus ...int) []perCPUMetric {
	result := make([]perCPUMetric, 0, len(cpus)*7)
	for _, cpu := range cpus {
		base := float64(cpu + 1)
		result = append(result,
			perCPUMetric{cpu: cpu, metricName: pkgconsts.MetricCPUUsageRatio, value: base / 10},
			perCPUMetric{cpu: cpu, metricName: pkgconsts.MetricCPUIOWaitRatio, value: base / 100},
			perCPUMetric{cpu: cpu, metricName: pkgconsts.MetricCPUSchedwait, value: base * 10},
			perCPUMetric{cpu: cpu, metricName: pkgconsts.MetricCPUIrqRatio, value: base / 1000},
			perCPUMetric{cpu: cpu, metricName: pkgconsts.MetricCPUCycles, value: base * 20},
			perCPUMetric{cpu: cpu, metricName: pkgconsts.MetricCPUInstructions, value: base * 10},
			perCPUMetric{cpu: cpu, metricName: pkgconsts.MetricCPUL3Misses, value: base * 100},
		)
	}
	return result
}

func metaServerWith(fetcher metrictypes.MetricsFetcher, details machine.CPUDetails) *metaserver.MetaServer {
	return &metaserver.MetaServer{MetaAgent: &agent.MetaAgent{
		MetricsFetcher: fetcher,
		KatalystMachineInfo: &machine.KatalystMachineInfo{
			CPUTopology: &machine.CPUTopology{CPUDetails: details},
		},
	}}
}

func viewWithProjection(level model.AppliedViewLevel, pools map[model.CPUSetPoolIdentity]machine.CPUSet) *model.AppliedView {
	view := &model.AppliedView{
		CPUSetPartitionView: model.NewCPUSetPartitionView(),
		Level:               level,
		PoolProjection:      model.NewAppliedPoolProjection(),
	}
	view.PoolProjection.CPUSetByIdentity = pools
	return view
}

func periodicalContext(view *model.AppliedView, emitter metrics.MetricEmitter, fetcher metrictypes.MetricsFetcher, details machine.CPUDetails) bulkheadapi.PeriodicalHandlerContext {
	return bulkheadapi.PeriodicalHandlerContext{
		Emitter:                       emitter,
		MetaServer:                    metaServerWith(fetcher, details),
		AppliedView:                   view,
		AppliedViewValidForPeriodical: true,
	}
}

func metricByKeyAndTags(t *testing.T, got []capturedMetric, key string, tags map[string]string) capturedMetric {
	t.Helper()
	for _, metric := range got {
		if metric.key == key && fmt.Sprint(metric.tags) == fmt.Sprint(tags) {
			return metric
		}
	}
	require.FailNow(t, "metric not found", "%s %#v", key, tags)
	return capturedMetric{}
}

func metricsByKey(got []capturedMetric, key string) []capturedMetric {
	result := make([]capturedMetric, 0)
	for _, metric := range got {
		if metric.key == key {
			result = append(result, metric)
		}
	}
	return result
}

func TestCPUMetricsPluginPeriodicalHandler(t *testing.T) {
	t.Parallel()

	pools := map[model.CPUSetPoolIdentity]machine.CPUSet{
		{Kind: model.CPUSetPoolKindReclaim}:                                                machine.NewCPUSet(4, 5),
		{Kind: model.CPUSetPoolKindShare, Name: "batch NUMA0"}:                             machine.NewCPUSet(0, 2),
		{Kind: model.CPUSetPoolKindDedicated, PodNamespace: "default", PodName: "api"}:     machine.NewCPUSet(1),
		{Kind: model.CPUSetPoolKindIsolation, PodNamespace: "kube-system", PodName: "qrm"}: machine.NewCPUSet(3),
		{Kind: model.CPUSetPoolKindShare, Name: "empty ignored"}:                           machine.NewCPUSet(),
	}
	details := machine.CPUDetails{
		0: {NUMANodeID: 0}, 1: {NUMANodeID: 0},
		2: {NUMANodeID: 1}, 3: {NUMANodeID: 1},
		4: {NUMANodeID: 1}, 5: {NUMANodeID: 1},
	}
	emitter := &captureEmitter{}
	err := (&CPUMetricsPlugin{}).PeriodicalHandler(context.Background(),
		periodicalContext(viewWithProjection(model.AppliedViewLevelFull, pools), emitter,
			newFetcherWithMetrics(completeSamples(0, 1, 2, 3, 4, 5)), details))
	require.NoError(t, err)

	got := emitter.snapshot()
	globalNames := []string{
		metricPoolCPUUsageRatio, metricPoolIOWaitRatio, metricPoolSchedWait,
		metricPoolIRQRatio, metricPoolCPI, metricPoolL3Misses,
	}
	numaNames := []string{
		metricPoolNUMACPUUsageRatio, metricPoolNUMAIOWaitRatio, metricPoolNUMASchedWait,
		metricPoolNUMAIRQRatio, metricPoolNUMACPI, metricPoolNUMAL3Misses,
	}
	for _, name := range globalNames {
		series := metricsByKey(got, name)
		require.Len(t, series, 4, name)
		for _, metric := range series {
			require.Len(t, metric.tags, 1)
			require.Contains(t, metric.tags, "pool_name")
		}
	}
	for _, name := range numaNames {
		series := metricsByKey(got, name)
		require.Len(t, series, 5, name)
		for _, metric := range series {
			require.Len(t, metric.tags, 2)
			require.Contains(t, metric.tags, "pool_name")
			require.Contains(t, metric.tags, "numa_id")
		}
	}

	require.Equal(t, 0.2, metricByKeyAndTags(t, got, metricPoolCPUUsageRatio,
		map[string]string{"pool_name": "batch_NUMA0"}).value)
	require.Equal(t, 400.0, metricByKeyAndTags(t, got, metricPoolL3Misses,
		map[string]string{"pool_name": "batch_NUMA0"}).value)
	require.Equal(t, 2.0, metricByKeyAndTags(t, got, metricPoolCPI,
		map[string]string{"pool_name": "batch_NUMA0"}).value)
	require.Equal(t, 0.1, metricByKeyAndTags(t, got, metricPoolNUMACPUUsageRatio,
		map[string]string{"pool_name": "batch_NUMA0", "numa_id": "0"}).value)
	require.Equal(t, 0.3, metricByKeyAndTags(t, got, metricPoolNUMACPUUsageRatio,
		map[string]string{"pool_name": "batch_NUMA0", "numa_id": "1"}).value)
	require.Equal(t, 24.0, metricByKeyAndTags(t, got, metricPoolMetricSeriesCount,
		map[string]string{"scope": "global"}).value)
	require.Equal(t, 30.0, metricByKeyAndTags(t, got, metricPoolMetricSeriesCount,
		map[string]string{"scope": "numa"}).value)
}

func TestCPUMetricsPluginMetricFamilyAndLabelContract(t *testing.T) {
	t.Parallel()

	view := viewWithProjection(model.AppliedViewLevelFull, map[model.CPUSetPoolIdentity]machine.CPUSet{
		{Kind: model.CPUSetPoolKindReclaim}: machine.NewCPUSet(0),
	})
	emitter := &captureEmitter{}
	require.NoError(t, (&CPUMetricsPlugin{}).PeriodicalHandler(context.Background(),
		periodicalContext(view, emitter, newFetcherWithMetrics(completeSamples(0)),
			machine.CPUDetails{0: {NUMANodeID: 7}})))

	wantTagsByFamily := map[string]map[string]string{
		"bulkhead_pool_cpu_usage_ratio":      {"pool_name": "reclaim"},
		"bulkhead_pool_iowait_ratio":         {"pool_name": "reclaim"},
		"bulkhead_pool_schedwait":            {"pool_name": "reclaim"},
		"bulkhead_pool_irq_ratio":            {"pool_name": "reclaim"},
		"bulkhead_pool_cpi":                  {"pool_name": "reclaim"},
		"bulkhead_pool_l3misses":             {"pool_name": "reclaim"},
		"bulkhead_pool_numa_cpu_usage_ratio": {"pool_name": "reclaim", "numa_id": "7"},
		"bulkhead_pool_numa_iowait_ratio":    {"pool_name": "reclaim", "numa_id": "7"},
		"bulkhead_pool_numa_schedwait":       {"pool_name": "reclaim", "numa_id": "7"},
		"bulkhead_pool_numa_irq_ratio":       {"pool_name": "reclaim", "numa_id": "7"},
		"bulkhead_pool_numa_cpi":             {"pool_name": "reclaim", "numa_id": "7"},
		"bulkhead_pool_numa_l3misses":        {"pool_name": "reclaim", "numa_id": "7"},
	}
	gotFamilies := make(map[string]map[string]string, len(wantTagsByFamily))
	for _, metric := range emitter.snapshot() {
		if _, ok := wantTagsByFamily[metric.key]; ok {
			require.NotContains(t, gotFamilies, metric.key)
			gotFamilies[metric.key] = metric.tags
		}
	}
	require.Equal(t, wantTagsByFamily, gotFamilies)
}

func TestCPUMetricsPluginDiagnostics(t *testing.T) {
	t.Parallel()

	view := viewWithProjection(model.AppliedViewLevelFull, map[model.CPUSetPoolIdentity]machine.CPUSet{
		{Kind: model.CPUSetPoolKindReclaim}:                                  machine.NewCPUSet(0),
		{Kind: model.CPUSetPoolKindShare, Name: "same label"}:                machine.NewCPUSet(1),
		{Kind: model.CPUSetPoolKindShare, Name: "same_label"}:                machine.NewCPUSet(2),
		{Kind: model.CPUSetPoolKindDedicated, PodNamespace: "", PodName: ""}: machine.NewCPUSet(3),
		{Kind: model.CPUSetPoolKindIsolation, PodNamespace: "", PodName: ""}: machine.NewCPUSet(4),
	})
	view.PoolProjection.UncoveredCPUs = machine.NewCPUSet(5, 6)
	view.PoolProjection.AmbiguousCPUs = machine.NewCPUSet(7)
	emitter := &captureEmitter{}
	plugin := &CPUMetricsPlugin{}
	ctx := periodicalContext(view, emitter, newFetcherWithMetrics(completeSamples(0, 1, 2, 3, 4)),
		machine.CPUDetails{0: {NUMANodeID: 0}})

	require.NoError(t, plugin.PeriodicalHandler(context.Background(), ctx))
	got := emitter.snapshot()
	require.Equal(t, 2.0, metricByKeyAndTags(t, got, metricPoolProjectionCPUCount,
		map[string]string{"status": "uncovered"}).value)
	require.Equal(t, 1.0, metricByKeyAndTags(t, got, metricPoolProjectionCPUCount,
		map[string]string{"status": "ambiguous"}).value)
	require.Equal(t, 2.0, metricByKeyAndTags(t, got, metricPoolLabelConflictCPUCount,
		map[string]string{"pool_kind": "share"}).value)
	require.Equal(t, 1.0, metricByKeyAndTags(t, got, metricPoolLabelConflictCPUCount,
		map[string]string{"pool_kind": "dedicated"}).value)
	require.Equal(t, 1.0, metricByKeyAndTags(t, got, metricPoolLabelConflictCPUCount,
		map[string]string{"pool_kind": "isolation"}).value)

	emitter.reset()
	clean := viewWithProjection(model.AppliedViewLevelFull, map[model.CPUSetPoolIdentity]machine.CPUSet{
		{Kind: model.CPUSetPoolKindReclaim}: machine.NewCPUSet(0),
	})
	ctx.AppliedView = clean
	require.NoError(t, plugin.PeriodicalHandler(context.Background(), ctx))
	got = emitter.snapshot()
	for _, status := range []string{"uncovered", "ambiguous"} {
		require.Zero(t, metricByKeyAndTags(t, got, metricPoolProjectionCPUCount,
			map[string]string{"status": status}).value)
	}
	for _, kind := range []string{"share", "dedicated", "isolation"} {
		require.Zero(t, metricByKeyAndTags(t, got, metricPoolLabelConflictCPUCount,
			map[string]string{"pool_kind": kind}).value)
	}
}

func TestCPUMetricsPluginReclaimOnlyAndNilTopology(t *testing.T) {
	t.Parallel()

	view := viewWithProjection(model.AppliedViewLevelReclaimOnly, map[model.CPUSetPoolIdentity]machine.CPUSet{
		{Kind: model.CPUSetPoolKindReclaim}:                  machine.NewCPUSet(0, 1),
		{Kind: model.CPUSetPoolKindShare, Name: "must omit"}: machine.NewCPUSet(2),
	})
	emitter := &captureEmitter{}
	require.NoError(t, (&CPUMetricsPlugin{}).PeriodicalHandler(context.Background(),
		periodicalContext(view, emitter, newFetcherWithMetrics(completeSamples(0, 1, 2)), nil)))
	got := emitter.snapshot()
	for _, metric := range got {
		require.NotEqual(t, metricPoolProjectionCPUCount, metric.key)
		require.NotEqual(t, metricPoolLabelConflictCPUCount, metric.key)
		require.NotContains(t, metric.tags, "numa_id")
		if poolName, ok := metric.tags["pool_name"]; ok {
			require.Equal(t, "reclaim", poolName)
		}
	}
	require.Len(t, metricsByKey(got, metricPoolCPUUsageRatio), 1)
	require.Equal(t, 6.0, metricByKeyAndTags(t, got, metricPoolMetricSeriesCount,
		map[string]string{"scope": "global"}).value)
	require.Zero(t, metricByKeyAndTags(t, got, metricPoolMetricSeriesCount,
		map[string]string{"scope": "numa"}).value)
}

func TestCPUMetricsPluginNilAndEmptyTopologyPreserveGlobal(t *testing.T) {
	t.Parallel()

	view := viewWithProjection(model.AppliedViewLevelFull, map[model.CPUSetPoolIdentity]machine.CPUSet{
		{Kind: model.CPUSetPoolKindReclaim}: machine.NewCPUSet(0),
	})
	fetcher := newFetcherWithMetrics(completeSamples(0))
	tests := []struct {
		name       string
		metaServer *metaserver.MetaServer
	}{
		{
			name: "nil topology",
			metaServer: &metaserver.MetaServer{MetaAgent: &agent.MetaAgent{
				MetricsFetcher:      fetcher,
				KatalystMachineInfo: &machine.KatalystMachineInfo{},
			}},
		},
		{
			name:       "empty CPU details",
			metaServer: metaServerWith(fetcher, machine.CPUDetails{}),
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			emitter := &captureEmitter{}
			require.NoError(t, (&CPUMetricsPlugin{}).PeriodicalHandler(context.Background(),
				bulkheadapi.PeriodicalHandlerContext{
					Emitter:                       emitter,
					MetaServer:                    tt.metaServer,
					AppliedView:                   view,
					AppliedViewValidForPeriodical: true,
				}))
			got := emitter.snapshot()
			require.Len(t, metricsByKey(got, metricPoolCPUUsageRatio), 1)
			require.Empty(t, metricsByKey(got, metricPoolNUMACPUUsageRatio))
			require.Equal(t, 6.0, metricByKeyAndTags(t, got, metricPoolMetricSeriesCount,
				map[string]string{"scope": "global"}).value)
			require.Zero(t, metricByKeyAndTags(t, got, metricPoolMetricSeriesCount,
				map[string]string{"scope": "numa"}).value)
		})
	}
}

func TestCPUMetricsPluginOmitsOnlyMissingSource(t *testing.T) {
	t.Parallel()

	view := viewWithProjection(model.AppliedViewLevelFull, map[model.CPUSetPoolIdentity]machine.CPUSet{
		{Kind: model.CPUSetPoolKindReclaim}: machine.NewCPUSet(0),
	})
	samples := completeSamples(0)
	filtered := samples[:0]
	for _, sample := range samples {
		if sample.metricName != pkgconsts.MetricCPUIrqRatio {
			filtered = append(filtered, sample)
		}
	}
	emitter := &captureEmitter{}
	require.NoError(t, (&CPUMetricsPlugin{}).PeriodicalHandler(context.Background(),
		periodicalContext(view, emitter, newFetcherWithMetrics(filtered), machine.CPUDetails{0: {NUMANodeID: 0}})))
	got := emitter.snapshot()
	require.Empty(t, metricsByKey(got, metricPoolIRQRatio))
	require.Empty(t, metricsByKey(got, metricPoolNUMAIRQRatio))
	require.Len(t, metricsByKey(got, metricPoolCPUUsageRatio), 1)
	require.Len(t, metricsByKey(got, metricPoolNUMACPUUsageRatio), 1)
	require.Equal(t, 5.0, metricByKeyAndTags(t, got, metricPoolMetricSeriesCount,
		map[string]string{"scope": "global"}).value)
	require.Equal(t, 5.0, metricByKeyAndTags(t, got, metricPoolMetricSeriesCount,
		map[string]string{"scope": "numa"}).value)
}

func TestCPUMetricsPluginEmitterFailure(t *testing.T) {
	t.Parallel()

	view := viewWithProjection(model.AppliedViewLevelFull, map[model.CPUSetPoolIdentity]machine.CPUSet{
		{Kind: model.CPUSetPoolKindReclaim}: machine.NewCPUSet(0),
	})
	emitter := &captureEmitter{failKey: metricPoolIOWaitRatio}
	require.NoError(t, (&CPUMetricsPlugin{}).PeriodicalHandler(context.Background(),
		periodicalContext(view, emitter, newFetcherWithMetrics(completeSamples(0)), machine.CPUDetails{0: {NUMANodeID: 0}})))
	got := emitter.snapshot()
	require.Len(t, metricsByKey(got, metricPoolIOWaitRatio), 1)
	require.Len(t, metricsByKey(got, metricPoolL3Misses), 1)
	require.Len(t, metricsByKey(got, metricPoolNUMAL3Misses), 1)
	require.Len(t, metricsByKey(got, metricPoolMetricSeriesCount), 2)
	require.Equal(t, 6.0, metricByKeyAndTags(t, got, metricPoolMetricSeriesCount,
		map[string]string{"scope": "global"}).value)
	require.Equal(t, 6.0, metricByKeyAndTags(t, got, metricPoolMetricSeriesCount,
		map[string]string{"scope": "numa"}).value)
}

func TestCPUMetricsPluginNoOps(t *testing.T) {
	t.Parallel()

	validView := viewWithProjection(model.AppliedViewLevelFull, map[model.CPUSetPoolIdentity]machine.CPUSet{
		{Kind: model.CPUSetPoolKindReclaim}: machine.NewCPUSet(0),
	})
	fetcher := newFetcherWithMetrics(completeSamples(0))
	tests := []struct {
		name string
		ctx  bulkheadapi.PeriodicalHandlerContext
	}{
		{name: "stale view", ctx: bulkheadapi.PeriodicalHandlerContext{
			Emitter: &captureEmitter{}, MetaServer: metaServerWith(fetcher, nil), AppliedView: validView,
		}},
		{name: "nil view", ctx: bulkheadapi.PeriodicalHandlerContext{
			Emitter: &captureEmitter{}, MetaServer: metaServerWith(fetcher, nil), AppliedViewValidForPeriodical: true,
		}},
		{name: "nil emitter", ctx: bulkheadapi.PeriodicalHandlerContext{
			MetaServer: metaServerWith(fetcher, nil), AppliedView: validView, AppliedViewValidForPeriodical: true,
		}},
		{name: "nil fetcher", ctx: bulkheadapi.PeriodicalHandlerContext{
			Emitter: &captureEmitter{}, MetaServer: metaServerWith(nil, nil), AppliedView: validView, AppliedViewValidForPeriodical: true,
		}},
		{name: "parent safe level", ctx: bulkheadapi.PeriodicalHandlerContext{
			Emitter: &captureEmitter{}, MetaServer: metaServerWith(fetcher, nil),
			AppliedView: viewWithProjection(model.AppliedViewLevelParentSafe, map[model.CPUSetPoolIdentity]machine.CPUSet{
				{Kind: model.CPUSetPoolKindReclaim}: machine.NewCPUSet(0),
			}), AppliedViewValidForPeriodical: true,
		}},
		{name: "empty level", ctx: bulkheadapi.PeriodicalHandlerContext{
			Emitter: &captureEmitter{}, MetaServer: metaServerWith(fetcher, nil),
			AppliedView: viewWithProjection("", map[model.CPUSetPoolIdentity]machine.CPUSet{
				{Kind: model.CPUSetPoolKindReclaim}: machine.NewCPUSet(0),
			}), AppliedViewValidForPeriodical: true,
		}},
		{name: "unknown level", ctx: bulkheadapi.PeriodicalHandlerContext{
			Emitter: &captureEmitter{}, MetaServer: metaServerWith(fetcher, nil),
			AppliedView: viewWithProjection(model.AppliedViewLevel("future"), map[model.CPUSetPoolIdentity]machine.CPUSet{
				{Kind: model.CPUSetPoolKindReclaim}: machine.NewCPUSet(0),
			}), AppliedViewValidForPeriodical: true,
		}},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.NoError(t, (&CPUMetricsPlugin{}).PeriodicalHandler(context.Background(), tt.ctx))
			if emitter, ok := tt.ctx.Emitter.(*captureEmitter); ok {
				require.Empty(t, emitter.snapshot())
			}
		})
	}
}

func TestCPUMetricsPluginEmptyProjectionDiagnostics(t *testing.T) {
	t.Parallel()

	fetcher := newFetcherWithMetrics(nil)
	tests := []struct {
		name  string
		level model.AppliedViewLevel
		want  []capturedMetric
	}{
		{
			name:  "full",
			level: model.AppliedViewLevelFull,
			want: []capturedMetric{
				{key: "bulkhead_pool_projection_cpu_count", tags: map[string]string{"status": "uncovered"}},
				{key: "bulkhead_pool_projection_cpu_count", tags: map[string]string{"status": "ambiguous"}},
				{key: "bulkhead_pool_label_conflict_cpu_count", tags: map[string]string{"pool_kind": "share"}},
				{key: "bulkhead_pool_label_conflict_cpu_count", tags: map[string]string{"pool_kind": "dedicated"}},
				{key: "bulkhead_pool_label_conflict_cpu_count", tags: map[string]string{"pool_kind": "isolation"}},
				{key: "bulkhead_pool_metric_series_count", tags: map[string]string{"scope": "global"}},
				{key: "bulkhead_pool_metric_series_count", tags: map[string]string{"scope": "numa"}},
			},
		},
		{
			name:  "reclaim only",
			level: model.AppliedViewLevelReclaimOnly,
			want: []capturedMetric{
				{key: "bulkhead_pool_metric_series_count", tags: map[string]string{"scope": "global"}},
				{key: "bulkhead_pool_metric_series_count", tags: map[string]string{"scope": "numa"}},
			},
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			emitter := &captureEmitter{}
			require.NoError(t, (&CPUMetricsPlugin{}).PeriodicalHandler(context.Background(),
				periodicalContext(viewWithProjection(tt.level, nil), emitter, fetcher, nil)))
			got := emitter.snapshot()
			require.Len(t, got, len(tt.want))
			for _, want := range tt.want {
				metric := metricByKeyAndTags(t, got, want.key, want.tags)
				require.Zero(t, metric.value)
			}
		})
	}
}

func TestCPUMetricsPluginStaticContract(t *testing.T) {
	t.Parallel()

	plugin := NewCPUMetricsPlugin(nil)
	require.Equal(t, CPUMetricsPluginName, plugin.Name())
	require.True(t, plugin.Enable(bulkheadapi.HandlerContext{}))
}

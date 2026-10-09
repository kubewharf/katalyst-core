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

// Package cpumetrics emits runtime-quality metrics projected from final applied
// pool ownership. It is read-only and never reconstructs ownership from mutable
// QRM state.
package cpumetrics

import (
	"context"
	"sort"
	"strconv"

	bulkheadapi "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/bulkhead/api"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/bulkhead/model"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	metrictypes "github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric/types"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

const CPUMetricsPluginName = "cpu_metrics"

const (
	metricPoolCPUUsageRatio = "bulkhead_pool_cpu_usage_ratio"
	metricPoolIOWaitRatio   = "bulkhead_pool_iowait_ratio"
	metricPoolSchedWait     = "bulkhead_pool_schedwait"
	metricPoolIRQRatio      = "bulkhead_pool_irq_ratio"
	metricPoolCPI           = "bulkhead_pool_cpi"
	metricPoolL3Misses      = "bulkhead_pool_l3misses"

	metricPoolNUMACPUUsageRatio = "bulkhead_pool_numa_cpu_usage_ratio"
	metricPoolNUMAIOWaitRatio   = "bulkhead_pool_numa_iowait_ratio"
	metricPoolNUMASchedWait     = "bulkhead_pool_numa_schedwait"
	metricPoolNUMAIRQRatio      = "bulkhead_pool_numa_irq_ratio"
	metricPoolNUMACPI           = "bulkhead_pool_numa_cpi"
	metricPoolNUMAL3Misses      = "bulkhead_pool_numa_l3misses"

	metricPoolProjectionCPUCount    = "bulkhead_pool_projection_cpu_count"
	metricPoolLabelConflictCPUCount = "bulkhead_pool_label_conflict_cpu_count"
	metricPoolMetricSeriesCount     = "bulkhead_pool_metric_series_count"

	poolNameTagKey = "pool_name"
	numaIDTagKey   = "numa_id"
	statusTagKey   = "status"
	poolKindTagKey = "pool_kind"
	scopeTagKey    = "scope"

	scopeGlobal = "global"
	scopeNUMA   = "numa"
)

var (
	_ bulkheadapi.Plugin            = (*CPUMetricsPlugin)(nil)
	_ bulkheadapi.PeriodicalCapable = (*CPUMetricsPlugin)(nil)
)

type metricDescriptor struct {
	globalName string
	numaName   string
	value      func(aggregateValues) *float64
}

var metricDescriptors = []metricDescriptor{
	{
		globalName: metricPoolCPUUsageRatio,
		numaName:   metricPoolNUMACPUUsageRatio,
		value:      func(values aggregateValues) *float64 { return values.cpuUsageRatio },
	},
	{
		globalName: metricPoolIOWaitRatio,
		numaName:   metricPoolNUMAIOWaitRatio,
		value:      func(values aggregateValues) *float64 { return values.ioWaitRatio },
	},
	{
		globalName: metricPoolSchedWait,
		numaName:   metricPoolNUMASchedWait,
		value:      func(values aggregateValues) *float64 { return values.schedWait },
	},
	{
		globalName: metricPoolIRQRatio,
		numaName:   metricPoolNUMAIRQRatio,
		value:      func(values aggregateValues) *float64 { return values.irqRatio },
	},
	{
		globalName: metricPoolCPI,
		numaName:   metricPoolNUMACPI,
		value:      func(values aggregateValues) *float64 { return values.cpi },
	},
	{
		globalName: metricPoolL3Misses,
		numaName:   metricPoolNUMAL3Misses,
		value:      func(values aggregateValues) *float64 { return values.l3Misses },
	},
}

type CPUMetricsPlugin struct{}

func NewCPUMetricsPlugin(_ *config.Configuration) bulkheadapi.Plugin {
	return &CPUMetricsPlugin{}
}

func (p *CPUMetricsPlugin) Name() string {
	return CPUMetricsPluginName
}

func (p *CPUMetricsPlugin) Enable(bulkheadapi.HandlerContext) bool {
	return true
}

func (p *CPUMetricsPlugin) PeriodicalHandler(_ context.Context, in bulkheadapi.PeriodicalHandlerContext) error {
	if !in.AppliedViewValidForPeriodical || in.AppliedView == nil {
		general.InfofV(6, "bulkhead cpu_metrics: applied view not valid for periodical, skipping")
		return nil
	}
	if in.AppliedView.Level != model.AppliedViewLevelFull &&
		in.AppliedView.Level != model.AppliedViewLevelReclaimOnly {
		general.InfofV(6, "bulkhead cpu_metrics: applied view level %q is not consumable, skipping",
			in.AppliedView.Level)
		return nil
	}
	if in.Emitter == nil {
		general.InfofV(6, "bulkhead cpu_metrics: nil emitter, skipping")
		return nil
	}
	fetcher := metricsFetcherFrom(in.MetaServer)
	if fetcher == nil {
		general.InfofV(6, "bulkhead cpu_metrics: nil metrics fetcher, skipping")
		return nil
	}

	projected := projectedPoolsForLevel(in.AppliedView)
	assignment := assignPoolLabels(projected)
	if len(assignment.pools) == 0 {
		p.emitDiagnostics(in.Emitter, in.AppliedView, assignment, 0, 0)
		return nil
	}

	// Collect all pool CPUs into a single set in one shot instead of folding
	// the growing union per pool (each Union copies the accumulated set).
	var unionCPUs []int
	for _, pool := range assignment.pools {
		if !pool.cpus.IsEmpty() {
			unionCPUs = append(unionCPUs, pool.cpus.ToSliceNoSortInt()...)
		}
	}
	union := machine.NewCPUSet(unionCPUs...)
	if union.IsEmpty() {
		general.InfofV(6, "bulkhead cpu_metrics: all projected pools are empty, skipping")
		p.emitDiagnostics(in.Emitter, in.AppliedView, assignment, 0, 0)
		return nil
	}

	cache := sampleRun(fetcher, union)
	globalAttempts := 0
	numaAttempts := 0
	numaCPUs := numaBuckets(in.MetaServer, union)
	// numaCPUs does not change between pools; sort its keys once instead of
	// re-sorting (and re-allocating the slice) on every pool iteration.
	sortedNUMAs := sortedNUMAIDs(numaCPUs)
	for _, pool := range assignment.pools {
		if pool.cpus.IsEmpty() {
			continue
		}
		globalAttempts += p.emitValues(in.Emitter, aggregateSamples(cache, pool.cpus), false,
			metrics.MetricTag{Key: poolNameTagKey, Val: pool.label})
		for _, numaID := range sortedNUMAs {
			intersection := pool.cpus.Intersection(numaCPUs[numaID])
			if intersection.IsEmpty() {
				continue
			}
			numaAttempts += p.emitValues(in.Emitter, aggregateSamples(cache, intersection), true,
				metrics.MetricTag{Key: poolNameTagKey, Val: pool.label},
				metrics.MetricTag{Key: numaIDTagKey, Val: strconv.Itoa(numaID)})
		}
	}
	p.emitDiagnostics(in.Emitter, in.AppliedView, assignment, globalAttempts, numaAttempts)
	return nil
}

func projectedPoolsForLevel(view *model.AppliedView) map[model.CPUSetPoolIdentity]machine.CPUSet {
	if view.Level != model.AppliedViewLevelReclaimOnly {
		return view.PoolProjection.CPUSetByIdentity
	}
	reclaim := model.CPUSetPoolIdentity{Kind: model.CPUSetPoolKindReclaim}
	cpus, ok := view.PoolProjection.CPUSetByIdentity[reclaim]
	if !ok {
		return nil
	}
	return map[model.CPUSetPoolIdentity]machine.CPUSet{reclaim: cpus}
}

func (p *CPUMetricsPlugin) emitValues(
	emitter metrics.MetricEmitter,
	values aggregateValues,
	numa bool,
	tags ...metrics.MetricTag,
) int {
	attempted := 0
	for _, descriptor := range metricDescriptors {
		value := descriptor.value(values)
		if value == nil {
			continue
		}
		name := descriptor.globalName
		if numa {
			name = descriptor.numaName
		}
		attempted++
		if err := emitter.StoreFloat64(name, *value, metrics.MetricTypeNameRaw, tags...); err != nil {
			general.Errorf("bulkhead cpu_metrics: emit %s tags=%v failed: %v", name, tags, err)
		}
	}
	return attempted
}

func (p *CPUMetricsPlugin) emitDiagnostics(
	emitter metrics.MetricEmitter,
	view *model.AppliedView,
	assignment labelAssignment,
	globalAttempts, numaAttempts int,
) {
	if view.Level == model.AppliedViewLevelFull {
		p.emitInt(emitter, metricPoolProjectionCPUCount, int64(view.PoolProjection.UncoveredCPUs.Size()),
			metrics.MetricTag{Key: statusTagKey, Val: "uncovered"})
		p.emitInt(emitter, metricPoolProjectionCPUCount, int64(view.PoolProjection.AmbiguousCPUs.Size()),
			metrics.MetricTag{Key: statusTagKey, Val: "ambiguous"})
		for _, kind := range []model.CPUSetPoolKind{
			model.CPUSetPoolKindShare,
			model.CPUSetPoolKindDedicated,
			model.CPUSetPoolKindIsolation,
		} {
			p.emitInt(emitter, metricPoolLabelConflictCPUCount, int64(assignment.conflictCPUByKind[kind].Size()),
				metrics.MetricTag{Key: poolKindTagKey, Val: string(kind)})
		}
	}
	p.emitInt(emitter, metricPoolMetricSeriesCount, int64(globalAttempts),
		metrics.MetricTag{Key: scopeTagKey, Val: scopeGlobal})
	p.emitInt(emitter, metricPoolMetricSeriesCount, int64(numaAttempts),
		metrics.MetricTag{Key: scopeTagKey, Val: scopeNUMA})
}

func (p *CPUMetricsPlugin) emitInt(
	emitter metrics.MetricEmitter,
	name string,
	value int64,
	tags ...metrics.MetricTag,
) {
	if err := emitter.StoreInt64(name, value, metrics.MetricTypeNameRaw, tags...); err != nil {
		general.Errorf("bulkhead cpu_metrics: emit %s tags=%v failed: %v", name, tags, err)
	}
}

func numaBuckets(ms *metaserver.MetaServer, cpus machine.CPUSet) map[int]machine.CPUSet {
	result := make(map[int]machine.CPUSet)
	if ms == nil || ms.MetaAgent == nil || ms.KatalystMachineInfo == nil ||
		ms.CPUTopology == nil || len(ms.CPUDetails) == 0 {
		general.InfofV(6, "bulkhead cpu_metrics: nil or empty CPU topology, skipping NUMA metrics")
		return result
	}
	// Collect CPU IDs per NUMA first, then build each bucket CPUSet once.
	// Repeated per-CPU Union would copy the accumulating set on every step
	// (O(N^2)) and allocate a fresh map per CPU.
	byNUMA := make(map[int][]int)
	for _, cpu := range cpus.ToSliceInt() {
		detail, ok := ms.CPUDetails[cpu]
		if !ok {
			continue
		}
		byNUMA[detail.NUMANodeID] = append(byNUMA[detail.NUMANodeID], cpu)
	}
	for numaID, ids := range byNUMA {
		result[numaID] = machine.NewCPUSet(ids...)
	}
	return result
}

func sortedNUMAIDs(byNUMA map[int]machine.CPUSet) []int {
	result := make([]int, 0, len(byNUMA))
	for numaID := range byNUMA {
		result = append(result, numaID)
	}
	sort.Ints(result)
	return result
}

func metricsFetcherFrom(ms *metaserver.MetaServer) metrictypes.MetricsFetcher {
	if ms == nil || ms.MetaAgent == nil {
		return nil
	}
	return ms.MetricsFetcher
}

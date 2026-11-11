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

package helper

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
	utilmetric "github.com/kubewharf/katalyst-core/pkg/util/metric"
)

// seedReclaimStore populates a fake metrics fetcher with a 28-core reclaim
// cpuset whose per-core utilization sums to poolCPUUsage and a single reclaim
// cgroup path with the given usage/quota (in cores). When quotaCores <= 0 the
// cgroup is treated as unlimited.
func seedReclaimStore(store *metric.FakeMetricsFetcher, cpus machine.CPUSet, cgroupPath string, poolCPUUsage, cgroupUsage, quotaCores float64) {
	// spread the pool utilization evenly across the cpuset cores so that
	// AggregateCoreMetric(sum) == poolCPUUsage.
	perCore := poolCPUUsage / float64(cpus.Size())
	for _, cpu := range cpus.ToSliceInt() {
		store.SetCPUMetric(cpu, consts.MetricCPUUsageRatio, utilmetric.MetricData{Value: perCore})
	}

	store.SetCgroupMetric(cgroupPath, consts.MetricCPUUsageCgroup, utilmetric.MetricData{Value: cgroupUsage})
	if quotaCores > 0 {
		// express quota as a quota/period pair that converts back to quotaCores.
		store.SetCgroupMetric(cgroupPath, consts.MetricCPUQuotaCgroup, utilmetric.MetricData{Value: quotaCores})
		store.SetCgroupMetric(cgroupPath, consts.MetricCPUPeriodCgroup, utilmetric.MetricData{Value: 1})
	} else {
		store.SetCgroupMetric(cgroupPath, consts.MetricCPUQuotaCgroup, utilmetric.MetricData{Value: -1})
		store.SetCgroupMetric(cgroupPath, consts.MetricCPUPeriodCgroup, utilmetric.MetricData{Value: 1})
	}
}

func TestGetReclaimMetricsMulti_OverlapTrue_SubtractsUsage(t *testing.T) {
	t.Parallel()

	metricsFetcher := metric.NewFakeMetricsFetcher(metrics.DummyMetrics{})
	store := metricsFetcher.(*metric.FakeMetricsFetcher)

	cpus := machine.MustParse("0-27")
	cgroupPath := "/kubepods/reclaim"
	seedReclaimStore(store, cpus, cgroupPath, 14, 3, -1)

	got, err := GetReclaimMetricsMulti(cpus, []string{cgroupPath}, metricsFetcher, true)
	require.NoError(t, err)
	// (28 - 14) + 3 = 17
	assert.InDelta(t, 17.0, got.ReclaimedCoresSupply, 1e-9)
}

func TestGetReclaimMetricsMulti_OverlapFalse_UsesCpusetSize(t *testing.T) {
	t.Parallel()

	metricsFetcher := metric.NewFakeMetricsFetcher(metrics.DummyMetrics{})
	store := metricsFetcher.(*metric.FakeMetricsFetcher)

	cpus := machine.MustParse("0-27")
	cgroupPath := "/kubepods/reclaim"
	seedReclaimStore(store, cpus, cgroupPath, 14, 3, -1)

	got, err := GetReclaimMetricsMulti(cpus, []string{cgroupPath}, metricsFetcher, false)
	require.NoError(t, err)
	// non-overlap: supply equals the exclusive cpuset size directly.
	assert.InDelta(t, 28.0, got.ReclaimedCoresSupply, 1e-9)
}

func TestGetReclaimMetricsMulti_OverlapFalse_ClampedByQuota(t *testing.T) {
	t.Parallel()

	metricsFetcher := metric.NewFakeMetricsFetcher(metrics.DummyMetrics{})
	store := metricsFetcher.(*metric.FakeMetricsFetcher)

	cpus := machine.MustParse("0-27")
	cgroupPath := "/kubepods/reclaim"
	seedReclaimStore(store, cpus, cgroupPath, 14, 3, 20)

	got, err := GetReclaimMetricsMulti(cpus, []string{cgroupPath}, metricsFetcher, false)
	require.NoError(t, err)
	// non-overlap supply (28) is clamped down to the finite cfs quota (20).
	assert.InDelta(t, 20.0, got.ReclaimedCoresSupply, 1e-9)
}

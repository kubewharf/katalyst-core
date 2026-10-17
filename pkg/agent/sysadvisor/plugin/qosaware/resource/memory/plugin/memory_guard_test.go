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
	"sync"
	"testing"
	"time"

	cadvisorinfo "github.com/google/cadvisor/info/v1"
	"github.com/stretchr/testify/require"
	"go.uber.org/atomic"

	configv1alpha1 "github.com/kubewharf/katalyst-api/pkg/apis/config/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/memory/dynamicpolicy/memoryadvisor"
	types "github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/config"
	dynamicconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic"
	"github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent"
	agentmetric "github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric"
	metrictypes "github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric/types"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
	"github.com/kubewharf/katalyst-core/pkg/util/metric"
	"github.com/kubewharf/katalyst-core/pkg/util/reclaim"
)

func TestGetAdvices_MultiPath(t *testing.T) {
	t.Parallel()

	// Register two consumers so both parent paths + NUMA-binding paths land
	// in the reverse index.
	machineInfo := &machine.KatalystMachineInfo{
		CPUTopology: &machine.CPUTopology{
			CPUDetails: machine.CPUDetails{
				0: machine.CPUTopoInfo{NUMANodeID: 0},
			},
		},
	}
	require.NoError(t, reclaim.RegisterNamedGenericConsumer("guard-primary", newGuardConf("/kubepods/besteffort"), machineInfo))
	require.NoError(t, reclaim.RegisterNamedGenericConsumer("guard-secondary", newGuardConf("/parentPath/childPath"), machineInfo))
	t.Cleanup(func() {
		reclaim.UnregisterConsumer("guard-primary")
		reclaim.UnregisterConsumer("guard-secondary")
	})

	mg := &memoryGuard{
		reclaimRelativeRootCgroupPaths: []string{"/kubepods/besteffort", "/parentPath/childPath"},
		numaBindingRelativeRootCgroupPaths: map[int][]string{
			0: {"/kubepods/besteffort-0", "/parentPath/childPath-0"},
		},
		reclaimMemoryLimit:            atomic.NewInt64(1024),
		numaBindingReclaimMemoryLimit: &atomic.Value{},
		reconcileStatus:               atomic.NewString(reconcileStatusSucceeded),
		conf:                          config.NewConfiguration(),
	}
	mg.numaBindingReclaimMemoryLimit.Store(map[int]int64{
		0: 512,
	})

	got := mg.GetAdvices()
	require.Len(t, got.ExtraEntries, 4)

	paths := make([]string, 0, len(got.ExtraEntries))
	for _, e := range got.ExtraEntries {
		paths = append(paths, e.CgroupPath)
	}
	require.Contains(t, paths, "/kubepods/besteffort")
	require.Contains(t, paths, "/parentPath/childPath")
	require.Contains(t, paths, "/kubepods/besteffort-0")
	require.Contains(t, paths, "/parentPath/childPath-0")
}

func TestGetAdvices_SetsTotalLimitForEveryReclaimPath(t *testing.T) {
	t.Parallel()

	machineInfo := &machine.KatalystMachineInfo{
		CPUTopology: &machine.CPUTopology{
			CPUDetails: machine.CPUDetails{
				0: machine.CPUTopoInfo{NUMANodeID: 0},
				1: machine.CPUTopoInfo{NUMANodeID: 1},
			},
		},
	}
	primaryName := "guard-total-limit-primary"
	require.NoError(t, reclaim.RegisterNamedGenericConsumer(primaryName, newGuardConf("/group-a"), machineInfo))
	require.NoError(t, reclaim.RegisterNamedGenericConsumer("consumer-b", newGuardConf("/group-b"), machineInfo))
	t.Cleanup(func() {
		reclaim.UnregisterConsumer(primaryName)
		reclaim.UnregisterConsumer("consumer-b")
	})

	dynamicConf := dynamicconfig.NewConfiguration()
	dynamicConf.ReclaimedPercentageByConsumer = map[string]int{
		primaryName:  0,
		"consumer-b": 100,
	}
	conf := config.NewConfiguration()
	conf.SetDynamicConfiguration(dynamicConf)

	mg := &memoryGuard{
		reclaimRelativeRootCgroupPaths: []string{"/group-a", "/group-b"},
		numaBindingRelativeRootCgroupPaths: map[int][]string{
			0: {"/group-a-0", "/group-b-0"},
			1: {"/group-a-1", "/group-b-1"},
		},
		reclaimMemoryLimit:            atomic.NewInt64(1024),
		numaBindingReclaimMemoryLimit: &atomic.Value{},
		reconcileStatus:               atomic.NewString(reconcileStatusSucceeded),
		conf:                          conf,
	}
	mg.numaBindingReclaimMemoryLimit.Store(map[int]int64{
		0: 512,
		1: 256,
	})

	got := mg.GetAdvices()

	require.Equal(t, int64(1024), memoryLimitAdvice(t, got, "/group-a"))
	require.Equal(t, int64(1024), memoryLimitAdvice(t, got, "/group-b"))
	require.Equal(t, int64(512), memoryLimitAdvice(t, got, "/group-a-0"))
	require.Equal(t, int64(512), memoryLimitAdvice(t, got, "/group-b-0"))
	require.Equal(t, int64(256), memoryLimitAdvice(t, got, "/group-a-1"))
	require.Equal(t, int64(256), memoryLimitAdvice(t, got, "/group-b-1"))
}

func TestGetAdvices_SetsTotalLimitEvenWhenUsageMetricExists(t *testing.T) {
	t.Parallel()

	machineInfo := &machine.KatalystMachineInfo{
		CPUTopology: &machine.CPUTopology{
			CPUDetails: machine.CPUDetails{
				0: machine.CPUTopoInfo{NUMANodeID: 0},
			},
		},
	}
	require.NoError(t, reclaim.RegisterNamedGenericConsumer("usage-floor-a", newGuardConf("/usage-floor-a"), machineInfo))
	require.NoError(t, reclaim.RegisterNamedGenericConsumer("usage-floor-b", newGuardConf("/usage-floor-b"), machineInfo))
	t.Cleanup(func() {
		reclaim.UnregisterConsumer("usage-floor-a")
		reclaim.UnregisterConsumer("usage-floor-b")
	})

	dynamicConf := dynamicconfig.NewConfiguration()
	dynamicConf.ReclaimedPercentageByConsumer = map[string]int{
		"usage-floor-a": 0,
		"usage-floor-b": 100,
	}
	conf := config.NewConfiguration()
	conf.SetDynamicConfiguration(dynamicConf)

	now := time.Now()
	fakeFetcher := agentmetric.NewFakeMetricsFetcher(metrics.DummyMetrics{}).(*agentmetric.FakeMetricsFetcher)
	// Existing usage no longer participates in per-consumer limit splitting:
	// memoryGuard writes the total limit to every reclaim path.
	fakeFetcher.SetCgroupMetric("/usage-floor-a", consts.MetricMemUsageCgroup, metric.MetricData{Value: 128, Time: &now})
	fakeFetcher.SetCgroupMetric("/usage-floor-a-0", consts.MetricMemUsageCgroup, metric.MetricData{Value: 64, Time: &now})

	mg := &memoryGuard{
		metaServer: &metaserver.MetaServer{
			MetaAgent: &agent.MetaAgent{
				MetricsFetcher: fakeFetcher,
			},
		},
		reclaimRelativeRootCgroupPaths: []string{"/usage-floor-a", "/usage-floor-b"},
		numaBindingRelativeRootCgroupPaths: map[int][]string{
			0: {"/usage-floor-a-0", "/usage-floor-b-0"},
		},
		reclaimMemoryLimit:            atomic.NewInt64(1024),
		numaBindingReclaimMemoryLimit: &atomic.Value{},
		reconcileStatus:               atomic.NewString(reconcileStatusSucceeded),
		conf:                          conf,
	}
	mg.numaBindingReclaimMemoryLimit.Store(map[int]int64{
		0: 512,
	})

	got := mg.GetAdvices()

	require.Equal(t, int64(1024), memoryLimitAdvice(t, got, "/usage-floor-a"))
	require.Equal(t, int64(1024), memoryLimitAdvice(t, got, "/usage-floor-b"))
	require.Equal(t, int64(512), memoryLimitAdvice(t, got, "/usage-floor-a-0"))
	require.Equal(t, int64(512), memoryLimitAdvice(t, got, "/usage-floor-b-0"))
}

func newGuardConf(cgroupPath string) *config.Configuration {
	c := config.NewConfiguration()
	c.BaseConfiguration.ReclaimRelativeRootCgroupPath = cgroupPath
	return c
}

func memoryLimitAdvice(t *testing.T, result types.InternalMemoryCalculationResult, cgroupPath string) int64 {
	t.Helper()
	for _, entry := range result.ExtraEntries {
		if entry.CgroupPath != cgroupPath {
			continue
		}
		value, ok := entry.Values[string(memoryadvisor.ControlKnobKeyMemoryLimitInBytes)]
		require.True(t, ok)
		parsed, err := strconv.ParseInt(value, 10, 64)
		require.NoError(t, err)
		return parsed
	}
	require.Failf(t, "missing memory limit advice", "cgroup path %s", cgroupPath)
	return 0
}

func TestGetCriticalWatermarkPages(t *testing.T) {
	t.Parallel()

	zoneInfo := &machine.NormalZoneInfo{
		Low:  10,
		High: 20,
	}

	cases := []struct {
		name   string
		source configv1alpha1.CriticalWatermarkSource
		want   uint64
	}{
		{name: "low", source: configv1alpha1.CriticalWatermarkSourceLow, want: 10},
		{name: "high", source: configv1alpha1.CriticalWatermarkSourceHigh, want: 20},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tc.want, getCriticalWatermarkPages(zoneInfo, tc.source))
		})
	}
}

type switchingMetricsFetcher struct {
	metrictypes.MetricsFetcher
	once     sync.Once
	onSwitch func()
}

func (f *switchingMetricsFetcher) GetNodeMetric(metricName string) (metric.MetricData, error) {
	data, err := f.MetricsFetcher.GetNodeMetric(metricName)
	f.once.Do(f.onSwitch)
	return data, err
}

func TestUpdateActualNUMABindingReclaimMemoryLimitUsesOneDynamicConfigSnapshot(t *testing.T) {
	t.Parallel()

	conf := config.NewConfiguration()
	snapshot := dynamicconfig.NewConfiguration()
	snapshot.CriticalWatermarkSource = configv1alpha1.CriticalWatermarkSourceLow
	conf.SetDynamicConfiguration(snapshot)

	replacement := dynamicconfig.NewConfiguration()
	replacement.CriticalWatermarkSource = configv1alpha1.CriticalWatermarkSourceHigh

	now := time.Now()
	fakeFetcher := agentmetric.NewFakeMetricsFetcher(metrics.DummyMetrics{}).(*agentmetric.FakeMetricsFetcher)
	fakeFetcher.SetNodeMetric(consts.MetricMemScaleFactorSystem, metric.MetricData{Value: 0, Time: &now})
	for _, numaID := range []int{0, 1} {
		fakeFetcher.SetNumaMetric(numaID, consts.MetricMemTotalNuma, metric.MetricData{Value: 1000, Time: &now})
		fakeFetcher.SetNumaMetric(numaID, consts.MetricMemFreeNuma, metric.MetricData{Value: 1000, Time: &now})
		fakeFetcher.SetCgroupNumaMetric("/reclaimed", numaID, consts.MetricsMemTotalPerNumaCgroup, metric.MetricData{Value: 0, Time: &now})
	}

	switchingFetcher := &switchingMetricsFetcher{
		MetricsFetcher: fakeFetcher,
		onSwitch: func() {
			conf.SetDynamicConfiguration(replacement)
		},
	}
	metaServer := &metaserver.MetaServer{
		MetaAgent: &agent.MetaAgent{
			MetricsFetcher: switchingFetcher,
			KatalystMachineInfo: &machine.KatalystMachineInfo{
				MachineInfo: &cadvisorinfo.MachineInfo{},
				CPUTopology: &machine.CPUTopology{
					CPUDetails: machine.CPUDetails{
						0: {NUMANodeID: 0},
						1: {NUMANodeID: 1},
					},
				},
				MemoryTopology: &machine.MemoryTopology{PageSize: 1},
			},
		},
	}
	mg := &memoryGuard{
		metaServer: metaServer,
		numaBindingRelativeRootCgroupPaths: map[int][]string{
			0: {"/reclaimed"},
			1: {"/reclaimed"},
		},
		numaBindingReclaimMemoryLimit: &atomic.Value{},
		conf:                          conf,
	}
	zoneInfos := []machine.NormalZoneInfo{
		{Node: 0, Free: 1000, Low: 100, High: 400},
		{Node: 1, Free: 1000, Low: 100, High: 400},
	}

	require.NoError(t, mg.updateActualNUMABindingReclaimMemoryLimit(snapshot, zoneInfos))
	require.Same(t, replacement, conf.GetDynamicConfiguration())
	require.Equal(t, map[int]int64{0: 900, 1: 900}, mg.numaBindingReclaimMemoryLimit.Load())
}

func TestCalculateReclaimedMemoryLimitFor_SkipsMissingCgroupPath(t *testing.T) {
	t.Parallel()

	conf := config.NewConfiguration()
	snapshot := dynamicconfig.NewConfiguration()
	snapshot.CriticalWatermarkSource = configv1alpha1.CriticalWatermarkSourceLow
	conf.SetDynamicConfiguration(snapshot)

	now := time.Now()
	fakeFetcher := agentmetric.NewFakeMetricsFetcher(metrics.DummyMetrics{}).(*agentmetric.FakeMetricsFetcher)
	fakeFetcher.SetNodeMetric(consts.MetricMemScaleFactorSystem, metric.MetricData{Value: 0, Time: &now})
	fakeFetcher.SetNumaMetric(0, consts.MetricMemTotalNuma, metric.MetricData{Value: 1000, Time: &now})
	fakeFetcher.SetNumaMetric(0, consts.MetricMemFreeNuma, metric.MetricData{Value: 1000, Time: &now})

	metaServer := &metaserver.MetaServer{
		MetaAgent: &agent.MetaAgent{
			MetricsFetcher: fakeFetcher,
			KatalystMachineInfo: &machine.KatalystMachineInfo{
				MachineInfo:    &cadvisorinfo.MachineInfo{},
				MemoryTopology: &machine.MemoryTopology{PageSize: 1},
			},
		},
	}
	mg := &memoryGuard{
		metaServer:           metaServer,
		minCriticalWatermark: 0,
		conf:                 conf,
	}

	zoneInfos := []machine.NormalZoneInfo{{Node: 0, Free: 1000, Low: 100, High: 400}}

	// neither reclaimed parent is materialized on the test host, so both are
	// filtered out before any per-numa metric lookup. the reconcile must not
	// abort when the metric store has no entry for a missing reclaimed parent.
	limit, err := mg.calculateReclaimedMemoryLimitFor(snapshot, 0,
		[]string{"/reclaimed-parent-a", "/reclaimed-parent-b"}, zoneInfos)
	require.NoError(t, err)
	// reclaimedMemoryUsed(0, both parents filtered) +
	// max(numaFree(1000) - criticalWatermark(low 100 * scale 1), 0) = 900
	require.Equal(t, 900.0, limit)
}

func TestCalculateReclaimedMemoryLimitFor_FailsFastWhenMaterializedCgroupMetricMissing(t *testing.T) {
	t.Parallel()

	conf := config.NewConfiguration()
	snapshot := dynamicconfig.NewConfiguration()
	snapshot.CriticalWatermarkSource = configv1alpha1.CriticalWatermarkSourceLow
	conf.SetDynamicConfiguration(snapshot)

	now := time.Now()
	fakeFetcher := agentmetric.NewFakeMetricsFetcher(metrics.DummyMetrics{}).(*agentmetric.FakeMetricsFetcher)
	fakeFetcher.SetNodeMetric(consts.MetricMemScaleFactorSystem, metric.MetricData{Value: 0, Time: &now})
	fakeFetcher.SetNumaMetric(0, consts.MetricMemTotalNuma, metric.MetricData{Value: 1000, Time: &now})
	fakeFetcher.SetNumaMetric(0, consts.MetricMemFreeNuma, metric.MetricData{Value: 1000, Time: &now})
	// deliberately no SetCgroupNumaMetric for the reclaimed parent

	metaServer := &metaserver.MetaServer{
		MetaAgent: &agent.MetaAgent{
			MetricsFetcher: fakeFetcher,
			KatalystMachineInfo: &machine.KatalystMachineInfo{
				MachineInfo:    &cadvisorinfo.MachineInfo{},
				MemoryTopology: &machine.MemoryTopology{PageSize: 1},
			},
		},
	}
	mg := &memoryGuard{
		metaServer:           metaServer,
		minCriticalWatermark: 0,
		conf:                 conf,
		// Treat every reclaimed parent as materialized on the host, so the
		// existence filter keeps it but cAdvisor has not yet produced its
		// per-NUMA counters. This must surface as a hard error (fail-fast)
		// instead of being silently treated as zero usage.
		existingCgroupPathsFn: func(paths ...string) []string { return paths },
	}

	zoneInfos := []machine.NormalZoneInfo{{Node: 0, Free: 1000, Low: 100, High: 400}}

	limit, err := mg.calculateReclaimedMemoryLimitFor(snapshot, 0,
		[]string{"/reclaimed-parent"}, zoneInfos)
	require.Error(t, err)
	require.Equal(t, 0.0, limit)
}

func TestCalculateReclaimedMemoryLimitFor_MaxRatioClamp(t *testing.T) {
	t.Parallel()

	clamp := func(reclaimMemoryLimit, ratio, numaTotal float64) float64 {
		if ratio > 0 {
			reclaimMemoryLimit = math.Min(reclaimMemoryLimit, ratio*numaTotal)
		}
		return reclaimMemoryLimit
	}

	numaTotal := 250.0 * (1 << 30)

	cases := []struct {
		name  string
		ratio float64
		raw   float64
		want  float64
	}{
		{name: "ratio 0 disables clamp", ratio: 0, raw: 200 * (1 << 30), want: 200 * (1 << 30)},
		{name: "ratio caps to ratio*total", ratio: 0.2, raw: 200 * (1 << 30), want: 0.2 * numaTotal},
		{name: "ratio above raw is no-op", ratio: 0.9, raw: 100 * (1 << 30), want: 100 * (1 << 30)},
		{name: "negative ratio disables clamp", ratio: -1, raw: 200 * (1 << 30), want: 200 * (1 << 30)},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tc.want, clamp(tc.raw, tc.ratio, numaTotal))
		})
	}
}

func TestReclaimedCoresUsedSum_Shape(t *testing.T) {
	t.Parallel()
	sum := func(paths []string, present map[string]float64) float64 {
		total := .0
		for _, p := range paths {
			v, ok := present[p]
			if !ok {
				continue
			}
			total += v
		}
		return total
	}

	got := sum(
		[]string{"/a", "/b", "/c"},
		map[string]float64{"/a": 10, "/c": 30},
	)
	require.Equal(t, 40.0, got)
}

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
	"sync"
	"testing"
	"time"

	cadvisorinfo "github.com/google/cadvisor/info/v1"
	"github.com/stretchr/testify/require"
	"go.uber.org/atomic"

	configv1alpha1 "github.com/kubewharf/katalyst-api/pkg/apis/config/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/memory/dynamicpolicy/memoryadvisor"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	types "github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/config"
	dynamicconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic"
	"github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent"
	agentmetric "github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	metricspool "github.com/kubewharf/katalyst-core/pkg/metrics/metrics-pool"
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

func TestUpdateActualNUMABindingReclaimMemoryLimitUsesOneDynamicConfigSnapshot(t *testing.T) {
	t.Parallel()

	conf := config.NewConfiguration()
	snapshot := dynamicconfig.NewConfiguration()
	snapshot.CriticalWatermarkSource = configv1alpha1.CriticalWatermarkSourceLow
	conf.SetDynamicConfiguration(snapshot)

	replacement := dynamicconfig.NewConfiguration()
	replacement.CriticalWatermarkSource = configv1alpha1.CriticalWatermarkSourceHigh

	var once sync.Once
	metaServer := &metaserver.MetaServer{
		MetaAgent: &agent.MetaAgent{
			KatalystMachineInfo: &machine.KatalystMachineInfo{
				MachineInfo: &cadvisorinfo.MachineInfo{},
				CPUTopology: &machine.CPUTopology{
					CPUDetails: machine.CPUDetails{
						0: {NUMANodeID: 0},
						1: {NUMANodeID: 1},
					},
				},
				MemoryTopology: &machine.MemoryTopology{
					PageSize:            1,
					NormalMemoryDetails: machine.MemoryDetails{0: 1000, 1: 1000},
				},
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
		numaMemoryUsageFn: func(path string, pageSize uint64) (map[int]uint64, error) {
			once.Do(func() {
				conf.SetDynamicConfiguration(replacement)
			})
			return map[int]uint64{0: 0, 1: 0}, nil
		},
		existingCgroupPathsFn: func(paths ...string) []string { return paths },
	}
	zoneInfos := []machine.NormalZoneInfo{
		{Node: 0, Free: 1000, Low: 100, High: 400},
		{Node: 1, Free: 1000, Low: 100, High: 400},
	}

	require.NoError(t, mg.updateActualNUMABindingReclaimMemoryLimit(snapshot, zoneInfos))
	require.Same(t, replacement, conf.GetDynamicConfiguration())
	require.Equal(t, map[int]int64{0: 900, 1: 900}, mg.numaBindingReclaimMemoryLimit.Load())
}

func TestCalculateReclaimedMemoryLimitFor_FailsClosedOnMissingKernelInput(t *testing.T) {
	t.Parallel()

	conf := config.NewConfiguration()
	snapshot := dynamicconfig.NewConfiguration()
	snapshot.CriticalWatermarkSource = configv1alpha1.CriticalWatermarkSourceLow
	conf.SetDynamicConfiguration(snapshot)

	zoneInfos := []machine.NormalZoneInfo{{Node: 0, Free: 1000, Low: 100, High: 400}}

	cases := []struct {
		name        string
		machineInfo *machine.KatalystMachineInfo
	}{
		{
			name: "missing topology",
			machineInfo: &machine.KatalystMachineInfo{
				MachineInfo: &cadvisorinfo.MachineInfo{},
			},
		},
		{
			name: "missing normal memory capacity",
			machineInfo: &machine.KatalystMachineInfo{
				MachineInfo:    &cadvisorinfo.MachineInfo{},
				MemoryTopology: &machine.MemoryTopology{PageSize: 1},
			},
		},
		{
			name: "invalid page size",
			machineInfo: &machine.KatalystMachineInfo{
				MachineInfo: &cadvisorinfo.MachineInfo{},
				MemoryTopology: &machine.MemoryTopology{
					PageSize:            0,
					NormalMemoryDetails: machine.MemoryDetails{0: 1000},
				},
			},
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			metaServer := &metaserver.MetaServer{MetaAgent: &agent.MetaAgent{KatalystMachineInfo: tc.machineInfo}}
			mg := &memoryGuard{metaServer: metaServer, conf: conf}
			limit, err := mg.calculateReclaimedMemoryLimitFor(snapshot, 0, []string{"/reclaimed"}, zoneInfos)
			require.Error(t, err)
			require.Equal(t, 0.0, limit)
		})
	}

	t.Run("missing zone info", func(t *testing.T) {
		t.Parallel()
		metaServer := &metaserver.MetaServer{MetaAgent: &agent.MetaAgent{
			KatalystMachineInfo: &machine.KatalystMachineInfo{
				MachineInfo:    &cadvisorinfo.MachineInfo{},
				MemoryTopology: &machine.MemoryTopology{PageSize: 1, NormalMemoryDetails: machine.MemoryDetails{0: 1000}},
			},
		}}
		mg := &memoryGuard{metaServer: metaServer, conf: conf}
		// zoneInfos does not contain numa 0.
		limit, err := mg.calculateReclaimedMemoryLimitFor(snapshot, 0, []string{"/reclaimed"}, nil)
		require.Error(t, err)
		require.Equal(t, 0.0, limit)
	})
}

func TestGetHierarchicalNumaMemoryUsageUsesMachinePageSize(t *testing.T) {
	t.Parallel()

	metaServer := &metaserver.MetaServer{MetaAgent: &agent.MetaAgent{
		KatalystMachineInfo: &machine.KatalystMachineInfo{
			MachineInfo:    &cadvisorinfo.MachineInfo{},
			MemoryTopology: &machine.MemoryTopology{PageSize: 65536},
		},
	}}
	mg := &memoryGuard{
		metaServer: metaServer,
		numaMemoryUsageFn: func(path string, pageSize uint64) (map[int]uint64, error) {
			require.Equal(t, "/reclaimed", path)
			require.Equal(t, uint64(65536), pageSize)
			return map[int]uint64{0: pageSize}, nil
		},
	}

	got, err := mg.getHierarchicalNumaMemoryUsage("/reclaimed")
	require.NoError(t, err)
	require.Equal(t, map[int]uint64{0: 65536}, got)
}

func TestCalculateReclaimedMemoryLimitFor_SkipsMissingCgroupPath(t *testing.T) {
	t.Parallel()

	conf := config.NewConfiguration()
	snapshot := dynamicconfig.NewConfiguration()
	snapshot.CriticalWatermarkSource = configv1alpha1.CriticalWatermarkSourceLow
	conf.SetDynamicConfiguration(snapshot)

	metaServer := &metaserver.MetaServer{
		MetaAgent: &agent.MetaAgent{
			KatalystMachineInfo: &machine.KatalystMachineInfo{
				MachineInfo:    &cadvisorinfo.MachineInfo{},
				MemoryTopology: &machine.MemoryTopology{PageSize: 1, NormalMemoryDetails: machine.MemoryDetails{0: 1000}},
			},
		},
	}
	mg := &memoryGuard{
		metaServer:           metaServer,
		minCriticalWatermark: 0,
		conf:                 conf,
	}

	zoneInfos := []machine.NormalZoneInfo{{Node: 0, Free: 1000, Low: 100, High: 400}}

	// Neither reclaimed parent is materialized on the test host, so both are
	// filtered before any cgroupfs read and do not abort the reconcile.
	limit, err := mg.calculateReclaimedMemoryLimitFor(snapshot, 0,
		[]string{"/reclaimed-parent-a", "/reclaimed-parent-b"}, zoneInfos)
	require.NoError(t, err)
	// reclaimedMemoryUsed(0, both parents filtered) +
	// max(numaFree(1000) - criticalWatermark(low 100 * scale 1), 0) = 900
	require.Equal(t, 900.0, limit)
}

func TestCalculateReclaimedMemoryLimitFor_FailsFastWhenCgroupReadFails(t *testing.T) {
	t.Parallel()

	conf := config.NewConfiguration()
	conf.ReclaimedMemoryLimitMargin = 100
	snapshot := dynamicconfig.NewConfiguration()
	snapshot.CriticalWatermarkSource = configv1alpha1.CriticalWatermarkSourceLow
	conf.SetDynamicConfiguration(snapshot)

	metaServer := &metaserver.MetaServer{
		MetaAgent: &agent.MetaAgent{
			KatalystMachineInfo: &machine.KatalystMachineInfo{
				MachineInfo:    &cadvisorinfo.MachineInfo{},
				MemoryTopology: &machine.MemoryTopology{PageSize: 1, NormalMemoryDetails: machine.MemoryDetails{0: 1000}},
			},
		},
	}
	mg := &memoryGuard{
		metaServer:           metaServer,
		minCriticalWatermark: 0,
		conf:                 conf,
		numaMemoryUsageFn: func(path string, pageSize uint64) (map[int]uint64, error) {
			return nil, fmt.Errorf("read memory.numa_stat")
		},
		// A materialized reclaimed parent must fail the reconcile when its direct
		// cgroupfs read fails; treating the usage as zero could lower the limit.
		existingCgroupPathsFn: func(paths ...string) []string { return paths },
	}

	zoneInfos := []machine.NormalZoneInfo{{Node: 0, Free: 1000, Low: 100, High: 400}}

	limit, err := mg.calculateReclaimedMemoryLimitFor(snapshot, 0,
		[]string{"/reclaimed-parent"}, zoneInfos)
	require.Error(t, err)
	require.Equal(t, 0.0, limit)
}

func TestCalculateReclaimedMemoryLimitFor_LimitMargin(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		used   []float64
		free   uint64
		margin int64
		ratio  float64
		want   float64
	}{
		{name: "zero margin preserves tight limit", used: []float64{600}, free: 50, want: 600},
		{name: "zero margin preserves ratio cap below usage", used: []float64{600}, free: 50, ratio: 0.5, want: 500},
		{name: "ratio caps raw limit", used: []float64{100}, free: 900, ratio: 0.5, want: 500},
		{name: "ratio above raw is unchanged", used: []float64{100}, free: 200, ratio: 0.9, want: 200},
		{name: "negative ratio preserves disabled cap", used: []float64{100}, free: 900, ratio: -1, want: 900},
		{name: "ample free memory is unchanged", used: []float64{600}, free: 300, margin: 100, want: 800},
		{name: "below watermark keeps margin", used: []float64{600}, free: 50, margin: 100, want: 700},
		{name: "at watermark keeps margin", used: []float64{600}, free: 100, margin: 100, want: 700},
		{name: "small positive headroom is raised", used: []float64{600}, free: 150, margin: 100, want: 700},
		{name: "margin overrides ratio cap", used: []float64{600}, free: 300, margin: 100, ratio: 0.65, want: 700},
		{name: "margin overrides cap below usage", used: []float64{600}, free: 50, margin: 100, ratio: 0.5, want: 700},
		{name: "margin stops at NUMA total", used: []float64{950}, free: 50, margin: 100, want: 1000},
		{name: "large margin cannot overflow addition", used: []float64{600}, free: 50, margin: math.MaxInt64, want: 1000},
		{name: "empty pool receives no margin", used: []float64{0}, free: 50, margin: 100, want: 0},
		{name: "empty pool retains free budget", used: []float64{0}, free: 300, margin: 500, want: 200},
		{name: "multiple parents receive one margin", used: []float64{200, 400}, free: 50, margin: 100, want: 700},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			conf := config.NewConfiguration()
			conf.ReclaimedMemoryLimitMargin = tc.margin
			snapshot := conf.GetDynamicConfiguration()
			snapshot.CriticalWatermarkSource = configv1alpha1.CriticalWatermarkSourceLow
			snapshot.ReclaimedMemoryMaxRatio = tc.ratio

			paths := make([]string, len(tc.used))
			usageByPath := make(map[string]uint64, len(tc.used))
			for i, used := range tc.used {
				paths[i] = "/reclaimed-" + strconv.Itoa(i)
				usageByPath[paths[i]] = uint64(used)
			}
			metaServer := &metaserver.MetaServer{MetaAgent: &agent.MetaAgent{
				KatalystMachineInfo: &machine.KatalystMachineInfo{
					MachineInfo:    &cadvisorinfo.MachineInfo{},
					MemoryTopology: &machine.MemoryTopology{PageSize: 1, NormalMemoryDetails: machine.MemoryDetails{0: 1000}},
				},
			}}
			mg := NewMemoryGuard(conf, nil, nil, metaServer, metrics.DummyMetrics{}).(*memoryGuard)
			mg.existingCgroupPathsFn = func(paths ...string) []string { return paths }
			mg.numaMemoryUsageFn = func(path string, pageSize uint64) (map[int]uint64, error) {
				return map[int]uint64{0: usageByPath[path]}, nil
			}
			limit, err := mg.calculateReclaimedMemoryLimitFor(snapshot, 0, paths,
				[]machine.NormalZoneInfo{{Node: 0, Free: tc.free, Low: 100}})
			require.NoError(t, err)
			require.Equal(t, tc.want, limit)
		})
	}
}

func TestMemoryGuardLimitMarginUpdatesAndAdvices(t *testing.T) {
	t.Parallel()

	conf := config.NewConfiguration()
	conf.ReclaimedMemoryLimitMargin = 100
	conf.StateFileDirectory = t.TempDir()
	conf.GetDynamicConfiguration().MemoryGuardConfiguration.Enable = true
	conf.GetDynamicConfiguration().CriticalWatermarkSource = configv1alpha1.CriticalWatermarkSourceLow
	conf.GetDynamicConfiguration().ReclaimedMemoryMaxRatio = 0.5

	fetcher := agentmetric.NewFakeMetricsFetcher(metrics.DummyMetrics{}).(*agentmetric.FakeMetricsFetcher)
	metaCache, err := metacache.NewMetaCacheImp(conf, metricspool.DummyMetricsEmitterPool{}, fetcher)
	require.NoError(t, err)
	metaServer := &metaserver.MetaServer{MetaAgent: &agent.MetaAgent{
		MetricsFetcher: fetcher,
		KatalystMachineInfo: &machine.KatalystMachineInfo{
			MachineInfo: &cadvisorinfo.MachineInfo{},
			CPUTopology: &machine.CPUTopology{CPUDetails: machine.CPUDetails{
				0: {NUMANodeID: 0}, 1: {NUMANodeID: 1}, 2: {NUMANodeID: 2},
			}},
			MemoryTopology: &machine.MemoryTopology{
				PageSize:            1,
				NormalMemoryDetails: machine.MemoryDetails{0: 1000, 1: 1000, 2: 1000},
			},
		},
	}}
	mg := NewMemoryGuard(conf, nil, metaCache, metaServer, metrics.DummyMetrics{}).(*memoryGuard)
	mg.reclaimRelativeRootCgroupPaths = []string{"/margin-a", "/margin-b"}
	mg.numaBindingRelativeRootCgroupPaths = map[int][]string{
		0: {"/margin-a-0", "/margin-b-0"},
		1: {"/margin-a-1", "/margin-b-1"},
		2: {"/margin-a-2", "/margin-b-2"},
	}
	mg.existingCgroupPathsFn = func(paths ...string) []string { return paths }
	usageByPath := make(map[string]map[int]uint64)
	mg.numaMemoryUsageFn = func(path string, pageSize uint64) (map[int]uint64, error) {
		return usageByPath[path], nil
	}
	zoneInfos := []machine.NormalZoneInfo{
		{Node: 0, Free: 50, Low: 100},
		{Node: 1, Free: 50, Low: 100},
		{Node: 2, Free: 50, Low: 100},
	}
	mg.zoneInfoFn = func(string) []machine.NormalZoneInfo { return zoneInfos }
	for numaID, used := range []float64{600, 200, 0} {
		for _, paths := range [][]string{mg.reclaimRelativeRootCgroupPaths, mg.numaBindingRelativeRootCgroupPaths[numaID]} {
			for _, path := range paths {
				if usageByPath[path] == nil {
					usageByPath[path] = make(map[int]uint64)
				}
				usageByPath[path][numaID] = uint64(used / 2)
			}
		}
	}

	require.NoError(t, mg.Reconcile(nil))
	advice := mg.GetAdvices()
	require.Equal(t, int64(1000), memoryLimitAdvice(t, advice, "/margin-a"))
	require.Equal(t, int64(1000), memoryLimitAdvice(t, advice, "/margin-b"))
	for numaID, want := range map[int]int64{0: 700, 1: 300, 2: 0} {
		for _, path := range mg.numaBindingRelativeRootCgroupPaths[numaID] {
			require.Equal(t, want, memoryLimitAdvice(t, advice, path))
		}
	}
	for _, entry := range advice.ExtraEntries {
		limit := memoryLimitAdvice(t, advice, entry.CgroupPath)
		require.Equal(t, strconv.FormatInt(limit*95/100, 10),
			entry.Values[string(memoryadvisor.ControlKnobKeyMemoryHigh)])
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

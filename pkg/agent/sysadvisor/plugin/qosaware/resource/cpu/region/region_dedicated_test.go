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

package region

import (
	"errors"
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	configapi "github.com/kubewharf/katalyst-api/pkg/apis/config/v1alpha1"
	workloadapis "github.com/kubewharf/katalyst-api/pkg/apis/workload/v1alpha1"
	apiconsts "github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/options"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent"
	agentmetric "github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/pod"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/spd"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
	utilmetric "github.com/kubewharf/katalyst-core/pkg/util/metric"
)

type dedicatedIndicatorFixture struct {
	conf      *config.Configuration
	cache     metacache.MetaCache
	fetcher   *agentmetric.FakeMetricsFetcher
	region    *QoSRegionDedicated
	container *types.ContainerInfo
}

func newDedicatedIndicatorFixture(
	t *testing.T,
	numaID int,
	assignments types.TopologyAwareAssignment,
) *dedicatedIndicatorFixture {
	t.Helper()

	conf, err := options.NewOptions().Config()
	require.NoError(t, err)
	conf.CPUAdvisorConfiguration.ProvisionPolicies = nil
	conf.CPUAdvisorConfiguration.HeadroomPolicies = nil

	cache := metacache.NewDummyMetaCacheImp()
	fetcher := agentmetric.NewFakeMetricsFetcher(metrics.DummyMetrics{}).(*agentmetric.FakeMetricsFetcher)
	metaServer := &metaserver.MetaServer{
		MetaAgent: &agent.MetaAgent{
			MetricsFetcher: fetcher,
			PodFetcher: &pod.PodFetcherStub{PodList: []*v1.Pod{{
				ObjectMeta: metav1.ObjectMeta{UID: "pod"},
			}}},
		},
		ServiceProfilingManager: &spd.DummyServiceProfilingManager{},
	}
	container := &types.ContainerInfo{
		PodUID:                   "pod",
		ContainerName:            "main",
		OwnerPoolName:            "dedicated-pool",
		OriginOwnerPoolName:      "dedicated-pool",
		QoSLevel:                 apiconsts.PodAnnotationQoSLevelDedicatedCores,
		TopologyAwareAssignments: assignments,
	}
	require.NoError(t, cache.SetContainerInfo(container.PodUID, container.ContainerName, container))

	r := NewQoSRegionDedicated(
		container,
		conf,
		numaID,
		nil,
		cache,
		metaServer,
		metrics.DummyMetrics{},
	).(*QoSRegionDedicated)
	require.NoError(t, r.AddContainer(container))

	return &dedicatedIndicatorFixture{
		conf:      conf,
		cache:     cache,
		fetcher:   fetcher,
		region:    r,
		container: container,
	}
}

func TestNewQoSRegionDedicatedRegistersSchedWaitGetter(t *testing.T) {
	t.Parallel()

	f := newDedicatedIndicatorFixture(t, 0, types.TopologyAwareAssignment{
		0: machine.NewCPUSet(0, 1),
	})

	require.Contains(t, f.region.indicatorCurrentGetters,
		string(workloadapis.ServiceSystemIndicatorNameCPUSchedWait))
}

func TestDefaultDedicatedIndicatorsDoNotEnableSchedWait(t *testing.T) {
	t.Parallel()

	conf, err := options.NewOptions().Config()
	require.NoError(t, err)

	for _, indicator := range conf.GetDynamicConfiguration().
		RegionIndicatorTargetConfiguration[configapi.QoSRegionTypeDedicated] {
		require.NotEqual(t, workloadapis.ServiceSystemIndicatorNameCPUSchedWait, indicator.Name)
	}
}

func TestQoSRegionDedicatedAssignedCPUSet(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		numaID      int
		assignments types.TopologyAwareAssignment
		want        machine.CPUSet
	}{
		{
			name:   "numa binding uses only current sibling",
			numaID: 0,
			assignments: types.TopologyAwareAssignment{
				0: machine.NewCPUSet(0, 1),
				1: machine.NewCPUSet(2, 3),
			},
			want: machine.NewCPUSet(0, 1),
		},
		{
			name:   "non binding uses all assigned cpus",
			numaID: commonstate.FakedNUMAID,
			assignments: types.TopologyAwareAssignment{
				0: machine.NewCPUSet(0, 1),
				1: machine.NewCPUSet(2, 3),
			},
			want: machine.NewCPUSet(0, 1, 2, 3),
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newDedicatedIndicatorFixture(t, tt.numaID, tt.assignments)

			got, err := f.region.getAssignedCPUSet()

			require.NoError(t, err)
			require.True(t, tt.want.Equals(got), "want %s, got %s", tt.want.String(), got.String())
		})
	}
}

func TestQoSRegionDedicatedAssignedCPUSetUnavailable(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		setup func(t *testing.T, f *dedicatedIndicatorFixture)
	}{
		{
			name: "missing container info",
			setup: func(t *testing.T, f *dedicatedIndicatorFixture) {
				sidecar := &types.ContainerInfo{
					PodUID:              f.container.PodUID,
					ContainerName:       "sidecar",
					OwnerPoolName:       f.container.OwnerPoolName,
					OriginOwnerPoolName: f.container.OriginOwnerPoolName,
					QoSLevel:            f.container.QoSLevel,
					TopologyAwareAssignments: types.TopologyAwareAssignment{
						0: machine.NewCPUSet(1),
					},
				}
				require.NoError(t, f.cache.SetContainerInfo(sidecar.PodUID, sidecar.ContainerName, sidecar))
				require.NoError(t, f.region.AddContainer(sidecar))
				require.NoError(t, f.cache.DeleteContainer(f.container.PodUID, f.container.ContainerName))
			},
		},
		{
			name: "binding numa has no assigned cpus",
			setup: func(_ *testing.T, f *dedicatedIndicatorFixture) {
				f.region.bindingNumas = machine.NewCPUSet(1)
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			f := newDedicatedIndicatorFixture(t, 0, types.TopologyAwareAssignment{
				0: machine.NewCPUSet(0),
			})
			tt.setup(t, f)

			_, err := f.region.getAssignedCPUSet()
			require.ErrorIs(t, err, errIndicatorUnavailable)

			dynamicConf := f.conf.GetDynamicConfiguration()
			dynamicConf.RegionIndicatorTargetConfiguration = map[configapi.QoSRegionType][]configapi.IndicatorTargetConfiguration{
				configapi.QoSRegionTypeDedicated: {
					{Name: workloadapis.ServiceSystemIndicatorNameCPUSchedWait, Target: 460},
					{Name: workloadapis.ServiceSystemIndicatorNameCPUUsageRatio, Target: 0.55},
				},
			}
			f.region.SetEssentials(types.ResourceEssentials{DynamicConfiguration: dynamicConf})
			f.region.indicatorCurrentGetters[string(workloadapis.ServiceSystemIndicatorNameCPUUsageRatio)] = func() (float64, error) { return 0.5, nil }

			indicators, err := f.region.getIndicators()
			require.NoError(t, err)
			require.NotContains(t, indicators, string(workloadapis.ServiceSystemIndicatorNameCPUSchedWait))
			require.Equal(t, types.IndicatorValue{Current: 0.5, Target: 0.55},
				indicators[string(workloadapis.ServiceSystemIndicatorNameCPUUsageRatio)])
		})
	}
}

func TestQoSRegionDedicatedAssignedCPUSetUsesLatestNonBindingAssignments(t *testing.T) {
	t.Parallel()

	f := newDedicatedIndicatorFixture(t, commonstate.FakedNUMAID, types.TopologyAwareAssignment{
		0: machine.NewCPUSet(0),
	})
	container, ok := f.cache.GetContainerInfo(f.container.PodUID, f.container.ContainerName)
	require.True(t, ok)
	container.TopologyAwareAssignments = types.TopologyAwareAssignment{
		1: machine.NewCPUSet(2, 3),
	}
	require.NoError(t, f.cache.SetContainerInfo(container.PodUID, container.ContainerName, container))

	got, err := f.region.getAssignedCPUSet()

	require.NoError(t, err)
	require.True(t, machine.NewCPUSet(2, 3).Equals(got), "got %s", got.String())
}

func TestQoSRegionDedicatedSchedWaitUsesSiblingCPUSet(t *testing.T) {
	t.Parallel()

	assignments := types.TopologyAwareAssignment{
		0: machine.NewCPUSet(0, 1),
		1: machine.NewCPUSet(2, 3),
	}
	numa0 := newDedicatedIndicatorFixture(t, 0, assignments)
	numa1 := newDedicatedIndicatorFixture(t, 1, assignments)
	now := time.Now()
	for cpuID, value := range map[int]float64{
		0: 10,
		1: 30,
		2: 50,
		3: 70,
	} {
		numa0.fetcher.SetCPUMetric(
			cpuID,
			consts.MetricCPUSchedwait,
			utilmetric.MetricData{Value: value, Time: &now},
		)
		numa1.fetcher.SetCPUMetric(
			cpuID,
			consts.MetricCPUSchedwait,
			utilmetric.MetricData{Value: value, Time: &now},
		)
	}

	numa0Value, err := numa0.region.getCPUSchedWait()
	require.NoError(t, err)
	numa1Value, err := numa1.region.getCPUSchedWait()
	require.NoError(t, err)

	require.Equal(t, 20.0, numa0Value)
	require.Equal(t, 60.0, numa1Value)
}

func TestQoSRegionDedicatedCPUUsageUsesAssignedCPUSet(t *testing.T) {
	t.Parallel()

	assignments := types.TopologyAwareAssignment{
		0: machine.NewCPUSet(0, 1),
		1: machine.NewCPUSet(2, 3),
	}
	numa0 := newDedicatedIndicatorFixture(t, 0, assignments)
	nonBinding := newDedicatedIndicatorFixture(t, commonstate.FakedNUMAID, assignments)
	now := time.Now()
	for cpuID, value := range map[int]float64{
		0: 0.1,
		1: 0.3,
		2: 0.5,
		3: 0.7,
	} {
		numa0.fetcher.SetCPUMetric(
			cpuID,
			consts.MetricCPUUsageRatio,
			utilmetric.MetricData{Value: value, Time: &now},
		)
		nonBinding.fetcher.SetCPUMetric(
			cpuID,
			consts.MetricCPUUsageRatio,
			utilmetric.MetricData{Value: value, Time: &now},
		)
	}

	numa0Value, err := numa0.region.getCPUUsageRatio()
	require.NoError(t, err)
	nonBindingValue, err := nonBinding.region.getCPUUsageRatio()
	require.NoError(t, err)

	require.InDelta(t, 0.2, numa0Value, 1e-9)
	require.InDelta(t, 0.4, nonBindingValue, 1e-9)
}

func TestQoSRegionDedicatedSchedWaitDeduplicatesContainerCPUAssignments(t *testing.T) {
	t.Parallel()

	assignments := types.TopologyAwareAssignment{
		0: machine.NewCPUSet(0, 1),
	}
	f := newDedicatedIndicatorFixture(t, 0, assignments)
	sidecar := &types.ContainerInfo{
		PodUID:              f.container.PodUID,
		ContainerName:       "sidecar",
		OwnerPoolName:       f.container.OwnerPoolName,
		OriginOwnerPoolName: f.container.OriginOwnerPoolName,
		QoSLevel:            f.container.QoSLevel,
		TopologyAwareAssignments: types.TopologyAwareAssignment{
			0: machine.NewCPUSet(1, 2),
		},
	}
	require.NoError(t, f.cache.SetContainerInfo(sidecar.PodUID, sidecar.ContainerName, sidecar))
	require.NoError(t, f.region.AddContainer(sidecar))

	now := time.Now()
	f.fetcher.SetCPUMetric(0, consts.MetricCPUSchedwait, utilmetric.MetricData{Value: 10, Time: &now})
	f.fetcher.SetCPUMetric(1, consts.MetricCPUSchedwait, utilmetric.MetricData{Value: 30, Time: &now})
	f.fetcher.SetCPUMetric(2, consts.MetricCPUSchedwait, utilmetric.MetricData{Value: 80, Time: &now})

	got, err := f.region.getCPUSchedWait()

	require.NoError(t, err)
	require.Equal(t, 40.0, got)
}

func TestQoSRegionDedicatedGetIndicatorsRejectsAllUnavailable(t *testing.T) {
	t.Parallel()

	f := newDedicatedIndicatorFixture(t, 0, types.TopologyAwareAssignment{
		0: machine.NewCPUSet(0),
	})
	dynamicConf := f.conf.GetDynamicConfiguration()
	dynamicConf.RegionIndicatorTargetConfiguration = map[configapi.QoSRegionType][]configapi.IndicatorTargetConfiguration{
		configapi.QoSRegionTypeDedicated: {
			{Name: workloadapis.ServiceSystemIndicatorNameCPUSchedWait, Target: 460},
		},
	}
	f.region.SetEssentials(types.ResourceEssentials{DynamicConfiguration: dynamicConf})

	indicators, err := f.region.getIndicators()

	require.ErrorIs(t, err, errIndicatorUnavailable)
	require.Nil(t, indicators)
}

func TestQoSRegionDedicatedGetIndicatorsIsolatesUnavailableSchedWait(t *testing.T) {
	t.Parallel()

	f := newDedicatedIndicatorFixture(t, 0, types.TopologyAwareAssignment{
		0: machine.NewCPUSet(0),
	})
	dynamicConf := f.conf.GetDynamicConfiguration()
	dynamicConf.RegionIndicatorTargetConfiguration = map[configapi.QoSRegionType][]configapi.IndicatorTargetConfiguration{
		configapi.QoSRegionTypeDedicated: {
			{Name: workloadapis.ServiceSystemIndicatorNameCPUSchedWait, Target: 460},
			{Name: workloadapis.ServiceSystemIndicatorNameCPUUsageRatio, Target: 0.55},
		},
	}
	dynamicConf.IndicatorTargetDefaultGetter = "test-default"
	f.region.SetEssentials(types.ResourceEssentials{DynamicConfiguration: dynamicConf})
	f.region.indicatorTargetGetters["test-default"] = func(
		_ workloadapis.ServiceSystemIndicatorName,
		target float64,
	) float64 {
		return target
	}
	f.region.indicatorCurrentGetters[string(workloadapis.ServiceSystemIndicatorNameCPUUsageRatio)] = func() (float64, error) { return 0.5, nil }

	indicators, err := f.region.getIndicators()
	require.NoError(t, err)
	require.NotContains(t, indicators, string(workloadapis.ServiceSystemIndicatorNameCPUSchedWait))
	require.Equal(t, types.IndicatorValue{Current: 0.5, Target: 0.55},
		indicators[string(workloadapis.ServiceSystemIndicatorNameCPUUsageRatio)])

	now := time.Now()
	f.fetcher.SetCPUMetric(0, consts.MetricCPUSchedwait, utilmetric.MetricData{
		Value: 0,
		Time:  &now,
	})

	indicators, err = f.region.getIndicators()
	require.NoError(t, err)
	require.Equal(t, types.IndicatorValue{Current: 0, Target: 460},
		indicators[string(workloadapis.ServiceSystemIndicatorNameCPUSchedWait)])
}

func TestQoSRegionDedicatedGetIndicatorsRejectsInvalidSchedWait(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		current float64
	}{
		{name: "negative", current: -1},
		{name: "nan", current: math.NaN()},
		{name: "positive infinity", current: math.Inf(1)},
		{name: "negative infinity", current: math.Inf(-1)},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			f := newDedicatedIndicatorFixture(t, 0, types.TopologyAwareAssignment{
				0: machine.NewCPUSet(0),
			})
			dynamicConf := f.conf.GetDynamicConfiguration()
			dynamicConf.RegionIndicatorTargetConfiguration = map[configapi.QoSRegionType][]configapi.IndicatorTargetConfiguration{
				configapi.QoSRegionTypeDedicated: {
					{Name: workloadapis.ServiceSystemIndicatorNameCPUSchedWait, Target: 460},
				},
			}
			dynamicConf.IndicatorTargetDefaultGetter = "test-default"
			f.region.SetEssentials(types.ResourceEssentials{DynamicConfiguration: dynamicConf})
			f.region.indicatorTargetGetters["test-default"] = func(
				_ workloadapis.ServiceSystemIndicatorName,
				target float64,
			) float64 {
				return target
			}
			f.region.indicatorCurrentGetters[string(workloadapis.ServiceSystemIndicatorNameCPUSchedWait)] = func() (float64, error) { return tt.current, nil }

			indicators, err := f.region.getIndicators()

			require.NoError(t, err)
			require.NotContains(t, indicators, string(workloadapis.ServiceSystemIndicatorNameCPUSchedWait))
		})
	}
}

func TestQoSRegionDedicatedGetIndicatorsPreservesUnexpectedErrors(t *testing.T) {
	t.Parallel()

	f := newDedicatedIndicatorFixture(t, 0, types.TopologyAwareAssignment{
		0: machine.NewCPUSet(0),
	})
	dynamicConf := f.conf.GetDynamicConfiguration()
	dynamicConf.RegionIndicatorTargetConfiguration = map[configapi.QoSRegionType][]configapi.IndicatorTargetConfiguration{
		configapi.QoSRegionTypeDedicated: {
			{Name: workloadapis.ServiceSystemIndicatorNameCPUSchedWait, Target: 460},
		},
	}
	f.region.SetEssentials(types.ResourceEssentials{DynamicConfiguration: dynamicConf})

	wantErr := errors.New("unexpected getter failure")
	f.region.indicatorCurrentGetters[string(workloadapis.ServiceSystemIndicatorNameCPUSchedWait)] = func() (float64, error) { return 0, wantErr }

	indicators, err := f.region.getIndicators()

	require.ErrorIs(t, err, wantErr)
	require.Nil(t, indicators)
}

func TestQoSRegionBaseGetIndicatorsPreservesShareSchedWaitZeroBehavior(t *testing.T) {
	t.Parallel()

	f := newDedicatedIndicatorFixture(t, 0, types.TopologyAwareAssignment{
		0: machine.NewCPUSet(0),
	})
	f.region.regionType = configapi.QoSRegionTypeShare
	dynamicConf := f.conf.GetDynamicConfiguration()
	dynamicConf.RegionIndicatorTargetConfiguration = map[configapi.QoSRegionType][]configapi.IndicatorTargetConfiguration{
		configapi.QoSRegionTypeShare: {
			{Name: workloadapis.ServiceSystemIndicatorNameCPUSchedWait, Target: 460},
		},
	}
	dynamicConf.IndicatorTargetDefaultGetter = "test-default"
	f.region.SetEssentials(types.ResourceEssentials{DynamicConfiguration: dynamicConf})
	f.region.indicatorTargetGetters["test-default"] = func(
		_ workloadapis.ServiceSystemIndicatorName,
		target float64,
	) float64 {
		return target
	}
	f.region.indicatorCurrentGetters[string(workloadapis.ServiceSystemIndicatorNameCPUSchedWait)] = func() (float64, error) { return 0, nil }

	indicators, err := f.region.getIndicators()

	require.NoError(t, err)
	require.NotContains(t, indicators, string(workloadapis.ServiceSystemIndicatorNameCPUSchedWait))
}

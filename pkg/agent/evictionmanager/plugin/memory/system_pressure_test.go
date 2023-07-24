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
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"

	apiconsts "github.com/kubewharf/katalyst-api/pkg/consts"
	pluginapi "github.com/kubewharf/katalyst-api/pkg/protocol/evictionplugin/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/config"
	evictionconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/adminqos/eviction"
	"github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
	utilMetric "github.com/kubewharf/katalyst-core/pkg/util/metric"
)

func makeMetaServer() *metaserver.MetaServer {
	return &metaserver.MetaServer{
		MetaAgent: &agent.MetaAgent{},
	}
}

var (
	evictionManagerSyncPeriod               = 10 * time.Second
	numaFreeBelowWatermarkTimesThreshold    = 3
	systemKswapdRateThreshold               = 1000
	systemKswapdRateExceedDurationThreshold = 90
	systemPluginSyncPeriod                  = 30
	systemPluginCoolDownPeriod              = 40

	scaleFactor = 600
	systemTotal = 100 * 1024 * 1024 * 1024

	highPriority int32 = 100000
	lowPriority  int32 = 50000
)

func makeConf() *config.Configuration {
	conf := config.NewConfiguration()
	conf.EvictionManagerSyncPeriod = evictionManagerSyncPeriod
	conf.SystemPressureSyncPeriod = systemPluginSyncPeriod
	conf.SystemPressureCoolDownPeriod = systemPluginCoolDownPeriod
	conf.GetDynamicConfiguration().EnableNumaLevelEviction = evictionconfig.DefaultEnableNumaLevelEviction
	conf.GetDynamicConfiguration().EnableSystemLevelEviction = evictionconfig.DefaultEnableSystemLevelEviction
	conf.GetDynamicConfiguration().NumaFreeBelowWatermarkTimesThreshold = numaFreeBelowWatermarkTimesThreshold
	conf.GetDynamicConfiguration().SystemKswapdRateThreshold = systemKswapdRateThreshold
	conf.GetDynamicConfiguration().SystemKswapdRateExceedDurationThreshold = systemKswapdRateExceedDurationThreshold
	conf.GetDynamicConfiguration().NumaEvictionRankingMetrics = evictionconfig.DefaultNumaEvictionRankingMetrics
	conf.GetDynamicConfiguration().SystemEvictionRankingMetrics = evictionconfig.DefaultSystemEvictionRankingMetrics
	conf.GetDynamicConfiguration().MemoryPressureEvictionConfiguration.GracePeriod = evictionconfig.DefaultGracePeriod

	return conf
}

func makeSystemPressureEvictionPlugin(conf *config.Configuration) (*SystemPressureEvictionPlugin, error) {
	cpuTopology, err := machine.GenerateDummyCPUTopology(16, 1, 2)
	if err != nil {
		return nil, err
	}

	metaServer := makeMetaServer()
	metaServer.KatalystMachineInfo = &machine.KatalystMachineInfo{
		CPUTopology: cpuTopology,
	}
	metaServer.MetricsFetcher = metric.NewFakeMetricsFetcher(metrics.DummyMetrics{})

	plugin := NewSystemPressureEvictionPlugin(nil, nil, metaServer, metrics.DummyMetrics{}, conf)
	res := plugin.(*SystemPressureEvictionPlugin)

	return res, nil
}

func TestNewSystemPressureEvictionPlugin(t *testing.T) {
	t.Parallel()

	plugin, err := makeSystemPressureEvictionPlugin(makeConf())
	assert.NoError(t, err)
	assert.NotNil(t, plugin)

	assert.Equal(t, evictionManagerSyncPeriod, plugin.evictionManagerSyncPeriod)
	assert.Equal(t, time.Duration(systemPluginSyncPeriod)*time.Second, plugin.syncPeriod)
	assert.Equal(t, numaFreeBelowWatermarkTimesThreshold, plugin.dynamicConfig.GetDynamicConfiguration().NumaFreeBelowWatermarkTimesThreshold)
	assert.Equal(t, systemKswapdRateThreshold, plugin.dynamicConfig.GetDynamicConfiguration().SystemKswapdRateThreshold)
	assert.Equal(t, systemKswapdRateExceedDurationThreshold, plugin.dynamicConfig.GetDynamicConfiguration().SystemKswapdRateExceedDurationThreshold)
	assert.Equal(t, evictionconfig.DefaultNumaEvictionRankingMetrics, plugin.dynamicConfig.GetDynamicConfiguration().NumaEvictionRankingMetrics)
	assert.Equal(t, evictionconfig.DefaultSystemEvictionRankingMetrics, plugin.dynamicConfig.GetDynamicConfiguration().SystemEvictionRankingMetrics)
}

func TestSystemPressureEvictionPlugin_ThresholdMet(t *testing.T) {
	t.Parallel()

	plugin, err := makeSystemPressureEvictionPlugin(makeConf())
	assert.NoError(t, err)
	assert.NotNil(t, plugin)

	fakeMetricsFetcher := plugin.metaServer.MetaAgent.MetricsFetcher.(*metric.FakeMetricsFetcher)
	assert.NotNil(t, fakeMetricsFetcher)

	start := time.Now()
	fakeMetricsFetcher.SetNodeMetric(consts.MetricMemTotalSystem, utilMetric.MetricData{Value: float64(systemTotal), Time: &start})
	fakeMetricsFetcher.SetNodeMetric(consts.MetricMemScaleFactorSystem, utilMetric.MetricData{Value: float64(scaleFactor), Time: &start})

	tests := []struct {
		name                      string
		round                     int64
		systemFree                float64
		systemKswapSteal          float64
		wantMetType               pluginapi.ThresholdMetType
		wantEvictionScope         string
		wantCondition             *pluginapi.Condition
		wantIsUnderSystemPressure bool
		wantSystemAction          int
	}{
		{
			name:                      "system above watermark, kswapd steal not exceed",
			round:                     0,
			systemFree:                45 * 1024 * 1024 * 1024,
			systemKswapSteal:          10000,
			wantMetType:               pluginapi.ThresholdMetType_NOT_MET,
			wantEvictionScope:         "",
			wantCondition:             nil,
			wantIsUnderSystemPressure: false,
			wantSystemAction:          actionNoop,
		},
		{
			name:              "system below watermark, kswapd steal exceed",
			round:             1,
			systemFree:        3 * 1024 * 1024 * 1024,
			systemKswapSteal:  45000,
			wantMetType:       pluginapi.ThresholdMetType_HARD_MET,
			wantEvictionScope: EvictionScopeSystemMemory,
			wantCondition: &pluginapi.Condition{
				ConditionType: pluginapi.ConditionType_NODE_CONDITION,
				Effects:       []string{string(v1.TaintEffectNoSchedule)},
				ConditionName: evictionConditionMemoryPressure,
				MetCondition:  true,
			},
			wantIsUnderSystemPressure: true,
			wantSystemAction:          actionReclaimedEviction,
		},
		{
			name:                      "system above watermark, kswapd steal not exceed",
			round:                     2,
			systemFree:                11 * 1024 * 1024 * 1024,
			systemKswapSteal:          55000,
			wantMetType:               pluginapi.ThresholdMetType_NOT_MET,
			wantCondition:             nil,
			wantIsUnderSystemPressure: false,
			wantSystemAction:          actionNoop,
		},
		{
			name:              "system below watermark, kswapd steal exceed",
			round:             3,
			systemFree:        3 * 1024 * 1024 * 1024,
			systemKswapSteal:  90000,
			wantMetType:       pluginapi.ThresholdMetType_HARD_MET,
			wantEvictionScope: EvictionScopeSystemMemory,
			wantCondition: &pluginapi.Condition{
				ConditionType: pluginapi.ConditionType_NODE_CONDITION,
				Effects:       []string{string(v1.TaintEffectNoSchedule)},
				ConditionName: evictionConditionMemoryPressure,
				MetCondition:  true,
			},
			wantIsUnderSystemPressure: true,
			wantSystemAction:          actionReclaimedEviction,
		},
		{
			name:              "system below watermark, kswapd steal exceed",
			round:             4,
			systemFree:        3 * 1024 * 1024 * 1024,
			systemKswapSteal:  125000,
			wantMetType:       pluginapi.ThresholdMetType_HARD_MET,
			wantEvictionScope: EvictionScopeSystemMemory,
			wantCondition: &pluginapi.Condition{
				ConditionType: pluginapi.ConditionType_NODE_CONDITION,
				Effects:       []string{string(v1.TaintEffectNoSchedule)},
				ConditionName: evictionConditionMemoryPressure,
				MetCondition:  true,
			},
			wantIsUnderSystemPressure: true,
			wantSystemAction:          actionReclaimedEviction,
		},
		{
			name:              "system above watermark, kswapd steal exceed",
			round:             5,
			systemFree:        12 * 1024 * 1024 * 1024,
			systemKswapSteal:  160000,
			wantMetType:       pluginapi.ThresholdMetType_HARD_MET,
			wantEvictionScope: EvictionScopeSystemMemory,
			wantCondition: &pluginapi.Condition{
				ConditionType: pluginapi.ConditionType_NODE_CONDITION,
				Effects:       []string{string(v1.TaintEffectNoSchedule)},
				ConditionName: evictionConditionMemoryPressure,
				MetCondition:  true,
			},
			wantIsUnderSystemPressure: true,
			wantSystemAction:          actionEviction,
		},
		{
			name:                      "system above watermark, kswapd not steal exceed",
			round:                     6,
			systemFree:                12 * 1024 * 1024 * 1024,
			systemKswapSteal:          170000,
			wantMetType:               pluginapi.ThresholdMetType_NOT_MET,
			wantCondition:             nil,
			wantIsUnderSystemPressure: false,
			wantSystemAction:          actionNoop,
		},
		{
			name:                      "system above watermark, kswapd not steal exceed",
			round:                     7,
			systemFree:                12 * 1024 * 1024 * 1024,
			systemKswapSteal:          210000,
			wantMetType:               pluginapi.ThresholdMetType_NOT_MET,
			wantCondition:             nil,
			wantIsUnderSystemPressure: false,
			wantSystemAction:          actionNoop,
		},
		{
			name:                      "system above watermark, kswapd not steal exceed",
			round:                     8,
			systemFree:                12 * 1024 * 1024 * 1024,
			systemKswapSteal:          260000,
			wantMetType:               pluginapi.ThresholdMetType_NOT_MET,
			wantCondition:             nil,
			wantIsUnderSystemPressure: false,
			wantSystemAction:          actionNoop,
		},
		{
			name:              "system above watermark, kswapd steal exceed",
			round:             9,
			systemFree:        12 * 1024 * 1024 * 1024,
			systemKswapSteal:  300000,
			wantMetType:       pluginapi.ThresholdMetType_HARD_MET,
			wantEvictionScope: EvictionScopeSystemMemory,
			wantCondition: &pluginapi.Condition{
				ConditionType: pluginapi.ConditionType_NODE_CONDITION,
				Effects:       []string{string(v1.TaintEffectNoSchedule)},
				ConditionName: evictionConditionMemoryPressure,
				MetCondition:  true,
			},
			wantIsUnderSystemPressure: true,
			wantSystemAction:          actionEviction,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			now := start.Add(time.Duration(tt.round) * plugin.syncPeriod)
			fakeMetricsFetcher.SetNodeMetric(consts.MetricMemFreeSystem, utilMetric.MetricData{Value: tt.systemFree, Time: &now})
			fakeMetricsFetcher.SetNodeMetric(consts.MetricMemKswapdstealSystem, utilMetric.MetricData{Value: tt.systemKswapSteal, Time: &now})

			plugin.detectSystemPressures(context.TODO())
			metResp, err := plugin.ThresholdMet(context.TODO())
			assert.NoError(t, err)
			assert.NotNil(t, metResp)
			assert.Equal(t, tt.wantMetType, metResp.MetType)
			assert.Equal(t, tt.wantEvictionScope, metResp.EvictionScope)
			if tt.wantCondition != nil && metResp.Condition != nil {
				assert.Equal(t, *(tt.wantCondition), *(metResp.Condition))
			} else {
				assert.Equal(t, tt.wantCondition, metResp.Condition)
			}

			assert.Equal(t, tt.wantIsUnderSystemPressure, plugin.isUnderSystemPressure)
			assert.Equal(t, tt.wantSystemAction, plugin.systemAction)
		})
	}
}

func TestSystemPressureEvictionPlugin_GetTopEvictionPods(t *testing.T) {
	t.Parallel()

	plugin, err := makeSystemPressureEvictionPlugin(makeConf())
	assert.NoError(t, err)
	assert.NotNil(t, plugin)

	fakeMetricsFetcher := plugin.metaServer.MetricsFetcher.(*metric.FakeMetricsFetcher)
	assert.NotNil(t, fakeMetricsFetcher)

	bePods := []*v1.Pod{
		{
			ObjectMeta: metav1.ObjectMeta{
				UID:  "000001",
				Name: "pod-1",
				Annotations: map[string]string{
					apiconsts.PodAnnotationQoSLevelKey: apiconsts.PodAnnotationQoSLevelReclaimedCores,
				},
			},
			Spec: v1.PodSpec{
				Priority: &highPriority,
				Containers: []v1.Container{
					{
						Name: "c",
					},
				},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				UID:  "000002",
				Name: "pod-2",
				Annotations: map[string]string{
					apiconsts.PodAnnotationQoSLevelKey: apiconsts.PodAnnotationQoSLevelReclaimedCores,
				},
			},
			Spec: v1.PodSpec{
				Priority: &highPriority,
				Containers: []v1.Container{
					{
						Name: "c",
					},
				},
			},
		},
	}

	bePodUsageSystem := []float64{
		10 * 1024 * 1024 * 1024,
		5 * 1024 * 1024 * 1024,
	}

	now := time.Now()
	for i, pod := range bePods {
		fakeMetricsFetcher.SetContainerMetric(string(pod.UID), pod.Spec.Containers[0].Name, consts.MetricMemUsageContainer, utilMetric.MetricData{Value: bePodUsageSystem[i], Time: &now})
	}

	tests := []struct {
		name                  string
		isUnderSystemPressure bool
		systemAction          int
		wantEvictPodSet       sets.String
		lastEvictionTime      time.Time
	}{
		{
			name:                  "not under pressure",
			isUnderSystemPressure: false,
			systemAction:          actionNoop,
			wantEvictPodSet:       sets.String{},
			lastEvictionTime:      time.Now().Add(-1 * time.Hour),
		},
		{
			name:                  "under pressure, need reclaim",
			isUnderSystemPressure: true,
			systemAction:          actionReclaimedEviction,
			wantEvictPodSet:       sets.NewString("pod-1"),
			lastEvictionTime:      time.Now().Add(-1 * time.Hour),
		},
		{
			name:                  "under pressure, need eviction",
			isUnderSystemPressure: true,
			systemAction:          actionEviction,
			wantEvictPodSet:       sets.NewString("pod-1"),
			lastEvictionTime:      time.Now().Add(-1 * time.Hour),
		},
		{
			name:                  "under pressure, need eviction",
			isUnderSystemPressure: true,
			systemAction:          actionEviction,
			wantEvictPodSet:       sets.NewString(),
			lastEvictionTime:      time.Now().Add(-10 * time.Second),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plugin.isUnderSystemPressure = tt.isUnderSystemPressure
			plugin.systemAction = tt.systemAction
			plugin.lastEvictionTime = tt.lastEvictionTime
			resp, err := plugin.GetTopEvictionPods(context.TODO(), &pluginapi.GetTopEvictionPodsRequest{
				ActivePods:    bePods,
				TopN:          1,
				EvictionScope: EvictionScopeSystemMemory,
			})
			assert.NoError(t, err)
			assert.NotNil(t, resp)

			targetPodSet := sets.String{}
			for _, pod := range resp.TargetPods {
				targetPodSet.Insert(pod.Name)
			}
			assert.Equal(t, tt.wantEvictPodSet, targetPodSet)
		})
	}
}

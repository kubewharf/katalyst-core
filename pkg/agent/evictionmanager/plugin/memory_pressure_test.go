// Copyright 2022 The Katalyst Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package plugin

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"

	apiconsts "github.com/kubewharf/katalyst-api/pkg/consts"
	pluginapi "github.com/kubewharf/katalyst-api/pkg/protocol/evictionplugin/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/config"
	evictionconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/eviction"
	"github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
	"github.com/kubewharf/katalyst-core/pkg/util/native"
)

var (
	evictionManagerSyncPeriod            = 10 * time.Second
	numaFreeBelowWatermarkTimesThreshold = 3
	systemKswapdRateThreshold            = 3000
	systemKswapdRateExceedTimesThreshold = 3

	scaleFactor  = 600
	systemTotal  = 100 * 1024 * 1024 * 1024
	numaTotalMap = []float64{50 * 1024 * 1024 * 1024, 50 * 1024 * 1024 * 1024}

	highPriority int32 = 100000
	lowPriority  int32 = 50000
)

func makeMemoryPressureEvictionPlugin(conf *config.Configuration) (*MemoryPressureEvictionPlugin, error) {
	metaServer := makeMetaServer()

	plugin := NewMemoryPressureEvictionPlugin(nil, nil, metaServer, metrics.DummyMetrics{}, conf)
	res := plugin.(*MemoryPressureEvictionPlugin)

	cpuTopology, err := machine.GenerateDummyCPUTopology(16, 1, 2)
	if err != nil {
		return nil, err
	}

	res.metaServer.KatalystMachineInfo = &machine.KatalystMachineInfo{
		CPUTopology: cpuTopology,
	}
	res.metaServer.MetricsFetcher = metric.NewFakeMetricsFetcher(metrics.DummyMetrics{})

	return res, nil
}

func makeMetaServer() *metaserver.MetaServer {
	return &metaserver.MetaServer{
		MetaAgent: &agent.MetaAgent{},
	}
}

func makeConf() *config.Configuration {
	conf := config.NewConfiguration()
	conf.EvictionManagerSyncPeriod = evictionManagerSyncPeriod
	conf.MemoryPressureEvictionPluginConfiguration.DynamicConf.SetEnableNumaLevelDetection(evictionconfig.DefaultEnableNumaLevelDetection)
	conf.MemoryPressureEvictionPluginConfiguration.DynamicConf.SetEnableSystemLevelDetection(evictionconfig.DefaultEnableSystemLevelDetection)
	conf.MemoryPressureEvictionPluginConfiguration.DynamicConf.SetNumaFreeBelowWatermarkTimesThreshold(numaFreeBelowWatermarkTimesThreshold)
	conf.MemoryPressureEvictionPluginConfiguration.DynamicConf.SetSystemKswapdRateThreshold(systemKswapdRateThreshold)
	conf.MemoryPressureEvictionPluginConfiguration.DynamicConf.SetSystemKswapdRateExceedTimesThreshold(systemKswapdRateExceedTimesThreshold)
	conf.MemoryPressureEvictionPluginConfiguration.DynamicConf.SetNumaEvictionRankingMetrics(evictionconfig.DefaultNumaEvictionRankingMetrics)
	conf.MemoryPressureEvictionPluginConfiguration.DynamicConf.SetSystemEvictionRankingMetrics(evictionconfig.DefaultSystemEvictionRankingMetrics)
	conf.MemoryPressureEvictionPluginConfiguration.DynamicConf.SetGracePeriod(evictionconfig.DefaultGracePeriod)

	return conf
}

func TestNewMemoryPressureEvictionPlugin(t *testing.T) {
	plugin, err := makeMemoryPressureEvictionPlugin(makeConf())
	assert.NoError(t, err)
	assert.NotNil(t, plugin)

	assert.Equal(t, evictionManagerSyncPeriod, plugin.evictionManagerSyncPeriod)
	assert.Equal(t, numaFreeBelowWatermarkTimesThreshold, plugin.memoryEvictionPluginConfig.DynamicConf.NumaFreeBelowWatermarkTimesThreshold())
	assert.Equal(t, systemKswapdRateThreshold, plugin.memoryEvictionPluginConfig.DynamicConf.SystemKswapdRateThreshold())
	assert.Equal(t, systemKswapdRateExceedTimesThreshold, plugin.memoryEvictionPluginConfig.DynamicConf.SystemKswapdRateExceedTimesThreshold())
	assert.Equal(t, evictionconfig.DefaultNumaEvictionRankingMetrics, plugin.memoryEvictionPluginConfig.DynamicConf.NumaEvictionRankingMetrics())
	assert.Equal(t, evictionconfig.DefaultSystemEvictionRankingMetrics, plugin.memoryEvictionPluginConfig.DynamicConf.SystemEvictionRankingMetrics())

	return
}

func TestThresholdMet(t *testing.T) {
	plugin, err := makeMemoryPressureEvictionPlugin(makeConf())
	assert.NoError(t, err)
	assert.NotNil(t, plugin)

	fakeMetricsFetcher := plugin.metaServer.MetricsFetcher.(*metric.FakeMetricsFetcher)
	assert.NotNil(t, fakeMetricsFetcher)

	fakeMetricsFetcher.SetNodeMetric(consts.MetricMemTotalSystem, float64(systemTotal))
	fakeMetricsFetcher.SetNodeMetric(consts.MetricMemScaleFactorSystem, float64(scaleFactor))
	for numaID, numaTotal := range numaTotalMap {
		fakeMetricsFetcher.SetNumaMetric(numaID, consts.MetricMemTotalNuma, numaTotal)
	}

	tests := []struct {
		name                      string
		numaFree                  map[int]float64
		systemFree                float64
		systemKswapSteal          float64
		wantMetType               pluginapi.ThresholdMetType
		wantEvictionScope         string
		wantCondition             *pluginapi.Condition
		wantIsUnderNumaPressure   bool
		wantIsUnderSystemPressure bool
		wantNumaAction            map[int]int
		wantSystemAction          int
	}{
		{
			name: "numa0 above watermark, numa1 above watermark, system above watermark, kswapd steal not exceed",
			numaFree: map[int]float64{
				0: 20 * 1024 * 1024 * 1024,
				1: 25 * 1024 * 1024 * 1024,
			},
			systemFree:                45 * 1024 * 1024 * 1024,
			systemKswapSteal:          10000,
			wantMetType:               pluginapi.ThresholdMetType_NOT_MET,
			wantEvictionScope:         "",
			wantCondition:             nil,
			wantIsUnderNumaPressure:   false,
			wantIsUnderSystemPressure: false,
			wantNumaAction: map[int]int{
				0: actionNoop,
				1: actionNoop,
			},
			wantSystemAction: actionNoop,
		},
		{
			name: "numa0 below watermark, numa1 below watermark, system below watermark, kswapd steal exceed",
			numaFree: map[int]float64{
				0: 1 * 1024 * 1024 * 1024,
				1: 2 * 1024 * 1024 * 1024,
			},
			systemFree:        3 * 1024 * 1024 * 1024,
			systemKswapSteal:  45000,
			wantMetType:       pluginapi.ThresholdMetType_HARD_MET,
			wantEvictionScope: evictionScopeMemory,
			wantCondition: &pluginapi.Condition{
				ConditionType: pluginapi.ConditionType_NODE_CONDITION,
				Effects:       []string{string(v1.TaintEffectNoSchedule)},
				ConditionName: evictionConditionMemoryPressure,
				MetCondition:  true,
			},
			wantIsUnderNumaPressure:   true,
			wantIsUnderSystemPressure: true,
			wantNumaAction: map[int]int{
				0: actionReclaimedEviction,
				1: actionReclaimedEviction,
			},
			wantSystemAction: actionReclaimedEviction,
		},
		{
			name: "numa0 below watermark, numa1 above watermark, system above watermark, kswapd steal not exceed",
			numaFree: map[int]float64{
				0: 1 * 1024 * 1024 * 1024,
				1: 10 * 1024 * 1024 * 1024,
			},
			systemFree:                11 * 1024 * 1024 * 1024,
			systemKswapSteal:          55000,
			wantMetType:               pluginapi.ThresholdMetType_HARD_MET,
			wantEvictionScope:         evictionScopeMemory,
			wantCondition:             nil,
			wantIsUnderNumaPressure:   true,
			wantIsUnderSystemPressure: false,
			wantNumaAction: map[int]int{
				0: actionReclaimedEviction,
				1: actionNoop,
			},
			wantSystemAction: actionNoop,
		},
		{
			name: "numa0 below watermark, numa1 below watermark, system below watermark, kswapd steal exceed",
			numaFree: map[int]float64{
				0: 1 * 1024 * 1024 * 1024,
				1: 2 * 1024 * 1024 * 1024,
			},
			systemFree:        3 * 1024 * 1024 * 1024,
			systemKswapSteal:  90000,
			wantMetType:       pluginapi.ThresholdMetType_HARD_MET,
			wantEvictionScope: evictionScopeMemory,
			wantCondition: &pluginapi.Condition{
				ConditionType: pluginapi.ConditionType_NODE_CONDITION,
				Effects:       []string{string(v1.TaintEffectNoSchedule)},
				ConditionName: evictionConditionMemoryPressure,
				MetCondition:  true,
			},
			wantIsUnderNumaPressure:   true,
			wantIsUnderSystemPressure: true,
			wantNumaAction: map[int]int{
				0: actionEviction,
				1: actionReclaimedEviction,
			},
			wantSystemAction: actionReclaimedEviction,
		},
		{
			name: "numa0 below watermark, numa1 below watermark, system below watermark, kswapd steal exceed",
			numaFree: map[int]float64{
				0: 1 * 1024 * 1024 * 1024,
				1: 2 * 1024 * 1024 * 1024,
			},
			systemFree:        3 * 1024 * 1024 * 1024,
			systemKswapSteal:  125000,
			wantMetType:       pluginapi.ThresholdMetType_HARD_MET,
			wantEvictionScope: evictionScopeMemory,
			wantCondition: &pluginapi.Condition{
				ConditionType: pluginapi.ConditionType_NODE_CONDITION,
				Effects:       []string{string(v1.TaintEffectNoSchedule)},
				ConditionName: evictionConditionMemoryPressure,
				MetCondition:  true,
			},
			wantIsUnderNumaPressure:   true,
			wantIsUnderSystemPressure: true,
			wantNumaAction: map[int]int{
				0: actionEviction,
				1: actionReclaimedEviction,
			},
			wantSystemAction: actionReclaimedEviction,
		},
		{
			name: "numa0 above watermark, numa1 below watermark, system above watermark, kswapd steal exceed",
			numaFree: map[int]float64{
				0: 10 * 1024 * 1024 * 1024,
				1: 2 * 1024 * 1024 * 1024,
			},
			systemFree:        12 * 1024 * 1024 * 1024,
			systemKswapSteal:  160000,
			wantMetType:       pluginapi.ThresholdMetType_HARD_MET,
			wantEvictionScope: evictionScopeMemory,
			wantCondition: &pluginapi.Condition{
				ConditionType: pluginapi.ConditionType_NODE_CONDITION,
				Effects:       []string{string(v1.TaintEffectNoSchedule)},
				ConditionName: evictionConditionMemoryPressure,
				MetCondition:  true,
			},
			wantIsUnderNumaPressure:   true,
			wantIsUnderSystemPressure: true,
			wantNumaAction: map[int]int{
				0: actionNoop,
				1: actionEviction,
			},
			wantSystemAction: actionEviction,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fakeMetricsFetcher.SetNodeMetric(consts.MetricMemFreeSystem, tt.systemFree)
			fakeMetricsFetcher.SetNodeMetric(consts.MetricMemKswapdstealSystem, tt.systemKswapSteal)
			for numaID, numaFree := range tt.numaFree {
				fakeMetricsFetcher.SetNumaMetric(numaID, consts.MetricMemFreeNuma, numaFree)
			}

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

			assert.Equal(t, tt.wantIsUnderNumaPressure, plugin.isUnderNumaPressure)
			assert.Equal(t, tt.wantIsUnderSystemPressure, plugin.isUnderSystemPressure)
			assert.Equal(t, tt.wantNumaAction, plugin.numaActionMap)
			assert.Equal(t, tt.wantSystemAction, plugin.systemAction)
		})
	}
}

func TestGetTopEvictionPods(t *testing.T) {
	plugin, err := makeMemoryPressureEvictionPlugin(makeConf())
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

	bePodUsageNuma := []map[int]float64{
		{
			0: 1 * 1024 * 1024 * 1024,
			1: 9 * 1024 * 1024 * 1024,
		},
		{
			0: 4 * 1024 * 1024 * 1024,
			1: 1 * 1024 * 1024 * 1024,
		},
	}

	for i, pod := range bePods {
		fakeMetricsFetcher.SetContainerMetric(string(pod.UID), pod.Spec.Containers[0].Name, consts.MetricMemUsageContainer, bePodUsageSystem[i])
		for numaID, usage := range bePodUsageNuma[i] {
			fakeMetricsFetcher.SetContainerNumaMetric(string(pod.UID), pod.Spec.Containers[0].Name, strconv.Itoa(numaID), consts.MetricsMemTotalPerNumaContainer, usage)
		}
	}

	tests := []struct {
		name                  string
		isUnderNumaPressure   bool
		isUnderSystemPressure bool
		numaAction            map[int]int
		systemAction          int
		wantEvictPodSet       sets.String
	}{
		{
			isUnderNumaPressure:   false,
			isUnderSystemPressure: false,
			systemAction:          actionNoop,
			numaAction: map[int]int{
				0: actionNoop,
				1: actionNoop,
			},
			wantEvictPodSet: sets.String{},
		},
		{
			isUnderSystemPressure: true,
			isUnderNumaPressure:   false,
			systemAction:          actionReclaimedEviction,
			numaAction: map[int]int{
				0: actionNoop,
				1: actionNoop,
			},
			wantEvictPodSet: sets.NewString("pod-1"),
		},
		{
			isUnderNumaPressure:   true,
			isUnderSystemPressure: false,
			systemAction:          actionNoop,
			numaAction: map[int]int{
				0: actionEviction,
				1: actionNoop,
			},
			wantEvictPodSet: sets.NewString("pod-2"),
		},
		{
			isUnderNumaPressure:   true,
			isUnderSystemPressure: false,
			systemAction:          actionNoop,
			numaAction: map[int]int{
				0: actionNoop,
				1: actionReclaimedEviction,
			},
			wantEvictPodSet: sets.NewString("pod-1"),
		},
		{
			isUnderNumaPressure:   true,
			isUnderSystemPressure: false,
			systemAction:          actionNoop,
			numaAction: map[int]int{
				0: actionEviction,
				1: actionEviction,
			},
			wantEvictPodSet: sets.NewString("pod-1", "pod-2"),
		},
		{
			isUnderNumaPressure:   true,
			isUnderSystemPressure: true,
			systemAction:          actionReclaimedEviction,
			numaAction: map[int]int{
				0: actionEviction,
				1: actionEviction,
			},
			wantEvictPodSet: sets.NewString("pod-1", "pod-2"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plugin.isUnderNumaPressure = tt.isUnderNumaPressure
			plugin.isUnderSystemPressure = tt.isUnderSystemPressure
			plugin.systemAction = tt.systemAction
			plugin.numaActionMap = tt.numaAction
			resp, err := plugin.GetTopEvictionPods(context.TODO(), &pluginapi.GetTopEvictionPodsRequest{
				ActivePods:    bePods,
				TopN:          1,
				EvictionScope: evictionScopeMemory,
			})
			assert.NoError(t, err)
			assert.NotNil(t, resp)

			targetPodSet := sets.String{}
			for _, pod := range resp.TargetPods {
				targetPodSet.Insert(pod.Name)
			}
			assert.Equal(t, true, targetPodSet.Equal(tt.wantEvictPodSet))
		})
	}
}

func TestMultiSorter(t *testing.T) {
	plugin, err := makeMemoryPressureEvictionPlugin(makeConf())
	assert.NoError(t, err)
	assert.NotNil(t, plugin)

	fakeMetricsFetcher := plugin.metaServer.MetricsFetcher.(*metric.FakeMetricsFetcher)
	assert.NotNil(t, fakeMetricsFetcher)

	pods := []*v1.Pod{
		{
			ObjectMeta: metav1.ObjectMeta{
				UID:  "000001",
				Name: "pod-1",
				Annotations: map[string]string{
					apiconsts.PodAnnotationQoSLevelKey: apiconsts.PodAnnotationQoSLevelReclaimedCores,
				},
			},
			Spec: v1.PodSpec{
				Priority: &lowPriority,
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
				Priority: &lowPriority,
				Containers: []v1.Container{
					{
						Name: "c",
					},
				},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				UID:  "000003",
				Name: "pod-3",
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
				UID:  "000004",
				Name: "pod-4",
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
				UID:  "000005",
				Name: "pod-5",
				Annotations: map[string]string{
					apiconsts.PodAnnotationQoSLevelKey: apiconsts.PodAnnotationQoSLevelSharedCores,
				},
			},
			Spec: v1.PodSpec{
				Priority: &lowPriority,
				Containers: []v1.Container{
					{
						Name: "c",
					},
				},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				UID:  "000006",
				Name: "pod-6",
				Annotations: map[string]string{
					apiconsts.PodAnnotationQoSLevelKey: apiconsts.PodAnnotationQoSLevelSharedCores,
				},
			},
			Spec: v1.PodSpec{
				Priority: &lowPriority,
				Containers: []v1.Container{
					{
						Name: "c",
					},
				},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				UID:  "000007",
				Name: "pod-7",
				Annotations: map[string]string{
					apiconsts.PodAnnotationQoSLevelKey: apiconsts.PodAnnotationQoSLevelSharedCores,
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
				UID:  "000008",
				Name: "pod-8",
				Annotations: map[string]string{
					apiconsts.PodAnnotationQoSLevelKey: apiconsts.PodAnnotationQoSLevelSharedCores,
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

	podUsageSystem := []float64{
		5 * 1024 * 1024 * 1024,
		10 * 1024 * 1024 * 1024,
		20 * 1024 * 1024 * 1024,
		30 * 1024 * 1024 * 1024,
		40 * 1024 * 1024 * 1024,
		50 * 1024 * 1024 * 1024,
		60 * 1024 * 1024 * 1024,
		70 * 1024 * 1024 * 1024,
		80 * 1024 * 1024 * 1024,
	}

	for i, pod := range pods {
		fakeMetricsFetcher.SetContainerMetric(string(pod.UID), pod.Spec.Containers[0].Name, consts.MetricMemUsageContainer, podUsageSystem[i])
	}
	general.NewMultiSorter(plugin.getEvictionCmpFuncs(plugin.memoryEvictionPluginConfig.DynamicConf.SystemEvictionRankingMetrics(),
		nonExistNumaID)...).Sort(native.NewPodSourceImpList(pods))

	wantPodNameList := []string{
		"pod-2",
		"pod-1",
		"pod-4",
		"pod-3",
		"pod-6",
		"pod-5",
		"pod-8",
		"pod-7",
	}
	for i := range pods {
		assert.Equal(t, wantPodNameList[i], pods[i].Name)
	}
}

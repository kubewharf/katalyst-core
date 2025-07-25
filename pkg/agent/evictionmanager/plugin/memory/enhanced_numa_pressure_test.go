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
	evictionconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/adminqos/eviction"
	"github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
	utilMetric "github.com/kubewharf/katalyst-core/pkg/util/metric"
)

func makeEnhancedNumaPressureEvictionPlugin(conf *config.Configuration) (*EnhancedNumaMemoryPressurePlugin, error) {
	cpuTopology, err := machine.GenerateDummyCPUTopology(16, 2, 4)
	if err != nil {
		return nil, err
	}

	metaServer := makeMetaServer()
	metaServer.KatalystMachineInfo = &machine.KatalystMachineInfo{
		CPUTopology: cpuTopology,
	}
	metaServer.MetricsFetcher = metric.NewFakeMetricsFetcher(metrics.DummyMetrics{})
	conf.GetDynamicConfiguration().EnableNumaLevelEviction = false
	conf.GetDynamicConfiguration().EnableSystemLevelEviction = false
	conf.GetDynamicConfiguration().EnableEnhancedNumaLevelEviction = true

	plugin := NewEnhancedNumaMemoryPressureEvictionPlugin(nil, nil, metaServer, metrics.DummyMetrics{}, conf)
	res := plugin.(*EnhancedNumaMemoryPressurePlugin)

	return res, nil
}

func TestNewEnhancedNumaMemoryPressureEvictionPlugin(t *testing.T) {
	t.Parallel()

	plugin, err := makeEnhancedNumaPressureEvictionPlugin(makeConf())
	assert.NoError(t, err)
	assert.NotNil(t, plugin)

	assert.Equal(t, evictionconfig.DefaultEnableEnhancedNumaLevelEviction, plugin.dynamicConfig.GetDynamicConfiguration().EnableEnhancedNumaLevelEviction)
	assert.Equal(t, evictionconfig.DefaultNumaEvictionRankingMetrics, plugin.dynamicConfig.GetDynamicConfiguration().NumaEvictionRankingMetrics)
	assert.Equal(t, evictionconfig.DefaultSystemEvictionRankingMetrics, plugin.dynamicConfig.GetDynamicConfiguration().SystemEvictionRankingMetrics)
}

func TestEnhancedNumaMemoryPressurePlugin_ThresholdMet(t *testing.T) {
	t.Parallel()

	plugin, err := makeEnhancedNumaPressureEvictionPlugin(makeConf())
	assert.NoError(t, err)
	assert.NotNil(t, plugin)

	fakeMetricsFetcher := plugin.metaServer.MetricsFetcher.(*metric.FakeMetricsFetcher)
	assert.NotNil(t, fakeMetricsFetcher)

	tests := []struct {
		name                   string
		numaFree               map[int]float64
		numaInactiveFile       map[int]float64
		numaKswapdSteal        map[int]float64
		systemKswapSteal       float64
		wantMetType            pluginapi.ThresholdMetType
		wantEvictionScope      string
		wantIsUnderMemPressure bool
		wantNumaAction         map[int]int
	}{
		{
			name: "numa0 free and inactive file above 2 times watermark",
			numaFree: map[int]float64{
				0: 20 * 1024 * 1024 * 1024,
				1: 25 * 1024 * 1024 * 1024,
				2: 20 * 1024 * 1024 * 1024,
				3: 25 * 1024 * 1024 * 1024,
			},
			numaInactiveFile: map[int]float64{
				0: 10 * 1024 * 1024 * 1024,
				1: 10 * 1024 * 1024 * 1024,
				2: 10 * 1024 * 1024 * 1024,
				3: 10 * 1024 * 1024 * 1024,
			},
			systemKswapSteal:       500,
			wantMetType:            pluginapi.ThresholdMetType_NOT_MET,
			wantEvictionScope:      "",
			wantIsUnderMemPressure: false,
			wantNumaAction: map[int]int{
				0: actionNoop,
				1: actionNoop,
				2: actionNoop,
				3: actionNoop,
			},
		},
		{
			name: "numa0 free and inactive file below 2 times watermark but kswapd is not running",
			numaFree: map[int]float64{
				0: 3 * 1024 * 1024 * 1024,
				1: 3 * 1024 * 1024 * 1024,
				2: 3 * 1024 * 1024 * 1024,
				3: 3 * 1024 * 1024 * 1024,
			},
			numaInactiveFile: map[int]float64{
				0: 1 * 1024 * 1024 * 1024,
				1: 10 * 1024 * 1024 * 1024,
				2: 10 * 1024 * 1024 * 1024,
				3: 10 * 1024 * 1024 * 1024,
			},
			systemKswapSteal:       500,
			wantMetType:            pluginapi.ThresholdMetType_NOT_MET,
			wantEvictionScope:      "",
			wantIsUnderMemPressure: false,
			wantNumaAction: map[int]int{
				0: actionNoop,
				1: actionNoop,
				2: actionNoop,
				3: actionNoop,
			},
		},
		{
			name: "numa0 free and inactive file below 2 times watermark and kswapd is running",
			numaFree: map[int]float64{
				0: 3 * 1024 * 1024 * 1024,
				1: 3 * 1024 * 1024 * 1024,
				2: 3 * 1024 * 1024 * 1024,
				3: 3 * 1024 * 1024 * 1024,
			},
			numaInactiveFile: map[int]float64{
				0: 1 * 1024 * 1024 * 1024,
				1: 10 * 1024 * 1024 * 1024,
				2: 10 * 1024 * 1024 * 1024,
				3: 10 * 1024 * 1024 * 1024,
			},
			systemKswapSteal:       1000,
			wantMetType:            pluginapi.ThresholdMetType_HARD_MET,
			wantEvictionScope:      EvictionScopeEnhancedNumaMemory,
			wantIsUnderMemPressure: true,
			wantNumaAction: map[int]int{
				0: actionEviction,
				1: actionNoop,
				2: actionNoop,
				3: actionNoop,
			},
		},
		{
			name: "numa0 and numa2 is detected in memory pressure",
			numaFree: map[int]float64{
				0: 3 * 1024 * 1024 * 1024,
				1: 3 * 1024 * 1024 * 1024,
				2: 3 * 1024 * 1024 * 1024,
				3: 3 * 1024 * 1024 * 1024,
			},
			numaInactiveFile: map[int]float64{
				0: 1 * 1024 * 1024 * 1024,
				1: 10 * 1024 * 1024 * 1024,
				2: 1 * 1024 * 1024 * 1024,
				3: 10 * 1024 * 1024 * 1024,
			},
			systemKswapSteal:       1500,
			wantMetType:            pluginapi.ThresholdMetType_HARD_MET,
			wantEvictionScope:      EvictionScopeEnhancedNumaMemory,
			wantIsUnderMemPressure: true,
			wantNumaAction: map[int]int{
				0: actionEviction,
				1: actionNoop,
				2: actionEviction,
				3: actionNoop,
			},
		},
	}

	now := time.Now()
	fakeMetricsFetcher.SetNodeMetric(consts.MetricMemScaleFactorSystem, utilMetric.MetricData{Value: float64(numaScaleFactor), Time: &now})
	for numaID, numaTotal := range numaTotalMap {
		fakeMetricsFetcher.SetNumaMetric(numaID, consts.MetricMemTotalNuma, utilMetric.MetricData{Value: numaTotal, Time: &now})
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			now := time.Now()
			fakeMetricsFetcher.SetNodeMetric(consts.MetricMemKswapdstealSystem, utilMetric.MetricData{Value: tt.systemKswapSteal, Time: &now})
			for numaID, numaFree := range tt.numaFree {
				fakeMetricsFetcher.SetNumaMetric(numaID, consts.MetricMemFreeNuma, utilMetric.MetricData{Value: numaFree, Time: &now})
			}
			for numaID, numaInactiveFile := range tt.numaInactiveFile {
				fakeMetricsFetcher.SetNumaMetric(numaID, consts.MetricMemInactiveFileNuma, utilMetric.MetricData{Value: numaInactiveFile, Time: &now})
			}

			metResp, err := plugin.ThresholdMet(context.TODO())
			assert.NoError(t, err)
			assert.NotNil(t, metResp)
			assert.Equal(t, tt.wantMetType, metResp.MetType)
			assert.Equal(t, tt.wantEvictionScope, metResp.EvictionScope)
			assert.Equal(t, tt.wantIsUnderMemPressure, plugin.isUnderNumaPressure)
			assert.Equal(t, tt.wantNumaAction, plugin.numaActionMap)
		})
	}
}

func TestEnhancedNumaMemoryPressurePlugin_GetTopEvictionPods(t *testing.T) {
	t.Parallel()

	plugin, err := makeEnhancedNumaPressureEvictionPlugin(makeConf())
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
	}

	bePodUsageNuma := []map[int]float64{
		{
			0: 3 * 1024 * 1024 * 1024,
			1: 9 * 1024 * 1024 * 1024,
			2: 1 * 1024,
			3: 1 * 1024,
		},
		{
			0: 4 * 1024 * 1024 * 1024,
			1: 1 * 1024 * 1024 * 1024,
			2: 1 * 1024,
			3: 1 * 1024,
		},
		{
			0: 1 * 1024 * 1024 * 1024,
			1: 1 * 1024 * 1024 * 1024,
			2: 4 * 1024 * 1024 * 1024,
			3: 1 * 1024,
		},
	}

	now := time.Now()
	for i, pod := range bePods {
		bePodUsageSystem := float64(0)
		for numaID, usage := range bePodUsageNuma[i] {
			fakeMetricsFetcher.SetContainerNumaMetric(string(pod.UID), pod.Spec.Containers[0].Name, strconv.Itoa(numaID), consts.MetricsMemTotalPerNumaContainer, utilMetric.MetricData{Value: usage, Time: &now})
			bePodUsageSystem += usage
		}
		fakeMetricsFetcher.SetContainerMetric(string(pod.UID), pod.Spec.Containers[0].Name, consts.MetricMemUsageContainer, utilMetric.MetricData{Value: bePodUsageSystem, Time: &now})
	}
	for numaID, numaTotal := range numaTotalMap {
		fakeMetricsFetcher.SetNumaMetric(numaID, consts.MetricMemTotalNuma, utilMetric.MetricData{Value: numaTotal, Time: &now})
	}

	tests := []struct {
		name                string
		isUnderNumaPressure bool
		numaAction          map[int]int
		numaPressureStatMap map[int]float64 // numaFree + numaInactiveFile - 2 * numaTotal * watermark_scale_factor/10000
		wantEvictPodSet     sets.String
	}{
		{
			name:                "no numa is under mem pressure",
			isUnderNumaPressure: false,
			numaAction: map[int]int{
				0: actionNoop,
				1: actionNoop,
				2: actionNoop,
				3: actionNoop,
			},
			numaPressureStatMap: map[int]float64{
				0: 1 * 1024 * 1024 * 1024,
				1: 1 * 1024 * 1024 * 1024,
				2: 1 * 1024 * 1024 * 1024,
				3: 1 * 1024 * 1024 * 1024,
			},
			wantEvictPodSet: sets.String{},
		},
		{
			name:                "one numa is under pressure, and the pressure is predicted to be released by evicting one pod",
			isUnderNumaPressure: true,
			numaAction: map[int]int{
				0: actionEviction,
				1: actionNoop,
				2: actionNoop,
				3: actionNoop,
			},
			numaPressureStatMap: map[int]float64{
				0: -2 * 1024 * 1024 * 1024,
				1: 1 * 1024 * 1024 * 1024,
				2: 1 * 1024 * 1024 * 1024,
				3: 1 * 1024 * 1024 * 1024,
			},
			wantEvictPodSet: sets.NewString("pod-2"),
		},
		{
			name: "two numa nodes are under pressure, and the pressure is predicted to be released by evicting only one pod",

			isUnderNumaPressure: true,
			numaAction: map[int]int{
				0: actionEviction,
				1: actionEviction,
				2: actionNoop,
				3: actionNoop,
			},
			numaPressureStatMap: map[int]float64{
				0: -2 * 1024 * 1024 * 1024,
				1: -2 * 1024 * 1024 * 1024,
				2: 1 * 1024 * 1024 * 1024,
				3: 1 * 1024 * 1024 * 1024,
			},
			wantEvictPodSet: sets.NewString("pod-1"),
		},
		{
			name:                "three numa nodes are under pressure, and the pressure is predicted to be released by evicting two pods",
			isUnderNumaPressure: true,
			numaAction: map[int]int{
				0: actionEviction,
				1: actionEviction,
				2: actionEviction,
				3: actionNoop,
			},
			numaPressureStatMap: map[int]float64{
				0: -2 * 1024 * 1024 * 1024,
				1: -2 * 1024 * 1024 * 1024,
				2: -2 * 1024 * 1024 * 1024,
				3: 1 * 1024 * 1024 * 1024,
			},
			wantEvictPodSet: sets.NewString("pod-1", "pod-3"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plugin.isUnderNumaPressure = tt.isUnderNumaPressure
			plugin.numaActionMap = tt.numaAction
			plugin.numaPressureStatMap = tt.numaPressureStatMap
			resp, err := plugin.GetTopEvictionPods(context.TODO(), &pluginapi.GetTopEvictionPodsRequest{
				ActivePods:    bePods,
				TopN:          1,
				EvictionScope: EvictionScopeEnhancedNumaMemory,
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

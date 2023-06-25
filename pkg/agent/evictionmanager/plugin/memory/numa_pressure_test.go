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

var (
	numaTotalMap = []float64{50 * 1024 * 1024 * 1024, 50 * 1024 * 1024 * 1024}
)

func makeNumaPressureEvictionPlugin(conf *config.Configuration) (*NumaMemoryPressurePlugin, error) {
	cpuTopology, err := machine.GenerateDummyCPUTopology(16, 1, 2)
	if err != nil {
		return nil, err
	}

	metaServer := makeMetaServer()
	metaServer.KatalystMachineInfo = &machine.KatalystMachineInfo{
		CPUTopology: cpuTopology,
	}
	metaServer.MetricsFetcher = metric.NewFakeMetricsFetcher(metrics.DummyMetrics{})

	plugin := NewNumaMemoryPressureEvictionPlugin(nil, nil, metaServer, metrics.DummyMetrics{}, conf)
	res := plugin.(*NumaMemoryPressurePlugin)

	return res, nil
}

func TestNewNumaPressureEvictionPlugin(t *testing.T) {
	t.Parallel()

	plugin, err := makeNumaPressureEvictionPlugin(makeConf())
	assert.NoError(t, err)
	assert.NotNil(t, plugin)

	assert.Equal(t, numaFreeBelowWatermarkTimesThreshold, plugin.dynamicConfig.GetDynamicConfiguration().NumaFreeBelowWatermarkTimesThreshold)
	assert.Equal(t, systemKswapdRateThreshold, plugin.dynamicConfig.GetDynamicConfiguration().SystemKswapdRateThreshold)
	assert.Equal(t, systemKswapdRateExceedDurationThreshold, plugin.dynamicConfig.GetDynamicConfiguration().SystemKswapdRateExceedDurationThreshold)
	assert.Equal(t, evictionconfig.DefaultNumaEvictionRankingMetrics, plugin.dynamicConfig.GetDynamicConfiguration().NumaEvictionRankingMetrics)
	assert.Equal(t, evictionconfig.DefaultSystemEvictionRankingMetrics, plugin.dynamicConfig.GetDynamicConfiguration().SystemEvictionRankingMetrics)
}

func TestNumaMemoryPressurePlugin_ThresholdMet(t *testing.T) {
	t.Parallel()

	plugin, err := makeNumaPressureEvictionPlugin(makeConf())
	assert.NoError(t, err)
	assert.NotNil(t, plugin)

	fakeMetricsFetcher := plugin.metaServer.MetricsFetcher.(*metric.FakeMetricsFetcher)
	assert.NotNil(t, fakeMetricsFetcher)

	now := time.Now()
	fakeMetricsFetcher.SetNodeMetric(consts.MetricMemScaleFactorSystem, utilMetric.MetricData{Value: float64(scaleFactor), Time: &now})
	for numaID, numaTotal := range numaTotalMap {
		fakeMetricsFetcher.SetNumaMetric(numaID, consts.MetricMemTotalNuma, utilMetric.MetricData{Value: numaTotal, Time: &now})
	}

	tests := []struct {
		name                    string
		numaFree                map[int]float64
		wantMetType             pluginapi.ThresholdMetType
		wantEvictionScope       string
		wantCondition           *pluginapi.Condition
		wantIsUnderNumaPressure bool
		wantNumaAction          map[int]int
	}{
		{
			name: "numa0 above watermark, numa1 above watermark",
			numaFree: map[int]float64{
				0: 20 * 1024 * 1024 * 1024,
				1: 25 * 1024 * 1024 * 1024,
			},
			wantMetType:             pluginapi.ThresholdMetType_NOT_MET,
			wantEvictionScope:       "",
			wantCondition:           nil,
			wantIsUnderNumaPressure: false,
			wantNumaAction: map[int]int{
				0: actionNoop,
				1: actionNoop,
			},
		},
		{
			name: "numa0 below watermark, numa1 below watermark",
			numaFree: map[int]float64{
				0: 1 * 1024 * 1024 * 1024,
				1: 2 * 1024 * 1024 * 1024,
			},
			wantMetType:             pluginapi.ThresholdMetType_HARD_MET,
			wantEvictionScope:       EvictionScopeNumaMemory,
			wantCondition:           nil,
			wantIsUnderNumaPressure: true,
			wantNumaAction: map[int]int{
				0: actionReclaimedEviction,
				1: actionReclaimedEviction,
			},
		},
		{
			name: "numa0 below watermark, numa1 above watermark",
			numaFree: map[int]float64{
				0: 1 * 1024 * 1024 * 1024,
				1: 10 * 1024 * 1024 * 1024,
			},
			wantMetType:             pluginapi.ThresholdMetType_HARD_MET,
			wantEvictionScope:       EvictionScopeNumaMemory,
			wantCondition:           nil,
			wantIsUnderNumaPressure: true,
			wantNumaAction: map[int]int{
				0: actionReclaimedEviction,
				1: actionNoop,
			},
		},
		{
			name: "numa0 below watermark, numa1 below watermark",
			numaFree: map[int]float64{
				0: 1 * 1024 * 1024 * 1024,
				1: 2 * 1024 * 1024 * 1024,
			},
			wantMetType:             pluginapi.ThresholdMetType_HARD_MET,
			wantEvictionScope:       EvictionScopeNumaMemory,
			wantCondition:           nil,
			wantIsUnderNumaPressure: true,
			wantNumaAction: map[int]int{
				0: actionEviction,
				1: actionReclaimedEviction,
			},
		},
		{
			name: "numa0 below watermark, numa1 below watermark",
			numaFree: map[int]float64{
				0: 1 * 1024 * 1024 * 1024,
				1: 2 * 1024 * 1024 * 1024,
			},
			wantMetType:             pluginapi.ThresholdMetType_HARD_MET,
			wantEvictionScope:       EvictionScopeNumaMemory,
			wantCondition:           nil,
			wantIsUnderNumaPressure: true,
			wantNumaAction: map[int]int{
				0: actionEviction,
				1: actionReclaimedEviction,
			},
		},
		{
			name: "numa0 above watermark, numa1 below watermark",
			numaFree: map[int]float64{
				0: 10 * 1024 * 1024 * 1024,
				1: 2 * 1024 * 1024 * 1024,
			},
			wantMetType:             pluginapi.ThresholdMetType_HARD_MET,
			wantEvictionScope:       EvictionScopeNumaMemory,
			wantCondition:           nil,
			wantIsUnderNumaPressure: true,
			wantNumaAction: map[int]int{
				0: actionNoop,
				1: actionEviction,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			now := time.Now()
			for numaID, numaFree := range tt.numaFree {
				fakeMetricsFetcher.SetNumaMetric(numaID, consts.MetricMemFreeNuma, utilMetric.MetricData{Value: numaFree, Time: &now})
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
			assert.Equal(t, tt.wantNumaAction, plugin.numaActionMap)
		})
	}
}

func TestNumaMemoryPressurePlugin_GetTopEvictionPods(t *testing.T) {
	t.Parallel()

	plugin, err := makeNumaPressureEvictionPlugin(makeConf())
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

	now := time.Now()
	for i, pod := range bePods {
		fakeMetricsFetcher.SetContainerMetric(string(pod.UID), pod.Spec.Containers[0].Name, consts.MetricMemUsageContainer, utilMetric.MetricData{Value: bePodUsageSystem[i], Time: &now})
		for numaID, usage := range bePodUsageNuma[i] {
			fakeMetricsFetcher.SetContainerNumaMetric(string(pod.UID), pod.Spec.Containers[0].Name, strconv.Itoa(numaID), consts.MetricsMemTotalPerNumaContainer, utilMetric.MetricData{Value: usage, Time: &now})
		}
	}

	tests := []struct {
		name                string
		isUnderNumaPressure bool
		numaAction          map[int]int
		wantEvictPodSet     sets.String
	}{
		{
			isUnderNumaPressure: false,
			numaAction: map[int]int{
				0: actionNoop,
				1: actionNoop,
			},
			wantEvictPodSet: sets.String{},
		},
		{
			isUnderNumaPressure: true,
			numaAction: map[int]int{
				0: actionEviction,
				1: actionNoop,
			},
			wantEvictPodSet: sets.NewString("pod-2"),
		},
		{
			isUnderNumaPressure: true,
			numaAction: map[int]int{
				0: actionNoop,
				1: actionReclaimedEviction,
			},
			wantEvictPodSet: sets.NewString("pod-1"),
		},
		{
			isUnderNumaPressure: true,
			numaAction: map[int]int{
				0: actionReclaimedEviction,
				1: actionReclaimedEviction,
			},
			wantEvictPodSet: sets.NewString("pod-1", "pod-2"),
		},
		{
			isUnderNumaPressure: true,
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
			plugin.numaActionMap = tt.numaAction
			resp, err := plugin.GetTopEvictionPods(context.TODO(), &pluginapi.GetTopEvictionPodsRequest{
				ActivePods:    bePods,
				TopN:          1,
				EvictionScope: EvictionScopeNumaMemory,
			})
			assert.NoError(t, err)
			assert.NotNil(t, resp)

			targetPodSet := sets.String{}
			for _, pod := range resp.TargetPods {
				targetPodSet.Insert(pod.Name)
			}
			assert.Equal(t, targetPodSet, tt.wantEvictPodSet)
		})
	}
}

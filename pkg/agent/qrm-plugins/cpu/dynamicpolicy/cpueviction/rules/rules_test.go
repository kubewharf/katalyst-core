package rules

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

import (
	"fmt"
	"sort"
	"testing"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/cpueviction/history"
	"github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/stretchr/testify/assert"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

func makePod(name string) *v1.Pod {
	pod1 := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "default",
			UID:       types.UID(name),
			Annotations: map[string]string{
				"katalyst.kubewharf.io/qos_level": "shared_cores",
			},
		},
		Spec: v1.PodSpec{
			Containers: []v1.Container{
				{
					Name: "container",
				},
			},
		},
	}
	return pod1
}

func TestFilterer_Filter(t *testing.T) {
	t.Parallel()

	pod1 := makePod("pod1")
	pod2 := makePod("pod2")
	pod2.OwnerReferences = []metav1.OwnerReference{
		{Kind: "DaemonSet"},
	}
	pod3 := makePod("pod3")
	pod3.OwnerReferences = []metav1.OwnerReference{
		{Kind: "Deployment"},
	}
	type fields struct {
		enabledFilters []string
		filterParams   map[string]interface{}
	}
	type args struct {
		pods []*v1.Pod
	}
	type want struct {
		filteredPods []string
	}

	tests := []struct {
		name   string
		fields fields
		args   args
		want   want
	}{
		{
			name: "no filters enabled",
			fields: fields{
				enabledFilters: []string{},
				filterParams:   nil,
			},
			args: args{
				pods: []*v1.Pod{pod1, pod2, pod3},
			},
			want: want{
				filteredPods: []string{"pod1", "pod2", "pod3"},
			},
		},
		{
			name: "OwnerRefFilter with DaemonSet skip",
			fields: fields{
				enabledFilters: []string{OwnerRefFilterName},
				filterParams: map[string]interface{}{
					OwnerRefFilterName: []string{"DaemonSet"},
				},
			},
			args: args{
				pods: []*v1.Pod{pod1, pod2, pod3},
			},
			want: want{
				filteredPods: []string{"pod1", "pod3"},
			},
		},
		{
			name: "OverRatioNumaFilter",
			fields: fields{
				enabledFilters: []string{OverRatioNumaFilterName},
				filterParams: map[string]interface{}{
					OverRatioNumaFilterName: []NumaOverStat{
						{
							NumaID: 0,
							MetricsHistory: &history.NumaMetricHistory{
								Inner: map[int]map[string]map[string]*history.MetricRing{
									0: {
										history.FakePodUID: {
											consts.MetricCPUUsageContainer: {
												MaxLen: 2,
												Queue: []*history.MetricSnapshot{
													{Info: history.MetricInfo{Name: consts.MetricCPUUsageContainer, Value: 1.0}},
													nil,
												},
												CurrentIndex: 1,
											},
										},
										"pod1": {
											consts.MetricCPUUsageContainer: {
												MaxLen: 2,
												Queue: []*history.MetricSnapshot{
													{Info: history.MetricInfo{Name: consts.MetricCPUUsageContainer, Value: 0.5}},
													nil,
												},
												CurrentIndex: 1,
											},
										},
										"pod2": {
											consts.MetricCPUUsageContainer: {
												MaxLen: 2,
												Queue: []*history.MetricSnapshot{
													{Info: history.MetricInfo{Name: consts.MetricCPUUsageContainer, Value: 0.25}},
													nil,
												},
												CurrentIndex: 1,
											},
										},
									},
								},
								RingSize: 2,
							},
						},
					},
				},
			},
			args: args{
				pods: []*v1.Pod{pod1, pod2, pod3},
			},
			want: want{
				filteredPods: []string{"pod1", "pod2"},
			},
		},
		{
			name: "multiple filters : OwnerRefFilter and OverRatioNumaFilter",
			fields: fields{
				enabledFilters: []string{OwnerRefFilterName, OverRatioNumaFilterName},
				filterParams: map[string]interface{}{
					OwnerRefFilterName: []string{"DaemonSet"},
					OverRatioNumaFilterName: []NumaOverStat{
						{
							NumaID: 0,
							MetricsHistory: &history.NumaMetricHistory{
								Inner: map[int]map[string]map[string]*history.MetricRing{
									0: {
										history.FakePodUID: {
											consts.MetricCPUUsageContainer: {
												MaxLen: 2,
												Queue: []*history.MetricSnapshot{
													{Info: history.MetricInfo{Name: consts.MetricCPUUsageContainer, Value: 1.0}},
													nil,
												},
												CurrentIndex: 1,
											},
										},
										"pod1": {
											consts.MetricCPUUsageContainer: {
												MaxLen: 2,
												Queue: []*history.MetricSnapshot{
													{Info: history.MetricInfo{Name: consts.MetricCPUUsageContainer, Value: 0.5}},
													nil,
												},
												CurrentIndex: 1,
											},
										},
										"pod2": {
											consts.MetricCPUUsageContainer: {
												MaxLen: 2,
												Queue: []*history.MetricSnapshot{
													{Info: history.MetricInfo{Name: consts.MetricCPUUsageContainer, Value: 0.25}},
													nil,
												},
												CurrentIndex: 1,
											},
										},
									},
								},
								RingSize: 2,
							},
						},
					},
				},
			},
			args: args{
				pods: []*v1.Pod{pod1, pod2, pod3},
			},
			want: want{
				filteredPods: []string{"pod1"},
			},
		},
		{
			name: "invalid filter params",
			fields: fields{
				enabledFilters: []string{OwnerRefFilterName, OverRatioNumaFilterName},
				filterParams: map[string]interface{}{
					OwnerRefFilterName:      "invalid OwnerRefFilter params",
					OverRatioNumaFilterName: "invalid OverRatioNumaFilter params",
				},
			},
			args: args{
				pods: []*v1.Pod{pod1, pod2, pod3},
			},
			want: want{
				filteredPods: []string{"pod1", "pod2", "pod3"},
			},
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			filterer, err := NewFilter(tt.fields.enabledFilters, metrics.DummyMetrics{}, tt.fields.filterParams)
			assert.NoError(t, err)
			result := filterer.Filter(tt.args.pods)
			resultNames := make([]string, 0, len(result))
			for _, pod := range result {
				resultNames = append(resultNames, pod.Name)
			}
			sort.Strings(resultNames)
			sort.Strings(tt.want.filteredPods)
			assert.Equal(t, tt.want.filteredPods, resultNames)
		})
	}
}

func TestScorer_Score(t *testing.T) {
	t.Parallel()

	// 测试用 Pod 和 CandidatePod
	pod1 := makePod("pod1")
	pod1.Spec.Priority = new(int32)
	*pod1.Spec.Priority = 100

	pod2 := makePod("pod2")
	pod2.Spec.Priority = new(int32)
	*pod2.Spec.Priority = 200

	// 添加新测试 Pod
	pod3 := makePod("pod3")
	pod3.Spec.Priority = new(int32)
	*pod3.Spec.Priority = 50

	pod4 := makePod("pod4")
	pod4.Spec.Priority = new(int32)
	*pod4.Spec.Priority = 150

	pod5 := makePod("pod5")
	pod5.Spec.Priority = new(int32)
	*pod5.Spec.Priority = 75

	// 构造带不同驱逐历史的 CandidatePod
	candidatePod1 := &CandidatePod{
		Pod: pod1,
		WorkloadsEvictionInfo: map[string]*WorkloadEvictionInfo{
			workloadName: {
				WorkloadName: workloadName,
				StatsByWindow: map[float64]*EvictionStats{
					1:  {EvictionCount: 5, EvictionRatio: 0.5},
					24: {EvictionCount: 20, EvictionRatio: 0.2},
				},
				Replicas:         10,
				LastEvictionTime: 1620000000,
				Limit:            3,
			},
		},
	}

	candidatePod2 := &CandidatePod{
		Pod: pod2,
		WorkloadsEvictionInfo: map[string]*WorkloadEvictionInfo{
			workloadName: {
				WorkloadName: workloadName,
				StatsByWindow: map[float64]*EvictionStats{
					1:  {EvictionCount: 2, EvictionRatio: 0.2},
					24: {EvictionCount: 10, EvictionRatio: 0.1},
				},
				Replicas:         10,
				LastEvictionTime: 1620000000,
				Limit:            3,
			},
		},
	}

	// 高驱逐频率的 Pod
	candidatePod3 := &CandidatePod{
		Pod: pod3,
		WorkloadsEvictionInfo: map[string]*WorkloadEvictionInfo{
			workloadName: {
				WorkloadName: workloadName,
				StatsByWindow: map[float64]*EvictionStats{
					1:  {EvictionCount: 15, EvictionRatio: 0.8},
					24: {EvictionCount: 50, EvictionRatio: 0.6},
				},
				Replicas:         5,
				LastEvictionTime: 1620000000,
				Limit:            1,
			},
		},
	}

	// 中等驱逐频率的 Pod
	candidatePod4 := &CandidatePod{
		Pod: pod4,
		WorkloadsEvictionInfo: map[string]*WorkloadEvictionInfo{
			workloadName: {
				WorkloadName: workloadName,
				StatsByWindow: map[float64]*EvictionStats{
					1:  {EvictionCount: 8, EvictionRatio: 0.4},
					24: {EvictionCount: 30, EvictionRatio: 0.3},
				},
				Replicas:         8,
				LastEvictionTime: 1620000000,
				Limit:            2,
			},
		},
	}

	// 低驱逐频率但高CPU使用率的 Pod
	candidatePod5 := &CandidatePod{
		Pod: pod5,
		WorkloadsEvictionInfo: map[string]*WorkloadEvictionInfo{
			workloadName: {
				WorkloadName: workloadName,
				StatsByWindow: map[float64]*EvictionStats{
					1:  {EvictionCount: 1, EvictionRatio: 0.1},
					24: {EvictionCount: 5, EvictionRatio: 0.05},
				},
				Replicas:         15,
				LastEvictionTime: 1620000000,
				Limit:            5,
			},
		},
	}

	// 构造 NUMA 指标历史（包含所有测试 Pod 的指标）
	mockMetricsHistory := &history.NumaMetricHistory{
		Inner: map[int]map[string]map[string]*history.MetricRing{
			0: {
				"pod1": {
					consts.MetricCPUUsageContainer: {
						MaxLen: 2,
						Queue: []*history.MetricSnapshot{
							{Info: history.MetricInfo{Value: 0.8}},
						},
					},
				},
				"pod2": {
					consts.MetricCPUUsageContainer: {
						MaxLen: 2,
						Queue: []*history.MetricSnapshot{
							{Info: history.MetricInfo{Value: 0.6}},
						},
					},
				},
				"pod3": {
					consts.MetricCPUUsageContainer: {
						MaxLen: 2,
						Queue: []*history.MetricSnapshot{
							{Info: history.MetricInfo{Value: 0.9}},
						},
					},
				},
				"pod4": {
					consts.MetricCPUUsageContainer: {
						MaxLen: 2,
						Queue: []*history.MetricSnapshot{
							{Info: history.MetricInfo{Value: 0.7}},
						},
					},
				},
				"pod5": {
					consts.MetricCPUUsageContainer: {
						MaxLen: 2,
						Queue: []*history.MetricSnapshot{
							{Info: history.MetricInfo{Value: 0.95}},
						},
					},
				},
			},
		},
		RingSize: 2,
	}

	type fields struct {
		enabledScorers []string
		scorerParams   map[string]interface{}
	}
	type args struct {
		pods []*CandidatePod
	}
	type want struct {
		sortedPodNames []string // 按分数升序排列的 Pod 名称
	}

	tests := []struct {
		name   string
		fields fields
		args   args
		want   want
	}{{
		name: "no scorers enabled",
		fields: fields{
			enabledScorers: []string{},
			scorerParams:   nil,
		},
		args: args{pods: []*CandidatePod{candidatePod1, candidatePod2, candidatePod3, candidatePod4, candidatePod5}},
		want: want{sortedPodNames: []string{"pod1", "pod2", "pod3", "pod4", "pod5"}},
	}, {
		name: "PriorityScorer only",
		fields: fields{
			enabledScorers: []string{PriorityScorerName},
			scorerParams:   nil,
		},
		args: args{pods: []*CandidatePod{candidatePod1, candidatePod2, candidatePod3, candidatePod4, candidatePod5}},
		want: want{sortedPodNames: []string{"pod3", "pod5", "pod1", "pod4", "pod2"}},
	}, {
		name: "DeploymentEvictionFrequencyScorer only",
		fields: fields{
			enabledScorers: []string{DeploymentEvictionFrequencyScorerName},
			scorerParams:   nil,
		},
		args: args{pods: []*CandidatePod{candidatePod1, candidatePod2, candidatePod3, candidatePod4, candidatePod5}},
		want: want{sortedPodNames: []string{"pod5", "pod2", "pod1", "pod4", "pod3"}},
	}, {
		name: "UsageGapScorer only",
		fields: fields{
			enabledScorers: []string{UsageGapScorerName},
			scorerParams: map[string]interface{}{
				UsageGapScorerName: []NumaOverStat{{
					NumaID:         0,
					Gap:            0.5,
					MetricsHistory: mockMetricsHistory,
				}},
			},
		},
		args: args{pods: []*CandidatePod{candidatePod1, candidatePod2, candidatePod3, candidatePod4, candidatePod5}},
		want: want{sortedPodNames: []string{"pod2", "pod4", "pod1", "pod3", "pod5"}},
	}, {
		name: "multiple scorers (Priority + DeploymentEvictionFrequency + UsageGap)",
		fields: fields{
			enabledScorers: []string{PriorityScorerName, DeploymentEvictionFrequencyScorerName, UsageGapScorerName},
			scorerParams: map[string]interface{}{
				UsageGapScorerName: []NumaOverStat{{
					NumaID:         0,
					Gap:            0.5,
					MetricsHistory: mockMetricsHistory,
				}},
			},
		},
		args: args{pods: []*CandidatePod{candidatePod1, candidatePod2, candidatePod3, candidatePod4, candidatePod5}},
		want: want{sortedPodNames: []string{"pod3", "pod5", "pod1", "pod4", "pod2"}},
	}}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			scorer, err := NewScorer(tt.fields.enabledScorers, metrics.DummyMetrics{}, tt.fields.scorerParams)
			assert.NoError(t, err)

			result := scorer.Score(tt.args.pods)

			resultNames := make([]string, len(result))
			for i, pod := range result {
				resultNames[i] = pod.Pod.Name
				fmt.Println(resultNames[i], "total score:", pod.TotalScore)
				for name, score := range pod.Scores {
					fmt.Println(resultNames[i], name, "score:", score)
				}
			}
			assert.Equal(t, tt.want.sortedPodNames, resultNames)
		})
	}
}

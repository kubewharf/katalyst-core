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

package rules

import (
	"fmt"
	"testing"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/cpueviction/history"
	"github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/stretchr/testify/assert"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

func TestScorer_Score(t *testing.T) {
	t.Parallel()

	// NUMA Metric History
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
		sortedPodNames []string
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
		args: args{pods: makeCandidatePods()},
		want: want{sortedPodNames: []string{"pod1", "pod2", "pod3", "pod4", "pod5"}},
	}, {
		name: "DeploymentEvictionFrequencyScorer only",
		fields: fields{
			enabledScorers: []string{DeploymentEvictionFrequencyScorerName},
			scorerParams:   nil,
		},
		args: args{pods: makeCandidatePods()},
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
		args: args{pods: makeCandidatePods()},
		want: want{sortedPodNames: []string{"pod2", "pod4", "pod1", "pod3", "pod5"}},
	}, {
		name: "multiple scorers (DeploymentEvictionFrequency + UsageGap)",
		fields: fields{
			enabledScorers: []string{DeploymentEvictionFrequencyScorerName, UsageGapScorerName},
			scorerParams: map[string]interface{}{
				UsageGapScorerName: []NumaOverStat{{
					NumaID:         0,
					Gap:            0.5,
					MetricsHistory: mockMetricsHistory,
				}},
			},
		},
		args: args{pods: makeCandidatePods()},
		want: want{sortedPodNames: []string{"pod2", "pod5", "pod1", "pod4", "pod3"}},
	}}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
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

func TestDeploymentEvictionFrequencyScorer(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		pod       *CandidatePod
		wantScore int
	}{
		{
			name: "multiple windows with different weights",
			pod: &CandidatePod{
				Pod: makePod("test-pod"),
				WorkloadsEvictionInfo: map[string]*WorkloadEvictionInfo{
					workloadName: {
						WorkloadName: workloadName,
						StatsByWindow: map[float64]*EvictionStats{
							1:  {EvictionCount: 3, EvictionRatio: 0.3},
							24: {EvictionCount: 24, EvictionRatio: 0.2},
						},
						Replicas: 10,
						Limit:    3,
					},
				},
			},
			wantScore: 35,
		},
		{
			name: "zero eviction count",
			pod: &CandidatePod{
				Pod: makePod("test-pod"),
				WorkloadsEvictionInfo: map[string]*WorkloadEvictionInfo{
					workloadName: {
						WorkloadName: workloadName,
						StatsByWindow: map[float64]*EvictionStats{
							1: {EvictionCount: 0, EvictionRatio: 0},
						},
						Replicas: 10,
						Limit:    3,
					},
				},
			},
			wantScore: 0,
		},
		{
			name: "exceed limit normalization",
			pod: &CandidatePod{
				Pod: makePod("test-pod"),
				WorkloadsEvictionInfo: map[string]*WorkloadEvictionInfo{
					workloadName: {
						WorkloadName: workloadName,
						StatsByWindow: map[float64]*EvictionStats{
							1: {EvictionCount: 6, EvictionRatio: 0.5},
						},
						Replicas: 10,
						Limit:    3,
					},
				},
			},
			wantScore: 100,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			score := DeploymentEvictionFrequencyScorer(tt.pod, nil)
			assert.Equal(t, tt.wantScore, score)
		})
	}
}

func TestUsageGapScorer(t *testing.T) {
	t.Parallel()

	mockMetricsHistory := &history.NumaMetricHistory{
		Inner: map[int]map[string]map[string]*history.MetricRing{
			0: {
				"pod-uid": {
					consts.MetricCPUUsageContainer: {
						MaxLen: 2,
						Queue: []*history.MetricSnapshot{
							{Info: history.MetricInfo{Value: 0.8}},
						},
					},
				},
			},
		},
		RingSize: 2,
	}

	tests := []struct {
		name      string
		pod       *CandidatePod
		params    interface{}
		wantScore int
	}{
		{
			name: "normal case",
			pod: &CandidatePod{
				Pod: &v1.Pod{ObjectMeta: metav1.ObjectMeta{UID: types.UID("pod-uid")}},
			},
			params: []NumaOverStat{{
				NumaID:         0,
				Gap:            0.5,
				MetricsHistory: mockMetricsHistory,
			}},
			wantScore: 30,
		},
		{
			name: "no metric history",
			pod: &CandidatePod{
				Pod: &v1.Pod{ObjectMeta: metav1.ObjectMeta{UID: types.UID("unknown-uid")}},
			},
			params: []NumaOverStat{{
				NumaID:         0,
				Gap:            0.5,
				MetricsHistory: mockMetricsHistory,
			}},
			wantScore: 10,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			score := UsageGapScorer(tt.pod, tt.params)
			assert.Equal(t, tt.wantScore, score)
		})
	}
}

func TestNewScorer_InvalidScorer(t *testing.T) {
	t.Parallel()
	_, err := NewScorer([]string{"invalidScorer"}, metrics.DummyMetrics{}, nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "scorer invalidScorer not exists")
}

func TestScore_NilPods(t *testing.T) {
	t.Parallel()
	scorer, _ := NewScorer([]string{PriorityScorerName}, metrics.DummyMetrics{}, nil)
	result := scorer.Score([]*CandidatePod{nil, {Pod: makePod("valid")}})
	assert.Len(t, result, 1)
	assert.NotNil(t, result[0])
}

func TestScorer_EmptyScorers(t *testing.T) {
	t.Parallel()
	scorer, _ := NewScorer([]string{}, metrics.DummyMetrics{}, nil)
	pods := []*CandidatePod{{Pod: makePod("test")}}
	result := scorer.Score(pods)
	assert.Equal(t, pods, result)
}

func TestNormalizeCount(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		perHourCount float64
		limit        int32
		want         float64
	}{
		{
			name:         "limit zero",
			perHourCount: 5,
			limit:        0,
			want:         5,
		},
		{
			name:         "count equals limit",
			perHourCount: 3,
			limit:        3,
			want:         10,
		},
		{
			name:         "count exceeds limit",
			perHourCount: 6,
			limit:        3,
			want:         20,
		},
		{
			name:         "count below limit",
			perHourCount: 1,
			limit:        3,
			want:         3.3333333333333335,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := normalizeCount(tt.perHourCount, tt.limit)
			assert.InDelta(t, tt.want, result, 0.001)
		})
	}
}

func TestSetScorerParam(t *testing.T) {
	t.Parallel()
	scorer, _ := NewScorer([]string{UsageGapScorerName}, metrics.DummyMetrics{}, nil)
	testParam := []NumaOverStat{{NumaID: 1}}
	scorer.SetScorerParam(UsageGapScorerName, testParam)
	assert.Equal(t, testParam, scorer.scorerParams[UsageGapScorerName])
}

func TestSetScorer(t *testing.T) {
	t.Parallel()
	scorer, _ := NewScorer([]string{}, metrics.DummyMetrics{}, nil)
	customScore := 100
	customScorer := func(pod *CandidatePod, params interface{}) int {
		return customScore
	}

	scorer.SetScorer("custom", customScorer)
	if _, exists := scorer.scorers["custom"]; !exists {
		t.Fatal("custom scorer not found in scorer.scorers")
	}
	testPod := &CandidatePod{Pod: makePod("test-pod")}
	scoredPods := scorer.Score([]*CandidatePod{testPod})
	if len(scoredPods) == 0 {
		t.Fatal("no pods scored")
	}
	actualScore, ok := scoredPods[0].Scores["custom"]
	if !ok {
		t.Error("custom scorer score not found in result")
	}
	assert.Equal(t, customScore, actualScore, "expected custom score %d, got %d", customScore, actualScore)
}

func makeCandidatePods() []*CandidatePod {
	// pod and CandidatePod
	pod1 := makePod("pod1")
	pod1.Spec.Priority = new(int32)
	*pod1.Spec.Priority = 100

	pod2 := makePod("pod2")
	pod2.Spec.Priority = new(int32)
	*pod2.Spec.Priority = 200

	pod3 := makePod("pod3")
	pod3.Spec.Priority = new(int32)
	*pod3.Spec.Priority = 50

	pod4 := makePod("pod4")
	pod4.Spec.Priority = new(int32)
	*pod4.Spec.Priority = 150

	pod5 := makePod("pod5")
	pod5.Spec.Priority = new(int32)
	*pod5.Spec.Priority = 75

	// CandidatePod with eviction history
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

	// CandidatePod with high eviction frequency
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

	// CandidatePod with medium eviction frequency
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

	// CandidatePod with low eviction frequency but high CPU usage
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
	return []*CandidatePod{candidatePod1, candidatePod2, candidatePod3, candidatePod4, candidatePod5}
}

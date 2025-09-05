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
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/util"
	"github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
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
		filterParams   EvictRules
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
				filterParams:   EvictRules{},
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
				filterParams: EvictRules{
					SkippedPodKinds: []string{"DaemonSet"},
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
			name: "OverloadNumaFilter",
			fields: fields{
				enabledFilters: []string{OverloadNumaFilterName},
				filterParams: EvictRules{
					NumaOverStats: []NumaOverStat{
						{
							NumaID: 0,
							MetricsHistory: &util.NumaMetricHistory{
								Inner: map[int]map[string]map[string]*util.MetricRing{
									0: {
										util.FakePodUID: {
											consts.MetricCPUUsageContainer: {
												MaxLen: 2,
												Queue: []*util.MetricSnapshot{
													{Info: util.MetricInfo{Name: consts.MetricCPUUsageContainer, Value: 1.0}},
													nil,
												},
												CurrentIndex: 1,
											},
										},
										"pod1": {
											consts.MetricCPUUsageContainer: {
												MaxLen: 2,
												Queue: []*util.MetricSnapshot{
													{Info: util.MetricInfo{Name: consts.MetricCPUUsageContainer, Value: 0.5}},
													nil,
												},
												CurrentIndex: 1,
											},
										},
										"pod2": {
											consts.MetricCPUUsageContainer: {
												MaxLen: 2,
												Queue: []*util.MetricSnapshot{
													{Info: util.MetricInfo{Name: consts.MetricCPUUsageContainer, Value: 0.25}},
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
			name: "multiple filters : OwnerRefFilter and OverloadNumaFilter",
			fields: fields{
				enabledFilters: []string{OwnerRefFilterName, OverloadNumaFilterName},
				filterParams: EvictRules{
					SkippedPodKinds: []string{"DaemonSet"},
					NumaOverStats: []NumaOverStat{
						{
							NumaID: 0,
							MetricsHistory: &util.NumaMetricHistory{
								Inner: map[int]map[string]map[string]*util.MetricRing{
									0: {
										util.FakePodUID: {
											consts.MetricCPUUsageContainer: {
												MaxLen: 2,
												Queue: []*util.MetricSnapshot{
													{Info: util.MetricInfo{Name: consts.MetricCPUUsageContainer, Value: 1.0}},
													nil,
												},
												CurrentIndex: 1,
											},
										},
										"pod1": {
											consts.MetricCPUUsageContainer: {
												MaxLen: 2,
												Queue: []*util.MetricSnapshot{
													{Info: util.MetricInfo{Name: consts.MetricCPUUsageContainer, Value: 0.5}},
													nil,
												},
												CurrentIndex: 1,
											},
										},
										"pod2": {
											consts.MetricCPUUsageContainer: {
												MaxLen: 2,
												Queue: []*util.MetricSnapshot{
													{Info: util.MetricInfo{Name: consts.MetricCPUUsageContainer, Value: 0.25}},
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
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			filterer, err := NewFilter(metrics.DummyMetrics{}, tt.fields.filterParams, tt.fields.enabledFilters)
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

func TestOwnerRefFilter(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		pod          *v1.Pod
		params       EvictRules
		wantFiltered bool
	}{
		{
			name: "daemonset owner should be filtered",
			pod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-pod",
					OwnerReferences: []metav1.OwnerReference{
						{Kind: "DaemonSet"},
					},
				},
			},
			params: EvictRules{
				SkippedPodKinds: []string{"DaemonSet"},
			},
			wantFiltered: false,
		},
		{
			name: "deployment owner should not be filtered",
			pod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-pod",
					OwnerReferences: []metav1.OwnerReference{
						{Kind: "Deployment"},
					},
				},
			},
			params: EvictRules{
				SkippedPodKinds: []string{"DaemonSet"},
			},
			wantFiltered: true,
		},
		{
			name:         "nil params should not filter",
			pod:          &v1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "test-pod"}},
			params:       EvictRules{},
			wantFiltered: true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := OwnerRefFilter(tt.pod, tt.params)
			assert.Equal(t, tt.wantFiltered, result)
		})
	}
}

func TestOverloadNumaFilter(t *testing.T) {
	t.Parallel()

	mockMetrics := &util.NumaMetricHistory{
		Inner: map[int]map[string]map[string]*util.MetricRing{
			0: {
				"pod-uid-1": {consts.MetricCPUUsageContainer: {Queue: []*util.MetricSnapshot{{Info: util.MetricInfo{Value: 0.8}}}}},
			},
		},
	}

	tests := []struct {
		name         string
		pod          *v1.Pod
		params       EvictRules
		wantFiltered bool
	}{
		{
			name: "pod with high usage should be reserved",
			pod:  &v1.Pod{ObjectMeta: metav1.ObjectMeta{UID: types.UID("pod-uid-1")}},
			params: EvictRules{
				NumaOverStats: []NumaOverStat{{
					NumaID:         0,
					MetricsHistory: mockMetrics,
				}},
			},
			wantFiltered: true,
		},
		{
			name: "pod without metric history should not be reserved",
			pod:  &v1.Pod{ObjectMeta: metav1.ObjectMeta{UID: types.UID("pod-uid-2")}},
			params: EvictRules{
				NumaOverStats: []NumaOverStat{{
					NumaID:         0,
					MetricsHistory: mockMetrics,
				}},
			},
			wantFiltered: false,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := OverloadNumaFilter(tt.pod, tt.params)
			assert.Equal(t, tt.wantFiltered, result)
		})
	}
}

func TestFilterer_SetFilter(t *testing.T) {
	t.Parallel()
	filterer, _ := NewFilter(metrics.DummyMetrics{}, EvictRules{}, []string{"custom"})
	customFilter := func(pod *v1.Pod, evictRules EvictRules) bool {
		return pod.Name != "custom-filter-pod"
	}

	filterer.SetFilter("custom", customFilter)
	if _, exists := filterer.filterFuncs["custom"]; !exists {
		t.Fatal("custom filter not found in filterer.filters")
	}
	testPod := &v1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "custom-filter-pod"}}
	result := filterer.Filter([]*v1.Pod{testPod})
	assert.Empty(t, result)
}

func TestFilterer_SetFilterParam(t *testing.T) {
	t.Parallel()
	filterer, _ := NewFilter(metrics.DummyMetrics{}, EvictRules{}, []string{OwnerRefFilterName})
	testParams := EvictRules{
		SkippedPodKinds: []string{"StatefulSet"},
	}

	filterer.SetFilterParam(testParams)
	assert.Equal(t, testParams, filterer.filterParams)

	result := filterer.Filter([]*v1.Pod{{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "test-pod",
			OwnerReferences: []metav1.OwnerReference{{Kind: "StatefulSet"}},
		},
	}})
	assert.Empty(t, result)
}

func TestNewFilter_InvalidFilter(t *testing.T) {
	t.Parallel()
	filterer, err := NewFilter(metrics.DummyMetrics{}, EvictRules{}, []string{"invalid-filter"})
	assert.NoError(t, err)
	pod1 := makePod("pod1")
	var result []*v1.Pod
	assert.NotPanics(t, func() {
		result = filterer.Filter([]*v1.Pod{pod1})
	})

	// Since the invalid filter is skipped, no filtering happens, and the original pod should be in the result.
	assert.Len(t, result, 1)
	assert.Equal(t, "pod1", result[0].Name)
}

func TestFilterer_EmptyPodList(t *testing.T) {
	t.Parallel()
	filterer, _ := NewFilter(metrics.DummyMetrics{}, EvictRules{
		SkippedPodKinds: []string{"DaemonSet"},
	}, []string{OwnerRefFilterName})
	result := filterer.Filter([]*v1.Pod{})
	assert.Empty(t, result)
}

func TestFilterer_NilPod(t *testing.T) {
	t.Parallel()
	filterer, _ := NewFilter(metrics.DummyMetrics{}, EvictRules{
		SkippedPodKinds: []string{"DaemonSet"},
	}, []string{OwnerRefFilterName})
	result := filterer.Filter([]*v1.Pod{nil, {ObjectMeta: metav1.ObjectMeta{Name: "valid-pod"}}})
	assert.Len(t, result, 1)
	assert.Equal(t, "valid-pod", result[0].Name)
}

func TestOverloadNumaFilter_InvalidMetrics(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		params       EvictRules
		wantFiltered bool
	}{
		{
			name: "nil metrics history",
			params: EvictRules{
				NumaOverStats: []NumaOverStat{{
					NumaID:         0,
					MetricsHistory: nil,
				}},
			},
			wantFiltered: true,
		},
		{
			name:         "empty numa stats",
			params:       EvictRules{NumaOverStats: []NumaOverStat{}},
			wantFiltered: true,
		},
		{
			name: "numa not found",
			params: EvictRules{NumaOverStats: []NumaOverStat{{
				NumaID:         999,
				MetricsHistory: &util.NumaMetricHistory{Inner: map[int]map[string]map[string]*util.MetricRing{}},
			}}},
			wantFiltered: true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pod := makePod("test-pod")
			result := OverloadNumaFilter(pod, tt.params)
			assert.Equal(t, tt.wantFiltered, result)
		})
	}
}

func TestRegisterFilter(t *testing.T) {
	t.Parallel()
	RegisterFilter("custom", func(pod *v1.Pod, evictRules EvictRules) bool {
		return true
	})
	filterer, _ := NewFilter(metrics.DummyMetrics{}, EvictRules{}, []string{"custom"})
	if _, exists := filterer.filterFuncs["custom"]; !exists {
		t.Fatal("custom filter not found in filterer.filters")
	}
}

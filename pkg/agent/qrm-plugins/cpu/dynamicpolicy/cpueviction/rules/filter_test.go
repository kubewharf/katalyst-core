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

func TestOwnerRefFilter(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		pod          *v1.Pod
		params       interface{}
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
			params:       []string{"DaemonSet"},
			wantFiltered: true,
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
			params:       []string{"DaemonSet"},
			wantFiltered: false,
		},
		{
			name:         "nil params should not filter",
			pod:          &v1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "test-pod"}},
			params:       nil,
			wantFiltered: false,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			result := OwnerRefFilter(tt.pod, tt.params)
			assert.Equal(t, tt.wantFiltered, result)
		})
	}
}

func TestOverRatioNumaFilter(t *testing.T) {
	t.Parallel()

	mockMetrics := &history.NumaMetricHistory{
		Inner: map[int]map[string]map[string]*history.MetricRing{
			0: {
				"pod-uid-1": {consts.MetricCPUUsageContainer: {Queue: []*history.MetricSnapshot{{Info: history.MetricInfo{Value: 0.8}}}}},
			},
		},
	}

	tests := []struct {
		name         string
		pod          *v1.Pod
		params       interface{}
		wantFiltered bool
	}{
		{
			name: "pod with high usage should be filtered",
			pod:  &v1.Pod{ObjectMeta: metav1.ObjectMeta{UID: types.UID("pod-uid-1")}},
			params: []NumaOverStat{{
				NumaID:         0,
				MetricsHistory: mockMetrics,
			}},
			wantFiltered: true,
		},
		{
			name: "pod without metric history should not be filtered",
			pod:  &v1.Pod{ObjectMeta: metav1.ObjectMeta{UID: types.UID("pod-uid-2")}},
			params: []NumaOverStat{{
				NumaID:         0,
				MetricsHistory: mockMetrics,
			}},
			wantFiltered: false,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			result := OverRatioNumaFilter(tt.pod, tt.params)
			assert.Equal(t, tt.wantFiltered, result)
		})
	}
}

func TestFilterer_SetFilter(t *testing.T) {
	filterer, _ := NewFilter([]string{}, metrics.DummyMetrics{}, nil)
	customFilter := func(pod *v1.Pod, params interface{}) bool { return pod.Name == "custom-filter-pod" }

	filterer.SetFilter("custom", customFilter)
	assert.Equal(t, customFilter, filterer.filters["custom"])

	result := filterer.Filter([]*v1.Pod{{ObjectMeta: metav1.ObjectMeta{Name: "custom-filter-pod"}}})
	assert.Empty(t, result)
}

func TestFilterer_SetFilterParam(t *testing.T) {
	filterer, _ := NewFilter([]string{OwnerRefFilterName}, metrics.DummyMetrics{}, nil)
	testParams := []string{"StatefulSet"}

	filterer.SetFilterParam(OwnerRefFilterName, testParams)
	assert.Equal(t, testParams, filterer.filterParams[OwnerRefFilterName])

	result := filterer.Filter([]*v1.Pod{{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "test-pod",
			OwnerReferences: []metav1.OwnerReference{{Kind: "StatefulSet"}},
		},
	}})
	assert.Empty(t, result)
}

func TestNewFilter_InvalidFilter(t *testing.T) {
	filterer, err := NewFilter([]string{"invalid-filter"}, metrics.DummyMetrics{}, nil)
	assert.NoError(t, err)
	assert.Empty(t, filterer.filters)
}

func TestFilterer_EmptyPodList(t *testing.T) {
	filterer, _ := NewFilter([]string{OwnerRefFilterName}, metrics.DummyMetrics{}, map[string]interface{}{OwnerRefFilterName: []string{"DaemonSet"}})
	result := filterer.Filter([]*v1.Pod{})
	assert.Empty(t, result)
}

func TestFilterer_NilPod(t *testing.T) {
	filterer, _ := NewFilter([]string{OwnerRefFilterName}, metrics.DummyMetrics{}, map[string]interface{}{OwnerRefFilterName: []string{"DaemonSet"}})
	result := filterer.Filter([]*v1.Pod{nil, {ObjectMeta: metav1.ObjectMeta{Name: "valid-pod"}}})
	assert.Len(t, result, 1)
	assert.Equal(t, "valid-pod", result[0].Name)
}

func TestOverRatioNumaFilter_InvalidMetrics(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		params       interface{}
		wantFiltered bool
	}{
		{
			name: "nil metrics history",
			params: []NumaOverStat{{
				NumaID:         0,
				MetricsHistory: nil,
			}},
			wantFiltered: false,
		},
		{
			name:         "empty numa stats",
			params:       []NumaOverStat{},
			wantFiltered: false,
		},
		{
			name: "numa not found",
			params: []NumaOverStat{{
				NumaID:         999,
				MetricsHistory: &history.NumaMetricHistory{Inner: map[int]map[string]map[string]*history.MetricRing{}},
			}},
			wantFiltered: false,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			pod := makePod("test-pod")
			result := OverRatioNumaFilter(pod, tt.params)
			assert.Equal(t, tt.wantFiltered, result)
		})
	}
}

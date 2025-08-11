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
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/sets"

	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

const (
	OwnerRefFilterName      = "OwnerRef"
	OverRatioNumaFilterName = "OverRatioNuma"
)

// Returns true if the pod should be filtered out (excluded from eviction candidates)
type FilterFunc func(pod *v1.Pod, params interface{}) bool

type Filterer struct {
	filters      map[string]FilterFunc
	filterParams map[string]interface{}
	emitter      metrics.MetricEmitter
}

// allFilters contains all available filter implementations that can be enabled
var allFilters = map[string]FilterFunc{
	OwnerRefFilterName:      OwnerRefFilter,
	OverRatioNumaFilterName: OverRatioNumaFilter,
}

func NewFilter(enabledFilters []string, emitter metrics.MetricEmitter, filterParams map[string]interface{}) (*Filterer, error) {
	if filterParams == nil {
		general.Warningf("filterParams is nil, using empty parameters for all filters")
		filterParams = make(map[string]interface{})
	}
	enabled := sets.NewString(enabledFilters...)
	filters := make(map[string]FilterFunc)
	params := make(map[string]interface{})

	for name := range enabled {
		filter, exists := allFilters[name]
		if !exists {
			general.Warningf("filter %s not found in available filters", name)
			continue
		}
		filters[name] = filter
		params[name] = filterParams[name]
	}
	general.Infof("initialized filterer with %d enabled filters", len(filters))
	return &Filterer{
		filters:      filters,
		filterParams: params,
		emitter:      emitter,
	}, nil
}

func (f *Filterer) Filter(pods []*v1.Pod) []*v1.Pod {
	if len(f.filters) == 0 {
		general.Warningf("no filters enabled, returning all pods")
		return pods
	}

	var filteredPods []*v1.Pod
podLoop:
	for _, pod := range pods {
		for name, filter := range f.filters {
			if filter(pod, f.filterParams[name]) {
				_ = f.emitter.StoreInt64("qrm_eviction_filter_pods_total", 1, metrics.MetricTypeNameCount,
					metrics.MetricTag{Key: "filter_name", Val: name},
					metrics.MetricTag{Key: "result", Val: "filtered"},
				)
				continue podLoop
			}
		}
		filteredPods = append(filteredPods, pod)
	}
	general.Infof("filtering completed: %d pods input, %d pods passed", len(pods), len(filteredPods))
	return filteredPods
}

func (f *Filterer) SetFilterParam(key string, value interface{}) {
	if key == "" {
		general.Warningf("filter key is empty, will not set filter param")
		return
	}
	f.filterParams[key] = value
}

func (f *Filterer) SetFilter(key string, filter FilterFunc) {
	if key == "" {
		general.Warningf("filter key is empty, will not set filter")
		return
	}
	f.filters[key] = filter
}

func OwnerRefFilter(pod *v1.Pod, params interface{}) bool {
	if pod == nil {
		general.Warningf("OwnerRefFilter: pod is nil, no pods will be filtered")
		return true
	}
	if pod.OwnerReferences == nil || len(pod.OwnerReferences) == 0 {
		general.Warningf("OwnerRefFilter: pod %s has no ownerRef, will not be filtered", pod.Name)
		return false
	}

	skippedPodKinds, ok := params.([]string)
	if !ok || len(skippedPodKinds) == 0 {
		general.Warningf("OwnerRefFilter params is not []string, no pods will be filtered")
		return false
	}
	general.Infof("OwnerRefFilter: skippedPodKinds %v", skippedPodKinds)
	for _, ownerRef := range pod.OwnerReferences {
		for _, kind := range skippedPodKinds {
			if ownerRef.Kind == kind {
				general.Infof("OwnerRefFilter: pod %s is owned by %s, will be filtered", pod.Name, kind)
				return true
			}
		}
	}
	return false
}

// OverRatioNumaFilter filters out pods that are overloaded on NUMA nodes
func OverRatioNumaFilter(pod *v1.Pod, params interface{}) bool {
	if pod == nil {
		general.Warningf("OverRatioNumaFilter: pod is nil, no pods will be filtered")
		return true
	}

	numaStats, ok := params.([]NumaOverStat)
	if !ok {
		general.Warningf("OverRatioNumaFilter params is not []NumaOverStat, no pods will be filtered")
		return false
	}
	if len(numaStats) == 0 {
		general.Warningf("OverRatioNumaFilter: numaStats is empty, no pods will be filtered")
		return false
	}
	numaID := numaStats[0].NumaID
	if numaStats[0].MetricsHistory == nil {
		general.Warningf("OverRatioNumaFilter: numaStats[0].MetricsHistory is nil, no pods will be filtered")
		return false
	}
	numaHis, ok := numaStats[0].MetricsHistory.Inner[numaID]
	if !ok {
		general.Warningf("OverRatioNumaFilter: numa %d not found in numaStats, no pods will be filtered", numaID)
		return false
	}

	_, existMetric := numaHis[string(pod.UID)]
	if existMetric {
		general.Infof("OverRatioNumaFilter: pod %s is overloaded on numa %d, will be filtered", pod.Name, numaID)
	}

	return !existMetric
}

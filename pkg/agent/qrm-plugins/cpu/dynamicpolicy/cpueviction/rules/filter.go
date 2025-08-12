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
	OwnerRefFilterName     = "OwnerRef"
	OverloadNumaFilterName = "OverloadNuma"
)

var DefaultEnabledFilters = []string{
	OwnerRefFilterName,
	OverloadNumaFilterName,
}

// Filter returns true if the pod should be filtered out (excluded from eviction candidates)
type FilterFunc func(pod *v1.Pod, params interface{}) bool

type Filterer struct {
	filters      map[string]FilterFunc
	filterParams map[string]interface{}
	emitter      metrics.MetricEmitter
}

// allFilters contains all available filter implementations that can be enabled
var allFilters = map[string]FilterFunc{
	OwnerRefFilterName:     OwnerRefFilter,
	OverloadNumaFilterName: OverloadNumaFilter,
}

func NewFilter(emitter metrics.MetricEmitter, filterParams map[string]interface{}) (*Filterer, error) {
	if filterParams == nil {
		general.Warningf("filterParams is nil, using empty parameters for all filters")
		filterParams = make(map[string]interface{})
	}

	filters := make(map[string]FilterFunc)
	params := make(map[string]interface{})

	for name, filter := range allFilters {
		filters[name] = filter
		params[name] = filterParams[name]
	}
	general.Infof("initialized filterer with %d enabled filters default", len(filters))
	return &Filterer{
		filters:      filters,
		filterParams: params,
		emitter:      emitter,
	}, nil
}

// Filter filters the given pods based on the enabled filters and their parameters
func (f *Filterer) Filter(enabledFilters []string, pods []*v1.Pod) []*v1.Pod {
	if len(f.filters) == 0 || len(enabledFilters) == 0 {
		general.Warningf("no filters, returning all pods")
		return pods
	}
	enabled := sets.NewString(enabledFilters...)
	// general.Infof(" registered filters : %v", f.filters)
	general.Infof("enabled filters finally: %v", enabled)

	var filteredPods []*v1.Pod
podLoop:
	for _, pod := range pods {
		for name := range enabled {
			filter, ok := f.filters[name]
			if !ok {
				general.Warningf("filter %q is enabled but not found, skipping this filter", name)
				continue
			}
			if !filter(pod, f.filterParams[name]) {
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
		// general.Warningf("OwnerRefFilter: pod is nil, will be filtered")
		return false
	}
	if pod.OwnerReferences == nil || len(pod.OwnerReferences) == 0 {
		// general.Warningf("OwnerRefFilter: pod %s has no ownerRef, will pass", pod.Name)
		return true
	}

	skippedPodKinds, ok := params.([]string)
	if !ok || len(skippedPodKinds) == 0 {
		// general.Warningf("OwnerRefFilter params is not []string, all pods pass")
		return true
	}
	// general.Infof("OwnerRefFilter: skippedPodKinds %v", skippedPodKinds)
	for _, ownerRef := range pod.OwnerReferences {
		for _, kind := range skippedPodKinds {
			if ownerRef.Kind == kind {
				// general.Infof("OwnerRefFilter: pod %s is owned by %s, will be filtered", pod.Name, kind)
				return false
			}
		}
	}
	// general.Infof("OwnerRefFilter: pod %s has no ownerRef in skippedPodKinds, will pass", pod.Name)
	return true
}

// OverloadNumaFilter filters out pods that are overloaded on NUMA nodes
func OverloadNumaFilter(pod *v1.Pod, params interface{}) bool {
	if pod == nil {
		// general.Warningf("OverloadNumaFilter: pod is nil, no pods will be filtered")
		return false
	}

	numaStats, ok := params.([]NumaOverStat)
	if !ok {
		// general.Warningf("OverloadNumaFilter params is not []NumaOverStat, no pods will be filtered")
		return true
	}
	if len(numaStats) == 0 {
		// general.Warningf("OverloadNumaFilter: numaStats is empty, no pods will be filtered")
		return true
	}
	numaID := numaStats[0].NumaID
	if numaStats[0].MetricsHistory == nil {
		// general.Warningf("OverloadNumaFilter: numaStats[0].MetricsHistory is nil, no pods will be filtered")
		return true
	}
	numaHis, ok := numaStats[0].MetricsHistory.Inner[numaID]
	if !ok {
		// general.Warningf("OverloadNumaFilter: numa %d not found in numaStats, no pods will be filtered", numaID)
		return true
	}

	_, existMetric := numaHis[string(pod.UID)]

	return existMetric
}

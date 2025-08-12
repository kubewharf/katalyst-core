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
type FilterFunc func(pod *v1.Pod, evictRules EvictRules) bool

type Filterer struct {
	filterFuncs  map[string]FilterFunc
	filterParams EvictRules
	emitter      metrics.MetricEmitter
}

// registeredFilters contains all available filter implementations that can be enabled
var registeredFilters = map[string]FilterFunc{
	OwnerRefFilterName:     OwnerRefFilter,
	OverloadNumaFilterName: OverloadNumaFilter,
}

func NewFilter(emitter metrics.MetricEmitter, evictRules EvictRules, enabledFilters []string) (*Filterer, error) {
	enabled := sets.NewString(enabledFilters...)
	filters := make(map[string]FilterFunc)
	for name := range enabled {
		if filter, ok := registeredFilters[name]; ok {
			filters[name] = filter
		} else {
			general.Warningf("filter %q is enabled but not found", name)
		}

	}

	general.Infof("initialized filterer with %d enabled filters: %v", len(filters), enabled)
	return &Filterer{
		filterFuncs:  filters,
		filterParams: evictRules,
		emitter:      emitter,
	}, nil
}

func (f *Filterer) Filter(pods []*v1.Pod) []*v1.Pod {
	if len(f.filterFuncs) == 0 {
		general.Warningf("no filters, returning all pods")
		return pods
	}
	var filteredPods []*v1.Pod

	for _, pod := range pods {
		allFiltersPassed := true
		for _, filter := range f.filterFuncs {
			if !filter(pod, f.filterParams) {
				allFiltersPassed = false
				break
			}
		}
		if allFiltersPassed {
			filteredPods = append(filteredPods, pod)
		}
	}
	general.Infof("filtering completed: %d pods input, %d pods passed", len(pods), len(filteredPods))
	return filteredPods
}

func (f *Filterer) SetFilterParam(evictRules EvictRules) {
	f.filterParams = evictRules
}

func (f *Filterer) SetFilter(key string, filter FilterFunc) {
	if key == "" {
		general.Warningf("filter key is empty, will not set filter")
		return
	}
	f.filterFuncs[key] = filter
}

func OwnerRefFilter(pod *v1.Pod, evictRules EvictRules) bool {
	if pod == nil {
		return false
	}
	if pod.OwnerReferences == nil || len(pod.OwnerReferences) == 0 {
		return true
	}

	skippedPodKinds := evictRules.SkippedPodKinds
	for _, ownerRef := range pod.OwnerReferences {
		for _, kind := range skippedPodKinds {
			if ownerRef.Kind == kind {
				return false
			}
		}
	}
	return true
}

// OverloadNumaFilter filters out pods that are overloaded on NUMA nodes
func OverloadNumaFilter(pod *v1.Pod, evictRules EvictRules) bool {
	if pod == nil {
		return false
	}
	numaStats := evictRules.NumaOverStats
	if len(numaStats) == 0 {
		return true
	}
	numaID := numaStats[0].NumaID
	if numaStats[0].MetricsHistory == nil {
		return true
	}
	numaHis, ok := numaStats[0].MetricsHistory.Inner[numaID]
	if !ok {
		return true
	}

	_, existMetric := numaHis[string(pod.UID)]

	return existMetric
}

func RegisterFilter(filterName string, filter FilterFunc) {
	registeredFilters[filterName] = filter
}

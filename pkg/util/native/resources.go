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

package native

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/klog/v2"

	"github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
)

type QuantityGetter func(resourceList v1.ResourceList) resource.Quantity

var (
	quantityGetterMutex  = sync.RWMutex{}
	cpuQuantityGetter    = DefaultCPUQuantityGetter
	memoryQuantityGetter = DefaultMemoryQuantityGetter
)

func CPUQuantityGetter() QuantityGetter {
	quantityGetterMutex.RLock()
	defer quantityGetterMutex.RUnlock()

	return cpuQuantityGetter
}

func SetCPUQuantityGetter(getter QuantityGetter) {
	quantityGetterMutex.Lock()
	defer quantityGetterMutex.Unlock()

	cpuQuantityGetter = getter
}

func MemoryQuantityGetter() QuantityGetter {
	quantityGetterMutex.RLock()
	defer quantityGetterMutex.RUnlock()

	return memoryQuantityGetter
}

func SetMemoryQuantityGetter(getter QuantityGetter) {
	quantityGetterMutex.Lock()
	defer quantityGetterMutex.Unlock()

	memoryQuantityGetter = getter
}

// DefaultCPUQuantityGetter returns cpu quantity for resourceList. since we may have
// different representations for cpu resource name, the prioritizes will be:
// native cpu name -> reclaimed milli cpu name
func DefaultCPUQuantityGetter(resourceList v1.ResourceList) resource.Quantity {
	if quantity, ok := resourceList[v1.ResourceCPU]; ok {
		return quantity
	}

	if quantity, ok := resourceList[consts.ReclaimedResourceMilliCPU]; ok {
		return *resource.NewMilliQuantity(quantity.Value(), quantity.Format)
	}

	return resource.Quantity{}
}

// DefaultMemoryQuantityGetter returns memory quantity for resourceList. since we may have
// different representations for memory resource name, the prioritizes will be:
// native memory name -> reclaimed memory name
func DefaultMemoryQuantityGetter(resourceList v1.ResourceList) resource.Quantity {
	if quantity, ok := resourceList[v1.ResourceMemory]; ok {
		return quantity
	}

	if quantity, ok := resourceList[consts.ReclaimedResourceMemory]; ok {
		return quantity
	}

	return resource.Quantity{}
}

// ResourceThreshold is map of resource name to threshold of water level
type ResourceThreshold map[v1.ResourceName]float64

func (t *ResourceThreshold) Type() string {
	return "ResourceThreshold"
}

func (t *ResourceThreshold) String() string {
	var pairs []string
	for k, v := range *t {
		pairs = append(pairs, fmt.Sprintf("%s=%f", k, v))
	}
	sort.Strings(pairs)
	return strings.Join(pairs, ",")
}

func (t *ResourceThreshold) Set(value string) error {
	for _, s := range strings.Split(value, ",") {
		if len(s) == 0 {
			continue
		}
		arr := strings.SplitN(s, "=", 2)
		if len(arr) == 2 {
			parseFloat, err := strconv.ParseFloat(arr[1], 64)
			if err != nil {
				return err
			}
			(*t)[v1.ResourceName(strings.TrimSpace(arr[0]))] = parseFloat
		}
	}
	return nil
}

// ResourcesEqual checks whether the given resources are equal with each other
func ResourcesEqual(a, b v1.ResourceList) bool {
	if len(a) != len(b) {
		return false
	}

	for name, quantity := range a {
		if !quantity.Equal(b[name]) {
			return false
		}
	}

	return true
}

// AddResources sums up two ResourceList, and returns the summed as results.
func AddResources(a, b v1.ResourceList) v1.ResourceList {
	res := make(v1.ResourceList)
	for resourceName := range a {
		res[resourceName] = a[resourceName].DeepCopy()
	}
	for resourceName := range b {
		quantity := b[resourceName].DeepCopy()
		if _, ok := res[resourceName]; ok {
			quantity.Add(res[resourceName])
		}

		res[resourceName] = quantity
	}
	return res
}

// MergeResources merge multi ResourceList into one ResourceList, the resource of
// same resource name in all ResourceList we only use the first merged one.
func MergeResources(updateList ...*v1.ResourceList) *v1.ResourceList {
	var result *v1.ResourceList

	for _, update := range updateList {
		if update == nil {
			continue
		}

		if result == nil {
			result = &v1.ResourceList{}
		}

		for name, res := range *update {
			if _, ok := (*result)[name]; !ok {
				(*result)[name] = res.DeepCopy()
			}
		}
	}

	return result
}

// EmitResourceMetrics emit metrics for given ResourceList.
func EmitResourceMetrics(name string, resourceList v1.ResourceList,
	tags map[string]string, emitter metrics.MetricEmitter) {
	if emitter == nil {
		klog.Warningf("EmitResourceMetrics by nil emitter")
		return
	}

	for k, q := range resourceList {
		resourceName, value := getResourceMetricsName(k, q)
		tags["resource"] = resourceName
		_ = emitter.StoreInt64(name, value, metrics.MetricTypeNameRaw, metrics.ConvertMapToTags(tags)...)
	}
}

// IsResourceGreaterThan checks if recommended resource is scaling down
func IsResourceGreaterThan(a resource.Quantity, b resource.Quantity) bool {
	return (&a).Cmp(b) > 0
}

// PodResourceDiff checks if pod resources are not the same as the given resource map, both for requests and limits.
func PodResourceDiff(pod *v1.Pod, containerResourcesToUpdate map[string]v1.ResourceRequirements) bool {
	for c, resources := range containerResourcesToUpdate {
		findContainer := false
		for _, container := range pod.Spec.Containers {
			if container.Name == c {
				findContainer = true
				for res, q := range resources.Limits {
					findResource := false
					for n, l := range container.Resources.Limits {
						if n == res {
							findResource = true
							if !q.Equal(l) {
								return true
							}
						}
					}
					if !findResource {
						return true
					}
				}
				for res, q := range resources.Requests {
					findResource := false
					for n, l := range container.Resources.Requests {
						if n == res {
							findResource = true
							if !q.Equal(l) {
								return true
							}
						}
					}
					if !findResource {
						return true
					}
				}
			}
		}
		if !findContainer {
			return false
		}
	}
	return false
}

// ResourceQuantityToInt64Value returns the int64 value according to its resource name
func ResourceQuantityToInt64Value(resourceName v1.ResourceName, quantity resource.Quantity) int64 {
	switch resourceName {
	case v1.ResourceCPU:
		return quantity.MilliValue()
	default:
		return quantity.Value()
	}
}

// getResourceMetricsName returns the normalized tags name and accuracy of quantity.
func getResourceMetricsName(resourceName v1.ResourceName, quantity resource.Quantity) (string, int64) {
	return resourceName.String(), ResourceQuantityToInt64Value(resourceName, quantity)
}

// MultiplyResourceQuantity scales quantity according to its resource name.
func MultiplyResourceQuantity(resourceName v1.ResourceName, quantity resource.Quantity, y float64) resource.Quantity {
	switch resourceName {
	case v1.ResourceCPU:
		return MultiplyMilliQuantity(quantity, y)
	case v1.ResourceMemory:
		fallthrough
	default:
		return MultiplyQuantity(quantity, y)
	}
}

// MultiplyMilliQuantity scales quantity by y.
func MultiplyMilliQuantity(quantity resource.Quantity, y float64) resource.Quantity {
	if 0 == y {
		return *resource.NewMilliQuantity(0, quantity.Format)
	}

	milliValue := quantity.MilliValue()
	if 0 == milliValue {
		return quantity
	}

	milliValue = int64(float64(milliValue) * y)
	return *resource.NewMilliQuantity(milliValue, quantity.Format)
}

// MultiplyQuantity scales quantity by y.
func MultiplyQuantity(quantity resource.Quantity, y float64) resource.Quantity {
	if 0 == y {
		return *resource.NewQuantity(0, quantity.Format)
	}

	value := quantity.Value()
	if 0 == value {
		return quantity
	}

	value = int64(float64(value) * y)
	return *resource.NewQuantity(value, quantity.Format)
}

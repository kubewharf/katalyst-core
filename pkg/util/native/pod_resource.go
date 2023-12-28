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
	v1 "k8s.io/api/core/v1"
	"k8s.io/klog/v2"
)

// PodResource key: namespace/name, value: pod requested ResourceList
type PodResource map[string]v1.ResourceList

func (pr *PodResource) AddPod(pod *v1.Pod) bool {
	key := pod.Namespace + "/" + pod.Name

	_, ok := (*pr)[key]
	if ok {
		klog.Warningf("add existing pod: %v", key)
	}

	requests := CalculateResource(pod)
	(*pr)[key] = requests
	return ok
}

func (pr *PodResource) DeletePod(pod *v1.Pod) bool {
	key := pod.Namespace + "/" + pod.Name
	_, ok := (*pr)[key]
	if !ok {
		klog.Warningf("delete missing pod: %v", key)
	}
	delete(*pr, key)
	return ok
}

// CalculateResource resourceRequest = max(sum(podSpec.Containers), podSpec.InitContainers)
func CalculateResource(pod *v1.Pod) v1.ResourceList {
	resources := make(v1.ResourceList)

	for _, c := range pod.Spec.Containers {
		for resourceName, quantity := range c.Resources.Requests {
			if q, ok := resources[resourceName]; ok {
				quantity.Add(q)
			}
			resources[resourceName] = quantity
		}
	}

	for _, c := range pod.Spec.InitContainers {
		for resourceName, quantity := range c.Resources.Requests {
			if q, ok := resources[resourceName]; ok && quantity.Cmp(q) <= 0 {
				continue
			}
			resources[resourceName] = quantity
		}
	}
	return resources
}

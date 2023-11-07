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

package cache

import (
	"fmt"

	"github.com/kubewharf/katalyst-api/pkg/apis/node/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/util/native"
)

type ResourceTopology struct {
	TopologyZone []*v1alpha1.TopologyZone

	TopologyPolicy v1alpha1.TopologyPolicy
}

type podFilter func(consumer string) bool

func (rt *ResourceTopology) Update(nrt *v1alpha1.CustomNodeResource) {
	cp := nrt.DeepCopy()

	rt.TopologyZone = cp.Status.TopologyZone

	rt.TopologyPolicy = cp.Status.TopologyPolicy
}

// WithPodReousrce add assumedPodResource to ResourceTopology,
// performing pessimistic overallocation across all the NUMA zones.
func (rt *ResourceTopology) WithPodReousrce(podResource native.PodResource, filter podFilter) *ResourceTopology {
	cp := rt.DeepCopy()

	for i := range cp.TopologyZone {
		if cp.TopologyZone[i].Type != v1alpha1.TopologyTypeSocket {
			continue
		}
		for j, child := range cp.TopologyZone[i].Children {
			if child.Type != v1alpha1.TopologyTypeNuma {
				continue
			}
			allocation := make([]*v1alpha1.Allocation, 0)

			if filter != nil {
				for _, alloc := range child.Allocations {
					if filter(alloc.Consumer) {
						allocation = append(allocation, alloc.DeepCopy())
					}
				}
			} else {
				allocation = append(allocation, child.Allocations...)
			}

			for key, podReq := range podResource {
				copyReq := podReq.DeepCopy()
				fakeAllocation := v1alpha1.Allocation{
					Consumer: fmt.Sprintf("fake-consumer/%s/uid", key),
					Requests: &copyReq,
				}
				allocation = append(allocation, &fakeAllocation)
			}

			cp.TopologyZone[i].Children[j].Allocations = allocation
		}
	}

	return cp
}

func (rt *ResourceTopology) DeepCopy() *ResourceTopology {
	out := new(ResourceTopology)
	if rt.TopologyZone != nil {
		out.TopologyZone = make([]*v1alpha1.TopologyZone, len(rt.TopologyZone))
		for i := range rt.TopologyZone {
			if rt.TopologyZone[i] != nil {
				out.TopologyZone[i] = rt.TopologyZone[i].DeepCopy()
			}
		}
	}
	out.TopologyPolicy = rt.TopologyPolicy
	return out
}

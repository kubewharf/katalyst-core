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

package qosawarenoderesources

import (
	v1 "k8s.io/api/core/v1"
	"k8s.io/klog/v2"
	kubeschedulerconfig "k8s.io/kubernetes/pkg/scheduler/apis/config"
	"k8s.io/kubernetes/pkg/scheduler/framework"

	"github.com/kubewharf/katalyst-core/pkg/util/native"

	"github.com/kubewharf/katalyst-api/pkg/apis/scheduling/config"
	"github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/scheduler/cache"
)

// resourceToWeightMap contains resource name and weight.
type resourceToWeightMap map[v1.ResourceName]int64

// scorer is decorator for resourceAllocationScorer
type scorer func(args *config.QoSAwareNodeResourcesFitArgs) *resourceAllocationScorer

// resourceAllocationScorer contains information to calculate resource allocation score.
type resourceAllocationScorer struct {
	Name string
	// used to decide whether to use Requested or NonZeroRequested for
	// cpu and memory.
	useRequested        bool
	scorer              func(requested, allocable resourceToValueMap) int64
	resourceToWeightMap resourceToWeightMap
}

// resourceToValueMap is keyed with resource name and valued with quantity.
type resourceToValueMap map[v1.ResourceName]int64

// score will use `scorer` function to calculate the score.
func (r *resourceAllocationScorer) score(
	pod *v1.Pod, extendedNodeInfo *cache.NodeInfo, nodeName string,
) (int64, *framework.Status) {
	if r.resourceToWeightMap == nil {
		return 0, framework.NewStatus(framework.Error, "resources not found")
	}

	requested := make(resourceToValueMap)
	allocatable := make(resourceToValueMap)
	for resource := range r.resourceToWeightMap {
		alloc, req := r.calculateQoSResourceAllocatableRequest(extendedNodeInfo, pod, resource)
		if alloc != 0 {
			// Only fill the extended resource entry when it's non-zero.
			allocatable[resource], requested[resource] = alloc, req
		}
	}

	score := r.scorer(requested, allocatable)

	klog.V(10).InfoS("Listing internal info for allocatable resources, requested resources and score", "pod",
		klog.KObj(pod), "nodeName", nodeName, "resourceAllocationScorer", r.Name,
		"allocatableResource", allocatable, "requestedResource", requested, "resourceScore", score,
	)

	return score, nil
}

// calculateQoSResourceAllocatableRequest returns 2 parameters:
// - 1st param: quantity of allocatable resource on the node.
// - 2nd param: aggregated quantity of requested resource on the node.
func (r *resourceAllocationScorer) calculateQoSResourceAllocatableRequest(extendedNodeInfo *cache.NodeInfo, pod *v1.Pod, resource v1.ResourceName) (int64, int64) {
	extendedNodeInfo.Mutex.RLock()
	defer extendedNodeInfo.Mutex.RUnlock()

	requested := extendedNodeInfo.QoSResourcesNonZeroRequested
	if r.useRequested {
		requested = extendedNodeInfo.QoSResourcesRequested
	}

	podQoSRequest := r.calculatePodQoSResourceRequest(pod, resource)
	switch resource {
	case consts.ReclaimedResourceMilliCPU:
		return extendedNodeInfo.QoSResourcesAllocatable.ReclaimedMilliCPU, requested.ReclaimedMilliCPU + podQoSRequest
	case consts.ReclaimedResourceMemory:
		return extendedNodeInfo.QoSResourcesAllocatable.ReclaimedMemory, requested.ReclaimedMemory + podQoSRequest
	}

	klog.V(10).InfoS("Requested resource is omitted for node score calculation", "resourceName", resource)
	return 0, 0
}

// calculatePodQoSResourceRequest returns the total non-zero requests. If Overhead is defined for the pod and the
// PodOverhead feature is enabled, the Overhead is added to the result.
// podResourceRequest = max(sum(podSpec.Containers), podSpec.InitContainers) + overHead
func (r *resourceAllocationScorer) calculatePodQoSResourceRequest(pod *v1.Pod, resource v1.ResourceName) int64 {
	var podRequest int64
	for i := range pod.Spec.Containers {
		container := &pod.Spec.Containers[i]
		value := native.GetRequestForQoSResource(resource, &container.Resources.Requests, !r.useRequested)
		podRequest += value
	}

	for i := range pod.Spec.InitContainers {
		initContainer := &pod.Spec.InitContainers[i]
		value := native.GetRequestForQoSResource(resource, &initContainer.Resources.Requests, !r.useRequested)
		if podRequest < value {
			podRequest = value
		}
	}

	// If Overhead is being utilized, add to the total requests for the pod
	if pod.Spec.Overhead != nil {
		if quantity, found := pod.Spec.Overhead[resource]; found {
			podRequest += quantity.Value()
		}
	}

	return podRequest
}

// resourcesToWeightMap make weightmap from resources spec
func resourcesToWeightMap(resources []kubeschedulerconfig.ResourceSpec) resourceToWeightMap {
	resourceToWeightMap := make(resourceToWeightMap)
	for _, resource := range resources {
		resourceToWeightMap[v1.ResourceName(resource.Name)] = resource.Weight
	}
	return resourceToWeightMap
}

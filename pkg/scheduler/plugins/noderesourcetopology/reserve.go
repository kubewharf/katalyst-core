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

package noderesourcetopology

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/klog/v2"
	"k8s.io/kubernetes/pkg/scheduler/framework"

	"github.com/kubewharf/katalyst-api/pkg/apis/node/v1alpha1"
	"github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/scheduler/cache"
	"github.com/kubewharf/katalyst-core/pkg/scheduler/util"
	"github.com/kubewharf/katalyst-core/pkg/util/native"
)

func (tm *TopologyMatch) Reserve(ctx context.Context, state *framework.CycleState, pod *corev1.Pod, nodeName string) *framework.Status {
	if tm.topologyMatchSupport(pod) {
		podCopy := pod.DeepCopy()

		if tm.resourcePolicy == consts.ResourcePluginPolicyNameDynamic && util.IsSharedPod(pod) && !native.CheckDaemonPod(pod) {
			// add shared request to cache for shared pods
			// pod.spec.nodeName is not set when reserve
			podCopy.Spec.NodeName = nodeName
			err := cache.GetCache().AddPod(podCopy)
			if err != nil {
				klog.Errorf("topologyMatch reserve fail, pod: %v, err: %v", pod.Name, err)
				return framework.AsStatus(err)
			}
			return framework.NewStatus(framework.Success, "")
		}
		if util.IsExclusive(pod) {
			// get node NUMA capacity
			rt := cache.GetCache().GetNodeResourceTopology(nodeName, nil)
			var numaCapacity *corev1.ResourceList
			for _, topologyZone := range rt.TopologyZone {
				if topologyZone.Type != v1alpha1.TopologyTypeSocket {
					continue
				}
				for _, child := range topologyZone.Children {
					if child.Type != v1alpha1.TopologyTypeNuma {
						continue
					}
					numaCapacity = child.Resources.Capacity
					break
				}
				if numaCapacity != nil {
					break
				}
			}
			if numaCapacity == nil {
				klog.Warningf("[TopologyMatch] reserve get node %v NUMA capacity fail")
			} else {
				adjustExclusivePodRequest(podCopy, *numaCapacity, tm.alignedResources)
			}
		}
		cache.GetCache().ReserveNodeResource(nodeName, podCopy)
	}
	return framework.NewStatus(framework.Success, "")
}

func (tm *TopologyMatch) Unreserve(ctx context.Context, state *framework.CycleState, pod *corev1.Pod, nodeName string) {
	if tm.topologyMatchSupport(pod) {
		if tm.resourcePolicy == consts.ResourcePluginPolicyNameDynamic && util.IsSharedPod(pod) {
			podCopy := pod.DeepCopy()
			podCopy.Spec.NodeName = nodeName
			err := cache.GetCache().RemovePod(podCopy)
			if err != nil {
				klog.Errorf("topologyMatch Unreserve fail, pod: %v, err: %v", pod.Name, err)
				return
			}
		}

		cache.GetCache().UnreserveNodeResource(nodeName, pod)
	}
}

func adjustExclusivePodRequest(pod *corev1.Pod, capacity corev1.ResourceList, alignedResource sets.String) {
	NUMAFit := false

	podRequests := util.GetPodEffectiveRequest(pod)

	for resourceName, request := range podRequests {
		if !alignedResource.Has(resourceName.String()) {
			continue
		}

		quantity, ok := capacity[resourceName]
		if !ok {
			continue
		}

		if request.Cmp(quantity) < 0 {
			NUMAFit = true
			podRequests[resourceName] = quantity
		}
	}

	if NUMAFit {
		pod.Spec.InitContainers = []corev1.Container{}
		pod.Spec.Containers = []corev1.Container{
			{
				Name: "fake-container",
				Resources: corev1.ResourceRequirements{
					Limits:   podRequests,
					Requests: podRequests,
				},
			},
		}
	}
}

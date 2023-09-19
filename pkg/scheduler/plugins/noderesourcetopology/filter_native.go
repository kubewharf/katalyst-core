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
	v1 "k8s.io/api/core/v1"
	"k8s.io/klog/v2"
	"k8s.io/kubernetes/pkg/scheduler/framework"

	"github.com/kubewharf/katalyst-api/pkg/apis/node/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/scheduler/util"
)

func (tm *TopologyMatch) nativeSingleNUMAContainerLevelHandler(pod *v1.Pod, zones []*v1alpha1.TopologyZone, nodeInfo *framework.NodeInfo) *framework.Status {
	klog.V(6).Infof("native Single NUMA node container handler for pod %s/%s, node %s", pod.Namespace, pod.Name, nodeInfo.Node().Name)

	return singleNUMAContainerLevelHandler(pod, zones, nodeInfo, tm.resourcePolicy, nativeAlignedResources)
}

func (tm *TopologyMatch) nativeSingleNUMAPodLevelHandler(pod *v1.Pod, zones []*v1alpha1.TopologyZone, nodeInfo *framework.NodeInfo) *framework.Status {
	klog.V(5).Info("native Single NUMA node pod handler for pod %s/%s", pod.Namespace, pod.Name)

	resources := util.GetPodEffectiveRequest(pod)

	NUMANodes := TopologyZonesToNUMANodeList(zones)

	if _, match := resourcesAvailableInAnyNUMANodes(NUMANodes, resources, nativeAlignedResources, nodeInfo); !match {
		klog.V(2).InfoS("cannot align pod", "name", pod.Name)
		return framework.NewStatus(framework.Unschedulable, "cannot align pod")
	}
	return nil
}

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
	"fmt"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/klog/v2"
	v1alpha12 "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"
	v1helper "k8s.io/kubernetes/pkg/apis/core/v1/helper"
	"k8s.io/kubernetes/pkg/apis/core/v1/helper/qos"
	"k8s.io/kubernetes/pkg/kubelet/cm/topologymanager"
	bm "k8s.io/kubernetes/pkg/kubelet/cm/topologymanager/bitmask"
	"k8s.io/kubernetes/pkg/scheduler/framework"

	"github.com/kubewharf/katalyst-api/pkg/apis/node/v1alpha1"
	"github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/scheduler/cache"
	"github.com/kubewharf/katalyst-core/pkg/scheduler/util"
)

// The maximum number of NUMA nodes that Topology Manager allows is 8
// https://kubernetes.io/docs/tasks/administer-cluster/topology-manager/#known-limitations
const (
	maxAllowableNUMANodes = 8
	maxNUMAId             = 64
)

func (tm *TopologyMatch) Filter(ctx context.Context, cycleState *framework.CycleState, pod *v1.Pod, nodeInfo *framework.NodeInfo) *framework.Status {
	if nodeInfo.Node() == nil {
		return framework.NewStatus(framework.Error, "node not found")
	}

	if !tm.topologyMatchSupport(pod) {
		return nil
	}

	var (
		nodeName                  = nodeInfo.Node().Name
		nodeResourceTopologycache *cache.ResourceTopology
	)
	if consts.ResourcePluginPolicyNameDynamic == tm.resourcePolicy {
		// only dedicated pods will participate in the calculation
		nodeResourceTopologycache = cache.GetCache().GetNodeResourceTopology(nodeName, tm.dedicatedPodsFilter(nodeInfo))
	} else {
		nodeResourceTopologycache = cache.GetCache().GetNodeResourceTopology(nodeName, nil)
	}
	if nodeResourceTopologycache == nil {
		return nil
	}

	handler := tm.filterHanlder(pod, nodeResourceTopologycache)
	if handler == nil {
		return nil
	}

	return handler(pod, nodeResourceTopologycache.TopologyZone, nodeInfo)
}

func (tm *TopologyMatch) filterHanlder(pod *v1.Pod, nodeResourceTopology *cache.ResourceTopology) filterFn {
	if tm.resourcePolicy == consts.ResourcePluginPolicyNameNative {
		// only single-numa-node supported
		if qos.GetPodQOS(pod) == v1.PodQOSGuaranteed && util.IsRequestFullCPU(pod) {
			switch nodeResourceTopology.TopologyPolicy {
			case v1alpha1.TopologyPolicySingleNUMANodeContainerLevel:
				return tm.nativeSingleNUMAContainerLevelHandler
			case v1alpha1.TopologyPolicySingleNUMANodePodLevel:
				return tm.nativeSingleNUMAPodLevelHandler
			default:
				return nil
			}
		}
	}

	if tm.resourcePolicy == consts.ResourcePluginPolicyNameDynamic {
		// shared_cores
		// check nonDedicated NUMA resource satisfied for shared requests.
		if util.IsSharedPod(pod) {
			return func(pod *v1.Pod, zones []*v1alpha1.TopologyZone, info *framework.NodeInfo) *framework.Status {
				return tm.sharedCoresHandler(pod, zones, info)
			}
		}

		// dedicated_cores + numa_binding
		// single-numa-node and numeric container level supported
		if util.IsDedicatedPod(pod) && util.IsNumaBinding(pod) && !util.IsExclusive(pod) {
			switch nodeResourceTopology.TopologyPolicy {
			case v1alpha1.TopologyPolicySingleNUMANodeContainerLevel:
				return func(pod *v1.Pod, zones []*v1alpha1.TopologyZone, info *framework.NodeInfo) *framework.Status {
					return tm.dedicatedCoresWithNUMABindingSingleNUMANodeContainerLevelHandler(pod, zones, info)
				}
			case v1alpha1.TopologyPolicyNumericContainerLevel:
				return func(pod *v1.Pod, zones []*v1alpha1.TopologyZone, info *framework.NodeInfo) *framework.Status {
					return tm.dedicatedCoresWithNUMABindingNumericContainerLevelHandler(pod, zones, info)
				}
			default:
				return nil
			}
		}

		// dedicated_cores + numa_binding + numa_exclusive
		// single-numa-node and numeric supported
		if util.IsDedicatedPod(pod) && util.IsNumaBinding(pod) && util.IsExclusive(pod) {
			switch nodeResourceTopology.TopologyPolicy {
			case v1alpha1.TopologyPolicySingleNUMANodeContainerLevel:
				return func(pod *v1.Pod, zones []*v1alpha1.TopologyZone, info *framework.NodeInfo) *framework.Status {
					return tm.dedicatedCoresWithNUMAExclusiveSingleNUMANodeContainerLevelHandler(pod, zones, info)
				}
			case v1alpha1.TopologyPolicyNumericContainerLevel:
				return func(pod *v1.Pod, zones []*v1alpha1.TopologyZone, info *framework.NodeInfo) *framework.Status {
					return tm.dedicatedCoresWithNUMAExclusiveNumericContainerLevelHandler(pod, zones, info)
				}
			default:
				return nil
			}
		}
	}

	return nil
}

func singleNUMAContainerLevelHandler(pod *v1.Pod, zones []*v1alpha1.TopologyZone, nodeInfo *framework.NodeInfo, policy consts.ResourcePluginPolicyName, alignedResource sets.String) *framework.Status {
	NUMANodes := TopologyZonesToNUMANodeList(zones)

	// init containers are skipped both in native and dynamic policy
	// sidecar containers are skipped in dynamic policy
	for _, container := range pod.Spec.Containers {
		containerType, _, err := util.GetContainerTypeAndIndex(pod, &container)
		if err != nil {
			klog.Error(err)
			return framework.NewStatus(framework.Unschedulable, "cannot get container type")
		}

		if policy == consts.ResourcePluginPolicyNameDynamic && containerType == v1alpha12.ContainerType_SIDECAR {
			continue
		}
		numaID, match := resourcesAvailableInAnyNUMANodes(NUMANodes, container.Resources.Requests, alignedResource, nodeInfo)
		if !match {
			// we can't align container, so definitely we can't align a pod
			klog.V(2).InfoS("cannot align container", "name", container.Name, "kind", "app")
			return framework.NewStatus(framework.Unschedulable, "cannot align container")
		}

		// subtract the resources requested by the container from the given NUMA.
		// so other containers in the pod can be fits correctly.
		subtractFromNUMA(NUMANodes, numaID, container)
	}

	if policy == consts.ResourcePluginPolicyNameDynamic {
		// check if nonDedicated NUMA resource satisfied for shared pods.
		err := resourcesAvailableForSharedPods(NUMANodes, nil, nodeInfo)
		if err != nil {
			return framework.NewStatus(framework.Unschedulable, err.Error())
		}
	}

	return nil
}

func resourcesAvailableForSharedPods(numaNodes NUMANodeList, sharedPod *v1.Pod, nodeInfo *framework.NodeInfo) error {
	// get allocated shared requests including reserved shared pods.
	nodeCache, err := cache.GetCache().GetNodeInfo(nodeInfo.Node().Name)
	if err != nil {
		klog.Errorf("getNodeInfo %v from cache fail: %v", nodeInfo.Node().Name, err)
		return err
	}
	sharedResourcesRequested := nodeCache.GetSharedResourcesRequested()
	if sharedPod != nil {
		// if sharedPod is not nil, add pod requests to allocated requests.
		podRequest := util.GetPodEffectiveRequest(sharedPod)
		sharedResourcesRequested.Add(podRequest)
	}

	// get available resource for shared pods from dedicated NUMA allocated data.
	availableResources := getAvailableResourceForSharedPods(numaNodes)
	klog.V(6).Infof("node %v sharedResourcesRequested cpu: %v, sharedResourceRequested memory: %v, availableResources cpu: %v, availableResources memory: %v",
		nodeInfo.Node().Name, sharedResourcesRequested.MilliCPU, sharedResourcesRequested.Memory, availableResources.MilliCPU, availableResources.Memory)

	// check resource satisfied, only CPU and memory now.
	if availableResources.MilliCPU < sharedResourcesRequested.MilliCPU {
		err = fmt.Errorf("node %v shared milliCPU insufficient, availableResources milliCPU: %v, sharedResourcesRequested milliCPU: %v",
			nodeInfo.Node().Name, availableResources.MilliCPU, sharedResourcesRequested.MilliCPU)
		klog.V(5).Infof(err.Error())
		return err
	}
	if availableResources.Memory < sharedResourcesRequested.Memory {
		err = fmt.Errorf("node %v shared memory insufficient, availableResources memory: %v, sharedResourcesRequested memory: %v",
			nodeInfo.Node().Name, availableResources.Memory, sharedResourcesRequested.Memory)
		klog.V(5).Infof(err.Error())
		return err
	}

	return nil
}

func getAvailableResourceForSharedPods(numaNodes NUMANodeList) *framework.Resource {
	resource := &framework.Resource{}

	for _, numaNode := range numaNodes {
		if numaNode.Allocated {
			continue
		}

		resource.Add(numaNode.Allocatable)
	}

	return resource
}

func resourcesAvailableInAnyNUMANodes(numaNodes NUMANodeList, resources v1.ResourceList, alignedResource sets.String, nodeInfo *framework.NodeInfo) (int, bool) {
	numaID := maxAllowableNUMANodes
	bitmask := bm.NewEmptyBitMask()
	bitmask.Fill()

	nodeName := nodeInfo.Node().Name

	for resourceName, quantity := range resources {
		if quantity.IsZero() {
			klog.V(4).InfoS("ignoring zero-qty resource request", "node", nodeInfo.Node().Name, "resource", resourceName)
			continue
		}

		hasNUMAAffinity := false
		resourceBitmask := bm.NewEmptyBitMask()
		for _, numaNode := range numaNodes {
			// all allocatable resource will be fill into available resource
			_, ok := numaNode.Available[resourceName]
			if !ok {
				continue
			}

			hasNUMAAffinity = true
			if !resourceSufficient(resourceName, quantity, numaNode, alignedResource.Has(resourceName.String())) {
				continue
			}

			resourceBitmask.Add(numaNode.NUMAID)
			klog.V(6).InfoS("feasible", "node", nodeName, "NUMA", numaNode.NUMAID, "resource", resourceName)
		}

		if !hasNUMAAffinity && !v1helper.IsNativeResource(resourceName) {
			klog.V(6).InfoS("resource available at node level (no NUMA affinity)", "node", nodeName, "resource", resourceName)
			continue
		}

		// resourceBitmask records which single numa is available for resource,
		// And with each resourceBitmask we will get single numa that is available for all resources.
		bitmask.And(resourceBitmask)
		if bitmask.IsEmpty() {
			// means all numa is unavailable
			klog.V(5).InfoS("early verdict", "node", nodeName, "resource", resourceName, "suitable", "false")
			return numaID, false
		}
	}

	// according to TopologyManager, the preferred NUMA affinity, is the narrowest one.
	// https://github.com/kubernetes/kubernetes/blob/v1.24.0-rc.1/pkg/kubelet/cm/topologymanager/policy.go#L155
	// in single-numa-node policy all resources should be allocated from a single NUMA,
	// which means that the lowest NUMA ID (with available resources) is the one to be selected by Kubelet.
	numaID = bitmask.GetBits()[0]

	ret := !bitmask.IsEmpty()
	klog.V(5).InfoS("final verdict", "node", nodeName, "suitable", ret)
	return numaID, ret
}

func resourceSufficient(resourceName v1.ResourceName, quantity resource.Quantity, numaNode NUMANode, exclusive bool) bool {
	availableQuantity := numaNode.Available[resourceName]
	allocatableQuantity := numaNode.Allocatable[resourceName]

	// numa is available only when numa is idle for exclusive resource
	if exclusive && !availableQuantity.Equal(allocatableQuantity) {
		return false
	}

	return availableQuantity.Cmp(quantity) >= 0
}

func subtractFromNUMA(nodes NUMANodeList, numaID int, container v1.Container) {
	for i := 0; i < len(nodes); i++ {
		if nodes[i].NUMAID != numaID {
			continue
		}

		nRes := nodes[i].Available
		for resName, quan := range container.Resources.Requests {
			nodeResQuan, ok := nRes[resName]
			if !ok {
				continue
			}
			nodeResQuan.Sub(quan)
			// we do not expect a negative value here, since this function only called
			// when resourcesAvailableInAnyNUMANodes function is passed
			// but let's log here if such unlikely case will occur
			if nodeResQuan.Sign() == -1 {
				klog.V(4).InfoS("resource quantity should not be a negative value", "resource", resName, "quantity", nodeResQuan.String())
			}
			nRes[resName] = nodeResQuan
		}
		nodes[i].Allocated = true
	}
}

func subtractFromNUMAs(
	nodeMap map[int]NUMANode,
	resourceTopologyHints map[string]topologymanager.TopologyHint,
	container v1.Container,
	alignedResource sets.String,
	numaBinding, exclusive bool,
) error {
	for resourceName, hint := range resourceTopologyHints {
		numaIDs := hint.NUMANodeAffinity.GetBits()
		// for numaBinding unexclusive resource, only one numaNode allowed.
		if alignedResource.Has(resourceName) && numaBinding && !exclusive {
			if len(numaIDs) > 1 {
				return fmt.Errorf("numaBinding but not exclusive resource %v only support one numaNode", resourceName)
			}
		}

		quantity, ok := container.Resources.Requests[v1.ResourceName(resourceName)]
		if !ok {
			return fmt.Errorf("resource %v not exist in container %v requests", resourceName, container.Name)
		}
		quantityCopy := quantity.DeepCopy()
		for _, numaID := range numaIDs {
			// we don't care the actual resource allocated on each NUMA,
			// at least one resource will be allocated on every NUMAs in resourceTopologyHints,
			// so these NUMANode can not be allocated for next exclusive container in the same pod.
			numaNode, ok := nodeMap[numaID]
			if !ok {
				return fmt.Errorf("numaID %v not exist in numaNodeMap", numaID)
			}
			resourceQuantity := numaNode.Available[v1.ResourceName(resourceName)]
			// for exclusive resource, subtract all resource from numaNode
			if alignedResource.Has(resourceName) && exclusive {
				resourceQuantity = *resource.NewQuantity(0, resourceQuantity.Format)
			} else {
				if quantityCopy.IsZero() {
					break
				}
				if resourceQuantity.Cmp(quantityCopy) > 0 {
					resourceQuantity.Sub(quantityCopy)
					quantityCopy = *resource.NewQuantity(0, quantityCopy.Format)
				} else {
					quantityCopy.Sub(resourceQuantity)
					resourceQuantity = *resource.NewQuantity(0, resourceQuantity.Format)
				}
			}
			numaNode.Available[v1.ResourceName(resourceName)] = resourceQuantity
			numaNode.Allocated = true
			nodeMap[numaID] = numaNode
		}
	}

	return nil
}

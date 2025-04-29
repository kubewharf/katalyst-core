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
	"sync"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"
	"k8s.io/kubernetes/pkg/scheduler/framework"

	apis "github.com/kubewharf/katalyst-api/pkg/apis/node/v1alpha1"
	"github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/scheduler/util"
	"github.com/kubewharf/katalyst-core/pkg/util/native"
)

// PodInfo is pod level aggregated information.
type PodInfo struct {
	QoSResourcesRequested        *native.QoSResource
	QoSResourcesNonZeroRequested *native.QoSResource

	ResourcesRequested        *framework.Resource
	ResourcesNonZeroRequested *framework.Resource
}

// NodeInfo is node level aggregated information.
type NodeInfo struct {
	// Mutex guards all fields within this NodeInfo struct.
	Mutex sync.RWMutex

	// Total requested qos resources of this node. This includes assumed
	// pods, which scheduler has sent for binding, but may not be scheduled yet.
	QoSResourcesRequested *native.QoSResource
	// Total requested qos resources of this node with a minimum value
	// applied to each container's CPU and memory requests. This does not reflect
	// the actual resource requests for this node, but is used to avoid scheduling
	// many zero-request pods onto one node.
	QoSResourcesNonZeroRequested *native.QoSResource
	// We store qos allocatedResources (which is CNR.Status.BestEffortResourceAllocatable.*) explicitly
	// as int64, to avoid conversions and accessing map.
	QoSResourcesAllocatable *native.QoSResource

	// Total requested shared resources of this node, includes assumed pods.
	// available resource on nonDedicated numa should be checked when scheduling
	// shared pods and dedicated pods.
	SharedResourcesRequested        *framework.Resource
	SharedResourcesNonZeroRequested *framework.Resource

	// record PodInfo here since we may have the functionality to
	// change pod resources.
	// reclaimed and shared pods are recorded.
	Pods map[string]*PodInfo

	// node TopologyPolicy and TopologyZones from CNR status.
	// is total CNR data necessary in extendedCache ?
	ResourceTopology *ResourceTopology

	// record assumed pod resource util pod is watched in CNR updated events.
	AssumedPodResources native.PodResource
}

// NewNodeInfo returns a ready to use empty NodeInfo object.
// If any pods are given in arguments, their information will be aggregated in
// the returned object.
func NewNodeInfo() *NodeInfo {
	ni := &NodeInfo{
		QoSResourcesRequested:           &native.QoSResource{},
		QoSResourcesNonZeroRequested:    &native.QoSResource{},
		QoSResourcesAllocatable:         &native.QoSResource{},
		SharedResourcesRequested:        &framework.Resource{},
		SharedResourcesNonZeroRequested: &framework.Resource{},
		Pods:                            make(map[string]*PodInfo),
		ResourceTopology:                new(ResourceTopology),
		AssumedPodResources:             native.PodResource{},
	}
	return ni
}

// UpdateNodeInfo updates the NodeInfo.
func (n *NodeInfo) UpdateNodeInfo(cnr *apis.CustomNodeResource) {
	n.Mutex.Lock()
	defer n.Mutex.Unlock()

	n.updateReclaimed(cnr)

	n.updateTopology(cnr)
}

func (n *NodeInfo) updateReclaimed(cnr *apis.CustomNodeResource) {
	if cnr.Status.Resources.Allocatable != nil {
		beResourceList := *cnr.Status.Resources.Allocatable
		if reclaimedMilliCPU, ok := beResourceList[consts.ReclaimedResourceMilliCPU]; ok {
			n.QoSResourcesAllocatable.ReclaimedMilliCPU = reclaimedMilliCPU.Value()
		} else {
			n.QoSResourcesAllocatable.ReclaimedMilliCPU = 0
		}

		if reclaimedMemory, ok := beResourceList[consts.ReclaimedResourceMemory]; ok {
			n.QoSResourcesAllocatable.ReclaimedMemory = reclaimedMemory.Value()
		} else {
			n.QoSResourcesAllocatable.ReclaimedMemory = 0
		}
	}
}

func (n *NodeInfo) updateTopology(cnr *apis.CustomNodeResource) {
	for _, topologyZone := range cnr.Status.TopologyZone {
		if topologyZone.Type != apis.TopologyTypeSocket {
			continue
		}
		for _, child := range topologyZone.Children {
			if child.Type != apis.TopologyTypeNuma {
				continue
			}

			for _, alloc := range child.Allocations {
				namespace, name, _, err := native.ParseNamespaceNameUIDKey(alloc.Consumer)
				if err != nil {
					klog.Errorf("unexpected CNR numa consumer: %v", err)
					continue
				}
				// delete all pod from AssumedPodResource
				n.AssumedPodResources.DeletePod(&v1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      name,
						Namespace: namespace,
					},
				})
			}
		}
	}

	n.ResourceTopology.Update(cnr)
}

// AddPod adds pod information to this NodeInfo.
func (n *NodeInfo) AddPod(key string, pod *v1.Pod) {
	qosLevel, err := util.GetQosLevelForPod(pod)
	if err != nil {
		klog.Errorf("AddPod %v fail: %v", key, err)
		return
	}

	switch qosLevel {
	case consts.PodAnnotationQoSLevelReclaimedCores:
		n.addReclaimedPod(key, pod)
	case consts.PodAnnotationQoSLevelSharedCores:
		n.addSharedPod(key, pod)
	default:
		// only add reclaimed and shared pods when they are watched.
		// dedicated caches will be updated when CNR updated.
		return
	}
}

// RemovePod subtracts pod information from this NodeInfo.
func (n *NodeInfo) RemovePod(key string, _ *v1.Pod) {
	n.Mutex.Lock()
	defer n.Mutex.Unlock()

	podInfo, ok := n.Pods[key]
	if !ok {
		return
	}
	n.QoSResourcesRequested.ReclaimedMilliCPU -= podInfo.QoSResourcesRequested.ReclaimedMilliCPU
	n.QoSResourcesRequested.ReclaimedMemory -= podInfo.QoSResourcesRequested.ReclaimedMemory

	n.QoSResourcesNonZeroRequested.ReclaimedMilliCPU -= podInfo.QoSResourcesNonZeroRequested.ReclaimedMilliCPU
	n.QoSResourcesNonZeroRequested.ReclaimedMemory -= podInfo.QoSResourcesNonZeroRequested.ReclaimedMemory

	n.SharedResourcesRequested.MilliCPU -= podInfo.ResourcesRequested.MilliCPU
	n.SharedResourcesRequested.Memory -= podInfo.ResourcesRequested.Memory

	n.SharedResourcesNonZeroRequested.MilliCPU -= podInfo.ResourcesNonZeroRequested.MilliCPU
	n.SharedResourcesNonZeroRequested.Memory -= podInfo.ResourcesNonZeroRequested.Memory

	delete(n.Pods, key)
}

func (n *NodeInfo) addReclaimedPod(key string, pod *v1.Pod) {
	n.RemovePod(key, pod)

	res, non0CPU, non0Mem := native.CalculateQoSResource(pod)

	n.Mutex.Lock()
	defer n.Mutex.Unlock()

	n.Pods[key] = &PodInfo{
		QoSResourcesRequested: &res,
		QoSResourcesNonZeroRequested: &native.QoSResource{
			ReclaimedMilliCPU: non0CPU,
			ReclaimedMemory:   non0Mem,
		},
		ResourcesRequested:        &framework.Resource{},
		ResourcesNonZeroRequested: &framework.Resource{},
	}

	n.QoSResourcesRequested.ReclaimedMilliCPU += res.ReclaimedMilliCPU
	n.QoSResourcesRequested.ReclaimedMemory += res.ReclaimedMemory

	n.QoSResourcesNonZeroRequested.ReclaimedMilliCPU += non0CPU
	n.QoSResourcesNonZeroRequested.ReclaimedMemory += non0Mem
}

func (n *NodeInfo) addSharedPod(key string, pod *v1.Pod) {
	// skip daemon pod
	if native.CheckDaemonPod(pod) {
		return
	}

	n.RemovePod(key, pod)

	res, non0CPU, non0Mem := util.CalculateEffectiveResource(pod)

	n.Mutex.Lock()
	defer n.Mutex.Unlock()

	n.Pods[key] = &PodInfo{
		QoSResourcesRequested:        &native.QoSResource{},
		QoSResourcesNonZeroRequested: &native.QoSResource{},
		ResourcesRequested:           &res,
		ResourcesNonZeroRequested: &framework.Resource{
			MilliCPU: non0CPU,
			Memory:   non0Mem,
		},
	}

	n.SharedResourcesRequested.MilliCPU += res.MilliCPU
	n.SharedResourcesRequested.Memory += res.Memory

	n.SharedResourcesNonZeroRequested.MilliCPU += non0CPU
	n.SharedResourcesNonZeroRequested.Memory += non0Mem
}

func (n *NodeInfo) AddAssumedPod(pod *v1.Pod) {
	n.Mutex.Lock()
	defer n.Mutex.Unlock()
	n.AssumedPodResources.AddPod(pod)
}

func (n *NodeInfo) DeleteAssumedPod(pod *v1.Pod) {
	n.Mutex.Lock()
	defer n.Mutex.Unlock()

	n.AssumedPodResources.DeletePod(pod)
}

func (n *NodeInfo) GetResourceTopologyCopy(filterFn podFilter) *ResourceTopology {
	n.Mutex.RLock()
	defer n.Mutex.RUnlock()

	if n.ResourceTopology == nil {
		return nil
	}

	return n.ResourceTopology.WithPodReousrce(n.AssumedPodResources, filterFn)
}

func (n *NodeInfo) GetSharedResourcesRequested() *framework.Resource {
	n.Mutex.RLock()
	defer n.Mutex.RUnlock()

	return n.SharedResourcesRequested.Clone()
}

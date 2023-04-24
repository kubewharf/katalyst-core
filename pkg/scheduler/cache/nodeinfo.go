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

	apis "github.com/kubewharf/katalyst-api/pkg/apis/node/v1alpha1"
	"github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/util/native"
)

// PodInfo is pod level aggregated information.
type PodInfo struct {
	QoSResourcesRequested        *native.QoSResource
	QoSResourcesNonZeroRequested *native.QoSResource
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

	// record PodInfo here since we may have the functionality to
	// change pod resources.
	Pods map[string]*PodInfo
}

// NewNodeInfo returns a ready to use empty NodeInfo object.
// If any pods are given in arguments, their information will be aggregated in
// the returned object.
func NewNodeInfo() *NodeInfo {
	ni := &NodeInfo{
		QoSResourcesRequested:        &native.QoSResource{},
		QoSResourcesNonZeroRequested: &native.QoSResource{},
		QoSResourcesAllocatable:      &native.QoSResource{},
		Pods:                         make(map[string]*PodInfo),
	}
	return ni
}

// UpdateNodeInfo updates the NodeInfo.
func (n *NodeInfo) UpdateNodeInfo(cnr *apis.CustomNodeResource) {
	n.Mutex.Lock()
	defer n.Mutex.Unlock()

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

// AddPod adds pod information to this NodeInfo.
func (n *NodeInfo) AddPod(key string, pod *v1.Pod) {
	// always try to clean previous pod, and then insert
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
	}

	n.QoSResourcesRequested.ReclaimedMilliCPU += res.ReclaimedMilliCPU
	n.QoSResourcesRequested.ReclaimedMemory += res.ReclaimedMemory

	n.QoSResourcesNonZeroRequested.ReclaimedMilliCPU += non0CPU
	n.QoSResourcesNonZeroRequested.ReclaimedMemory += non0Mem
}

// RemovePod subtracts pod information from this NodeInfo.
func (n *NodeInfo) RemovePod(key string, pod *v1.Pod) {
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
}

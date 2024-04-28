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

package nodeovercommitment

import (
	"context"
	"fmt"

	overcommitutil "github.com/kubewharf/katalyst-core/pkg/util/overcommit"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/klog/v2"
	"k8s.io/kubernetes/pkg/scheduler/framework"

	"github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/scheduler/plugins/nodeovercommitment/cache"
	"github.com/kubewharf/katalyst-core/pkg/util/native"
)

type preFilterState struct {
	framework.Resource
	GuaranteedCPUs int
}

func (s *preFilterState) Clone() framework.StateData {
	return s
}

func (n *NodeOvercommitment) PreFilter(ctx context.Context, cycleState *framework.CycleState, pod *v1.Pod) (*framework.PreFilterResult, *framework.Status) {
	cycleState.Write(preFilterStateKey, computePodResourceRequest(pod))
	return nil, nil
}

func (n *NodeOvercommitment) PreFilterExtensions() framework.PreFilterExtensions {
	return nil
}

func getPreFilterState(cycleState *framework.CycleState) (*preFilterState, error) {
	c, err := cycleState.Read(preFilterStateKey)
	if err != nil {
		return nil, fmt.Errorf("error reading %q from cycleState: %w", preFilterStateKey, err)
	}

	s, ok := c.(*preFilterState)
	if !ok {
		return nil, fmt.Errorf("%+v convert to NodeOvercommitment.preFilterState error", c)
	}
	return s, nil
}

func (n *NodeOvercommitment) Filter(ctx context.Context, cycleState *framework.CycleState, pod *v1.Pod, nodeInfo *framework.NodeInfo) *framework.Status {
	if nodeInfo.Node() == nil {
		return framework.NewStatus(framework.Error, "node not found")
	}

	s, err := getPreFilterState(cycleState)
	if err != nil {
		klog.Error(err)
		return framework.AsStatus(err)
	}
	if s.GuaranteedCPUs == 0 && s.MilliCPU == 0 {
		return nil
	}

	var nodeName = nodeInfo.Node().GetName()
	nodeCache, err := cache.GetCache().GetNode(nodeName)
	if err != nil {
		err := fmt.Errorf("GetNodeInfo %s from cache fail: %v", nodeName, err)
		klog.Error(err)
		return framework.NewStatus(framework.Error, err.Error())
	}

	CPUManagerAvailable, memoryManagerAvailable := nodeCache.HintProvidersAvailable()
	CPUOvercommitRatio, memoryOvercommitRatio, err := n.nodeOvercommitRatio(nodeInfo)
	if err != nil {
		klog.Error(err)
		return framework.NewStatus(framework.Error, err.Error())
	}

	if memoryManagerAvailable {
		if memoryOvercommitRatio > 1.0 {
			err = fmt.Errorf("node %v memoryManager and memoryOvercommit both available", nodeName)
			klog.Error(err)
			return framework.NewStatus(framework.Unschedulable, err.Error())
		}
	}

	if CPUManagerAvailable && CPUOvercommitRatio > 1.0 {
		nodeCPUOriginAllocatable, err := n.nodeCPUAllocatable(nodeInfo)
		if err != nil {
			klog.Error(err)
			return framework.NewStatus(framework.Error, err.Error())
		}

		guaranteedCPUs := resource.NewQuantity(int64(nodeCache.GetGuaranteedCPUs()), resource.DecimalSI)
		nonGuaranteedRequestCPU := nodeInfo.Requested.MilliCPU - guaranteedCPUs.MilliValue()

		nodeCPUOriginAllocatable.Sub(*guaranteedCPUs)
		*nodeCPUOriginAllocatable = native.MultiplyResourceQuantity(v1.ResourceCPU, *nodeCPUOriginAllocatable, CPUOvercommitRatio)

		klog.V(5).Infof("nodeOvercommitment, pod guranteedCPUs: %v, pod cpus: %v, CPUOvercommitRatio: %v, nodeAllocatable: %v, guaranteedCPUs: %v, nonGuaranteedRequestCPu: %v",
			s.GuaranteedCPUs, s.MilliCPU, CPUOvercommitRatio, nodeCPUOriginAllocatable.MilliValue(), guaranteedCPUs.MilliValue(), nonGuaranteedRequestCPU)

		if s.GuaranteedCPUs > 0 {
			if int64(float64(s.GuaranteedCPUs)*1000.0*CPUOvercommitRatio) > nodeCPUOriginAllocatable.MilliValue()-nonGuaranteedRequestCPU {
				return framework.NewStatus(framework.Unschedulable, "node overcommitment insufficient cpu")
			}
		} else {
			if s.MilliCPU > nodeCPUOriginAllocatable.MilliValue()-nonGuaranteedRequestCPU {
				return framework.NewStatus(framework.Unschedulable, "node overcommitment insufficient cpu")
			}
		}
	}

	return nil
}

func (n *NodeOvercommitment) nodeOvercommitRatio(nodeInfo *framework.NodeInfo) (CPUOvercommitRatio, memoryOvercommitRatio float64, err error) {
	CPUOvercommitRatio, memoryOvercommitRatio = 1.0, 1.0

	if nodeInfo.Node() == nil || nodeInfo.Node().GetAnnotations() == nil {
		return
	}

	var (
		annotation = nodeInfo.Node().GetAnnotations()
	)
	CPUOvercommitRatio, err = overcommitutil.OvercommitRatioValidate(annotation, consts.NodeAnnotationCPUOvercommitRatioKey, consts.NodeAnnotationRealtimeCPUOvercommitRatioKey)
	if err != nil {
		klog.Error(err)
		return
	}

	memoryOvercommitRatio, err = overcommitutil.OvercommitRatioValidate(annotation, consts.NodeAnnotationMemoryOvercommitRatioKey, consts.NodeAnnotationRealtimeMemoryOvercommitRatioKey)
	if err != nil {
		klog.Error(err)
		return
	}

	return
}

func computePodResourceRequest(pod *v1.Pod) *preFilterState {
	result := &preFilterState{}

	CPUs := native.PodGuaranteedCPUs(pod)
	if CPUs > 0 {
		result.GuaranteedCPUs = CPUs
		return result
	}

	for _, container := range pod.Spec.Containers {
		result.Add(container.Resources.Requests)
	}

	// take max_resource(sum_pod, any_init_container)
	for _, container := range pod.Spec.InitContainers {
		result.SetMaxResource(container.Resources.Requests)
	}

	// If Overhead is being utilized, add to the total requests for the pod
	if pod.Spec.Overhead != nil {
		result.Add(pod.Spec.Overhead)
	}
	return result
}

func (n *NodeOvercommitment) nodeCPUAllocatable(nodeInfo *framework.NodeInfo) (*resource.Quantity, error) {
	node := nodeInfo.Node()
	if node == nil {
		return nil, fmt.Errorf("nil nodeInfo")
	}

	if node.GetAnnotations() == nil {
		return node.Status.Allocatable.Cpu(), nil
	}

	originalAllocatableCPU, ok := node.Annotations[consts.NodeAnnotationOriginalAllocatableCPUKey]
	if !ok {
		return node.Status.Allocatable.Cpu(), nil
	}

	quantity, err := resource.ParseQuantity(originalAllocatableCPU)
	return &quantity, err
}

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
	"fmt"
	"strconv"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/sets"
	quotav1 "k8s.io/apiserver/pkg/quota/v1"
	"k8s.io/klog/v2"
	"k8s.io/kubernetes/pkg/apis/core/v1/helper/qos"
	"k8s.io/kubernetes/pkg/scheduler/apis/config"
	"k8s.io/kubernetes/pkg/scheduler/framework"

	"github.com/kubewharf/katalyst-api/pkg/apis/node/v1alpha1"
	apisconfig "github.com/kubewharf/katalyst-api/pkg/apis/scheduling/config"
	"github.com/kubewharf/katalyst-api/pkg/apis/scheduling/config/validation"
	"github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/scheduler/eventhandlers"
	"github.com/kubewharf/katalyst-core/pkg/scheduler/util"
	"github.com/kubewharf/katalyst-core/pkg/util/native"
)

const (
	TopologyMatchName = "NodeResourceTopology"
)

var (
	nativeAlignedResources = sets.NewString()
)

type filterFn func(*v1.Pod, []*v1alpha1.TopologyZone, *framework.NodeInfo) *framework.Status
type scoringFn func(*v1.Pod, []*v1alpha1.TopologyZone) (int64, *framework.Status)

type NUMANode struct {
	SocketID    string
	NUMAID      int
	Capacity    v1.ResourceList
	Allocatable v1.ResourceList
	Available   v1.ResourceList
	Costs       map[int]int
}

type NUMANodeList []NUMANode

type TopologyMatch struct {
	scoreStrategyFunc   scoreStrategyFn
	scoreStrategyType   config.ScoringStrategyType
	resourceToWeightMap resourceToWeightMap
	alignedResources    sets.String
	resourcePolicy      consts.ResourcePluginPolicyName
	sharedLister        framework.SharedLister
}

var _ framework.FilterPlugin = &TopologyMatch{}
var _ framework.ScorePlugin = &TopologyMatch{}
var _ framework.ReservePlugin = &TopologyMatch{}
var _ framework.EnqueueExtensions = &TopologyMatch{}

// Name returns name of the plugin.
func (tm *TopologyMatch) Name() string {
	return TopologyMatchName
}

func New(args runtime.Object, h framework.Handle) (framework.Plugin, error) {
	klog.Info("Creating new TopologyMatch plugin")
	klog.Infof("args: %+v", args)
	tcfg, ok := args.(*apisconfig.NodeResourceTopologyArgs)
	if !ok {
		return nil, fmt.Errorf("want args to be of type NodeResourceTopologyArgs, got %T", args)
	}

	if err := validation.ValidateNodeResourceTopologyMatchArgs(nil, tcfg); err != nil {
		return nil, err
	}

	resourceToWeightMap := make(resourceToWeightMap)
	for _, resource := range tcfg.ScoringStrategy.Resources {
		resourceToWeightMap[v1.ResourceName(resource.Name)] = resource.Weight
	}

	alignedResources := sets.NewString(tcfg.AlignedResources...)

	strategy, err := getScoringStrategyFunction(tcfg.ScoringStrategy.Type)
	if err != nil {
		return nil, err
	}

	eventhandlers.RegisterCommonPodHandler()
	eventhandlers.RegisterCommonCNRHandler()

	return &TopologyMatch{
		scoreStrategyType:   tcfg.ScoringStrategy.Type,
		alignedResources:    alignedResources,
		resourceToWeightMap: resourceToWeightMap,
		scoreStrategyFunc:   strategy,
		resourcePolicy:      tcfg.ResourcePluginPolicy,
		sharedLister:        h.SnapshotSharedLister(),
	}, nil
}

// EventsToRegister returns the possible events that may make a Pod
// failed by this plugin schedulable.
// NOTE: if in-place-update (KEP 1287) gets implemented, then PodUpdate event
// should be registered for this plugin since a Pod update may free up resources
// that make other Pods schedulable.
func (tm *TopologyMatch) EventsToRegister() []framework.ClusterEvent {
	// To register a custom event, follow the naming convention at:
	// https://git.k8s.io/kubernetes/pkg/scheduler/eventhandlers.go#L403-L410
	cnrGVK := fmt.Sprintf("customnoderesources.v1alpha1.%v", v1alpha1.GroupName)
	return []framework.ClusterEvent{
		{Resource: framework.Pod, ActionType: framework.Delete},
		{Resource: framework.Node, ActionType: framework.Add | framework.UpdateNodeAllocatable},
		{Resource: framework.GVK(cnrGVK), ActionType: framework.Add | framework.Update},
	}
}

func (tm *TopologyMatch) topologyMatchSupport(pod *v1.Pod) bool {
	if tm.resourcePolicy == consts.ResourcePluginPolicyNameNative {
		// native policy, only Guaranteed pod with full CPU supported
		if qos.GetPodQOS(pod) == v1.PodQOSGuaranteed && util.IsRequestFullCPU(pod) {
			return true
		}
		return false
	}

	if tm.resourcePolicy == consts.ResourcePluginPolicyNameDynamic {
		// dynamic policy, only dedicated_cores with numaBinding supported
		if util.IsDedicatedPod(pod) && util.IsNumaBinding(pod) {
			return true
		}
	}

	return false
}

func (tm *TopologyMatch) dedicatedPodsFilter(nodeInfo *framework.NodeInfo) func(consumer string) bool {
	dedicatedPods := make(map[string]struct{})
	for _, podInfo := range nodeInfo.Pods {
		if util.IsDedicatedPod(podInfo.Pod) {
			key := native.GenerateNamespaceNameKey(podInfo.Pod.Namespace, podInfo.Pod.Name)
			dedicatedPods[key] = struct{}{}
		}
	}

	return func(consumer string) bool {
		namespace, name, _, err := native.ParseNamespaceNameUIDKey(consumer)
		if err != nil {
			klog.Errorf("ParseNamespaceNameUIDKey consumer %v fail: %v", consumer, err)
			return false
		}

		// read only after map inited
		key := native.GenerateNamespaceNameKey(namespace, name)
		if _, ok := dedicatedPods[key]; ok {
			return true
		}

		return false
	}
}

func getScoringStrategyFunction(strategy config.ScoringStrategyType) (scoreStrategyFn, error) {
	switch strategy {
	case config.MostAllocated:
		return mostAllocatedScoreStrategy, nil
	case config.LeastAllocated:
		return leastAllocatedScoreStrategy, nil
	case consts.BalancedAllocation:
		return balancedAllocationScoreStrategy, nil
	case consts.LeastNUMANodes:
		return nil, fmt.Errorf("LeastNUMANodes not support yet")
	default:
		return nil, fmt.Errorf("illegal scoring strategy found")
	}
}

func TopologyZonesToNUMANodeList(zones []*v1alpha1.TopologyZone) NUMANodeList {
	nodes := NUMANodeList{}

	for _, topologyZone := range zones {
		if topologyZone.Type != v1alpha1.TopologyTypeSocket {
			continue
		}
		for _, child := range topologyZone.Children {
			if child.Type != v1alpha1.TopologyTypeNuma {
				continue
			}
			numaID, err := getID(child.Name)
			if err != nil {
				klog.Error(err)
				continue
			}
			capacity, allocatable, available := extractAvailableResources(child)
			nodes = append(nodes, NUMANode{
				SocketID:    topologyZone.Name,
				NUMAID:      numaID,
				Capacity:    capacity,
				Allocatable: allocatable,
				Available:   available,
			})
		}
	}

	return nodes
}

func TopologyZonesToNUMANodeMap(zones []*v1alpha1.TopologyZone) map[int]NUMANode {
	numaNodeMap := make(map[int]NUMANode)

	for _, topologyZone := range zones {
		if topologyZone.Type != v1alpha1.TopologyTypeSocket {
			continue
		}
		for _, child := range topologyZone.Children {
			if child.Type != v1alpha1.TopologyTypeNuma {
				continue
			}
			numaID, err := getID(child.Name)
			if err != nil {
				klog.Error(err)
				continue
			}
			capacity, allocatable, available := extractAvailableResources(child)
			numaNodeMap[numaID] = NUMANode{
				SocketID:    topologyZone.Name,
				NUMAID:      numaID,
				Capacity:    capacity,
				Allocatable: allocatable,
				Available:   available,
			}
		}
	}

	return numaNodeMap
}

func getID(name string) (int, error) {
	numaID, err := strconv.Atoi(name)
	if err != nil {
		return -1, fmt.Errorf("invalid zone format zone: %s : %v", name, err)
	}

	if numaID > maxNUMAId-1 || numaID < 0 {
		return -1, fmt.Errorf("invalid NUMA id range numaID: %d", numaID)
	}

	return numaID, nil
}

func extractAvailableResources(zone *v1alpha1.TopologyZone) (capacity, allocatable, available v1.ResourceList) {
	used := make(v1.ResourceList)
	for _, alloc := range zone.Allocations {
		for resName, quantity := range *alloc.Requests {
			if _, ok := used[resName]; !ok {
				used[resName] = quantity.DeepCopy()
			} else {
				value := used[resName]
				value.Add(quantity)
				used[resName] = value
			}
		}
	}
	return zone.Resources.Capacity.DeepCopy(), zone.Resources.Allocatable.DeepCopy(), quotav1.SubtractWithNonNegativeResult(*zone.Resources.Allocatable, used)
}

func minNumaNodeCount(resourceName v1.ResourceName, quantity resource.Quantity, numaNodeMap map[int]NUMANode) int {
	var (
		i           = 0
		sumResource resource.Quantity
	)

	// allocatable in each numa may not equal because of resource reserve
	for _, numaNode := range numaNodeMap {
		i++
		if i == 1 {
			sumResource = numaNode.Capacity[resourceName]
		} else {
			sumResource.Add(numaNode.Capacity[resourceName])
		}
		if sumResource.Cmp(quantity) >= 0 {
			return i
		}
	}
	return i
}

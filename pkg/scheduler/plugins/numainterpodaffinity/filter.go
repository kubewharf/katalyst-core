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

package numainterpodaffinity

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	v1 "k8s.io/api/core/v1"
	"k8s.io/klog/v2"
	"k8s.io/kubernetes/pkg/scheduler/framework"

	"github.com/kubewharf/katalyst-api/pkg/apis/node/v1alpha1"
	"github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/scheduler/cache"
	schedulerUtil "github.com/kubewharf/katalyst-core/pkg/scheduler/util"
	affinityutil "github.com/kubewharf/katalyst-core/pkg/util/affinity"
	"github.com/kubewharf/katalyst-core/pkg/util/native"
)

const (
	// preFilterStateKey is the key in CycleState to InterPodAffinity pre-computed data for Filtering.
	// Using the name of the plugin will likely help us avoid collisions with other plugins.
	preFilterStateKey = "PreFilter" + NameNUMAInterPodAffinity

	// The maximum number of NUMAID that Topology Manager allows is 64
	// https://kubernetes.io/docs/tasks/administer-cluster/topology-manager/#known-limitations
	maxNUMAId = 64
)

// key means node name
type allNodesNUMAAffinityCount map[string]affinityutil.TopologyAffinityCount

// Record the number of affinities and anti-affinities of each numa node to pod.
type PreFilterState struct {
	// A map of numa id to the number of existing pods that has anti-affinity seletor that match the "pod".
	ExistingAntiAffinityCounts allNodesNUMAAffinityCount
	// A map of numa id to the number of existing pods that match the affinity seletor of the "pod".
	AffinityCounts allNodesNUMAAffinityCount
	// A map of numa id to the number of existing pods that match the anti-affinity seletor of the "pod".
	AntiAffinityCounts allNodesNUMAAffinityCount
	// Flag that does not need to participate in the Filter process
	Skip bool
}

// All numaInfo on the node
type NUMAInfoList []affinityutil.NumaInfo

type socket2numaList map[int][]int

func (nc allNodesNUMAAffinityCount) clone() allNodesNUMAAffinityCount {
	newTc := make(allNodesNUMAAffinityCount, len(nc))
	for nodeUID, tc := range nc {
		newTc[nodeUID] = make(affinityutil.TopologyAffinityCount, len(tc))
		newTc[nodeUID].Append(tc)
	}
	return newTc
}

func (ps *PreFilterState) Clone() framework.StateData {
	if ps == nil {
		return nil
	}

	var newState PreFilterState
	newState.AffinityCounts = ps.AffinityCounts.clone()
	newState.AntiAffinityCounts = ps.AntiAffinityCounts.clone()
	newState.ExistingAntiAffinityCounts = ps.ExistingAntiAffinityCounts.clone()
	newState.Skip = ps.Skip
	return &newState
}

func (na *NUMAInterPodAffinity) dedicatedPodsFilter(nodeInfo *framework.NodeInfo) func(consumer string) bool {
	dedicatedPods := make(map[string]struct{})
	for _, podInfo := range nodeInfo.Pods {
		if schedulerUtil.IsDedicatedPod(podInfo.Pod) {
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

func (na *NUMAInterPodAffinity) PreFilter(ctx context.Context, cycleState *framework.CycleState, pod *v1.Pod) (*framework.PreFilterResult, *framework.Status) {
	// Get labels and selectors information of pod
	podAffinity, err := affinityutil.UnmarshalAffinity(pod.Annotations)
	if err != nil {
		return nil, framework.AsStatus(err)
	}

	anno, err := unmarshalAnnotation(pod.Annotations)
	if err != nil {
		return nil, framework.AsStatus(err)
	}

	// If pod does not have affinity & anti-affinity and label information,
	// there are not need to filter, end preFilter directly.
	if podAffinity.Affinity == nil && podAffinity.AntiAffinity == nil && pod.Labels == nil {
		cycleState.Write(preFilterStateKey, &PreFilterState{Skip: true})
		return nil, nil
	}

	// If pod does not set numa_binding as true,
	// there are not need to filter, end preFilter directly.
	if anno[consts.PodAnnotationMemoryEnhancementNumaBinding] != consts.PodAnnotationMemoryEnhancementNumaBindingEnable ||
		pod.Annotations[consts.PodAnnotationQoSLevelKey] != consts.PodAnnotationQoSLevelDedicatedCores {
		cycleState.Write(preFilterStateKey, &PreFilterState{Skip: true})
		return nil, nil
	}

	// If pod set numa_exclusive as true, and it has affinity seletor,
	// the pod can't be scheduled, return error;
	// if it doesn't has affinity seletor,
	// there are not need to filter, end preFilter directly.
	if anno[consts.PodAnnotationMemoryEnhancementNumaExclusive] == consts.PodAnnotationMemoryEnhancementNumaExclusiveEnable {
		if podAffinity.Affinity != nil {
			return nil, framework.AsStatus(fmt.Errorf("exclusive=true and has affinity seletors, can't be scheduled"))
		}
		cycleState.Write(preFilterStateKey, &PreFilterState{Skip: true})
		return nil, nil
	}

	podAffinityInfo := affinityutil.RequiredPodAffinityInfo(podAffinity, pod.Labels)

	// Get labels and selectors information of numa nodes
	var allNodes []*framework.NodeInfo
	var nodesWithRequiredAntiAffinityPods []*framework.NodeInfo
	var nodesWithLabels []*framework.NodeInfo

	if allNodes, err = na.sharedLister.NodeInfos().List(); err != nil {
		return nil, framework.AsStatus(fmt.Errorf("failed to list NodeInfos: %w", err))
	}

	allNodesNUMAInfoList := make(map[string]NUMAInfoList)
	allNodesSocket2NumaList := make(map[string]socket2numaList)

	for _, nodeInfo := range allNodes {
		nodeName := nodeInfo.Node().Name
		// nodeResourceTopologycache := cache.GetCache().GetNodeResourceTopology(nodeName, na.dedicatedPodsFilter(nodeInfo))
		nodeResourceTopologycache := cache.GetCache().GetNodeResourceTopology(nodeName, na.dedicatedPodsFilter(nodeInfo))
		nodeAssumedPodAffinity := cache.GetCache().GetAssumedPodAffinityInfo(nodeName)

		NUMAInfoList, withAnti, withLabel, socket2numaList := TopologyZonesToNUMAInfoList(nodeResourceTopologycache.TopologyZone, nodeAssumedPodAffinity, nodeInfo)
		allNodesNUMAInfoList[nodeName] = NUMAInfoList
		allNodesSocket2NumaList[nodeName] = socket2numaList
		if withAnti {
			nodesWithRequiredAntiAffinityPods = append(nodesWithRequiredAntiAffinityPods, nodeInfo)
		}
		if withLabel {
			nodesWithLabels = append(nodesWithLabels, nodeInfo)
		}
	}

	// Calculate affinity
	var state PreFilterState
	state.ExistingAntiAffinityCounts = na.getExistingAntiAffinityCounts(ctx, podAffinityInfo, allNodesNUMAInfoList,
		nodesWithRequiredAntiAffinityPods, allNodesSocket2NumaList)
	state.AntiAffinityCounts = na.getAntiAffinityCounts(ctx, podAffinityInfo, allNodesNUMAInfoList,
		nodesWithLabels, allNodesSocket2NumaList)
	state.AffinityCounts = na.getAffinityCounts(ctx, podAffinityInfo, allNodesNUMAInfoList,
		nodesWithLabels, allNodesSocket2NumaList)

	state.Skip = false
	cycleState.Write(preFilterStateKey, &state)
	return nil, nil
}

// PreFilterExtensions returns prefilter extensions, pod add and remove.
func (na *NUMAInterPodAffinity) PreFilterExtensions() framework.PreFilterExtensions {
	return na
}

// AddPod from pre-computed data in cycleState.
func (na *NUMAInterPodAffinity) AddPod(ctx context.Context, cycleState *framework.CycleState, podToSchedule *v1.Pod, podInfoToAdd *framework.PodInfo, nodeInfo *framework.NodeInfo) *framework.Status {
	return nil
}

// RemovePod from pre-computed data in cycleState.
func (na *NUMAInterPodAffinity) RemovePod(ctx context.Context, cycleState *framework.CycleState, podToSchedule *v1.Pod, podInfoToRemove *framework.PodInfo, nodeInfo *framework.NodeInfo) *framework.Status {
	return nil
}

func (na *NUMAInterPodAffinity) getExistingAntiAffinityCounts(ctx context.Context, podAffinityInfo affinityutil.PodInfo,
	allNodesNUMAInfoList map[string]NUMAInfoList, nodesInfo []*framework.NodeInfo,
	allNodesSocket2NumaList map[string]socket2numaList) allNodesNUMAAffinityCount {
	topoMaps := make(allNodesNUMAAffinityCount)

	processNode := func(nodeIndex int) {
		nodeName := nodesInfo[nodeIndex].Node().Name
		var state = affinityutil.PreFilterState{
			PodAffinityInfo:            podAffinityInfo,
			NumaAffinityInfoList:       allNodesNUMAInfoList[nodeName],
			ExistingAntiAffinityCounts: make(affinityutil.TopologyAffinityCount),
		}
		affinityutil.GetExistingAntiAffinityCountsSerial(&state, allNodesSocket2NumaList[nodeName])
		topoMaps[nodeName] = state.ExistingAntiAffinityCounts
	}
	na.parallelizer.Until(ctx, len(nodesInfo), processNode)

	return topoMaps
}

func (na *NUMAInterPodAffinity) getAntiAffinityCounts(ctx context.Context, podAffinityInfo affinityutil.PodInfo,
	allNodesNUMAInfoList map[string]NUMAInfoList, nodesInfo []*framework.NodeInfo,
	allNodesSocket2NumaList map[string]socket2numaList) allNodesNUMAAffinityCount {
	topoMaps := make(allNodesNUMAAffinityCount)

	processNode := func(nodeIndex int) {
		nodeName := nodesInfo[nodeIndex].Node().Name
		var state = affinityutil.PreFilterState{
			PodAffinityInfo:      podAffinityInfo,
			NumaAffinityInfoList: allNodesNUMAInfoList[nodeName],
			AntiAffinityCounts:   make(affinityutil.TopologyAffinityCount),
		}
		affinityutil.GetAntiAffinityCountsSerial(&state, allNodesSocket2NumaList[nodeName])
		topoMaps[nodeName] = state.AntiAffinityCounts
	}
	na.parallelizer.Until(ctx, len(nodesInfo), processNode)

	return topoMaps
}

func (na *NUMAInterPodAffinity) getAffinityCounts(ctx context.Context, podAffinityInfo affinityutil.PodInfo,
	allNodesNUMAInfoList map[string]NUMAInfoList, nodesInfo []*framework.NodeInfo,
	allNodesSocket2NumaList map[string]socket2numaList) allNodesNUMAAffinityCount {
	topoMaps := make(allNodesNUMAAffinityCount)

	processNode := func(nodeIndex int) {
		nodeName := nodesInfo[nodeIndex].Node().Name
		var state = affinityutil.PreFilterState{
			PodAffinityInfo:      podAffinityInfo,
			NumaAffinityInfoList: allNodesNUMAInfoList[nodeName],
			AffinityCounts:       make(affinityutil.TopologyAffinityCount),
		}
		affinityutil.GetAffinityCountsSerial(&state, allNodesSocket2NumaList[nodeName])
		topoMaps[nodeName] = state.AffinityCounts
	}
	na.parallelizer.Until(ctx, len(nodesInfo), processNode)

	return topoMaps
}

// Organize the topology information of pods on all nodes,
// and record nodes with anti-affinity selectors and labels.
func TopologyZonesToNUMAInfoList(zones []*v1alpha1.TopologyZone, assumedPodAffinity cache.AssumedPodAffinityInfo,
	nodeInfo *framework.NodeInfo) (NUMAInfoList, bool, bool, map[int][]int) {
	NUMAInfoList := NUMAInfoList{}
	withRequiredAntiAffinity := false
	withLabels := false
	var socket2numaList = make(map[int][]int)

	for _, topologyZone := range zones {
		if topologyZone.Type != v1alpha1.TopologyTypeSocket {
			continue
		}
		socketID, err := strconv.Atoi(topologyZone.Name)
		if err != nil {
			klog.Error(err)
			continue
		}
		socket2numaList[socketID] = make([]int, 0)

		for _, child := range topologyZone.Children {
			if child.Type != v1alpha1.TopologyTypeNuma {
				continue
			}
			numaID, err := getID(child.Name)
			if err != nil {
				klog.Error(err)
				continue
			}
			socket2numaList[socketID] = append(socket2numaList[socketID], numaID)

			labels := make(map[string][]string)
			seletors := []affinityutil.Selector{}
			withExclusive := false
			for _, allocation := range child.Allocations {
				podID, err := getPod(allocation.Consumer, nodeInfo)
				if err != nil {
					klog.Error(err)
					continue
				}
				pod := nodeInfo.Pods[podID].Pod
				anno, err := unmarshalAnnotation(pod.Annotations)
				if err != nil {
					klog.Error(err)
					continue
				}

				if anno[consts.PodAnnotationMemoryEnhancementNumaExclusive] == consts.PodAnnotationMemoryEnhancementNumaExclusiveEnable {
					withExclusive = true
				}

				labels = affinityutil.MergeNumaInfoMap(pod.Labels, labels)
				podAffinity, err := affinityutil.UnmarshalAffinity(pod.Annotations)
				if err != nil {
					klog.Error(err)
					continue
				}
				if podAffinity.AntiAffinity != nil && podAffinity.AntiAffinity.Required != nil {
					seletors = append(seletors, podAffinity.AntiAffinity.Required...)
				}
			}
			for _, podAffinity := range assumedPodAffinity {
				labels = affinityutil.MergeNumaInfoMap(podAffinity.Labels, labels)
				podAffinity, err := affinityutil.UnmarshalAffinity(podAffinity.Annotations)
				if err != nil {
					klog.Error(err)
					continue
				}
				if podAffinity.AntiAffinity != nil && podAffinity.AntiAffinity.Required != nil {
					seletors = append(seletors, podAffinity.AntiAffinity.Required...)
				}
			}

			if len(seletors) > 0 {
				withRequiredAntiAffinity = true
			}
			if len(labels) > 0 {
				withLabels = true
			}
			NUMAInfoList = append(NUMAInfoList, affinityutil.NumaInfo{
				Labels:                        labels,
				SocketID:                      socketID,
				NumaID:                        numaID,
				AntiAffinityRequiredSelectors: seletors,
				Exclusive:                     withExclusive,
			})

		}
	}

	return NUMAInfoList, withRequiredAntiAffinity, withLabels, socket2numaList
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

func getPod(consumer string, nodeInfo *framework.NodeInfo) (int, error) {
	ns, name, uid, err := native.ParseNamespaceNameUIDKey(consumer)
	if err != nil {
		return -1, err
	}
	for i, podInfo := range nodeInfo.Pods {
		if string(podInfo.Pod.UID) == uid && podInfo.Pod.Name == name && podInfo.Pod.Namespace == ns {
			return i, nil
		}
	}
	return -1, fmt.Errorf("unable to find pod with uid %v", uid)
}

func getPreFilterState(cycleState *framework.CycleState) (*PreFilterState, error) {
	c, err := cycleState.Read(preFilterStateKey)
	if err != nil {
		// preFilterState doesn't exist, likely PreFilter wasn't invoked.
		return nil, fmt.Errorf("error reading %q from cycleState: %w", preFilterStateKey, err)
	}

	s, ok := c.(*PreFilterState)
	if !ok {
		return nil, fmt.Errorf("%+v  convert to interpodaffinity.state error", c)
	}
	return s, nil
}

func (na *NUMAInterPodAffinity) Filter(ctx context.Context, cycleState *framework.CycleState, pod *v1.Pod, nodeInfo *framework.NodeInfo) *framework.Status {

	if nodeInfo.Node() == nil {
		return framework.NewStatus(framework.Error, "node not found")
	}

	state, err := getPreFilterState(cycleState)
	if err != nil {
		return framework.NewStatus(framework.Error, ErrGetPreFilterState)
	}
	// If state is nil, it means there are not need to filter.
	if state.Skip {
		return nil
	}

	nodeName := nodeInfo.Node().Name
	nodeResourceTopologycache := cache.GetCache().GetNodeResourceTopology(nodeName, na.dedicatedPodsFilter(nodeInfo))

	numaList := make([]int, 0)
	for _, socket := range nodeResourceTopologycache.TopologyZone {
		if socket.Type != v1alpha1.TopologyTypeSocket {
			continue
		}
		for _, numa := range socket.Children {
			numaID, err := getID(numa.Name)
			if err != nil {
				continue
			}
			numaList = append(numaList, numaID)
		}
	}
	if len(numaList) == 0 {
		return framework.NewStatus(framework.Unschedulable, ErrGetNodeResourceTopology)
	}

	podAffinity, err := affinityutil.UnmarshalAffinity(pod.Annotations)
	if err != nil {
		return framework.NewStatus(framework.Error, fmt.Sprintf("unmarshal pod affinity failed: %v", err))
	}

	affinityFlag := false
	if podAffinity.Affinity != nil && podAffinity.Affinity.Required != nil {
		affinityFlag = true
	}
	availableNUMA := len(numaList)
	for _, numaID := range numaList {
		// Filter for existing pods with anti-affinity
		if !(state.ExistingAntiAffinityCounts[nodeName] == nil || state.ExistingAntiAffinityCounts[nodeName][numaID] <= 0) {
			availableNUMA -= 1
			continue
		}
		// Filter for pods with anti-affinity
		if !(state.AntiAffinityCounts[nodeName] == nil || state.AntiAffinityCounts[nodeName][numaID] <= 0) {
			availableNUMA -= 1
			continue
		}
		// Filter for pods with affinity
		if (state.AffinityCounts[nodeName] == nil || state.AffinityCounts[nodeName][numaID] <= 0) && affinityFlag {
			availableNUMA -= 1
			continue
		}
	}

	if availableNUMA <= 0 {
		return framework.NewStatus(framework.Unschedulable, ErrNUMAInterPodAffinityNotMatch)
	}
	return nil
}

func unmarshalAnnotation(annotation map[string]string) (map[string]string, error) {
	if annotation[consts.PodAnnotationMemoryEnhancementNumaBinding] != "" &&
		annotation[consts.PodAnnotationMemoryEnhancementNumaExclusive] != "" {
		return annotation, nil
	}

	if _, ok := annotation[consts.PodAnnotationMemoryEnhancementKey]; !ok {
		return annotation, nil
	}
	enhancementKVs := make(map[string]string)
	err := json.Unmarshal([]byte(annotation[consts.PodAnnotationMemoryEnhancementKey]), &enhancementKVs)
	if err != nil {
		return nil, err
	}
	anno := make(map[string]string)
	for k, v := range enhancementKVs {
		anno[k] = v
	}
	return anno, nil
}

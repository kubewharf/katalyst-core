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

package util

import (
	"encoding/json"
	"fmt"
	"sync"

	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	apiconsts "github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

// Inter-pod affinity annotations
type Selector struct {
	MatchLabels map[string]string
	Zone        string
}
type MicroTopologyPodAffinityAnnotation struct {
	Required  []Selector
	Preferred []Selector
}

// Pod's inter-pod affinity & anti-affinity seletor at NUMA level
type MicroTopologyPodAffnity struct {
	Affinity     *MicroTopologyPodAffinityAnnotation
	AntiAffinity *MicroTopologyPodAffinityAnnotation
}

type TopologyAffinityCount map[int]int

// Record all numa level affinity information on numa
type NumaInfo struct {
	Labels                        map[string][]string
	SocketID                      int
	NumaID                        int
	AntiAffinityRequiredSelectors []Selector
}

// Record numa level affinity information on pod
type PodInfo struct {
	Labels                        map[string]string
	AffinityRequiredSelectors     []Selector
	AntiAffinityRequiredSelectors []Selector
}

type PreFilterState struct {
	// A map of numa id to the number of existing pods that has anti-affinity seletor that match the "pod".
	ExistingAntiAffinityCounts TopologyAffinityCount
	// A map of numa id to the number of existing pods that match the affinity seletor of the "pod".
	AffinityCounts TopologyAffinityCount
	// A map of numa id to the number of existing pods that match the anti-affinity seletor of the "pod".
	AntiAffinityCounts   TopologyAffinityCount
	PodAffinityInfo      PodInfo
	NumaAffinityInfoList []NumaInfo
}

func UnmarshalAffinity(annotations map[string]string) (*MicroTopologyPodAffnity, error) {
	AffinityStrList := []string{apiconsts.PodAnnotationMicroTopologyInterPodAffinity, apiconsts.PodAnnotationMicroTopologyInterPodAntiAffinity}
	AffinityMap := make(map[string]*MicroTopologyPodAffinityAnnotation)

	for _, affinityStr := range AffinityStrList {
		if annotations[affinityStr] != "" {
			var affinity *MicroTopologyPodAffinityAnnotation
			if err := json.Unmarshal([]byte(annotations[affinityStr]), &affinity); err != nil {
				return nil, fmt.Errorf("unmarshal %s: %s failed with error: %v",
					affinityStr, annotations[affinityStr], err)
			}

			for j, content := range affinity.Required {
				// matchLabel must has content
				if content.MatchLabels == nil {
					return nil, fmt.Errorf("MatchLabels of %s:%v can not be nil", affinityStr, affinity)
				}
				// Zone defaults to numa
				if content.Zone == "" {
					affinity.Required[j].Zone = apiconsts.PodAnnotationMicroTopologyAffinityDefaultZone
				}
				if content.Zone != "" && content.Zone != apiconsts.PodAnnotationMicroTopologyAffinitySocket &&
					content.Zone != apiconsts.PodAnnotationMicroTopologyAffinityNUMA {
					return nil, fmt.Errorf("zone must be numa or socket")
				}
			}
			for j, content := range affinity.Preferred {
				if content.MatchLabels == nil {
					return nil, fmt.Errorf("MatchLabels of %s:%v can not be nil", affinityStr, affinity)
				}
				if content.Zone == "" {
					affinity.Required[j].Zone = apiconsts.PodAnnotationMicroTopologyAffinityDefaultZone
				}
			}

			AffinityMap[affinityStr] = affinity

		}
	}

	return &MicroTopologyPodAffnity{
		Affinity:     AffinityMap[apiconsts.PodAnnotationMicroTopologyInterPodAffinity],
		AntiAffinity: AffinityMap[apiconsts.PodAnnotationMicroTopologyInterPodAntiAffinity],
	}, nil
}

func MergeNumaInfoMap(podLabels map[string]string, numaLabels map[string][]string) map[string][]string {
	for key, val := range podLabels {
		if numaLabels[key] != nil {
			for _, label := range numaLabels[key] {
				if label == val {
					break
				}
			}
			numaLabels[key] = append(numaLabels[key], val)
			continue
		}
		numaLabels[key] = []string{val}
	}
	return numaLabels
}

// Analyze whether the existing pod on NUMA is compatible with the new pod,
// and calculate numa nodes' util.TopologyAffinityCount through imformation of Seletors and labels
func matchNUMAAffinity(Seletors []Selector, labels map[string]string,
	socket int, numa int, cpuSet machine.CPUSet) TopologyAffinityCount {
	topologyMap := make(TopologyAffinityCount)
	for _, seletor := range Seletors {
		for key, value := range seletor.MatchLabels {
			if labels[key] == value {
				if seletor.Zone == apiconsts.PodAnnotationMicroTopologyAffinitySocket {
					numaList := cpuSet.ToSliceInt()
					for _, n := range numaList {
						topologyMap[n] += 1
					}
				} else {
					topologyMap[numa] += 1
				}
			}
		}
	}
	return topologyMap
}

// Analyze whether the new pod is compatible with the existing pod on NUMA,
// Calculate numa nodes' util.TopologyAffinityCount through imformation of Seletors and labels
func matchPodAffinity(Seletors []Selector, labels map[string][]string,
	socket int, numa int, numaSet machine.CPUSet) TopologyAffinityCount {
	topologyMap := make(TopologyAffinityCount)
	for _, seletor := range Seletors {
		for key, value := range seletor.MatchLabels {
			for _, numaVal := range labels[key] {
				if numaVal == value {
					if seletor.Zone == apiconsts.PodAnnotationMicroTopologyAffinitySocket {
						numaList := numaSet.ToSliceInt()
						for _, n := range numaList {
							topologyMap[n] += 1
						}
					} else {
						topologyMap[numa] += 1
					}
				}
			}

		}
	}
	return topologyMap
}

// Calculate the number of existing pods that has anti-affinity seletor that match the "pod",
// and update the util.TopologyAffinityCount imformation
func GetExistingAntiAffinityCounts(state *PreFilterState, topology *machine.CPUTopology) {
	numNUMA := len(state.NumaAffinityInfoList)
	topologyMaps := make([]TopologyAffinityCount, numNUMA)

	var wg sync.WaitGroup
	for i := 0; i < numNUMA; i++ {
		wg.Add(1)
		go func(numaID int) {
			defer wg.Done()
			numaAffinity := state.NumaAffinityInfoList[numaID]
			numaSet := topology.CPUDetails.NUMANodesInSockets(numaAffinity.SocketID)
			topologyMaps[numaID] = matchNUMAAffinity(numaAffinity.AntiAffinityRequiredSelectors,
				state.PodAffinityInfo.Labels, numaAffinity.SocketID, numaAffinity.NumaID, numaSet)
		}(i)
	}

	wg.Wait()

	for i := 0; i < numNUMA; i++ {
		state.ExistingAntiAffinityCounts.Append(topologyMaps[i])
	}

}

// Calculate the number of existing pods that match the anti-affinity seletor of the "pod",
// and update the util.TopologyAffinityCount imformation
func GetAntiAffinityCounts(state *PreFilterState, topology *machine.CPUTopology) {
	numNUMA := len(state.NumaAffinityInfoList)
	topologyMaps := make([]TopologyAffinityCount, numNUMA)

	var wg sync.WaitGroup
	for i := 0; i < numNUMA; i++ {
		wg.Add(1)
		go func(numaID int) {
			defer wg.Done()
			numaAffinity := state.NumaAffinityInfoList[numaID]
			numaSet := topology.CPUDetails.NUMANodesInSockets(numaAffinity.SocketID)
			topologyMaps[numaID] = matchPodAffinity(state.PodAffinityInfo.AntiAffinityRequiredSelectors,
				numaAffinity.Labels, numaAffinity.SocketID, numaAffinity.NumaID, numaSet)
		}(i)
	}

	wg.Wait()

	for i := 0; i < numNUMA; i++ {
		state.AntiAffinityCounts.Append(topologyMaps[i])
	}

}

// Calculate the number of existing pods that match the affinity seletor of the "pod",
// and update the util.TopologyAffinityCount imformation
func GetAffinityCounts(state *PreFilterState, topology *machine.CPUTopology) {
	numNUMA := len(state.NumaAffinityInfoList)
	topologyMaps := make([]TopologyAffinityCount, numNUMA)

	var wg sync.WaitGroup
	for i := 0; i < numNUMA; i++ {
		wg.Add(1)
		go func(numaID int) {
			defer wg.Done()
			numaAffinity := state.NumaAffinityInfoList[numaID]
			numaSet := topology.CPUDetails.NUMANodesInSockets(numaAffinity.SocketID)
			topologyMaps[numaID] = matchPodAffinity(state.PodAffinityInfo.AffinityRequiredSelectors,
				numaAffinity.Labels, numaAffinity.SocketID, numaAffinity.NumaID, numaSet)
		}(i)
	}

	wg.Wait()

	for i := 0; i < numNUMA; i++ {
		state.AffinityCounts.Append(topologyMaps[i])
	}

}

// Judge whether hint meets the affinity requirements, true means the hint is valid
func HintPodAffinityFilter(state *PreFilterState, hint *pluginapi.TopologyHint) bool {
	numaList := hint.GetNodes()
	for _, numa := range numaList {
		if state.ExistingAntiAffinityCounts[int(numa)] > 0 {
			return false
		}
		if state.AntiAffinityCounts[int(numa)] > 0 {
			return false
		}
		if len(state.PodAffinityInfo.AffinityRequiredSelectors) > 0 &&
			state.AffinityCounts[int(numa)] <= 0 {
			return false
		}
	}
	return true
}

func RequiredPodAffinityInfo(podAffinity *MicroTopologyPodAffnity, req *pluginapi.ResourceRequest) PodInfo {
	var affinityReq []Selector
	var antiAffinityReq []Selector
	if podAffinity.Affinity != nil {
		affinityReq = podAffinity.Affinity.Required
	}
	if podAffinity.AntiAffinity != nil {
		antiAffinityReq = podAffinity.AntiAffinity.Required
	}
	return PodInfo{
		Labels:                        req.Labels,
		AffinityRequiredSelectors:     affinityReq,
		AntiAffinityRequiredSelectors: antiAffinityReq,
	}
}

func (t TopologyAffinityCount) Append(toAppend TopologyAffinityCount) {
	for key, value := range toAppend {
		t[key] += value
	}
}

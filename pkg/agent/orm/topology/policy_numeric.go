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

package topology

import (
	"sort"

	v1 "k8s.io/api/core/v1"
	"k8s.io/klog/v2"

	"github.com/kubewharf/katalyst-core/pkg/util/bitmask"
)

// numericPolicy implements a policy:
// 1. for align resources, hints should be totally equal and at-least-one preferred.
// 2. for other resources, bigger hints always contains smaller hints.
// 3. more preferredHint count hints permutation is preferred.
// 4. smaller maxNumaCount hints permutation is preferred.
type numericPolicy struct {
	// alignResourceNames are those resources which should be aligned in numa node.
	alignResourceNames []string
}

// PolicyNumeric policy name.
const PolicyNumeric string = "numeric"

var defaultAlignResourceNames = []string{v1.ResourceCPU.String(), v1.ResourceMemory.String()}

// NewNumericPolicy returns numeric policy.
func NewNumericPolicy(alignResourceNames []string) Policy {
	if alignResourceNames == nil {
		alignResourceNames = defaultAlignResourceNames
	}
	return &numericPolicy{
		alignResourceNames: alignResourceNames,
	}
}

// Name returns numericPolicy name
func (p *numericPolicy) Name() string {
	return PolicyNumeric
}

func (p *numericPolicy) Merge(providersHints []map[string][]TopologyHint) (map[string]TopologyHint, bool) {
	if len(providersHints) == 0 {
		return map[string]TopologyHint{
			defaultResourceKey: {nil, true},
		}, true
	}

	if existEmptyHintSlice(providersHints) {
		klog.Infof("[numeric_policy] admit failed due to existing empty hint slice")
		return nil, false
	}

	filteredHints, resourceNames := filterProvidersHints(providersHints)

	bestHints := findBestNumericPermutation(filteredHints, getAlignResourceIndexes(resourceNames, p.alignResourceNames))
	// no permutation fits the policy
	if bestHints == nil {
		return nil, false
	}

	if len(bestHints) != len(resourceNames) {
		// This should not happen.
		klog.Warningf("[numeric policy] wrong hints length %d vs resource length %d", len(bestHints), len(resourceNames))
		return nil, false
	}

	result := make(map[string]TopologyHint)
	for i := range resourceNames {
		result[resourceNames[i]] = bestHints[i]
	}
	return result, true
}

// existEmptyHintSlice returns true if there is empty hint slice in providersHints
func existEmptyHintSlice(providersHints []map[string][]TopologyHint) bool {
	for _, hints := range providersHints {
		for resource := range hints {
			// hint providers return nil if there is no possible NUMA affinity for resource
			// hint providers return empty slice if there is no preference NUMA affinity for resource
			if hints[resource] != nil && len(hints[resource]) == 0 {
				klog.Infof("[numeric_policy] hint Provider has no possible NUMA affinity for resource: %s", resource)
				return true
			}
		}
	}

	return false
}

// findBestNumericPermutation finds the best numeric permutation.
func findBestNumericPermutation(filteredHints [][]TopologyHint, alignResourceIndexes []int) []TopologyHint {
	var bestHints []TopologyHint

	iterateAllProviderTopologyHints(filteredHints, func(permutation []TopologyHint) {
		// the length of permutation and the order of the resources hints in it are equal to filteredHints,
		// align and unaligned resource hints can be filtered by alignResourceIndexes

		// 1. check if align resource hints are equal,
		// and there should be at least one preferred hint.
		var alignHasPreferred bool
		for i := 0; i < len(alignResourceIndexes)-1; i++ {
			cur := alignResourceIndexes[i]
			next := alignResourceIndexes[i+1]

			if !numaAffinityAligned(permutation[cur], permutation[next]) {
				// hints are not aligned
				return
			}
			alignHasPreferred = permutation[cur].Preferred || permutation[next].Preferred
		}
		if len(alignResourceIndexes) == 1 {
			alignHasPreferred = permutation[alignResourceIndexes[0]].Preferred
		}
		if len(alignResourceIndexes) > 0 && !alignHasPreferred {
			// all hints are not preferred
			return
		}

		// 2. check if bigger numa-node hints contains smaller numa-node hints.
		if !isSubsetPermutation(permutation) {
			return
		}

		if bestHints == nil {
			bestHints = DeepCopyTopologyHints(permutation)
		}

		// 3. If preferredHint count beside align resources in this permutation is larger than
		// that in current bestHints, always choose more preferredHint permutation.
		if preferredCountBesideAlign(permutation, alignResourceIndexes) >
			preferredCountBesideAlign(bestHints, alignResourceIndexes) {
			bestHints = DeepCopyTopologyHints(permutation)
			return
		}

		// 4. Only Consider permutation that have smaller maxNumaCount than the
		// maxNumaCount in the current bestHint.
		if getMaxNumaCount(permutation) < getMaxNumaCount(bestHints) {
			bestHints = DeepCopyTopologyHints(permutation)
			return
		}
	})

	return bestHints
}

// getAlignResourceIndexes gets align resource indexes in resources array.
func getAlignResourceIndexes(resources []string, alignResourceNames []string) []int {
	resourceIndexes := make(map[string]int)
	for i, rn := range resources {
		resourceIndexes[rn] = i
	}
	var result []int
	for _, align := range alignResourceNames {
		index, ok := resourceIndexes[align]
		if ok {
			result = append(result, index)
		}
	}
	return result
}

// getMaxNumaCount returns the max numa count in the given hints.
func getMaxNumaCount(permutation []TopologyHint) int {
	var result int
	for _, hint := range permutation {
		if hint.NUMANodeAffinity == nil {
			continue
		}
		if hint.NUMANodeAffinity.Count() > result {
			result = hint.NUMANodeAffinity.Count()
		}
	}
	return result
}

// preferredCountBesideAlign counts the preferred hints beside align resources.
func preferredCountBesideAlign(hints []TopologyHint, alignIndexes []int) int {
	var result int
	alignIndexesMap := map[int]bool{}
	for _, index := range alignIndexes {
		alignIndexesMap[index] = true
	}
	for i, hint := range hints {
		if _, ok := alignIndexesMap[i]; ok {
			continue
		}
		if hint.Preferred {
			result++
		}
	}
	return result
}

// numaAffinityAligned checks a,b TopologyHint could be aligned or not.
func numaAffinityAligned(a, b TopologyHint) bool {
	if a.NUMANodeAffinity == nil && b.NUMANodeAffinity == nil {
		return a.Preferred == b.Preferred
	} else if a.NUMANodeAffinity == nil { // b.NUMANodeAffinity != nil
		// if a.Preferred, there is no NUMA preference for a, so it can be aligned with b.
		return a.Preferred
	} else if b.NUMANodeAffinity == nil { // a.NUMANodeAffinity != nil
		// if b.Preferred, there is no NUMA preference for b, so it can be aligned with a.
		return b.Preferred
	}

	// NUMANodeAffinity of alignResources should be totally equal
	return a.NUMANodeAffinity.IsEqual(b.NUMANodeAffinity)
}

// isSubsetPermutation checks whether permutation meets that bigger numa-node hints always
// contain smaller numa-node hints or not.
func isSubsetPermutation(permutation []TopologyHint) bool {
	// When NUMANodeAffinity is nil,  means this has no preference.
	// We should ignore it.
	var filters []TopologyHint
	for _, hint := range permutation {
		if hint.NUMANodeAffinity != nil {
			filters = append(filters, hint)
		}
	}

	// Sort from small numa node count to big count.
	sort.Slice(filters, func(i, j int) bool {
		return filters[i].NUMANodeAffinity.Count() <= filters[j].NUMANodeAffinity.Count()
	})

	for i := 0; i < len(filters)-1; i++ {
		cur := filters[i]
		next := filters[i+1]
		if !bitmask.And(next.NUMANodeAffinity, cur.NUMANodeAffinity).IsEqual(cur.NUMANodeAffinity) {
			return false
		}
	}

	return true
}

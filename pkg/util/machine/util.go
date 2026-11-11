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

package machine

import (
	"fmt"
	"math"
	"sort"
)

const (
	LargeNUMAsPoint = 16
)

// CalculateGlobalRampUpReclaimTarget applies the QRM ramp-up ratio semantics
// to a node-level CPU count: floor the ratio result, align it down to an even
// number, then preserve the configured minimum.
func CalculateGlobalRampUpReclaimTarget(total int, ratio float64, minimum int) int {
	target := int(math.Floor(float64(total) * ratio))
	target -= target % 2
	if target < minimum {
		return minimum
	}
	return target
}

// DistributeNUMATarget distributes a global target across NUMA nodes while
// respecting per-NUMA capacity and minimum constraints. The returned counts
// sum to target and differ by at most one. Remainders are assigned by ascending
// NUMA ID among nodes with sufficient capacity, making the result stable.
func DistributeNUMATarget(available map[int]int, target, minPerNUMA int) (map[int]int, error) {
	if len(available) == 0 {
		return nil, fmt.Errorf("cannot distribute target %d across no NUMA nodes", target)
	}
	if minPerNUMA < 0 {
		return nil, fmt.Errorf("per-NUMA minimum must be non-negative, got %d", minPerNUMA)
	}

	numaIDs := make([]int, 0, len(available))
	for numaID := range available {
		numaIDs = append(numaIDs, numaID)
	}
	sort.Ints(numaIDs)
	for _, numaID := range numaIDs {
		capacity := available[numaID]
		if capacity < minPerNUMA {
			return nil, fmt.Errorf("NUMA %d capacity %d is below minimum %d", numaID, capacity, minPerNUMA)
		}
	}

	minTotal := len(available) * minPerNUMA
	if target < minTotal {
		return nil, fmt.Errorf("target %d is below per-NUMA minimum total %d", target, minTotal)
	}

	base := target / len(available)
	remainder := target % len(available)
	result := make(map[int]int, len(available))
	capableForRemainder := make([]int, 0, len(available))
	for _, numaID := range numaIDs {
		capacity := available[numaID]
		if capacity < base {
			return nil, fmt.Errorf("cannot distribute target %d within NUMA capacities while keeping counts balanced", target)
		}
		result[numaID] = base
		if capacity > base {
			capableForRemainder = append(capableForRemainder, numaID)
		}
	}
	if len(capableForRemainder) < remainder {
		return nil, fmt.Errorf("cannot distribute target %d within NUMA capacities while keeping counts balanced", target)
	}
	for _, numaID := range capableForRemainder[:remainder] {
		result[numaID]++
	}
	return result, nil
}

// TransformCPUAssignmentFormat transforms cpu assignment string format to cpuset format
func TransformCPUAssignmentFormat(assignment map[uint64]string) map[int]CPUSet {
	res := make(map[int]CPUSet)
	for k, v := range assignment {
		res[int(k)] = MustParse(v)
	}
	return res
}

// ParseCPUAssignmentFormat parses the given assignments into string format
func ParseCPUAssignmentFormat(assignments map[int]CPUSet) map[uint64]string {
	if assignments == nil {
		return nil
	}

	res := make(map[uint64]string)
	for id, cset := range assignments {
		res[uint64(id)] = cset.String()
	}
	return res
}

// CountCPUAssignmentCPUs returns sum of cpus among all numas in assignment
func CountCPUAssignmentCPUs(assignment map[int]CPUSet) int {
	res := 0
	for _, v := range assignment {
		res += v.Size()
	}
	return res
}

// ParseCPUAssignmentQuantityMap is used to generate cpu resource counting map
// based on the given CPUSet map
func ParseCPUAssignmentQuantityMap(csetMap map[string]map[int]CPUSet) map[string]map[int]int {
	ret := make(map[string]map[int]int)

	for name, numaToCset := range csetMap {
		ret[name] = make(map[int]int)

		for numaID, cset := range numaToCset {
			ret[name][numaID] = cset.Size()
		}
	}

	return ret
}

// DeepcopyCPUAssignment returns a deep-copied assignments for the given one
func DeepcopyCPUAssignment(assignment map[int]CPUSet) map[int]CPUSet {
	if assignment == nil {
		return nil
	}

	copied := make(map[int]CPUSet)
	for numaNode, cset := range assignment {
		copied[numaNode] = cset.Clone()
	}
	return copied
}

// GetCPUAssignmentNUMAs returns memset for cpuset
func GetCPUAssignmentNUMAs(assignment map[int]CPUSet) CPUSet {
	memset := NewCPUSet()
	for numaID, cpuset := range assignment {
		if cpuset.Size() > 0 {
			memset.Add(numaID)
		}
	}
	return memset
}

// MaskToUInt64Array transforms bit mask to uint slices
func MaskToUInt64Array(mask BitMask) []uint64 {
	maskBits := mask.GetBits()

	maskBitsUint64 := make([]uint64, 0, len(maskBits))
	for _, numaNode := range maskBits {
		maskBitsUint64 = append(maskBitsUint64, uint64(numaNode))
	}

	return maskBitsUint64
}

// GetNICAllocateNUMAs returns numa nodes that are allocated to the given nic
func GetNICAllocateNUMAs(nic InterfaceInfo, info *KatalystMachineInfo) (CPUSet, error) {
	if info == nil {
		return NewCPUSet(), fmt.Errorf("got nil machineInfo")
	}

	sockets, ok := info.IfIndex2Sockets[nic.IfIndex]
	if !ok {
		return NewCPUSet(), fmt.Errorf("nic: %s has no socket", nic.Name)
	}

	return info.CPUDetails.NUMANodesInSockets(sockets...), nil
}

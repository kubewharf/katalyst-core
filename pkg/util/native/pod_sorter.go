// Copyright 2022 The Katalyst Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package native

import (
	"sort"

	v1 "k8s.io/api/core/v1"
	corev1helpers "k8s.io/component-helpers/scheduling/corev1"
)

// CmpFunc compares p1 and p2 and returns:
//
//   -1 if p1 <  p2
//    0 if p1 == p2
//   +1 if p1 >  p2
//
type CmpFunc func(p1, p2 *v1.Pod) int

// MultiPodSorter implements the Sort interface, sorting changes within.
type MultiPodSorter struct {
	pods []*v1.Pod
	cmp  []CmpFunc
}

// NewMultiSorter returns a Sorter that sorts using the cmp functions, in order.
// Call its Sort method to sort the data.
func NewMultiSorter(cmp ...CmpFunc) *MultiPodSorter {
	return &MultiPodSorter{
		cmp: cmp,
	}
}

// Sort sorts the argument slice according to the less functions passed to NewMultiSorter.
func (ms *MultiPodSorter) Sort(pods []*v1.Pod) {
	ms.pods = pods
	sort.Sort(ms)
}

// Len is part of sort.Interface.
func (ms *MultiPodSorter) Len() int {
	return len(ms.pods)
}

// Swap is part of sort.Interface.
func (ms *MultiPodSorter) Swap(i, j int) {
	ms.pods[i], ms.pods[j] = ms.pods[j], ms.pods[i]
}

// Less is part of sort.Interface.
func (ms *MultiPodSorter) Less(i, j int) bool {
	p1, p2 := ms.pods[i], ms.pods[j]
	var k int
	for k = 0; k < len(ms.cmp)-1; k++ {
		cmpResult := ms.cmp[k](p1, p2)
		// p1 is less than p2
		if cmpResult < 0 {
			return true
		}
		// p1 is greater than p2
		if cmpResult > 0 {
			return false
		}
		// we don't know yet
	}
	// the last cmp func is the final decider
	return ms.cmp[k](p1, p2) < 0
}

// PodCPURequestCmpFunc is to compare two pods cpu request, placing smaller before greater
func PodCPURequestCmpFunc(p1, p2 *v1.Pod) int {
	p1Request := SumUpPodRequestResources(p1)
	p2Request := SumUpPodRequestResources(p2)

	p1CPUQuantity := GetCPUQuantity(p1Request)
	p2CPUQuantity := GetCPUQuantity(p2Request)

	return p1CPUQuantity.Cmp(p2CPUQuantity)
}

// PodPriorityCmpFunc compares two priority of pods, placing higher priority before lower one if reverse is true
func PodPriorityCmpFunc(p1, p2 *v1.Pod) int {
	priority1 := corev1helpers.PodPriority(p1)
	priority2 := corev1helpers.PodPriority(p2)

	return CmpInt32(priority1, priority2)
}

// ReverseCmpFunc reverse cmp func
func ReverseCmpFunc(cmpFunc CmpFunc) CmpFunc {
	return func(p1, p2 *v1.Pod) int {
		return -cmpFunc(p1, p2)
	}
}

// CmpBool compares booleans, placing true before false
func CmpBool(a, b bool) int {
	if a == b {
		return 0
	}
	if !b {
		return -1
	}
	return 1
}

// CmpError compares errors, placing not nil before nil
func CmpError(err1, err2 error) int {
	if err1 != nil && err2 != nil {
		return 0
	}
	if err1 != nil {
		return -1
	}
	return 1
}

// CmpFloat64 compares float64s, placing greater before smaller
func CmpFloat64(a, b float64) int {
	if a == b {
		return 0
	}
	if a < b {
		return 1
	}
	return -1
}

// CmpInt32 compares int32s, placing greater before smaller
func CmpInt32(a, b int32) int {
	if a == b {
		return 0
	}
	if a < b {
		return 1
	}
	return -1
}

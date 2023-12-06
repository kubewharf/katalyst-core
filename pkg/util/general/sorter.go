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

package general

import (
	"sort"
)

// CmpFunc compares object-1 and object-2 and returns:
//
//   -1 if object-1 <  object-2
//    0 if object-1 == object-2
//   +1 if object-1 >  object-2
//

type CmpFunc func(i1, i2 interface{}) int

type SourceList interface {
	Len() int
	GetSource(index int) interface{}
	SetSource(index int, s interface{})
}

// MultiSorter implements the Sort interface, sorting changes within.
type MultiSorter struct {
	cmp []CmpFunc
}

// NewMultiSorter returns a Sorter that sorts using the cmp functions, in order.
// Call its Sort method to sort the data.
func NewMultiSorter(cmp ...CmpFunc) *MultiSorter {
	return &MultiSorter{
		cmp: cmp,
	}
}

// Sort sorts the argument slice according to the Less functions passed to NewMultiSorter.
func (ms *MultiSorter) Sort(sources SourceList) {
	sort.Sort(&sortableSourceList{
		sources: sources,
		cmp:     ms.cmp,
	})
}

type sortableSourceList struct {
	sources SourceList
	cmp     []CmpFunc
}

// Len is part of sort.Interface.
func (ms *sortableSourceList) Len() int {
	return ms.sources.Len()
}

// Swap is part of sort.Interface.
func (ms *sortableSourceList) Swap(i, j int) {
	si, sj := ms.sources.GetSource(i), ms.sources.GetSource(j)
	ms.sources.SetSource(i, sj)
	ms.sources.SetSource(j, si)
}

// Less is part of sort.Interface.
func (ms *sortableSourceList) Less(i, j int) bool {
	s1, s2 := ms.sources.GetSource(i), ms.sources.GetSource(j)
	var k int
	for k = 0; k < len(ms.cmp)-1; k++ {
		cmpResult := ms.cmp[k](s1, s2)
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
	return ms.cmp[k](s1, s2) < 0
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

// CmpString compares strings, placing greater before smaller
func CmpString(a, b string) int {
	if a == b {
		return 0
	}

	if a < b {
		return 1
	}

	return -1
}

func ReverseCmpFunc(cmpFunc CmpFunc) CmpFunc {
	return func(i1, i2 interface{}) int {
		return -cmpFunc(i1, i2)
	}
}

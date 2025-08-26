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
	"bytes"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"k8s.io/klog/v2"
)

type CPUSet struct {
	// nil elems and empty elems both will be unmarshal to
	// empty elems, so we must use Initialed property to identify them
	Initialed bool
	elems     map[int]struct{}
}

func NewCPUSet(cpus ...int) CPUSet {
	cs := CPUSet{true, make(map[int]struct{})}
	cs.Add(cpus...)

	return cs
}

func NewCPUSetInt64(cpus ...int64) (CPUSet, error) {
	cs := CPUSet{true, make(map[int]struct{})}
	err := cs.AddInt64(cpus...)
	if err != nil {
		return cs, err
	}

	return cs, nil
}

func NewCPUSetUint64(cpus ...uint64) (CPUSet, error) {
	cs := CPUSet{true, make(map[int]struct{})}
	err := cs.AddUint64(cpus...)
	if err != nil {
		return cs, err
	}

	return cs, nil
}

func (s CPUSet) Clone() CPUSet {
	s2 := NewCPUSet()
	for elem := range s.elems {
		s2.Add(elem)
	}
	return s2
}

func (s *CPUSet) UnmarshalJSON(b []byte) error {
	if len(b) < 2 || b[0] != byte('"') || b[len(b)-1] != byte('"') {
		return fmt.Errorf("invalid cpuset string")
	}

	b = b[1 : len(b)-1]
	if string(b) == "nil" {
		*s = CPUSet{}
		return nil
	}

	cs, err := Parse(string(b))
	if err != nil {
		return err
	}

	*s = cs
	return nil
}

func (s CPUSet) MarshalJSON() ([]byte, error) {
	if s.Initialed {
		return []byte(fmt.Sprintf(`"%s"`, s.String())), nil
	} else {
		return []byte(`"nil"`), nil
	}
}

// Add adds the supplied elements to the result.
func (s CPUSet) Add(elems ...int) {
	for _, elem := range elems {
		s.elems[elem] = struct{}{}
	}
}

// AddInt64 adds the supplied int64 elements to the result.
func (s CPUSet) AddInt64(elems ...int64) error {
	for _, elem := range elems {
		elemInt := int(elem)

		if elemInt < 0 || int64(elemInt) != elem {
			return fmt.Errorf("parse elem: %d to int failed", elem)
		}

		s.Add(elemInt)
	}

	return nil
}

// AddUint64 adds the supplied uint64 elements to the result.
func (s CPUSet) AddUint64(elems ...uint64) error {
	for _, elem := range elems {
		elemInt := int(elem)

		if elemInt < 0 || uint64(elemInt) != elem {
			return fmt.Errorf("parse elem: %d to int failed", elem)
		}

		s.Add(elemInt)
	}

	return nil
}

// Size returns the number of elements in this set.
func (s CPUSet) Size() int {
	return len(s.elems)
}

// IsEmpty returns true if there are zero elements in this set.
func (s CPUSet) IsEmpty() bool {
	return s.Size() == 0
}

// Contains returns true if the supplied element is present in this set.
func (s CPUSet) Contains(cpu int) bool {
	_, found := s.elems[cpu]
	return found
}

// Equals returns true if the supplied set contains exactly the same elements
// as this set (s IsSubsetOf s2 and s2 IsSubsetOf s).
func (s CPUSet) Equals(s2 CPUSet) bool {
	return reflect.DeepEqual(s.elems, s2.elems)
}

// Filter returns a new CPU set that contains all elements from this
// set that match the supplied predicate, without mutating the source set.
func (s CPUSet) Filter(predicate func(int) bool) CPUSet {
	s2 := NewCPUSet()
	for cpu := range s.elems {
		if predicate(cpu) {
			s2.Add(cpu)
		}
	}
	return s2
}

// FilterNot returns a new CPU set that contains all elements from this
// set that do not match the supplied predicate, without mutating the source set.
func (s CPUSet) FilterNot(predicate func(int) bool) CPUSet {
	s2 := NewCPUSet()
	for cpu := range s.elems {
		if !predicate(cpu) {
			s2.Add(cpu)
		}
	}
	return s2
}

// IsSubsetOf returns true if the supplied set contains all the elements
func (s CPUSet) IsSubsetOf(s2 CPUSet) bool {
	result := true
	for cpu := range s.elems {
		if !s2.Contains(cpu) {
			result = false
			break
		}
	}
	return result
}

// Union returns a new CPU set that contains all elements from this
// set and all elements from the supplied set, without mutating either source set.
func (s CPUSet) Union(s2 CPUSet) CPUSet {
	s3 := NewCPUSet()
	for cpu := range s.elems {
		s3.Add(cpu)
	}
	for cpu := range s2.elems {
		s3.Add(cpu)
	}
	return s3
}

// UnionAll returns a new CPU set that contains all elements from this
// set and all elements from the supplied sets, without mutating either source set.
func (s CPUSet) UnionAll(s2 []CPUSet) CPUSet {
	s3 := NewCPUSet()
	for cpu := range s.elems {
		s3.Add(cpu)
	}
	for _, cs := range s2 {
		for cpu := range cs.elems {
			s3.Add(cpu)
		}
	}
	return s3
}

// Intersection returns a new CPU set that contains all of the elements
// that are present in both this set and the supplied set, without mutating
// either source set.
func (s CPUSet) Intersection(s2 CPUSet) CPUSet {
	return s.Filter(func(cpu int) bool { return s2.Contains(cpu) })
}

// Difference returns a new CPU set that contains all of the elements that
// are present in this set and not the supplied set, without mutating either
// source set.
func (s CPUSet) Difference(s2 CPUSet) CPUSet {
	return s.FilterNot(func(cpu int) bool { return s2.Contains(cpu) })
}

// ToSliceInt returns an ordered slice of int that contains
// all elements from this set
func (s CPUSet) ToSliceInt() []int {
	result := []int{}
	for cpu := range s.elems {
		result = append(result, cpu)
	}
	sort.Ints(result)
	return result
}

// ToSliceIntReversely returns a reverse ordered slice of int that contains
// all elements from this set
func (s CPUSet) ToSliceIntReversely() []int {
	result := []int{}
	for cpu := range s.elems {
		result = append(result, cpu)
	}
	sort.Sort(sort.Reverse(sort.IntSlice(result)))
	return result
}

// ToSliceInt64 returns an ordered slice of int64 that contains
// all elements from this set
func (s CPUSet) ToSliceInt64() []int64 {
	result := []int64{}
	for cpu := range s.elems {
		result = append(result, int64(cpu))
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result
}

// ToSliceUInt64 returns an ordered slice of uint64 that contains
// all elements from this set
func (s CPUSet) ToSliceUInt64() []uint64 {
	result := []uint64{}
	for cpu := range s.elems {
		result = append(result, uint64(cpu))
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result
}

// ToSliceNoSortInt returns an ordered slice of int that contains
// all elements from this set
func (s CPUSet) ToSliceNoSortInt() []int {
	result := []int{}
	for cpu := range s.elems {
		result = append(result, cpu)
	}
	return result
}

// ToSliceNoSortInt64 returns an ordered slice of int64 that contains
// all elements from this set
func (s CPUSet) ToSliceNoSortInt64() []int64 {
	result := []int64{}
	for cpu := range s.elems {
		result = append(result, int64(cpu))
	}
	return result
}

// ToSliceNoSortUInt64 returns an ordered slice of uint64 that contains
// all elements from this set
func (s CPUSet) ToSliceNoSortUInt64() []uint64 {
	result := []uint64{}
	for cpu := range s.elems {
		result = append(result, uint64(cpu))
	}
	return result
}

// String returns a new string representation of the elements in this CPU set
// in canonical linux CPU list format.
//
// See: http://man7.org/linux/man-pages/man7/cpuset.7.html#FORMATS
func (s CPUSet) String() string {
	if s.IsEmpty() {
		return ""
	}

	elems := s.ToSliceInt()
	type rng struct {
		start int
		end   int
	}

	ranges := []rng{{elems[0], elems[0]}}
	for i := 1; i < len(elems); i++ {
		lastRange := &ranges[len(ranges)-1]
		// if this element is adjacent to the high end of the last range
		if elems[i] == lastRange.end+1 {
			// then extend the last range to include this element
			lastRange.end = elems[i]
			continue
		}
		// otherwise, start a new range beginning with this element
		ranges = append(ranges, rng{elems[i], elems[i]})
	}

	// construct string from ranges
	var result bytes.Buffer
	for _, r := range ranges {
		if r.start == r.end {
			result.WriteString(strconv.Itoa(r.start))
		} else {
			result.WriteString(fmt.Sprintf("%d-%d", r.start, r.end))
		}
		result.WriteString(",")
	}
	return strings.TrimRight(result.String(), ",")
}

// MustParse CPUSet constructs a new CPU set from a Linux CPU list formatted
// string. Unlike Parse, it does not return an error but rather panics if the
// input cannot be used to construct a CPU set.
func MustParse(s string) CPUSet {
	res, err := Parse(s)
	if err != nil {
		klog.Fatalf("unable to parse [%s] as CPUSet: %v", s, err)
	}
	return res
}

// Parse CPUSet constructs a new CPU set from a Linux CPU list formatted string.
//
// See: http://man7.org/linux/man-pages/man7/cpuset.7.html#FORMATS
func Parse(s string) (CPUSet, error) {
	s2 := NewCPUSet()

	// Handle empty string.
	if s == "" {
		return s2, nil
	}

	// Split CPU list string:
	// "0-5,34,46-48 => ["0-5", "34", "46-48"]
	ranges := strings.Split(s, ",")

	for _, r := range ranges {
		boundaries := strings.Split(r, "-")
		if len(boundaries) == 1 {
			// Handle ranges that consist of only one element like "34".
			elem, err := strconv.Atoi(boundaries[0])
			if err != nil {
				return NewCPUSet(), err
			}
			s2.Add(elem)
		} else if len(boundaries) == 2 {
			// Handle multi-element ranges like "0-5".
			start, err := strconv.Atoi(boundaries[0])
			if err != nil {
				return NewCPUSet(), err
			}
			end, err := strconv.Atoi(boundaries[1])
			if err != nil {
				return NewCPUSet(), err
			}
			// Add all elements to the result.
			// e.g. "0-5", "46-48" => [0, 1, 2, 3, 4, 5, 46, 47, 48].
			for e := start; e <= end; e++ {
				s2.Add(e)
			}
		}
	}
	return s2, nil
}

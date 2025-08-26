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
	"encoding/json"
	"testing"
)

func TestNewCPUSet(t *testing.T) {
	cs := NewCPUSet(1, 2, 3)
	if cs.Size() != 3 {
		t.Errorf("expected size 3, got %d", cs.Size())
	}
	if !cs.Contains(1) || !cs.Contains(2) || !cs.Contains(3) {
		t.Errorf("expected set to contain 1, 2, 3")
	}
}

func TestNewCPUSetInt64(t *testing.T) {
	cs, err := NewCPUSetInt64(1, 2, 3)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if cs.Size() != 3 {
		t.Errorf("expected size 3, got %d", cs.Size())
	}

	_, err = NewCPUSetInt64(-1)
	if err == nil {
		t.Error("expected error for negative value")
	}
}

func TestNewCPUSetUint64(t *testing.T) {
	cs, err := NewCPUSetUint64(1, 2, 3)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if cs.Size() != 3 {
		t.Errorf("expected size 3, got %d", cs.Size())
	}

	//
	_, err = NewCPUSetUint64(18446744073709551615)
	if err == nil {
		t.Error("expected error for value exceeding int range")
	}
}

func TestCPUSet_Clone(t *testing.T) {
	cs := NewCPUSet(1, 2, 3)
	clone := cs.Clone()

	if !cs.Equals(clone) {
		t.Error("clone should be equal to original")
	}

	clone.Add(4)
	if cs.Contains(4) {
		t.Error("original set should not be modified when clone is modified")
	}
}

func TestCPUSet_MarshalUnmarshalJSON(t *testing.T) {
	cs := NewCPUSet(1, 2, 3)
	jsonData, err := json.Marshal(cs)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}

	var cs2 CPUSet
	if err := json.Unmarshal(jsonData, &cs2); err != nil {
		t.Errorf("unexpected error: %v", err)
	}

	if !cs.Equals(cs2) {
		t.Error("unmarshaled set should be equal to original")
	}

	emptyCS := NewCPUSet()
	jsonData, err = json.Marshal(emptyCS)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}

	var emptyCS2 CPUSet
	if err := json.Unmarshal(jsonData, &emptyCS2); err != nil {
		t.Errorf("unexpected error: %v", err)
	}

	if !emptyCS.Equals(emptyCS2) {
		t.Error("unmarshaled empty set should be equal to original")
	}

	nilCSJSON := []byte(`"nil"`)
	var nilCS CPUSet
	if err := json.Unmarshal(nilCSJSON, &nilCS); err != nil {
		t.Errorf("unexpected error: %v", err)
	}

	if nilCS.Initialed {
		t.Error("unmarshaled nil set should not be initialized")
	}
}

func TestCPUSet_Size(t *testing.T) {
	cs := NewCPUSet(1, 2, 3)
	if cs.Size() != 3 {
		t.Errorf("expected size 3, got %d", cs.Size())
	}

	cs.Add(4)
	if cs.Size() != 4 {
		t.Errorf("expected size 4 after adding element, got %d", cs.Size())
	}
}

func TestCPUSet_IsEmpty(t *testing.T) {
	emptyCS := NewCPUSet()
	if !emptyCS.IsEmpty() {
		t.Error("new CPUSet should be empty")
	}

	cs := NewCPUSet(1)
	if cs.IsEmpty() {
		t.Error("CPUSet with elements should not be empty")
	}
}

func TestCPUSet_Contains(t *testing.T) {
	cs := NewCPUSet(1, 2, 3)
	if !cs.Contains(1) || !cs.Contains(2) || !cs.Contains(3) {
		t.Error("set should contain added elements")
	}

	if cs.Contains(4) {
		t.Error("set should not contain element not added")
	}
}

func TestCPUSet_Equals(t *testing.T) {
	cs1 := NewCPUSet(1, 2, 3)
	cs2 := NewCPUSet(3, 2, 1)
	cs3 := NewCPUSet(1, 2, 4)

	if !cs1.Equals(cs2) {
		t.Error("sets with same elements in different order should be equal")
	}

	if cs1.Equals(cs3) {
		t.Error("sets with different elements should not be equal")
	}
}

func TestCPUSet_Filter(t *testing.T) {
	cs := NewCPUSet(1, 2, 3, 4, 5)
	evenCS := cs.Filter(func(cpu int) bool { return cpu%2 == 0 })

	if evenCS.Size() != 2 {
		t.Errorf("expected size 2 for even filter, got %d", evenCS.Size())
	}
	if !evenCS.Contains(2) || !evenCS.Contains(4) {
		t.Error("even filter should include 2 and 4")
	}
}

func TestCPUSet_FilterNot(t *testing.T) {
	cs := NewCPUSet(1, 2, 3, 4, 5)
	oddCS := cs.FilterNot(func(cpu int) bool { return cpu%2 == 0 })

	if oddCS.Size() != 3 {
		t.Errorf("expected size 3 for odd filter, got %d", oddCS.Size())
	}
	if !oddCS.Contains(1) || !oddCS.Contains(3) || !oddCS.Contains(5) {
		t.Error("odd filter should include 1, 3, and 5")
	}
}

func TestCPUSet_IsSubsetOf(t *testing.T) {
	cs1 := NewCPUSet(1, 2)
	cs2 := NewCPUSet(1, 2, 3)
	cs3 := NewCPUSet(3, 4)

	if !cs1.IsSubsetOf(cs2) {
		t.Error("{1,2} should be subset of {1,2,3}")
	}

	if cs2.IsSubsetOf(cs1) {
		t.Error("{1,2,3} should not be subset of {1,2}")
	}

	if cs1.IsSubsetOf(cs3) {
		t.Error("{1,2} should not be subset of {3,4}")
	}
}

func TestCPUSet_Union(t *testing.T) {
	cs1 := NewCPUSet(1, 2)
	cs2 := NewCPUSet(3, 4)
	union := cs1.Union(cs2)

	if union.Size() != 4 {
		t.Errorf("expected size 4 for union, got %d", union.Size())
	}
	if !union.Contains(1) || !union.Contains(2) || !union.Contains(3) || !union.Contains(4) {
		t.Error("union should contain all elements from both sets")
	}
}

func TestCPUSet_Intersection(t *testing.T) {
	cs1 := NewCPUSet(1, 2, 3)
	cs2 := NewCPUSet(2, 3, 4)
	intersection := cs1.Intersection(cs2)

	if intersection.Size() != 2 {
		t.Errorf("expected size 2 for intersection, got %d", intersection.Size())
	}
	if !intersection.Contains(2) || !intersection.Contains(3) {
		t.Error("intersection should contain elements present in both sets")
	}
}

func TestCPUSet_Difference(t *testing.T) {
	cs1 := NewCPUSet(1, 2, 3)
	cs2 := NewCPUSet(2, 3, 4)
	difference := cs1.Difference(cs2)

	if difference.Size() != 1 {
		t.Errorf("expected size 1 for difference, got %d", difference.Size())
	}
	if !difference.Contains(1) {
		t.Error("difference should contain elements in first set but not in second set")
	}
}

func TestCPUSet_ToSliceInt(t *testing.T) {
	cs := NewCPUSet(3, 1, 2)
	slice := cs.ToSliceInt()

	if len(slice) != 3 {
		t.Errorf("expected slice length 3, got %d", len(slice))
	}
	if slice[0] != 1 || slice[1] != 2 || slice[2] != 3 {
		t.Error("slice should be sorted")
	}
}

func TestCPUSet_ToSliceIntReversely(t *testing.T) {
	cs := NewCPUSet(3, 1, 2)
	slice := cs.ToSliceIntReversely()

	if len(slice) != 3 {
		t.Errorf("expected slice length 3, got %d", len(slice))
	}
	if slice[0] != 3 || slice[1] != 2 || slice[2] != 1 {
		t.Error("slice should be sorted in reverse order")
	}
}

func TestCPUSet_String(t *testing.T) {
	cs1 := NewCPUSet(1)
	if cs1.String() != "1" {
		t.Errorf("expected string \"1\", got \"%s\"", cs1.String())
	}

	cs2 := NewCPUSet(1, 2, 3)
	if cs2.String() != "1-3" {
		t.Errorf("expected string \"1-3\", got \"%s\"", cs2.String())
	}

	cs3 := NewCPUSet(1, 3, 5)
	if cs3.String() != "1,3,5" {
		t.Errorf("expected string \"1,3,5\", got \"%s\"", cs3.String())
	}

	cs4 := NewCPUSet(1, 2, 3, 5, 6)
	if cs4.String() != "1-3,5-6" {
		t.Errorf("expected string \"1-3,5-6\", got \"%s\"", cs4.String())
	}

	cs5 := NewCPUSet()
	if cs5.String() != "" {
		t.Errorf("expected empty string, got \"%s\"", cs5.String())
	}
}

func TestParse(t *testing.T) {
	cs1, err := Parse("1")
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if !cs1.Equals(NewCPUSet(1)) {
		t.Error("parsed set should equal NewCPUSet(1)")
	}

	cs2, err := Parse("1-3")
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if !cs2.Equals(NewCPUSet(1, 2, 3)) {
		t.Error("parsed set should equal NewCPUSet(1, 2, 3)")
	}

	cs3, err := Parse("1-3,5,7-9")
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if !cs3.Equals(NewCPUSet(1, 2, 3, 5, 7, 8, 9)) {
		t.Error("parsed set should equal expected set")
	}

	cs4, err := Parse("")
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if !cs4.Equals(NewCPUSet()) {
		t.Error("parsed empty string should equal NewCPUSet()")
	}

	invalidFormats := []string{
		"abc",
		"1,2,3,",
		"1..3",
	}

	for _, format := range invalidFormats {
		_, err = Parse(format)
		if err == nil {
			t.Errorf("expected error for invalid format '%s'", format)
		}
	}
}

func TestMustParse(t *testing.T) {
	cs := MustParse("1-3")
	if !cs.Equals(NewCPUSet(1, 2, 3)) {
		t.Error("MustParse should return correct set")
	}
}

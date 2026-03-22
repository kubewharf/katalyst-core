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

package resourcepackage

import (
	"reflect"
	"testing"

	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/sets"

	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

type mockResourcePackageState struct {
	attributes   map[string]string
	pinnedCPUSet machine.CPUSet
}

func (m *mockResourcePackageState) GetAttributes() map[string]string {
	if m == nil {
		return nil
	}
	return m.attributes
}

func (m *mockResourcePackageState) GetPinnedCPUSet() machine.CPUSet {
	if m == nil {
		return machine.NewCPUSet()
	}
	return m.pinnedCPUSet
}

func TestGetMatchedPinnedCPUSet(t *testing.T) {
	t.Parallel()
	states := map[string]*mockResourcePackageState{
		"pkg1": {
			attributes:   map[string]string{"disable-reclaim": "true"},
			pinnedCPUSet: machine.NewCPUSet(1, 2),
		},
		"pkg2": {
			attributes:   map[string]string{"disable-reclaim": "false"},
			pinnedCPUSet: machine.NewCPUSet(3, 4),
		},
		"pkg3": nil,
	}

	selector, _ := labels.Parse("disable-reclaim=true")

	res := GetMatchedPinnedCPUSet(states, selector)
	if !reflect.DeepEqual(res.ToSliceInt(), []int{1, 2}) {
		t.Errorf("expected [1, 2], got %v", res.ToSliceInt())
	}

	resEmpty := GetMatchedPinnedCPUSet(states, nil)
	if resEmpty.Size() != 0 {
		t.Errorf("expected empty cpuset, got %v", resEmpty.ToSliceInt())
	}
}

func TestGetNUMAMatchedPinnedCPUSet(t *testing.T) {
	t.Parallel()
	numaStates := map[int]map[string]*mockResourcePackageState{
		0: {
			"pkg1": {
				attributes:   map[string]string{"disable-reclaim": "true"},
				pinnedCPUSet: machine.NewCPUSet(1, 2),
			},
		},
		1: {
			"pkg2": {
				attributes:   map[string]string{"disable-reclaim": "true"},
				pinnedCPUSet: machine.NewCPUSet(3, 4),
			},
		},
	}

	selector, _ := labels.Parse("disable-reclaim=true")
	res := GetNUMAMatchedPinnedCPUSet(numaStates, selector)

	if !reflect.DeepEqual(res[0].ToSliceInt(), []int{1, 2}) {
		t.Errorf("expected [1, 2] for NUMA 0, got %v", res[0].ToSliceInt())
	}
	if !reflect.DeepEqual(res[1].ToSliceInt(), []int{3, 4}) {
		t.Errorf("expected [3, 4] for NUMA 1, got %v", res[1].ToSliceInt())
	}
}

func TestGetMatchedPackages(t *testing.T) {
	t.Parallel()
	states := map[string]*mockResourcePackageState{
		"pkg1": {
			attributes: map[string]string{"disable-reclaim": "true"},
		},
		"pkg2": {
			attributes: map[string]string{"disable-reclaim": "false"},
		},
		"pkg3": nil,
	}

	selector, _ := labels.Parse("disable-reclaim=true")
	res := GetMatchedPackages(states, selector)

	expected := sets.NewString("pkg1")
	if !res.Equal(expected) {
		t.Errorf("expected %v, got %v", expected, res)
	}
}

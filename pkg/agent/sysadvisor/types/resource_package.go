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

package types

import "github.com/kubewharf/katalyst-core/pkg/util/machine"

// ResourcePackageState stores the state of a resource package on a specific NUMA node.
type ResourcePackageState struct {
	PinnedCPUSet machine.CPUSet
	Attributes   map[string]string
}

func (s *ResourcePackageState) GetAttributes() map[string]string {
	if s == nil {
		return nil
	}
	return s.Attributes
}

func (s *ResourcePackageState) GetPinnedCPUSet() machine.CPUSet {
	if s == nil {
		return machine.NewCPUSet()
	}
	return s.PinnedCPUSet
}

// Clone returns a deep copy of ResourcePackageState.
func (s *ResourcePackageState) Clone() *ResourcePackageState {
	if s == nil {
		return nil
	}
	clone := &ResourcePackageState{
		PinnedCPUSet: s.PinnedCPUSet.Clone(),
	}
	if s.Attributes != nil {
		clone.Attributes = make(map[string]string, len(s.Attributes))
		for k, v := range s.Attributes {
			clone.Attributes[k] = v
		}
	}
	return clone
}

// ResourcePackageConfig stores resource package related configurations organized by NUMA node.
// It is used as an in-memory snapshot in sysadvisor metacache, and is expected to be deep-copied
// when being read/written across module boundaries.
//
// Key format:
// - first key: NUMA id
// - second key: resource package name
// - value: state for the resource package on that NUMA node
type ResourcePackageConfig map[int]map[string]*ResourcePackageState

// Clone returns a deep copy of ResourcePackageConfig.
func (c ResourcePackageConfig) Clone() ResourcePackageConfig {
	if c == nil {
		return nil
	}

	out := make(ResourcePackageConfig, len(c))
	for numaID, pkgMap := range c {
		if pkgMap == nil {
			out[numaID] = nil
			continue
		}

		outPkgMap := make(map[string]*ResourcePackageState, len(pkgMap))
		for pkgName, state := range pkgMap {
			outPkgMap[pkgName] = state.Clone()
		}
		out[numaID] = outPkgMap
	}
	return out
}

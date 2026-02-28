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

// ResourcePackageConfig stores resource package related configurations organized by NUMA node.
// It is used as an in-memory snapshot in sysadvisor metacache, and is expected to be deep-copied
// when being read/written across module boundaries.
//
// Key format:
// - first key: NUMA id
// - second key: resource package name
// - value: pinned CPUSet for the resource package on that NUMA node
type ResourcePackageConfig map[int]map[string]machine.CPUSet

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

		outPkgMap := make(map[string]machine.CPUSet, len(pkgMap))
		for pkgName, cpuset := range pkgMap {
			outPkgMap[pkgName] = cpuset.Clone()
		}
		out[numaID] = outPkgMap
	}
	return out
}

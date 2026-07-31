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

package planner

import "github.com/kubewharf/katalyst-core/pkg/util/machine"

type ResourcePackageDomainCache struct {
	pinnedUnion   map[int]machine.CPUSet
	packageDomain map[int]map[string]machine.CPUSet
	commonDomain  map[int]machine.CPUSet
	revision      uint64
}

func BuildResourcePackageDomainCache(
	eligible map[int]machine.CPUSet,
	pinned map[int]map[string]machine.CPUSet,
	revision uint64,
) *ResourcePackageDomainCache {
	cache := &ResourcePackageDomainCache{
		pinnedUnion:   make(map[int]machine.CPUSet, len(eligible)),
		packageDomain: make(map[int]map[string]machine.CPUSet, len(eligible)),
		commonDomain:  make(map[int]machine.CPUSet, len(eligible)),
		revision:      revision,
	}

	for numaID, domain := range eligible {
		eligibleDomain := domain.Clone()
		union := machine.NewCPUSet()
		cache.packageDomain[numaID] = make(map[string]machine.CPUSet)

		for pkgName, pkgSet := range pinned[numaID] {
			pkgDomain := eligibleDomain.Intersection(pkgSet.Clone())
			cache.packageDomain[numaID][pkgName] = pkgDomain
			union = union.Union(pkgDomain)
		}

		cache.pinnedUnion[numaID] = union
		cache.commonDomain[numaID] = eligibleDomain.Difference(union)
	}

	return cache
}

func (c *ResourcePackageDomainCache) Revision() uint64 {
	if c == nil {
		return 0
	}

	return c.revision
}

func (c *ResourcePackageDomainCache) PinnedUnion(numaID int) machine.CPUSet {
	if c == nil {
		return machine.NewCPUSet()
	}

	return cloneCPUSet(c.pinnedUnion[numaID])
}

func (c *ResourcePackageDomainCache) PackageDomain(numaID int, pkg string) machine.CPUSet {
	if c == nil {
		return machine.NewCPUSet()
	}

	return cloneCPUSet(c.packageDomain[numaID][pkg])
}

func (c *ResourcePackageDomainCache) CommonDomain(numaID int) machine.CPUSet {
	if c == nil {
		return machine.NewCPUSet()
	}

	return cloneCPUSet(c.commonDomain[numaID])
}

func cloneCPUSet(set machine.CPUSet) machine.CPUSet {
	return set.Clone()
}

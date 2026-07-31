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

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

func TestBuildResourcePackageDomainCache(t *testing.T) {
	t.Parallel()

	t.Run("separates pinned packages from common domain", func(t *testing.T) {
		t.Parallel()

		eligible := map[int]machine.CPUSet{
			0: machine.NewCPUSet(0, 1, 2, 3, 4, 5, 6, 7),
		}
		pinned := map[int]map[string]machine.CPUSet{
			0: {
				"pkg-a": machine.NewCPUSet(0, 1),
				"pkg-b": machine.NewCPUSet(2, 3),
			},
		}

		cache := BuildResourcePackageDomainCache(eligible, pinned, 7)

		require.Equal(t, machine.NewCPUSet(0, 1, 2, 3), cache.PinnedUnion(0))
		require.Equal(t, machine.NewCPUSet(0, 1), cache.PackageDomain(0, "pkg-a"))
		require.Equal(t, machine.NewCPUSet(2, 3), cache.PackageDomain(0, "pkg-b"))
		require.Equal(t, machine.NewCPUSet(4, 5, 6, 7), cache.CommonDomain(0))
		require.Equal(t, uint64(7), cache.Revision())
	})

	t.Run("intersects package domain with eligible cpuset", func(t *testing.T) {
		t.Parallel()

		eligible := map[int]machine.CPUSet{
			0: machine.NewCPUSet(0, 1, 2, 3),
		}
		pinned := map[int]map[string]machine.CPUSet{
			0: {
				"pkg-a": machine.NewCPUSet(0, 1, 8, 9),
			},
		}

		cache := BuildResourcePackageDomainCache(eligible, pinned, 8)

		require.Equal(t, machine.NewCPUSet(0, 1), cache.PinnedUnion(0))
		require.Equal(t, machine.NewCPUSet(0, 1), cache.PackageDomain(0, "pkg-a"))
		require.Equal(t, machine.NewCPUSet(2, 3), cache.CommonDomain(0))
		require.Equal(t, uint64(8), cache.Revision())
	})

	t.Run("clones input cpusets", func(t *testing.T) {
		t.Parallel()

		eligibleSet := machine.NewCPUSet(0, 1, 2, 3)
		pinnedSet := machine.NewCPUSet(0, 1)
		eligible := map[int]machine.CPUSet{
			0: eligibleSet,
		}
		pinned := map[int]map[string]machine.CPUSet{
			0: {
				"pkg-a": pinnedSet,
			},
		}

		cache := BuildResourcePackageDomainCache(eligible, pinned, 9)
		eligibleSet.Add(4)
		pinnedSet.Add(2)
		eligible[0].Add(5)
		pinned[0]["pkg-a"].Add(3)

		require.Equal(t, machine.NewCPUSet(0, 1), cache.PinnedUnion(0))
		require.Equal(t, machine.NewCPUSet(0, 1), cache.PackageDomain(0, "pkg-a"))
		require.Equal(t, machine.NewCPUSet(2, 3), cache.CommonDomain(0))
		require.Equal(t, uint64(9), cache.Revision())
	})

	t.Run("handles nil pinned map", func(t *testing.T) {
		t.Parallel()

		eligible := map[int]machine.CPUSet{
			0: machine.NewCPUSet(0, 1),
		}

		cache := BuildResourcePackageDomainCache(eligible, nil, 10)

		require.Equal(t, machine.NewCPUSet(), cache.PinnedUnion(0))
		require.Equal(t, machine.NewCPUSet(), cache.PackageDomain(0, "pkg-a"))
		require.Equal(t, machine.NewCPUSet(0, 1), cache.CommonDomain(0))
		require.Equal(t, uint64(10), cache.Revision())
	})

	t.Run("handles missing pinned numa", func(t *testing.T) {
		t.Parallel()

		eligible := map[int]machine.CPUSet{
			0: machine.NewCPUSet(0, 1),
			1: machine.NewCPUSet(2, 3),
		}
		pinned := map[int]map[string]machine.CPUSet{
			0: {
				"pkg-a": machine.NewCPUSet(0),
			},
		}

		cache := BuildResourcePackageDomainCache(eligible, pinned, 11)

		require.Equal(t, machine.NewCPUSet(0), cache.PinnedUnion(0))
		require.Equal(t, machine.NewCPUSet(0), cache.PackageDomain(0, "pkg-a"))
		require.Equal(t, machine.NewCPUSet(1), cache.CommonDomain(0))
		require.Equal(t, machine.NewCPUSet(), cache.PinnedUnion(1))
		require.Equal(t, machine.NewCPUSet(), cache.PackageDomain(1, "pkg-a"))
		require.Equal(t, machine.NewCPUSet(2, 3), cache.CommonDomain(1))
		require.Equal(t, uint64(11), cache.Revision())
	})

	t.Run("accessors return cpuset clones", func(t *testing.T) {
		t.Parallel()

		eligible := map[int]machine.CPUSet{
			0: machine.NewCPUSet(0, 1, 2, 3),
		}
		pinned := map[int]map[string]machine.CPUSet{
			0: {
				"pkg-a": machine.NewCPUSet(0),
			},
		}

		cache := BuildResourcePackageDomainCache(eligible, pinned, 12)
		pinnedUnion := cache.PinnedUnion(0)
		packageDomain := cache.PackageDomain(0, "pkg-a")
		commonDomain := cache.CommonDomain(0)

		pinnedUnion.Add(4)
		packageDomain.Add(5)
		commonDomain.Add(6)

		require.Equal(t, machine.NewCPUSet(0), cache.PinnedUnion(0))
		require.Equal(t, machine.NewCPUSet(0), cache.PackageDomain(0, "pkg-a"))
		require.Equal(t, machine.NewCPUSet(1, 2, 3), cache.CommonDomain(0))
		require.Equal(t, uint64(12), cache.Revision())
	})
}

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

package cpumetrics

import (
	"math/rand"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/bulkhead/model"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
	utilmetric "github.com/kubewharf/katalyst-core/pkg/util/metric"
)

func TestAssignPoolLabels(t *testing.T) {
	t.Parallel()

	reclaim := model.CPUSetPoolIdentity{Kind: model.CPUSetPoolKindReclaim}
	share := func(name string) model.CPUSetPoolIdentity {
		return model.CPUSetPoolIdentity{Kind: model.CPUSetPoolKindShare, Name: name}
	}
	dedicated := func(namespace, name string) model.CPUSetPoolIdentity {
		return model.CPUSetPoolIdentity{Kind: model.CPUSetPoolKindDedicated, PodNamespace: namespace, PodName: name}
	}
	isolation := func(namespace, name string) model.CPUSetPoolIdentity {
		return model.CPUSetPoolIdentity{Kind: model.CPUSetPoolKindIsolation, PodNamespace: namespace, PodName: name}
	}
	wantEmptyConflicts := func() map[model.CPUSetPoolKind]machine.CPUSet {
		return map[model.CPUSetPoolKind]machine.CPUSet{
			model.CPUSetPoolKindShare:     machine.NewCPUSet(),
			model.CPUSetPoolKindDedicated: machine.NewCPUSet(),
			model.CPUSetPoolKindIsolation: machine.NewCPUSet(),
		}
	}

	t.Run("assigns fixed formatted pool labels", func(t *testing.T) {
		t.Parallel()

		byIdentity := map[model.CPUSetPoolIdentity]machine.CPUSet{
			reclaim:                               machine.NewCPUSet(0),
			share("batch NUMA0"):                  machine.NewCPUSet(1),
			dedicated("default", "api-pod"):       machine.NewCPUSet(2),
			isolation("kube-system", "agent pod"): machine.NewCPUSet(3),
		}

		got := assignPoolLabels(byIdentity)

		require.Equal(t, []labeledPool{
			{identity: dedicated("default", "api-pod"), label: "dedicated-default/api-pod", cpus: machine.NewCPUSet(2)},
			{identity: isolation("kube-system", "agent pod"), label: "isolation-kube-system/agent_pod", cpus: machine.NewCPUSet(3)},
			{identity: reclaim, label: "reclaim", cpus: machine.NewCPUSet(0)},
			{identity: share("batch NUMA0"), label: "batch_NUMA0", cpus: machine.NewCPUSet(1)},
		}, got.pools)
		require.Equal(t, wantEmptyConflicts(), got.conflictCPUByKind)
	})

	t.Run("omits pod identities missing namespace or name", func(t *testing.T) {
		t.Parallel()

		got := assignPoolLabels(map[model.CPUSetPoolIdentity]machine.CPUSet{
			dedicated("", "pod"):        machine.NewCPUSet(1),
			isolation("default", ""):    machine.NewCPUSet(2),
			dedicated("default", "pod"): machine.NewCPUSet(3),
		})

		require.Equal(t, []labeledPool{
			{identity: dedicated("default", "pod"), label: "dedicated-default/pod", cpus: machine.NewCPUSet(3)},
		}, got.pools)
		require.Equal(t, map[model.CPUSetPoolKind]machine.CPUSet{
			model.CPUSetPoolKindShare:     machine.NewCPUSet(),
			model.CPUSetPoolKindDedicated: machine.NewCPUSet(1),
			model.CPUSetPoolKindIsolation: machine.NewCPUSet(2),
		}, got.conflictCPUByKind)
	})

	t.Run("keeps namespace as part of the pod pool label", func(t *testing.T) {
		t.Parallel()

		got := assignPoolLabels(map[model.CPUSetPoolIdentity]machine.CPUSet{
			dedicated("team-a", "api"): machine.NewCPUSet(1),
			dedicated("team-b", "api"): machine.NewCPUSet(2),
		})

		require.Equal(t, []labeledPool{
			{identity: dedicated("team-a", "api"), label: "dedicated-team-a/api", cpus: machine.NewCPUSet(1)},
			{identity: dedicated("team-b", "api"), label: "dedicated-team-b/api", cpus: machine.NewCPUSet(2)},
		}, got.pools)
		require.Equal(t, wantEmptyConflicts(), got.conflictCPUByKind)
	})

	t.Run("omits fixed pod label colliding with a shared label", func(t *testing.T) {
		t.Parallel()

		got := assignPoolLabels(map[model.CPUSetPoolIdentity]machine.CPUSet{
			share("dedicated-default/pod"):    machine.NewCPUSet(1),
			dedicated("default", "pod"):       machine.NewCPUSet(2),
			dedicated("default", "other-pod"): machine.NewCPUSet(3),
		})

		require.Equal(t, []labeledPool{
			{identity: dedicated("default", "other-pod"), label: "dedicated-default/other-pod", cpus: machine.NewCPUSet(3)},
		}, got.pools)
		require.Equal(t, map[model.CPUSetPoolKind]machine.CPUSet{
			model.CPUSetPoolKindShare:     machine.NewCPUSet(1),
			model.CPUSetPoolKindDedicated: machine.NewCPUSet(2),
			model.CPUSetPoolKindIsolation: machine.NewCPUSet(),
		}, got.conflictCPUByKind)
	})

	t.Run("omits all shared identities whose formatted labels collide", func(t *testing.T) {
		t.Parallel()

		got := assignPoolLabels(map[model.CPUSetPoolIdentity]machine.CPUSet{
			share("batch NUMA0"): machine.NewCPUSet(1, 3),
			share("batch_NUMA0"): machine.NewCPUSet(2, 3),
		})

		require.Empty(t, got.pools)
		require.Equal(t, map[model.CPUSetPoolKind]machine.CPUSet{
			model.CPUSetPoolKindShare:     machine.NewCPUSet(1, 2, 3),
			model.CPUSetPoolKindDedicated: machine.NewCPUSet(),
			model.CPUSetPoolKindIsolation: machine.NewCPUSet(),
		}, got.conflictCPUByKind)
	})

	t.Run("omits shared identities whose long names collide after formatter truncation", func(t *testing.T) {
		t.Parallel()

		common := strings.Repeat("s", utilmetric.MaxTagLength)
		firstName := common + "-first"
		secondName := common + "-second"
		require.Greater(t, len(firstName), utilmetric.MaxTagLength)
		require.Greater(t, len(secondName), utilmetric.MaxTagLength)
		require.Equal(t,
			utilmetric.MetricTagValueFormat(firstName),
			utilmetric.MetricTagValueFormat(secondName))

		got := assignPoolLabels(map[model.CPUSetPoolIdentity]machine.CPUSet{
			share(firstName):  machine.NewCPUSet(1, 3),
			share(secondName): machine.NewCPUSet(2, 3),
		})

		require.Empty(t, got.pools)
		require.Equal(t, map[model.CPUSetPoolKind]machine.CPUSet{
			model.CPUSetPoolKindShare:     machine.NewCPUSet(1, 2, 3),
			model.CPUSetPoolKindDedicated: machine.NewCPUSet(),
			model.CPUSetPoolKindIsolation: machine.NewCPUSet(),
		}, got.conflictCPUByKind)
	})

	t.Run("omits pod identities whose formatted labels collide", func(t *testing.T) {
		t.Parallel()

		common := strings.Repeat("a", utilmetric.MaxTagLength)
		got := assignPoolLabels(map[model.CPUSetPoolIdentity]machine.CPUSet{
			dedicated(common+"1", "pod"): machine.NewCPUSet(4),
			dedicated(common+"2", "pod"): machine.NewCPUSet(5),
		})

		require.Empty(t, got.pools)
		require.Equal(t, map[model.CPUSetPoolKind]machine.CPUSet{
			model.CPUSetPoolKindShare:     machine.NewCPUSet(),
			model.CPUSetPoolKindDedicated: machine.NewCPUSet(4, 5),
			model.CPUSetPoolKindIsolation: machine.NewCPUSet(),
		}, got.conflictCPUByKind)
	})

	t.Run("is deterministic across randomized map insertion order", func(t *testing.T) {
		t.Parallel()

		type entry struct {
			identity model.CPUSetPoolIdentity
			cpus     machine.CPUSet
		}
		entries := []entry{
			{identity: reclaim, cpus: machine.NewCPUSet(0)},
			{identity: share("batch NUMA0"), cpus: machine.NewCPUSet(1)},
			{identity: dedicated("default", "api-b"), cpus: machine.NewCPUSet(2)},
			{identity: dedicated("default", "api-a"), cpus: machine.NewCPUSet(3)},
			{identity: isolation("kube-system", "agent"), cpus: machine.NewCPUSet(4)},
		}
		baselineMap := make(map[model.CPUSetPoolIdentity]machine.CPUSet, len(entries))
		for _, entry := range entries {
			baselineMap[entry.identity] = entry.cpus
		}
		want := assignPoolLabels(baselineMap)
		random := rand.New(rand.NewSource(42))

		for iteration := 0; iteration < 50; iteration++ {
			random.Shuffle(len(entries), func(i, j int) {
				entries[i], entries[j] = entries[j], entries[i]
			})
			shuffled := make(map[model.CPUSetPoolIdentity]machine.CPUSet, len(entries))
			for _, entry := range entries {
				shuffled[entry.identity] = entry.cpus
			}
			require.Equal(t, want, assignPoolLabels(shuffled))
		}
	})

	t.Run("contracts labels after a colliding Pod is removed", func(t *testing.T) {
		t.Parallel()

		withCollision := assignPoolLabels(map[model.CPUSetPoolIdentity]machine.CPUSet{
			share("dedicated-default/api"): machine.NewCPUSet(1),
			dedicated("default", "api"):    machine.NewCPUSet(2),
		})
		withoutCollision := assignPoolLabels(map[model.CPUSetPoolIdentity]machine.CPUSet{
			dedicated("default", "api"): machine.NewCPUSet(2),
		})

		require.Empty(t, withCollision.pools)
		require.Equal(t, []labeledPool{
			{identity: dedicated("default", "api"), label: "dedicated-default/api", cpus: machine.NewCPUSet(2)},
		}, withoutCollision.pools)
	})
}

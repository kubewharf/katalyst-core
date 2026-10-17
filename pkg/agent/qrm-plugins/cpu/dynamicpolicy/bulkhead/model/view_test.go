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

package model

import (
	"fmt"
	"sort"
	"testing"

	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

func TestCPUSetPoolIdentityValid_BitsUT(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		identity CPUSetPoolIdentity
		want     bool
	}{
		{
			name:     "reclaim",
			identity: CPUSetPoolIdentity{Kind: CPUSetPoolKindReclaim},
			want:     true,
		},
		{
			name:     "share",
			identity: CPUSetPoolIdentity{Kind: CPUSetPoolKindShare, Name: "share-NUMA0"},
			want:     true,
		},
		{
			name:     "dedicated",
			identity: CPUSetPoolIdentity{Kind: CPUSetPoolKindDedicated, PodNamespace: "default", PodName: "dedicated-pod"},
			want:     true,
		},
		{
			name:     "isolation",
			identity: CPUSetPoolIdentity{Kind: CPUSetPoolKindIsolation, PodNamespace: "default", PodName: "isolation-pod"},
			want:     true,
		},
		{
			name:     "unknown kind",
			identity: CPUSetPoolIdentity{Kind: CPUSetPoolKind("unknown")},
		},
		{
			name:     "reclaim with name",
			identity: CPUSetPoolIdentity{Kind: CPUSetPoolKindReclaim, Name: "reclaim"},
		},
		{
			name:     "reclaim with pod namespace",
			identity: CPUSetPoolIdentity{Kind: CPUSetPoolKindReclaim, PodNamespace: "default"},
		},
		{
			name:     "reclaim with pod name",
			identity: CPUSetPoolIdentity{Kind: CPUSetPoolKindReclaim, PodName: "pod"},
		},
		{
			name:     "share without name",
			identity: CPUSetPoolIdentity{Kind: CPUSetPoolKindShare},
		},
		{
			name:     "share with pod namespace",
			identity: CPUSetPoolIdentity{Kind: CPUSetPoolKindShare, Name: "share", PodNamespace: "default"},
		},
		{
			name:     "dedicated without pod namespace",
			identity: CPUSetPoolIdentity{Kind: CPUSetPoolKindDedicated},
		},
		{
			name:     "dedicated without pod name",
			identity: CPUSetPoolIdentity{Kind: CPUSetPoolKindDedicated, PodNamespace: "default"},
		},
		{
			name:     "dedicated with name",
			identity: CPUSetPoolIdentity{Kind: CPUSetPoolKindDedicated, Name: "dedicated", PodNamespace: "default", PodName: "pod"},
		},
		{
			name:     "isolation without pod namespace",
			identity: CPUSetPoolIdentity{Kind: CPUSetPoolKindIsolation},
		},
		{
			name:     "isolation without pod name",
			identity: CPUSetPoolIdentity{Kind: CPUSetPoolKindIsolation, PodNamespace: "default"},
		},
		{
			name:     "isolation with name",
			identity: CPUSetPoolIdentity{Kind: CPUSetPoolKindIsolation, Name: "isolation", PodNamespace: "default", PodName: "pod"},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := tt.identity.Valid(); got != tt.want {
				t.Fatalf("Valid() = %v, want %v for %#v", got, tt.want, tt.identity)
			}
		})
	}
}

func TestCPUSetPoolIdentityLess_BitsUT(t *testing.T) {
	t.Parallel()

	got := []CPUSetPoolIdentity{
		{Kind: CPUSetPoolKindDedicated, PodNamespace: "team-b", PodName: "api"},
		{Kind: CPUSetPoolKindReclaim},
		{Kind: CPUSetPoolKindDedicated, PodNamespace: "team-a", PodName: "api-b"},
		{Kind: CPUSetPoolKindShare, Name: "share-b"},
		{Kind: CPUSetPoolKindDedicated, PodNamespace: "team-a", PodName: "api-a"},
		{Kind: CPUSetPoolKindShare, Name: "share-a"},
	}
	sort.Slice(got, func(i, j int) bool {
		return got[i].Less(got[j])
	})

	want := []CPUSetPoolIdentity{
		{Kind: CPUSetPoolKindDedicated, PodNamespace: "team-a", PodName: "api-a"},
		{Kind: CPUSetPoolKindDedicated, PodNamespace: "team-a", PodName: "api-b"},
		{Kind: CPUSetPoolKindDedicated, PodNamespace: "team-b", PodName: "api"},
		{Kind: CPUSetPoolKindReclaim},
		{Kind: CPUSetPoolKindShare, Name: "share-a"},
		{Kind: CPUSetPoolKindShare, Name: "share-b"},
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("sorted identity[%d] = %+v, want %+v; all=%+v", i, got[i], want[i], got)
		}
	}
}

func TestDesiredViewPoolOwnersConstructorDeepCopyAndEquality_BitsUT(t *testing.T) {
	t.Parallel()

	share := CPUSetPoolIdentity{Kind: CPUSetPoolKindShare, Name: "share-NUMA0"}
	desired := NewDesiredView()
	if desired.PoolOwners == nil {
		t.Fatal("NewDesiredView should initialize PoolOwners")
	}
	desired.PoolOwners[share] = DesiredPoolOwner{
		ExpectedCPUSet: machine.NewCPUSet(0, 1),
		ContainerCPUSetByName: map[string]machine.CPUSet{
			"main": machine.NewCPUSet(0),
		},
	}

	copied := desired.DeepCopy()
	if !EqualDesiredView(desired, copied) {
		t.Fatal("copied desired pool owners should compare equal")
	}
	owner := copied.PoolOwners[share]
	owner.ExpectedCPUSet.Add(99)
	copied.PoolOwners[share] = owner
	if desired.PoolOwners[share].ExpectedCPUSet.Contains(99) {
		t.Fatal("DesiredView.DeepCopy should isolate owner expected CPU sets")
	}
	if EqualDesiredView(desired, copied) {
		t.Fatal("EqualDesiredView should compare owner expected CPU sets")
	}

	copied = desired.DeepCopy()
	owner = copied.PoolOwners[share]
	owner.ContainerCPUSetByName["main"].Add(99)
	copied.PoolOwners[share] = owner
	if desired.PoolOwners[share].ContainerCPUSetByName["main"].Contains(99) {
		t.Fatal("DesiredView.DeepCopy should isolate owner container CPU sets")
	}
	if EqualDesiredView(desired, copied) {
		t.Fatal("EqualDesiredView should compare owner container CPU sets")
	}

	copied = desired.DeepCopy()
	delete(copied.PoolOwners, share)
	copied.PoolOwners[CPUSetPoolIdentity{Kind: CPUSetPoolKindShare, Name: "share-NUMA1"}] = desired.PoolOwners[share]
	if EqualDesiredView(desired, copied) {
		t.Fatal("EqualDesiredView should compare desired owner identities")
	}
}

func TestDesiredViewToAppliedViewDoesNotCopyPoolOwners_BitsUT(t *testing.T) {
	t.Parallel()

	projection := NewAppliedPoolProjection()
	if projection.CPUSetByIdentity == nil ||
		!projection.UncoveredCPUs.IsEmpty() ||
		!projection.AmbiguousCPUs.IsEmpty() {
		t.Fatal("NewAppliedPoolProjection should initialize empty fields")
	}

	desired := NewDesiredView()
	desired.PoolOwners[CPUSetPoolIdentity{Kind: CPUSetPoolKindReclaim}] = DesiredPoolOwner{
		ExpectedCPUSet: machine.NewCPUSet(0, 1),
	}

	applied := desired.ToAppliedView()
	if applied.PoolProjection.CPUSetByIdentity == nil ||
		len(applied.PoolProjection.CPUSetByIdentity) != 0 ||
		!applied.PoolProjection.UncoveredCPUs.IsEmpty() ||
		!applied.PoolProjection.AmbiguousCPUs.IsEmpty() {
		t.Fatal("ToAppliedView should initialize an empty applied pool projection")
	}
	if len(desired.PoolOwners) != 1 {
		t.Fatal("ToAppliedView should not mutate desired pool owners")
	}
}

func TestDesiredViewToAppliedViewDeepCopyIsolation_BitsUT(t *testing.T) {
	t.Parallel()

	desired := NewDesiredView()
	desired.NonReclaimPool = machine.NewCPUSet(0, 1)
	desired.ReclaimEffective = machine.NewCPUSet(2, 3)
	desired.ReclaimEffectivePerNUMA[0] = machine.NewCPUSet(2)
	desired.SharePoolMap["share"] = machine.NewCPUSet(0, 1)
	desired.ContainerCPUSetByPod["pod"] = map[string]machine.CPUSet{
		"container": machine.NewCPUSet(2),
	}

	applied := desired.ToAppliedView()
	desired.ReclaimEffective.Add(99)
	desired.ReclaimEffectivePerNUMA[0].Add(99)
	desired.SharePoolMap["share"].Add(99)
	desired.ContainerCPUSetByPod["pod"]["container"].Add(99)

	if applied.ReclaimEffective.Contains(99) {
		t.Fatalf("AppliedView should be isolated from DesiredView cpuset mutation")
	}
	if applied.ReclaimEffectivePerNUMA[0].Contains(99) {
		t.Fatalf("AppliedView per-NUMA map should be isolated from DesiredView mutation")
	}
	if applied.SharePoolMap["share"].Contains(99) {
		t.Fatalf("AppliedView share-pool map should be isolated from DesiredView mutation")
	}
	if applied.ContainerCPUSetByPod["pod"]["container"].Contains(99) {
		t.Fatalf("AppliedView container map should be isolated from DesiredView mutation")
	}
}

func TestAppliedViewDeepCopyIsolation_BitsUT(t *testing.T) {
	t.Parallel()

	dedicated := CPUSetPoolIdentity{Kind: CPUSetPoolKindDedicated, PodNamespace: "default", PodName: "api"}
	applied := &AppliedView{
		CPUSetByRel: map[string]machine.CPUSet{
			"system": machine.NewCPUSet(2, 3),
		},
		PoolProjection: AppliedPoolProjection{
			CPUSetByIdentity: map[CPUSetPoolIdentity]machine.CPUSet{
				dedicated: machine.NewCPUSet(2, 3),
			},
			UncoveredCPUs: machine.NewCPUSet(4),
			AmbiguousCPUs: machine.NewCPUSet(5),
		},
		CPUSetPartitionView: CPUSetPartitionView{
			NonReclaimPool:   machine.NewCPUSet(0, 1),
			ReclaimEffective: machine.NewCPUSet(2, 3),
			ReclaimEffectivePerNUMA: map[int]machine.CPUSet{
				1: machine.NewCPUSet(3),
			},
			SharePoolMap: map[string]machine.CPUSet{
				"share": machine.NewCPUSet(0),
			},
			ContainerCPUSetByPod: map[string]map[string]machine.CPUSet{
				"pod": {
					"container": machine.NewCPUSet(3),
				},
			},
		},
	}

	copied := applied.DeepCopy()
	copied.ReclaimEffective.Add(99)
	copied.ReclaimEffectivePerNUMA[1].Add(99)
	copied.SharePoolMap["share"].Add(99)
	copied.ContainerCPUSetByPod["pod"]["container"].Add(99)
	copied.CPUSetByRel["system"].Add(99)
	copied.PoolProjection.CPUSetByIdentity[dedicated].Add(99)
	copied.PoolProjection.UncoveredCPUs.Add(99)
	copied.PoolProjection.AmbiguousCPUs.Add(99)

	if applied.ReclaimEffective.Contains(99) ||
		applied.ReclaimEffectivePerNUMA[1].Contains(99) ||
		applied.SharePoolMap["share"].Contains(99) ||
		applied.ContainerCPUSetByPod["pod"]["container"].Contains(99) ||
		applied.CPUSetByRel["system"].Contains(99) ||
		applied.PoolProjection.CPUSetByIdentity[dedicated].Contains(99) ||
		applied.PoolProjection.UncoveredCPUs.Contains(99) ||
		applied.PoolProjection.AmbiguousCPUs.Contains(99) {
		t.Fatalf("AppliedView.DeepCopy should isolate nested CPUSet fields")
	}
}

func TestAppliedViewPoolProjectionEquality_BitsUT(t *testing.T) {
	t.Parallel()

	dedicated := CPUSetPoolIdentity{Kind: CPUSetPoolKindDedicated, PodNamespace: "default", PodName: "api"}
	applied := &AppliedView{
		CPUSetPartitionView: NewCPUSetPartitionView(),
		PoolProjection: AppliedPoolProjection{
			CPUSetByIdentity: map[CPUSetPoolIdentity]machine.CPUSet{
				dedicated: machine.NewCPUSet(2, 3),
			},
			UncoveredCPUs: machine.NewCPUSet(4),
			AmbiguousCPUs: machine.NewCPUSet(5),
		},
	}

	copied := applied.DeepCopy()
	if !EqualAppliedView(applied, copied) {
		t.Fatal("equal applied pool projections should compare equal")
	}

	copied.PoolProjection.CPUSetByIdentity[dedicated].Add(99)
	if EqualAppliedView(applied, copied) {
		t.Fatal("EqualAppliedView should compare projected pool CPU sets")
	}

	copied = applied.DeepCopy()
	delete(copied.PoolProjection.CPUSetByIdentity, dedicated)
	copied.PoolProjection.CPUSetByIdentity[CPUSetPoolIdentity{
		Kind: CPUSetPoolKindDedicated, PodNamespace: "default", PodName: "api-v2",
	}] = machine.NewCPUSet(2, 3)
	if EqualAppliedView(applied, copied) {
		t.Fatal("EqualAppliedView should compare projected pool identities")
	}

	copied = applied.DeepCopy()
	copied.PoolProjection.UncoveredCPUs.Add(99)
	if EqualAppliedView(applied, copied) {
		t.Fatal("EqualAppliedView should compare uncovered CPUs")
	}

	copied = applied.DeepCopy()
	copied.PoolProjection.AmbiguousCPUs.Add(99)
	if EqualAppliedView(applied, copied) {
		t.Fatal("EqualAppliedView should compare ambiguous CPUs")
	}
}

func TestAppliedViewRelProofDeepCopyAndEqual_BitsUT(t *testing.T) {
	t.Parallel()

	applied := &AppliedView{
		CPUSetPartitionView: NewCPUSetPartitionView(),
		RelProofByRel: map[string]CgroupRelProof{
			"system": {
				Device: 7,
				Inode:  11,
				CPUSet: machine.NewCPUSet(2, 3),
			},
		},
	}

	copied := applied.DeepCopy()
	proof := copied.RelProofByRel["system"]
	proof.CPUSet.Add(99)
	copied.RelProofByRel["system"] = proof
	if applied.RelProofByRel["system"].CPUSet.Contains(99) {
		t.Fatalf("AppliedView.DeepCopy should isolate rel proof cpusets")
	}
	if EqualAppliedView(applied, copied) {
		t.Fatalf("EqualAppliedView should compare rel proof cpusets")
	}

	copied = applied.DeepCopy()
	proof = copied.RelProofByRel["system"]
	proof.Inode++
	copied.RelProofByRel["system"] = proof
	if EqualAppliedView(applied, copied) {
		t.Fatalf("EqualAppliedView should compare rel proof identity")
	}
}

func TestAppliedViewReclaimOnlyLevelParticipatesInCopyAndEquality_BitsUT(t *testing.T) {
	t.Parallel()

	applied := &AppliedView{
		CPUSetPartitionView: NewCPUSetPartitionView(),
		Level:               AppliedViewLevelReclaimOnly,
		CPUSetByRel:         map[string]machine.CPUSet{},
		RelProofByRel:       map[string]CgroupRelProof{},
	}
	copied := applied.DeepCopy()
	if copied.Level != AppliedViewLevelReclaimOnly {
		t.Fatalf("copied level = %q, want %q", copied.Level, AppliedViewLevelReclaimOnly)
	}
	if !EqualAppliedView(applied, copied) {
		t.Fatal("equal reclaim-only applied views should compare equal")
	}

	copied.Level = AppliedViewLevelFull
	if EqualAppliedView(applied, copied) {
		t.Fatal("applied views with different levels should not compare equal")
	}
}

// BenchmarkCloneCPUSetMap measures the deep-copy of a rel->CPUSet map, a hot
// operation on every convergence round.
func BenchmarkCloneCPUSetMap(b *testing.B) {
	const relCount = 256
	in := make(map[string]machine.CPUSet, relCount)
	for i := 0; i < relCount; i++ {
		in[fmt.Sprintf("root/node-%04d", i)] = machine.NewCPUSet(i*2, i*2+1)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if out := CloneCPUSetMap(in); len(out) != relCount {
			b.Fatalf("cloned map size = %d, want %d", len(out), relCount)
		}
	}
}

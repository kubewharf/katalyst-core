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

package cpusettopology

import (
	"testing"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/bulkhead/model"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

func TestBuildAppliedPoolProjectionFull(t *testing.T) {
	t.Parallel()

	reclaim := model.CPUSetPoolIdentity{Kind: model.CPUSetPoolKindReclaim}
	shareA := model.CPUSetPoolIdentity{Kind: model.CPUSetPoolKindShare, Name: "share-a"}
	shareB := model.CPUSetPoolIdentity{Kind: model.CPUSetPoolKindShare, Name: "share-b"}
	dedicatedA := model.CPUSetPoolIdentity{Kind: model.CPUSetPoolKindDedicated, PodNamespace: "default", PodName: "dedicated-a"}
	dedicatedB := model.CPUSetPoolIdentity{Kind: model.CPUSetPoolKindDedicated, PodNamespace: "default", PodName: "dedicated-b"}
	isolationA := model.CPUSetPoolIdentity{Kind: model.CPUSetPoolKindIsolation, PodNamespace: "default", PodName: "isolation-a"}
	isolationB := model.CPUSetPoolIdentity{Kind: model.CPUSetPoolKindIsolation, PodNamespace: "default", PodName: "isolation-b"}
	invalid := model.CPUSetPoolIdentity{Kind: model.CPUSetPoolKindDedicated}

	desired := model.NewDesiredView()
	desired.PoolOwners = map[model.CPUSetPoolIdentity]model.DesiredPoolOwner{
		reclaim: {
			ExpectedCPUSet: machine.NewCPUSet(0, 1, 11),
		},
		shareA: {
			ExpectedCPUSet: machine.NewCPUSet(2, 3, 4, 8),
		},
		shareB: {
			ExpectedCPUSet: machine.NewCPUSet(4, 5, 6),
		},
		dedicatedA: {
			ProofPodUID:    "dedicated-a",
			ExpectedCPUSet: machine.NewCPUSet(3, 6, 7),
			ContainerCPUSetByName: map[string]machine.CPUSet{
				"main":    machine.NewCPUSet(3, 6),
				"sidecar": machine.NewCPUSet(6, 7),
			},
		},
		dedicatedB: {
			ProofPodUID:    "dedicated-b",
			ExpectedCPUSet: machine.NewCPUSet(11),
			ContainerCPUSetByName: map[string]machine.CPUSet{
				"main": machine.NewCPUSet(11),
			},
		},
		isolationA: {
			ProofPodUID:    "isolation-a",
			ExpectedCPUSet: machine.NewCPUSet(6, 7),
			ContainerCPUSetByName: map[string]machine.CPUSet{
				"main": machine.NewCPUSet(6, 7),
			},
		},
		isolationB: {
			ProofPodUID:    "isolation-b",
			ExpectedCPUSet: machine.NewCPUSet(12),
			ContainerCPUSetByName: map[string]machine.CPUSet{
				"main": machine.NewCPUSet(12),
			},
		},
		invalid: {
			ExpectedCPUSet: machine.NewCPUSet(8),
			ContainerCPUSetByName: map[string]machine.CPUSet{
				"main": machine.NewCPUSet(8),
			},
		},
	}
	applied := &model.AppliedView{
		CPUSetPartitionView: model.NewCPUSetPartitionView(),
		Level:               model.AppliedViewLevelFull,
	}
	applied.ReclaimEffective = machine.NewCPUSet(0, 1)
	applied.NonReclaimPool = machine.NewCPUSet(2, 3, 4, 5, 6, 7, 8, 9, 10)
	applied.Reserve = machine.NewCPUSet(9)
	applied.ContainerCPUSetByPod = map[string]map[string]machine.CPUSet{
		"dedicated-a": {
			"main":    machine.NewCPUSet(3, 6),
			"sidecar": machine.NewCPUSet(6),
		},
		"isolation-a": {
			"main": machine.NewCPUSet(6, 7),
		},
		"": {
			"main": machine.NewCPUSet(8),
		},
	}

	got := buildAppliedPoolProjection(model.AppliedViewLevelFull, desired, applied)

	assertProjectedCPUSet(t, got.CPUSetByIdentity, reclaim, machine.NewCPUSet(0, 1))
	assertProjectedCPUSet(t, got.CPUSetByIdentity, shareA, machine.NewCPUSet(2))
	assertProjectedCPUSet(t, got.CPUSetByIdentity, shareB, machine.NewCPUSet(5))
	assertProjectedCPUSet(t, got.CPUSetByIdentity, dedicatedA, machine.NewCPUSet(3))
	assertProjectedCPUSet(t, got.CPUSetByIdentity, isolationA, machine.NewCPUSet(7))
	if _, ok := got.CPUSetByIdentity[dedicatedB]; ok {
		t.Fatal("dedicated owner without final leaf proof was published")
	}
	if _, ok := got.CPUSetByIdentity[isolationB]; ok {
		t.Fatal("isolation owner without final leaf proof was published")
	}
	if _, ok := got.CPUSetByIdentity[invalid]; ok {
		t.Fatal("invalid owner was published")
	}
	if !got.AmbiguousCPUs.Equals(machine.NewCPUSet(4, 6, 8)) {
		t.Fatalf("ambiguous CPUs = %s, want 4,6,8", got.AmbiguousCPUs.String())
	}
	if !got.UncoveredCPUs.Equals(machine.NewCPUSet(10)) {
		t.Fatalf("uncovered CPUs = %s, want 10", got.UncoveredCPUs.String())
	}
}

func TestBuildAppliedPoolProjectionExcludesReserveBeforeOwnershipChecks(t *testing.T) {
	t.Parallel()

	share := model.CPUSetPoolIdentity{Kind: model.CPUSetPoolKindShare, Name: "share"}
	dedicated := model.CPUSetPoolIdentity{Kind: model.CPUSetPoolKindDedicated, PodNamespace: "default", PodName: "pod"}
	invalid := model.CPUSetPoolIdentity{Kind: model.CPUSetPoolKindDedicated}
	desired := model.NewDesiredView()
	desired.PoolOwners = map[model.CPUSetPoolIdentity]model.DesiredPoolOwner{
		share: {
			ExpectedCPUSet: machine.NewCPUSet(0, 1),
		},
		dedicated: {
			ProofPodUID:    "pod",
			ExpectedCPUSet: machine.NewCPUSet(1),
			ContainerCPUSetByName: map[string]machine.CPUSet{
				"main": machine.NewCPUSet(1),
			},
		},
		invalid: {
			ExpectedCPUSet: machine.NewCPUSet(1),
			ContainerCPUSetByName: map[string]machine.CPUSet{
				"main": machine.NewCPUSet(1),
			},
		},
	}
	applied := &model.AppliedView{
		CPUSetPartitionView: model.NewCPUSetPartitionView(),
		Level:               model.AppliedViewLevelFull,
	}
	applied.NonReclaimPool = machine.NewCPUSet(0, 1)
	applied.Reserve = machine.NewCPUSet(1)
	applied.ContainerCPUSetByPod = map[string]map[string]machine.CPUSet{
		"pod": {"main": machine.NewCPUSet(1)},
		"":    {"main": machine.NewCPUSet(1)},
	}

	got := buildAppliedPoolProjection(model.AppliedViewLevelFull, desired, applied)

	assertProjectedCPUSet(t, got.CPUSetByIdentity, share, machine.NewCPUSet(0))
	if _, ok := got.CPUSetByIdentity[dedicated]; ok {
		t.Fatal("owner proof containing only reserve CPUs was published")
	}
	if _, ok := got.CPUSetByIdentity[invalid]; ok {
		t.Fatal("invalid owner was published")
	}
	if !got.AmbiguousCPUs.IsEmpty() {
		t.Fatalf("reserve CPUs must not be ambiguous: %s", got.AmbiguousCPUs.String())
	}
	if !got.UncoveredCPUs.IsEmpty() {
		t.Fatalf("reserve CPUs must not be uncovered: %s", got.UncoveredCPUs.String())
	}
}

func TestBuildAppliedPoolProjectionMissingOrDeferredLeafDoesNotFallBackToDesired(t *testing.T) {
	t.Parallel()

	identity := model.CPUSetPoolIdentity{Kind: model.CPUSetPoolKindDedicated, PodNamespace: "default", PodName: "pod"}
	for _, tt := range []struct {
		name       string
		finalByPod func() map[string]map[string]machine.CPUSet
	}{
		{
			name: "missing pod proof",
			finalByPod: func() map[string]map[string]machine.CPUSet {
				return map[string]map[string]machine.CPUSet{}
			},
		},
		{
			name: "deferred container proof",
			finalByPod: func() map[string]map[string]machine.CPUSet {
				return map[string]map[string]machine.CPUSet{"pod": {}}
			},
		},
	} {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			desired := model.NewDesiredView()
			desired.PoolOwners[identity] = model.DesiredPoolOwner{
				ProofPodUID:    "pod",
				ExpectedCPUSet: machine.NewCPUSet(2),
				ContainerCPUSetByName: map[string]machine.CPUSet{
					"main": machine.NewCPUSet(2),
				},
			}
			applied := &model.AppliedView{
				CPUSetPartitionView: model.NewCPUSetPartitionView(),
				Level:               model.AppliedViewLevelFull,
			}
			applied.NonReclaimPool = machine.NewCPUSet(2)
			applied.ContainerCPUSetByPod = tt.finalByPod()

			got := buildAppliedPoolProjection(model.AppliedViewLevelFull, desired, applied)

			if _, ok := got.CPUSetByIdentity[identity]; ok {
				t.Fatal("missing/deferred leaf proof fell back to desired ownership")
			}
			if !got.AmbiguousCPUs.IsEmpty() {
				t.Fatalf("missing/deferred leaf proof must not be ambiguous: %s", got.AmbiguousCPUs.String())
			}
			if !got.UncoveredCPUs.Equals(machine.NewCPUSet(2)) {
				t.Fatalf("uncovered CPUs = %s, want 2", got.UncoveredCPUs.String())
			}
		})
	}
}

func TestBuildAppliedPoolProjectionReclaimOnly(t *testing.T) {
	t.Parallel()

	reclaim := model.CPUSetPoolIdentity{Kind: model.CPUSetPoolKindReclaim}
	desired := model.NewDesiredView()
	desired.PoolOwners[reclaim] = model.DesiredPoolOwner{
		ExpectedCPUSet: machine.NewCPUSet(0, 1, 2),
	}
	desired.PoolOwners[model.CPUSetPoolIdentity{
		Kind: model.CPUSetPoolKindShare,
		Name: "share",
	}] = model.DesiredPoolOwner{ExpectedCPUSet: machine.NewCPUSet(3, 4)}
	applied := &model.AppliedView{
		CPUSetPartitionView: model.NewCPUSetPartitionView(),
		Level:               model.AppliedViewLevelReclaimOnly,
	}
	applied.ReclaimEffective = machine.NewCPUSet(1, 2, 5)
	applied.NonReclaimPool = machine.NewCPUSet(3, 4)

	got := buildAppliedPoolProjection(model.AppliedViewLevelReclaimOnly, desired, applied)

	if len(got.CPUSetByIdentity) != 1 {
		t.Fatalf("reclaim-only identities = %d, want 1", len(got.CPUSetByIdentity))
	}
	assertProjectedCPUSet(t, got.CPUSetByIdentity, reclaim, machine.NewCPUSet(1, 2))
	if !got.UncoveredCPUs.IsEmpty() || !got.AmbiguousCPUs.IsEmpty() {
		t.Fatalf("reclaim-only diagnostics must be empty: uncovered=%s ambiguous=%s",
			got.UncoveredCPUs.String(), got.AmbiguousCPUs.String())
	}
}

func TestBuildAppliedPoolProjectionOmitsEmptyAndHandlesNilInputs(t *testing.T) {
	t.Parallel()

	emptyOwner := model.CPUSetPoolIdentity{Kind: model.CPUSetPoolKindShare, Name: "empty"}
	desired := model.NewDesiredView()
	desired.PoolOwners[emptyOwner] = model.DesiredPoolOwner{
		ExpectedCPUSet: machine.NewCPUSet(),
	}
	applied := &model.AppliedView{CPUSetPartitionView: model.NewCPUSetPartitionView()}

	got := buildAppliedPoolProjection(model.AppliedViewLevelFull, desired, applied)
	if len(got.CPUSetByIdentity) != 0 {
		t.Fatalf("empty owner was published: %#v", got.CPUSetByIdentity)
	}

	for name, projection := range map[string]model.AppliedPoolProjection{
		"nil desired": buildAppliedPoolProjection(model.AppliedViewLevelFull, nil, applied),
		"nil applied": buildAppliedPoolProjection(model.AppliedViewLevelFull, desired, nil),
	} {
		if len(projection.CPUSetByIdentity) != 0 ||
			!projection.UncoveredCPUs.IsEmpty() ||
			!projection.AmbiguousCPUs.IsEmpty() {
			t.Fatalf("%s returned non-empty projection: %#v", name, projection)
		}
	}
}

func assertProjectedCPUSet(
	t *testing.T,
	projection map[model.CPUSetPoolIdentity]machine.CPUSet,
	identity model.CPUSetPoolIdentity,
	want machine.CPUSet,
) {
	t.Helper()
	got, ok := projection[identity]
	if !ok {
		t.Fatalf("identity %+v is missing", identity)
	}
	if !got.Equals(want) {
		t.Fatalf("identity %+v CPUs = %s, want %s", identity, got.String(), want.String())
	}
}

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

package topology

import (
	"testing"

	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

// TestComputeEffectiveTargetsCoversPrimaryOwnRequiredFloor pins the cgroup-v1
// DomainPrimary seed-leaf gap: a freshly admitted seed leaf imposes a required
// CPU floor (RequiredCPUSetByRel) that exceeds its not-yet-projected desired
// (seed) target. The effective target must be widened to cover the leaf's own
// required floor; otherwise ParentSafety stays forever owed CPUs the expand
// envelope can never reach and the ParentSafe fixed-point compilation stalls as
// a bare ErrNoProgress.
func TestComputeEffectiveTargetsCoversPrimaryOwnRequiredFloor(t *testing.T) {
	t.Parallel()

	dag := mustPlanDAG(t, []NodeSpec{
		{Rel: "kubepods", Role: TopoNodeRolePrimary, Domain: DomainPrimary, CPUs: machine.NewCPUSet(0, 1, 2, 3), TrustAnchor: true},
		{Rel: "kubepods/seed", ParentRel: "kubepods", Role: TopoNodeRolePrimary, Domain: DomainPrimary, CPUs: machine.NewCPUSet(0)},
	})
	requiredByRel := map[string]machine.CPUSet{
		"kubepods/seed": machine.NewCPUSet(0, 1, 2, 3),
	}

	effective, err := computeEffectiveTargets(dag, false, nil, nil, requiredByRel)
	if err != nil {
		t.Fatalf("computeEffectiveTargets: %v", err)
	}
	if got := effective["kubepods/seed"]; !requiredByRel["kubepods/seed"].IsSubsetOf(got) {
		t.Fatalf("seed effective target %s does not cover its required floor %s",
			got.String(), requiredByRel["kubepods/seed"].String())
	}
}

// TestComputeEffectiveTargetsV2EmptyPrimaryUnchanged pins the cgroup-v2 escape:
// when allowEmptyTarget=true, a DomainPrimary node whose desired cpuset is
// intentionally empty must keep an empty effective target even if its required
// floor is non-empty. The required-floor union must run after the v2 skip, so the
// empty-target inheritance semantics are never erased.
func TestComputeEffectiveTargetsV2EmptyPrimaryUnchanged(t *testing.T) {
	t.Parallel()

	dag := mustPlanDAG(t, []NodeSpec{
		{Rel: "kubepods", Role: TopoNodeRolePrimary, Domain: DomainPrimary, CPUs: machine.NewCPUSet(0, 1), TrustAnchor: true},
		{Rel: "kubepods/empty", ParentRel: "kubepods", Role: TopoNodeRolePrimary, Domain: DomainPrimary, CPUs: machine.NewCPUSet()},
	})
	requiredByRel := map[string]machine.CPUSet{
		"kubepods/empty": machine.NewCPUSet(0, 1),
	}

	effective, err := computeEffectiveTargets(dag, true, nil, nil, requiredByRel)
	if err != nil {
		t.Fatalf("computeEffectiveTargets: %v", err)
	}
	if got := effective["kubepods/empty"]; !got.IsEmpty() {
		t.Fatalf("v2 empty primary effective target = %s, must stay empty (required floor %s must not widen it)",
			got.String(), requiredByRel["kubepods/empty"].String())
	}
}

// TestComputeEffectiveTargetsReclaimRequiredFloorUnchanged pins that required
// floors are only folded into controlled DomainPrimary effective targets. A
// reclaim rel carrying a requiredByRel entry must NOT widen the reclaim effective
// target; reclaim buckets are deducted from primaries, never widened by the
// required-floor pass.
func TestComputeEffectiveTargetsReclaimRequiredFloorUnchanged(t *testing.T) {
	t.Parallel()

	dag := mustPlanDAG(t, []NodeSpec{
		{Rel: "kubepods", Role: TopoNodeRolePrimary, Domain: DomainPrimary, CPUs: machine.NewCPUSet(0, 1), TrustAnchor: true},
		{Rel: "reclaim", Role: TopoNodeRoleReclaim, Domain: DomainReclaim, CPUs: machine.NewCPUSet(2, 3), TrustAnchor: true},
	})
	// A required floor on a reclaim rel must be ignored by the primary widening.
	requiredByRel := map[string]machine.CPUSet{
		"reclaim": machine.NewCPUSet(2, 3, 4, 5),
	}

	effective, err := computeEffectiveTargets(dag, false, nil, nil, requiredByRel)
	if err != nil {
		t.Fatalf("computeEffectiveTargets: %v", err)
	}
	if got := effective["reclaim"]; !got.Equals(machine.NewCPUSet(2, 3)) {
		t.Fatalf("reclaim effective target = %s, must stay at desired 2-3 (required floor on reclaim rel must not widen it)",
			got.String())
	}
}

// TestValidateNoPrimaryReclaimOverlapFailClosed pins that the primary/reclaim
// overlap backstop stays fail-closed: even though normalization normally deducts
// primaries out of reclaim, any residual overlap must surface as an error rather
// than be silently tolerated.
func TestValidateNoPrimaryReclaimOverlapFailClosed(t *testing.T) {
	t.Parallel()

	dag := mustPlanDAG(t, []NodeSpec{
		{Rel: "kubepods", Role: TopoNodeRolePrimary, Domain: DomainPrimary, CPUs: machine.NewCPUSet(0, 1), TrustAnchor: true},
		{Rel: "reclaim", Role: TopoNodeRoleReclaim, Domain: DomainReclaim, CPUs: machine.NewCPUSet(1, 2), TrustAnchor: true},
	})
	// Hand-craft an effective map that still overlaps on CPU 1 (as if normalization
	// had failed to deduct it) and assert the validator rejects it.
	effective := map[string]machine.CPUSet{
		"kubepods": machine.NewCPUSet(0, 1),
		"reclaim":  machine.NewCPUSet(1, 2),
	}
	if err := validateNoPrimaryReclaimOverlap(dag, effective); err == nil {
		t.Fatalf("expected primary/reclaim overlap error, got nil; effective=%v", effective)
	}
}

func TestEffectiveTargetsScopePendingProtection(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		scopeRel   string
		wantRels   []string
		rejectRels []string
	}{
		{
			name:       "ordinary shared burstable",
			scopeRel:   "kubepods/burstable/pod-shared",
			wantRels:   []string{"kubepods", "kubepods/burstable"},
			rejectRels: []string{"kubepods/besteffort", "kubepods/pod-dnb"},
		},
		{
			name:       "snb burstable",
			scopeRel:   "kubepods/burstable/pod-snb",
			wantRels:   []string{"kubepods", "kubepods/burstable"},
			rejectRels: []string{"kubepods/besteffort"},
		},
		{
			name:       "dnb guaranteed",
			scopeRel:   "kubepods/pod-dnb",
			wantRels:   []string{"kubepods"},
			rejectRels: []string{"kubepods/burstable", "kubepods/besteffort"},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			dag := mustPlanDAG(t, []NodeSpec{
				{Rel: "kubepods", Role: TopoNodeRolePrimary, Domain: DomainPrimary, CPUs: machine.NewCPUSet(0, 1, 2, 3, 4, 5), TrustAnchor: true},
				{Rel: "kubepods/burstable", ParentRel: "kubepods", Role: TopoNodeRolePrimary, Domain: DomainPrimary, CPUs: machine.NewCPUSet(0, 1, 2, 3, 4, 5)},
				{Rel: "kubepods/besteffort", ParentRel: "kubepods", Role: TopoNodeRolePrimary, Domain: DomainPrimary, CPUs: machine.NewCPUSet(0, 1, 2, 3, 4, 5)},
				{Rel: "reclaim", Role: TopoNodeRoleReclaim, Domain: DomainReclaim, CPUs: machine.NewCPUSet(8, 9), TrustAnchor: true},
			})
			pending := machine.NewCPUSet(6, 7)
			required, err := pendingRequiredCPUSetByRel(dag, []PendingProtection{{
				ScopeRel: tt.scopeRel,
				CPUs:     pending,
				PodUID:   tt.name,
				Source:   PendingProtectionSourceExpectedPod,
			}})
			if err != nil {
				t.Fatalf("pendingRequiredCPUSetByRel: %v", err)
			}

			effective, err := computeEffectiveTargets(dag, false, nil, required, nil, nil)
			if err != nil {
				t.Fatalf("computeEffectiveTargets: %v", err)
			}
			for _, rel := range tt.wantRels {
				if !pending.IsSubsetOf(effective[rel]) {
					t.Errorf("effective[%q] = %s, want pending CPUs %s", rel, effective[rel].String(), pending.String())
				}
			}
			for _, rel := range tt.rejectRels {
				if !effective[rel].Intersection(pending).IsEmpty() {
					t.Errorf("effective[%q] = %s, want no pending CPUs %s", rel, effective[rel].String(), pending.String())
				}
			}
		})
	}
}

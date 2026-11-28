/*
Copyright 2022 The Kubernetes Authors.

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

package utils

import (
	"testing"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/bulkhead/model"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/bulkhead/utils/topology"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

// pendingScope is the convenience constructor for a single pending protection.
func pendingScope(rel, podUID string, cpus machine.CPUSet) topology.PendingProtection {
	return topology.PendingProtection{
		ScopeRel: rel,
		PodUID:   podUID,
		Source:   topology.PendingProtectionSourceExistingPod,
		CPUs:     cpus,
	}
}

// TestProjectPendingFallsBackToValidatedNonReclaimPartition pins the gate-false
// fix: when the soft/non-overlap projection gate is on but there is no
// pre-admission AppliedView, the clip domain falls back to the materialized,
// validated non-reclaim partition (NonReclaimPool) already folded into the input,
// so a whole-NUMA pending scope is clipped to CPUs already proven non-reclaim
// instead of being preserved verbatim and widening the primary onto the reclaim
// bucket.
func TestProjectPendingFallsBackToValidatedNonReclaimPartition(t *testing.T) {
	t.Parallel()
	topo := make4NUMASMTTopology()

	// Validated, materialized non-reclaim partition present; Desired* projection
	// authority intentionally empty.
	view := &model.DesiredView{
		CPUSetPartitionView: model.CPUSetPartitionView{
			Reserve:        machine.NewCPUSet(),
			Dedicated:      machine.NewCPUSet(),
			Isolation:      machine.NewCPUSet(),
			NonReclaimPool: machine.MustParse("0-3,32-35"),
			// DesiredNonReclaimPool left empty on purpose.
		},
	}

	wholeNUMA0 := machine.MustParse("0-31")
	protections := []topology.PendingProtection{
		pendingScope("kubepods/besteffort/podX", "podX", wholeNUMA0),
	}

	got := ProjectPendingProtectionsToDesiredNonReclaim(view, topo, protections, nil)
	if len(got) != 1 {
		t.Fatalf("expected 1 projected protection, got %d", len(got))
	}
	want := machine.MustParse("0-3") // whole NUMA0 ∩ NonReclaimPool
	if !got[0].CPUs.Equals(want) {
		t.Fatalf("clipped pending = %s, want %s", got[0].CPUs.String(), want.String())
	}
	if !got[0].SharedProjectedCPUs.Equals(want) {
		t.Fatalf("shared-projected = %s, want %s", got[0].SharedProjectedCPUs.String(), want.String())
	}
}

// TestProjectPendingPreservesExactSeedWhenNoValidatedDomain pins the safety net:
// when there is no pre-admission AppliedView and both the materialized
// NonReclaimPool and the business DesiredNonReclaimPool are empty, there is
// genuinely no non-reclaim domain to clip against, so the pending scope must be
// preserved verbatim (exact seed retention) rather than emptied.
func TestProjectPendingPreservesExactSeedWhenNoValidatedDomain(t *testing.T) {
	t.Parallel()
	topo := make4NUMASMTTopology()

	view := &model.DesiredView{
		CPUSetPartitionView: model.CPUSetPartitionView{
			Reserve:   machine.NewCPUSet(),
			Dedicated: machine.NewCPUSet(),
			Isolation: machine.NewCPUSet(),
			// Both DesiredNonReclaimPool and NonReclaimPool left empty.
		},
	}

	wholeNUMA0 := machine.MustParse("0-31")
	protections := []topology.PendingProtection{
		pendingScope("kubepods/besteffort/podX", "podX", wholeNUMA0),
	}

	got := ProjectPendingProtectionsToDesiredNonReclaim(view, topo, protections, nil)
	if len(got) != 1 {
		t.Fatalf("expected 1 protection, got %d", len(got))
	}
	if !got[0].CPUs.Equals(wholeNUMA0) {
		t.Fatalf("pending must be preserved verbatim when no validated domain, got %s want %s",
			got[0].CPUs.String(), wholeNUMA0.String())
	}
	if !got[0].SharedProjectedCPUs.IsEmpty() {
		t.Fatalf("shared-projected must stay empty for an exact seed, got %s",
			got[0].SharedProjectedCPUs.String())
	}
}

// TestProjectPendingValidatedPoolBoundsSelfExpandedDesired pins the authority fix:
// the clip authority is the manager-owned validated non-reclaim partition, never
// the self-expanded business DesiredNonReclaimPool. When the business
// DesiredNonReclaimPool has grown to include reclaim candidates (the admission
// round where it already covers the whole NUMA the pending pod wants), it must NOT
// widen the clip domain: the broader desired pool is business truth, not a ceiling.
func TestProjectPendingValidatedPoolBoundsSelfExpandedDesired(t *testing.T) {
	t.Parallel()
	topo := make4NUMASMTTopology()

	// Business DesiredNonReclaimPool is the broad, self-expanded set {0-15}
	// (includes reclaim candidates 4,7). The validated materialized NonReclaimPool
	// is the narrow proven set {0-3,32-35}. The projection must follow the narrow
	// validated pool, not the broad self-expanded desired.
	view := &model.DesiredView{
		CPUSetPartitionView: model.CPUSetPartitionView{
			Reserve:               machine.NewCPUSet(),
			Dedicated:             machine.NewCPUSet(),
			Isolation:             machine.NewCPUSet(),
			DesiredNonReclaimPool: machine.MustParse("0-15"),
			NonReclaimPool:        machine.MustParse("0-3,32-35"),
		},
	}

	wholeNUMA0 := machine.MustParse("0-31")
	protections := []topology.PendingProtection{
		pendingScope("kubepods/besteffort/podX", "podX", wholeNUMA0),
	}

	got := ProjectPendingProtectionsToDesiredNonReclaim(view, topo, protections, nil)
	if len(got) != 1 {
		t.Fatalf("expected 1 protection, got %d", len(got))
	}
	want := machine.MustParse("0-3") // validated NonReclaimPool ∩ whole NUMA0, NOT 0-15
	if !got[0].CPUs.Equals(want) {
		t.Fatalf("clipped pending = %s, want %s (validated pool must bound the self-expanded desired)",
			got[0].CPUs.String(), want.String())
	}
}

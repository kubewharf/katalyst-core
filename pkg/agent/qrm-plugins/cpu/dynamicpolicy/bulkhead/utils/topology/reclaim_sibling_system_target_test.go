/*
Copyright 2026 The Katalyst Authors.

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
	"errors"
	"testing"

	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

// auditReclaimSiblingSystemShape builds the NodeSpec set that
// BuildTopologyNodeSpecsFromView emits for a production-shaped 128C/4NUMA box
// (4 NUMA x 32 cores) on the Retry/mixed-projection checkpoint:
//
//   - primary rel "kubepods"        <- NonReclaimPool (120 cores)
//   - reclaim rel "reclaimed"       <- AGGREGATE ReclaimEffective (collapsed to empty)
//   - per-NUMA buckets "reclaimed/reclaimed-0..3" <- authoritative hard-partition
//     reclaim, 2 cores/NUMA (8 union), child of the reclaim rel
//   - system reclaim sibling "custom-reclaim-root" <- AGGREGATE ReclaimEffective (empty),
//     top-level controlled root
//
// The aggregate reclaim has collapsed (e.g. ReclaimRaw empty on retry/recovery)
// while the authoritative per-NUMA buckets still own 8 cores. This is the exact
// mixed projection the builder produces on node.
func auditReclaimSiblingSystemShape() ([]NodeSpec, machine.CPUDetails) {
	cpuDetails := benchmarkCPUDetails(4, 32) // 128 CPUs, NUMA 0..3, 32 cores each

	reclaimPerNUMA := map[int]machine.CPUSet{
		0: machine.NewCPUSet(0, 1),
		1: machine.NewCPUSet(32, 33),
		2: machine.NewCPUSet(64, 65),
		3: machine.NewCPUSet(96, 97),
	}
	bucketUnion := machine.NewCPUSet()
	all := machine.NewCPUSet()
	for c := 0; c < 128; c++ {
		all = all.Union(machine.NewCPUSet(c))
	}
	for _, cpus := range reclaimPerNUMA {
		bucketUnion = bucketUnion.Union(cpus)
	}
	nonReclaim := all.Difference(bucketUnion) // 120 cores
	aggregateReclaim := machine.NewCPUSet()   // collapsed aggregate ReclaimEffective

	specs := []NodeSpec{{
		Rel: "kubepods", Role: TopoNodeRolePrimary, Domain: DomainPrimary,
		CPUs: nonReclaim, ControlledRoot: true, TrustAnchor: true,
	}}
	specs = append(specs, NodeSpec{
		Rel: "reclaimed", Role: TopoNodeRoleReclaim, Domain: DomainReclaim,
		CPUs: aggregateReclaim, ControlledRoot: true, TrustAnchor: true,
	})
	for numaID := range reclaimPerNUMA {
		cpus := reclaimPerNUMA[numaID]
		specs = append(specs, NodeSpec{
			Rel: "reclaimed/reclaimed-" + itoa(numaID), Role: TopoNodeRoleReclaimNUMABucket,
			CPUs: cpus, ParentRel: "reclaimed", Domain: DomainReclaim, TrustAnchor: true,
			Constraint: TopologyConstraint{
				CPUUpperBound: cpuDetails.CPUsInNUMANodes(numaID),
				Scope:         TopologyScopeNUMANode,
			},
			Metadata: map[string]string{"numa": itoa(numaID)},
		})
	}
	// system reclaim sibling: top-level controlled root, seeded from the same
	// (collapsed) aggregate ReclaimEffective as the reclaim parent.
	specs = append(specs, NodeSpec{
		Rel: "custom-reclaim-root", Role: TopoNodeRoleReclaimSibling, Domain: DomainReclaim,
		CPUs: aggregateReclaim, ControlledRoot: true, TrustAnchor: true,
	})
	return specs, cpuDetails
}

func claimPerNUMAValues(m map[int]machine.CPUSet) []machine.CPUSet {
	out := make([]machine.CPUSet, 0, len(m))
	for _, cpus := range m {
		out = append(out, cpus)
	}
	return out
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	digits := ""
	for n > 0 {
		digits = string(rune('0'+n%10)) + digits
		n /= 10
	}
	return digits
}

// TestComputeEffectiveTargetsSystemSiblingFollowsNormalizedReclaimUnion pins the
// cgroup-v1 Retry/mixed-projection invariant: when the aggregate ReclaimEffective
// collapses but the per-NUMA reclaim buckets still hold 8 cores, the normalized
// reclaim parent is widened to contain the buckets (8), and the canonical system
// reclaim-sibling target MUST follow that same union (8) rather than stay seeded
// from the collapsed aggregate. An empty controlled system target on cgroup v1 is
// rejected downstream by validateExecutableEmptyTargets as UnsupportedEmptyTargetError.
func TestComputeEffectiveTargetsSystemSiblingFollowsNormalizedReclaimUnion(t *testing.T) {
	t.Parallel()

	specs, cpuDetails := auditReclaimSiblingSystemShape()
	dag := mustPlanDAG(t, specs)
	requiredByRel := RequiredCPUSetByRelFromNodeSpecs(specs)

	effective, err := computeEffectiveTargets(dag, false, cpuDetails, nil, requiredByRel)
	if err != nil {
		t.Fatalf("computeEffectiveTargets: %v", err)
	}

	if got := effective["kubepods"]; got.Size() != 120 {
		t.Fatalf("primary effective size = %d (%s), want 120", got.Size(), got.String())
	}
	if got := effective["reclaimed"]; got.Size() != 8 {
		t.Fatalf("reclaim parent effective size = %d (%s), want 8", got.Size(), got.String())
	}
	unionBuckets := machine.NewCPUSet()
	for _, rel := range []string{"reclaimed/reclaimed-0", "reclaimed/reclaimed-1", "reclaimed/reclaimed-2", "reclaimed/reclaimed-3"} {
		unionBuckets = unionBuckets.Union(effective[rel])
	}
	if unionBuckets.Size() != 8 {
		t.Fatalf("per-NUMA reclaim union size = %d (%s), want 8", unionBuckets.Size(), unionBuckets.String())
	}

	system := effective["custom-reclaim-root"]
	if system.Size() != 8 {
		t.Fatalf("system reclaim sibling effective size = %d (%s), want 8 (must follow normalized reclaim union)",
			system.Size(), system.String())
	}
	if !system.Equals(effective["reclaimed"]) {
		t.Fatalf("system target %s does not equal normalized reclaim parent %s",
			system.String(), effective["reclaimed"].String())
	}
	// primary/reclaim must stay disjoint.
	if overlap := system.Intersection(effective["kubepods"]); !overlap.IsEmpty() {
		t.Fatalf("system target overlaps primary: %s", overlap.String())
	}
}

// TestComputeEffectiveTargetsSystemSiblingV2EmptyPrimaryUnchanged ensures the
// sibling-widening change does not disturb cgroup-v2 empty-target semantics for
// PRIMARY nodes: an intentionally empty DomainPrimary leaf must stay empty on v2
// even when it carries a required floor (the widenPrimary skip is untouched).
func TestComputeEffectiveTargetsSystemSiblingV2EmptyPrimaryUnchanged(t *testing.T) {
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
		t.Fatalf("v2 intentionally-empty primary effective = %s, must stay empty", got.String())
	}
}

// TestBuildPhasePlanV1SystemSiblingSatisfiesEmptyTargetGate is the direct
// writer/BuildPhasePlan-level regression. It feeds the normalized effective
// targets of the 128C/4NUMA mixed-projection shape into BuildPhasePlan under
// cgroup-v1 semantics (AllowEmptyTarget=false) with a NON-EMPTY current system
// sibling cgroup. After normalization the controlled system rel must carry the
// 8-core reclaim union, so validateExecutableEmptyTargets must NOT raise
// UnsupportedEmptyTargetError. Pre-fix (system desired empty) the same setup
// raises it for the controlled system rel.
func TestBuildPhasePlanV1SystemSiblingSatisfiesEmptyTargetGate(t *testing.T) {
	t.Parallel()

	specs, cpuDetails := auditReclaimSiblingSystemShape()
	dag := mustPlanDAG(t, specs)
	requiredByRel := RequiredCPUSetByRelFromNodeSpecs(specs)

	effective, err := computeEffectiveTargets(dag, false, cpuDetails, nil, requiredByRel)
	if err != nil {
		t.Fatalf("computeEffectiveTargets: %v", err)
	}

	// The normalized system sibling must already carry the reclaim union before
	// the writer gate runs.
	if got := effective["custom-reclaim-root"]; got.Size() != 8 {
		t.Fatalf("normalized system sibling desired = %d (%s), want 8", got.Size(), got.String())
	}

	reclaimPerNUMA := map[int]machine.CPUSet{
		0: machine.NewCPUSet(0, 1),
		1: machine.NewCPUSet(32, 33),
		2: machine.NewCPUSet(64, 65),
		3: machine.NewCPUSet(96, 97),
	}
	bucketUnion := machine.NewCPUSet()
	all := machine.NewCPUSet()
	for c := 0; c < 128; c++ {
		all = all.Union(machine.NewCPUSet(c))
	}
	for _, cpus := range claimPerNUMAValues(reclaimPerNUMA) {
		bucketUnion = bucketUnion.Union(cpus)
	}
	nonReclaim := all.Difference(bucketUnion)

	// Current kernel state: the system sibling cgroup currently holds the
	// reclaim union (non-empty), so an empty desired target is forbidden on v1.
	entries := map[string]EntryState{
		"kubepods":            {CPUs: nonReclaim},
		"reclaimed":           {CPUs: bucketUnion},
		"custom-reclaim-root": {CPUs: bucketUnion},
	}
	children := map[string][]ChildRef{}
	for numaID, cpus := range reclaimPerNUMA {
		rel := "reclaimed/reclaimed-" + itoa(numaID)
		entries[rel] = EntryState{CPUs: cpus}
		children["reclaimed"] = append(children["reclaimed"], ChildRef{Name: rel})
	}
	snapshot := planSnapshot(entries, map[DomainID]machine.CPUSet{
		DomainPrimary: nonReclaim,
		DomainReclaim: bucketUnion,
	})
	snapshot.Children = children
	snapshot.DomainByRel = map[string]DomainID{
		"kubepods":  DomainPrimary,
		"reclaimed": DomainReclaim,
	}
	for numaID := range reclaimPerNUMA {
		snapshot.DomainByRel["reclaimed/reclaimed-"+itoa(numaID)] = DomainReclaim
	}
	snapshot.DomainByRel["custom-reclaim-root"] = DomainReclaim

	_, buildErr := BuildPhasePlan(PhasePlanInput{
		Kind:         PhaseDrain,
		DAG:          dag,
		Snapshot:     snapshot,
		DesiredByRel: effective,
		AllowedCPUs:  all,
		// cgroup v1: empty controlled targets are forbidden.
		AllowEmptyTarget: false,
		Capabilities:     cgroupV1Policy.capabilities(true),
		CPUDetails:       cpuDetails,
		Budget:           NewBudgetTracker(ConvergenceBudget{}),
	})
	var targetErr *UnsupportedEmptyTargetError
	if errors.As(buildErr, &targetErr) {
		t.Fatalf("BuildPhasePlan raised UnsupportedEmptyTargetError after normalization: rel=%q source=%v current=%s",
			targetErr.Rel, targetErr.Source, targetErr.Current.String())
	}
}

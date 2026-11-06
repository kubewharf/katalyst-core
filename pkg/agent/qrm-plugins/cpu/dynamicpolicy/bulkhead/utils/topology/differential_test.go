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
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

// planFingerprint deterministically serializes a PhasePlan for differential
// testing. Two plans that are semantically equivalent must produce the same
// fingerprint. The fingerprint covers every externally-visible field.
func planFingerprint(plan PhasePlan) string {
	var b strings.Builder
	b.WriteString("planID=")
	b.WriteString(plan.PlanID)
	b.WriteString("\nconvID=")
	b.WriteString(plan.ConvergenceID)
	b.WriteString("\nkind=")
	b.WriteString(string(plan.Kind))
	b.WriteString("\nallowEmpty=")
	b.WriteString(fmt.Sprintf("%v", plan.AllowEmptyTarget))
	b.WriteString("\ncap=")
	b.WriteString(fmt.Sprintf("%+v", plan.Capabilities))

	// ControlledRels
	controlled := append([]string(nil), plan.ControlledRels...)
	sort.Strings(controlled)
	b.WriteString("\ncontrolled=")
	b.WriteString(strings.Join(controlled, ","))

	// FailClosedRoots
	fcr := append([]string(nil), plan.FailClosedRoots...)
	sort.Strings(fcr)
	b.WriteString("\nfcr=")
	b.WriteString(strings.Join(fcr, ","))

	// TargetByRel (sorted by rel)
	rels := make([]string, 0, len(plan.TargetByRel))
	for rel := range plan.TargetByRel {
		rels = append(rels, rel)
	}
	sort.Strings(rels)
	b.WriteString("\ntargets[")
	for _, rel := range rels {
		t := plan.TargetByRel[rel]
		fmt.Fprintf(&b, "%s:cpus=%s;mems=%s;", rel, t.CPUs.String(), t.Mems)
	}
	b.WriteString("]")

	// CanonicalTargetByRel (sorted by rel)
	crels := make([]string, 0, len(plan.CanonicalTargetByRel))
	for rel := range plan.CanonicalTargetByRel {
		crels = append(crels, rel)
	}
	sort.Strings(crels)
	b.WriteString("\ncanonical[")
	for _, rel := range crels {
		t := plan.CanonicalTargetByRel[rel]
		fmt.Fprintf(&b, "%s:cpus=%s;mems=%s;", rel, t.CPUs.String(), t.Mems)
	}
	b.WriteString("]")

	// Operations (already sorted by buildPlanOperations, but re-sort by rel+direction for stability)
	type opKey struct {
		rel, dir, childUnion, curCPUs, curMems, tgtCPUs, tgtMems, parentRel string
		ownMems, writeMems                                                  bool
	}
	ops := make([]opKey, 0, len(plan.Operations))
	for _, op := range plan.Operations {
		ops = append(ops, opKey{
			rel: op.Rel, dir: string(op.Direction),
			childUnion: op.ExpectedChildUnion.String(),
			curCPUs:    op.ExpectedCurrent.CPUs.String(), curMems: op.ExpectedCurrent.Mems,
			tgtCPUs: op.Target.CPUs.String(), tgtMems: op.Target.Mems,
			parentRel: op.ParentRel,
			ownMems:   op.OwnsMems, writeMems: op.WriteMems,
		})
	}
	sort.Slice(ops, func(i, j int) bool {
		if ops[i].rel != ops[j].rel {
			return ops[i].rel < ops[j].rel
		}
		return ops[i].dir < ops[j].dir
	})
	b.WriteString("\nops[")
	for _, op := range ops {
		fmt.Fprintf(&b, "%s/%s:childUnion=%s;cur=%s/%s;tgt=%s/%s;parent=%s;ownMems=%v;writeMems=%v;",
			op.rel, op.dir, op.childUnion, op.curCPUs, op.curMems, op.tgtCPUs, op.tgtMems, op.parentRel, op.ownMems, op.writeMems)
	}
	b.WriteString("]")

	// CostUpperBound
	fmt.Fprintf(&b, "\ncost=%+v", plan.CostUpperBound)
	return b.String()
}

func buildPlanForFingerprint(tb testing.TB, shape string, size int) PhasePlan {
	tb.Helper()
	dag, snapshot, desired := planTreeFixture(tb, shape, size)
	budget := NewBudgetTracker(DefaultConvergenceBudget())
	plan, err := BuildPhasePlan(PhasePlanInput{
		Kind:         PhaseExpand,
		DAG:          dag,
		Snapshot:     snapshot,
		DesiredByRel: desired,
		AllowedCPUs:  machine.NewCPUSet(0, 1),
		Budget:       budget,
	})
	if err != nil {
		tb.Fatalf("BuildPhasePlan(%s,%d): %v", shape, size, err)
	}
	return plan
}

// TestDifferentialPlanEquivalence is the golden-output differential test.
// Run with -update-golden to regenerate expected values.
func TestDifferentialPlanEquivalence(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		shape string
		size  int
	}{
		{"wide_10", "wide", 10},
		{"wide_100", "wide", 100},
		{"deep_10", "deep", 10},
		{"deep_100", "deep", 100},
		{"deep_200", "deep", 200},
	}

	// Golden PlanIDs captured from the planner implementation before the
	// performance refactors (incremental hash, memoized depth/childRels,
	// interned mems). These are the strongest equivalence invariant: two
	// semantically identical plans MUST produce the same PlanID.
	goldenPlanIDs := map[string]string{
		"wide_10":  "761f31fab090d4cfa567199bcf90953bbd230d36aefe472049fdf6a03a40923a",
		"wide_100": "8621575c1eb34367c07e5bc834ec418c5cf18b7d3061a9729abb013c623cae14",
		"deep_10":  "8415aa6132e974dd45be8ec9fcdc029506e68646eba38399d4fb06d89c962e91",
		"deep_100": "e87145a93c2fe07d41a2b6d4bb69ee35eaa56d99f67e0fade52e71b1c843709c",
		"deep_200": "3fbeb493db776bb522424e7646eddee9852b414bb61ca38082cd6fbc24b362df",
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			plan := buildPlanForFingerprint(t, tc.shape, tc.size)

			if plan.PlanID == "" {
				t.Fatalf("PlanID is empty")
			}
			for i, op := range plan.Operations {
				if op.PlanID != plan.PlanID {
					t.Fatalf("operation %d PlanID mismatch: op=%s plan=%s", i, op.PlanID, plan.PlanID)
				}
			}

			// Determinism: rebuilding must produce the same result.
			plan2 := buildPlanForFingerprint(t, tc.shape, tc.size)
			if plan2.PlanID != plan.PlanID {
				t.Fatalf("non-deterministic PlanID: first=%s second=%s", plan.PlanID, plan2.PlanID)
			}
			if fp, fp2 := planFingerprint(plan), planFingerprint(plan2); fp != fp2 {
				t.Fatalf("non-deterministic fingerprint")
			}

			if expected, ok := goldenPlanIDs[tc.name]; ok {
				if plan.PlanID != expected {
					t.Fatalf("PlanID drift: got=%s want=%s", plan.PlanID, expected)
				}
			}
		})
	}
}

// TestBuildPlanOperationsNilChildRelsByRelFallback is a regression test for the
// E2E admission failure where buildPlanOperations was called with a nil
// childRelsByRel (from the coordinator replan path), causing ExpectedChildUnion
// to be empty for parent rels that DO have children. This triggered
// "projected child union differs from frozen evidence" and replan exhaustion.
func TestBuildPlanOperationsNilChildRelsByRelFallback(t *testing.T) {
	t.Parallel()
	for _, shape := range []string{"deep", "wide"} {
		shape := shape
		t.Run(shape, func(t *testing.T) {
			t.Parallel()
			_, snapshot, _ := planTreeFixture(t, shape, 20)
			if snapshot == nil {
				t.Skip("fixture unavailable")
			}
			depthByRel, relOrder := buildSnapshotDepthByRel(snapshot, nil)
			domainByRel, parentByRel := buildPlannerRelations(snapshot, nil, depthByRel, relOrder.relsAsc, relOrder.childRelsByRel, nil)

			// Shrink every rel to empty so operations are generated for ALL rels,
			// including parent rels that have children.
			targets := make(map[string]CPUSetTarget, len(snapshot.Entries))
			for rel, entry := range snapshot.Entries {
				targets[rel] = CPUSetTarget{CPUs: machine.NewCPUSet(), Mems: entry.Mems}
			}

			operationCount, err := countPlanOperations(PhaseDrain, HierarchyCapabilities{EmptyConfiguredCPUSet: true}, targets, snapshot, nil, nil)
			if err != nil {
				t.Fatalf("countPlanOperations: %v", err)
			}

			// Run with proper childRelsByRel (optimized path).
			opsWithRel := buildPlanOperations(
				PhaseDrain, true, HierarchyCapabilities{EmptyConfiguredCPUSet: true},
				targets, snapshot, depthByRel, domainByRel, parentByRel,
				nil, operationCount, nil, relOrder.childRelsByRel,
			)

			// Run with nil childRelsByRel (coordinator replan fallback path).
			opsNil := buildPlanOperations(
				PhaseDrain, true, HierarchyCapabilities{EmptyConfiguredCPUSet: true},
				targets, snapshot, depthByRel, domainByRel, parentByRel,
				nil, operationCount, nil, nil,
			)

			if len(opsWithRel) != len(opsNil) {
				t.Fatalf("operation count mismatch: withChildRels=%d nil=%d", len(opsWithRel), len(opsNil))
			}
			// Build a lookup by rel for comparison.
			nilByRel := make(map[string]PlanOperation, len(opsNil))
			for _, op := range opsNil {
				nilByRel[op.Rel] = op
			}
			for i := range opsWithRel {
				rel := opsWithRel[i].Rel
				opNil, ok := nilByRel[rel]
				if !ok {
					t.Fatalf("op %s missing from nil-childRels result", rel)
				}
				if !opsWithRel[i].ExpectedChildUnion.Equals(opNil.ExpectedChildUnion) {
					t.Fatalf("op %s ExpectedChildUnion mismatch: withRel=%s nil=%s",
						rel, opsWithRel[i].ExpectedChildUnion.String(), opNil.ExpectedChildUnion.String())
				}
				// Critical assertion: any parent with children must have non-empty ExpectedChildUnion.
				if children, hasChildren := relOrder.childRelsByRel[rel]; hasChildren && len(children) > 0 {
					if opNil.ExpectedChildUnion.IsEmpty() {
						t.Fatalf("op %s ExpectedChildUnion is empty but has %d children (nil childRelsByRel regression)",
							rel, len(children))
					}
				}
			}
		})
	}
}

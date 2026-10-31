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
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/bulkhead/utils/topology/deadlock"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

type overlapChurnReplayFixture struct {
	Source              string `json:"source"`
	CPUCount            int    `json:"cpu_count"`
	PrimaryCurrent      [2]int `json:"primary_current"`
	ReclaimCurrent      [2]int `json:"reclaim_current"`
	PrimaryDesired      [2]int `json:"primary_desired"`
	ReclaimDesired      [2]int `json:"reclaim_desired"`
	PendingLeafCount    int    `json:"pending_leaf_count"`
	ProtectedRelCount   int    `json:"protected_rel_count"`
	DeadlockProbeBudget int    `json:"deadlock_probe_budget"`
}

func TestBuildPhasePlanDetectsPrimaryReclaimSingletonV1Deadlock(t *testing.T) {
	t.Parallel()
	input := primaryReclaimSwapInput(t, machine.NewCPUSet(0), machine.NewCPUSet(1))
	_, err := BuildPhasePlan(input)
	var structural *deadlock.StructuralV1NonEmptyDeadlock
	if !errors.As(err, &structural) {
		t.Fatalf("BuildPhasePlan error = %v, want deadlock.StructuralV1NonEmptyDeadlock", err)
	}
	if structural.Analysis.Completeness != deadlock.ProbeComplete || len(structural.Analysis.Atoms) != 2 {
		t.Fatalf("analysis = %+v, want complete two-atom proof", structural.Analysis)
	}
}

func TestBuildPhasePlanFindsSafeSeedWhenOneSideCanRelease(t *testing.T) {
	t.Parallel()
	input := primaryReclaimSwapInput(t, machine.NewCPUSet(0, 2), machine.NewCPUSet(1))
	input.DesiredByRel["primary"] = machine.NewCPUSet(1, 2)
	plan, err := BuildPhasePlan(input)
	if err != nil {
		t.Fatalf("BuildPhasePlan: %v", err)
	}
	if len(plan.Operations) == 0 {
		t.Fatal("safe multi-CPU seed should produce a drain operation")
	}
}

func TestBuildPhasePlanUsesProvenSafeSeedInsteadOfArbitraryHeldCPUs(t *testing.T) {
	t.Parallel()
	dag := mustPlanDAG(t, []NodeSpec{
		{Rel: "primary-a", Role: TopoNodeRolePrimary, Domain: DomainPrimary, CPUs: machine.NewCPUSet(3), TrustAnchor: true},
		{Rel: "primary-b", Role: TopoNodeRolePrimary, Domain: DomainPrimary, CPUs: machine.NewCPUSet(3), TrustAnchor: true},
		{Rel: "primary-c", Role: TopoNodeRolePrimary, Domain: DomainPrimary, CPUs: machine.NewCPUSet(4), TrustAnchor: true},
		{Rel: "reclaim", Role: TopoNodeRoleReclaim, Domain: DomainReclaim, CPUs: machine.NewCPUSet(0, 1, 2), TrustAnchor: true},
	})
	snapshot := planSnapshot(map[string]EntryState{
		"primary-a": {Identity: CgroupIdentity{Inode: 1}, CPUs: machine.NewCPUSet(0)},
		"primary-b": {Identity: CgroupIdentity{Inode: 2}, CPUs: machine.NewCPUSet(1)},
		"primary-c": {Identity: CgroupIdentity{Inode: 3}, CPUs: machine.NewCPUSet(2, 4)},
		"reclaim":   {Identity: CgroupIdentity{Inode: 4}, CPUs: machine.NewCPUSet(3)},
	}, map[DomainID]machine.CPUSet{
		DomainPrimary: machine.NewCPUSet(0, 1, 2, 4),
		DomainReclaim: machine.NewCPUSet(3),
	})
	snapshot.DomainByRel = map[string]DomainID{
		"primary-a": DomainPrimary,
		"primary-b": DomainPrimary,
		"primary-c": DomainPrimary,
		"reclaim":   DomainReclaim,
	}
	snapshot.ID = fingerprintSnapshot(snapshot)

	plan, err := BuildPhasePlan(PhasePlanInput{
		Kind: PhaseDrain, DAG: dag, Snapshot: snapshot,
		DesiredByRel: map[string]machine.CPUSet{
			"primary-a": machine.NewCPUSet(3),
			"primary-b": machine.NewCPUSet(3),
			"primary-c": machine.NewCPUSet(4),
			"reclaim":   machine.NewCPUSet(0, 1, 2),
		},
		AllowedCPUs: machine.NewCPUSet(0, 1, 2, 3, 4),
		Selection: DrainSelectionPolicy{
			MaxCPUsDrainRatio:         0.5,
			RequirePairedSwapProgress: true,
		},
		Budget: NewBudgetTracker(ConvergenceBudget{}),
	})
	if err != nil {
		t.Fatalf("BuildPhasePlan: %v", err)
	}
	if !plan.DrainBatch[DomainPrimary].Contains(2) {
		t.Fatalf("primary drain batch = %s, want proven safe seed CPU 2", plan.DrainBatch[DomainPrimary].String())
	}
	if got := plan.TargetByRel["primary-c"].CPUs; got.Contains(2) {
		t.Fatalf("primary-c drain target = %s, want safe seed CPU 2 released", got.String())
	}
}

func TestBuildPhasePlanMandatoryDrainSeedIncludesCompleteSMTCore(t *testing.T) {
	t.Parallel()
	input, _ := smt2RequiredCoreDeadlockInput(t, true, 0)

	plan, err := BuildPhasePlan(input)
	if err != nil {
		t.Fatalf("BuildPhasePlan: %v", err)
	}
	want := machine.NewCPUSet(2, 5)
	if got := plan.DrainBatch[DomainPrimary]; !got.Equals(want) {
		t.Fatalf("primary mandatory drain seed = %s, want complete SMT2 core %s",
			got.String(), want.String())
	}
	wantTarget := machine.NewCPUSet(4)
	if got := plan.TargetByRel["primary-release"].CPUs; !got.Equals(wantTarget) {
		t.Fatalf("primary-release target = %s, want %s after releasing complete SMT2 core",
			got.String(), wantTarget.String())
	}
	var drainOperation *PlanOperation
	for i := range plan.Operations {
		if plan.Operations[i].Rel == "primary-release" {
			drainOperation = &plan.Operations[i]
			break
		}
	}
	if drainOperation == nil {
		t.Fatalf("operations = %+v, want primary-release drain operation", plan.Operations)
	}
	if drainOperation.Direction != WriteShrink || !drainOperation.Target.CPUs.Equals(wantTarget) {
		t.Fatalf("primary-release operation = %+v, want shrink target %s",
			*drainOperation, wantTarget.String())
	}
	if removed := drainOperation.ExpectedCurrent.CPUs.Difference(drainOperation.Target.CPUs); !removed.Equals(want) {
		t.Fatalf("primary-release drain operation removes %s, want complete SMT2 core %s; operation=%+v",
			removed.String(), want.String(), *drainOperation)
	}
}

func TestBuildPhasePlanMissingCompleteCoreReleaseWitnessFailsClosedDeterministically(t *testing.T) {
	t.Parallel()
	const attempts = 16
	signatures := make(map[string]struct{}, attempts)
	for attempt := 0; attempt < attempts; attempt++ {
		input, signature := smt2RequiredCoreDeadlockInput(t, false, attempt)
		signatures[signature] = struct{}{}
		plan, err := BuildPhasePlan(input)
		if err == nil {
			t.Fatalf("attempt %d BuildPhasePlan succeeded with partial mandatory drain seed %s; "+
				"want fail-closed without a complete SMT2 core release witness",
				attempt, plan.DrainBatch[DomainPrimary].String())
		}
		if !errors.Is(err, deadlock.ErrIncompleteRequiredCoreReleaseWitness) {
			t.Fatalf("attempt %d BuildPhasePlan error = %v, want errors.Is(_, deadlock.ErrIncompleteRequiredCoreReleaseWitness)",
				attempt, err)
		}
	}
	if len(signatures) <= 5 {
		t.Fatalf("%d attempts produced only %d distinct insertion signatures, want > 5: %v",
			attempts, len(signatures), signatures)
	}
}

func TestDeadlockAnalysisDoesNotCallPendingProtectionStructural(t *testing.T) {
	t.Parallel()
	input := primaryReclaimSwapInput(t, machine.NewCPUSet(0), machine.NewCPUSet(1))
	input.ProtectedByRel = map[string]machine.CPUSet{"primary": machine.NewCPUSet(0)}
	_, err := BuildPhasePlan(input)
	var structural *deadlock.StructuralV1NonEmptyDeadlock
	if errors.As(err, &structural) {
		t.Fatalf("pending protection must not be classified structural: %v", err)
	}
}

func TestDeadlockAnalysisDoesNotCallUnmaterializedPendingProtectionStructural(t *testing.T) {
	t.Parallel()
	input := primaryReclaimSwapInput(t, machine.NewCPUSet(0), machine.NewCPUSet(1))
	input.ProtectedPending = machine.NewCPUSet(0)
	_, err := BuildPhasePlan(input)
	var structural *deadlock.StructuralV1NonEmptyDeadlock
	if errors.As(err, &structural) {
		t.Fatalf("unmaterialized pending protection must not be classified structural: %v", err)
	}
}

func TestDeadlockAnalysisBudgetExhaustionFailsClosed(t *testing.T) {
	t.Parallel()
	input := primaryReclaimSwapInput(t, machine.NewCPUSet(0), machine.NewCPUSet(1))
	input.Budget = NewBudgetTracker(ConvergenceBudget{MaxDeadlockProbeOperations: 2})
	analysis, err := analyzeV1Deadlock(input)
	if !errors.Is(err, ErrDeadlockProbeBudgetExceeded) {
		t.Fatalf("analyzeV1Deadlock error = %v, want ErrDeadlockProbeBudgetExceeded", err)
	}
	if analysis.Completeness != deadlock.ProbeIndeterminate {
		t.Fatalf("completeness = %s, want %s", analysis.Completeness, deadlock.ProbeIndeterminate)
	}
	if analysis.SafeSeed != nil {
		t.Fatalf("safe seed = %+v, want nil under incomplete probe", analysis.SafeSeed)
	}
	if analysis.ProbeStats.ProbeLimit != 2 {
		t.Fatalf("probe limit = %d, want explicit limit 2", analysis.ProbeStats.ProbeLimit)
	}
	if after := input.Budget.Usage(); after.DeadlockProbeOperations != 2 {
		t.Fatalf("deadlock probe operations = %d, want shared-budget limit 2", after.DeadlockProbeOperations)
	}

	_, err = BuildPhasePlan(input)
	if !errors.Is(err, ErrDeadlockProbeBudgetExceeded) {
		t.Fatalf("BuildPhasePlan error = %v, want fail-closed deadlock probe budget error", err)
	}
}

func TestDeadlockAnalysisOverlapChurnReplayReportsInputScale(t *testing.T) {
	t.Parallel()
	input, fixture := loadOverlapChurnReplayInput(t)

	analysis, err := analyzeV1Deadlock(input)
	if err != nil {
		t.Fatalf("analyzeV1Deadlock: %v", err)
	}
	if analysis.Completeness != deadlock.ProbeComplete {
		t.Fatalf("analysis completeness = %s, want %s", analysis.Completeness, deadlock.ProbeComplete)
	}
	stats := analysis.ProbeStats
	if stats.Atoms != 96 || stats.SnapshotEntries != 98 ||
		stats.SnapshotChildEdges != 96 || stats.ProtectedRels != 12 {
		t.Fatalf("unexpected probe stats: %+v; fixture source=%q", stats, fixture.Source)
	}
}

func TestDeadlockAnalysisOverlapChurnReplayReducesProbeOperations(t *testing.T) {
	t.Parallel()
	input, fixture := loadOverlapChurnReplayInput(t)
	input.Budget = NewBudgetTracker(ConvergenceBudget{MaxDeadlockProbeOperations: 1_000_000})

	before := measureLegacyDeadlockProbeOperations(t, input)
	analysis, err := analyzeV1Deadlock(input)
	if err != nil {
		t.Fatalf("analyzeV1Deadlock optimized replay: %v", err)
	}
	after := analysis.ProbeStats.ProbeOperations
	if before != 115080 {
		t.Fatalf("legacy probe operations = %d, want fixture baseline 115080", before)
	}
	if after != 942 {
		t.Fatalf("optimized probe operations = %d, want fixture baseline 942", after)
	}
	if after >= before {
		t.Fatalf("probe operations before=%d after=%d, want preaggregation to reduce work; fixture source=%q",
			before, after, fixture.Source)
	}
	reduction := 100 * (1 - float64(after)/float64(before))
	t.Logf("probe_operations before=%d after=%d reduction=%.2f%%", before, after, reduction)
}

func TestDeadlockAnalysisOverlapChurnReplayDefaultBudgetCompletes(t *testing.T) {
	t.Parallel()
	input, _ := loadOverlapChurnReplayInput(t)
	input.Budget = NewBudgetTracker(DefaultConvergenceBudget())

	analysis, err := analyzeV1Deadlock(input)
	if err != nil {
		t.Fatalf("analyzeV1Deadlock: %v", err)
	}
	if analysis.Completeness != deadlock.ProbeComplete {
		t.Fatalf("analysis completeness = %s, want %s", analysis.Completeness, deadlock.ProbeComplete)
	}
	if got := input.Budget.Usage().DeadlockProbeOperations; got != 942 {
		t.Fatalf("default-budget probe operations = %d, want 942", got)
	}
}

func TestDeadlockAnalysisScaledOverlapCompletesWithinDefaultBudget(t *testing.T) {
	t.Parallel()
	golden := map[int]int{96: 942, 192: 1902, 384: 3822}
	contextGolden := map[int]int{96: 606, 192: 1182, 384: 2334}
	baseGolden := map[int]int{96: 194, 192: 386, 384: 770}
	for _, cpuCount := range []int{96, 192, 384} {
		cpuCount := cpuCount
		t.Run(fmt.Sprintf("%d-cpu", cpuCount), func(t *testing.T) {
			t.Parallel()
			input := buildScaledOverlapChurnInput(t, cpuCount, 12)
			input.Budget = NewBudgetTracker(DefaultConvergenceBudget())

			analysis, err := analyzeV1Deadlock(input)
			if err != nil {
				t.Fatalf("analyzeV1Deadlock(%d CPUs): %v", cpuCount, err)
			}
			if analysis.Completeness != deadlock.ProbeComplete {
				t.Fatalf("analysis completeness = %s, want %s", analysis.Completeness, deadlock.ProbeComplete)
			}
			if got := analysis.ProbeStats.ProbeOperations; got >= deadlock.DefaultDeadlockProbeBudget {
				t.Fatalf("probe operations = %d, want < %d", got, deadlock.DefaultDeadlockProbeBudget)
			}
			if got := analysis.ProbeStats.ProbeOperations; got != golden[cpuCount] {
				t.Fatalf("probe operations = %d, want golden %d", got, golden[cpuCount])
			}
			if got := analysis.ProbeStats.ContextOperations; got != contextGolden[cpuCount] {
				t.Fatalf("context operations = %d, want golden %d", got, contextGolden[cpuCount])
			}
			if got := analysis.ProbeStats.BaseOperations; got != baseGolden[cpuCount] {
				t.Fatalf("base operations = %d, want golden %d", got, baseGolden[cpuCount])
			}
			if got := analysis.ProbeStats.AtomOperations; got != golden[cpuCount]-contextGolden[cpuCount] {
				t.Fatalf("atom operations = %d, want golden %d",
					got, golden[cpuCount]-contextGolden[cpuCount])
			}
			t.Logf("cpu_count=%d probe_operations=%d", cpuCount, analysis.ProbeStats.ProbeOperations)
		})
	}
}

func TestDeadlockAnalysisE2EShapeCompletesWithinDefaultBudget(t *testing.T) {
	t.Parallel()
	input := buildE2EShapeDeadlockInput(t)
	input.Budget = NewBudgetTracker(DefaultConvergenceBudget())

	analysis, err := analyzeV1Deadlock(input)
	if err != nil {
		t.Fatalf("analyzeV1Deadlock E2E shape: %v", err)
	}
	if analysis.Completeness != deadlock.ProbeComplete {
		t.Fatalf("analysis completeness = %s, want %s", analysis.Completeness, deadlock.ProbeComplete)
	}
	if analysis.ProbeStats.SnapshotEntries != 232 {
		t.Fatalf("snapshot entries = %d, want 232", analysis.ProbeStats.SnapshotEntries)
	}
	if analysis.ProbeStats.SnapshotChildEdges != 219 {
		t.Fatalf("snapshot child edges = %d, want 219", analysis.ProbeStats.SnapshotChildEdges)
	}
	if analysis.ProbeStats.ProtectedRels != 2 {
		t.Fatalf("protected rels = %d, want 2", analysis.ProbeStats.ProtectedRels)
	}
	if analysis.ProbeStats.ProtectedPendingCPUs != 12 {
		t.Fatalf("protected pending CPUs = %d, want 12", analysis.ProbeStats.ProtectedPendingCPUs)
	}
	if analysis.ProbeStats.ProbeOperations >= deadlock.DefaultDeadlockProbeBudget {
		t.Fatalf("probe operations = %d, want < %d",
			analysis.ProbeStats.ProbeOperations, deadlock.DefaultDeadlockProbeBudget)
	}
	if analysis.ProbeStats.ProbeOperations != 1437 ||
		analysis.ProbeStats.ContextOperations != 1357 ||
		analysis.ProbeStats.BaseOperations != 451 ||
		analysis.ProbeStats.AtomOperations != 80 ||
		analysis.ProbeStats.ContextPhase != "complete" {
		t.Fatalf("unexpected E2E-shape probe stats: %+v", analysis.ProbeStats)
	}
	if got := contextPhaseOperationTotal(analysis.ProbeStats); got != analysis.ProbeStats.ContextOperations {
		t.Fatalf("context phase operation total = %d, want %d: %+v",
			got, analysis.ProbeStats.ContextOperations, analysis.ProbeStats)
	}
	t.Logf("probe stats: %+v", analysis.ProbeStats)
}

func TestDeadlockAnalysisUnprotectedABCE2EShapeCompletesWithinDefaultBudget(t *testing.T) {
	t.Parallel()
	input := buildUnprotectedABCE2EInput(t)
	input.Budget = NewBudgetTracker(DefaultConvergenceBudget())

	analysis, err := analyzeV1Deadlock(input)
	if err != nil {
		t.Fatalf("analyzeV1Deadlock unprotected A-B-C shape: %v", err)
	}
	if analysis.Completeness != deadlock.ProbeComplete {
		t.Fatalf("analysis completeness = %s, want %s", analysis.Completeness, deadlock.ProbeComplete)
	}
	if analysis.ProbeStats.SnapshotEntries != 262 ||
		analysis.ProbeStats.SnapshotChildEdges != 249 ||
		analysis.ProbeStats.Atoms != 23 ||
		analysis.ProbeStats.ProtectedRels != 0 ||
		analysis.ProbeStats.ProtectedPendingCPUs != 0 {
		t.Fatalf("unexpected unprotected A-B-C stats: %+v", analysis.ProbeStats)
	}
	if got := contextPhaseOperationTotal(analysis.ProbeStats); got != analysis.ProbeStats.ContextOperations {
		t.Fatalf("context phase operation total = %d, want %d: %+v",
			got, analysis.ProbeStats.ContextOperations, analysis.ProbeStats)
	}
	if analysis.ProbeStats.ProbeLimit != 17405 {
		t.Fatalf("auto probe limit = %d, want 17405", analysis.ProbeStats.ProbeLimit)
	}
	if analysis.ProbeStats.ProbeOperations != 9008 {
		t.Fatalf("probe operations = %d, want 9008", analysis.ProbeStats.ProbeOperations)
	}
	t.Logf("probe stats: %+v", analysis.ProbeStats)
}

func TestDeadlockAnalysisAutoBudgetAccountsProtectedPendingCPUs(t *testing.T) {
	t.Parallel()
	baseInput := buildE2EShapeDeadlockInput(t)
	baseInput.Budget = NewBudgetTracker(DefaultConvergenceBudget())
	baseAnalysis, err := analyzeV1Deadlock(baseInput)
	if err != nil {
		t.Fatalf("analyzeV1Deadlock base pending CPUs: %v", err)
	}

	input := buildE2EShapeDeadlockInput(t)
	input.ProtectedPending = replayCPUSet([2]int{0, 38})
	input.Budget = NewBudgetTracker(DefaultConvergenceBudget())

	analysis, err := analyzeV1Deadlock(input)
	if err != nil {
		t.Fatalf("analyzeV1Deadlock with SNB-sized pending CPUs: %v", err)
	}
	if analysis.Completeness != deadlock.ProbeComplete {
		t.Fatalf("analysis completeness = %s, want %s", analysis.Completeness, deadlock.ProbeComplete)
	}
	if analysis.ProbeStats.ProtectedPendingCPUs != 39 {
		t.Fatalf("protected pending CPUs = %d, want 39", analysis.ProbeStats.ProtectedPendingCPUs)
	}
	if analysis.ProbeStats.ProbeLimit <= 4096 {
		t.Fatalf("auto probe limit = %d, want greater than fixed default 4096 for pending CPUs",
			analysis.ProbeStats.ProbeLimit)
	}
	if analysis.ProbeStats.ProbeLimit <= baseAnalysis.ProbeStats.ProbeLimit {
		t.Fatalf("auto probe limit with 39 pending CPUs = %d, want greater than base pending limit %d",
			analysis.ProbeStats.ProbeLimit, baseAnalysis.ProbeStats.ProbeLimit)
	}
	if analysis.ProbeStats.ProbeLimit < analysis.ProbeStats.ProbeOperations {
		t.Fatalf("auto probe limit = %d below actual operations %d: %+v",
			analysis.ProbeStats.ProbeLimit, analysis.ProbeStats.ProbeOperations, analysis.ProbeStats)
	}
}

func TestDeadlockAnalysisAutoBudgetSupportsMultipleRoundsOnSharedTracker(t *testing.T) {
	t.Parallel()
	tracker := NewBudgetTracker(DefaultConvergenceBudget())
	for round := 1; round <= 2; round++ {
		input := buildUnprotectedABCE2EInput(t)
		input.Budget = tracker
		analysis, err := analyzeV1Deadlock(input)
		if err != nil {
			t.Fatalf("round %d analyzeV1Deadlock: %v", round, err)
		}
		if analysis.Completeness != deadlock.ProbeComplete {
			t.Fatalf("round %d completeness = %s, want %s",
				round, analysis.Completeness, deadlock.ProbeComplete)
		}
	}
	if got := tracker.Usage().DeadlockProbeOperations; got != 18016 {
		t.Fatalf("two-round probe operations = %d, want 18016", got)
	}
	if got := tracker.DeadlockProbeLimit(); got != 26413 {
		t.Fatalf("two-round auto probe limit = %d, want 26413", got)
	}
}

func TestDeadlockAnalysisContextBudgetErrorReportsPhase(t *testing.T) {
	t.Parallel()
	input := buildE2EShapeDeadlockInput(t)
	input.Budget = NewBudgetTracker(ConvergenceBudget{MaxDeadlockProbeOperations: 500})

	analysis, err := analyzeV1Deadlock(input)
	if !errors.Is(err, ErrDeadlockProbeBudgetExceeded) {
		t.Fatalf("analyzeV1Deadlock error = %v, want ErrDeadlockProbeBudgetExceeded", err)
	}
	if analysis.ProbeStats.ContextPhase == "" {
		t.Fatalf("context phase is empty: stats=%+v error=%v", analysis.ProbeStats, err)
	}
	if !strings.Contains(err.Error(), "context_phase="+analysis.ProbeStats.ContextPhase) {
		t.Fatalf("error %q does not contain context phase %q",
			err, analysis.ProbeStats.ContextPhase)
	}
	if !strings.Contains(err.Error(), "protected_pending_cpus=") {
		t.Fatalf("error %q does not contain protected pending CPU diagnostics", err)
	}
	if !strings.Contains(err.Error(), "auto_budget=") {
		t.Fatalf("error %q does not contain auto budget diagnostics", err)
	}
}

func TestDeadlockAnalysisProtectedPreaggregationBudgetErrorReportsPhase(t *testing.T) {
	t.Parallel()
	input := buildE2EShapeDeadlockInput(t)
	input.Budget = NewBudgetTracker(ConvergenceBudget{MaxDeadlockProbeOperations: 1})

	analysis, err := analyzeV1Deadlock(input)
	if !errors.Is(err, ErrDeadlockProbeBudgetExceeded) {
		t.Fatalf("analyzeV1Deadlock error = %v, want ErrDeadlockProbeBudgetExceeded", err)
	}
	if analysis.ProbeStats.ContextPhase != "protected_descendant_preaggregation" {
		t.Fatalf("context phase = %q, want protected_descendant_preaggregation",
			analysis.ProbeStats.ContextPhase)
	}
	if analysis.ProbeStats.ContextOperations != 1 {
		t.Fatalf("context operations = %d, want 1", analysis.ProbeStats.ContextOperations)
	}
}

func TestDeadlockProbeCapacityUpperBoundIncludesProtectedAncestorPreaggregation(t *testing.T) {
	t.Parallel()
	const protectedLeafCount = 4080

	entries := make(map[string]EntryState, protectedLeafCount+16)
	rel := "root"
	entries[rel] = EntryState{}
	for depth := 0; depth < 15; depth++ {
		rel = filepath.Join(rel, fmt.Sprintf("level-%02d", depth))
		entries[rel] = EntryState{}
	}
	protectedByRel := make(map[string]machine.CPUSet, protectedLeafCount)
	for leaf := 0; leaf < protectedLeafCount; leaf++ {
		leafRel := filepath.Join(rel, fmt.Sprintf("leaf-%04d", leaf))
		entries[leafRel] = EntryState{}
		protectedByRel[leafRel] = machine.NewCPUSet(0)
	}
	snapshot := planSnapshot(entries, map[DomainID]machine.CPUSet{})

	protectedOperations := protectedAncestorPreaggregationOperations(snapshot, protectedByRel)
	if protectedOperations <= 60_000 {
		t.Fatalf("protected preaggregation operations = %d, want a large shared-chain workload", protectedOperations)
	}
	upperBound := deadlockProbeCapacityUpperBound(
		len(snapshot.Entries), len(snapshot.Entries)-1, 2, 0, protectedOperations)
	if upperBound < protectedOperations {
		t.Fatalf("auto probe capacity = %d, below protected preaggregation operations %d",
			upperBound, protectedOperations)
	}
}

func TestSingleAtomIncrementalProjectionMatchesFullReplay(t *testing.T) {
	t.Parallel()
	input := buildScaledOverlapChurnInput(t, 96, 12)
	input.Budget = nil
	desiredByDomain := desiredDomainUnions(input.DAG, input.DesiredByRel)
	depthByRel, relOrder := buildSnapshotDepthByRel(input.Snapshot, nil)
	domainByRel, parentByRel := buildPlannerRelations(input.Snapshot, input.DAG, depthByRel, relOrder.relsAsc, relOrder.childRelsByRel, nil)
	leavingByDomain := map[DomainID]machine.CPUSet{
		DomainPrimary: input.Snapshot.DomainUnion[DomainPrimary].Difference(desiredByDomain[DomainPrimary]),
		DomainReclaim: input.Snapshot.DomainUnion[DomainReclaim].Difference(desiredByDomain[DomainReclaim]),
	}
	projectionContext, err := buildDrainProjectionContext(
		context.Background(), input.Snapshot, input.ProtectedByRel, nil)
	if err != nil {
		t.Fatalf("buildDrainProjectionContext: %v", err)
	}
	baseInput := DrainProjectionInput{
		PlanInput: input, LeavingByDomain: leavingByDomain,
		DomainByRel: domainByRel, ParentByRel: parentByRel, DepthByRel: depthByRel,
		Context: projectionContext,
	}
	if err := prepareIncrementalDrainProjectionContext(baseInput, projectionContext); err != nil {
		t.Fatalf("prepareIncrementalDrainProjectionContext: %v", err)
	}
	for cpu := 12; cpu < 96; cpu++ {
		source := DomainPrimary
		destination := DomainReclaim
		if cpu >= 48 {
			source, destination = DomainReclaim, DomainPrimary
		}
		atom := deadlock.DrainAtom{
			Source: source, Destination: destination, CPUs: machine.NewCPUSet(cpu),
		}
		atomInput := baseInput
		atomInput.DrainBatch = map[DomainID]machine.CPUSet{
			DomainPrimary: machine.NewCPUSet(),
			DomainReclaim: machine.NewCPUSet(),
			source:        atom.CPUs,
		}
		full, fullErr := projectDrainTargets(atomInput)
		incremental, incrementalErr := projectSingleAtomIncremental(atomInput, atom)
		if (fullErr == nil) != (incrementalErr == nil) {
			t.Fatalf("cpu=%d error mismatch full=%v incremental=%v", cpu, fullErr, incrementalErr)
		}
		if fullErr != nil {
			continue
		}
		for _, rel := range projectionContext.affectedRelsByCPU[cpu] {
			want := full.TargetByRel[rel]
			got := incremental.TargetByRel[rel]
			if !got.CPUs.Equals(want.CPUs) || got.Mems != want.Mems {
				t.Fatalf("cpu=%d rel=%s target=%+v, want %+v", cpu, rel, got, want)
			}
		}
		for _, domain := range []DomainID{DomainPrimary, DomainReclaim} {
			if !incremental.DomainUnion[domain].Equals(full.DomainUnion[domain]) {
				t.Fatalf("cpu=%d domain=%s union=%s, want %s",
					cpu, domain, incremental.DomainUnion[domain].String(), full.DomainUnion[domain].String())
			}
		}
		if !equalCPUSetMap(incremental.EmptyBlockers, full.EmptyBlockers) {
			t.Fatalf("cpu=%d blockers=%v, want %v", cpu, incremental.EmptyBlockers, full.EmptyBlockers)
		}
	}
}

func TestSingleAtomIncrementalProjectionFallsBackForMultiCPUAtom(t *testing.T) {
	t.Parallel()
	input := buildScaledOverlapChurnInput(t, 96, 12)
	input.Budget = nil
	desiredByDomain := desiredDomainUnions(input.DAG, input.DesiredByRel)
	depthByRel, relOrder := buildSnapshotDepthByRel(input.Snapshot, nil)
	domainByRel, parentByRel := buildPlannerRelations(input.Snapshot, input.DAG, depthByRel, relOrder.relsAsc, relOrder.childRelsByRel, nil)
	leavingByDomain := map[DomainID]machine.CPUSet{
		DomainPrimary: input.Snapshot.DomainUnion[DomainPrimary].Difference(desiredByDomain[DomainPrimary]),
		DomainReclaim: input.Snapshot.DomainUnion[DomainReclaim].Difference(desiredByDomain[DomainReclaim]),
	}
	projectionContext, err := buildDrainProjectionContext(
		context.Background(), input.Snapshot, input.ProtectedByRel, nil)
	if err != nil {
		t.Fatalf("buildDrainProjectionContext: %v", err)
	}
	projectionInput := DrainProjectionInput{
		PlanInput: input, LeavingByDomain: leavingByDomain,
		DomainByRel: domainByRel, ParentByRel: parentByRel, DepthByRel: depthByRel,
		Context: projectionContext,
	}
	if err := prepareIncrementalDrainProjectionContext(projectionInput, projectionContext); err != nil {
		t.Fatalf("prepareIncrementalDrainProjectionContext: %v", err)
	}
	atom := deadlock.DrainAtom{
		Source: DomainPrimary, Destination: DomainReclaim, CPUs: machine.NewCPUSet(12, 13),
	}
	projectionInput.DrainBatch = map[DomainID]machine.CPUSet{
		DomainPrimary: atom.CPUs,
		DomainReclaim: machine.NewCPUSet(),
	}
	full, err := projectDrainTargets(projectionInput)
	if err != nil {
		t.Fatalf("full projection: %v", err)
	}
	got, err := projectSingleAtomIncremental(projectionInput, atom)
	if err != nil {
		t.Fatalf("fallback projection: %v", err)
	}
	if len(got.TargetByRel) != len(input.Snapshot.Entries) {
		t.Fatalf("fallback target count = %d, want full snapshot count %d",
			len(got.TargetByRel), len(input.Snapshot.Entries))
	}
	for rel, want := range full.TargetByRel {
		if target := got.TargetByRel[rel]; !target.CPUs.Equals(want.CPUs) || target.Mems != want.Mems {
			t.Fatalf("rel=%s fallback target=%+v, want %+v", rel, target, want)
		}
	}
}

func TestSingleAtomIncrementalProjectionFallbackIncludesPartialCost(t *testing.T) {
	t.Parallel()
	input := buildScaledOverlapChurnInput(t, 96, 12)
	input.Budget = nil
	desiredByDomain := desiredDomainUnions(input.DAG, input.DesiredByRel)
	depthByRel, relOrder := buildSnapshotDepthByRel(input.Snapshot, nil)
	domainByRel, parentByRel := buildPlannerRelations(input.Snapshot, input.DAG, depthByRel, relOrder.relsAsc, relOrder.childRelsByRel, nil)
	leavingByDomain := map[DomainID]machine.CPUSet{
		DomainPrimary: input.Snapshot.DomainUnion[DomainPrimary].Difference(desiredByDomain[DomainPrimary]),
		DomainReclaim: input.Snapshot.DomainUnion[DomainReclaim].Difference(desiredByDomain[DomainReclaim]),
	}
	projectionContext, err := buildDrainProjectionContext(
		context.Background(), input.Snapshot, input.ProtectedByRel, nil)
	if err != nil {
		t.Fatalf("buildDrainProjectionContext: %v", err)
	}
	projectionInput := DrainProjectionInput{
		PlanInput: input, LeavingByDomain: leavingByDomain,
		DomainByRel: domainByRel, ParentByRel: parentByRel, DepthByRel: depthByRel,
		Context: projectionContext,
	}
	if err := prepareIncrementalDrainProjectionContext(projectionInput, projectionContext); err != nil {
		t.Fatalf("prepareIncrementalDrainProjectionContext: %v", err)
	}
	atom := deadlock.DrainAtom{
		Source: DomainPrimary, Destination: DomainReclaim, CPUs: machine.NewCPUSet(12),
	}
	projectionInput.DrainBatch = map[DomainID]machine.CPUSet{
		DomainPrimary: atom.CPUs,
		DomainReclaim: machine.NewCPUSet(),
	}
	full, err := projectDrainTargets(projectionInput)
	if err != nil {
		t.Fatalf("full projection: %v", err)
	}
	leaf := "kubepods/pod-pending-12"
	tampered := projectionContext.baseProjection.TargetByRel[leaf]
	tampered.CPUs = tampered.CPUs.Union(machine.NewCPUSet(13))
	projectionContext.baseProjection.TargetByRel[leaf] = tampered

	fallback, err := projectSingleAtomIncremental(projectionInput, atom)
	if err != nil {
		t.Fatalf("fallback projection: %v", err)
	}
	if fallback.Cost.Total() <= full.Cost.Total() {
		t.Fatalf("fallback cost = %d, want greater than full replay cost %d",
			fallback.Cost.Total(), full.Cost.Total())
	}
}

func equalCPUSetMap(left, right map[string]machine.CPUSet) bool {
	if len(left) != len(right) {
		return false
	}
	for rel, leftCPUs := range left {
		rightCPUs, ok := right[rel]
		if !ok || !leftCPUs.Equals(rightCPUs) {
			return false
		}
	}
	return true
}

func TestDeadlockAnalysisHonorsCanceledContext(t *testing.T) {
	t.Parallel()
	input := primaryReclaimSwapInput(t, machine.NewCPUSet(0), machine.NewCPUSet(1))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	input.Context = ctx
	input.Budget = NewBudgetTracker(ConvergenceBudget{MaxDeadlockProbeOperations: 16})

	_, err := analyzeV1Deadlock(input)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("analyzeV1Deadlock error = %v, want context.Canceled", err)
	}
}

func TestBuildPhasePlanDoesNotCallCycleStructuralWhenFinalDesiredCPUIsVerifiedUnowned(t *testing.T) {
	t.Parallel()
	input := primaryReclaimSwapInput(t, machine.NewCPUSet(0), machine.NewCPUSet(1))
	input.DesiredByRel["primary"] = machine.NewCPUSet(1, 2)

	plan, err := BuildPhasePlan(input)
	if err != nil {
		t.Fatalf("BuildPhasePlan with legal final grow anchor: %v", err)
	}
	if len(plan.Operations) != 0 {
		t.Fatalf("drain operations = %#v, want no drain before verified-unowned grow anchor", plan.Operations)
	}
}

func TestDeadlockAnalysisAppliesNUMABucketConstraintToAggregateEdge(t *testing.T) {
	t.Parallel()
	dag := mustPlanDAG(t, []NodeSpec{
		{Rel: "primary", Role: TopoNodeRolePrimary, Domain: DomainPrimary, CPUs: machine.NewCPUSet(1), TrustAnchor: true},
		{Rel: "reclaim", Role: TopoNodeRoleReclaim, Domain: DomainReclaim, CPUs: machine.NewCPUSet(0, 2), TrustAnchor: true},
		{
			Rel: "reclaim/bucket-0", ParentRel: "reclaim", Role: TopoNodeRoleReclaimNUMABucket,
			Domain: DomainReclaim, CPUs: machine.NewCPUSet(0),
			Constraint: TopologyConstraint{CPUUpperBound: machine.NewCPUSet(0), Scope: TopologyScopeNUMANode},
		},
		{
			Rel: "reclaim/bucket-1", ParentRel: "reclaim", Role: TopoNodeRoleReclaimNUMABucket,
			Domain: DomainReclaim, CPUs: machine.NewCPUSet(2),
			Constraint: TopologyConstraint{CPUUpperBound: machine.NewCPUSet(2), Scope: TopologyScopeNUMANode},
		},
	})
	snapshot := planSnapshot(map[string]EntryState{
		"primary":          {CPUs: machine.NewCPUSet(0)},
		"reclaim":          {CPUs: machine.NewCPUSet(1, 2)},
		"reclaim/bucket-0": {CPUs: machine.NewCPUSet(1)},
		"reclaim/bucket-1": {CPUs: machine.NewCPUSet(2)},
	}, map[DomainID]machine.CPUSet{
		DomainPrimary: machine.NewCPUSet(0), DomainReclaim: machine.NewCPUSet(1, 2),
	})
	snapshot.Children = map[string][]ChildRef{
		"reclaim": {{Name: "bucket-0"}, {Name: "bucket-1"}},
	}
	snapshot.DomainByRel = map[string]DomainID{
		"primary": DomainPrimary, "reclaim": DomainReclaim,
		"reclaim/bucket-0": DomainReclaim, "reclaim/bucket-1": DomainReclaim,
	}
	snapshot.ID = fingerprintSnapshot(snapshot)
	_, err := BuildPhasePlan(PhasePlanInput{
		Kind: PhaseDrain, DAG: dag, Snapshot: snapshot,
		DesiredByRel: map[string]machine.CPUSet{
			"primary": machine.NewCPUSet(1), "reclaim": machine.NewCPUSet(0, 2),
			"reclaim/bucket-0": machine.NewCPUSet(0), "reclaim/bucket-1": machine.NewCPUSet(2),
		},
		AllowedCPUs: machine.NewCPUSet(0, 1, 2),
		Budget:      NewBudgetTracker(ConvergenceBudget{}),
	})
	var structural *deadlock.StructuralV1NonEmptyDeadlock
	if !errors.As(err, &structural) {
		t.Fatalf("BuildPhasePlan error = %v, want bucket-constrained structural deadlock", err)
	}
}

func TestStructuralV1DeadlockRequiresEveryAtomToBeV1EmptyBlocked(t *testing.T) {
	t.Parallel()
	analysis := deadlock.DeadlockAnalysis{
		Completeness: deadlock.ProbeComplete,
		Atoms: []deadlock.DrainAtom{
			{Source: DomainPrimary, Destination: DomainReclaim, CPUs: machine.NewCPUSet(0)},
			{Source: DomainReclaim, Destination: DomainPrimary, CPUs: machine.NewCPUSet(1)},
		},
		AtomClasses: []deadlock.DrainAtomClass{
			deadlock.DrainAtomClassV1Empty,
			deadlock.DrainAtomClassHeld,
		},
		EmptyBlockers: map[string]machine.CPUSet{
			"primary": machine.NewCPUSet(0),
		},
	}
	if structuralV1Deadlock(analysis) {
		t.Fatal("mixed atom blockers must not be classified as an all-v1-empty structural deadlock")
	}
}

func TestStructuralV1DeadlockRejectsProtectedAtom(t *testing.T) {
	t.Parallel()
	analysis := deadlock.DeadlockAnalysis{
		Completeness: deadlock.ProbeComplete,
		Atoms: []deadlock.DrainAtom{
			{Source: DomainPrimary, Destination: DomainReclaim, CPUs: machine.NewCPUSet(0)},
			{Source: DomainReclaim, Destination: DomainPrimary, CPUs: machine.NewCPUSet(1)},
		},
		AtomClasses: []deadlock.DrainAtomClass{
			deadlock.DrainAtomClassV1Empty,
			deadlock.DrainAtomClassProtected,
		},
		EmptyBlockers: map[string]machine.CPUSet{
			"primary": machine.NewCPUSet(0),
		},
		Protected: machine.NewCPUSet(1),
	}
	if structuralV1Deadlock(analysis) {
		t.Fatal("protected atom must not be classified as an all-v1-empty structural deadlock")
	}
}

func primaryReclaimSwapInput(t *testing.T, primary, reclaim machine.CPUSet) PhasePlanInput {
	t.Helper()
	dag := mustPlanDAG(t, []NodeSpec{
		{Rel: "primary", Role: TopoNodeRolePrimary, Domain: DomainPrimary, CPUs: reclaim, TrustAnchor: true},
		{Rel: "reclaim", Role: TopoNodeRoleReclaim, Domain: DomainReclaim, CPUs: primary, TrustAnchor: true},
	})
	snapshot := planSnapshot(map[string]EntryState{
		"primary": {Identity: CgroupIdentity{Inode: 1}, CPUs: primary},
		"reclaim": {Identity: CgroupIdentity{Inode: 2}, CPUs: reclaim},
	}, map[DomainID]machine.CPUSet{
		DomainPrimary: primary, DomainReclaim: reclaim,
	})
	snapshot.DomainByRel = map[string]DomainID{
		"primary": DomainPrimary, "reclaim": DomainReclaim,
	}
	snapshot.ID = fingerprintSnapshot(snapshot)
	return PhasePlanInput{
		Kind: PhaseDrain, DAG: dag, Snapshot: snapshot,
		DesiredByRel: map[string]machine.CPUSet{
			"primary": reclaim, "reclaim": primary,
		},
		AllowedCPUs: primary.Union(reclaim).Union(machine.NewCPUSet(2)),
		Selection: DrainSelectionPolicy{
			RequirePairedSwapProgress: true,
		},
		Budget: NewBudgetTracker(ConvergenceBudget{}),
	}
}

func smt2RequiredCoreDeadlockInput(t *testing.T, completeReleaseWitness bool, attempt int) (PhasePlanInput, string) {
	t.Helper()

	// CPUs 2 and 5 are the two SMT siblings of one physical core. Both are
	// currently owned by the primary source and are mandatory in reclaim's
	// desired target, so deadlock progress must release them atomically.
	primaryReleaseCurrent := machine.NewCPUSet(2, 4, 5)
	primaryReleaseDesired := machine.NewCPUSet(4)
	specs := []NodeSpec{
		{Rel: "primary-held-0", Role: TopoNodeRolePrimary, Domain: DomainPrimary, CPUs: machine.NewCPUSet(3), TrustAnchor: true},
		{Rel: "primary-held-1", Role: TopoNodeRolePrimary, Domain: DomainPrimary, CPUs: machine.NewCPUSet(3), TrustAnchor: true},
		{Rel: "primary-release", Role: TopoNodeRolePrimary, Domain: DomainPrimary, CPUs: primaryReleaseDesired, TrustAnchor: true},
		{Rel: "reclaim", Role: TopoNodeRoleReclaim, Domain: DomainReclaim, CPUs: machine.NewCPUSet(0, 1, 2, 5), TrustAnchor: true},
	}
	entryByRel := map[string]EntryState{
		"primary-held-0": {Identity: CgroupIdentity{Inode: 1}, CPUs: machine.NewCPUSet(0)},
		"primary-held-1": {Identity: CgroupIdentity{Inode: 2}, CPUs: machine.NewCPUSet(1)},
		"primary-release": {
			Identity: CgroupIdentity{Inode: 3},
			CPUs:     primaryReleaseCurrent,
		},
		"reclaim": {Identity: CgroupIdentity{Inode: 4}, CPUs: machine.NewCPUSet(3)},
	}
	if !completeReleaseWitness {
		// CPU 5 remains required by a distinct source leaf. CPU 2 alone is
		// releasable, but that partial witness cannot justify draining the core.
		specs = append(specs, NodeSpec{
			Rel: "primary-holds-sibling", Role: TopoNodeRolePrimary,
			Domain: DomainPrimary, CPUs: machine.NewCPUSet(3), TrustAnchor: true,
		})
		entryByRel["primary-release"] = EntryState{
			Identity: CgroupIdentity{Inode: 3},
			CPUs:     machine.NewCPUSet(2, 4),
		}
		entryByRel["primary-holds-sibling"] = EntryState{
			Identity: CgroupIdentity{Inode: 5},
			CPUs:     machine.NewCPUSet(5),
		}
	}

	rels := []string{"primary-held-0", "primary-held-1", "primary-release", "reclaim"}
	if !completeReleaseWitness {
		rels = append(rels, "primary-holds-sibling")
	}
	rngFor := func(stream int64) *rand.Rand {
		const attemptStride = int64(1_000_003)
		return rand.New(rand.NewSource(int64(attempt+1)*attemptStride + stream))
	}
	rngFor(1).Shuffle(len(specs), func(i, j int) {
		specs[i], specs[j] = specs[j], specs[i]
	})
	shuffledRels := func(stream int64) []string {
		order := append([]string(nil), rels...)
		rngFor(stream).Shuffle(len(order), func(i, j int) {
			order[i], order[j] = order[j], order[i]
		})
		return order
	}
	entryOrder := shuffledRels(2)
	domainOrder := shuffledRels(3)
	desiredOrder := shuffledRels(4)

	domainValueByRel := make(map[string]DomainID, len(rels))
	desiredValueByRel := make(map[string]machine.CPUSet, len(rels))
	for _, rel := range rels {
		domainValueByRel[rel] = DomainPrimary
		desiredValueByRel[rel] = machine.NewCPUSet(3)
	}
	domainValueByRel["reclaim"] = DomainReclaim
	desiredValueByRel["primary-release"] = primaryReleaseDesired
	desiredValueByRel["reclaim"] = machine.NewCPUSet(0, 1, 2, 5)

	entries := make(map[string]EntryState, len(rels))
	domainByRel := make(map[string]DomainID, len(rels))
	desiredByRel := make(map[string]machine.CPUSet, len(rels))
	for _, rel := range entryOrder {
		entries[rel] = entryByRel[rel]
	}
	for _, rel := range domainOrder {
		domainByRel[rel] = domainValueByRel[rel]
	}
	for _, rel := range desiredOrder {
		desiredByRel[rel] = desiredValueByRel[rel]
	}

	dag := mustPlanDAG(t, specs)
	snapshot := planSnapshot(entries, map[DomainID]machine.CPUSet{
		DomainPrimary: machine.NewCPUSet(0, 1, 2, 4, 5),
		DomainReclaim: machine.NewCPUSet(3),
	})
	snapshot.DomainByRel = domainByRel
	snapshot.ID = fingerprintSnapshot(snapshot)

	specOrder := make([]string, len(specs))
	for i := range specs {
		specOrder[i] = specs[i].Rel
	}
	signature := fmt.Sprintf("specs=%s entries=%s domains=%s desired=%s",
		strings.Join(specOrder, ","),
		strings.Join(entryOrder, ","),
		strings.Join(domainOrder, ","),
		strings.Join(desiredOrder, ","))

	return PhasePlanInput{
		Kind: PhaseDrain, DAG: dag, Snapshot: snapshot,
		DesiredByRel: desiredByRel,
		AllowedCPUs:  machine.NewCPUSet(0, 1, 2, 3, 4, 5),
		CPUDetails: machine.CPUDetails{
			0: {NUMANodeID: 0, SocketID: 0, CoreID: 0},
			3: {NUMANodeID: 0, SocketID: 0, CoreID: 0},
			1: {NUMANodeID: 0, SocketID: 0, CoreID: 1},
			4: {NUMANodeID: 0, SocketID: 0, CoreID: 1},
			2: {NUMANodeID: 0, SocketID: 0, CoreID: 2},
			5: {NUMANodeID: 0, SocketID: 0, CoreID: 2},
		},
		Selection: DrainSelectionPolicy{
			MaxCPUsDrainRatio:         0.5,
			RequirePairedSwapProgress: true,
		},
		Budget: NewBudgetTracker(ConvergenceBudget{}),
	}, signature
}

func loadOverlapChurnReplayInput(t *testing.T) (PhasePlanInput, overlapChurnReplayFixture) {
	t.Helper()

	data, err := os.ReadFile(filepath.Join("testdata", "overlap_churn_deadlock_snapshot.json"))
	if err != nil {
		t.Fatalf("read overlap-churn replay fixture: %v", err)
	}
	var fixture overlapChurnReplayFixture
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatalf("decode overlap-churn replay fixture: %v", err)
	}
	if fixture.CPUCount != 96 {
		t.Fatalf("fixture cpu_count = %d, want 96", fixture.CPUCount)
	}

	input := buildScaledOverlapChurnInput(t, fixture.CPUCount, fixture.ProtectedRelCount)
	input.Budget = NewBudgetTracker(ConvergenceBudget{
		MaxDeadlockProbeOperations: fixture.DeadlockProbeBudget,
	})
	return input, fixture
}

func buildScaledOverlapChurnInput(t *testing.T, cpuCount, protectedRelCount int) PhasePlanInput {
	t.Helper()
	if cpuCount <= 0 || cpuCount%2 != 0 {
		t.Fatalf("cpuCount = %d, want positive even number", cpuCount)
	}
	primaryCurrent := replayCPUSet([2]int{0, cpuCount/2 - 1})
	reclaimCurrent := replayCPUSet([2]int{cpuCount / 2, cpuCount - 1})
	primaryDesired := reclaimCurrent
	reclaimDesired := primaryCurrent
	dag := mustPlanDAG(t, []NodeSpec{
		{Rel: "kubepods", Role: TopoNodeRolePrimary, Domain: DomainPrimary, CPUs: primaryDesired, TrustAnchor: true},
		{Rel: "custom-reclaim-root", Role: TopoNodeRoleReclaim, Domain: DomainReclaim, CPUs: reclaimDesired, TrustAnchor: true},
	})
	snapshot := planSnapshot(map[string]EntryState{
		"kubepods":            {Rel: "kubepods", Identity: CgroupIdentity{Inode: 1}, CPUs: primaryCurrent},
		"custom-reclaim-root": {Rel: "custom-reclaim-root", Identity: CgroupIdentity{Inode: 2}, CPUs: reclaimCurrent},
	}, map[DomainID]machine.CPUSet{
		DomainPrimary: primaryCurrent,
		DomainReclaim: reclaimCurrent,
	})
	snapshot.DomainByRel = map[string]DomainID{
		"kubepods": DomainPrimary, "custom-reclaim-root": DomainReclaim,
	}
	protectedByRel := make(map[string]machine.CPUSet, protectedRelCount)
	for i := 0; i < cpuCount; i++ {
		name := fmt.Sprintf("pod-pending-%02d", i)
		parent := "kubepods"
		domain := DomainPrimary
		cpu := i
		if i >= cpuCount/2 {
			parent = "custom-reclaim-root"
			domain = DomainReclaim
		}
		rel := filepath.Join(parent, name)
		cpus := machine.NewCPUSet(cpu)
		identity := CgroupIdentity{Inode: uint64(i + 3)}
		snapshot.Entries[rel] = EntryState{Rel: rel, Identity: identity, CPUs: cpus}
		snapshot.Children[parent] = append(snapshot.Children[parent], ChildRef{Name: name, Identity: identity})
		snapshot.DomainByRel[rel] = domain
		if i < protectedRelCount {
			protectedByRel[rel] = cpus
		}
	}
	snapshot.ID = fingerprintSnapshot(snapshot)

	return PhasePlanInput{
		Context: context.Background(), Kind: PhaseDrain, DAG: dag, Snapshot: snapshot,
		DesiredByRel: map[string]machine.CPUSet{
			"kubepods": primaryDesired, "custom-reclaim-root": reclaimDesired,
		},
		AllowedCPUs:    replayCPUSet([2]int{0, cpuCount - 1}),
		ProtectedByRel: protectedByRel,
		Selection: DrainSelectionPolicy{
			RequirePairedSwapProgress: true,
		},
		Budget: NewBudgetTracker(DefaultConvergenceBudget()),
	}
}

func buildE2EShapeDeadlockInput(t *testing.T) PhasePlanInput {
	t.Helper()
	input := buildScaledOverlapChurnInput(t, 96, 2)
	primaryDesired := replayCPUSet([2]int{0, 37}).Union(replayCPUSet([2]int{48, 57}))
	reclaimDesired := replayCPUSet([2]int{38, 47}).Union(replayCPUSet([2]int{58, 95}))
	input.DesiredByRel["kubepods"] = primaryDesired
	input.DesiredByRel["custom-reclaim-root"] = reclaimDesired
	input.ProtectedPending = replayCPUSet([2]int{0, 11})
	if input.Snapshot.DomainByRel == nil {
		input.Snapshot.DomainByRel = make(map[string]DomainID)
	}
	if input.DynamicByRel == nil {
		input.DynamicByRel = make(map[string]machine.CPUSet)
	}

	allCPUs := replayCPUSet([2]int{0, 95})
	childIndex := 0
	for rootIndex := 0; rootIndex < 11; rootIndex++ {
		root := fmt.Sprintf("noise-%02d", rootIndex)
		input.Snapshot.Entries[root] = EntryState{CPUs: allCPUs}
		input.Snapshot.DomainByRel[root] = DomainPrimary
		input.DynamicByRel[root] = allCPUs
		childCount := 11
		if rootIndex < 2 {
			childCount = 12
		}
		for i := 0; i < childCount; i++ {
			name := fmt.Sprintf("child-%03d", childIndex)
			rel := filepath.Join(root, name)
			input.Snapshot.Entries[rel] = EntryState{CPUs: allCPUs}
			input.Snapshot.DomainByRel[rel] = DomainPrimary
			input.Snapshot.Children[root] = append(
				input.Snapshot.Children[root], ChildRef{Name: name})
			input.DynamicByRel[rel] = allCPUs
			childIndex++
		}
	}
	if childIndex != 123 {
		t.Fatalf("noise child count = %d, want 123", childIndex)
	}
	input.Snapshot.ID = fingerprintSnapshot(input.Snapshot)
	return input
}

func buildUnprotectedABCE2EInput(t *testing.T) PhasePlanInput {
	t.Helper()
	reserve := machine.NewCPUSet(0, 24)
	reclaimB := machine.MustParse("9-13,20-21,36,39-40,42,57,59-60,68,84,87-88,90")
	reclaimC := machine.MustParse("13-18,20-21,35-40,42,62-66,68,83-88,90")
	machineCPUs := replayCPUSet([2]int{0, 95})
	primaryB := machineCPUs.Difference(reserve).Difference(reclaimB)
	primaryC := machineCPUs.Difference(reserve).Difference(reclaimC)
	transferCPUs := reclaimB.Difference(reclaimC).Union(reclaimC.Difference(reclaimB))
	if transferCPUs.Size() != 23 {
		t.Fatalf("B-C transfer CPUs = %s size=%d, want 23", transferCPUs, transferCPUs.Size())
	}

	dag := mustPlanDAG(t, []NodeSpec{
		{Rel: "kubepods", Role: TopoNodeRolePrimary, Domain: DomainPrimary, CPUs: primaryC, TrustAnchor: true},
		{Rel: "custom-reclaim-root", Role: TopoNodeRoleReclaim, Domain: DomainReclaim, CPUs: reclaimC, TrustAnchor: true},
	})
	entries := map[string]EntryState{
		"kubepods":            {Rel: "kubepods", Identity: CgroupIdentity{Inode: 1}, CPUs: primaryB},
		"custom-reclaim-root": {Rel: "custom-reclaim-root", Identity: CgroupIdentity{Inode: 2}, CPUs: reclaimB},
	}
	snapshot := planSnapshot(entries, map[DomainID]machine.CPUSet{
		DomainPrimary: primaryB,
		DomainReclaim: reclaimB,
	})
	snapshot.DomainByRel = map[string]DomainID{
		"kubepods": DomainPrimary, "custom-reclaim-root": DomainReclaim,
	}
	inode := uint64(3)
	for _, cpu := range primaryB.ToSliceInt() {
		name := fmt.Sprintf("primary-leaf-%02d", cpu)
		rel := filepath.Join("kubepods", name)
		identity := CgroupIdentity{Inode: inode}
		inode++
		snapshot.Entries[rel] = EntryState{Rel: rel, Identity: identity, CPUs: machine.NewCPUSet(cpu)}
		snapshot.Children["kubepods"] = append(snapshot.Children["kubepods"], ChildRef{Name: name, Identity: identity})
		snapshot.DomainByRel[rel] = DomainPrimary
	}
	for _, cpu := range reclaimB.ToSliceInt() {
		name := fmt.Sprintf("reclaim-leaf-%02d", cpu)
		rel := filepath.Join("custom-reclaim-root", name)
		identity := CgroupIdentity{Inode: inode}
		inode++
		snapshot.Entries[rel] = EntryState{Rel: rel, Identity: identity, CPUs: machine.NewCPUSet(cpu)}
		snapshot.Children["custom-reclaim-root"] = append(snapshot.Children["custom-reclaim-root"], ChildRef{Name: name, Identity: identity})
		snapshot.DomainByRel[rel] = DomainReclaim
	}
	childIndex := 0
	for rootIndex := 0; rootIndex < 11; rootIndex++ {
		root := fmt.Sprintf("dynamic-noise-%02d", rootIndex)
		snapshot.Entries[root] = EntryState{Rel: root, Identity: CgroupIdentity{Inode: inode}, CPUs: transferCPUs}
		inode++
		snapshot.DomainByRel[root] = DomainPrimary
		childCount := 14
		if rootIndex == 0 {
			childCount = 15
		}
		for i := 0; i < childCount; i++ {
			name := fmt.Sprintf("child-%03d", childIndex)
			rel := filepath.Join(root, name)
			identity := CgroupIdentity{Inode: inode}
			inode++
			snapshot.Entries[rel] = EntryState{Rel: rel, Identity: identity, CPUs: transferCPUs}
			snapshot.Children[root] = append(snapshot.Children[root], ChildRef{Name: name, Identity: identity})
			snapshot.DomainByRel[rel] = DomainPrimary
			childIndex++
		}
	}
	if len(snapshot.Entries) != 262 || snapshotChildEdgeCount(snapshot) != 249 || childIndex != 155 {
		t.Fatalf("fixture shape entries=%d edges=%d noise_children=%d, want 262/249/155",
			len(snapshot.Entries), snapshotChildEdgeCount(snapshot), childIndex)
	}
	snapshot.ID = fingerprintSnapshot(snapshot)
	return PhasePlanInput{
		Context: context.Background(), Kind: PhaseDrain, DAG: dag, Snapshot: snapshot,
		DesiredByRel: map[string]machine.CPUSet{
			"kubepods": primaryC, "custom-reclaim-root": reclaimC,
		},
		AllowedCPUs: machineCPUs,
		Selection: DrainSelectionPolicy{
			RequirePairedSwapProgress: true,
		},
		Budget: NewBudgetTracker(DefaultConvergenceBudget()),
	}
}

func contextPhaseOperationTotal(stats deadlock.DeadlockProbeStats) int {
	return stats.BaseOperations +
		stats.ProtectedOperations +
		stats.RelIndexOperations +
		stats.ChildIndexOps +
		stats.FrontierIndexOps +
		stats.AncestorClosureOps
}

func measureLegacyDeadlockProbeOperations(t *testing.T, in PhasePlanInput) int {
	t.Helper()

	desiredByDomain := desiredDomainUnions(in.DAG, in.DesiredByRel)
	domains := sortedDomains(in.Snapshot.DomainUnion, desiredByDomain)
	graph := buildTransferGraph(domains, in.Snapshot.DomainUnion, desiredByDomain, nil)
	protectedByDomain := protectedCPUSetByDomain(in.ProtectedByRel, in.ProtectedPending, in.DAG)
	depthByRel, relOrder := buildSnapshotDepthByRel(in.Snapshot, nil)
	domainByRel, parentByRel := buildPlannerRelations(in.Snapshot, in.DAG, depthByRel, relOrder.relsAsc, relOrder.childRelsByRel, nil)
	leavingByDomain := make(map[DomainID]machine.CPUSet, len(domains))
	for _, domain := range domains {
		leavingByDomain[domain] = in.Snapshot.DomainUnion[domain].Difference(desiredByDomain[domain])
	}
	budget := NewBudgetTracker(ConvergenceBudget{MaxDeadlockProbeOperations: 1_000_000})
	for _, source := range []DomainID{DomainPrimary, DomainReclaim} {
		destinations := make([]DomainID, 0, len(graph[source]))
		for destination := range graph[source] {
			destinations = append(destinations, destination)
		}
		sort.Slice(destinations, func(i, j int) bool { return destinations[i] < destinations[j] })
		for _, destination := range destinations {
			for _, cpu := range graph[source][destination].ToSliceInt() {
				atomCPUs := machine.NewCPUSet(cpu)
				if !atomCPUs.Intersection(protectedByDomain[source]).IsEmpty() {
					continue
				}
				_, err := projectDrainTargets(DrainProjectionInput{
					PlanInput: in,
					DrainBatch: map[DomainID]machine.CPUSet{
						DomainPrimary: machine.NewCPUSet(),
						DomainReclaim: machine.NewCPUSet(),
						source:        atomCPUs,
					},
					LeavingByDomain: leavingByDomain,
					DomainByRel:     domainByRel,
					ParentByRel:     parentByRel,
					DepthByRel:      depthByRel,
					ProbeContext:    context.Background(),
					ProbeBudget:     budget,
				})
				if err != nil {
					t.Fatalf("legacy projection source=%s destination=%s cpu=%d: %v",
						source, destination, cpu, err)
				}
			}
		}
	}
	return budget.Usage().DeadlockProbeOperations
}

func replayCPUSet(bounds [2]int) machine.CPUSet {
	cpus := make([]int, 0, bounds[1]-bounds[0]+1)
	for cpu := bounds[0]; cpu <= bounds[1]; cpu++ {
		cpus = append(cpus, cpu)
	}
	return machine.NewCPUSet(cpus...)
}

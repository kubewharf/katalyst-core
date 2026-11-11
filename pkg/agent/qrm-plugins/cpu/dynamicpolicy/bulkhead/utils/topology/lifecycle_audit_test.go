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
	"context"
	"fmt"
	"syscall"
	"testing"

	"github.com/stretchr/testify/require"

	cgroupclient "github.com/kubewharf/katalyst-core/pkg/util/cgroup/client"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

func TestPreWriteChildAbsenceAuthorizesZeroWriteReplan(t *testing.T) {
	t.Parallel()
	for _, throughCoordinator := range []bool{false, true} {
		throughCoordinator := throughCoordinator
		t.Run(map[bool]string{false: "transaction", true: "coordinator"}[throughCoordinator], func(t *testing.T) {
			t.Parallel()
			f := newSubtreeRetirementFixture(t)
			base := f.snapshot()
			budget := NewAdjustmentBudget(context.Background(), ConvergenceBudget{MaxPlanOperations: 1000})
			f.round.adjustmentBudget = budget
			lists, trigger := 0, 2
			if throughCoordinator {
				trigger = 3
			}
			retired := false
			f.driver.beforeCall = func(op HierarchyOperation, rel string) error {
				if op == HierarchyOperationList && rel == subtreeRetirementPodRel {
					lists++
				}
				if lists >= trigger && !retired && op == HierarchyOperationRead && rel == subtreeRetirementPodRel+"/ctr-c" {
					require.Positive(t, reservedWritesCount(budget))
					retireSubtreeNode(f, "ctr-c")
					retired = true
				}
				return nil
			}
			res := &ConvergenceResult{}
			var err error
			if throughCoordinator {
				cg := newTopologyFakeCgroup()
				cg.version = cgroupclient.CgroupVersionV2
				provider := &coordinatorSnapshotTestCgroup{topologyFakeCgroup: cg, driver: f.driver}
				*res, err = (TopologyCoordinator{}).Converge(context.Background(), CoordinatorInput{
					DAG: f.round.dag, Cgroup: provider, Objective: ConvergenceObjectiveParentSafe,
					CPUDetails: f.cpuDetails, AdjustmentBudget: budget, RequiredCPUSetByRel: f.requiredByRel,
				})
			} else {
				_, err = f.round.executeParentSafeAdmission(context.Background(), base, res, nil, nil)
			}
			require.True(t, retired)
			require.ErrorIs(t, err, syscall.ENOENT)
			require.True(t, phaseTraceStale(err))
			require.Equal(t, ReplanSafeNoPhysicalWrites, res.ReplanDisposition)
			require.Zero(t, res.forwardWriteAttempts)
			require.Zero(t, f.driver.PhysicalWriteCount())
			require.Zero(t, reservedWritesCount(budget))
		})
	}
}

func TestSnapshotBudgetFailureDoesNotAuthorizeReplanThroughIdentityCause(t *testing.T) {
	t.Parallel()
	err := &SnapshotError{Class: HierarchyErrorBudget, Err: fmt.Errorf("%w: %v", ErrCgroupIdentityChanged, ErrHierarchyIOOperationBudgetExceeded)}
	res := &ConvergenceResult{}
	authorizePreWriteStaleReplan(res, err)
	require.False(t, phaseTraceStale(err))
	require.False(t, replanRequired(err))
	require.Equal(t, ReplanNotAllowed, res.ReplanDisposition)
}

func TestInvalidHolderMemsRemainsHardPreflightError(t *testing.T) {
	t.Parallel()
	f, _ := newTask9FinalizeFixture(t, false)
	addChurnHolder(t, f, "kubepods/holder", "1")
	base := f.snapshot()
	f.round.adjustmentBudget = NewAdjustmentBudget(context.Background(), ConvergenceBudget{MaxPlanOperations: 1000})
	f.driver.nodes["kubepods/holder"].mems = "invalid"
	f.driver.nodes["kubepods/holder"].configuredMems = "invalid"
	res := &ConvergenceResult{}
	_, err := f.round.executeParentSafeAdmission(context.Background(), base, res, nil, nil)
	require.ErrorContains(t, err, "invalid mems state")
	require.False(t, phaseTraceStale(err))
	require.Equal(t, ReplanNotAllowed, res.ReplanDisposition)
	require.Nil(t, res.FinalSnapshot)
	require.False(t, res.FinalSnapshotCurrent)
	require.Zero(t, res.forwardWriteAttempts)
}

func TestInvalidHolderMemsRemainsHardFinalizationError(t *testing.T) {
	t.Parallel()
	f, _ := newTask9FinalizeFixture(t, false)
	addChurnHolder(t, f, "kubepods/holder", "1")
	base := f.snapshot()
	budget := NewAdjustmentBudget(context.Background(), ConvergenceBudget{MaxPlanOperations: 1000})
	f.round.adjustmentBudget = budget
	injected := false
	f.driver.beforeCall = func(op HierarchyOperation, rel string) error {
		if !injected && f.driver.PhysicalWriteCount() > 0 && op == HierarchyOperationRead && rel == "kubepods/holder" {
			f.driver.nodes[rel].mems = "invalid"
			f.driver.nodes[rel].configuredMems = "invalid"
			injected = true
		}
		return nil
	}
	res := &ConvergenceResult{}
	_, err := f.round.executeParentSafeAdmission(context.Background(), base, res, nil, nil)
	require.True(t, injected)
	require.ErrorContains(t, err, "invalid mems state")
	require.False(t, phaseTraceStale(err))
	require.Equal(t, ReplanNotAllowed, res.ReplanDisposition)
	require.Positive(t, res.forwardWriteAttempts)
	require.Positive(t, res.rollbackWriteAttempts)
	require.Zero(t, res.Applied)
	require.Empty(t, res.Journal)
	require.Zero(t, reservedWritesCount(budget))
}

func TestAbsorbedSnapshotIsClearedOnFailedPreflight(t *testing.T) {
	t.Parallel()
	f, base := newTask9FinalizeFixture(t, false)
	budget := NewAdjustmentBudget(context.Background(), ConvergenceBudget{MaxPlanOperations: 1000})
	f.round.adjustmentBudget = budget
	addChurnHolder(t, f, "kubepods/churn-holder", "0")
	reads := 0
	f.driver.beforeCall = func(op HierarchyOperation, rel string) error {
		if op == HierarchyOperationRead && rel == "reclaimed" {
			reads++
			if reads == 3 {
				return syscall.ENOENT
			}
		}
		return nil
	}
	res := &ConvergenceResult{}
	_, err := f.round.executeParentSafeAdmission(context.Background(), base, res, nil, nil)
	require.Error(t, err)
	require.Len(t, res.Rounds, 2)
	var drift *frozenInitialSnapshotDriftError
	require.ErrorAs(t, err, &drift)
	require.Nil(t, drift.current)
	require.Equal(t, ReplanSafeNoPhysicalWrites, res.ReplanDisposition)
	require.Nil(t, res.FinalSnapshot)
	require.False(t, res.FinalSnapshotCurrent)
	require.Zero(t, res.forwardWriteAttempts)
	require.Zero(t, reservedWritesCount(budget))
}

func TestChurnRefreshPreservesHardFailure(t *testing.T) {
	t.Parallel()
	f, base := newTask9FinalizeFixture(t, false)
	budget := NewAdjustmentBudget(context.Background(), ConvergenceBudget{MaxPlanOperations: 1000})
	f.round.adjustmentBudget = budget
	addChurnHolder(t, f, "kubepods/churn-holder", "0")
	reads := 0
	f.driver.beforeCall = func(op HierarchyOperation, rel string) error {
		if op == HierarchyOperationRead && rel == "reclaimed" {
			reads++
			if reads == 2 {
				return syscall.EACCES
			}
		}
		return nil
	}
	res := &ConvergenceResult{}
	_, err := f.round.executeParentSafeAdmission(context.Background(), base, res, nil, nil)
	require.Equal(t, 2, reads)
	require.ErrorIs(t, err, syscall.EACCES)
	require.ErrorIs(t, err, ErrCoordinatorPlanStale)
	require.False(t, replanRequired(err))
	require.False(t, phaseTraceStale(err))
	require.Equal(t, ReplanNotAllowed, res.ReplanDisposition)
	require.Nil(t, res.FinalSnapshot)
	require.False(t, res.FinalSnapshotCurrent)
	require.Zero(t, res.forwardWriteAttempts)
	require.Zero(t, reservedWritesCount(budget))
}

func TestFinalSnapshotAbsenceAuthorizesVerifiedRollbackReplan(t *testing.T) {
	t.Parallel()
	f := newSubtreeRetirementFixture(t)
	base := f.snapshot()
	budget := NewAdjustmentBudget(context.Background(), ConvergenceBudget{MaxPlanOperations: 1000})
	f.round.adjustmentBudget = budget
	retired := false
	f.driver.beforeCall = func(op HierarchyOperation, rel string) error {
		if !retired && f.driver.PhysicalWriteCount() > 0 && op == HierarchyOperationRead && rel == subtreeRetirementPodRel+"/ctr-c" {
			retireSubtreeNode(f, "ctr-c")
			retired = true
		}
		return nil
	}
	res := &ConvergenceResult{}
	_, err := f.round.executeParentSafeAdmission(context.Background(), base, res, nil, nil)
	require.True(t, retired)
	var snapshotErr *SnapshotError
	require.ErrorAs(t, err, &snapshotErr)
	require.Equal(t, HierarchyErrorStale, snapshotErr.Class)
	require.Positive(t, res.forwardWriteAttempts)
	require.Positive(t, res.rollbackWriteAttempts)
	require.Zero(t, res.Applied)
	require.Empty(t, res.Journal)
	require.Equal(t, "0-7", f.driver.nodes["kubepods"].cpus.String())
	require.Equal(t, "0-7", f.driver.nodes[subtreeRetirementPodRel].cpus.String())
	require.Equal(t, "8", f.driver.nodes["reclaimed"].cpus.String())
	require.True(t, phaseTraceStale(err))
	require.Equal(t, ReplanSafeAfterVerifiedRollback, res.ReplanDisposition)
	require.True(t, budget.ReplanSafe(res.ReplanDisposition))
	require.Zero(t, reservedWritesCount(budget))
}

func TestRetirementDuringSnapshotRequiresNewCompleteEvidence(t *testing.T) {
	t.Parallel()
	f := newSubtreeRetirementFixture(t)
	base := f.snapshot()
	f.round.adjustmentBudget = NewAdjustmentBudget(context.Background(), ConvergenceBudget{MaxPlanOperations: 1000})
	retired := false
	f.driver.beforeCall = func(op HierarchyOperation, rel string) error {
		if !retired && op == HierarchyOperationRead && rel == subtreeRetirementPodRel+"/ctr-c" {
			retireSubtreeNode(f, "ctr-c")
			retired = true
		}
		return nil
	}
	res := &ConvergenceResult{}
	_, err := f.round.executeParentSafeAdmission(context.Background(), base, res, nil, nil)
	require.True(t, retired)
	require.ErrorIs(t, err, syscall.ENOENT)
	require.Equal(t, ReplanSafeNoPhysicalWrites, res.ReplanDisposition)
	require.Nil(t, res.FinalSnapshot)
	require.False(t, res.FinalSnapshotCurrent)
	require.Zero(t, res.forwardWriteAttempts)
}

func TestRetirementRebuildRejectsConcurrentStateChanges(t *testing.T) {
	t.Parallel()
	mutations := map[string]func(*admissionTraceFixture){
		"replacement": func(f *admissionTraceFixture) { f.driver.bumpIdentity(subtreeRetirementPodRel + "/ctr-b") },
		"appearance":  func(f *admissionTraceFixture) { addChurnHolder(t, f, subtreeRetirementPodRel+"/new-holder", "1") },
		"holder_cpu": func(f *admissionTraceFixture) {
			f.driver.nodes[subtreeRetirementPodRel+"/ctr-b"].cpus = machine.NewCPUSet(0)
		},
		"holder_mems":        func(f *admissionTraceFixture) { f.driver.nodes[subtreeRetirementPodRel+"/ctr-b"].mems = "1" },
		"controlled_state":   func(f *admissionTraceFixture) { f.driver.nodes["kubepods"].cpus = machine.MustParse("0-6") },
		"controlled_absence": func(f *admissionTraceFixture) { delete(f.driver.nodes, "reclaimed") },
	}
	for name, mutate := range mutations {
		name, mutate := name, mutate
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newSubtreeRetirementFixture(t)
			base := f.snapshot()
			f.round.adjustmentBudget = NewAdjustmentBudget(context.Background(), ConvergenceBudget{MaxPlanOperations: 1000})
			retireSubtreeNode(f, "ctr-c")
			mutate(f)
			res := &ConvergenceResult{}
			_, err := f.round.executeParentSafeAdmission(context.Background(), base, res, nil, nil)
			require.Error(t, err)
			require.Len(t, res.Rounds, 1)
			require.Zero(t, res.forwardWriteAttempts)
			require.Zero(t, reservedWritesCount(f.round.adjustmentBudget))
		})
	}
}

func TestRetirementRebuildPreservesCallerRequiredReferences(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"required", "expected", "pending_scope", "identity", "protected"} {
		name := name
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newSubtreeRetirementFixture(t)
			base := f.snapshot()
			rel := subtreeRetirementPodRel + "/ctr-c"
			switch name {
			case "required":
				f.round.requiredByRel[rel] = machine.NewCPUSet(2)
			case "expected":
				f.round.dynamicByRel[rel] = machine.NewCPUSet(2)
			case "pending_scope":
				f.round.pendingRequiredByRel = map[string]machine.CPUSet{rel: machine.NewCPUSet(2)}
			case "protected":
				f.round.protectedByRel = map[string]machine.CPUSet{rel: machine.NewCPUSet(2)}
			case "identity":
				f.round.requiredIdentityByRel = map[string]CgroupIdentity{rel: base.Entries[rel].Identity}
			}
			f.round.adjustmentBudget = NewAdjustmentBudget(context.Background(), ConvergenceBudget{MaxPlanOperations: 1000})
			removed := false
			f.driver.beforeCall = func(op HierarchyOperation, candidate string) error {
				if !removed && op == HierarchyOperationRead && candidate == "kubepods" {
					retireSubtreeNode(f, "ctr-c")
					removed = true
				}
				return nil
			}
			res := &ConvergenceResult{}
			_, err := f.round.executeParentSafeAdmission(context.Background(), base, res, nil, nil)
			require.True(t, removed)
			require.Error(t, err)
			require.Len(t, res.Rounds, 1)
			require.Zero(t, res.forwardWriteAttempts)
			require.Zero(t, reservedWritesCount(f.round.adjustmentBudget))
		})
	}
}

func TestRetirementRebuildRejectsReplacementAndNewChild(t *testing.T) {
	t.Parallel()
	for _, replacement := range []bool{false, true} {
		replacement := replacement
		t.Run(map[bool]string{false: "appearance", true: "replacement"}[replacement], func(t *testing.T) {
			t.Parallel()
			f := newSubtreeRetirementFixture(t)
			base := f.snapshot()
			if replacement {
				f.driver.bumpIdentity(subtreeRetirementPodRel + "/ctr-c")
			} else {
				addChurnHolder(t, f, subtreeRetirementPodRel+"/new-holder", "1")
			}
			f.round.adjustmentBudget = NewAdjustmentBudget(context.Background(), ConvergenceBudget{MaxPlanOperations: 1000})
			res := &ConvergenceResult{}
			_, err := f.round.executeParentSafeAdmission(context.Background(), base, res, nil, nil)
			require.Error(t, err)
			require.False(t, isFrozenBoundaryDynamicDrift(err))
			require.Equal(t, ReplanSafeFromVerifiedFinalState, res.ReplanDisposition)
			require.Len(t, res.Rounds, 1)
			require.Zero(t, res.forwardWriteAttempts)
		})
	}
}

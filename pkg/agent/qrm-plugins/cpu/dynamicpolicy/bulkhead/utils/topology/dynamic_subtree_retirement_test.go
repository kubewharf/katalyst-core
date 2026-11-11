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
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/bulkhead/model"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

const subtreeRetirementPodRel = "kubepods/burstable/pod-retiring"

func newSubtreeRetirementFixture(t *testing.T) *admissionTraceFixture {
	t.Helper()
	f := newAdmissionTraceFixture(t)
	for cpu := 0; cpu <= 8; cpu++ {
		f.cpuDetails[cpu] = machine.CPUTopoInfo{NUMANodeID: 0, SocketID: 0, CoreID: cpu}
	}
	f.driver.capabilities = cgroupV2Policy.capabilities(true)
	f.round.allowEmptyTarget = true
	f.addPrimary("kubepods", "0-7", "0")
	// QoS, pod and container scopes are observed descendants, not controlled DAG nodes.
	for _, rel := range []string{"kubepods/burstable", subtreeRetirementPodRel} {
		f.driver.add(rel, CgroupIdentity{Device: 1, Inode: f.allocInode()}, "0-7", "0")
	}
	for cpu, name := range []string{"ctr-a", "ctr-b", "ctr-c", "ctr-d"} {
		f.driver.add(subtreeRetirementPodRel+"/"+name,
			CgroupIdentity{Device: 1, Inode: f.allocInode()}, machine.NewCPUSet(cpu).String(), "0")
	}
	f.addReclaim("reclaimed", "8", "0")
	f.targetByRel["kubepods"] = machine.MustParse("0-3")
	f.requireCPUSet("reclaimed", "4-8")
	for index := range f.specs {
		f.specs[index].CPUs = f.targetByRel[f.specs[index].Rel].Clone()
	}
	return f
}

func retireSubtreeNode(f *admissionTraceFixture, name string) {
	delete(f.driver.nodes, subtreeRetirementPodRel+"/"+name)
}

func TestReferencedDynamicSubtreeRetirementRejectsOldTrace(t *testing.T) {
	t.Parallel()
	f := newSubtreeRetirementFixture(t)
	base := f.snapshot()
	validated, err := f.round.compileValidatedFixedPointTrace(context.Background(), base)
	require.NoError(t, err)
	trace, err := validated.trace()
	require.NoError(t, err)
	require.NotContains(t, trace.FrozenBoundary.ControlledRels, subtreeRetirementPodRel)
	require.Len(t, trace.FrozenBoundary.ShrinkChildrenByRel[subtreeRetirementPodRel], 4)
	require.NotContains(t, trace.FrozenBoundary.RetirableCPUHolders, subtreeRetirementPodRel+"/ctr-c")
	retireSubtreeNode(f, "ctr-c")
	evaluation, err := EvaluateFrozenBoundary(context.Background(), f.driver, f.round.dag, f.budget, trace.FrozenBoundary, base)
	require.ErrorContains(t, err, `required child "ctr-c" disappeared`)
	require.NotNil(t, evaluation.Snapshot)
	require.Zero(t, f.driver.PhysicalWriteCount())
}

func TestReferencedSubtreeRetirementRebuildsRemainingTraceInTransaction(t *testing.T) {
	t.Parallel()
	f := newSubtreeRetirementFixture(t)
	base := f.snapshot()
	budget := NewAdjustmentBudget(context.Background(), ConvergenceBudget{MaxPlanOperations: 1000})
	f.round.adjustmentBudget = budget
	validated, err := f.round.compileValidatedFixedPointTrace(context.Background(), base)
	require.NoError(t, err)
	original, err := validated.trace()
	require.NoError(t, err)
	require.NotContains(t, original.FrozenBoundary.ControlledRels, subtreeRetirementPodRel)
	require.Len(t, original.FrozenBoundary.ShrinkChildrenByRel[subtreeRetirementPodRel], 4)
	targets := model.CloneCPUSetMap(f.round.targetByRel)
	required := model.CloneCPUSetMap(f.round.requiredByRel)
	removed := 0
	f.driver.beforeCall = func(op HierarchyOperation, rel string) error {
		if removed < 4 && op == HierarchyOperationRead && rel == "kubepods" {
			require.Positive(t, reservedWritesCount(budget), "each retirement must follow a real reservation")
			require.Zero(t, f.driver.PhysicalWriteCount(), "rejected traces must not write")
			retireSubtreeNode(f, []string{"ctr-c", "ctr-b", "ctr-a", "ctr-d"}[removed])
			removed++
		}
		return nil
	}
	published := 0
	result := &ConvergenceResult{}
	_, err = f.round.executeParentSafeAdmission(context.Background(), base, result,
		func(snapshot *CompleteSnapshot) error {
			published++
			require.Empty(t, snapshot.Children[subtreeRetirementPodRel])
			return nil
		}, nil)
	require.NoError(t, err)
	require.Equal(t, 4, removed)
	require.Len(t, result.Rounds, 5)
	require.True(t, result.Converged)
	require.False(t, result.ParentSafe)
	require.True(t, result.FinalSnapshotCurrent)
	require.True(t, result.Published)
	require.Equal(t, 1, published)
	require.Zero(t, budget.replans)
	require.Zero(t, reservedWritesCount(budget))
	require.Positive(t, result.forwardWriteAttempts)
	require.Zero(t, result.rollbackWriteAttempts)
	require.Equal(t, targets, f.round.targetByRel)
	require.Equal(t, required, f.round.requiredByRel)
	require.Len(t, original.FrozenBoundary.ShrinkChildrenByRel[subtreeRetirementPodRel], 4, "old trace remains immutable")
	require.Equal(t, "0-3", f.driver.nodes["kubepods"].cpus.String())
	require.Equal(t, "0-3", f.driver.nodes[subtreeRetirementPodRel].cpus.String())
	require.Equal(t, "4-8", f.driver.nodes["reclaimed"].cpus.String())
	require.Empty(t, result.FinalSnapshot.Children[subtreeRetirementPodRel])
}

func TestReferencedOperationScopeRetirementRecompilesWithoutScopeWrites(t *testing.T) {
	t.Parallel()
	f := newSubtreeRetirementFixture(t)
	base := f.snapshot()
	budget := NewAdjustmentBudget(context.Background(), ConvergenceBudget{MaxPlanOperations: 1000})
	f.round.adjustmentBudget = budget
	retired := false
	f.driver.beforeCall = func(op HierarchyOperation, rel string) error {
		if !retired && op == HierarchyOperationRead && rel == "kubepods" {
			require.Positive(t, reservedWritesCount(budget))
			for _, name := range []string{"ctr-a", "ctr-b", "ctr-c", "ctr-d"} {
				name := name
				retireSubtreeNode(f, name)
			}
			delete(f.driver.nodes, subtreeRetirementPodRel)
			retired = true
		}
		if op == HierarchyOperationWriteCPUs {
			require.NotEqual(t, subtreeRetirementPodRel, rel)
		}
		return nil
	}
	res := &ConvergenceResult{}
	_, err := f.round.executeParentSafeAdmission(context.Background(), base, res, nil, nil)
	require.NoError(t, err)
	require.True(t, retired)
	require.True(t, res.Converged)
	require.Len(t, res.Rounds, 2)
	require.NotContains(t, res.FinalSnapshot.Entries, subtreeRetirementPodRel)
	require.Equal(t, "4-8", f.driver.nodes["reclaimed"].cpus.String())
	require.Zero(t, budget.replans)
	require.Zero(t, reservedWritesCount(budget))
}

func TestRetirementRebuildStopsAtExistingExecutionBudgets(t *testing.T) {
	t.Parallel()
	for _, cancelExecution := range []bool{false, true} {
		cancelExecution := cancelExecution
		t.Run(map[bool]string{false: "hierarchy_io", true: "context"}[cancelExecution], func(t *testing.T) {
			t.Parallel()
			f := newSubtreeRetirementFixture(t)
			base := f.snapshot()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			budget := NewAdjustmentBudget(ctx, ConvergenceBudget{MaxPlanOperations: 1000})
			f.round.adjustmentBudget = budget
			reads := 0
			f.driver.beforeCall = func(op HierarchyOperation, rel string) error {
				if op == HierarchyOperationRead && rel == "kubepods" {
					reads++
					if reads == 1 {
						retireSubtreeNode(f, "ctr-c")
					}
					if reads == 2 {
						if cancelExecution {
							cancel()
						} else {
							f.budget.limit.MaxHierarchyIOOperations = f.budget.Usage().HierarchyIOOperations
						}
					}
				}
				return nil
			}
			res := &ConvergenceResult{}
			_, err := f.round.executeParentSafeAdmission(ctx, base, res, nil, nil)
			if cancelExecution {
				require.ErrorIs(t, err, context.Canceled)
			} else {
				require.ErrorIs(t, err, ErrHierarchyIOOperationBudgetExceeded)
			}
			require.Equal(t, 2, reads)
			require.Len(t, res.Rounds, 2)
			require.Equal(t, ReplanNotAllowed, res.ReplanDisposition)
			require.Nil(t, res.FinalSnapshot)
			require.False(t, res.FinalSnapshotCurrent)
			require.Zero(t, res.forwardWriteAttempts)
			require.Zero(t, budget.replans)
			require.Zero(t, reservedWritesCount(budget))
		})
	}
}

func TestRetiredSubtreeBeforeCompileConverges(t *testing.T) {
	t.Parallel()
	f := newSubtreeRetirementFixture(t)
	for _, name := range []string{"ctr-a", "ctr-b", "ctr-c", "ctr-d"} {
		name := name
		retireSubtreeNode(f, name)
	}
	base := f.snapshot()
	f.round.adjustmentBudget = NewAdjustmentBudget(context.Background(), ConvergenceBudget{MaxPlanOperations: 1000})
	result := &ConvergenceResult{}
	_, err := f.round.executeParentSafeAdmission(context.Background(), base, result, nil, nil)
	require.NoError(t, err)
	require.True(t, result.Converged)
	require.Len(t, result.Rounds, 1)
	require.Positive(t, result.forwardWriteAttempts)
}

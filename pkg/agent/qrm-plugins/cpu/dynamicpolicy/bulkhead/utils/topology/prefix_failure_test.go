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
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

type phaseFailureDriver struct {
	*fakeHierarchyDriver
	mode            string
	failure         error
	rollbackFailure error
	evidenceFailure error
	cancel          context.CancelFunc
	calls           []string
}

func (d *phaseFailureDriver) ReadEntry(ctx context.Context, rel string) (EntryState, error) {
	if d.mode == "unreadable_unknown" && rel == "other" && !isAdjustmentRecoveryContext(ctx) && len(d.calls) > 1 {
		entry, _ := d.fakeHierarchyDriver.ReadEntry(ctx, rel)
		return entry, d.evidenceFailure // partial result must never be treated as trustworthy
	}
	return d.fakeHierarchyDriver.ReadEntry(ctx, rel)
}

func (d *phaseFailureDriver) WriteCPUs(ctx context.Context, rel string, id CgroupIdentity, cpus machine.CPUSet) error {
	d.calls = append(d.calls, rel+"="+cpus.String())
	if err := ctx.Err(); err != nil {
		return err
	}
	if rel == "other" && !isAdjustmentRecoveryContext(ctx) {
		switch d.mode {
		case "unknown", "unreadable_unknown":
			if err := d.fakeHierarchyDriver.WriteCPUs(ctx, rel, id, cpus); err != nil {
				return err
			}
		case "replacement":
			d.nodes["primary/pod"].identity.Inode++
		case "third":
			d.nodes["primary/pod"].cpus = machine.NewCPUSet(3, 51)
			d.nodes["primary/pod"].configuredCPUs = machine.NewCPUSet(3, 51)
		}
		return d.failure
	}
	if d.mode == "rollback_failure" && len(d.fakeHierarchyDriver.writes) > 0 {
		return d.rollbackFailure
	}
	err := d.fakeHierarchyDriver.WriteCPUs(ctx, rel, id, cpus)
	if err == nil && (d.mode == "cancel" || d.mode == "budget_expire") && !isAdjustmentRecoveryContext(ctx) {
		d.cancel()
	}
	return err
}

func TestPhaseWriterPrefixFailureRecovery(t *testing.T) {
	t.Parallel()

	for _, mode := range []string{"unknown", "unreadable_unknown", "replacement", "third", "rollback_failure", "cancel", "budget_expire", "reserve_reject", "prior_prefix"} {
		mode := mode
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			f, _ := dormantTransferFixture(t)
			f.driver.add("other", CgroupIdentity{Device: 1, Inode: f.allocInode()}, "0-3", "0")
			initial := f.driver.snapshot()
			pod, parent, other := f.driver.nodes["primary/pod"], f.driver.nodes["primary"], f.driver.nodes["other"]
			plan := PhasePlan{ConvergenceID: "prefix-failure", Kind: PhaseDrain, Capabilities: cgroupV1Policy.capabilities(true), Operations: []PlanOperation{
				{
					Rel: "primary/pod", ExpectedIdentity: pod.identity, ParentRel: "primary", ExpectedParentIdentity: parent.identity,
					ExpectedCurrent: CPUSetTarget{CPUs: pod.cpus.Clone(), Mems: pod.mems}, Target: CPUSetTarget{CPUs: pod.cpus.Difference(machine.NewCPUSet(1, 49)), Mems: pod.mems}, Direction: WriteShrink,
				},
				{
					Rel: "other", ExpectedIdentity: other.identity, ExpectedCurrent: CPUSetTarget{CPUs: other.cpus.Clone(), Mems: other.mems},
					Target: CPUSetTarget{CPUs: machine.MustParse("0-2"), Mems: other.mems}, Direction: WriteShrink,
				},
			}}
			plan.PlanID = canonicalExecutionPlanID(plan)
			for i := range plan.Operations {
				plan.Operations[i].PlanID = plan.PlanID
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			d := &phaseFailureDriver{fakeHierarchyDriver: f.driver, mode: mode, failure: errors.New("second write failed"), rollbackFailure: errors.New("inverse failed"), evidenceFailure: errors.New("unknown readback failed"), cancel: cancel}
			res := &ConvergenceResult{}
			prior := AppliedPlanOperation{PlanID: "prior-phase", Rel: "unrelated", Observed: CPUSetTarget{CPUs: machine.NewCPUSet(9), Mems: "0"}}
			if mode == "prior_prefix" {
				res.Applied = 1
				res.Journal = []AppliedPlanOperation{prior}
			}
			writer := newSafeCPUSetWriter(d, NewBudgetTracker(ConvergenceBudget{}), res)
			writer.recoveryContextFactory = deterministicRecoveryContext
			adjustmentCtx, expireAdjustment := context.WithCancel(context.Background())
			defer expireAdjustment()
			adjustment := NewAdjustmentBudget(adjustmentCtx, ConvergenceBudget{DeadlineDuration: -1})
			adjustment.maxWrites = 4 // exact two forward plus two inverse writes
			writer = writer.withAdjustmentBudget(adjustment)
			if mode == "budget_expire" {
				d.cancel = expireAdjustment
			}
			if mode == "reserve_reject" {
				adjustment.maxWrites = 3
			}
			err := writer.execute(ctx, plan)
			require.Error(t, err)
			if mode == "reserve_reject" {
				require.ErrorIs(t, err, ErrAdjustmentWriteBudgetExceeded)
				require.Empty(t, d.calls)
				require.Zero(t, adjustment.reservedWrites)
				return
			}
			if mode == "cancel" || mode == "budget_expire" {
				require.ErrorIs(t, err, context.Canceled)
			} else {
				require.ErrorIs(t, err, d.failure)
			}
			require.False(t, res.Converged)
			switch mode {
			case "replacement":
				require.ErrorIs(t, err, ErrCgroupIdentityChanged)
				require.NotEqual(t, initial["primary/pod"].identity, d.nodes["primary/pod"].identity)
				require.Len(t, d.calls, 2, "replacement must never receive an inverse")
			case "third":
				require.Contains(t, err.Error(), "neither rollback after nor before")
				require.True(t, d.nodes["primary/pod"].cpus.Equals(machine.NewCPUSet(3, 51)))
				require.Len(t, d.calls, 2, "third state must never receive an inverse")
			case "rollback_failure":
				require.ErrorIs(t, err, d.rollbackFailure)
				require.NotEmpty(t, res.Journal, "uncorrected physical impact must remain visible")
			default:
				require.NotContains(t, err.Error(), "rollback failed")
				require.True(t, initial["primary/pod"].cpus.Equals(d.nodes["primary/pod"].cpus))
				require.True(t, initial["other"].cpus.Equals(d.nodes["other"].cpus))
				if mode == "prior_prefix" {
					require.Equal(t, 1, res.Applied)
					require.Equal(t, []AppliedPlanOperation{prior}, res.Journal)
				} else {
					require.Empty(t, res.Journal)
					require.Zero(t, res.Applied)
				}
				if mode == "unreadable_unknown" {
					require.ErrorIs(t, err, d.evidenceFailure)
				}
				if mode == "unknown" || mode == "unreadable_unknown" {
					require.Len(t, d.calls, 4)
					require.Contains(t, d.calls[2], "other=")
					require.Contains(t, d.calls[3], "primary/pod=")
				}
			}
			require.Zero(t, adjustment.reservedWrites, "settlement must follow recovery")
			require.Equal(t, res.forwardWriteAttempts, adjustment.physicalWrites)
			require.Equal(t, res.rollbackWriteAttempts, adjustment.rollbackWrites)
			require.False(t, res.ReplanDisposition.AllowsReplan(), "one plan rollback does not authorize a whole-attempt replan")
			t.Logf("mode=%s forward=%d rollback=%d applied=%d journal=%d calls=%v error=%v", mode, res.forwardWriteAttempts, res.rollbackWriteAttempts, res.Applied, len(res.Journal), d.calls, err)
		})
	}
}

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

func TestPhaseWriterPrefixRestoresConfiguredAndEffectiveState(t *testing.T) {
	t.Parallel()

	for _, v2 := range []bool{false, true} {
		for _, omitResult := range []bool{false, true} {
			name := "v1"
			if v2 {
				name = "v2-inherited"
			}
			if omitResult {
				name += "-no-result"
			}
			v2, omitResult := v2, omitResult
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				d := newFakeHierarchyDriver()
				d.allowUnwitnessedExpansion = true
				caps := cgroupV1Policy.capabilities(true)
				if v2 {
					caps = cgroupV2Policy.capabilities(true)
				}
				d.capabilities = caps
				d.add("root", CgroupIdentity{Device: 1, Inode: 1}, "0-3", "0-1")
				d.add("root/leaf", CgroupIdentity{Device: 1, Inode: 2}, "0-3", "0-1")
				d.add("other", CgroupIdentity{Device: 1, Inode: 3}, "0-3", "0")
				before := d.snapshot()
				cpus, mems := machine.NewCPUSet(0, 1), "0"
				if v2 {
					cpus = machine.NewCPUSet()
					mems = ""
				}
				plan := PhasePlan{ConvergenceID: name, Kind: PhaseDrain, Capabilities: caps, Operations: []PlanOperation{
					{
						Rel: "root/leaf", ExpectedIdentity: d.nodes["root/leaf"].identity, ParentRel: "root", ExpectedParentIdentity: d.nodes["root"].identity,
						ExpectedCurrent: CPUSetTarget{CPUs: machine.MustParse("0-3"), Mems: "0-1"}, Target: CPUSetTarget{CPUs: cpus, Mems: mems}, WriteMems: true, OwnsMems: true, Direction: WriteShrink,
					},
					{
						Rel: "other", ExpectedIdentity: d.nodes["other"].identity, ExpectedCurrent: CPUSetTarget{CPUs: machine.MustParse("0-3"), Mems: "0"},
						Target: CPUSetTarget{CPUs: machine.MustParse("0-2"), Mems: "0"}, Direction: WriteShrink,
					},
				}}
				plan.PlanID = canonicalExecutionPlanID(plan)
				for i := range plan.Operations {
					plan.Operations[i].PlanID = plan.PlanID
				}
				failed := errors.New("second operation refused")
				driver := &phaseFailureDriver{fakeHierarchyDriver: d, mode: "fail", failure: failed}
				res := &ConvergenceResult{}
				if omitResult {
					res = nil
				}
				// This case verifies restoration and exact write accounting.
				// Triggered-deadline tests cover expiry independently.
				budget := NewAdjustmentBudget(context.Background(), ConvergenceBudget{DeadlineDuration: -1})
				budget.maxWrites = 6
				writer := newSafeCPUSetWriter(driver, NewBudgetTracker(ConvergenceBudget{}), res).withAdjustmentBudget(budget)
				writer.recoveryContextFactory = deterministicRecoveryContext
				err := writer.execute(context.Background(), plan)
				if v2 {
					// Existing ordinary mems proof rejects an inherited empty target
					// before the second operation; recovery must still restore both
					// resources. This test does not broaden that contract.
					require.ErrorIs(t, err, ErrCoordinatorPlanStale)
					require.Equal(t, 2, budget.physicalWrites)
				} else {
					require.ErrorIs(t, err, failed)
					require.Equal(t, 3, budget.physicalWrites)
				}
				require.NotContains(t, err.Error(), "rollback failed")
				require.Equal(t, before, d.snapshot(), "both configured and effective CPUs/mems must restore")
				require.Equal(t, 2, budget.rollbackWrites)
				require.Zero(t, budget.reservedWrites)
				require.Len(t, d.writes, 4, "two forward and two inverse successful writes")
				if res != nil {
					require.Zero(t, res.Applied)
					if v2 {
						require.Len(t, res.Journal, 1, "existing post-write mismatch evidence is retained")
						require.True(t, res.Journal[0].Observed.CPUs.Equals(before["root/leaf"].cpus))
						require.Equal(t, before["root/leaf"].mems, res.Journal[0].Observed.Mems)
					} else {
						require.Empty(t, res.Journal)
					}
					require.False(t, res.ReplanDisposition.AllowsReplan())
				}
			})
		}
	}
}

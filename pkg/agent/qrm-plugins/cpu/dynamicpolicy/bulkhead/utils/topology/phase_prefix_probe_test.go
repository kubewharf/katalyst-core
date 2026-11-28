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

	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

// Ancestor activity changing after the first write must reject the remaining
// operations and restore the ordinary PhasePlan's already-written prefix.
func TestPhaseWriterAncestorActivationRestoresWrittenPrefix(t *testing.T) {
	t.Parallel()

	f, _ := dormantTransferFixture(t)
	f.driver.markInactive("reclaim")
	raw, err := newDormantCompleteSnapshotSource(f.driver, f.round.dag, f.budget)(context.Background())
	require.NoError(t, err)
	m := materializeTargets(f.round.dag, raw, false, f.round.semanticTargetByRel)
	f.driver.add("other", CgroupIdentity{Device: 1, Inode: f.allocInode()}, "0-3", "0")
	other := f.driver.nodes["other"]
	initial := f.driver.snapshot()
	pod := f.driver.nodes["primary/pod"]
	primary := f.driver.nodes["primary"]
	target := primary.cpus.Difference(machine.NewCPUSet(1, 49))
	plan := PhasePlan{ConvergenceID: "ancestor-prefix", Kind: PhaseDrain, Capabilities: cgroupV1Policy.capabilities(true), Operations: []PlanOperation{
		{
			Rel: "primary/pod", ExpectedIdentity: pod.identity, ParentRel: "primary", ExpectedParentIdentity: primary.identity,
			ExpectedCurrent: CPUSetTarget{CPUs: pod.cpus.Clone(), Mems: pod.mems}, Target: CPUSetTarget{CPUs: target, Mems: pod.mems}, Direction: WriteShrink,
		},
		{
			Rel: "other", ExpectedIdentity: other.identity,
			ExpectedCurrent: CPUSetTarget{CPUs: other.cpus.Clone(), Mems: other.mems}, Target: CPUSetTarget{CPUs: machine.MustParse("0-2"), Mems: other.mems}, Direction: WriteShrink,
		},
	}}
	plan.PlanID = canonicalExecutionPlanID(plan)
	for i := range plan.Operations {
		plan.Operations[i].PlanID = plan.PlanID
	}
	f.driver.writeHook = func(d *fakeHierarchyDriver, _ fakeHierarchyWrite) error {
		if len(d.writes) == 0 {
			d.nodes["reclaim"].activity.TasksEmpty = false
		}
		return nil
	}
	writer := newSafeCPUSetWriter(f.driver, NewBudgetTracker(ConvergenceBudget{}), nil)
	// This probe checks prefix restoration; recovery expiry has separate coverage.
	writer.recoveryContextFactory = deterministicRecoveryContext
	writer.dormantProofs = m.DormantProofs
	err = writer.execute(context.Background(), plan)
	require.ErrorIs(t, err, ErrCoordinatorPlanStale)
	t.Logf("error=%v writes=%d initial_pod=%s current_pod=%s", err, len(f.driver.writes), initial["primary/pod"].cpus, f.driver.nodes["primary/pod"].cpus)
	require.NotEmpty(t, f.driver.writes, "probe must reach the first actual write")
	require.True(t, initial["primary/pod"].cpus.Equals(f.driver.nodes["primary/pod"].cpus),
		"ordinary writer must not leave an earlier operation applied after ancestor rejection")
}

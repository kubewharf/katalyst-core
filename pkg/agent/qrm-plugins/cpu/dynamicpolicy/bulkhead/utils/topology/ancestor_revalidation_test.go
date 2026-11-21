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

// The direct-occupancy observation used to remove an ancestor containment mask
// must still be valid before any write. The first write is intentionally on a
// different branch, so narrow leaf-only checks cannot accidentally cover it.
func TestAncestorContainmentProofRevalidatedBeforeFirstWrite(t *testing.T) {
	t.Parallel()

	for _, change := range []string{"stable", "tasks", "processes", "identity", "child"} {
		change := change
		t.Run(change, func(t *testing.T) {
			t.Parallel()
			f, _ := dormantTransferFixture(t)
			f.driver.markInactive("reclaim")
			raw, err := newDormantCompleteSnapshotSource(f.driver, f.round.dag, f.budget)(context.Background())
			require.NoError(t, err)
			m := materializeTargets(f.round.dag, raw, false, f.round.semanticTargetByRel)
			require.Contains(t, m.DormantRels, "reclaim/bucket-1")
			switch change {
			case "tasks":
				f.driver.nodes["reclaim"].activity.TasksEmpty = false
			case "processes":
				f.driver.nodes["reclaim"].activity.CgroupProcsEmpty = false
			case "identity":
				f.driver.bumpIdentity("reclaim")
			case "child":
				f.driver.add("reclaim/new-child", CgroupIdentity{Device: 1, Inode: f.allocInode()}, "24,72", "1")
			}
			primary := f.driver.nodes["primary"]
			target := primary.cpus.Difference(machine.NewCPUSet(1, 49))
			// Child cpusets still fit this narrowed primary at preflight.
			pod := f.driver.nodes["primary/pod"]
			pod.cpus = pod.cpus.Difference(machine.NewCPUSet(1, 49))
			pod.configuredCPUs = pod.cpus.Clone()
			plan := PhasePlan{ConvergenceID: "ancestor-proof", Kind: PhaseDrain, Capabilities: cgroupV1Policy.capabilities(true), Operations: []PlanOperation{{
				Rel: "primary", ExpectedIdentity: primary.identity,
				ExpectedCurrent: CPUSetTarget{CPUs: primary.cpus.Clone(), Mems: primary.mems},
				Target:          CPUSetTarget{CPUs: target, Mems: primary.mems}, Direction: WriteShrink,
			}}}
			plan.PlanID = canonicalExecutionPlanID(plan)
			plan.Operations[0].PlanID = plan.PlanID
			writer := newSafeCPUSetWriter(f.driver, NewBudgetTracker(ConvergenceBudget{}), nil)
			writer.dormantProofs = m.DormantProofs
			err = writer.execute(context.Background(), plan)
			if change == "stable" {
				require.NoError(t, err)
				require.Len(t, f.driver.writes, 1)
				require.True(t, primary.cpus.Equals(target))
			} else {
				require.ErrorIs(t, err, ErrCoordinatorPlanStale, "stale ancestor containment cannot authorize any write")
				require.Empty(t, f.driver.writes, "stale evidence must stop before first physical write")
				require.True(t, primary.cpus.Equals(plan.Operations[0].ExpectedCurrent.CPUs))
			}
		})
	}
}

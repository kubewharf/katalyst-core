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

// This uses a synthetic hierarchy and the actual compiled-trace writer. It is
// not a replay of the node's Mixed target or an automatic envelope migration.
func TestFrozenTraceAncestorActivationRollsBackWrittenPrefix(t *testing.T) {
	t.Parallel()

	f := newAdmissionTraceFixture(t)
	f.round.allowEmptyTarget = false
	f.addPrimary("primary", "0-3", "0")
	f.addDynamicDescendant("primary/pod", "0-3", "0")
	f.addReclaim("reclaim", "0,4", "0-1")
	f.addReclaim("reclaim/idle", "0", "0")
	f.addReclaim("reclaim/active", "4", "1")
	f.cpuDetails[4] = machine.CPUTopoInfo{NUMANodeID: 1}
	f.round.cpuDetails = f.cpuDetails
	for i := range f.specs {
		switch f.specs[i].Rel {
		case "primary", "primary/pod":
			f.specs[i].CPUs = machine.MustParse("0-2")
		case "reclaim":
			f.specs[i].CPUs = machine.NewCPUSet(4)
		case "reclaim/active":
			f.specs[i].Role = TopoNodeRoleReclaimNUMABucket
			f.specs[i].Constraint = TopologyConstraint{CPUUpperBound: machine.NewCPUSet(4)}
		case "reclaim/idle":
			f.specs[i].Role = TopoNodeRoleReclaimNUMABucket
			f.specs[i].CPUs = machine.NewCPUSet()
			f.specs[i].Constraint = TopologyConstraint{CPUUpperBound: machine.NewCPUSet(0)}
		}
		f.targetByRel[f.specs[i].Rel] = f.specs[i].CPUs.Clone()
	}
	f.dynamicByRel["primary/pod"] = machine.MustParse("0-2")
	f.driver.markInactive("reclaim")
	f.driver.markInactive("reclaim/idle")
	f.snapshot()
	f.round.semanticTargetByRel = desiredTargets(f.round.dag)
	raw, err := newDormantCompleteSnapshotSource(f.driver, f.round.dag, f.budget)(context.Background())
	require.NoError(t, err)
	prepared, err := f.round.preparePlanningSnapshot(context.Background(), raw)
	require.NoError(t, err)
	f.round.objective = ConvergenceObjectiveFull
	trace, err := f.round.compileFixedPointTrace(context.Background(), prepared)
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(flattenTraceOperations(trace)), 2, "must reach a check after first successful write")
	initial := f.driver.snapshot()
	round := frozenExecutionRound(t, trace, f.driver)
	round.dormantProofs = f.round.dormantProofs
	f.driver.writeHook = func(d *fakeHierarchyDriver, _ fakeHierarchyWrite) error {
		if len(d.writes) == 0 {
			d.nodes["reclaim"].activity.TasksEmpty = false
		}
		return nil
	}
	ticket := reserveTraceWithBudget(t, round.budget, trace)
	res := &ConvergenceResult{}
	_, err = round.executeFrozenTrace(context.Background(), trace, ticket, res)
	require.ErrorIs(t, err, ErrCoordinatorPlanStale)
	require.Equal(t, 1, res.forwardWriteAttempts, "activation must stop the second forward write")
	require.Equal(t, 1, res.rollbackWriteAttempts, "the written prefix must roll back")
	for rel, n := range initial {
		require.True(t, n.cpus.Equals(f.driver.nodes[rel].cpus), "cpuset restored for %s", rel)
	}
	require.False(t, res.Published)
}

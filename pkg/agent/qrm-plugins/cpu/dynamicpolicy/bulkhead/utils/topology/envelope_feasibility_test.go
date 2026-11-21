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

// This is a counterfactual feasibility check, not an automatic relocation or a
// complete node reproduction. Only the nonempty NUMA1 envelope is varied.
func TestEmptyNUMABucketHasNoDisjointEnvelopeUnderExactTargets(t *testing.T) {
	t.Parallel()

	f, base := dormantTransferFixture(t)
	reclaimTarget := f.round.semanticTargetByRel["reclaim"]
	primaryTarget := f.round.semanticTargetByRel["primary"]
	upper := machine.MustParse("24-47,72-95")
	require.True(t, reclaimTarget.Intersection(upper).IsEmpty())
	require.True(t, upper.IsSubsetOf(primaryTarget))
	require.Contains(t, f.round.dormantRels, "reclaim/bucket-1")
	f.round.objective = ConvergenceObjectiveFull
	_, err := f.round.compileFixedPointTrace(context.Background(), base)
	require.Error(t, err, "the recorded exact target must not be reported as fully converged")
	require.Empty(t, f.driver.writes, "compilation is read-only")
	t.Logf("exact target: primary=%s reclaim=%s idle_upper=%s compile_error=%v", primaryTarget, reclaimTarget, upper, err)

	for core := 24; core < 48; core++ {
		envelope := machine.NewCPUSet(core, core+48)
		require.False(t, envelope.IsEmpty())
		require.True(t, envelope.IsSubsetOf(upper))
		require.True(t, envelope.IsSubsetOf(primaryTarget), "every NUMA-local whole-core envelope remains in exact primary target")
		require.False(t, envelope.IsSubsetOf(reclaimTarget), "no core can fit inside the exact reclaim root target")
		// Pre-setting an envelope is deliberately a counterfactual. It is never
		// passed to a live writer or claimed as an implemented migration.
		counter, _ := dormantTransferFixture(t)
		counter.driver.nodes["reclaim/bucket-1"].cpus = envelope.Clone()
		counter.driver.nodes["reclaim/bucket-1"].configuredCPUs = envelope.Clone()
		rootMask := machine.NewCPUSet(0, 48).Union(envelope)
		for _, rel := range []string{"reclaim", "system"} {
			counter.driver.nodes[rel].cpus = rootMask.Clone()
			counter.driver.nodes[rel].configuredCPUs = rootMask.Clone()
		}
		raw, err := newDormantCompleteSnapshotSource(counter.driver, counter.round.dag, counter.budget)(context.Background())
		require.NoError(t, err)
		raw, err = counter.round.preparePlanningSnapshot(context.Background(), raw)
		require.NoError(t, err)
		counter.round.objective = ConvergenceObjectiveFull
		trace, err := counter.round.compileFixedPointTrace(context.Background(), raw)
		if err == nil {
			require.True(t, trace.FinalEvaluation.Report.FullyConverged)
			t.Logf("counterfactual envelope=%s full=true leaf=%s root=%s primary=%s", envelope,
				trace.FinalSnapshot.Entries["primary/pod/leaf"].CPUs,
				trace.FinalSnapshot.Entries["reclaim"].CPUs, trace.FinalSnapshot.Entries["primary"].CPUs)
		} else {
			t.Logf("counterfactual envelope=%s compile_error=%v", envelope, err)
		}
		require.Empty(t, counter.driver.writes)
	}
}

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

package dynamicpolicy

import (
	"math/rand"
	"testing"

	"github.com/stretchr/testify/require"

	apiconsts "github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

// isWholeCore reports whether s occupies complete physical cores: every CPU in
// s must have all of its SMT siblings also in s. This is a structural property
// derived from the topology itself, so it holds for any dummy topology.
func isWholeCore(topology *machine.CPUTopology, s machine.CPUSet) bool {
	present := make(map[int]bool, s.Size())
	for _, cpu := range s.ToSliceInt() {
		present[cpu] = true
	}
	for cpu := range present {
		coreID := topology.CPUDetails[cpu].CoreID
		for sibling, detail := range topology.CPUDetails {
			if detail.CoreID == coreID && !present[sibling] {
				return false
			}
		}
	}
	return true
}

// rematRampUpAllocation builds a RampUp shared-cores AllocationInfo pinned to
// `allocation`, optionally carrying the shared-NUMA-binding annotation. It
// mirrors the shape produced by the committed
// TestRematerializeRampUpSharedAgainstFinalReclaim fixtures.
func rematRampUpAllocation(topology *machine.CPUTopology, allocation machine.CPUSet, snb bool) *state.AllocationInfo {
	annotations := map[string]string{}
	if snb {
		annotations[apiconsts.PodAnnotationMemoryEnhancementNumaBinding] = apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable
	}
	assignments, err := machine.GetNumaAwareAssignments(topology, allocation)
	if err != nil {
		panic(err)
	}
	return &state.AllocationInfo{
		AllocationMeta: commonstate.AllocationMeta{
			QoSLevel:      apiconsts.PodAnnotationQoSLevelSharedCores,
			Annotations:   annotations,
			PodUid:        "ramp-up-pod",
			ContainerName: "main",
		},
		RampUp:                           true,
		AllocationResult:                 allocation.Clone(),
		OriginalAllocationResult:         allocation.Clone(),
		TopologyAwareAssignments:         assignments,
		OriginalTopologyAwareAssignments: machine.DeepcopyCPUAssignment(assignments),
	}
}

// FuzzRampUpRematerializeInvariants property-tests the post-ef9742497
// contract of rematerializeRampUpSharedAgainstFinalReclaim against randomly
// generated ramp-up fleets and random final reclaim pools:
//
//   - Plain and shared-NUMA-binding (SNB) ramp-up allocations, when the call
//     succeeds, end up disjoint from the final reclaim pool, confined to their
//     own original remainder (never widening into reclaim CPUs), and whole-core
//     whenever rematerialization actually selects a replacement. SNB inputs are
//     generated within one NUMA, so the subset invariant also proves the result
//     never crosses its original NUMA scope.
//   - The reclaim pool entry itself, steady (non-ramp-up) containers, and
//     reclaimed-cores containers are never touched.
//
// Fail-closed configurations (a ramp-up allocation fully covered by reclaim, or
// one left without a complete physical core) surface as a non-nil error; on that
// branch allocations may be partially rewritten in iteration order, so only the
// always-holds invariants (current/original allocation stay synchronized, decoys
// and reclaim untouched) are asserted.
func FuzzRampUpRematerializeInvariants(f *testing.F) {
	f.Add(int64(1))
	f.Add(int64(7))
	f.Add(int64(42))
	f.Add(int64(20261007))

	f.Fuzz(func(t *testing.T, seed int64) {
		topology, err := machine.GenerateDummyCPUTopology(16, 1, 4)
		require.NoError(t, err)
		policy, err := getTestDynamicPolicyWithoutInitialization(topology, t.TempDir())
		require.NoError(t, err)

		rng := rand.New(rand.NewSource(seed))

		// pickSubsetFrom returns a random non-empty subset of candidates.
		pickSubsetFrom := func(candidates machine.CPUSet) machine.CPUSet {
			set := machine.NewCPUSet()
			for _, cpu := range candidates.ToSliceInt() {
				if rng.Intn(2) == 0 {
					set.Add(cpu)
				}
			}
			if set.IsEmpty() {
				cpus := candidates.ToSliceInt()
				set.Add(cpus[rng.Intn(len(cpus))])
			}
			return set
		}
		pickSubset := func() machine.CPUSet {
			return pickSubsetFrom(topology.CPUDetails.CPUs())
		}

		type track struct {
			orig machine.CPUSet
		}
		entries := state.PodEntries{}
		tracked := map[string]track{}

		// A random fleet of 1..4 ramp-up shared containers, half SNB. Real SNB
		// allocations are single-NUMA, so generate them from one NUMA's CPU set.
		nPods := 1 + rng.Intn(4)
		for i := 0; i < nPods; i++ {
			snb := rng.Intn(2) == 0
			cpus := pickSubset()
			if snb {
				numaID := rng.Intn(topology.NumNUMANodes)
				cpus = pickSubsetFrom(topology.CPUDetails.CPUsInNUMANodes(numaID))
			}
			ai := rematRampUpAllocation(topology, cpus, snb)
			pod := string(rune('a'+i)) + "-ramp"
			ai.PodUid = pod
			entries[pod] = state.ContainerEntries{"main": ai}
			tracked[pod] = track{orig: cpus.Clone()}
		}

		// Decoy containers that must never be rewritten:
		//  - a steady (materialized, RampUp=false) shared container;
		steady := rematRampUpAllocation(topology, pickSubset(), false)
		steady.RampUp = false
		steady.PodUid = "steady"
		entries["steady"] = state.ContainerEntries{"main": steady}
		tracked["steady"] = track{orig: steady.AllocationResult.Clone()}

		//  - a reclaimed-cores ramp-up container (skipped via CheckReclaimed()).
		reclaimed := rematRampUpAllocation(topology, pickSubset(), false)
		reclaimed.QoSLevel = apiconsts.PodAnnotationQoSLevelReclaimedCores
		reclaimed.PodUid = "reclaimed"
		entries["reclaimed"] = state.ContainerEntries{"main": reclaimed}
		tracked["reclaimed"] = track{orig: reclaimed.AllocationResult.Clone()}

		// Random final reclaim pool. Allow it to be empty (no-op path) as well.
		reclaimResult := pickSubset()
		if rng.Intn(3) == 0 {
			reclaimResult = machine.NewCPUSet()
		}
		entries[commonstate.PoolNameReclaim] = state.ContainerEntries{
			commonstate.FakedContainerName: {
				AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
				AllocationResult: reclaimResult.Clone(),
			},
		}

		err = policy.rematerializeRampUpSharedAgainstFinalReclaim(entries)

		// The reclaim pool entry itself is never mutated.
		gotReclaim := entries[commonstate.PoolNameReclaim][commonstate.FakedContainerName].AllocationResult
		require.True(t, gotReclaim.Equals(reclaimResult),
			"seed=%d: reclaim pool mutated: before=%s after=%s", seed, reclaimResult, gotReclaim)

		for pod, tr := range tracked {
			ai := entries[pod]["main"]
			require.NotNil(t, ai)
			require.True(t, ai.OriginalAllocationResult.Equals(ai.AllocationResult),
				"seed=%d: %s current/original allocation diverged: current=%s original=%s",
				seed, pod, ai.AllocationResult, ai.OriginalAllocationResult)
			if pod == "steady" || pod == "reclaimed" {
				// Non-ramp-up / reclaimed decoys are always skipped.
				require.True(t, ai.AllocationResult.Equals(tr.orig),
					"seed=%d: decoy %s mutated: before=%s after=%s", seed, pod, tr.orig, ai.AllocationResult)
				continue
			}
			// Ramp-up shared/SNB: only constrain when the call succeeded; on a
			// fail-closed error earlier entries may already have been rewritten
			// and later ones left untouched, so nothing deterministic can be said.
			if err == nil {
				overlap := ai.AllocationResult.Intersection(reclaimResult)
				require.True(t, overlap.IsEmpty(),
					"seed=%d: ramp-up %s still overlaps reclaim: alloc=%s reclaim=%s overlap=%s",
					seed, pod, ai.AllocationResult, reclaimResult, overlap)
				// Never widen beyond (original - reclaim). For SNB this also
				// proves the result stays inside its original single-NUMA scope.
				require.True(t, ai.AllocationResult.IsSubsetOf(tr.orig.Difference(reclaimResult)),
					"seed=%d: ramp-up %s widened: after=%s not subset of orig-minus-reclaim(%s - %s)",
					seed, pod, ai.AllocationResult, tr.orig, reclaimResult)
				// Whole-core alignment only applies to entries the rematerialization
				// actually re-selected. An already-disjoint allocation takes the
				// zero-churn path and may retain an arbitrary original cpuset.
				if origOverlap := tr.orig.Intersection(reclaimResult); !origOverlap.IsEmpty() &&
					!ai.AllocationResult.IsEmpty() {
					require.True(t, isWholeCore(topology, ai.AllocationResult),
						"seed=%d: rematerialized ramp-up %s result %s is not whole-core",
						seed, pod, ai.AllocationResult)
				}
			}
		}
	})
}

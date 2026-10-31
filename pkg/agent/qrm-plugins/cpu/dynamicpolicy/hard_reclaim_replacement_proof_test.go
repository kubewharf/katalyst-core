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
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

// TestReplacementProof_TargetDrivenDedicatedShrinksToFrozenTarget proves the
// wired production path: a target-driven reclaim dedicated source may shrink
// from its pre-reclaim footprint to its frozen sourceTarget, and the proof
// accepts that (instead of rejecting it as a before!=after ownership change).
func TestReplacementProof_TargetDrivenDedicatedShrinksToFrozenTarget(t *testing.T) {
	t.Parallel()
	topology, err := machine.GenerateDummyCPUTopology(16, 1, 1)
	require.NoError(t, err)

	demands := []partitionDemand{
		{
			key: "rec", class: advisorBlockClassMandatoryReclaim, quantity: 2,
			preferred: machine.NewCPUSet(0, 8), eligible: machine.NewCPUSet(0, 8),
		},
		{
			key: "td", class: advisorBlockClassDedicated, requestGroupKey: "td",
			quantity: 2, requestQuantity: 4,
			preferred: machine.NewCPUSet(1, 9, 2, 10, 3, 11), eligible: machine.NewCPUSet(1, 9, 2, 10, 3, 11),
			targetDriven: true, sourceTarget: 2,
		},
		{
			key: "legacy", class: advisorBlockClassDedicated, requestGroupKey: "legacy",
			quantity: 2, requestQuantity: 2,
			preferred: machine.NewCPUSet(4, 12), eligible: machine.NewCPUSet(4, 12),
		},
	}
	assignments := map[string]machine.CPUSet{
		"rec":    machine.NewCPUSet(0, 8),
		"td":     machine.NewCPUSet(1, 9),  // shrank 6 -> frozen target 2
		"legacy": machine.NewCPUSet(4, 12), // legacy keeps before==after
	}
	proof, err := validateHardReclaimReplacement(demands, assignments, topology, map[int]int{0: 2})
	require.NoError(t, err)
	require.Equal(t, 2, proof.dedicatedAfterByGroup["td"].Size())
}

func TestReplacementProof_TargetDrivenBelowFrozenTargetRejected(t *testing.T) {
	t.Parallel()
	topology, err := machine.GenerateDummyCPUTopology(16, 1, 1)
	require.NoError(t, err)

	demands := []partitionDemand{
		{
			key: "rec", class: advisorBlockClassMandatoryReclaim, quantity: 2,
			preferred: machine.NewCPUSet(0, 8), eligible: machine.NewCPUSet(0, 8),
		},
		{
			key: "td", class: advisorBlockClassDedicated, requestGroupKey: "td",
			quantity: 2, requestQuantity: 4,
			preferred: machine.NewCPUSet(1, 9, 2, 10, 3, 11), eligible: machine.NewCPUSet(1, 9, 2, 10, 3, 11),
			targetDriven: true, sourceTarget: 4,
		}, // frozen floor is 4
	}
	assignments := map[string]machine.CPUSet{
		"rec": machine.NewCPUSet(0, 8),
		"td":  machine.NewCPUSet(2, 3), // after=2 < floor 4
	}
	_, err = validateHardReclaimReplacement(demands, assignments, topology, map[int]int{0: 2})
	require.ErrorContains(t, err, "below frozen target")
}

// TestReplacementRejectsCrossNUMAMismatchWithEqualSum proves per-NUMA exact
// equality: same group total but NUMA rebalance must be rejected.
func TestReplacementRejectsCrossNUMAMismatchWithEqualSum(t *testing.T) {
	t.Parallel()
	topology, err := machine.GenerateDummyCPUTopology(64, 1, 2)
	require.NoError(t, err)

	demands := []partitionDemand{
		{
			key: "g0", requestGroupKey: "g0", class: advisorBlockClassDedicated,
			targetDriven: true, sourceTarget: 28, quantity: 28, numaID: 0,
			eligible:  machine.NewCPUSet(0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31, 32, 33, 34, 35, 36, 37, 38, 39, 40, 41, 42, 43, 44, 45, 46, 47, 48, 49, 50, 51, 52, 53, 54, 55, 56, 57, 58, 59, 60, 61, 62, 63),
			preferred: machine.NewCPUSet(0, 16, 1, 17, 2, 18, 3, 19, 4, 20, 5, 21, 6, 22, 7, 23, 8, 24, 9, 25, 10, 26, 11, 27, 12, 28, 13, 29),
		},
		{
			key: "g1", requestGroupKey: "g0", class: advisorBlockClassDedicated,
			targetDriven: true, sourceTarget: 30, quantity: 30, numaID: 1,
			eligible:  machine.NewCPUSet(0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31, 32, 33, 34, 35, 36, 37, 38, 39, 40, 41, 42, 43, 44, 45, 46, 47, 48, 49, 50, 51, 52, 53, 54, 55, 56, 57, 58, 59, 60, 61, 62, 63),
			preferred: machine.NewCPUSet(32, 48, 33, 49, 34, 50, 35, 51, 36, 52, 37, 53, 38, 54, 39, 55, 40, 56, 41, 57, 42, 58, 43, 59, 44, 60, 45, 61, 46, 62),
		},
	}
	// Cross-NUMA rebalance: g0 keeps 28 cpus (26 on NUMA0 + 2 on NUMA1).
	// per-source floor 28>=28, group sum 58 ok, but NUMA0 after=26!=28.
	assignBad := map[string]machine.CPUSet{
		"g0": demands[0].preferred.Difference(machine.NewCPUSet(13, 29)).Union(machine.NewCPUSet(47, 63)),
		"g1": demands[1].preferred.Clone(),
	}
	_, err = validateHardReclaimReplacement(demands, assignBad, topology, map[int]int{0: 0, 1: 0})
	require.Error(t, err)
	require.ErrorContains(t, err, "NUMA")
}

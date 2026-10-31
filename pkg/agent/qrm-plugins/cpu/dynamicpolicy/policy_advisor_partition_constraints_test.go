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

package dynamicpolicy

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

func TestSolveDisjointPartitionsWithCoreFloorsPrefersConflictFreeCandidate(t *testing.T) {
	t.Parallel()
	topology, err := machine.GenerateDummyCPUTopology(130, 1, 1)
	require.NoError(t, err)

	narrowEligible := machine.NewCPUSet()
	for cpu := 0; cpu < 64; cpu++ {
		narrowEligible.Add(cpu)
	}
	allCPUs := topology.CPUDetails.CPUs()
	demands := []partitionDemand{
		{
			key:      "narrow",
			quantity: 64,
			eligible: narrowEligible,
			class:    advisorBlockClassDedicated,
		},
		{
			key:       "floor",
			quantity:  2,
			eligible:  allCPUs,
			preferred: machine.NewCPUSet(64, 65),
			class:     advisorBlockClassMandatoryReclaim,
		},
	}

	assignments, err := solveDisjointPartitionsWithCoreFloors(
		demands,
		[]partitionCoreFloorConstraint{{demandKey: "floor"}},
		topology,
	)
	require.NoError(t, err)
	require.Equal(t, machine.NewCPUSet(64, 129), assignments["floor"])
	requireCoreAligned(t, topology, assignments["floor"])
}

func TestSolveDisjointPartitionsWithCoreFloorsRejectsNonPositiveFloorDemand(t *testing.T) {
	t.Parallel()
	topology, err := machine.GenerateDummyCPUTopology(8, 1, 1)
	require.NoError(t, err)

	_, err = solveDisjointPartitionsWithCoreFloors(
		[]partitionDemand{{
			key:      "floor",
			quantity: 0,
			eligible: topology.CPUDetails.CPUs(),
			class:    advisorBlockClassMandatoryReclaim,
		}},
		[]partitionCoreFloorConstraint{{demandKey: "floor"}},
		topology,
	)

	require.ErrorContains(t, err, `partition core-floor demand "floor" has non-positive quantity 0`)
}

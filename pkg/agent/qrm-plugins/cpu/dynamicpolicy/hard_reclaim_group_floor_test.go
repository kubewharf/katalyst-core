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

// twoTargetDrivenDonors builds two target-driven donors in the same group on
// numa0, each with 14 cpus (7 cores) and a frozen sourceTarget.
func twoTargetDrivenDonors(topology *machine.CPUTopology, perTarget int) []hardReclaimPartitionDonor {
	// numa0 SMT2: core c = {c, c+16} for a 32-cpu topology.
	cpusA := machine.NewCPUSet(0, 16, 1, 17, 2, 18, 3, 19, 4, 20, 5, 21, 6, 22)
	cpusB := machine.NewCPUSet(7, 23, 8, 24, 9, 25, 10, 26, 11, 27, 12, 28, 13, 29)
	return []hardReclaimPartitionDonor{
		{key: "A", groupKey: "g", cpus: cpusA, requestQuantity: 14, targetDriven: true, sourceTarget: perTarget, reclaimQuota: 100},
		{key: "B", groupKey: "g", cpus: cpusB, requestQuantity: 14, targetDriven: true, sourceTarget: perTarget, reclaimQuota: 100},
	}
}

// TestFastPath_GroupFloorIsSumOfSourceTargets proves the per-group retained floor
// is the SUM of the per-source frozen targets, not the MAX. Two sources each
// target 12 (sum=24) can donate at most 4 cpus (28-24, quota-capped at 4 total).
func TestFastPath_GroupFloorIsSumOfSourceTargets(t *testing.T) {
	t.Parallel()
	topology, err := machine.GenerateDummyCPUTopology(32, 1, 1)
	require.NoError(t, err)

	in := hardReclaimPartitionInput{
		topology:        topology,
		targetByNUMA:    map[int]int{0: 8}, // asks for 8, but only 4 are donatable
		currentReclaim:  machine.NewCPUSet(),
		reclaimEligible: topology.CPUDetails.CPUs(),
		donors:          twoTargetDrivenDonors(topology, 12),
	}
	_, err = planHardReclaimPartition(in)
	require.Error(t, err, "only 4 cpus donatable (sum floor=24); 8 must be infeasible")
}

func TestFastPath_GroupFloorSumSufficientDonation(t *testing.T) {
	t.Parallel()
	topology, err := machine.GenerateDummyCPUTopology(32, 1, 1)
	require.NoError(t, err)

	in := hardReclaimPartitionInput{
		topology:        topology,
		targetByNUMA:    map[int]int{0: 4}, // exactly the donatable excess
		currentReclaim:  machine.NewCPUSet(),
		reclaimEligible: topology.CPUDetails.CPUs(),
		donors:          twoTargetDrivenDonors(topology, 12),
	}
	plan, err := planHardReclaimPartition(in)
	require.NoError(t, err)
	require.Equal(t, 4, plan.reclaim.Size())
	// group retained = 28 - 4 = 24 == sum of frozen targets.
	require.Equal(t, 24, plan.donorCPUs["A"].Size()+plan.donorCPUs["B"].Size())
}

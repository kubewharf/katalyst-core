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

// TestFastPath_PerSourceQuotaIsRespected proves that within a target-driven NUMA,
// a source whose frozen reclaim quota is zero never has its CPUs donated, even
// when another source on the same NUMA has spare quota.
func TestFastPath_PerSourceQuotaIsRespected(t *testing.T) {
	t.Parallel()
	topology, err := machine.GenerateDummyCPUTopology(32, 1, 1)
	require.NoError(t, err)

	// Source A: quota=0, must not donate.
	srcA := machine.NewCPUSet(0, 16, 1, 17, 2, 18, 3, 19, 4, 20, 5, 21, 6, 22)
	// Source B: quota=4 (14 cpus, frozen target 10).
	srcB := machine.NewCPUSet(7, 23, 8, 24, 9, 25, 10, 26, 11, 27, 12, 28, 13, 29)

	in := hardReclaimPartitionInput{
		topology:                 topology,
		targetByNUMA:             map[int]int{0: 2},
		currentReclaim:           machine.NewCPUSet(),
		reclaimEligible:          topology.CPUDetails.CPUs(),
		targetDrivenReclaimNUMAs: map[int]struct{}{0: {}},
		donors: []hardReclaimPartitionDonor{
			{key: "A", groupKey: "g", cpus: srcA, requestQuantity: 14, targetDriven: true, sourceTarget: 14, reclaimQuota: 0},
			{key: "B", groupKey: "g", cpus: srcB, requestQuantity: 14, targetDriven: true, sourceTarget: 10, reclaimQuota: 4},
		},
	}
	plan, err := planHardReclaimPartition(in)
	require.NoError(t, err)
	require.True(t, plan.reclaim.IsSubsetOf(srcB),
		"source A has quota=0: reclaim must come from source B only, got %v", plan.reclaim)
	// A is untouched.
	require.Equal(t, 14, plan.donorCPUs["A"].Size())
}

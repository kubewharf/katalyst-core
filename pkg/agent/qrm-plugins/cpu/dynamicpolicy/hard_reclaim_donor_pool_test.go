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

// TestFastPath_LegacyTargetOnTargetDrivenNUMAMayUseLegacyDonor proves the
// donor-pool switch is keyed on the reclaim target own target-driven-ness, not
// the NUMA. A legacy reclaim target on a NUMA that also hosts a target-driven
// donor must still be able to draw on the legacy donor.
func TestFastPath_LegacyTargetOnTargetDrivenNUMAMayUseLegacyDonor(t *testing.T) {
	t.Parallel()
	topology, err := machine.GenerateDummyCPUTopology(32, 1, 1)
	require.NoError(t, err)

	targetDrivenDonor := machine.NewCPUSet(0, 16, 1, 17, 2, 18, 3, 19, 4, 20, 5, 21, 6, 22)
	legacyDonor := machine.NewCPUSet(7, 23, 8, 24, 9, 25, 10, 26, 11, 27, 12, 28, 13, 29)

	in := hardReclaimPartitionInput{
		topology:                 topology,
		targetByNUMA:             map[int]int{0: 2},
		currentReclaim:           machine.NewCPUSet(),
		reclaimEligible:          topology.CPUDetails.CPUs(),
		targetDrivenReclaimNUMAs: map[int]struct{}{}, // legacy reclaim target
		donors: []hardReclaimPartitionDonor{
			// target-driven donor with NO spare (floor == cpus).
			{key: "td", groupKey: "td", cpus: targetDrivenDonor, requestQuantity: 14, targetDriven: true, sourceTarget: 14, reclaimQuota: 0},
			// legacy donor with spare capacity.
			{key: "leg", groupKey: "leg", cpus: legacyDonor, requestQuantity: 10},
		},
	}
	plan, err := planHardReclaimPartition(in)
	require.NoError(t, err)
	require.Equal(t, 2, plan.reclaim.Size())
	// The reclaim must come from the legacy donor, not the spare-less target-driven one.
	require.True(t, plan.reclaim.IsSubsetOf(legacyDonor),
		"legacy reclaim target should draw on legacy donor cpus, got %v", plan.reclaim)
}

// TestFastPath_TargetDrivenTargetCannotUseLegacyDonor is the G2 guard: a
// target-driven reclaim target must draw only on target-driven donor cpus.
func TestFastPath_TargetDrivenTargetCannotUseLegacyDonor(t *testing.T) {
	t.Parallel()
	topology, err := machine.GenerateDummyCPUTopology(32, 1, 1)
	require.NoError(t, err)

	targetDrivenDonor := machine.NewCPUSet(0, 16, 1, 17, 2, 18, 3, 19, 4, 20, 5, 21, 6, 22)
	legacyDonor := machine.NewCPUSet(7, 23, 8, 24, 9, 25, 10, 26, 11, 27, 12, 28, 13, 29)

	in := hardReclaimPartitionInput{
		topology:                 topology,
		targetByNUMA:             map[int]int{0: 2},
		currentReclaim:           machine.NewCPUSet(),
		reclaimEligible:          topology.CPUDetails.CPUs(),
		targetDrivenReclaimNUMAs: map[int]struct{}{0: {}}, // target-driven reclaim target
		donors: []hardReclaimPartitionDonor{
			{key: "td", groupKey: "td", cpus: targetDrivenDonor, requestQuantity: 14, targetDriven: true, sourceTarget: 12, reclaimQuota: 4},
			{key: "leg", groupKey: "leg", cpus: legacyDonor, requestQuantity: 10},
		},
	}
	plan, err := planHardReclaimPartition(in)
	require.NoError(t, err)
	require.True(t, plan.reclaim.IsSubsetOf(targetDrivenDonor),
		"target-driven reclaim target must not draw on legacy donor, got %v", plan.reclaim)
}

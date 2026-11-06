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
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

// buildTargetDrivenNUMASplit builds a real before/after split for one NUMA.
// dedicatedOldCpus is the current dedicated footprint; dedicatedTarget is the
// frozen floor the dedicated group must retain; reclaimOldCpus is
// already-reclaimed cpus; reclaimTargetCpus is the advisor desired reclaim
// size. The donor may donate exactly (dedicatedOldCpus - dedicatedTarget)
// cpus to close the gap.
func buildTargetDrivenNUMASplit(t *testing.T,
	topology *machine.CPUTopology, numa, dedicatedOldCpus, dedicatedTarget,
	reclaimOldCpus, reclaimTargetCpus int,
) (donor hardReclaimPartitionDonor, currentReclaim machine.CPUSet) {
	dedicatedCores := dedicatedOldCpus / 2
	reclaimOldCores := reclaimOldCpus / 2
	totalCores := 16 // 32 cpus / SMT2
	require.Equal(t, 0, dedicatedOldCpus%2)
	require.Equal(t, 0, reclaimOldCpus%2)
	require.Equal(t, totalCores, dedicatedCores+reclaimOldCores)

	dedicated := coresInNUMA(topology, numa, 0, dedicatedCores)
	reclaim := coresInNUMA(topology, numa, dedicatedCores, totalCores)
	require.Equal(t, dedicatedOldCpus, dedicated.Size())
	require.Equal(t, reclaimOldCpus, reclaim.Size())

	donor = hardReclaimPartitionDonor{
		key:             "target-driven-" + strconv.Itoa(numa),
		groupKey:        "target-driven-" + strconv.Itoa(numa),
		cpus:            dedicated,
		requestQuantity: 62.0, // declared request, larger than old and target (diagnostic only)
		targetDriven:    true,
		sourceTarget:    dedicatedTarget,
		reclaimQuota:    dedicatedOldCpus - dedicatedTarget, // exactly the donatable excess
	}
	return donor, reclaim
}

func runTargetDrivenNUMADonationScenario(t *testing.T, dedicatedTarget, reclaimTarget map[int][2]int) {
	t.Helper()
	topology, err := machine.GenerateDummyCPUTopology(128, 2, 4)
	require.NoError(t, err)

	targetByNUMA := make(map[int]int)
	currentReclaim := machine.NewCPUSet()
	donorByKey := make(map[int]hardReclaimPartitionDonor)
	var donors []hardReclaimPartitionDonor
	targetDrivenTargets := make(map[int]struct{})

	// Before state: NUMA0/NUMA1 dedicated=30 reclaim=2; NUMA2/3 dedicated=24 reclaim=8.
	before := map[int][2]int{0: {30, 2}, 1: {30, 2}, 2: {24, 8}, 3: {24, 8}}
	for numa, b := range before {
		dTarget := dedicatedTarget[numa][0]
		rTarget := reclaimTarget[numa][0]
		donor, reclaim := buildTargetDrivenNUMASplit(t, topology, numa, b[0], dTarget, b[1], rTarget)
		donors = append(donors, donor)
		donorByKey[numa] = donor
		currentReclaim = currentReclaim.Union(reclaim)
		targetByNUMA[numa] = rTarget
		targetDrivenTargets[numa] = struct{}{}
	}

	plan, err := planHardReclaimPartition(hardReclaimPartitionInput{
		topology:                 topology,
		targetByNUMA:             targetByNUMA,
		currentReclaim:           currentReclaim,
		reclaimEligible:          topology.CPUDetails.CPUs(),
		donors:                   donors,
		targetDrivenReclaimNUMAs: targetDrivenTargets,
	})
	require.NoError(t, err)
	requireCoreAligned(t, topology, plan.reclaim)

	for numa := range before {
		got := plan.reclaim.Intersection(topology.CPUDetails.CPUsInNUMANodes(numa)).Size()
		require.Equal(t, reclaimTarget[numa][0], got, "numa %d reclaim mismatch", numa)
		// dedicated donor must retain exactly its frozen target.
		require.Equal(t, dedicatedTarget[numa][0],
			plan.donorCPUs[donorByKey[numa].key].Size(), "numa %d dedicated retained mismatch", numa)
	}
}

// TestTargetDrivenNUMADonationShrinksNUMA0: NUMA0 must donate 2 cpus
// (dedicated 30->28, reclaim 2->4).
func TestTargetDrivenNUMADonationShrinksNUMA0(t *testing.T) {
	t.Parallel()
	runTargetDrivenNUMADonationScenario(t,
		map[int][2]int{0: {28, 0}, 1: {30, 0}, 2: {24, 0}, 3: {24, 0}}, // dedicated targets
		map[int][2]int{0: {4, 0}, 1: {2, 0}, 2: {8, 0}, 3: {8, 0}},     // reclaim targets
	)
}

// TestTargetDrivenNUMADonationShrinksNUMA1: swap, NUMA1 donates 2 cpus.
func TestTargetDrivenNUMADonationShrinksNUMA1(t *testing.T) {
	t.Parallel()
	runTargetDrivenNUMADonationScenario(t,
		map[int][2]int{0: {30, 0}, 1: {28, 0}, 2: {24, 0}, 3: {24, 0}},
		map[int][2]int{0: {2, 0}, 1: {4, 0}, 2: {8, 0}, 3: {8, 0}},
	)
}

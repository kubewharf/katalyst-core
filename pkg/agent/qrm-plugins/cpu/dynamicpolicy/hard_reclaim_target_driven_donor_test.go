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

// TestPlanHardReclaimPartitionTargetDrivenUsesFrozenTargetAndQuota is a
// four-NUMA SMT2 shrink regression: a target-driven dedicated source must
// donate exactly its frozen reclaim quota and retain its frozen source target,
// rather than donating all of its excess up to a requestQuantity ceiling.
func TestPlanHardReclaimPartitionTargetDrivenUsesFrozenTargetAndQuota(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(64, 1, 2)
	require.NoError(t, err)
	numa := topology.CPUDetails.CPUsInNUMANodes(0)
	free := coresInNUMA(topology, 0, 0, 2)      // 2 free cores = 4 cpus
	dedicated := coresInNUMA(topology, 0, 2, 6) // 4 dedicated cores = 8 cpus

	// source must keep 6 cpus (3 frozen cores); it may lend exactly 2 cpus (1 core).
	plan, err := planHardReclaimPartition(hardReclaimPartitionInput{
		topology:        topology,
		targetByNUMA:    map[int]int{0: 6},
		free:            free,
		reclaimEligible: numa,
		donors: []hardReclaimPartitionDonor{{
			key: "target-driven-a", cpus: dedicated, requestQuantity: 6,
			targetDriven: true, sourceTarget: 6, reclaimQuota: 2,
		}},
	})
	require.NoError(t, err)
	// free supplies 4 cpus, the source donates its frozen quota of 2 cpus -> 6.
	require.Equal(t, 6, plan.reclaim.Size())
	// the donor retains exactly its frozen source target of 6 cpus.
	require.Equal(t, 6, plan.donorCPUs["target-driven-a"].Size())
	// reclaim borrows exactly one core from the donor (its 2-cpu quota).
	require.Equal(t, 2, plan.reclaim.Intersection(dedicated).Size())
	requireCoreAligned(t, topology, plan.reclaim)
	requireCoreAligned(t, topology, plan.donorCPUs["target-driven-a"])
}

// TestPlanHardReclaimPartitionTargetDrivenCannotDonateBeyondQuota proves the
// frozen reclaim quota is a hard cap: even if the source has more excess and
// reclaim needs more, it does not lend past the quota.
func TestPlanHardReclaimPartitionTargetDrivenCannotDonateBeyondQuota(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(64, 1, 2)
	require.NoError(t, err)
	numa := topology.CPUDetails.CPUsInNUMANodes(0)
	free := coresInNUMA(topology, 0, 0, 1) // 1 free core = 2 cpus
	dedicated := coresInNUMA(topology, 0, 1, 6)

	// source keeps 6, may lend only 2; reclaim needs 8 but free(2)+quota(2)=4 < 8.
	_, err = planHardReclaimPartition(hardReclaimPartitionInput{
		topology:        topology,
		targetByNUMA:    map[int]int{0: 8},
		free:            free,
		reclaimEligible: numa,
		donors: []hardReclaimPartitionDonor{{
			key: "target-driven-a", cpus: dedicated, requestQuantity: 6,
			targetDriven: true, sourceTarget: 6, reclaimQuota: 2,
		}},
	})
	require.Error(t, err)
}

// TestPlanHardReclaimPartitionLegacyDonorNotConsumedByTargetDrivenReclaim is
// G2: a legacy donor on the same NUMA must never have its CPUs donated to
// satisfy a target-driven reclaim target.
func TestPlanHardReclaimPartitionLegacyDonorNotConsumedByTargetDrivenReclaim(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(64, 1, 2)
	require.NoError(t, err)
	numa := topology.CPUDetails.CPUsInNUMANodes(0)
	free := coresInNUMA(topology, 0, 0, 2) // 2 free cores = 4 cpus
	targetDrivenDedicated := coresInNUMA(topology, 0, 2, 4)
	legacyDedicated := coresInNUMA(topology, 0, 4, 6)

	// reclaim needs 6: free(4) + target-driven quota(2) = 6. The legacy donor
	// must be untouched even though it has free excess.
	plan, err := planHardReclaimPartition(hardReclaimPartitionInput{
		topology:        topology,
		targetByNUMA:    map[int]int{0: 6},
		free:            free,
		reclaimEligible: numa,
		donors: []hardReclaimPartitionDonor{
			{
				key: "target-driven-a", cpus: targetDrivenDedicated, requestQuantity: 2,
				targetDriven: true, sourceTarget: 2, reclaimQuota: 2,
			},
			{
				key: "legacy-b", cpus: legacyDedicated, requestQuantity: 4,
				targetDriven: false,
			},
		},
	})
	require.NoError(t, err)
	require.Equal(t, 6, plan.reclaim.Size())
	// the legacy donor is never donated from.
	require.True(t, plan.reclaim.Intersection(legacyDedicated).IsEmpty())
	require.Equal(t, 4, plan.donorCPUs["legacy-b"].Size())
}

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

// twoCPUTargetDrivenDonor is a 32-cpu SMT2 numa0 target-driven donor over
// `cores` whole cores.
func twoCPUTargetDrivenDonor(key string, cores int) hardReclaimPartitionDonor {
	cpus := machine.NewCPUSet()
	for c := 0; c < cores; c++ {
		cpus = cpus.Union(machine.NewCPUSet(c, c+16))
	}
	return hardReclaimPartitionDonor{
		key: key, groupKey: key, cpus: cpus, requestQuantity: 62,
		targetDriven: true, sourceTarget: cores * 2, reclaimQuota: 0,
	}
}

// TestTargetDrivenExpansionWithFreeWholeCore: reclaim grows when a free whole
// core is available in the same domain.
func TestTargetDrivenExpansionWithFreeWholeCore(t *testing.T) {
	t.Parallel()
	topology, err := machine.GenerateDummyCPUTopology(32, 1, 1)
	require.NoError(t, err)
	// donor owns cores 0-9 (20 cpus), sourceTarget=20 (no donation). Free cores
	// 10-11 (4 cpus) are available to grow reclaim.
	donor := twoCPUTargetDrivenDonor("dedicated", 10)
	free := machine.NewCPUSet(10, 26, 11, 27)
	plan, err := planHardReclaimPartition(hardReclaimPartitionInput{
		topology:                 topology,
		targetByNUMA:             map[int]int{0: 4},
		currentReclaim:           machine.NewCPUSet(),
		free:                     free,
		reclaimEligible:          topology.CPUDetails.CPUs(),
		donors:                   []hardReclaimPartitionDonor{donor},
		targetDrivenReclaimNUMAs: map[int]struct{}{0: {}},
	})
	require.NoError(t, err)
	require.Equal(t, 4, plan.reclaim.Size())
	requireCoreAligned(t, topology, plan.reclaim)
}

// TestTargetDrivenZeroTargetReleasesWholeCore: a zero reclaim target leaves no
// placeholder reclaim cpu.
func TestTargetDrivenZeroTargetReleasesWholeCore(t *testing.T) {
	t.Parallel()
	topology, err := machine.GenerateDummyCPUTopology(32, 1, 1)
	require.NoError(t, err)
	donor := twoCPUTargetDrivenDonor("dedicated", 10)
	plan, err := planHardReclaimPartition(hardReclaimPartitionInput{
		topology:                 topology,
		targetByNUMA:             map[int]int{0: 0},
		currentReclaim:           machine.NewCPUSet(10, 26),
		reclaimEligible:          topology.CPUDetails.CPUs(),
		donors:                   []hardReclaimPartitionDonor{donor},
		targetDrivenReclaimNUMAs: map[int]struct{}{0: {}},
	})
	require.NoError(t, err)
	require.True(t, plan.reclaim.IsEmpty())
}

// TestTargetDrivenOddTargetIsWholeCoreInfeasible: an odd reclaim target on SMT2
// is a typed whole-core infeasible, never silently rounded.
func TestTargetDrivenOddTargetIsWholeCoreInfeasible(t *testing.T) {
	t.Parallel()
	topology, err := machine.GenerateDummyCPUTopology(32, 1, 1)
	require.NoError(t, err)
	donor := twoCPUTargetDrivenDonor("dedicated", 10)
	_, err = planHardReclaimPartition(hardReclaimPartitionInput{
		topology:                 topology,
		targetByNUMA:             map[int]int{0: 3},
		currentReclaim:           machine.NewCPUSet(),
		reclaimEligible:          topology.CPUDetails.CPUs(),
		donors:                   []hardReclaimPartitionDonor{donor},
		targetDrivenReclaimNUMAs: map[int]struct{}{0: {}},
	})
	require.Error(t, err)
	require.Contains(t, classifyHardReclaimSolveOutcome(err), "infeasible")
}

// TestMissingRequestQuantityIsDiagnosticOnly: a donor with a zero/absent request
// quantity still honors the frozen sourceTarget; requestQuantity never overrides.
func TestMissingRequestQuantityIsDiagnosticOnly(t *testing.T) {
	t.Parallel()
	topology, err := machine.GenerateDummyCPUTopology(32, 1, 1)
	require.NoError(t, err)
	donor := twoCPUTargetDrivenDonor("dedicated", 10)
	donor.requestQuantity = 0 // absent diagnostic
	donor.sourceTarget = 18   // frozen floor: may donate one core (2 cpus)
	donor.reclaimQuota = 2
	plan, err := planHardReclaimPartition(hardReclaimPartitionInput{
		topology:                 topology,
		targetByNUMA:             map[int]int{0: 2},
		currentReclaim:           machine.NewCPUSet(),
		reclaimEligible:          topology.CPUDetails.CPUs(),
		donors:                   []hardReclaimPartitionDonor{donor},
		targetDrivenReclaimNUMAs: map[int]struct{}{0: {}},
	})
	require.NoError(t, err)
	require.Equal(t, 18, plan.donorCPUs["dedicated"].Size())
}

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

	apiconsts "github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

func TestTakeReclaimSupplementPrefersCommittedCompleteCore(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(8, 1, 1)
	require.NoError(t, err)
	committed := coresInNUMA(topology, 0, 0, 1)

	got := takeReclaimSupplementWithPreference(
		topology,
		topology.CPUDetails.CPUsInNUMANodes(0),
		committed.Size(),
		&numaBindingAllocationPreference{reclaim: committed},
	)

	require.True(t, committed.Equals(got), "committed=%s got=%s", committed, got)
}

func TestSelectNumaBindingReclaimPartitionPrefersCommittedOverConfiguredReserveIdentity(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(8, 1, 1)
	require.NoError(t, err)
	p, err := getTestDynamicPolicyWithInitialization(topology, t.TempDir())
	require.NoError(t, err)
	p.config.dynamicConfig.GetDynamicConfiguration().EnableReclaim = true
	p.config.dynamicConfig.GetDynamicConfiguration().EnableRampUpReclaimHardPartition = true

	configuredReserve := coresInNUMA(topology, 0, 0, 1)
	committedReclaim := coresInNUMA(topology, 0, 1, 2)
	eligible := topology.CPUDetails.CPUsInNUMANodes(0)
	p.reclaim.reservedReclaimedCPUSet = configuredReserve
	p.reclaim.reservedReclaimedCPUsSize = configuredReserve.Size()

	got, err := p.selectNumaBindingReclaimPartitionWithPreference(
		configuredReserve,
		map[int]machine.CPUSet{0: eligible},
		map[int]machine.CPUSet{0: eligible},
		[]uint64{0},
		true,
		&numaBindingAllocationPreference{reclaim: committedReclaim},
	)

	require.NoError(t, err)
	require.True(t, committedReclaim.Equals(got),
		"configured=%s committed=%s got=%s", configuredReserve, committedReclaim, got)
}

func TestTakeReclaimSupplementSkipsPartialCommittedCoreForNonSNB(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(8, 1, 1)
	require.NoError(t, err)
	partialReclaim := machine.NewCPUSet(coresInNUMA(topology, 0, 0, 1).ToSliceInt()[0])
	snb := coresInNUMA(topology, 0, 1, 2)
	nonSNB := coresInNUMA(topology, 0, 2, 3)
	candidates := partialReclaim.Union(snb).Union(nonSNB)

	got := takeReclaimSupplementWithPreference(
		topology,
		candidates,
		nonSNB.Size(),
		&numaBindingAllocationPreference{
			reclaim:      partialReclaim,
			snbAllocated: snb,
		},
	)

	require.True(t, nonSNB.Equals(got), "nonSNB=%s got=%s", nonSNB, got)
}

func TestTakeReclaimSupplementUsesLargestBoundWhenNoNonSNBCompleteCore(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(8, 1, 1)
	require.NoError(t, err)
	partialReclaim := machine.NewCPUSet(coresInNUMA(topology, 0, 0, 1).ToSliceInt()[0])
	smallBound := coresInNUMA(topology, 0, 1, 2)
	largeBound := coresInNUMA(topology, 0, 2, 4)
	candidates := partialReclaim.Union(smallBound).Union(largeBound)

	preference := &numaBindingAllocationPreference{
		reclaim:      partialReclaim,
		snbAllocated: smallBound.Union(largeBound),
		boundPreemptionCandidates: []boundPreemptionCandidate{
			{podUID: "large", containerName: "main", cpus: largeBound},
			{podUID: "small", containerName: "main", cpus: smallBound},
		},
	}
	got := takeReclaimSupplementWithPreference(topology, candidates, smallBound.Size(), preference)
	gotAgain := takeReclaimSupplementWithPreference(topology, candidates, smallBound.Size(), preference)

	require.True(t, got.IsSubsetOf(largeBound), "largeBound=%s got=%s", largeBound, got)
	require.True(t, got.Equals(gotAgain), "first=%s second=%s", got, gotAgain)
}

func TestTakeReclaimSupplementRanksBoundCandidatesByCurrentNUMACompleteCores(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(16, 1, 2)
	require.NoError(t, err)
	largeGlobal := coresInNUMA(topology, 0, 0, 1).
		Union(coresInNUMA(topology, 1, 0, 3))
	largeLocal := coresInNUMA(topology, 0, 1, 3)
	candidates := coresInNUMA(topology, 0, 0, 3)

	preference := &numaBindingAllocationPreference{
		snbAllocated: candidates,
		boundPreemptionCandidates: []boundPreemptionCandidate{
			{podUID: "large-global", containerName: "main", cpus: largeGlobal},
			{podUID: "large-local", containerName: "main", cpus: largeLocal},
		},
	}
	got := takeReclaimSupplementWithPreference(
		topology, candidates, topology.CPUsPerCore(), preference)

	require.True(t, got.IsSubsetOf(largeLocal), "largeLocal=%s got=%s", largeLocal, got)
}

func TestTakeReclaimSupplementUsesCurrentRequestOnlyAsFallback(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(8, 1, 1)
	require.NoError(t, err)
	current := coresInNUMA(topology, 0, 0, 1)

	got := takeReclaimSupplementWithPreference(
		topology,
		current,
		current.Size(),
		&numaBindingAllocationPreference{
			currentRequestAllocation: current,
		},
	)

	require.True(t, current.Equals(got), "current=%s got=%s", current, got)
}

func TestBoundPreemptionCandidatesIncludeDNBAndSNBOnly(t *testing.T) {
	t.Parallel()

	dnb := machine.NewCPUSet(0, 4)
	snb := machine.NewCPUSet(1, 2, 5, 6)
	sharedNonBinding := machine.NewCPUSet(3, 7)
	entries := state.PodEntries{
		"dnb": {
			"main": newBoundAllocationForIdempotencyTest(
				"dnb", "main", apiconsts.PodAnnotationQoSLevelDedicatedCores, true, dnb),
		},
		"snb": {
			"main": newBoundAllocationForIdempotencyTest(
				"snb", "main", apiconsts.PodAnnotationQoSLevelSharedCores, true, snb),
		},
		"shared": {
			"main": newBoundAllocationForIdempotencyTest(
				"shared", "main", apiconsts.PodAnnotationQoSLevelSharedCores, false, sharedNonBinding),
		},
	}

	got := boundPreemptionCandidatesBySize(entries, "", "")

	require.Len(t, got, 2)
	require.Equal(t, "snb", got[0].podUID)
	require.True(t, snb.Equals(got[0].cpus))
	require.Equal(t, "dnb", got[1].podUID)
	require.True(t, dnb.Equals(got[1].cpus))
}

func TestShouldUseNumaBindingAllocationPreferenceOnlyForExistingExclusiveDNB(t *testing.T) {
	t.Parallel()

	dnb := newBoundAllocationForIdempotencyTest(
		"dnb", "main", apiconsts.PodAnnotationQoSLevelDedicatedCores, true, machine.NewCPUSet(0, 4))
	dnb.Annotations[apiconsts.PodAnnotationMemoryEnhancementNumaExclusive] = apiconsts.PodAnnotationMemoryEnhancementNumaExclusiveEnable
	snb := newBoundAllocationForIdempotencyTest(
		"snb", "main", apiconsts.PodAnnotationQoSLevelSharedCores, true, machine.NewCPUSet(1, 5))

	require.True(t, shouldUseNumaBindingAllocationPreference(dnb))
	require.False(t, shouldUseNumaBindingAllocationPreference(snb))
	require.False(t, shouldUseNumaBindingAllocationPreference(nil))
}

func TestExistingAllocationSatisfiesRequestWithElasticNUMABindingDNB(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name      string
		exclusive bool
	}{
		{name: "exclusive", exclusive: true},
		{name: "non-exclusive", exclusive: false},
	} {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			allocation := newBoundAllocationForIdempotencyTest(
				"dnb", "main", apiconsts.PodAnnotationQoSLevelDedicatedCores, true, machine.NewCPUSet(1, 2, 5, 6))
			if tt.exclusive {
				allocation.Annotations[apiconsts.PodAnnotationMemoryEnhancementNumaExclusive] = apiconsts.PodAnnotationMemoryEnhancementNumaExclusiveEnable
			}
			allocation.RequestQuantity = 6
			allocation.RampUp = true

			require.True(t, existingAllocationSatisfiesRequest(allocation, 6, true))
			require.False(t, existingAllocationSatisfiesRequest(allocation, 6, false))
		})
	}
}

func TestExistingAllocationSatisfiesRequestRejectsIneligibleOrStaleShortAllocation(t *testing.T) {
	t.Parallel()

	allocation := newBoundAllocationForIdempotencyTest(
		"dnb", "main", apiconsts.PodAnnotationQoSLevelDedicatedCores, true, machine.NewCPUSet(1, 2, 5, 6))
	allocation.RequestQuantity = 6
	allocation.RampUp = true

	require.False(t, existingAllocationSatisfiesRequest(allocation, 6, false))

	allocation.RequestQuantity = 8
	require.False(t, existingAllocationSatisfiesRequest(allocation, 6, true))
}

func TestRemovePodOwnerEntriesForReallocationDropsSiblingContainers(t *testing.T) {
	t.Parallel()

	mainAllocation := newBoundAllocationForIdempotencyTest(
		"dnb", "main", apiconsts.PodAnnotationQoSLevelDedicatedCores, true, machine.NewCPUSet(1, 2, 5, 6))
	mpsAllocation := newBoundAllocationForIdempotencyTest(
		"dnb", "mps", apiconsts.PodAnnotationQoSLevelDedicatedCores, true, machine.NewCPUSet(1, 2, 5, 6))
	otherAllocation := newBoundAllocationForIdempotencyTest(
		"other", "main", apiconsts.PodAnnotationQoSLevelDedicatedCores, true, machine.NewCPUSet(3, 7))
	entries := state.PodEntries{
		"dnb": {
			"main": mainAllocation,
			"mps":  mpsAllocation,
		},
		"other": {
			"main": otherAllocation,
		},
	}

	removePodOwnerEntriesForReallocation(entries, "dnb", "main")

	require.NotContains(t, entries, "dnb")
	require.Same(t, otherAllocation, entries["other"]["main"])
}

func TestRemovePodOwnerEntriesForReallocationKeepsDistinctSiblingContainers(t *testing.T) {
	t.Parallel()

	mainAllocation := newBoundAllocationForIdempotencyTest(
		"dnb", "main", apiconsts.PodAnnotationQoSLevelDedicatedCores, true, machine.NewCPUSet(1, 2, 5, 6))
	sidecarAllocation := newBoundAllocationForIdempotencyTest(
		"dnb", "sidecar", apiconsts.PodAnnotationQoSLevelDedicatedCores, true, machine.NewCPUSet(3, 7))
	entries := state.PodEntries{
		"dnb": {
			"main":    mainAllocation,
			"sidecar": sidecarAllocation,
		},
	}

	removePodOwnerEntriesForReallocation(entries, "dnb", "main")

	require.NotContains(t, entries["dnb"], "main")
	require.Same(t, sidecarAllocation, entries["dnb"]["sidecar"])
}

func newBoundAllocationForIdempotencyTest(
	podUID, containerName, qosLevel string,
	numaBinding bool,
	cpus machine.CPUSet,
) *state.AllocationInfo {
	annotations := make(map[string]string)
	if numaBinding {
		annotations[apiconsts.PodAnnotationMemoryEnhancementNumaBinding] = apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable
	}
	return &state.AllocationInfo{
		AllocationMeta: commonstate.AllocationMeta{
			PodUid:        podUID,
			ContainerName: containerName,
			QoSLevel:      qosLevel,
			Annotations:   annotations,
		},
		AllocationResult:         cpus.Clone(),
		OriginalAllocationResult: cpus.Clone(),
	}
}

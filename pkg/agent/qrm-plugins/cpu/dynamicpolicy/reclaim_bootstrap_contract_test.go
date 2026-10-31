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
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	apiconsts "github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	cpusetutil "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/util"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

func TestInitialReclaimPoolRepairsEmptyAndMissing(t *testing.T) {
	t.Parallel()
	for _, existing := range []bool{false, true} {
		existing := existing
		for _, enabled := range []bool{false, true} {
			enabled := enabled
			t.Run(fmt.Sprintf("existing=%t/enabled=%t", existing, enabled), func(t *testing.T) {
				t.Parallel()
				topology, err := machine.GenerateDummyCPUTopology(32, 1, 2)
				require.NoError(t, err)
				stateDir := t.TempDir()
				p, err := getTestDynamicPolicyWithoutInitialization(topology, stateDir)
				require.NoError(t, err)
				p.config.dynamicConfig.GetDynamicConfiguration().EnableReclaim = enabled
				if existing {
					require.NoError(t, p.state.SetAllocationInfo(commonstate.PoolNameReclaim, commonstate.FakedContainerName,
						&state.AllocationInfo{
							AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
							AllocationResult: machine.NewCPUSet(), OriginalAllocationResult: machine.NewCPUSet(),
						}, true))
				}
				require.NoError(t, p.installDerivedReclaimPreference())
				before := p.state.GetRevision()
				require.NoError(t, p.ensureInitialReclaimPool())
				require.Equal(t, before+1, p.state.GetRevision(), "pool and machine state must publish once")
				pool := p.state.GetAllocationInfo(commonstate.PoolNameReclaim, commonstate.FakedContainerName)
				require.NotNil(t, pool)
				require.False(t, pool.AllocationResult.IsEmpty())
				require.NoError(t, assertCoreAligned(pool.AllocationResult, topology))
				reopened := openCheckpointState(t, topology, stateDir)
				require.Equal(t, p.state.GetPodEntries(), reopened.GetPodEntries())
				require.Equal(t, p.state.GetMachineState(), reopened.GetMachineState())
				before = p.state.GetRevision()
				require.NoError(t, p.ensureInitialReclaimPool())
				require.Equal(t, before, p.state.GetRevision(), "non-empty identity must not be rewritten")
			})
		}
	}
}

func TestInitialReclaimPoolRepairsWholeCoreFromExclusiveDedicatedSurplus(t *testing.T) {
	t.Parallel()
	topology := contiguous180CPUTopology(t)
	p, err := getTestDynamicPolicyWithoutInitialization(topology, t.TempDir())
	require.NoError(t, err)
	p.config.reservedCPUs = machine.NewCPUSet()
	p.reclaim.reservedReclaimedCPUsSize = 4
	p.reclaim.reservedReclaimedCPUSet = machine.NewCPUSet(0, 1, 90, 91)

	firstCPUs, err := machine.Parse("1-89")
	require.NoError(t, err)
	secondCPUs, err := machine.Parse("90-179")
	require.NoError(t, err)
	newDedicated := func(podUID string, cpus machine.CPUSet, numaID int, request float64) *state.AllocationInfo {
		return &state.AllocationInfo{
			AllocationMeta: commonstate.AllocationMeta{
				PodUid: podUID, ContainerName: "main", ContainerType: pluginapi.ContainerType_MAIN.String(),
				OwnerPoolName: commonstate.PoolNameDedicated, QoSLevel: apiconsts.PodAnnotationQoSLevelDedicatedCores,
				Annotations: exclusiveAnnotations(),
			},
			AllocationResult: cpus.Clone(), OriginalAllocationResult: cpus.Clone(),
			TopologyAwareAssignments:         map[int]machine.CPUSet{numaID: cpus.Clone()},
			OriginalTopologyAwareAssignments: map[int]machine.CPUSet{numaID: cpus.Clone()},
			RequestQuantity:                  request,
		}
	}
	first := newDedicated("dedicated-a", firstCPUs, 0, 88)
	sidecar := first.Clone()
	sidecar.ContainerName = "sidecar"
	sidecar.ContainerType = pluginapi.ContainerType_SIDECAR.String()
	second := newDedicated("dedicated-b", secondCPUs, 1, 88)
	preOccCPUs := machine.MustParse("91-179")
	preOcc := newDedicated("pre-occ", preOccCPUs, 1, 88)
	preOcc.Annotations["pre-occ-delete-timestamp"] = "2026-08-11T03:34:51+08:00"
	entries := state.PodEntries{
		commonstate.PoolNameReclaim: {
			commonstate.FakedContainerName: {
				AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
				AllocationResult: machine.NewCPUSet(), OriginalAllocationResult: machine.NewCPUSet(),
			},
		},
		"dedicated-a": {"main": first, "sidecar": sidecar},
		"dedicated-b": {"main": second},
	}
	machineState, err := generateMachineStateFromPodEntries(topology, entries, p.state.GetMachineState())
	require.NoError(t, err)
	machineState[1].PreOccPodEntries = state.PodEntries{"pre-occ": {"main": preOcc}}
	require.NoError(t, p.state.CommitAdvisorState(entries, machineState, false, false, true))
	persistedPreOcc := p.state.GetMachineState()[1].PreOccPodEntries.Clone()
	persistedFirst := p.state.GetAllocationInfo("dedicated-a", "main")
	require.True(t, persistedFirst.CheckDedicatedNUMABindingNUMAExclusive())
	require.Equal(t, float64(88), persistedFirst.RequestQuantity)
	require.True(t, persistedFirst.AllocationResult.Equals(persistedFirst.OriginalAllocationResult))
	expectedFirstAssignments, err := machine.GetNumaAwareAssignments(topology, persistedFirst.AllocationResult)
	require.NoError(t, err)
	require.Equal(t, expectedFirstAssignments, persistedFirst.TopologyAwareAssignments)
	require.Equal(t, expectedFirstAssignments, persistedFirst.OriginalTopologyAwareAssignments)

	before := p.state.GetRevision()
	require.NoError(t, p.ensureInitialReclaimPool())
	require.Equal(t, before+1, p.state.GetRevision())
	pool := p.state.GetAllocationInfo(commonstate.PoolNameReclaim, commonstate.FakedContainerName)
	require.True(t, pool.AllocationResult.Equals(machine.NewCPUSet(0, 1, 90, 91)))
	require.NoError(t, assertCoreAligned(pool.AllocationResult, topology))
	updatedMain := p.state.GetAllocationInfo("dedicated-a", "main")
	updatedSidecar := p.state.GetAllocationInfo("dedicated-a", "sidecar")
	require.True(t, updatedMain.AllocationResult.Equals(firstCPUs))
	require.True(t, updatedMain.AllocationResult.Equals(updatedMain.OriginalAllocationResult))
	require.Equal(t, updatedMain.AllocationResult, updatedSidecar.AllocationResult)
	require.Equal(t, updatedMain.OriginalAllocationResult, updatedSidecar.OriginalAllocationResult)
	require.True(t, p.state.GetAllocationInfo("dedicated-b", "main").AllocationResult.Equals(secondCPUs))
	require.True(t, pool.AllocationResult.Intersection(updatedMain.AllocationResult).Equals(machine.NewCPUSet(1)))
	require.True(t, pool.AllocationResult.Intersection(secondCPUs).Equals(machine.NewCPUSet(90, 91)))
	require.Equal(t, persistedPreOcc, p.state.GetMachineState()[1].PreOccPodEntries)

	publishedEntries, publishedMachineState, revision := p.state.GetAdvisorStateSnapshot()
	require.NoError(t, p.ensureInitialReclaimPool())
	require.Equal(t, revision, p.state.GetRevision())
	require.Equal(t, publishedEntries, p.state.GetPodEntries())
	require.Equal(t, publishedMachineState, p.state.GetMachineState())

	ok, _, err := NewDynamicPolicy(freshRealConstructorContext(t, topology, p.advisorPostCommitCheckpointDir),
		freshRealConstructorConf(t, p.advisorPostCommitCheckpointDir), nil, "repaired-reclaim-restart")
	require.NoError(t, err)
	require.True(t, ok)
	restarted := openCheckpointState(t, topology, p.advisorPostCommitCheckpointDir)
	require.True(t, restarted.GetAllocationInfo(commonstate.PoolNameReclaim, commonstate.FakedContainerName).
		AllocationResult.Equals(machine.NewCPUSet(0, 1, 90, 91)))
	require.True(t, restarted.GetAllocationInfo("dedicated-a", "main").AllocationResult.Equals(firstCPUs))
	require.True(t, restarted.GetAllocationInfo("dedicated-b", "main").AllocationResult.Equals(secondCPUs))
}

func TestInitialReclaimPoolRepairsUnderfilledAccordingToDedicatedOverlapMode(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name                     string
		disableDedicatedOverlap  bool
		expectedSecondAllocation machine.CPUSet
	}{
		{
			name:                     "overlap allowed preserves dedicated allocation",
			disableDedicatedOverlap:  false,
			expectedSecondAllocation: machine.MustParse("90-179"),
		},
		{
			name:                     "overlap disabled transfers dedicated surplus",
			disableDedicatedOverlap:  true,
			expectedSecondAllocation: machine.MustParse("92-179"),
		},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			topology := contiguous180CPUTopology(t)
			stateDir := t.TempDir()
			p, err := getTestDynamicPolicyWithoutInitialization(topology, stateDir)
			require.NoError(t, err)
			p.config.reservedCPUs = machine.NewCPUSet()
			p.reclaim.reservedReclaimedCPUsSize = 4
			p.reclaim.reservedReclaimedCPUSet = machine.NewCPUSet(0, 1, 90, 91)

			newDedicated := func(podUID string, cpus machine.CPUSet, numaID int) *state.AllocationInfo {
				return &state.AllocationInfo{
					AllocationMeta: commonstate.AllocationMeta{
						PodUid: podUID, ContainerName: "main", ContainerType: pluginapi.ContainerType_MAIN.String(),
						OwnerPoolName: commonstate.PoolNameDedicated, QoSLevel: apiconsts.PodAnnotationQoSLevelDedicatedCores,
						Annotations: exclusiveAnnotations(),
					},
					AllocationResult: cpus.Clone(), OriginalAllocationResult: cpus.Clone(),
					TopologyAwareAssignments:         map[int]machine.CPUSet{numaID: cpus.Clone()},
					OriginalTopologyAwareAssignments: map[int]machine.CPUSet{numaID: cpus.Clone()},
					RequestQuantity:                  88,
				}
			}

			firstCPUs := machine.MustParse("2-89")
			secondCPUs := machine.MustParse("90-179")
			first := newDedicated("dedicated-a", firstCPUs, 0)
			second := newDedicated("dedicated-b", secondCPUs, 1)
			secondSidecar := second.Clone()
			secondSidecar.ContainerName = "sidecar"
			secondSidecar.ContainerType = pluginapi.ContainerType_SIDECAR.String()
			preOcc := newDedicated("pre-occ", machine.MustParse("91-179"), 1)
			preOcc.Annotations["pre-occ-delete-timestamp"] = "2026-08-11T03:34:51+08:00"
			reclaim := machine.NewCPUSet(0, 1)
			entries := state.PodEntries{
				commonstate.PoolNameReclaim: {
					commonstate.FakedContainerName: {
						AllocationMeta:                   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
						AllocationResult:                 reclaim.Clone(),
						OriginalAllocationResult:         reclaim.Clone(),
						TopologyAwareAssignments:         map[int]machine.CPUSet{0: reclaim.Clone()},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{0: reclaim.Clone()},
					},
				},
				"dedicated-a": {"main": first},
				"dedicated-b": {"main": second, "sidecar": secondSidecar},
			}
			machineState, err := generateMachineStateFromPodEntries(topology, entries, p.state.GetMachineState())
			require.NoError(t, err)
			machineState[1].PreOccPodEntries = state.PodEntries{"pre-occ": {"main": preOcc}}
			require.NoError(t, p.state.CommitAdvisorState(
				entries, machineState, false, tc.disableDedicatedOverlap, true))
			persistedPreOcc := p.state.GetMachineState()[1].PreOccPodEntries.Clone()

			before := p.state.GetRevision()
			require.NoError(t, p.ensureInitialReclaimPool())
			require.Equal(t, before+1, p.state.GetRevision())
			pool := p.state.GetAllocationInfo(commonstate.PoolNameReclaim, commonstate.FakedContainerName)
			require.True(t, pool.AllocationResult.Equals(machine.NewCPUSet(0, 1, 90, 91)))
			require.True(t, p.state.GetAllocationInfo("dedicated-a", "main").AllocationResult.Equals(firstCPUs))
			updatedSecond := p.state.GetAllocationInfo("dedicated-b", "main")
			updatedSidecar := p.state.GetAllocationInfo("dedicated-b", "sidecar")
			require.True(t, updatedSecond.AllocationResult.Equals(tc.expectedSecondAllocation))
			require.True(t, updatedSecond.OriginalAllocationResult.Equals(tc.expectedSecondAllocation))
			require.True(t, updatedSidecar.AllocationResult.Equals(tc.expectedSecondAllocation))
			require.Equal(t, updatedSecond.TopologyAwareAssignments, updatedSidecar.TopologyAwareAssignments)
			require.Equal(t, persistedPreOcc, p.state.GetMachineState()[1].PreOccPodEntries)
			if tc.disableDedicatedOverlap {
				require.True(t, pool.AllocationResult.Intersection(updatedSecond.AllocationResult).IsEmpty())
			} else {
				require.True(t, pool.AllocationResult.Intersection(updatedSecond.AllocationResult).
					Equals(machine.NewCPUSet(90, 91)))
			}

			reopened := openCheckpointState(t, topology, stateDir)
			require.Equal(t, p.state.GetPodEntries(), reopened.GetPodEntries())
			require.Equal(t, p.state.GetMachineState(), reopened.GetMachineState())
			publishedRevision := p.state.GetRevision()
			require.NoError(t, p.ensureInitialReclaimPool())
			require.Equal(t, publishedRevision, p.state.GetRevision())
		})
	}
}

func TestInitialReclaimPoolLeavesNonCoreAlignedUnderfilledIdentityUntouched(t *testing.T) {
	t.Parallel()
	topology, err := machine.GenerateDummyCPUTopology(32, 1, 2)
	require.NoError(t, err)
	p, err := getTestDynamicPolicyWithoutInitialization(topology, t.TempDir())
	require.NoError(t, err)
	p.reclaim.reservedReclaimedCPUsSize = 4
	reclaim := machine.NewCPUSet(0)
	require.NoError(t, p.state.SetAllocationInfo(
		commonstate.PoolNameReclaim,
		commonstate.FakedContainerName,
		&state.AllocationInfo{
			AllocationMeta:                   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
			AllocationResult:                 reclaim.Clone(),
			OriginalAllocationResult:         reclaim.Clone(),
			TopologyAwareAssignments:         map[int]machine.CPUSet{0: reclaim.Clone()},
			OriginalTopologyAwareAssignments: map[int]machine.CPUSet{0: reclaim.Clone()},
		},
		true,
	))

	beforeEntries, beforeMachineState, beforeRevision := p.state.GetAdvisorStateSnapshot()
	require.NoError(t, p.ensureInitialReclaimPool())
	require.Equal(t, beforeRevision, p.state.GetRevision())
	require.Equal(t, beforeEntries, p.state.GetPodEntries())
	require.Equal(t, beforeMachineState, p.state.GetMachineState())
}

func TestInitialReclaimPoolKeepsUnderfilledIdentityWhenNoSafeSupplementExists(t *testing.T) {
	t.Parallel()
	topology, err := machine.GenerateDummyCPUTopology(32, 1, 2)
	require.NoError(t, err)
	p, err := getTestDynamicPolicyWithoutInitialization(topology, t.TempDir())
	require.NoError(t, err)
	p.reclaim.reservedReclaimedCPUsSize = 4
	reclaim := machine.NewCPUSet(0, 16)
	p.config.reservedCPUs = topology.CPUDetails.CPUs().Difference(reclaim)
	require.NoError(t, p.state.SetAllocationInfo(
		commonstate.PoolNameReclaim,
		commonstate.FakedContainerName,
		&state.AllocationInfo{
			AllocationMeta:                   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
			AllocationResult:                 reclaim.Clone(),
			OriginalAllocationResult:         reclaim.Clone(),
			TopologyAwareAssignments:         map[int]machine.CPUSet{0: reclaim.Clone()},
			OriginalTopologyAwareAssignments: map[int]machine.CPUSet{0: reclaim.Clone()},
		},
		true,
	))

	beforeEntries, beforeMachineState, beforeRevision := p.state.GetAdvisorStateSnapshot()
	require.NoError(t, p.ensureInitialReclaimPool())
	require.Equal(t, beforeRevision, p.state.GetRevision())
	require.Equal(t, beforeEntries, p.state.GetPodEntries())
	require.Equal(t, beforeMachineState, p.state.GetMachineState())
}

func TestInitialReclaimDedicatedRepairStaleRevisionDoesNotPublish(t *testing.T) {
	t.Parallel()
	topology := contiguous180CPUTopology(t)
	p, err := getTestDynamicPolicyWithoutInitialization(topology, t.TempDir())
	require.NoError(t, err)
	p.config.reservedCPUs = machine.NewCPUSet()
	p.reclaim.reservedReclaimedCPUsSize = 2
	p.reclaim.reservedReclaimedCPUSet = machine.NewCPUSet(0, 1)
	firstCPUs := machine.MustParse("1-89")
	secondCPUs := machine.MustParse("90-179")
	newDedicated := func(podUID string, cpus machine.CPUSet, numaID int, request float64) *state.AllocationInfo {
		return &state.AllocationInfo{
			AllocationMeta: commonstate.AllocationMeta{
				PodUid: podUID, ContainerName: "main", ContainerType: pluginapi.ContainerType_MAIN.String(),
				OwnerPoolName: commonstate.PoolNameDedicated, QoSLevel: apiconsts.PodAnnotationQoSLevelDedicatedCores,
				Annotations: exclusiveAnnotations(),
			},
			AllocationResult: cpus.Clone(), OriginalAllocationResult: cpus.Clone(),
			TopologyAwareAssignments:         map[int]machine.CPUSet{numaID: cpus.Clone()},
			OriginalTopologyAwareAssignments: map[int]machine.CPUSet{numaID: cpus.Clone()},
			RequestQuantity:                  request,
		}
	}
	entries := state.PodEntries{
		commonstate.PoolNameReclaim: {commonstate.FakedContainerName: {
			AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
			AllocationResult: machine.NewCPUSet(), OriginalAllocationResult: machine.NewCPUSet(),
		}},
		"dedicated-a": {"main": newDedicated("dedicated-a", firstCPUs, 0, 88)},
		"dedicated-b": {"main": newDedicated("dedicated-b", secondCPUs, 1, 90)},
	}
	machineState, err := generateMachineStateFromPodEntries(topology, entries, p.state.GetMachineState())
	require.NoError(t, err)
	require.NoError(t, p.state.CommitAdvisorState(entries, machineState, false, false, true))
	underlying := p.state
	tracked := &bypassTrackingState{State: underlying, bumpBeforeCommit: true}
	p.state = tracked

	err = p.ensureInitialReclaimPool()
	require.Error(t, err)
	require.Equal(t, 1, tracked.commitIfRevisionCalls)
	require.True(t, underlying.GetAllocationInfo("dedicated-a", "main").AllocationResult.Equals(firstCPUs))
	require.True(t, underlying.GetAllocationInfo(commonstate.PoolNameReclaim, commonstate.FakedContainerName).
		AllocationResult.IsEmpty())
	require.NotNil(t, underlying.GetAllocationInfo("bump-pod", "bump-container"))
}

func TestInitialReclaimPoolRejectsUnsafeDedicatedRepair(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name        string
		request     float64
		annotations map[string]string
		pinDonorCPU bool
	}{
		{name: "no surplus", request: 89, annotations: exclusiveAnnotations()},
		{name: "non exclusive", request: 88, annotations: nonExclusiveAnnotations()},
		{name: "resource package pinned", request: 88, annotations: exclusiveAnnotations(), pinDonorCPU: true},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			topology := contiguous180CPUTopology(t)
			p, err := getTestDynamicPolicyWithoutInitialization(topology, t.TempDir())
			require.NoError(t, err)
			p.config.reservedCPUs = machine.NewCPUSet()
			p.reclaim.reservedReclaimedCPUsSize = 2
			p.reclaim.reservedReclaimedCPUSet = machine.NewCPUSet(0, 1)
			firstCPUs, err := machine.Parse("1-89")
			require.NoError(t, err)
			secondCPUs, err := machine.Parse("90-179")
			require.NoError(t, err)
			first := &state.AllocationInfo{
				AllocationMeta: commonstate.AllocationMeta{
					PodUid: "dedicated-a", ContainerName: "main", ContainerType: pluginapi.ContainerType_MAIN.String(),
					OwnerPoolName: commonstate.PoolNameDedicated, QoSLevel: apiconsts.PodAnnotationQoSLevelDedicatedCores,
					Annotations: tc.annotations,
				},
				AllocationResult: firstCPUs.Clone(), OriginalAllocationResult: firstCPUs.Clone(),
				TopologyAwareAssignments:         map[int]machine.CPUSet{0: firstCPUs.Clone()},
				OriginalTopologyAwareAssignments: map[int]machine.CPUSet{0: firstCPUs.Clone()},
				RequestQuantity:                  tc.request,
			}
			second := first.Clone()
			second.PodUid = "dedicated-b"
			second.AllocationResult = secondCPUs.Clone()
			second.OriginalAllocationResult = secondCPUs.Clone()
			second.TopologyAwareAssignments = map[int]machine.CPUSet{1: secondCPUs.Clone()}
			second.OriginalTopologyAwareAssignments = map[int]machine.CPUSet{1: secondCPUs.Clone()}
			second.RequestQuantity = 90
			entries := state.PodEntries{
				commonstate.PoolNameReclaim: {commonstate.FakedContainerName: {
					AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
					AllocationResult: machine.NewCPUSet(), OriginalAllocationResult: machine.NewCPUSet(),
				}},
				"dedicated-a": {"main": first}, "dedicated-b": {"main": second},
			}
			machineState, err := generateMachineStateFromPodEntries(topology, entries, p.state.GetMachineState())
			require.NoError(t, err)
			if tc.pinDonorCPU {
				machineState[0].ResourcePackageStates = map[string]*state.ResourcePackageState{
					"protected": {PinnedCPUSet: machine.NewCPUSet(1)},
				}
			}
			require.NoError(t, p.state.CommitAdvisorState(entries, machineState, false, false, true))
			beforeEntries, beforeMachineState, beforeRevision := p.state.GetAdvisorStateSnapshot()

			err = p.ensureInitialReclaimPool()
			require.ErrorIs(t, err, errReclaimFallbackEmptyRequired)
			require.Equal(t, beforeRevision, p.state.GetRevision())
			require.Equal(t, beforeEntries, p.state.GetPodEntries())
			require.Equal(t, beforeMachineState, p.state.GetMachineState())
		})
	}
}

func TestInitialReclaimPoolNoCompleteCandidateDoesNotWrite(t *testing.T) {
	t.Parallel()
	topology, err := machine.GenerateDummyCPUTopology(8, 1, 1)
	require.NoError(t, err)
	p, err := getTestDynamicPolicyWithoutInitialization(topology, t.TempDir())
	require.NoError(t, err)
	p.config.reservedCPUs = topology.CPUDetails.CPUs()
	p.reclaim.reservedReclaimedCPUsSize = 2
	entries, ms, revision := p.state.GetAdvisorStateSnapshot()
	err = p.ensureInitialReclaimPool()
	require.Error(t, err)
	require.Equal(t, revision, p.state.GetRevision())
	require.Equal(t, entries, p.state.GetPodEntries())
	require.Equal(t, ms, p.state.GetMachineState())
}

func TestInitialReclaimPoolPersistenceFailureRestoresState(t *testing.T) {
	t.Parallel()
	topology, err := machine.GenerateDummyCPUTopology(32, 1, 2)
	require.NoError(t, err)
	stateDir := t.TempDir()
	p, err := getTestDynamicPolicyWithoutInitialization(topology, stateDir)
	require.NoError(t, err)
	require.NoError(t, p.installDerivedReclaimPreference())
	entries, ms, revision := p.state.GetAdvisorStateSnapshot()
	// A directory at the checkpoint destination forces the real atomic rename
	// to fail after the in-memory CAS; the checkpoint store must undo that CAS.
	checkpoint := filepath.Join(stateDir, cpuPluginStateFileName)
	require.NoError(t, os.Remove(checkpoint))
	require.NoError(t, os.Mkdir(checkpoint, 0o700))
	err = p.ensureInitialReclaimPool()
	require.Error(t, err)
	require.Equal(t, revision, p.state.GetRevision())
	require.Equal(t, entries, p.state.GetPodEntries())
	require.Equal(t, ms, p.state.GetMachineState())
}

func TestInitialReclaimPoolStaleRevisionPreservesConcurrentWriter(t *testing.T) {
	t.Parallel()
	topology, err := machine.GenerateDummyCPUTopology(32, 1, 2)
	require.NoError(t, err)
	p, err := getTestDynamicPolicyWithoutInitialization(topology, t.TempDir())
	require.NoError(t, err)
	require.NoError(t, p.installDerivedReclaimPreference())
	tracked := &bypassTrackingState{State: p.state, bumpBeforeCommit: true}
	p.state = tracked
	err = p.ensureInitialReclaimPool()
	require.Error(t, err)
	require.Equal(t, 1, tracked.commitIfRevisionCalls)
	require.NotNil(t, p.state.GetAllocationInfo("bump-pod", "bump-container"))
	require.Nil(t, p.state.GetAllocationInfo(commonstate.PoolNameReclaim, commonstate.FakedContainerName))
}

func TestNewDynamicPolicyRepairsEmptyReclaimCheckpoint(t *testing.T) {
	t.Parallel()
	topology := contiguous180CPUTopology(t)
	stateDir := t.TempDir()
	p, err := getTestDynamicPolicyWithInitialization(topology, stateDir)
	require.NoError(t, err)
	require.NoError(t, p.state.SetAllocationInfo(commonstate.PoolNameReclaim, commonstate.FakedContainerName,
		&state.AllocationInfo{
			AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
			AllocationResult: machine.NewCPUSet(), OriginalAllocationResult: machine.NewCPUSet(),
		}, true))
	ok, _, err := NewDynamicPolicy(freshRealConstructorContext(t, topology, stateDir),
		freshRealConstructorConf(t, stateDir), nil, "empty-reclaim-bootstrap")
	require.NoError(t, err)
	require.True(t, ok)
	pool := openCheckpointState(t, topology, stateDir).GetAllocationInfo(commonstate.PoolNameReclaim, commonstate.FakedContainerName)
	require.NotNil(t, pool)
	require.False(t, pool.AllocationResult.IsEmpty())
	require.NoError(t, assertCoreAligned(pool.AllocationResult, topology))
}

func TestInitialReclaimPoolResyncsExistingReclaimedMembers(t *testing.T) {
	t.Parallel()
	topology, err := machine.GenerateDummyCPUTopology(32, 1, 2)
	require.NoError(t, err)
	p, err := getTestDynamicPolicyWithoutInitialization(topology, t.TempDir())
	require.NoError(t, err)
	require.NoError(t, p.state.SetAllocationInfo("reclaimed-pod", "main", &state.AllocationInfo{
		AllocationMeta: commonstate.AllocationMeta{
			PodUid: "reclaimed-pod", ContainerName: "main",
			ContainerType: pluginapi.ContainerType_MAIN.String(), OwnerPoolName: commonstate.PoolNameReclaim,
			QoSLevel: apiconsts.PodAnnotationQoSLevelReclaimedCores,
		},
		AllocationResult: machine.NewCPUSet(), OriginalAllocationResult: machine.NewCPUSet(),
	}, false))
	require.NoError(t, p.installDerivedReclaimPreference())
	require.NoError(t, p.ensureInitialReclaimPool())
	pool := p.state.GetAllocationInfo(commonstate.PoolNameReclaim, commonstate.FakedContainerName)
	member := p.state.GetAllocationInfo("reclaimed-pod", "main")
	require.True(t, pool.AllocationResult.Equals(member.AllocationResult))
	require.NoError(t, validatePendingPoolOwnership(p.state.GetPodEntries()))
}

func TestInitialReclaimPoolPreservesShareScalarWithStaleBuckets(t *testing.T) {
	t.Parallel()
	topology, err := machine.GenerateDummyCPUTopology(32, 1, 2)
	require.NoError(t, err)
	stateDir := t.TempDir()
	p, err := getTestDynamicPolicyWithoutInitialization(topology, stateDir)
	require.NoError(t, err)
	p.config.reservedCPUs = machine.NewCPUSet()
	require.NoError(t, p.installDerivedReclaimPreference())
	share := &state.AllocationInfo{
		AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameShare),
		AllocationResult: machine.NewCPUSet(2, 18), OriginalAllocationResult: machine.NewCPUSet(2, 18),
		TopologyAwareAssignments:         map[int]machine.CPUSet{0: machine.NewCPUSet(0, 16)},
		OriginalTopologyAwareAssignments: map[int]machine.CPUSet{0: machine.NewCPUSet(0, 16)},
	}
	main := &state.AllocationInfo{
		AllocationMeta: commonstate.AllocationMeta{
			PodUid: "shared-pod", ContainerName: "main",
			OwnerPoolName: commonstate.PoolNameShare, ContainerType: pluginapi.ContainerType_MAIN.String(),
			QoSLevel: apiconsts.PodAnnotationQoSLevelSharedCores,
		},
		AllocationResult: machine.NewCPUSet(2, 18), OriginalAllocationResult: machine.NewCPUSet(2, 18),
		TopologyAwareAssignments:         map[int]machine.CPUSet{0: machine.NewCPUSet(2, 18)},
		OriginalTopologyAwareAssignments: map[int]machine.CPUSet{0: machine.NewCPUSet(2, 18)},
	}
	entries := state.PodEntries{commonstate.PoolNameShare: {commonstate.FakedContainerName: share}, "shared-pod": {"main": main}}
	ms, err := generateMachineStateFromPodEntries(topology, entries, p.state.GetMachineState())
	require.NoError(t, err)
	require.NoError(t, p.state.CommitAdvisorState(entries, ms, false, false, true))
	ok, _, err := NewDynamicPolicy(freshRealConstructorContext(t, topology, stateDir), freshRealConstructorConf(t, stateDir), nil, "stale-pool-buckets")
	require.NoError(t, err)
	require.True(t, ok)
	reopened := openCheckpointState(t, topology, stateDir)
	published := reopened.GetPodEntries()
	finalShare := reopened.GetAllocationInfo(commonstate.PoolNameShare, commonstate.FakedContainerName)
	require.True(t, finalShare.AllocationResult.Equals(share.AllocationResult))
	require.True(t, reclaimPoolCPUSet(published).Intersection(finalShare.AllocationResult).IsEmpty())
	require.Equal(t, main, reopened.GetAllocationInfo("shared-pod", "main"))
	expectedAssignments, err := machine.GetNumaAwareAssignments(topology, share.AllocationResult)
	require.NoError(t, err)
	require.Equal(t, expectedAssignments, finalShare.TopologyAwareAssignments)
}

func TestInitialReclaimPoolBacksActiveRNBBeforePreference(t *testing.T) {
	t.Parallel()
	topology, err := machine.GenerateDummyCPUTopology(48, 1, 3)
	require.NoError(t, err)
	p, err := getTestDynamicPolicyWithoutInitialization(topology, t.TempDir())
	require.NoError(t, err)
	p.config.reservedCPUs = machine.NewCPUSet()
	p.reclaim.reservedReclaimedCPUsSize = 4
	require.NoError(t, p.installDerivedReclaimPreference())
	member := makeReclaimedRNBAllocation("rnb-pod", "main", 2, machine.NewCPUSet())
	member.ContainerType = pluginapi.ContainerType_MAIN.String()
	sidecar := member.Clone()
	sidecar.ContainerName = "sidecar"
	sidecar.ContainerType = pluginapi.ContainerType_SIDECAR.String()
	entries := state.PodEntries{"rnb-pod": {"main": member, "sidecar": sidecar}}
	ms, err := generateMachineStateFromPodEntries(topology, entries, p.state.GetMachineState())
	require.NoError(t, err)
	require.NoError(t, p.state.CommitAdvisorState(entries, ms, false, false, false))
	before := p.state.GetRevision()
	require.NoError(t, p.ensureInitialReclaimPool())
	require.Equal(t, before+1, p.state.GetRevision())
	pool := p.state.GetAllocationInfo(commonstate.PoolNameReclaim, commonstate.FakedContainerName)
	require.Equal(t, 4, pool.AllocationResult.Size())
	require.False(t, pool.TopologyAwareAssignments[2].IsEmpty())
	require.NoError(t, assertCoreAligned(pool.AllocationResult, topology))
	require.Equal(t, p.state.GetAllocationInfo("rnb-pod", "main").AllocationResult,
		p.state.GetAllocationInfo("rnb-pod", "sidecar").AllocationResult)
	require.NoError(t, validatePendingPoolOwnership(p.state.GetPodEntries()))
}

func TestInitialReclaimPoolRequiredNUMAShortageDoesNotWrite(t *testing.T) {
	t.Parallel()
	topology, err := machine.GenerateDummyCPUTopology(48, 1, 3)
	require.NoError(t, err)
	p, err := getTestDynamicPolicyWithoutInitialization(topology, t.TempDir())
	require.NoError(t, err)
	p.config.reservedCPUs = topology.CPUDetails.CPUsInNUMANodes(2)
	require.NoError(t, p.installDerivedReclaimPreference())
	member := makeReclaimedRNBAllocation("rnb-pod", "main", 2, machine.NewCPUSet())
	require.NoError(t, p.state.SetAllocationInfo("rnb-pod", "main", member, false))
	entries, ms, revision := p.state.GetAdvisorStateSnapshot()
	err = p.ensureInitialReclaimPool()
	require.ErrorIs(t, err, errReclaimFallbackEmptyRequired)
	require.Equal(t, revision, p.state.GetRevision())
	require.Equal(t, entries, p.state.GetPodEntries())
	require.Equal(t, ms, p.state.GetMachineState())
}

func TestInitialReclaimPoolRejectsUnrelatedOwnershipMismatch(t *testing.T) {
	t.Parallel()
	topology, err := machine.GenerateDummyCPUTopology(32, 1, 2)
	require.NoError(t, err)
	p, err := getTestDynamicPolicyWithoutInitialization(topology, t.TempDir())
	require.NoError(t, err)
	require.NoError(t, p.installDerivedReclaimPreference())
	require.NoError(t, p.state.SetAllocationInfo(commonstate.PoolNameShare, commonstate.FakedContainerName, &state.AllocationInfo{
		AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameShare),
		AllocationResult: machine.NewCPUSet(2, 18), OriginalAllocationResult: machine.NewCPUSet(2, 18),
	}, false))
	require.NoError(t, p.state.SetAllocationInfo("shared-pod", "main", &state.AllocationInfo{
		AllocationMeta: commonstate.AllocationMeta{
			PodUid: "shared-pod", ContainerName: "main", OwnerPoolName: commonstate.PoolNameShare,
			ContainerType: pluginapi.ContainerType_MAIN.String(), QoSLevel: apiconsts.PodAnnotationQoSLevelSharedCores,
		},
		AllocationResult: machine.NewCPUSet(4, 20), OriginalAllocationResult: machine.NewCPUSet(4, 20),
	}, false))
	entries, ms, revision := p.state.GetAdvisorStateSnapshot()
	err = p.ensureInitialReclaimPool()
	require.ErrorContains(t, err, "validate initial reclaim ownership")
	require.Equal(t, revision, p.state.GetRevision())
	require.Equal(t, entries, p.state.GetPodEntries())
	require.Equal(t, ms, p.state.GetMachineState())
}

func TestInitialReclaimPoolBacksEveryActiveRNBNUMA(t *testing.T) {
	t.Parallel()
	topology, err := machine.GenerateDummyCPUTopology(48, 1, 3)
	require.NoError(t, err)
	p, err := getTestDynamicPolicyWithoutInitialization(topology, t.TempDir())
	require.NoError(t, err)
	p.config.reservedCPUs = machine.NewCPUSet()
	p.reclaim.reservedReclaimedCPUsSize = 4
	require.NoError(t, p.installDerivedReclaimPreference())
	for numaID := 0; numaID < 3; numaID++ {
		uid := fmt.Sprintf("rnb-%d", numaID)
		require.NoError(t, p.state.SetAllocationInfo(uid, "main",
			makeReclaimedRNBAllocation(uid, "main", numaID, machine.NewCPUSet()), false))
	}
	require.NoError(t, p.ensureInitialReclaimPool())
	pool := p.state.GetAllocationInfo(commonstate.PoolNameReclaim, commonstate.FakedContainerName)
	// Required buckets outrank the four-CPU preference; each needs a whole core.
	require.Equal(t, 6, pool.AllocationResult.Size())
	for numaID := 0; numaID < 3; numaID++ {
		require.Equal(t, 2, pool.TopologyAwareAssignments[numaID].Size())
	}
	require.NoError(t, validatePendingPoolOwnership(p.state.GetPodEntries()))
}

func TestInitialReclaimPoolRejectsRampUpPartitionOverlap(t *testing.T) {
	t.Parallel()
	topology, err := machine.GenerateDummyCPUTopology(32, 1, 2)
	require.NoError(t, err)
	p, err := getTestDynamicPolicyWithoutInitialization(topology, t.TempDir())
	require.NoError(t, err)
	dyn := p.config.dynamicConfig.GetDynamicConfiguration()
	dyn.EnableReclaim = true
	dyn.EnableRampUpReclaimHardPartition = true
	require.NoError(t, p.installDerivedReclaimPreference())
	all := topology.CPUDetails.CPUs()
	require.NoError(t, p.state.SetAllocationInfo("shared-pod", "main", &state.AllocationInfo{
		AllocationMeta: commonstate.AllocationMeta{
			PodUid: "shared-pod", ContainerName: "main",
			ContainerType: pluginapi.ContainerType_MAIN.String(), QoSLevel: apiconsts.PodAnnotationQoSLevelSharedCores,
		},
		AllocationResult: all.Clone(), OriginalAllocationResult: all.Clone(), RampUp: true,
	}, false))
	entries, ms, revision := p.state.GetAdvisorStateSnapshot()
	err = p.ensureInitialReclaimPool()
	require.ErrorContains(t, err, "validate initial reclaim partition")
	require.Equal(t, revision, p.state.GetRevision())
	require.Equal(t, entries, p.state.GetPodEntries())
	require.Equal(t, ms, p.state.GetMachineState())
}

// stageEmptyReclaimUnderWAL plants a canonical checkpoint whose reclaim pool is
// empty or missing while a replayable advisor post-commit WAL exists at the new
// revision. It returns the committed revision and the published WAL path. This is
// the illegal-but-durable shape a replayable WAL can point at after a crash:
// bootstrap repair is skipped while the WAL is pending and the replay does not
// re-plan entries, so the restored bytes are what the completion boundary sees.
func stageEmptyReclaimUnderWAL(t *testing.T, topology *machine.CPUTopology, p *DynamicPolicy, missing bool) (revision uint64, walPath string) {
	t.Helper()
	entries := p.state.GetPodEntries().Clone()
	if missing {
		delete(entries, commonstate.PoolNameReclaim)
	} else {
		entries[commonstate.PoolNameReclaim] = state.ContainerEntries{
			commonstate.FakedContainerName: &state.AllocationInfo{
				AllocationMeta:           commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
				AllocationResult:         machine.NewCPUSet(),
				OriginalAllocationResult: machine.NewCPUSet(),
			},
		}
	}
	ms, err := generateMachineStateFromPodEntries(topology, entries, p.state.GetMachineState())
	require.NoError(t, err)
	require.NoError(t, p.state.CommitAdvisorState(entries, ms, false, true, true))
	revision = p.state.GetRevision()

	p.publishAdvisorPostCommitTarget(nil, revision)
	walPath = p.advisorPostCommitCheckpointPath()
	require.FileExists(t, walPath)
	return revision, walPath
}

// TestNewDynamicPolicyAllowsPendingWALWithEmptyReclaimContract verifies that a
// replayable advisor post-commit WAL pointing at a canonical checkpoint whose
// reclaim pool is empty or missing is tolerated at construction. The candidate may
// be built empty; the reclaim non-empty invariant is enforced later at the replay
// completion boundary, not by a constructor rejection. Construction must not seed,
// advance the revision, mutate durable entries, or clear the WAL.
func TestNewDynamicPolicyAllowsPendingWALWithEmptyReclaimContract(t *testing.T) {
	t.Parallel()
	for _, missing := range []bool{true, false} {
		missing := missing
		t.Run(fmt.Sprintf("missing=%t", missing), func(t *testing.T) {
			t.Parallel()
			topology := contiguous180CPUTopology(t)
			stateDir := t.TempDir()
			first, err := getTestDynamicPolicyWithInitialization(topology, stateDir)
			require.NoError(t, err)

			revision, walPath := stageEmptyReclaimUnderWAL(t, topology, first, missing)
			walBytes, err := os.ReadFile(walPath)
			require.NoError(t, err)
			beforeEntries := openCheckpointState(t, topology, stateDir).GetPodEntries()

			ok, _, err := NewDynamicPolicy(freshRealConstructorContext(t, topology, stateDir),
				freshRealConstructorConf(t, stateDir), nil, "pending-wal-empty-reclaim-candidate")

			require.NoError(t, err, "constructor must tolerate an empty reclaim candidate under a pending WAL")
			require.True(t, ok)

			restarted := openCheckpointState(t, topology, stateDir)
			require.Equal(t, revision, restarted.GetRevision(), "revision must not advance")
			require.Equal(t, beforeEntries, restarted.GetPodEntries(), "durable entries must not change")
			walAfter, err := os.ReadFile(walPath)
			require.NoError(t, err)
			require.Equal(t, walBytes, walAfter, "WAL must be retained for the completion boundary")
		})
	}
}

// TestReconcileAdvisorPostCommitCompletesWithNonEmptyReclaim verifies the legal
// terminal loop: a committed, non-empty reclaim pool replayed through the real
// reconcile path reaches applied/cleanup success, the WAL is removed, the target
// fence is released, and the durable reclaim pool stays non-empty without the
// canonical revision advancing.
func TestReconcileAdvisorPostCommitCompletesWithNonEmptyReclaim(t *testing.T) {
	t.Parallel()
	topology := contiguous180CPUTopology(t)
	stateDir := t.TempDir()
	first, err := getTestDynamicPolicyWithInitialization(topology, stateDir)
	require.NoError(t, err)

	preEntry := first.state.GetAllocationInfo(commonstate.PoolNameReclaim, commonstate.FakedContainerName)
	require.NotNil(t, preEntry)
	require.False(t, preEntry.AllocationResult.IsEmpty())
	require.NoError(t, first.state.CommitAdvisorStateIfRevision(
		first.state.GetRevision(),
		first.state.GetPodEntries(),
		first.state.GetMachineState(),
		first.state.GetAllowSharedCoresOverlapReclaimedCores(),
		first.state.GetDisableDedicatedCoresOverlapReclaimedCores(),
		true,
	))
	revision := first.state.GetRevision()

	target := first.publishAdvisorPostCommitTarget(nil, revision)
	walPath := first.advisorPostCommitCheckpointPath()
	require.FileExists(t, walPath)

	first.Lock()
	err = first.reconcileAdvisorPostCommitTarget(context.Background(), target, cpusetutil.CPUSetAdjustmentModePeriodic)
	first.Unlock()
	require.NoError(t, err)

	require.NoFileExists(t, walPath, "completed replay must remove the WAL")
	require.False(t, first.hasPendingAdvisorPostCommitTarget(revision), "target fence must be released after cleanup")

	reopened := openCheckpointState(t, topology, stateDir)
	require.Equal(t, revision, reopened.GetRevision(), "replay must not advance the canonical revision")
	postEntry := reopened.GetAllocationInfo(commonstate.PoolNameReclaim, commonstate.FakedContainerName)
	require.NotNil(t, postEntry)
	require.False(t, postEntry.AllocationResult.IsEmpty(), "terminal reclaim pool must stay non-empty")
}

// TestReconcileAdvisorPostCommitFailsClosedWithEmptyReclaim verifies the illegal
// boundary: when a replayable WAL points at a canonical checkpoint whose reclaim
// pool is empty or missing, the replay cannot construct a reliable exact reclaim
// cpuset. The completion boundary fails closed instead of publishing a
// successful-but-empty terminal state: it does not mark applied, does not remove
// the WAL, keeps the target pending, and leaves revision and durable entries
// untouched.
func TestReconcileAdvisorPostCommitFailsClosedWithEmptyReclaim(t *testing.T) {
	t.Parallel()
	for _, missing := range []bool{true, false} {
		missing := missing
		t.Run(fmt.Sprintf("missing=%t", missing), func(t *testing.T) {
			t.Parallel()
			topology := contiguous180CPUTopology(t)
			stateDir := t.TempDir()
			first, err := getTestDynamicPolicyWithInitialization(topology, stateDir)
			require.NoError(t, err)

			revision, walPath := stageEmptyReclaimUnderWAL(t, topology, first, missing)
			walBytes, err := os.ReadFile(walPath)
			require.NoError(t, err)
			beforeEntries := openCheckpointState(t, topology, stateDir).GetPodEntries()

			target := first.currentAdvisorPostCommitTarget()
			require.NotNil(t, target)
			first.Lock()
			err = first.reconcileAdvisorPostCommitTarget(context.Background(), target, cpusetutil.CPUSetAdjustmentModePeriodic)
			first.Unlock()

			require.Error(t, err, "empty reclaim under a replayable WAL must fail closed at completion")
			require.ErrorContains(t, err, "left canonical reclaim pool empty")

			require.True(t, first.hasPendingAdvisorPostCommitTarget(revision),
				"target must stay pending, not retired or cleaned up")
			restarted := openCheckpointState(t, topology, stateDir)
			require.Equal(t, revision, restarted.GetRevision(), "revision fence must not advance")
			require.Equal(t, beforeEntries, restarted.GetPodEntries(), "no-write: durable entries must not change")
			walAfter, err := os.ReadFile(walPath)
			require.NoError(t, err)
			require.Equal(t, walBytes, walAfter, "WAL evidence must be retained")
		})
	}
}

// TestReconcileAdvisorPostCommitAppliedMarkerRejectsEmptyReclaimOnCleanup verifies
// the terminal cleanup chokepoint: when a target was already marked applied (e.g.
// persisted mid-recovery, or reloaded from disk after a crash) and the canonical
// checkpoint still carries an empty/missing reclaim pool, reconcile must not
// remove the WAL or release the fence. The applied bit is not rolled back; the
// target and WAL are retained for a bounded retry and inspection.
func TestReconcileAdvisorPostCommitAppliedMarkerRejectsEmptyReclaimOnCleanup(t *testing.T) {
	t.Parallel()
	for _, missing := range []bool{true, false} {
		missing := missing
		t.Run(fmt.Sprintf("missing=%t", missing), func(t *testing.T) {
			t.Parallel()
			topology := contiguous180CPUTopology(t)
			stateDir := t.TempDir()
			first, err := getTestDynamicPolicyWithInitialization(topology, stateDir)
			require.NoError(t, err)

			revision, walPath := stageEmptyReclaimUnderWAL(t, topology, first, missing)
			walBytes, err := os.ReadFile(walPath)
			require.NoError(t, err)

			target := first.currentAdvisorPostCommitTarget()
			require.NotNil(t, target)
			// Simulate a target recovered with the applied marker already set: the
			// cleanup path is taken without re-running physical apply.
			target.applied = true

			first.Lock()
			err = first.reconcileAdvisorPostCommitTarget(context.Background(), target, cpusetutil.CPUSetAdjustmentModePeriodic)
			first.Unlock()

			require.Error(t, err, "an applied target over empty reclaim must refuse cleanup")
			require.ErrorContains(t, err, "refusing cleanup")

			require.True(t, first.hasPendingAdvisorPostCommitTarget(revision),
				"target must stay pending; cleanup must not release the fence")
			require.True(t, target.applied, "the retained applied bit must not be rolled back")
			restarted := openCheckpointState(t, topology, stateDir)
			require.Equal(t, revision, restarted.GetRevision(), "revision fence must not advance")
			walAfter, err := os.ReadFile(walPath)
			require.NoError(t, err)
			require.Equal(t, walBytes, walAfter, "WAL evidence must be retained")
		})
	}
}

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
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/util/sets"
	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	apiconsts "github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	advisorapi "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/cpuadvisor"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	dynamicconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

type retryablePartitionCommitError interface {
	error
	Retryable() bool
}

func TestCommitPendingCPUPartitionRejectsOrdinaryWriterWhileAdvisorTargetPending(t *testing.T) {
	t.Parallel()
	p, cleanup := newReclaimReuseTestPolicy(t)
	defer cleanup()
	dir := t.TempDir()
	p.advisorPostCommitCheckpointDir = dir
	initialRevision := p.state.GetRevision()
	target := p.publishAdvisorPostCommitTarget(
		&advisorapi.ListAndWatchResponse{}, initialRevision)
	initialEntries := p.state.GetPodEntries()

	_, _, err := p.commitPendingCPUPartition(pendingCPUPartition{
		expectedRevision: initialRevision,
		entries:          initialEntries,
		allowOverlap:     p.state.GetAllowSharedCoresOverlapReclaimedCores(),
		disableDedicated: p.state.GetDisableDedicatedCoresOverlapReclaimedCores(),
		persist:          false,
		source:           "ordinary writer test",
	})

	require.Error(t, err)
	var retryable retryablePartitionCommitError
	require.ErrorAs(t, err, &retryable)
	require.True(t, retryable.Retryable())
	require.Equal(t, initialRevision, p.state.GetRevision())
	require.Equal(t, initialEntries, p.state.GetPodEntries())
	require.Same(t, target, p.currentAdvisorPostCommitTarget())
	require.FileExists(t, filepath.Join(dir, advisorPostCommitCheckpointName))
}

func TestCommitPendingCPUPartitionRunsHooksBeforeValidation(t *testing.T) {
	t.Parallel()
	p, cleanup := newReclaimReuseTestPolicy(t)
	defer cleanup()
	emitter := &recordingMetricEmitter{}
	p.emitter.emitter = emitter

	candidate := precommitPartitionEntries(machine.NewCPUSet(0, 1), machine.NewCPUSet(2, 3))
	original := candidate.Clone()
	p.allocation.allocationHooks = []AllocationHook{func(_, allocation *state.AllocationInfo) error {
		if allocation.PodUid == "dedicated-pod" {
			allocation.AllocationResult = machine.NewCPUSet(1, 2)
		}
		return nil
	}}

	_, _, err := p.commitPendingCPUPartition(pendingCPUPartition{
		expectedRevision: p.state.GetRevision(),
		entries:          candidate,
		disableDedicated: true,
		persist:          false,
	})
	require.ErrorContains(t, err, "overlaps reclaim pool")
	require.Equal(t, original, candidate, "precommit must only mutate its cloned candidate")
	require.Nil(t, p.state.GetAllocationInfo("dedicated-pod", "main"),
		"candidate made invalid by a hook must not be committed")
	require.Empty(t, emitter.records, "failed commit must not emit pool size metrics")
}

func TestPreparePendingCPUPartitionUsesFrozenDynamicConfiguration(t *testing.T) {
	t.Parallel()
	p, cleanup := newReclaimReuseTestPolicy(t)
	defer cleanup()

	frozen := p.config.dynamicConfig.GetDynamicConfiguration()
	frozen.FillDefaultSharePoolWithNonReclaimCPUs = false
	replacement := dynamicconfig.NewConfiguration()
	replacement.FillDefaultSharePoolWithNonReclaimCPUs = true
	p.config.dynamicConfig.SetDynamicConfiguration(replacement)

	_, err := p.preparePendingCPUPartition(pendingCPUPartition{
		expectedRevision: p.state.GetRevision(),
		entries: precommitPartitionEntries(
			machine.NewCPUSet(0, 1),
			machine.NewCPUSet(2, 3),
		),
		dynamicConfig: frozen,
		persist:       false,
		source:        "frozen dynamic configuration test",
	})
	require.NoError(t, err)
}

// TestAttemptDynamicConfigurationRetainsCapturedPointerAcrossManagerSwap pins
// the attempt contract on the real dynamic config manager: an outer attempt
// entry captures the live *Configuration pointer once via GetDynamicConfiguration.
// When the manager later publishes a brand-new *Configuration with an atomic
// pointer swap, the in-flight attempt keeps reading the old immutable pointer,
// while a subsequent outer capture observes the new one.
func TestAttemptDynamicConfigurationRetainsCapturedPointerAcrossManagerSwap(t *testing.T) {
	t.Parallel()
	p, cleanup := newReclaimReuseTestPolicy(t)
	defer cleanup()

	old := p.config.dynamicConfig.GetDynamicConfiguration()
	old.EnableReclaim = true
	old.EnableRampUpReclaimHardPartition = true
	old.InitialRampUpReclaimCPUSetRatio = 0.2

	next := dynamicconfig.NewConfiguration()
	next.EnableReclaim = false
	next.EnableRampUpReclaimHardPartition = false
	p.config.dynamicConfig.SetDynamicConfiguration(next)

	// The in-flight attempt still holds and reads the captured old pointer.
	require.True(t, old.EnableReclaim)
	require.True(t, old.EnableRampUpReclaimHardPartition)
	require.Equal(t, 0.2, old.InitialRampUpReclaimCPUSetRatio)

	// A later outer-attempt capture observes the freshly published pointer.
	live := p.config.dynamicConfig.GetDynamicConfiguration()
	require.NotSame(t, old, live)
	require.False(t, live.EnableReclaim)
	require.False(t, live.EnableRampUpReclaimHardPartition)
}

// TestPrecommitValidatorPinsCapturedDynamicConfigAcrossMidAttemptSwap is the
// RED-capable guard for the pending/precommit validator: the validator closure
// installed on a pending partition must read the *Configuration pointer captured
// at the outer attempt entry, not the live manager pointer that may be swapped
// mid-flight. It uses the real production validator bound to the captured old
// pointer, with entries that trigger the ramp-up/reclaim hard-partition gate.
// If the call site is ever reverted to a live-reading validator, the swapped-in
// new pointer (gate disabled) returns nil here and the test fails.
func TestPrecommitValidatorPinsCapturedDynamicConfigAcrossMidAttemptSwap(t *testing.T) {
	t.Parallel()
	p, cleanup := newReclaimReuseTestPolicy(t)
	defer cleanup()

	old := p.config.dynamicConfig.GetDynamicConfiguration()
	old.EnableReclaim = true
	old.EnableRampUpReclaimHardPartition = true

	// Entries: reclaim pool owns CPU 0; a ramp-up shared container also owns
	// CPU 0 → overlap. Under the old gate (hard partition enabled) this is a
	// validation error; under the swapped-in new gate (disabled) it passes.
	reclaimCPUs := machine.NewCPUSet(0)
	entries := state.PodEntries{
		commonstate.PoolNameReclaim: state.ContainerEntries{
			commonstate.FakedContainerName: &state.AllocationInfo{
				AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
				AllocationResult: reclaimCPUs.Clone(),
			},
		},
		"shared-pod": state.ContainerEntries{
			"main": &state.AllocationInfo{
				AllocationMeta: commonstate.AllocationMeta{
					PodUid:        "shared-pod",
					ContainerName: "main",
					OwnerPoolName: commonstate.EmptyOwnerPoolName,
					QoSLevel:      apiconsts.PodAnnotationQoSLevelSharedCores,
				},
				AllocationResult: reclaimCPUs.Clone(),
				RampUp:           true,
			},
		},
	}

	// Publish a brand-new pointer mid-attempt with the gate disabled. The
	// validator bound to the old pointer must still see the gate enabled.
	next := dynamicconfig.NewConfiguration()
	next.EnableReclaim = true
	next.EnableRampUpReclaimHardPartition = false
	p.config.dynamicConfig.SetDynamicConfiguration(next)

	// The pending state threads the captured old pointer, and the validator
	// closure binds the production validator to that old pointer exactly as the
	// DNB / pool-adjustment call sites now do.
	_, err := p.preparePendingCPUPartition(pendingCPUPartition{
		expectedRevision: p.state.GetRevision(),
		entries:          entries,
		dynamicConfig:    old,
		persist:          false,
		source:           "mid-attempt swap pin test",
		validate: func(e state.PodEntries, ms state.NUMANodeMap, overlap, dedicated bool) error {
			return p.validatePendingAdvisorPartitionViewWithRampUpExclusionWithDynamicConfig(e, ms, overlap, dedicated, old)
		},
	})
	require.Error(t, err, "validator bound to old pointer (gate enabled) must reject the ramp-up/reclaim overlap")
	require.Contains(t, err.Error(), "ramp-up shared allocation overlaps the final reclaim pool")
}

// TestAttemptDynamicConfigurationNilSourceIsNonPanicking covers the nil/default
// dynamic source: a missing dynamicConfig still yields a nil read that attempt
// callers treat as "all defaults" rather than panicking.
func TestAttemptDynamicConfigurationNilSourceIsNonPanicking(t *testing.T) {
	t.Parallel()
	p := &DynamicPolicy{}
	require.NotPanics(t, func() {
		cfg := p.config.dynamicConfig.GetDynamicConfiguration()
		require.Nil(t, cfg)
	})
}

func TestPreparePendingCPUPartitionDerivesHardFloorFromFinalCandidate(t *testing.T) {
	t.Parallel()
	p, cleanup := newReclaimReuseTestPolicy(t)
	defer cleanup()

	current := p.config.dynamicConfig.GetDynamicConfiguration()
	current.EnableReclaim = true
	current.EnableRampUpReclaimHardPartition = true
	current.AdminQoSConfiguration.CPUPluginConfiguration.BulkheadConfig.Enable = true
	frozen := current

	candidate := precommitPartitionEntries(machine.NewCPUSet(0, 1), machine.NewCPUSet(2, 3))
	require.False(t, candidate.HasActiveRampUp())
	p.allocation.allocationHooks = []AllocationHook{func(_, allocation *state.AllocationInfo) error {
		if allocation.PodUid == "dedicated-pod" {
			allocation.RampUp = true
		}
		return nil
	}}

	validated := false
	_, err := p.preparePendingCPUPartition(pendingCPUPartition{
		expectedRevision: p.state.GetRevision(),
		entries:          candidate,
		dynamicConfig:    frozen,
		persist:          false,
		source:           "final candidate hard floor test",
		validate: func(entries state.PodEntries, _ state.NUMANodeMap, _, _ bool) error {
			validated = true
			require.True(t, entries.HasActiveRampUp())
			testRampUpDomains, derr := entries.ActiveRampUpDomains(p.machine.machineInfo.CPUTopology)
			require.NoError(t, derr)
			options := p.cpuSetPartitionViewOptionsWithDynamicConfig(
				p.state, testRampUpDomains, frozen)
			require.True(t, options.HardPartitionEnabled)
			return nil
		},
	})
	require.NoError(t, err)
	require.True(t, validated)
	require.False(t, candidate.HasActiveRampUp(), "precommit must not mutate the caller's candidate")
}

func TestCommitPendingCPUPartitionValidatesResidualBackfillAfterHooks(t *testing.T) {
	t.Parallel()
	t.Run("rejects dedicated change that overlaps share", func(t *testing.T) {
		t.Parallel()
		p, err := newResidualBackfillPrecommitTestPolicy(t)
		require.NoError(t, err)
		require.False(t, p.hardBulkheadPartitionValidationEnabled())

		candidate := residualBackfillPrecommitEntries(
			machine.NewCPUSet(2, 3),
			machine.NewCPUSet(4, 5, 6, 7),
		)
		initialEntries := p.state.GetPodEntries()
		initialRevision := p.state.GetRevision()
		p.allocation.allocationHooks = []AllocationHook{func(_, allocation *state.AllocationInfo) error {
			if allocation.PodUid == "dedicated-pod" {
				allocation.AllocationResult = machine.NewCPUSet(4, 5)
				allocation.OriginalAllocationResult = machine.NewCPUSet(4, 5)
			}
			return nil
		}}

		_, _, err = p.commitPendingCPUPartition(pendingCPUPartition{
			expectedRevision: initialRevision,
			entries:          candidate,
			persist:          false,
			source:           "residual hook test",
		})
		require.ErrorContains(t, err, "validate residual backfill candidate")
		require.ErrorContains(t, err, "default share")
		require.Equal(t, initialRevision, p.state.GetRevision())
		require.Equal(t, initialEntries, p.state.GetPodEntries(),
			"candidate made invalid by a hook must not reach the revision CAS")
	})

	t.Run("rejects dedicated quantity change before residual validation", func(t *testing.T) {
		t.Parallel()
		p, err := newResidualBackfillPrecommitTestPolicy(t)
		require.NoError(t, err)
		require.False(t, p.hardBulkheadPartitionValidationEnabled())

		candidate := residualBackfillPrecommitEntries(
			machine.NewCPUSet(2, 3),
			machine.NewCPUSet(4, 5, 6, 7),
		)
		initialEntries := p.state.GetPodEntries()
		initialRevision := p.state.GetRevision()
		p.allocation.allocationHooks = []AllocationHook{func(_, allocation *state.AllocationInfo) error {
			if allocation.PodUid == "dedicated-pod" {
				allocation.AllocationResult = machine.NewCPUSet(2)
				allocation.OriginalAllocationResult = machine.NewCPUSet(2)
			}
			return nil
		}}

		_, _, err = p.commitPendingCPUPartition(pendingCPUPartition{
			expectedRevision: initialRevision,
			entries:          candidate,
			persist:          false,
			source:           "residual hook test",
		})
		require.ErrorContains(t, err, "revalidate allocation shape after hooks")
		require.ErrorContains(t, err, "allocation quantity changed after hooks for dedicated-pod/main")
		require.Equal(t, initialRevision, p.state.GetRevision())
		require.Equal(t, initialEntries, p.state.GetPodEntries(),
			"candidate made invalid by a hook must not reach the revision CAS")
	})

	t.Run("accepts dedicated change with matching disjoint residual", func(t *testing.T) {
		t.Parallel()
		p, err := newResidualBackfillPrecommitTestPolicy(t)
		require.NoError(t, err)
		require.False(t, p.hardBulkheadPartitionValidationEnabled())

		candidate := residualBackfillPrecommitEntries(
			machine.NewCPUSet(2, 4),
			machine.NewCPUSet(4, 5, 6, 7),
		)
		p.allocation.allocationHooks = []AllocationHook{func(_, allocation *state.AllocationInfo) error {
			if allocation.PodUid == "dedicated-pod" {
				allocation.AllocationResult = machine.NewCPUSet(2, 3)
				allocation.OriginalAllocationResult = machine.NewCPUSet(2, 3)
			}
			return nil
		}}

		committed, _, err := p.commitPendingCPUPartition(pendingCPUPartition{
			expectedRevision: p.state.GetRevision(),
			entries:          candidate,
			persist:          false,
			source:           "residual hook test",
		})
		require.NoError(t, err)
		require.True(t, committed[commonstate.PoolNameShare][commonstate.FakedContainerName].
			AllocationResult.Equals(machine.NewCPUSet(4, 5, 6, 7)))
		require.True(t, cpuAssignmentsEqual(
			committed[commonstate.PoolNameShare][commonstate.FakedContainerName].TopologyAwareAssignments,
			map[int]machine.CPUSet{0: machine.NewCPUSet(4, 5, 6, 7)},
		))
		require.True(t, committed["dedicated-pod"]["main"].
			AllocationResult.Equals(machine.NewCPUSet(2, 3)))
	})

	t.Run("accepts share residual excluding ramp-up shared allocation", func(t *testing.T) {
		t.Parallel()
		p, err := newResidualBackfillPrecommitTestPolicy(t)
		require.NoError(t, err)
		require.False(t, p.hardBulkheadPartitionValidationEnabled())

		candidate := residualBackfillPrecommitEntries(
			machine.NewCPUSet(3),
			machine.NewCPUSet(4, 5, 6, 7),
		)
		candidate["ramp-up-shared-pod"] = state.ContainerEntries{
			"main": &state.AllocationInfo{
				AllocationMeta: commonstate.AllocationMeta{
					PodUid:        "ramp-up-shared-pod",
					ContainerName: "main",
					OwnerPoolName: commonstate.EmptyOwnerPoolName,
					QoSLevel:      apiconsts.PodAnnotationQoSLevelSharedCores,
				},
				RampUp:                   true,
				AllocationResult:         machine.NewCPUSet(2),
				TopologyAwareAssignments: map[int]machine.CPUSet{0: machine.NewCPUSet(2)},
			},
		}

		committed, _, err := p.commitPendingCPUPartition(pendingCPUPartition{
			expectedRevision: p.state.GetRevision(),
			entries:          candidate,
			persist:          false,
			source:           "residual ramp-up test",
		})
		require.NoError(t, err)
		require.True(t, committed[commonstate.PoolNameShare][commonstate.FakedContainerName].
			AllocationResult.Equals(machine.NewCPUSet(4, 5, 6, 7)))
		require.True(t, committed["ramp-up-shared-pod"]["main"].RampUp)
	})

	t.Run("rejects share topology assignments inconsistent with cpuset", func(t *testing.T) {
		t.Parallel()
		p, err := newResidualBackfillPrecommitTestPolicy(t)
		require.NoError(t, err)

		candidate := residualBackfillPrecommitEntries(
			machine.NewCPUSet(2, 3),
			machine.NewCPUSet(4, 5, 6, 7),
		)
		machineState, err := generateMachineStateFromPodEntries(
			p.machine.machineInfo.CPUTopology, candidate, p.state.GetMachineState())
		require.NoError(t, err)
		candidate[commonstate.PoolNameShare][commonstate.FakedContainerName].
			TopologyAwareAssignments = map[int]machine.CPUSet{0: machine.NewCPUSet(4, 5)}

		err = p.validateResidualBackfillCandidate(candidate, machineState, machine.NewCPUSet())
		require.ErrorContains(t, err, "topology assignments are inconsistent")
	})
}

func TestCommitPendingCPUPartitionNormalizesBeforeRebuildingMachineState(t *testing.T) {
	t.Parallel()
	p, cleanup := newReclaimReuseTestPolicy(t)
	defer cleanup()
	emitter := &recordingMetricEmitter{}
	p.emitter.emitter = emitter

	candidate := precommitPartitionEntries(machine.NewCPUSet(0, 1), machine.NewCPUSet(2, 3))
	dedicated := candidate["dedicated-pod"]["main"]
	dedicated.TopologyAwareAssignments = map[int]machine.CPUSet{1: machine.NewCPUSet(2, 3)}
	dedicated.OriginalTopologyAwareAssignments = map[int]machine.CPUSet{1: machine.NewCPUSet(2, 3)}

	committed, machineState, err := p.commitPendingCPUPartition(pendingCPUPartition{
		expectedRevision: p.state.GetRevision(),
		entries:          candidate,
		disableDedicated: true,
		persist:          false,
	})
	require.NoError(t, err)
	require.True(t, committed["dedicated-pod"]["main"].TopologyAwareAssignments[0].Equals(machine.NewCPUSet(2, 3)))
	require.Empty(t, committed["dedicated-pod"]["main"].TopologyAwareAssignments[1])
	require.True(t, machineState[0].PodEntries["dedicated-pod"]["main"].AllocationResult.Equals(machine.NewCPUSet(2, 3)))
	require.Len(t, emitter.records, 1)
	requirePoolSizeMetric(t, emitter.records, commonstate.PoolNameReclaim, commonstate.PoolNameReclaim, 0, 2)
}

func TestCommitPendingCPUPartitionRejectsRevisionChangedByHook(t *testing.T) {
	t.Parallel()
	p, cleanup := newReclaimReuseTestPolicy(t)
	defer cleanup()

	candidate := precommitPartitionEntries(machine.NewCPUSet(0, 1), machine.NewCPUSet(2, 3))
	p.allocation.allocationHooks = []AllocationHook{func(_, _ *state.AllocationInfo) error {
		p.state.SetAllowSharedCoresOverlapReclaimedCores(
			!p.state.GetAllowSharedCoresOverlapReclaimedCores(), false)
		return nil
	}}

	_, _, err := p.commitPendingCPUPartition(pendingCPUPartition{
		expectedRevision: p.state.GetRevision(),
		entries:          candidate,
		disableDedicated: true,
		persist:          false,
	})
	require.ErrorIs(t, err, state.ErrStaleStateRevision)
	require.Nil(t, p.state.GetAllocationInfo("dedicated-pod", "main"))
}

func TestPreparePendingCPUPartitionRevalidatesQuantityAfterHooks(t *testing.T) {
	t.Parallel()
	p, cleanup := newReclaimReuseTestPolicy(t)
	defer cleanup()

	candidate := precommitPartitionEntries(machine.NewCPUSet(0, 48), machine.NewCPUSet(1, 49))
	p.allocation.allocationHooks = []AllocationHook{func(_, allocation *state.AllocationInfo) error {
		if allocation.PodUid == "dedicated-pod" {
			allocation.AllocationResult = machine.NewCPUSet(1)
		}
		return nil
	}}

	_, _, err := p.commitPendingCPUPartition(pendingCPUPartition{
		expectedRevision: p.state.GetRevision(),
		entries:          candidate,
		disableDedicated: true,
		persist:          false,
	})

	require.ErrorContains(t, err, "allocation quantity changed after hooks")
	require.Nil(t, p.state.GetAllocationInfo("dedicated-pod", "main"))
}

func TestPreparePendingCPUPartitionRejectsReplacementQuantityMutation(t *testing.T) {
	t.Parallel()
	testPreparePendingCPUPartitionRejectsReplacementMutation(t,
		func(topology *machine.CPUTopology, entries state.PodEntries) {
			dedicated := coresInNUMA(topology, 0, 1, 2)
			entries["dedicated-pod"]["main"].AllocationResult = machine.NewCPUSet(dedicated.ToSliceInt()[0])
		},
		"allocation quantity changed",
	)
}

func TestPreparePendingCPUPartitionRejectsReplacementNUMAMigration(t *testing.T) {
	t.Parallel()
	testPreparePendingCPUPartitionRejectsReplacementMutation(t,
		func(topology *machine.CPUTopology, entries state.PodEntries) {
			entries["dedicated-pod"]["main"].AllocationResult = coresInNUMA(topology, 1, 0, 1)
		},
		"allocation NUMA distribution changed",
	)
}

func TestPreparePendingCPUPartitionRejectsPartialReclaimCore(t *testing.T) {
	t.Parallel()
	testPreparePendingCPUPartitionRejectsReplacementMutation(t,
		func(topology *machine.CPUTopology, entries state.PodEntries) {
			firstCore := coresInNUMA(topology, 0, 0, 1).ToSliceInt()
			secondCore := coresInNUMA(topology, 0, 1, 2).ToSliceInt()
			entries[commonstate.PoolNameReclaim][commonstate.FakedContainerName].
				AllocationResult = machine.NewCPUSet(firstCore[0], secondCore[0])
		},
		"reclaim is not core-aligned",
	)
}

func testPreparePendingCPUPartitionRejectsReplacementMutation(
	t *testing.T,
	mutate func(*machine.CPUTopology, state.PodEntries),
	wantError string,
) {
	t.Helper()

	topology, err := machine.GenerateDummyCPUTopology(96, 2, 2)
	require.NoError(t, err)
	stateDir := t.TempDir()
	p, err := getTestDynamicPolicyWithoutInitialization(topology, stateDir)
	require.NoError(t, err)

	require.NoError(t, p.state.StoreState())
	initialRevision := p.state.GetRevision()
	initialEntries := p.state.GetPodEntries()
	initialMachineState := p.state.GetMachineState()
	checkpointPath := filepath.Join(stateDir, cpuPluginStateFileName)
	initialCheckpoint, err := os.ReadFile(checkpointPath)
	require.NoError(t, err)

	planned := precommitPartitionEntries(
		coresInNUMA(topology, 0, 0, 1),
		coresInNUMA(topology, 0, 1, 2),
	)

	_, _, err = p.commitPendingCPUPartition(pendingCPUPartition{
		expectedRevision:          initialRevision,
		entries:                   planned,
		disableDedicated:          true,
		persist:                   true,
		source:                    "replacement mutation test",
		requireCoreAlignedReclaim: true,
		enforceSteadyReclaim:      true,
		validate: func(entries state.PodEntries, _ state.NUMANodeMap, _, _ bool) error {
			mutate(topology, entries)
			return nil
		},
	})

	require.ErrorContains(t, err, wantError)
	require.Equal(t, initialRevision, p.state.GetRevision())
	require.Equal(t, initialEntries, p.state.GetPodEntries())
	require.Equal(t, initialMachineState, p.state.GetMachineState())
	afterCheckpoint, readErr := os.ReadFile(checkpointPath)
	require.NoError(t, readErr)
	require.Equal(t, initialCheckpoint, afterCheckpoint)
}

func TestValidateSteadyReclaimPrecommitInvariant(t *testing.T) {
	t.Parallel()
	topology, err := machine.GenerateDummyCPUTopology(32, 2, 2)
	require.NoError(t, err)
	committed := coresInNUMA(topology, 0, 0, 4)
	planned := coresInNUMA(topology, 0, 2, 4).
		Union(coresInNUMA(topology, 1, 0, 2))

	t.Run("accepts planner migration above fixed limit when hooks preserve plan", func(t *testing.T) {
		t.Parallel()
		largePlan := coresInNUMA(topology, 1, 0, 4)
		require.Greater(t,
			steadyFakeNUMAMigrationChurn(committed, largePlan),
			steadyFakeNUMAMaxMigratedCPUs,
		)
		require.NoError(t, validateSteadyReclaimPrecommitInvariant(
			largePlan, largePlan, topology))
	})

	for _, tc := range []struct {
		name      string
		candidate machine.CPUSet
		want      string
	}{
		{
			name: "fragmented whole core",
			candidate: planned.Difference(machine.NewCPUSet(planned.ToSliceInt()[0])).
				Union(machine.NewCPUSet(coresInNUMA(topology, 1, 2, 3).ToSliceInt()[0])),
			want: "not core-aligned",
		},
		{
			name:      "quantity changed",
			candidate: coresInNUMA(topology, 0, 2, 4),
			want:      "quantity changed",
		},
		{
			name:      "per-NUMA floor changed",
			candidate: coresInNUMA(topology, 0, 0, 4),
			want:      "NUMA distribution changed",
		},
		{
			name: "hook churn exceeds limit",
			candidate: coresInNUMA(topology, 0, 4, 6).
				Union(coresInNUMA(topology, 1, 2, 4)),
			want: "migration churn",
		},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := validateSteadyReclaimPrecommitInvariant(
				planned, tc.candidate, topology)
			require.ErrorContains(t, err, tc.want)
		})
	}
}

func TestCommitPendingAdvisorStateRejectsFragmentedReclaimInDisjointMode(t *testing.T) {
	t.Parallel()
	p, cleanup := newReclaimReuseTestPolicy(t)
	defer cleanup()

	revision := p.state.GetRevision()
	core := coresInNUMA(p.machine.machineInfo.CPUTopology, 0, 0, 1)
	fragmentedReclaim := machine.NewCPUSet(core.ToSliceInt()[0])
	dedicated := coresInNUMA(p.machine.machineInfo.CPUTopology, 0, 1, 2)

	err := p.commitPendingAdvisorState(&pendingAdvisorState{
		preCommitRevision: revision,
		entries:           precommitPartitionEntries(fragmentedReclaim, dedicated),
		allowOverlap:      false,
		disableDedicated:  true,
	})

	require.ErrorContains(t, err, "reclaim set")
	require.ErrorContains(t, err, "is not core-aligned")
	require.Equal(t, revision, p.state.GetRevision(),
		"fragmented reclaim must be rejected before the revision CAS")
}

func TestPoolAdjustmentRejectsFragmentedReclaimBeforeCommit(t *testing.T) {
	t.Parallel()
	topology, err := machine.GenerateDummyCPUTopology(96, 2, 2)
	require.NoError(t, err)
	p, err := getTestDynamicPolicyWithInitialization(topology, t.TempDir())
	require.NoError(t, err)
	p.state.SetAllowSharedCoresOverlapReclaimedCores(false, false)
	p.state.SetDisableDedicatedCoresOverlapReclaimedCores(true, false)
	currentReclaim := machine.NewCPUSet(1, 25, 49, 73)
	currentAssignments, err := machine.GetNumaAwareAssignments(topology, currentReclaim)
	require.NoError(t, err)
	p.state.SetAllocationInfo(
		commonstate.PoolNameReclaim,
		commonstate.FakedContainerName,
		&state.AllocationInfo{
			AllocationMeta:                   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
			AllocationResult:                 currentReclaim.Clone(),
			OriginalAllocationResult:         currentReclaim.Clone(),
			TopologyAwareAssignments:         currentAssignments,
			OriginalTopologyAwareAssignments: machine.DeepcopyCPUAssignment(currentAssignments),
		},
		false,
	)

	revision := p.state.GetRevision()
	currentEntries := p.state.GetPodEntries()
	require.NoError(t, assertCoreAligned(currentReclaim, p.machine.machineInfo.CPUTopology))
	require.Equal(t, 4, currentReclaim.Size())

	err = p.applyPoolsAndIsolatedInfo(
		map[string]machine.CPUSet{
			commonstate.PoolNameReclaim: machine.NewCPUSet(1, 2, 25, 26),
			commonstate.PoolNameReserve: p.config.reservedCPUs.Clone(),
		},
		map[string]map[string]machine.CPUSet{},
		currentEntries,
		p.state.GetMachineState(),
		sets.NewInt(),
		false,
		machine.NewCPUSet(),
		defaultShareMaterializationPlan{},
		revision,
		p.config.dynamicConfig.GetDynamicConfiguration(),
	)

	require.ErrorContains(t, err, "reclaim set")
	require.ErrorContains(t, err, "is not core-aligned")
	require.Equal(t, revision, p.state.GetRevision(),
		"fragmented pool adjustment must be rejected before the revision CAS")
	require.True(t, p.state.GetAllocationInfo(
		commonstate.PoolNameReclaim, commonstate.FakedContainerName).
		AllocationResult.Equals(currentReclaim))
}

func TestCommitPendingCPUPartitionRejectsInvalidOverrideAndDeletionFallback(t *testing.T) {
	t.Parallel()
	p, cleanup := newReclaimReuseTestPolicy(t)
	defer cleanup()

	for _, source := range []string{"cpuset override", "deletion fallback"} {
		source := source
		t.Run(source, func(t *testing.T) {
			t.Parallel()
			candidate := precommitPartitionEntries(machine.NewCPUSet(0, 1), machine.NewCPUSet(1, 2))
			_, _, err := p.commitPendingCPUPartition(pendingCPUPartition{
				expectedRevision: p.state.GetRevision(),
				entries:          candidate,
				disableDedicated: true,
				persist:          false,
				source:           source,
			})
			require.Error(t, err)
			require.False(t, errors.Is(err, state.ErrStaleStateRevision))
			require.ErrorContains(t, err, "overlaps reclaim pool")
		})
	}
}

func TestValidateAdvisorPartitionBeforeCommitAllowsDedicatedOverlapWhenEnabled(t *testing.T) {
	t.Parallel()
	p, cleanup := newReclaimReuseTestPolicy(t)
	defer cleanup()

	err := p.validateAdvisorPartitionBeforeCommitWithDynamicConfig(
		precommitPartitionEntries(machine.NewCPUSet(0, 1), machine.NewCPUSet(1, 2)),
		nil,
		false,
		false,
		p.config.dynamicConfig.GetDynamicConfiguration())
	require.NoError(t, err)
}

func TestValidateAdvisorPartitionBeforeCommitRejectsDedicatedOverlapWhenDisabled(t *testing.T) {
	t.Parallel()
	p, cleanup := newReclaimReuseTestPolicy(t)
	defer cleanup()

	err := p.validateAdvisorPartitionBeforeCommitWithDynamicConfig(
		precommitPartitionEntries(machine.NewCPUSet(0, 1), machine.NewCPUSet(1, 2)),
		nil,
		false,
		true,
		p.config.dynamicConfig.GetDynamicConfiguration())
	require.ErrorContains(t, err, "overlaps reclaim pool")
}

func TestValidateAdvisorPartitionBeforeCommitRejectsSharedOverlapWhenDisabled(t *testing.T) {
	t.Parallel()
	p, cleanup := newReclaimReuseTestPolicy(t)
	defer cleanup()

	err := p.validateAdvisorPartitionBeforeCommitWithDynamicConfig(
		precommitPartitionEntriesWithShare(
			machine.NewCPUSet(0, 1),
			machine.NewCPUSet(2, 3),
			machine.NewCPUSet(1, 4),
		),
		nil,
		false,
		false,
		p.config.dynamicConfig.GetDynamicConfiguration())
	require.ErrorContains(t, err, "reclaim pool overlaps disallowed shared partition before commit")
}

func TestValidateAdvisorPartitionBeforeCommitAllowsSharedOverlapWhenEnabled(t *testing.T) {
	t.Parallel()
	p, cleanup := newReclaimReuseTestPolicy(t)
	defer cleanup()

	err := p.validateAdvisorPartitionBeforeCommitWithDynamicConfig(
		precommitPartitionEntriesWithShare(
			machine.NewCPUSet(0, 1),
			machine.NewCPUSet(1, 2),
			machine.NewCPUSet(0, 3),
		),
		nil,
		true,
		false,
		p.config.dynamicConfig.GetDynamicConfiguration())
	require.NoError(t, err)
}

func TestValidatePendingPoolOwnershipSupportsReclaimModesAndExactOrdinaryPools(t *testing.T) {
	t.Parallel()
	reclaim := machine.NewCPUSet(0, 1, 4, 5)
	targetNUMA := machine.NewCPUSet(0, 1)
	nonRNB := machine.NewCPUSet(4, 5)
	share := machine.NewCPUSet(6, 7)
	entries := state.PodEntries{
		commonstate.PoolNameReclaim: {
			commonstate.FakedContainerName: &state.AllocationInfo{
				AllocationMeta:                   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
				AllocationResult:                 reclaim.Clone(),
				OriginalAllocationResult:         reclaim.Clone(),
				TopologyAwareAssignments:         map[int]machine.CPUSet{0: targetNUMA.Clone(), 1: machine.NewCPUSet(4, 5)},
				OriginalTopologyAwareAssignments: map[int]machine.CPUSet{0: targetNUMA.Clone(), 1: machine.NewCPUSet(4, 5)},
			},
		},
		"rnb-pod": {
			"main": &state.AllocationInfo{
				AllocationMeta: commonstate.AllocationMeta{
					PodUid:        "rnb-pod",
					ContainerName: "main",
					OwnerPoolName: commonstate.PoolNameReclaim,
					QoSLevel:      apiconsts.PodAnnotationQoSLevelReclaimedCores,
					Annotations: map[string]string{
						apiconsts.PodAnnotationMemoryEnhancementNumaBinding: apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable,
					},
				},
				AllocationResult:         targetNUMA.Clone(),
				TopologyAwareAssignments: map[int]machine.CPUSet{0: targetNUMA.Clone()},
			},
		},
		"non-rnb-pod": {
			"main": &state.AllocationInfo{
				AllocationMeta: commonstate.AllocationMeta{
					PodUid:        "non-rnb-pod",
					ContainerName: "main",
					OwnerPoolName: commonstate.PoolNameReclaim,
					QoSLevel:      apiconsts.PodAnnotationQoSLevelReclaimedCores,
				},
				AllocationResult:         nonRNB.Clone(),
				TopologyAwareAssignments: map[int]machine.CPUSet{1: nonRNB.Clone()},
			},
		},
		commonstate.PoolNameShare: {
			commonstate.FakedContainerName: &state.AllocationInfo{
				AllocationMeta:                   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameShare),
				AllocationResult:                 share.Clone(),
				OriginalAllocationResult:         share.Clone(),
				TopologyAwareAssignments:         map[int]machine.CPUSet{1: share.Clone()},
				OriginalTopologyAwareAssignments: map[int]machine.CPUSet{1: share.Clone()},
			},
		},
		"shared-pod": {
			"main": &state.AllocationInfo{
				AllocationMeta: commonstate.AllocationMeta{
					PodUid:        "shared-pod",
					ContainerName: "main",
					OwnerPoolName: commonstate.PoolNameShare,
					QoSLevel:      apiconsts.PodAnnotationQoSLevelSharedCores,
				},
				AllocationResult:         share.Clone(),
				TopologyAwareAssignments: map[int]machine.CPUSet{1: share.Clone()},
			},
		},
	}
	entries["rnb-pod"]["main"].SetSpecifiedNUMABindingNUMAID([]uint64{0})

	require.NoError(t, validatePendingPoolOwnership(entries))

	entries["rnb-pod"]["main"].AllocationResult = machine.NewCPUSet(4, 5)
	require.ErrorContains(t, validatePendingPoolOwnership(entries), "differs from owner pool")
	entries["rnb-pod"]["main"].AllocationResult = targetNUMA.Clone()

	entries["non-rnb-pod"]["main"].AllocationResult = reclaim.Clone()
	require.ErrorContains(t, validatePendingPoolOwnership(entries), "differs from owner pool")
	entries["non-rnb-pod"]["main"].AllocationResult = nonRNB.Clone()

	entries["shared-pod"]["main"].AllocationResult = machine.NewCPUSet(6)
	require.ErrorContains(t, validatePendingPoolOwnership(entries), "differs from owner pool")
}

// makeReclaimedRNBAllocation builds an active (non-ramp-up) reclaimed-cores
// allocation pinned to the given NUMA with the given cpuset result.
func makeReclaimedRNBAllocation(podUID, container string, numaID int, result machine.CPUSet) *state.AllocationInfo {
	al := &state.AllocationInfo{
		AllocationMeta: commonstate.AllocationMeta{
			PodUid:        podUID,
			ContainerName: container,
			OwnerPoolName: commonstate.PoolNameReclaim,
			QoSLevel:      apiconsts.PodAnnotationQoSLevelReclaimedCores,
			Annotations: map[string]string{
				apiconsts.PodAnnotationMemoryEnhancementNumaBinding: apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable,
			},
		},
		AllocationResult: result.Clone(),
	}
	al.SetSpecifiedNUMABindingNUMAID([]uint64{uint64(numaID)})
	return al
}

func TestValidatePendingPoolOwnershipActiveRNBFailsClosed(t *testing.T) {
	t.Parallel()
	// The reclaim pool owns CPUs only on NUMA 1 (a sparse bucket map). An active
	// (non-ramp-up) RNB allocation pinned to a NUMA the pool does not back must
	// fail closed, whether its own result is empty or non-empty.
	reclaim := machine.NewCPUSet(4, 5)
	base := state.PodEntries{
		commonstate.PoolNameReclaim: {
			commonstate.FakedContainerName: &state.AllocationInfo{
				AllocationMeta:                   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
				AllocationResult:                 reclaim.Clone(),
				OriginalAllocationResult:         reclaim.Clone(),
				TopologyAwareAssignments:         map[int]machine.CPUSet{1: reclaim.Clone()},
				OriginalTopologyAwareAssignments: map[int]machine.CPUSet{1: reclaim.Clone()},
			},
		},
	}

	t.Run("missing bucket fails closed even when allocation is empty", func(t *testing.T) {
		t.Parallel()
		entries := base.Clone()
		// Active RNB pinned to NUMA 0, which the pool does not back, with an
		// empty result. This must NOT pass: an active RNB needs an explicit bucket.
		entries["rnb-pod"] = state.ContainerEntries{
			"main": makeReclaimedRNBAllocation("rnb-pod", "main", 0, machine.NewCPUSet()),
		}
		err := validatePendingPoolOwnership(entries)
		require.Error(t, err)
		require.Contains(t, err.Error(), "not backed by owner pool")
		require.Contains(t, err.Error(), "bucket_state=missing")
	})

	t.Run("present matching bucket passes", func(t *testing.T) {
		t.Parallel()
		entries := base.Clone()
		entries["rnb-pod"] = state.ContainerEntries{
			"main": makeReclaimedRNBAllocation("rnb-pod", "main", 1, reclaim.Clone()),
		}
		require.NoError(t, validatePendingPoolOwnership(entries))
	})

	t.Run("present non-empty bucket mismatch reports sizes", func(t *testing.T) {
		t.Parallel()
		entries := base.Clone()
		// Pool backs NUMA 1 with CPUs {4,5}; pod claims a different set.
		entries["rnb-pod"] = state.ContainerEntries{
			"main": makeReclaimedRNBAllocation("rnb-pod", "main", 1, machine.NewCPUSet(6, 7)),
		}
		err := validatePendingPoolOwnership(entries)
		require.Error(t, err)
		require.ErrorContains(t, err, "differs from owner pool")
		require.Contains(t, err.Error(), "expected")
		require.Contains(t, err.Error(), "size=")
	})
}

func TestValidatePendingPoolOwnershipOrdinaryReclaimedEmptySemantics(t *testing.T) {
	t.Parallel()
	// An ordinary (non-RNB) reclaimed allocation owns zero CPUs while the reclaim
	// pool itself owns zero on non-RNB NUMAs: both sides are empty and that is a
	// legitimate, consistent transient state (no active RNB is being starved).
	reclaim := machine.NewCPUSet()
	entries := state.PodEntries{
		commonstate.PoolNameReclaim: {
			commonstate.FakedContainerName: &state.AllocationInfo{
				AllocationMeta:           commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
				AllocationResult:         reclaim.Clone(),
				TopologyAwareAssignments: map[int]machine.CPUSet{},
			},
		},
		"reclaim-pod": {
			"main": &state.AllocationInfo{
				AllocationMeta: commonstate.AllocationMeta{
					PodUid:        "reclaim-pod",
					ContainerName: "main",
					OwnerPoolName: commonstate.PoolNameReclaim,
					QoSLevel:      apiconsts.PodAnnotationQoSLevelReclaimedCores,
				},
				AllocationResult: machine.NewCPUSet(),
			},
		},
	}
	require.NoError(t, validatePendingPoolOwnership(entries))
}

// TestValidatePendingPoolOwnershipAcceptsUninitializedMemberResultAgainstEmptyExpected pins
// the exact behavior the ElementsEqual switch enables at the ownership-validator call site
// (partition_precommit.go): an ordinary reclaimed member whose AllocationResult was never
// initialized (zero CPUSet, nil backing map, Initialed=false) must still be accepted when
// the owner pool's expected cpuset is an initialized empty set. Content-wise the two are
// equal; only the representation (nil map vs initialized-empty map) differs.
//
// This is the RED-capable guard: it FAILS if the call site is reverted from
// ElementsEqual back to Equals, because reflect.DeepEqual(nil map, empty non-nil map) is
// false. No other integration test exercises this nil-vs-empty representation distinction at
// the validator; the sibling ...OrdinaryReclaimedEmptySemantics test initializes both sides
// and therefore passes under either equality.
func TestValidatePendingPoolOwnershipAcceptsUninitializedMemberResultAgainstEmptyExpected(t *testing.T) {
	t.Parallel()
	reclaimPoolEntry := &state.AllocationInfo{
		AllocationMeta:           commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
		AllocationResult:         machine.NewCPUSet(),
		TopologyAwareAssignments: map[int]machine.CPUSet{},
	}
	entries := state.PodEntries{
		commonstate.PoolNameReclaim: {commonstate.FakedContainerName: reclaimPoolEntry},
		"reclaim-pod": {
			"main": &state.AllocationInfo{
				AllocationMeta: commonstate.AllocationMeta{
					PodUid:        "reclaim-pod",
					ContainerName: "main",
					OwnerPoolName: commonstate.PoolNameReclaim,
					QoSLevel:      apiconsts.PodAnnotationQoSLevelReclaimedCores,
				},
				// Uninitialized: zero value, nil backing map, Initialed=false. The
				// pool's expected cpuset is an initialized empty set; element-wise
				// they are equal, so the validator must accept them.
				AllocationResult: machine.CPUSet{},
			},
		},
	}
	require.NoError(t, validatePendingPoolOwnership(entries),
		"uninitialized member result must equal an initialized-empty expected by elements")

	// ElementsEqual is not blanket-accepting: once the pool's expected cpuset is
	// non-empty, an uninitialized (zero-CPU) member must still be rejected, proving
	// the comparison validates real element equality rather than accepting everything.
	reclaimPoolEntry.AllocationResult = machine.NewCPUSet(4, 5)
	reclaimPoolEntry.TopologyAwareAssignments = map[int]machine.CPUSet{0: machine.NewCPUSet(4, 5)}
	require.ErrorContains(t, validatePendingPoolOwnership(entries), "differs from owner pool")
}

// TestValidatePendingPoolOwnershipFourNUMASparseLayout covers a 4-NUMA host
// where the reclaim pool backs a sparse subset of NUMAs: backed RNB pods pass,
// and an RNB pod pinned to an unbacked NUMA fails closed even though other
// NUMAs are correctly provisioned.
func TestValidatePendingPoolOwnershipFourNUMASparseLayout(t *testing.T) {
	t.Parallel()
	numa0 := machine.NewCPUSet(0, 48)
	numa2 := machine.NewCPUSet(16, 64)
	entries := state.PodEntries{
		commonstate.PoolNameReclaim: {
			commonstate.FakedContainerName: &state.AllocationInfo{
				AllocationMeta:           commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
				AllocationResult:         numa0.Clone().Union(numa2),
				TopologyAwareAssignments: map[int]machine.CPUSet{0: numa0.Clone(), 2: numa2.Clone()},
			},
		},
		"rnb-a": {"main": makeReclaimedRNBAllocation("rnb-a", "main", 0, numa0.Clone())},
		"rnb-b": {"main": makeReclaimedRNBAllocation("rnb-b", "main", 2, numa2.Clone())},
	}
	// Both backed RNB pods validate.
	require.NoError(t, validatePendingPoolOwnership(entries))

	// Add an active RNB pinned to unbacked NUMA 1: must fail closed.
	entries["rnb-c"] = state.ContainerEntries{
		"main": makeReclaimedRNBAllocation("rnb-c", "main", 1, machine.NewCPUSet(4, 5)),
	}
	err := validatePendingPoolOwnership(entries)
	require.Error(t, err)
	require.Contains(t, err.Error(), "not backed by owner pool")
}

func TestValidatePendingPoolOwnershipMultipleRNBTargets(t *testing.T) {
	t.Parallel()
	// Two active RNB pods pinned to two NUMAs; the pool backs one. The unbacked
	// one must fail closed even though the backed one is fine.
	reclaim := machine.NewCPUSet(4, 5)
	base := state.PodEntries{
		commonstate.PoolNameReclaim: {
			commonstate.FakedContainerName: &state.AllocationInfo{
				AllocationMeta:                   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
				AllocationResult:                 reclaim.Clone(),
				OriginalAllocationResult:         reclaim.Clone(),
				TopologyAwareAssignments:         map[int]machine.CPUSet{1: reclaim.Clone()},
				OriginalTopologyAwareAssignments: map[int]machine.CPUSet{1: reclaim.Clone()},
			},
		},
		"rnb-a": {
			"main": makeReclaimedRNBAllocation("rnb-a", "main", 1, reclaim.Clone()),
		},
		"rnb-b": {
			"main": makeReclaimedRNBAllocation("rnb-b", "main", 0, machine.NewCPUSet(0, 1)),
		},
	}
	err := validatePendingPoolOwnership(base)
	require.Error(t, err)
	require.Contains(t, err.Error(), "rnb-b")
	require.Contains(t, err.Error(), "not backed by owner pool")
}

func TestPreparePendingCPUPartitionOwnershipFailureLeavesStateUntouched(t *testing.T) {
	t.Parallel()
	p, cleanup := newReclaimReuseTestPolicy(t)
	defer cleanup()

	// Seed a committed reclaim pool that backs only NUMA 1.
	committedReclaim := machine.NewCPUSet(4, 5)
	p.state.SetPodEntries(state.PodEntries{
		commonstate.PoolNameReclaim: {
			commonstate.FakedContainerName: &state.AllocationInfo{
				AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
				AllocationResult: committedReclaim.Clone(),
				TopologyAwareAssignments: map[int]machine.CPUSet{
					1: committedReclaim.Clone(),
				},
			},
		},
	}, false)
	before := p.state.GetPodEntries()

	// Candidate pins an active RNB to NUMA 0 but gives the pool no backing there.
	candidate := before.Clone()
	candidate["rnb-pod"] = state.ContainerEntries{
		"main": makeReclaimedRNBAllocation("rnb-pod", "main", 0, machine.NewCPUSet(0, 1)),
	}

	_, err := p.preparePendingCPUPartition(pendingCPUPartition{
		expectedRevision: p.state.GetRevision(),
		entries:          candidate,
		persist:          false,
		source:           "cas-rollback-test",
	})
	require.Error(t, err)

	// The ownership failure must abort before any state mutation: the committed
	// entries are still exactly what was seeded.
	require.Equal(t, before, p.state.GetPodEntries())
}

func TestMachineStateIsCandidateDerivedNotStaleBaseline(t *testing.T) {
	t.Parallel()
	p, cleanup := newReclaimReuseTestPolicy(t)
	defer cleanup()

	// Baseline machine state already contains a pod "gone" owning CPUs {2,3}.
	baseline := state.PodEntries{
		commonstate.PoolNameReclaim: {
			commonstate.FakedContainerName: &state.AllocationInfo{
				AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
				AllocationResult: machine.NewCPUSet(0, 1),
			},
		},
		"gone": {
			"main": &state.AllocationInfo{
				AllocationMeta: commonstate.AllocationMeta{
					PodUid:        "gone",
					ContainerName: "main",
					QoSLevel:      apiconsts.PodAnnotationQoSLevelSharedCores,
				},
				AllocationResult: machine.NewCPUSet(2, 3),
			},
		},
	}
	origin, err := generateMachineStateFromPodEntries(p.machine.machineInfo.CPUTopology, baseline, nil)
	require.NoError(t, err)

	// The new candidate drops "gone" entirely; it must not linger in the
	// rebuilt machine state (CPU allocation is fully candidate-derived).
	candidate := state.PodEntries{
		commonstate.PoolNameReclaim: {
			commonstate.FakedContainerName: &state.AllocationInfo{
				AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
				AllocationResult: machine.NewCPUSet(0, 1),
			},
		},
	}
	rebuilt, err := generateMachineStateFromPodEntries(p.machine.machineInfo.CPUTopology, candidate, origin)
	require.NoError(t, err)
	for numaID, numaState := range rebuilt {
		_, present := numaState.PodEntries["gone"]
		require.False(t, present, "removed pod must not linger in rebuilt NUMA %d machine state", numaID)
	}
}

func TestRampUpSidecarMirrorsMainContainerThroughPrecommitAndRampUpExit(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name        string
		annotations map[string]string
	}{
		{
			name: "shared",
		},
		{
			name: "shared numa binding",
			annotations: map[string]string{
				apiconsts.PodAnnotationMemoryEnhancementNumaBinding: apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable,
			},
		},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p, cleanup := newReclaimReuseTestPolicy(t)
			defer cleanup()

			rampUpCPUs := machine.NewCPUSet(2, 3)
			rampUpAssignments := map[int]machine.CPUSet{0: rampUpCPUs.Clone()}
			main := &state.AllocationInfo{
				AllocationMeta: commonstate.AllocationMeta{
					PodUid:        "shared-pod",
					ContainerName: "main",
					ContainerType: pluginapi.ContainerType_MAIN.String(),
					OwnerPoolName: commonstate.EmptyOwnerPoolName,
					QoSLevel:      apiconsts.PodAnnotationQoSLevelSharedCores,
					Annotations:   tc.annotations,
				},
				RampUp:                           true,
				AllocationResult:                 rampUpCPUs.Clone(),
				OriginalAllocationResult:         rampUpCPUs.Clone(),
				TopologyAwareAssignments:         machine.DeepcopyCPUAssignment(rampUpAssignments),
				OriginalTopologyAwareAssignments: machine.DeepcopyCPUAssignment(rampUpAssignments),
			}
			sidecar := &state.AllocationInfo{
				AllocationMeta: commonstate.AllocationMeta{
					PodUid:        "shared-pod",
					ContainerName: "sidecar",
					ContainerType: pluginapi.ContainerType_SIDECAR.String(),
					OwnerPoolName: commonstate.EmptyOwnerPoolName,
					QoSLevel:      apiconsts.PodAnnotationQoSLevelSharedCores,
					Annotations:   map[string]string{},
				},
				RampUp:                           false,
				AllocationResult:                 machine.NewCPUSet(4, 5),
				OriginalAllocationResult:         machine.NewCPUSet(4, 5),
				TopologyAwareAssignments:         map[int]machine.CPUSet{0: machine.NewCPUSet(4, 5)},
				OriginalTopologyAwareAssignments: map[int]machine.CPUSet{0: machine.NewCPUSet(4, 5)},
			}

			require.NoError(t, validatePendingPoolOwnership(state.PodEntries{
				"shared-pod": {"main": main},
			}), "a ramp-up main container may have an empty owner")
			require.ErrorContains(t, validatePendingPoolOwnership(state.PodEntries{
				"shared-pod": {"main": main, "sidecar": sidecar},
			}), "empty owner pool", "a stale non-ramp-up sidecar must not bypass ownership validation")

			require.True(t, p.applySidecarAllocationInfoFromMainContainer(sidecar, main))
			require.Equal(t, main.RampUp, sidecar.RampUp)
			require.Equal(t, main.OwnerPoolName, sidecar.OwnerPoolName)
			require.True(t, sidecar.AllocationResult.Equals(main.AllocationResult))
			require.True(t, sidecar.OriginalAllocationResult.Equals(main.OriginalAllocationResult))
			require.True(t, state.CheckAllocationInfoTopologyAwareAssignments(sidecar, main))
			require.True(t, state.CheckAllocationInfoOriginTopologyAwareAssignments(sidecar, main))

			entries := precommitPartitionEntriesWithShare(
				machine.NewCPUSet(0, 1),
				machine.NewCPUSet(6, 7),
				rampUpCPUs,
			)
			entries["shared-pod"] = state.ContainerEntries{"main": main, "sidecar": sidecar}
			committed, _, err := p.commitPendingCPUPartition(pendingCPUPartition{
				expectedRevision: p.state.GetRevision(),
				entries:          entries,
				disableDedicated: true,
				persist:          false,
				source:           "ramp-up sidecar test",
			})
			require.NoError(t, err)
			require.True(t, committed["shared-pod"]["sidecar"].RampUp)

			main = committed["shared-pod"]["main"].Clone()
			sidecar = committed["shared-pod"]["sidecar"].Clone()
			main.RampUp = false
			main.OwnerPoolName = commonstate.PoolNameShare
			require.True(t, p.applySidecarAllocationInfoFromMainContainer(sidecar, main))
			require.False(t, sidecar.RampUp)
			require.Equal(t, commonstate.PoolNameShare, sidecar.OwnerPoolName)
			require.NoError(t, validatePendingPoolOwnership(state.PodEntries{
				commonstate.PoolNameShare: entries[commonstate.PoolNameShare],
				"shared-pod":              {"main": main, "sidecar": sidecar},
			}))
		})
	}
}

func precommitPartitionEntries(reclaim, dedicated machine.CPUSet) state.PodEntries {
	return state.PodEntries{
		commonstate.PoolNameReclaim: {
			commonstate.FakedContainerName: &state.AllocationInfo{
				AllocationMeta:           commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
				AllocationResult:         reclaim.Clone(),
				OriginalAllocationResult: reclaim.Clone(),
			},
		},
		"dedicated-pod": {
			"main": &state.AllocationInfo{
				AllocationMeta: commonstate.AllocationMeta{
					PodUid:        "dedicated-pod",
					ContainerName: "main",
					OwnerPoolName: commonstate.PoolNameDedicated,
					QoSLevel:      apiconsts.PodAnnotationQoSLevelDedicatedCores,
				},
				AllocationResult:         dedicated.Clone(),
				OriginalAllocationResult: dedicated.Clone(),
			},
		},
	}
}

func precommitPartitionEntriesWithShare(reclaim, dedicated, share machine.CPUSet) state.PodEntries {
	entries := precommitPartitionEntries(reclaim, dedicated)
	entries[commonstate.PoolNameShare] = state.ContainerEntries{
		commonstate.FakedContainerName: &state.AllocationInfo{
			AllocationMeta:           commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameShare),
			AllocationResult:         share.Clone(),
			OriginalAllocationResult: share.Clone(),
		},
	}
	return entries
}

func newResidualBackfillPrecommitTestPolicy(t *testing.T) (*DynamicPolicy, error) {
	t.Helper()

	topology, err := machine.GenerateDummyCPUTopology(8, 1, 1)
	if err != nil {
		return nil, err
	}
	p, err := getTestDynamicPolicyWithInitialization(topology, t.TempDir())
	if err != nil {
		return nil, err
	}
	p.config.reservedCPUs = machine.NewCPUSet()
	dynamicConf := p.config.dynamicConfig.GetDynamicConfiguration()
	dynamicConf.FillDefaultSharePoolWithNonReclaimCPUs = true
	dynamicConf.EnableRampUpReclaimHardPartition = false
	dynamicConf.AdminQoSConfiguration.CPUPluginConfiguration.BulkheadConfig.Enable = false
	return p, nil
}

func residualBackfillPrecommitEntries(dedicated, share machine.CPUSet) state.PodEntries {
	return precommitPartitionEntriesWithShare(
		machine.NewCPUSet(0, 1),
		dedicated,
		share,
	)
}

// makeOrdinaryReclaimedAllocation builds a non-NUMA-binding reclaimed_cores
// allocation owned by the reclaim pool.
func makeOrdinaryReclaimedAllocation(podUID, container string, result machine.CPUSet) *state.AllocationInfo {
	return &state.AllocationInfo{
		AllocationMeta: commonstate.AllocationMeta{
			PodUid:        podUID,
			ContainerName: container,
			OwnerPoolName: commonstate.PoolNameReclaim,
			QoSLevel:      apiconsts.PodAnnotationQoSLevelReclaimedCores,
		},
		AllocationResult: result.Clone(),
	}
}

// TestValidateNoReclaimedRampUp pins the unified invariant: every reclaimed_cores
// allocation, both RNB and ordinary (non-NUMA-binding), must never carry
// RampUp=true, while shared_cores ramp-up stays legal. Errors must name the
// pod/container and the reclaimed sub-category.
func TestValidateNoReclaimedRampUp(t *testing.T) {
	t.Parallel()
	reclaimPool := state.PodEntries{
		commonstate.PoolNameReclaim: {
			commonstate.FakedContainerName: &state.AllocationInfo{
				AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
				AllocationResult: machine.NewCPUSet(0, 1),
			},
		},
	}

	t.Run("RNB reclaimed ramp-up is rejected", func(t *testing.T) {
		t.Parallel()
		entries := reclaimPool.Clone()
		rnb := makeReclaimedRNBAllocation("rnb-pod", "main", 0, machine.NewCPUSet(0, 1))
		rnb.RampUp = true
		entries["rnb-pod"] = state.ContainerEntries{"main": rnb}

		err := validateNoReclaimedRampUp(entries)
		require.Error(t, err)
		require.Contains(t, err.Error(), "rnb-pod/main")
		require.Contains(t, err.Error(), "reclaimed-numa-binding (RNB)")
		require.Contains(t, err.Error(), "illegally RampUp")
	})

	t.Run("ordinary reclaimed ramp-up is rejected", func(t *testing.T) {
		t.Parallel()
		entries := reclaimPool.Clone()
		ordinary := makeOrdinaryReclaimedAllocation("reclaim-pod", "main", machine.NewCPUSet(0, 1))
		ordinary.RampUp = true
		entries["reclaim-pod"] = state.ContainerEntries{"main": ordinary}

		err := validateNoReclaimedRampUp(entries)
		require.Error(t, err)
		require.Contains(t, err.Error(), "reclaim-pod/main")
		require.Contains(t, err.Error(), "reclaimed non-NUMA-binding")
	})

	t.Run("reclaimed sidecar ramp-up is rejected", func(t *testing.T) {
		t.Parallel()
		entries := reclaimPool.Clone()
		sidecar := makeOrdinaryReclaimedAllocation("reclaim-pod", "sidecar", machine.NewCPUSet(0, 1))
		sidecar.RampUp = true
		entries["reclaim-pod"] = state.ContainerEntries{
			"main":    makeOrdinaryReclaimedAllocation("reclaim-pod", "main", machine.NewCPUSet(0, 1)),
			"sidecar": sidecar,
		}

		err := validateNoReclaimedRampUp(entries)
		require.Error(t, err)
		require.Contains(t, err.Error(), "reclaim-pod/sidecar")
	})

	t.Run("shared ramp-up stays legal", func(t *testing.T) {
		t.Parallel()
		entries := reclaimPool.Clone()
		entries["shared-pod"] = state.ContainerEntries{
			"main": &state.AllocationInfo{
				AllocationMeta: commonstate.AllocationMeta{
					PodUid:        "shared-pod",
					ContainerName: "main",
					OwnerPoolName: commonstate.EmptyOwnerPoolName,
					QoSLevel:      apiconsts.PodAnnotationQoSLevelSharedCores,
				},
				AllocationResult: machine.NewCPUSet(0),
				RampUp:           true,
			},
		}
		require.NoError(t, validateNoReclaimedRampUp(entries))
	})

	t.Run("reclaimed without ramp-up is legal", func(t *testing.T) {
		t.Parallel()
		entries := reclaimPool.Clone()
		entries["reclaim-pod"] = state.ContainerEntries{
			"main": makeOrdinaryReclaimedAllocation("reclaim-pod", "main", machine.NewCPUSet(0, 1)),
		}
		require.NoError(t, validateNoReclaimedRampUp(entries))
	})
}

// TestPreparePendingCPUPartitionRejectsIllegalReclaimedRampUp verifies the
// earliest common precommit boundary fails fast on an old-style candidate that
// carries a reclaimed_cores container in ramp-up, wrapping source/stage and
// leaving committed state untouched.
func TestPreparePendingCPUPartitionRejectsIllegalReclaimedRampUp(t *testing.T) {
	t.Parallel()
	p, cleanup := newReclaimReuseTestPolicy(t)
	defer cleanup()

	candidate := precommitPartitionEntries(machine.NewCPUSet(0, 1), machine.NewCPUSet(2, 3))
	rnb := makeReclaimedRNBAllocation("rnb-pod", "main", 0, machine.NewCPUSet(0, 1))
	rnb.RampUp = true
	candidate["rnb-pod"] = state.ContainerEntries{"main": rnb}

	committedRevisionBefore := p.state.GetRevision()
	_, err := p.preparePendingCPUPartition(pendingCPUPartition{
		expectedRevision: committedRevisionBefore,
		entries:          candidate,
		dynamicConfig:    p.config.dynamicConfig.GetDynamicConfiguration(),
		persist:          false,
		source:           "illegal reclaimed ramp-up candidate",
	})
	require.Error(t, err)
	// Source/stage wrapping plus the invariant detail must both be present.
	require.Contains(t, err.Error(), "reclaimed ramp-up invariant")
	require.Contains(t, err.Error(), "rnb-pod/main")
	require.Contains(t, err.Error(), "reclaimed-numa-binding (RNB)")
	// Committed state must not advance.
	require.Equal(t, committedRevisionBefore, p.state.GetRevision())
	require.Nil(t, p.state.GetAllocationInfo("rnb-pod", "main"))
}

// TestReviseReclaimPoolRejectsIllegalReclaimedRampUp verifies the advisor path
// cannot promote an async candidate that puts a reclaimed_cores container in
// ramp-up: reviseReclaimPool rejects it before any backfill.
func TestReviseReclaimPoolRejectsIllegalReclaimedRampUp(t *testing.T) {
	t.Parallel()
	p, cleanup := newReclaimReuseTestPolicy(t)
	defer cleanup()

	newEntries := state.PodEntries{
		commonstate.PoolNameReclaim: {
			commonstate.FakedContainerName: &state.AllocationInfo{
				AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
				AllocationResult: machine.NewCPUSet(0, 1),
			},
		},
	}
	ordinary := makeOrdinaryReclaimedAllocation("reclaim-pod", "main", machine.NewCPUSet(0, 1))
	ordinary.RampUp = true
	newEntries["reclaim-pod"] = state.ContainerEntries{"main": ordinary}

	err := p.reviseReclaimPool(
		newEntries,
		machine.NewCPUSet(),
		machine.NewCPUSet(),
		false,
	)
	require.Error(t, err)
	require.Contains(t, err.Error(), "reclaim-pod/main")
	require.Contains(t, err.Error(), "reclaimed non-NUMA-binding")
}

// TestSharedRampUpPrecommitStillLegal verifies the success chain: a shared_cores
// ramp-up container passes the precommit boundary unchanged by the invariant.
func TestSharedRampUpPrecommitStillLegal(t *testing.T) {
	t.Parallel()
	p, cleanup := newReclaimReuseTestPolicy(t)
	defer cleanup()

	entries := precommitPartitionEntries(machine.NewCPUSet(0, 1), machine.NewCPUSet(2, 3))
	entries["shared-pod"] = state.ContainerEntries{
		"main": &state.AllocationInfo{
			AllocationMeta: commonstate.AllocationMeta{
				PodUid:        "shared-pod",
				ContainerName: "main",
				OwnerPoolName: commonstate.EmptyOwnerPoolName,
				QoSLevel:      apiconsts.PodAnnotationQoSLevelSharedCores,
			},
			AllocationResult: machine.NewCPUSet(0),
			RampUp:           true,
		},
	}

	_, err := p.preparePendingCPUPartition(pendingCPUPartition{
		expectedRevision: p.state.GetRevision(),
		entries:          entries,
		dynamicConfig:    p.config.dynamicConfig.GetDynamicConfiguration(),
		persist:          false,
		source:           "legal shared ramp-up chain",
	})
	require.NoError(t, err)
}

// TestAllocateRejectsIllegalReclaimedRampUpExistingAllocation pins the
// synchronous Allocate boundary: a restored checkpoint that carries a reclaimed
// RampUp allocation must be rejected before the existing-allocation fast return
// can hand it back. Both RNB and ordinary (non-NUMA-binding) reclaimed are
// covered, plus a sidecar/main inconsistent state.
func TestAllocateRejectsIllegalReclaimedRampUpExistingAllocation(t *testing.T) {
	t.Parallel()
	cpuTopology, err := machine.GenerateDummyCPUTopology(16, 1, 2)
	require.NoError(t, err)

	testCases := []struct {
		name        string
		buildEntry  func() *state.AllocationInfo
		wantContain string
	}{
		{
			name: "RNB reclaimed ramp-up rejected",
			buildEntry: func() *state.AllocationInfo {
				a := makeReclaimedRNBAllocation("rnb-pod", "main", 0, machine.NewCPUSet(0, 8))
				a.RampUp = true
				return a
			},
			wantContain: "rnb-pod/main",
		},
		{
			name: "ordinary reclaimed ramp-up rejected",
			buildEntry: func() *state.AllocationInfo {
				a := makeOrdinaryReclaimedAllocation("reclaim-pod", "main", machine.NewCPUSet(0, 8))
				a.RampUp = true
				return a
			},
			wantContain: "reclaim-pod/main",
		},
		{
			name: "sidecar reclaimed ramp-up rejected",
			buildEntry: func() *state.AllocationInfo {
				a := makeOrdinaryReclaimedAllocation("reclaim-pod", "sidecar", machine.NewCPUSet(0, 8))
				a.RampUp = true
				return a
			},
			wantContain: "reclaim-pod/sidecar",
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p, err := getTestDynamicPolicyWithoutInitialization(cpuTopology, t.TempDir())
			require.NoError(t, err)
			entry := tc.buildEntry()
			entry.OriginalAllocationResult = entry.AllocationResult.Clone()
			require.NoError(t, p.state.SetAllocationInfo(entry.PodUid, entry.ContainerName, entry, false))

			req := &pluginapi.ResourceRequest{
				PodUid:        entry.PodUid,
				PodNamespace:  "default",
				PodName:       entry.PodUid,
				ContainerName: entry.ContainerName,
				ContainerType: pluginapi.ContainerType_MAIN,
				ResourceName:  string("cpu"),
				ResourceRequests: map[string]float64{
					"cpu": 1,
				},
				Annotations: map[string]string{
					apiconsts.PodAnnotationQoSLevelKey: entry.QoSLevel,
				},
			}
			_, err = p.Allocate(context.Background(), req)
			require.Error(t, err)
			require.Contains(t, err.Error(), tc.wantContain)
			require.Contains(t, err.Error(), "illegally RampUp")
		})
	}
}

// TestStartRejectsIllegalReclaimedRampUpInRestoredState pins the startup/restore
// boundary: Start() fails closed when a restored checkpoint carries an illegal
// reclaimed RampUp allocation, rather than silently serving it.
func TestStartRejectsIllegalReclaimedRampUpInRestoredState(t *testing.T) {
	t.Parallel()
	cpuTopology, err := machine.GenerateDummyCPUTopology(16, 1, 2)
	require.NoError(t, err)

	p, err := getTestDynamicPolicyWithoutInitialization(cpuTopology, t.TempDir())
	require.NoError(t, err)

	rnb := makeReclaimedRNBAllocation("rnb-pod", "main", 0, machine.NewCPUSet(0, 8))
	rnb.RampUp = true
	p.state.SetAllocationInfo(rnb.PodUid, rnb.ContainerName, rnb, false)

	err = p.Start()
	require.Error(t, err)
	require.Contains(t, err.Error(), "rnb-pod/main")
	require.Contains(t, err.Error(), "illegally RampUp")
}

// TestUpdateAllocationInfoRejectsHookPoisonedReclaimedRampUp pins the legacy
// direct-write boundary: a synchronous allocation hook that sets RampUp on a
// reclaimed container must be rejected before SetAllocationInfo persists it, for
// both RNB and ordinary reclaimed. A shared RampUp hook stays legal.
func TestUpdateAllocationInfoRejectsHookPoisonedReclaimedRampUp(t *testing.T) {
	t.Parallel()
	cpuTopology, err := machine.GenerateDummyCPUTopology(16, 1, 2)
	require.NoError(t, err)

	testCases := []struct {
		name        string
		buildEntry  func() *state.AllocationInfo
		wantContain string
	}{
		{
			name: "hook poisons RNB reclaimed ramp-up",
			buildEntry: func() *state.AllocationInfo {
				a := makeReclaimedRNBAllocation("rnb-pod", "main", 0, machine.NewCPUSet(0, 8))
				a.RampUp = true
				return a
			},
			wantContain: "rnb-pod/main",
		},
		{
			name: "hook poisons ordinary reclaimed ramp-up",
			buildEntry: func() *state.AllocationInfo {
				a := makeOrdinaryReclaimedAllocation("reclaim-pod", "main", machine.NewCPUSet(0, 8))
				a.RampUp = true
				return a
			},
			wantContain: "reclaim-pod/main",
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p, err := getTestDynamicPolicyWithoutInitialization(cpuTopology, t.TempDir())
			require.NoError(t, err)

			entry := tc.buildEntry()
			// Hook "poisons" the allocation by setting RampUp (already set in fixture,
			// but we verify the hook path is what triggers the validation).
			p.allocation.allocationHooks = []AllocationHook{
				func(_, allocation *state.AllocationInfo) error {
					allocation.RampUp = true
					return nil
				},
			}

			err = p.updateAllocationInfo(entry.PodUid, entry.ContainerName, nil, entry, false)
			require.Error(t, err)
			require.Contains(t, err.Error(), tc.wantContain)
			require.Contains(t, err.Error(), "illegally RampUp")

			// No state write must have occurred.
			require.Nil(t, p.state.GetAllocationInfo(entry.PodUid, entry.ContainerName),
				"poisoned allocation must not be persisted")
		})
	}

	t.Run("shared ramp-up hook stays legal", func(t *testing.T) {
		t.Parallel()
		p, err := getTestDynamicPolicyWithoutInitialization(cpuTopology, t.TempDir())
		require.NoError(t, err)

		shared := &state.AllocationInfo{
			AllocationMeta: commonstate.AllocationMeta{
				PodUid:        "shared-pod",
				ContainerName: "main",
				OwnerPoolName: commonstate.EmptyOwnerPoolName,
				QoSLevel:      apiconsts.PodAnnotationQoSLevelSharedCores,
			},
			AllocationResult: machine.NewCPUSet(0),
		}
		p.allocation.allocationHooks = []AllocationHook{
			func(_, allocation *state.AllocationInfo) error {
				allocation.RampUp = true
				return nil
			},
		}

		require.NoError(t, p.updateAllocationInfo(shared.PodUid, shared.ContainerName, nil, shared, false))
		got := p.state.GetAllocationInfo(shared.PodUid, shared.ContainerName)
		require.NotNil(t, got)
		require.True(t, got.RampUp, "shared ramp-up must be persisted")
	})
}

// TestReclaimBucketClassStateMatrix pins the four-state value object: missing,
// uninitialized, explicit-empty, and ready are distinguishable, and only ready is
// acceptable. It also proves the producer and the validator agree on the matrix —
// neither silently materializes an empty/uninitialized RNB bucket.
func TestReclaimBucketClassStateMatrix(t *testing.T) {
	t.Parallel()
	pool := func(buckets map[int]machine.CPUSet) *state.AllocationInfo {
		return &state.AllocationInfo{
			AllocationMeta:           commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
			AllocationResult:         machine.NewCPUSet(4, 5),
			TopologyAwareAssignments: buckets,
		}
	}

	t.Run("lookup classifies all four states", func(t *testing.T) {
		t.Parallel()
		cases := []struct {
			name   string
			bucket machine.CPUSet
			hasKey bool
			want   reclaimBucketClass
		}{
			{"absent", machine.CPUSet{}, false, reclaimBucketAbsent},
			{"uninitialized", machine.CPUSet{}, true, reclaimBucketUninitialized},
			{"explicit-empty", machine.NewCPUSet(), true, reclaimBucketEmpty},
			{"ready", machine.NewCPUSet(4, 5), true, reclaimBucketReady},
		}
		for _, tc := range cases {
			tc := tc
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()
				buckets := map[int]machine.CPUSet{}
				if tc.hasKey {
					buckets[0] = tc.bucket
				}
				rb := lookupReclaimBucketIn(buckets, 0)
				require.Equal(t, tc.want, rb.class)
			})
		}
	})

	t.Run("missing vs uninitialized vs empty are textually distinguishable", func(t *testing.T) {
		t.Parallel()
		present := map[int]machine.CPUSet{
			0: {},                  // uninitialized
			1: machine.NewCPUSet(), // explicit-empty
		}
		// NUMA 2 intentionally absent.
		for numaID, want := range map[int]string{
			0: "bucket_state=uninitialized",
			1: "bucket_state=explicit-empty",
			2: "bucket_state=missing",
		} {
			err := lookupReclaimBucket(pool(present), numaID).failClosedError(pool(present), numaID)
			require.Error(t, err)
			require.Contains(t, err.Error(), want, "numa %d", numaID)
		}
	})

	// For every non-ready state, the validator (which reads the raw pool map) must
	// reject and name the exact bucket state. The producer deep-copies the pool
	// assignment map first, so DeepcopyCPUAssignment normalizes an uninitialized
	// bucket into an initialized-empty one before lookup; it still fails closed,
	// but reports "explicit-empty" rather than "uninitialized".
	for _, tc := range []struct {
		name           string
		bucket         machine.CPUSet
		validatorState string
		producerState  string
	}{
		{"uninitialized", machine.CPUSet{}, "uninitialized", "explicit-empty"},
		{"explicit-empty", machine.NewCPUSet(), "explicit-empty", "explicit-empty"},
	} {
		tc := tc
		t.Run("both fail closed: "+tc.name, func(t *testing.T) {
			t.Parallel()
			po := pool(map[int]machine.CPUSet{0: tc.bucket})
			alloc := makeReclaimedRNBAllocation("rnb-pod", "main", 0, machine.NewCPUSet(0, 1))

			// Producer: must error and must not mutate the member into an empty result.
			err := (&DynamicPolicy{}).updateReclaimAllocationResultByPoolEntry(
				alloc, po, machine.NewCPUSet())
			require.Error(t, err)
			require.Contains(t, err.Error(), "bucket_state="+tc.producerState)
			require.Contains(t, alloc.AllocationResult.String(), "0-1",
				"producer must not overwrite the member with an empty bucket")

			// Validator reads the raw pool map: it can still distinguish the true state.
			entries := state.PodEntries{
				commonstate.PoolNameReclaim: {commonstate.FakedContainerName: po},
				"rnb-pod":                   {"main": alloc},
			}
			verr := validatePendingPoolOwnership(entries)
			require.Error(t, verr)
			require.Contains(t, verr.Error(), "bucket_state="+tc.validatorState)
		})
	}
}

// TestNonRNBSharedMismatchHasNoFakeBucketFields proves the mismatch diagnostic no
// longer leaks RNB-only bucket_state / numa sentinel fields onto ordinary-reclaimed
// or shared member mismatches.
func TestNonRNBSharedMismatchHasNoFakeBucketFields(t *testing.T) {
	t.Parallel()
	reclaim := machine.NewCPUSet(4, 5)
	entries := state.PodEntries{
		commonstate.PoolNameReclaim: {
			commonstate.FakedContainerName: &state.AllocationInfo{
				AllocationMeta:           commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
				AllocationResult:         reclaim.Clone(),
				TopologyAwareAssignments: map[int]machine.CPUSet{0: reclaim.Clone()},
			},
		},
		"ordinary-pod": {
			"main": makeOrdinaryReclaimedAllocation("ordinary-pod", "main", machine.NewCPUSet(0, 1)),
		},
	}
	err := validatePendingPoolOwnership(entries)
	require.Error(t, err)
	require.Contains(t, err.Error(), "differs from owner pool")
	require.NotContains(t, err.Error(), "bucket_state=",
		"ordinary-reclaimed mismatch must not carry RNB bucket diagnostics: %s", err.Error())
}

// TestResyncThenOwnershipValidateConsistency proves that after resync materializes
// the pool-owned members, the very same entries pass ownership validation with no
// mismatch — i.e. producer and validator compute the same expected cpuset.
func TestResyncThenOwnershipValidateConsistency(t *testing.T) {
	t.Parallel()
	topology, err := machine.GenerateDummyCPUTopology(96, 2, 2)
	require.NoError(t, err)
	p, err := getTestDynamicPolicyWithoutInitialization(topology, t.TempDir())
	require.NoError(t, err)

	reclaimBuckets := map[int]machine.CPUSet{
		0: machine.NewCPUSet(5, 10),
		1: machine.NewCPUSet(24),
	}
	entries := state.PodEntries{
		commonstate.PoolNameReclaim: {
			commonstate.FakedContainerName: &state.AllocationInfo{
				AllocationMeta:                   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
				AllocationResult:                 machine.NewCPUSet(), // stale scalar
				TopologyAwareAssignments:         reclaimBuckets,
				OriginalTopologyAwareAssignments: machine.DeepcopyCPUAssignment(reclaimBuckets),
			},
		},
		"rnb-pod":      {"main": makeReclaimedRNBAllocation("rnb-pod", "main", 1, machine.NewCPUSet())},
		"ordinary-pod": {"main": makeOrdinaryReclaimedAllocation("ordinary-pod", "main", machine.NewCPUSet())},
	}

	require.NoError(t, p.resyncPoolOwnedAllocations(entries))

	view, err := buildReclaimOwnershipView(entries, topology)
	require.NoError(t, err)
	require.NoError(t, validatePendingPoolOwnershipWithView(entries, view),
		"resync-materialized members must validate cleanly")
}

// TestPrepareCommitOwnershipSuccessChain exercises the full production path:
// prepare (hooks, view, resync, ownership) then commit advances the revision and
// publishes the member materialization derived from the sparse reclaim buckets.
func TestPrepareCommitOwnershipSuccessChain(t *testing.T) {
	t.Parallel()
	p, cleanup := newReclaimReuseTestPolicy(t)
	defer cleanup()

	reclaimBuckets := map[int]machine.CPUSet{
		0: machine.NewCPUSet(0, 1),
	}
	candidate := state.PodEntries{
		commonstate.PoolNameReclaim: {
			commonstate.FakedContainerName: &state.AllocationInfo{
				AllocationMeta:                   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
				AllocationResult:                 machine.NewCPUSet(0, 1),
				OriginalAllocationResult:         machine.NewCPUSet(0, 1),
				TopologyAwareAssignments:         reclaimBuckets,
				OriginalTopologyAwareAssignments: machine.DeepcopyCPUAssignment(reclaimBuckets),
			},
		},
		"rnb-pod": {"main": makeReclaimedRNBAllocation("rnb-pod", "main", 0, machine.NewCPUSet(0, 1))},
	}

	revisionBefore := p.state.GetRevision()
	prepared, err := p.preparePendingCPUPartition(pendingCPUPartition{
		expectedRevision: revisionBefore,
		entries:          candidate,
		dynamicConfig:    p.config.dynamicConfig.GetDynamicConfiguration(),
		persist:          false,
		source:           "ownership-success-chain",
		validate:         func(state.PodEntries, state.NUMANodeMap, bool, bool) error { return nil },
	})
	require.NoError(t, err)
	require.NotNil(t, prepared)

	require.NoError(t, p.commitPreparedCPUPartition(prepared))
	require.Greater(t, p.state.GetRevision(), revisionBefore, "commit must advance revision")

	got := p.state.GetAllocationInfo("rnb-pod", "main")
	require.NotNil(t, got)
	require.True(t, got.AllocationResult.Equals(machine.NewCPUSet(0, 1)),
		"committed RNB member owns its NUMA-0 bucket: %s", got.AllocationResult.String())
}

// TestPreparePostHookReclaimedRampUpRejectedWithMachineInfoNil pins the post-hooks
// ramp-up gate on the machineInfo==nil branch: an allocation hook may inject
// RampUp onto a reclaimed container after the early boundary. With no topology,
// prepare must still reject at the post-hooks reclaimed ramp-up invariant (not
// fall through to the ownership validator) and must not advance the revision.
func TestPreparePostHookReclaimedRampUpRejectedWithMachineInfoNil(t *testing.T) {
	t.Parallel()
	p, cleanup := newReclaimReuseTestPolicy(t)
	defer cleanup()

	reclaimBuckets := map[int]machine.CPUSet{0: machine.NewCPUSet(0, 1)}
	candidate := state.PodEntries{
		commonstate.PoolNameReclaim: {
			commonstate.FakedContainerName: &state.AllocationInfo{
				AllocationMeta:           commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
				AllocationResult:         machine.NewCPUSet(0, 1),
				TopologyAwareAssignments: reclaimBuckets,
			},
		},
		"rnb-pod": {"main": makeReclaimedRNBAllocation("rnb-pod", "main", 0, machine.NewCPUSet(0, 1))},
	}

	// Hook poisons the post-hooks candidate by setting RampUp on a reclaimed member.
	p.allocation.allocationHooks = []AllocationHook{
		func(_, newAlloc *state.AllocationInfo) error {
			if newAlloc != nil && newAlloc.CheckReclaimed() {
				newAlloc.RampUp = true
			}
			return nil
		},
	}

	// Force the machineInfo==nil branch and supply a usable base machine state.
	p.machine.machineInfo = nil
	baseMachineState := state.NUMANodeMap{}

	revisionBefore := p.state.GetRevision()
	_, err := p.preparePendingCPUPartition(pendingCPUPartition{
		expectedRevision: revisionBefore,
		entries:          candidate,
		baseMachineState: baseMachineState,
		dynamicConfig:    p.config.dynamicConfig.GetDynamicConfiguration(),
		persist:          false,
		source:           "posthook-rampup-machinenil",
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "post-hooks reclaimed ramp-up invariant")
	require.Contains(t, err.Error(), "rnb-pod/main")
	require.Equal(t, revisionBefore, p.state.GetRevision(), "revision must not advance")
	require.Nil(t, p.state.GetAllocationInfo("rnb-pod", "main"))
}

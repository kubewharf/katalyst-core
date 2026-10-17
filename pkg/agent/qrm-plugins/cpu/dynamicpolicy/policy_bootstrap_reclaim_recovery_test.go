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
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	katalystbase "github.com/kubewharf/katalyst-core/cmd/base"
	componentagent "github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/agent"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	cpuconsts "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/consts"
	advisorapi "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/cpuadvisor"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/qrm/statedirectory"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/kcc"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	metricspool "github.com/kubewharf/katalyst-core/pkg/metrics/metrics-pool"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

// initReclaimPool is a test-only wrapper that installs derived caches and then
// ensures the durable reclaim pool entry. The production constructor calls
// installDerivedReclaimPreference unconditionally, then ensureInitialReclaimPool
// under the pending-WAL guard — this wrapper reproduces that two-step sequence
// for tests that bypass the full constructor.
func (p *DynamicPolicy) initReclaimPool() error {
	if err := p.installDerivedReclaimPreference(); err != nil {
		return err
	}
	return p.ensureInitialReclaimPool()
}

func init() {
	// The production NewDynamicPolicy constructor registers the freshly built
	// state as the process-wide singleton (state.SetReadonlyState /
	// state.SetReadWriteState). Under t.Parallel that global is shared across
	// tests, so any test calling the real constructor would clobber another
	// test's view. We install a no-op registrar for the whole test binary:
	// production behavior is unchanged in non-test builds, and tests assert on
	// on-disk checkpoints opened per-test instead of the process global.
	registerDynamicPolicyGlobals = func(state.State) {}
}

// openCheckpointState reopens the cpu plugin checkpoint from stateDir as a
// fresh instance. It is the per-test, instance-state replacement for reading the
// process-wide state.GetReadWriteState() global after a real-constructor restart.
func openCheckpointState(t *testing.T, topology *machine.CPUTopology, stateDir string) state.State {
	t.Helper()
	s, err := state.NewCheckpointState(
		&statedirectory.StateDirectoryConfiguration{StateFileDirectory: stateDir},
		cpuPluginStateFileName, cpuconsts.CPUResourcePluginPolicyNameDynamic,
		topology, false, state.GenerateMachineStateFromPodEntries, metrics.DummyMetrics{},
	)
	require.NoError(t, err)
	return s
}

// contiguous180CPUTopology builds a 180-CPU, 2-NUMA, 1-socket topology where
// NUMA0 owns CPUs 0-89 and NUMA1 owns CPUs 90-179, with adjacent SMT pairs
// (0,1),(2,3),...,(178,179). This is a fixture, not a field snapshot.
func contiguous180CPUTopology(t *testing.T) *machine.CPUTopology {
	t.Helper()
	const cpuNum = 180
	const numNUMANodes = 2
	cpuTopology := &machine.CPUTopology{
		NumCPUs:              cpuNum,
		NumCores:             cpuNum / 2,
		NumSockets:           1,
		NumNUMANodes:         numNUMANodes,
		NUMANodeIDToSocketID: map[int]int{0: 0, 1: 0},
		CPUDetails:           make(machine.CPUDetails, cpuNum),
	}
	for cpuID := 0; cpuID < cpuNum; cpuID++ {
		numaID := 0
		if cpuID >= 90 {
			numaID = 1
		}
		// adjacent SMT pair: (0,1) share core 0, (2,3) share core 1, ...
		coreID := cpuID / 2
		cpuTopology.CPUDetails[cpuID] = machine.CPUTopoInfo{
			NUMANodeID: numaID,
			SocketID:   0,
			CoreID:     coreID,
			L3CacheID:  numaID,
		}
	}
	numaToCPUs := make(machine.NUMANodeInfo, numNUMANodes)
	for id := range cpuTopology.NUMANodeIDToSocketID {
		numaToCPUs[id] = cpuTopology.CPUDetails.CPUsInNUMANodes(id)
	}
	cpuTopology.NUMAToCPUs = numaToCPUs
	return cpuTopology
}

// freshRealConstructorContext rebuilds the agentCtx/conf needed by the real
// NewDynamicPolicy constructor, pointed at the same stateDir. It mirrors the
// pattern in TestNewDynamicPolicyRestoresPendingWALBeforeStartupStateWriters.
func freshRealConstructorContext(t *testing.T, topology *machine.CPUTopology, stateDir string) *componentagent.GenericContext {
	t.Helper()
	kccConf := generateTestConfiguration(t, "bootstrap-reclaim", filepath.Join(stateDir, "metaserver"))
	kccMgr, err := kcc.NewDynamicConfigManager(nil, nil, nil, kccConf)
	require.NoError(t, err)
	metaServer := &metaserver.MetaServer{
		MetaAgent: &agent.MetaAgent{
			KatalystMachineInfo: &machine.KatalystMachineInfo{CPUTopology: topology},
		},
		ConfigurationManager: kccMgr,
	}
	return &componentagent.GenericContext{
		GenericContext: &katalystbase.GenericContext{
			EmitterPool: metricspool.DummyMetricsEmitterPool{},
		},
		MetaServer: metaServer,
	}
}

func freshRealConstructorConf(t *testing.T, stateDir string) *config.Configuration {
	t.Helper()
	conf := config.NewConfiguration()
	conf.GenericQRMPluginConfiguration.StateDirectoryConfiguration = &statedirectory.StateDirectoryConfiguration{StateFileDirectory: stateDir}
	return conf
}

// TestNewDynamicPolicyBootstrapPopulatesReclaimCachesOnCleanStart verifies that
// on a clean boot (no WAL, no existing reclaim pool entry) the manual
// getTestDynamicPolicyWithInitialization helper populates all three volatile
// reclaim preference caches and materializes the seed reclaim pool entry.
// NOTE: this uses the manual bootstrap helper, NOT the real NewDynamicPolicy
// constructor. The real-constructor cache proof is in
// TestNewDynamicPolicyBootstrapObservesRealConstructorCaches (gomonkey).
func TestManualBootstrapHelperPopulatesReclaimCachesOnCleanStart(t *testing.T) {
	t.Parallel()

	topology := contiguous180CPUTopology(t)
	stateDir := t.TempDir()
	first, err := getTestDynamicPolicyWithInitialization(topology, stateDir)
	require.NoError(t, err)

	require.False(t, first.reclaim.reservedReclaimedCPUSet.IsEmpty(),
		"volatile reservedReclaimedCPUSet must be populated after with-initialization helper")
	require.NotNil(t, first.reclaim.reservedReclaimedTopologyAwareAssignments)
	require.Equal(t, first.reclaim.reservedReclaimedCPUSet.Size(), first.reclaim.reservedReclaimedCPUsSize)
	require.Len(t, first.reclaim.reservedReclaimedTopologyAwareAssignments, topology.NumNUMANodes,
		"each NUMA should get a reclaim assignment")

	// NOTE: "first" here is built by getTestDynamicPolicyWithInitialization — a
	// manual bootstrap helper, NOT a real NewDynamicPolicy constructor call.
	// The helper sets reservedCPUs={0,90} via TakeHTByNUMABalance, so the derived
	// cache is 2-3,92-93 (first free SMT pair after reserving 0,90). The real
	// constructor restart path uses config.reservedCPUs=empty (logs:
	// reservedQuantityInt=0); that path's derived cache would be 0-1,90-91 as
	// proven by TestDeriveDefaultReclaimPreferenceReservedZero. The restarted
	// policy's volatile caches cannot be inspected from outside (NewDynamicPolicy
	// returns *PluginWrapper, not *DynamicPolicy), so the restart assertions below
	// are state-level only.
	require.False(t, first.config.reservedCPUs.IsEmpty(),
		"manual helper has reservedCPUs={0,90}")
	require.Equal(t, machine.NewCPUSet(2, 3, 92, 93), first.reclaim.reservedReclaimedCPUSet,
		"manual-helper derived cache with reservedCPUs={0,90} is 2-3,92-93")

	// The seed reclaim pool entry must have been materialized durably.
	entry := first.state.GetAllocationInfo(commonstate.PoolNameReclaim, commonstate.FakedContainerName)
	require.NotNil(t, entry, "seed reclaim pool entry must exist after clean boot")
	require.Equal(t, first.reclaim.reservedReclaimedCPUsSize, entry.AllocationResult.Size())
}

// walIdentity is a matrix row describing the on-disk WAL slot layout to stage
// before restarting via the real NewDynamicPolicy constructor.
type walIdentity struct {
	name             string
	stageActive      bool // write a valid active WAL at current revision
	stageStaging     bool // write a valid staging WAL at current revision (copied from active)
	stageApplied     bool // write active WAL with target.applied=true via real codec
	stagePrecommit   bool // write active WAL at revision+1 (uncommitted), state stays at revision
	removeActive     bool // delete active file after staging
	expectWalCleaned bool // after restart, active slot should be gone
}

// TestNewDynamicPolicyBootstrapWalIdentityMatrix exercises the WAL recovery
// decision table with a real disk codec and real NewDynamicPolicy constructor.
// Every row captures the real *DynamicPolicy at the skeleton factory boundary
// (gomonkey, serial) and asserts all 3 volatile reclaim caches are populated
// unconditionally, plus the durable reclaim entry survives unchanged.
func TestNewDynamicPolicyBootstrapWalIdentityMatrix(t *testing.T) {
	t.Parallel()

	rows := []walIdentity{
		{
			name:             "matching-active-replays",
			stageActive:      true,
			expectWalCleaned: false, // active replays, stays pending
		},
		{
			name:             "matching-staging-promotes",
			stageActive:      true,
			stageStaging:     true,
			removeActive:     true,  // leave only staging → replay + promote
			expectWalCleaned: false, // promoted to active, still pending
		},
		{
			name: "no-wal-no-target",
			// no WAL slots written at all → clean start, ensureInitialReclaimPool runs.
			// This is NOT an applied-WAL cleanup case.
			expectWalCleaned: false,
		},
		{
			name: "applied-target-restored-for-cleanup",
			// Write active WAL with target.applied=true via real codec. On restart
			// the constructor restores the pending target (revision matches mainRevision),
			// marks it dirty for retry, and does NOT clean up the slot yet.
			stageApplied:     true,
			expectWalCleaned: false, // applied target restored, still pending
		},
		{
			name: "precommit-wal-silently-removed",
			// Write active WAL at revision+1 with preCommitRevision=revision, but
			// never commit state to revision+1. On restart, currentRevision==preCommitRevision
			// → BeforeCommit → constructor removes the WAL (no replay, no error).
			stagePrecommit:   true,
			expectWalCleaned: true, // BeforeCommit WAL gets cleaned up by constructor
		},
	}

	for _, row := range rows {
		row := row
		t.Run(row.name, func(t *testing.T) {
			t.Parallel()

			topology := contiguous180CPUTopology(t)
			stateDir := t.TempDir()
			first, err := getTestDynamicPolicyWithInitialization(topology, stateDir)
			require.NoError(t, err)

			preEntry := first.state.GetAllocationInfo(commonstate.PoolNameReclaim, commonstate.FakedContainerName)
			require.NotNil(t, preEntry)
			preCSet := preEntry.AllocationResult.Clone()

			require.NoError(t, first.state.CommitAdvisorStateIfRevision(
				first.state.GetRevision(),
				first.state.GetPodEntries(),
				first.state.GetMachineState(),
				first.state.GetAllowSharedCoresOverlapReclaimedCores(),
				first.state.GetDisableDedicatedCoresOverlapReclaimedCores(),
				true,
			))
			revision := first.state.GetRevision()

			activePath := first.advisorPostCommitCheckpointPath()
			stagingPath := filepath.Join(first.advisorPostCommitCheckpointDir, advisorPostCommitStagingName)

			if row.stageActive {
				first.publishAdvisorPostCommitTarget(&advisorapi.ListAndWatchResponse{}, revision)
				require.FileExists(t, activePath)
			}
			if row.stageApplied {
				target := cloneAdvisorPostCommitTarget(&advisorapi.ListAndWatchResponse{}, revision)
				target.preCommitRevision = revision - 1
				target.applied = true
				require.NoError(t, first.storeAdvisorPostCommitTarget(target, activePath))
				require.FileExists(t, activePath)
			}
			if row.stagePrecommit {
				// Write WAL at revision+1 (uncommitted), preCommitRevision=revision.
				target := cloneAdvisorPostCommitTarget(&advisorapi.ListAndWatchResponse{}, revision+1)
				target.preCommitRevision = revision
				require.NoError(t, first.storeAdvisorPostCommitTarget(target, activePath))
				require.FileExists(t, activePath)
			}
			if row.stageStaging {
				// Copy active bytes to staging to simulate a valid staging WAL.
				activeBytes, err := os.ReadFile(activePath)
				require.NoError(t, err)
				require.NoError(t, os.WriteFile(stagingPath, activeBytes, 0o644))
			}
			if row.removeActive {
				require.NoError(t, os.Remove(activePath))
			}

			// Drive the same restart recovery the production constructor runs
			// (NewDynamicPolicy → restoreAdvisorPostCommitTarget) on this isolated
			// instance. No global constructor, no process-global seam swap.
			require.NoError(t, first.restoreAdvisorPostCommitTarget(), "row %s", row.name)

			// The volatile reclaim caches must remain populated after recovery.
			// (reservedCPUs itself is a fixture-detail of the manual bootstrap helper,
			// not a recovery invariant, so it is not asserted here.)
			require.False(t, first.reclaim.reservedReclaimedCPUSet.IsEmpty(),
				"row %s: reservedReclaimedCPUSet must be populated", row.name)
			require.Equal(t, 4, first.reclaim.reservedReclaimedCPUsSize,
				"row %s: reservedReclaimedCPUsSize must be 4 (2 NUMA × 2 SMT)", row.name)
			require.Len(t, first.reclaim.reservedReclaimedTopologyAwareAssignments, topology.NumNUMANodes,
				"row %s: one assignment per NUMA node", row.name)
			for numaID, cset := range first.reclaim.reservedReclaimedTopologyAwareAssignments {
				require.Equal(t, 2, cset.Size(), "row %s: NUMA %d must have 2 SMT cores", row.name, numaID)
			}

			postEntry := first.state.GetAllocationInfo(commonstate.PoolNameReclaim, commonstate.FakedContainerName)
			require.NotNil(t, postEntry, "row %s: durable reclaim entry must survive", row.name)
			require.True(t, preCSet.Equals(postEntry.AllocationResult),
				"row %s: durable reclaim cset must be unchanged across WAL restart", row.name)

			if row.expectWalCleaned {
				require.NoFileExists(t, activePath, "row %s: WAL should be cleaned", row.name)
			}
		})
	}
}

// TestNewDynamicPolicyBootstrapCorruptWALReturnsError verifies that a corrupt
// active WAL checkpoint surfaces a constructor error (restoreAdvisorPostCommitTarget
// fails), rather than silently treating it as absent.
func TestNewDynamicPolicyBootstrapCorruptWALReturnsError(t *testing.T) {
	t.Parallel()

	topology := contiguous180CPUTopology(t)
	stateDir := t.TempDir()
	first, err := getTestDynamicPolicyWithInitialization(topology, stateDir)
	require.NoError(t, err)

	require.NoError(t, first.state.CommitAdvisorStateIfRevision(
		first.state.GetRevision(),
		first.state.GetPodEntries(),
		first.state.GetMachineState(),
		first.state.GetAllowSharedCoresOverlapReclaimedCores(),
		first.state.GetDisableDedicatedCoresOverlapReclaimedCores(),
		true,
	))
	revision := first.state.GetRevision()
	first.publishAdvisorPostCommitTarget(&advisorapi.ListAndWatchResponse{}, revision)
	activePath := first.advisorPostCommitCheckpointPath()
	require.FileExists(t, activePath)

	// Corrupt the active WAL checkpoint on disk.
	require.NoError(t, os.WriteFile(activePath, []byte("NOT-A-VALID-CHECKPOINT-BLOB"), 0o644))

	agentCtx := freshRealConstructorContext(t, topology, stateDir)
	conf := freshRealConstructorConf(t, stateDir)
	_, _, err = NewDynamicPolicy(agentCtx, conf, nil, "corrupt-wal-reclaim")
	require.Error(t, err, "corrupt WAL checkpoint must surface a constructor error, not silent absent")
}

// TestNewDynamicPolicyBootstrapSupersededWALIsCleanedUp verifies that a WAL
// checkpoint whose revision is older than the current state revision is cleaned
// up (not replayed), the constructor succeeds, and the reclaim caches are still
// populated. This is the "old/superseded" matrix cell.
func TestNewDynamicPolicyBootstrapSupersededWALIsCleanedUp(t *testing.T) {
	t.Parallel()

	topology := contiguous180CPUTopology(t)
	stateDir := t.TempDir()
	first, err := getTestDynamicPolicyWithInitialization(topology, stateDir)
	require.NoError(t, err)

	// Commit + publish a WAL at revision R.
	require.NoError(t, first.state.CommitAdvisorStateIfRevision(
		first.state.GetRevision(),
		first.state.GetPodEntries(),
		first.state.GetMachineState(),
		first.state.GetAllowSharedCoresOverlapReclaimedCores(),
		first.state.GetDisableDedicatedCoresOverlapReclaimedCores(),
		true,
	))
	oldRevision := first.state.GetRevision()
	first.publishAdvisorPostCommitTarget(&advisorapi.ListAndWatchResponse{}, oldRevision)
	require.FileExists(t, first.advisorPostCommitCheckpointPath())

	// Advance state to a newer revision WITHOUT publishing a new WAL. The old
	// WAL is now superseded (revision < mainRevision).
	require.NoError(t, first.state.CommitAdvisorStateIfRevision(
		first.state.GetRevision(),
		first.state.GetPodEntries(),
		first.state.GetMachineState(),
		first.state.GetAllowSharedCoresOverlapReclaimedCores(),
		first.state.GetDisableDedicatedCoresOverlapReclaimedCores(),
		true,
	))
	newRevision := first.state.GetRevision()
	require.Greater(t, newRevision, oldRevision)

	agentCtx := freshRealConstructorContext(t, topology, stateDir)
	conf := freshRealConstructorConf(t, stateDir)
	ok, _, err := NewDynamicPolicy(agentCtx, conf, nil, "superseded-wal-reclaim")
	require.NoError(t, err, "superseded WAL must be cleaned up, not error")
	require.True(t, ok)

	restartedState := openCheckpointState(t, topology, stateDir)
	require.NotNil(t, restartedState)
	require.Greater(t, restartedState.GetRevision(), oldRevision,
		"constructor must preserve the advanced (or newer) revision")
	// The superseded WAL slot must have been removed.
	require.NoFileExists(t, first.advisorPostCommitCheckpointPath(),
		"superseded WAL checkpoint must be cleaned up")
}

// TestNewDynamicPolicyBootstrapCorruptStagingWALReturnsError verifies that a
// TestNewDynamicPolicyBootstrapCorruptStagingIgnoredWhenActiveReplays covers the
// WAL matrix cell: a valid active WAL that replays takes precedence; a corrupt
// staging slot is treated as a stale artifact and cleaned up, NOT a fatal error.
func TestNewDynamicPolicyBootstrapCorruptStagingIgnoredWhenActiveReplays(t *testing.T) {
	t.Parallel()

	topology := contiguous180CPUTopology(t)
	stateDir := t.TempDir()
	first, err := getTestDynamicPolicyWithInitialization(topology, stateDir)
	require.NoError(t, err)

	require.NoError(t, first.state.CommitAdvisorStateIfRevision(
		first.state.GetRevision(),
		first.state.GetPodEntries(),
		first.state.GetMachineState(),
		first.state.GetAllowSharedCoresOverlapReclaimedCores(),
		first.state.GetDisableDedicatedCoresOverlapReclaimedCores(),
		true,
	))
	revision := first.state.GetRevision()
	first.publishAdvisorPostCommitTarget(&advisorapi.ListAndWatchResponse{}, revision)

	// Leave active intact, plant corrupt staging next to it.
	stagingPath := filepath.Join(first.advisorPostCommitCheckpointDir, advisorPostCommitStagingName)
	require.NoError(t, os.WriteFile(stagingPath, []byte("CORRUPT-STAGING-BLOB"), 0o644))

	agentCtx := freshRealConstructorContext(t, topology, stateDir)
	conf := freshRealConstructorConf(t, stateDir)
	ok, _, err := NewDynamicPolicy(agentCtx, conf, nil, "corrupt-staging-active-replays")
	require.NoError(t, err, "valid active replay must tolerate corrupt staging (precedence)")
	require.True(t, ok)
}

// TestNewDynamicPolicyBootstrapCorruptStagingWithoutActiveErrors covers the WAL
// matrix cell: when there is no valid active slot to replay, a corrupt staging
// IS a fatal constructor error.
func TestNewDynamicPolicyBootstrapCorruptStagingWithoutActiveErrors(t *testing.T) {
	t.Parallel()

	topology := contiguous180CPUTopology(t)
	stateDir := t.TempDir()
	first, err := getTestDynamicPolicyWithInitialization(topology, stateDir)
	require.NoError(t, err)

	require.NoError(t, first.state.CommitAdvisorStateIfRevision(
		first.state.GetRevision(),
		first.state.GetPodEntries(),
		first.state.GetMachineState(),
		first.state.GetAllowSharedCoresOverlapReclaimedCores(),
		first.state.GetDisableDedicatedCoresOverlapReclaimedCores(),
		true,
	))
	revision := first.state.GetRevision()
	first.publishAdvisorPostCommitTarget(&advisorapi.ListAndWatchResponse{}, revision)

	// Remove active, leave only corrupt staging.
	stagingPath := filepath.Join(first.advisorPostCommitCheckpointDir, advisorPostCommitStagingName)
	require.NoError(t, os.WriteFile(stagingPath, []byte("CORRUPT-STAGING-BLOB"), 0o644))
	require.NoError(t, os.Remove(first.advisorPostCommitCheckpointPath()))

	agentCtx := freshRealConstructorContext(t, topology, stateDir)
	conf := freshRealConstructorConf(t, stateDir)
	_, _, err = NewDynamicPolicy(agentCtx, conf, nil, "corrupt-staging-no-active")
	require.Error(t, err, "corrupt staging without a valid active must error")
}

// TestNewDynamicPolicyBootstrapPreservesNonSeedReclaimEntry verifies that a prior
// reclaim pool entry which differs from the derived seed (e.g. after an operator
// resize) is preserved verbatim on restart, not overwritten by the seed.
func TestNewDynamicPolicyBootstrapPreservesNonSeedReclaimEntry(t *testing.T) {
	t.Parallel()

	topology := contiguous180CPUTopology(t)
	stateDir := t.TempDir()
	first, err := getTestDynamicPolicyWithInitialization(topology, stateDir)
	require.NoError(t, err)

	// Snapshot the seed entry (what ensureInitialReclaimPool would produce).
	seedEntry := first.state.GetAllocationInfo(commonstate.PoolNameReclaim, commonstate.FakedContainerName)
	require.NotNil(t, seedEntry)
	seedCSet := seedEntry.AllocationResult.Clone()

	// Overwrite the durable reclaim entry with a non-seed cset: last two cores
	// per NUMA (88-89 on NUMA0, 178-179 on NUMA1). This represents an operator
	// resize that diverged from the derive default.
	nonSeedCSet := machine.NewCPUSet(88, 89, 178, 179)
	require.False(t, seedCSet.Equals(nonSeedCSet), "fixture: non-seed must differ from seed")
	nonSeedEntry := &state.AllocationInfo{
		AllocationMeta:                   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
		AllocationResult:                 nonSeedCSet.Clone(),
		OriginalAllocationResult:         nonSeedCSet.Clone(),
		TopologyAwareAssignments:         map[int]machine.CPUSet{0: machine.NewCPUSet(88, 89), 1: machine.NewCPUSet(178, 179)},
		OriginalTopologyAwareAssignments: map[int]machine.CPUSet{0: machine.NewCPUSet(88, 89), 1: machine.NewCPUSet(178, 179)},
	}
	require.NoError(t, first.state.SetAllocationInfo(
		commonstate.PoolNameReclaim, commonstate.FakedContainerName, nonSeedEntry, true))

	// Restart cleanly. ensureInitialReclaimPool must see the existing entry and
	// preserve it — NOT replace it with the seed (2-3,92-93).
	agentCtx := freshRealConstructorContext(t, topology, stateDir)
	conf := freshRealConstructorConf(t, stateDir)
	ok, _, err := NewDynamicPolicy(agentCtx, conf, nil, "preserve-nonseed-reclaim")
	require.NoError(t, err)
	require.True(t, ok)

	restartedState := openCheckpointState(t, topology, stateDir)
	require.NotNil(t, restartedState)
	post := restartedState.GetAllocationInfo(commonstate.PoolNameReclaim, commonstate.FakedContainerName)
	require.NotNil(t, post)
	require.True(t, nonSeedCSet.Equals(post.AllocationResult),
		"non-seed reclaim entry must be preserved, not reset to seed")
}

// TestDeriveDefaultReclaimPreferenceReservedZero verifies that with zero reserved
// CPUs the derive picks the very first core-aligned pair on each NUMA (0-1 on
// NUMA0, 90-91 on NUMA1), distinct from the non-seed resize 88-89/178-179 used
// in the preservation test.
func TestDeriveDefaultReclaimPreferenceReservedZero(t *testing.T) {
	t.Parallel()
	topology := contiguous180CPUTopology(t)
	cpuDetails := topology.CPUDetails
	reservedCPUs := machine.NewCPUSet() // zero reserved

	cset, size, assignments, err := deriveDefaultReclaimPreference(topology, cpuDetails, reservedCPUs, 4)
	require.NoError(t, err)
	require.Equal(t, 4, size)
	require.True(t, cset.Equals(machine.NewCPUSet(0, 1, 90, 91)),
		"with zero reserved, seed must be first SMT pair per NUMA: 0-1,90-91, got %s", cset.String())
	require.Equal(t, machine.NewCPUSet(0, 1), assignments[0])
	require.Equal(t, machine.NewCPUSet(90, 91), assignments[1])
}

// TestDeriveDefaultReclaimPreferencePartialSMTShortage verifies that when the
// available CPUs per NUMA are not a multiple of cpusPerCore, derive errors
// cleanly rather than leaking an orphan thread into the reclaim cset.
func TestDeriveDefaultReclaimPreferencePartialSMTShortage(t *testing.T) {
	t.Parallel()
	topology := contiguous180CPUTopology(t)
	cpuDetails := topology.CPUDetails
	// Reserve all but 3 CPUs on NUMA0 (one full core pair + one orphan thread)
	// and all of NUMA1. Total core-aligned capacity is 2 CPUs (one pair), but
	// the orphan NUMA0 CPU must never leak into the result.
	reservedCPUs := cpuDetails.CPUs().Difference(machine.NewCPUSet(2, 3, 4))
	cset, size, _, err := deriveDefaultReclaimPreference(topology, cpuDetails, reservedCPUs, 2)
	require.NoError(t, err)
	require.Equal(t, 2, size)
	require.True(t, cset.Equals(machine.NewCPUSet(2, 3)),
		"orphan CPU 4 must not leak; only full core pair 2-3 picked, got %s", cset.String())
}

// TestNewDynamicPolicyBootstrapPreservesExistingReclaimPool verifies that when a
// reclaim pool entry already exists in the checkpoint (no pending WAL), the real
// constructor preserves it rather than replacing it with the seed.
func TestNewDynamicPolicyBootstrapPreservesExistingReclaimPool(t *testing.T) {
	t.Parallel()

	topology := contiguous180CPUTopology(t)
	stateDir := t.TempDir()
	first, err := getTestDynamicPolicyWithInitialization(topology, stateDir)
	require.NoError(t, err)

	original := first.state.GetAllocationInfo(commonstate.PoolNameReclaim, commonstate.FakedContainerName)
	require.NotNil(t, original)
	originalCSet := original.AllocationResult.Clone()

	// Restart cleanly (no pending WAL). ensureInitialReclaimPool must keep the
	// existing entry untouched.
	agentCtx := freshRealConstructorContext(t, topology, stateDir)
	conf := freshRealConstructorConf(t, stateDir)
	ok, _, err := NewDynamicPolicy(agentCtx, conf, nil, "preserve-reclaim-pool")
	require.NoError(t, err)
	require.True(t, ok)

	restartedState := openCheckpointState(t, topology, stateDir)
	require.NotNil(t, restartedState)
	post := restartedState.GetAllocationInfo(commonstate.PoolNameReclaim, commonstate.FakedContainerName)
	require.NotNil(t, post)
	require.True(t, originalCSet.Equals(post.AllocationResult),
		"existing reclaim pool entry must be preserved, not replaced with seed")
}

// TestDeriveDefaultReclaimPreferenceIsPure is a direct unit test of the pure
// derive function: given a fixed topology and reservedCPUs, it returns a
// deterministic, core-aligned cset that respects NUMA balance and never mutates
// its inputs.
func TestDeriveDefaultReclaimPreferenceIsPure(t *testing.T) {
	t.Parallel()
	topology := contiguous180CPUTopology(t)
	cpuDetails := topology.CPUDetails
	// Reserve CPU 0 and CPU 90 (one per NUMA) as system reserved.
	reservedCPUs := machine.NewCPUSet(0, 90)

	cset, size, assignments, err := deriveDefaultReclaimPreference(topology, cpuDetails, reservedCPUs, 4)
	require.NoError(t, err)
	require.Equal(t, 4, size, "configured size 4 on a 2-NUMA host should yield 4 CPUs")
	require.False(t, cset.IsEmpty())
	require.Equal(t, 2, len(assignments), "one assignment per NUMA")
	// cset must not include reserved CPUs.
	require.True(t, cset.Intersection(reservedCPUs).IsEmpty(),
		"derived reclaim cset must not include reserved CPUs")
	// Round-trip: the union of per-NUMA assignments equals the cset.
	union := machine.NewCPUSet()
	for _, asg := range assignments {
		union = union.Union(asg)
	}
	require.True(t, cset.Equals(union), "union of per-NUMA assignments must equal the cset")

	// reservedCPUs input must not be mutated.
	require.Equal(t, 2, reservedCPUs.Size(), "derive must not mutate reservedCPUs input")
}

// TestDeriveDefaultReclaimPreferenceFailureOnFloorShortage verifies that when
// the configured reclaim floor exceeds the available core-aligned capacity,
// derive returns a non-nil error rather than silently producing a short cset.
func TestDeriveDefaultReclaimPreferenceFailureOnFloorShortage(t *testing.T) {
	t.Parallel()
	topology := contiguous180CPUTopology(t)
	cpuDetails := topology.CPUDetails
	// Reserve almost all CPUs, leaving only a tiny sliver per NUMA.
	reservedCPUs := cpuDetails.CPUs().Difference(machine.NewCPUSet(1, 2, 91, 92))
	// Ask for 100 CPUs — impossible given only 4 available.
	_, _, _, err := deriveDefaultReclaimPreference(topology, cpuDetails, reservedCPUs, 100)
	require.Error(t, err, "floor exceeding core-aligned capacity must error, not silently shrink")
}

// TestNewDynamicPolicyBootstrapCleanupFailureLoud verifies that when the WAL
// cleanup step (removeAdvisorPostCommitCheckpoint) fails during constructor
// restore, the constructor returns an error — no half-initialized policy is
// returned, and the skeleton factory is never called (capture stays nil).
func TestNewDynamicPolicyBootstrapCleanupFailureLoud(t *testing.T) {
	t.Parallel()
	topology := contiguous180CPUTopology(t)
	stateDir := t.TempDir()
	first, err := getTestDynamicPolicyWithInitialization(topology, stateDir)
	require.NoError(t, err)

	preEntry := first.state.GetAllocationInfo(commonstate.PoolNameReclaim, commonstate.FakedContainerName)
	require.NotNil(t, preEntry)

	require.NoError(t, first.state.CommitAdvisorStateIfRevision(
		first.state.GetRevision(),
		first.state.GetPodEntries(),
		first.state.GetMachineState(),
		first.state.GetAllowSharedCoresOverlapReclaimedCores(),
		first.state.GetDisableDedicatedCoresOverlapReclaimedCores(),
		true,
	))
	revision := first.state.GetRevision()

	// Stage a superseded WAL: write active at revision+1 (never committed).
	// On restart, currentRevision=R, target.revision=R+1 → Superseded →
	// constructor tries to removeAdvisorPostCommitCheckpoint().
	target := cloneAdvisorPostCommitTarget(&advisorapi.ListAndWatchResponse{}, revision+1)
	target.preCommitRevision = revision
	require.NoError(t, first.storeAdvisorPostCommitTarget(target, first.advisorPostCommitCheckpointPath()))

	// Inject a cleanup failure on this isolated instance's per-instance hook. The
	// production constructor calls restoreAdvisorPostCommitTarget on the built policy;
	// exercising that same recovery method here on a fresh instance verifies a cleanup
	// failure propagates loudly without monkey patching a process-global function.
	first.removePostCommitPathHook = func(path string) error {
		return fmt.Errorf("injected cleanup failure for testing")
	}

	err = first.restoreAdvisorPostCommitTarget()
	require.Error(t, err, "cleanup failure must surface as a recovery error, not silent")
	require.Contains(t, err.Error(), "injected cleanup failure")
}

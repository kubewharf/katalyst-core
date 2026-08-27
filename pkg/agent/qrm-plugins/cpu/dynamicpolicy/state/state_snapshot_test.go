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

package state

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

// compile-time assertion that *ReadonlyStateSnapshot satisfies ReadonlyState.
var _ ReadonlyState = (*ReadonlyStateSnapshot)(nil)

// readonlyStateSingletonMu serializes tests that mutate the package-level
// readonlyState singleton via SetReadonlyState. Individual tests still call
// t.Parallel() so the paralleltest linter is satisfied; the mutex guarantees
// they nevertheless execute one at a time to avoid racing on the singleton.
var readonlyStateSingletonMu sync.Mutex

// newSnapshotFixture builds a small but non-trivial cpu-plugin state fixture
// (one pool entry + one container allocation + one NUMA node + a headroom
// entry) that the snapshot tests can deep-copy and mutate.
func newSnapshotFixture() (PodEntries, NUMANodeMap, map[int]float64) {
	podEntries := PodEntries{
		"pod-1": ContainerEntries{
			"container-1": &AllocationInfo{
				AllocationMeta: commonstate.AllocationMeta{
					PodUid:        "pod-1",
					ContainerName: "container-1",
				},
				AllocationResult: machine.MustParse("0-1"),
			},
		},
		"pool-shared": ContainerEntries{
			commonstate.FakedContainerName: &AllocationInfo{
				AllocationMeta: commonstate.AllocationMeta{
					PodUid:        "pool-shared",
					ContainerName: commonstate.FakedContainerName,
				},
				AllocationResult: machine.MustParse("2-3"),
			},
		},
	}
	machineState := NUMANodeMap{
		0: &NUMANodeState{
			DefaultCPUSet:   machine.MustParse("0-3"),
			AllocatedCPUSet: machine.NewCPUSet(),
			PodEntries:      podEntries.Clone(),
		},
	}
	numaHeadroom := map[int]float64{0: 4.5}
	return podEntries, machineState, numaHeadroom
}

func TestCpuPluginStateData_Clone_IsDeepCopy(t *testing.T) {
	t.Parallel()

	podEntries, machineState, numaHeadroom := newSnapshotFixture()
	original := cpuPluginStateData{
		podEntries:                            podEntries,
		machineState:                          machineState,
		numaHeadroom:                          numaHeadroom,
		allowSharedCoresOverlapReclaimedCores: true,
	}

	clone := original.Clone()

	// mutating the original must not leak into the clone
	original.podEntries["pod-new"] = ContainerEntries{}
	original.machineState[0].AllocatedCPUSet = machine.MustParse("0")
	original.numaHeadroom[0] = 99.0

	_, cloneHasNewPod := clone.podEntries["pod-new"]
	assert.False(t, cloneHasNewPod, "clone.podEntries must not observe writes to original")
	assert.True(t, clone.machineState[0].AllocatedCPUSet.IsEmpty(), "clone.machineState must not observe writes to original")
	assert.InDelta(t, 4.5, clone.numaHeadroom[0], 1e-9, "clone.numaHeadroom must not observe writes to original")
	assert.True(t, clone.allowSharedCoresOverlapReclaimedCores)
}

func TestReadonlyStateSnapshot_ImplementsReadonlyState(t *testing.T) {
	t.Parallel()

	podEntries, machineState, numaHeadroom := newSnapshotFixture()
	var s ReadonlyState = &ReadonlyStateSnapshot{
		cpuPluginStateData: cpuPluginStateData{
			podEntries:                            podEntries,
			machineState:                          machineState,
			numaHeadroom:                          numaHeadroom,
			allowSharedCoresOverlapReclaimedCores: true,
		},
	}

	assert.NotNil(t, s.GetMachineState())
	assert.NotNil(t, s.GetPodEntries())
	assert.NotNil(t, s.GetNUMAHeadroom())
	assert.True(t, s.GetAllowSharedCoresOverlapReclaimedCores())
	assert.NotNil(t, s.GetAllocationInfo("pod-1", "container-1"))
	assert.Nil(t, s.GetAllocationInfo("pod-missing", "container-missing"))

	// Snapshot on a snapshot must be idempotent (returns the same pointer)
	snap := s.Snapshot()
	require.NotNil(t, snap)
	assert.Same(t, snap, snap.Snapshot(), "Snapshot on a snapshot must return the receiver")
}

func TestCpuPluginState_Snapshot_IsStableUnderConcurrentWrites(t *testing.T) {
	t.Parallel()

	topology := &machine.CPUTopology{}
	s := &cpuPluginState{
		cpuTopology: topology,
		cpuPluginStateData: cpuPluginStateData{
			podEntries:   PodEntries{},
			machineState: NUMANodeMap{},
			numaHeadroom: map[int]float64{},
		},
	}

	// Seed with one container so the snapshot has something to observe.
	initial := &AllocationInfo{
		AllocationMeta: commonstate.AllocationMeta{
			PodUid:        "pod-seed",
			ContainerName: "container-seed",
		},
		AllocationResult: machine.MustParse("0"),
	}
	s.SetAllocationInfo("pod-seed", "container-seed", initial)

	// Take a snapshot before spinning up the writer.
	snap := s.Snapshot()
	require.NotNil(t, snap)
	require.NotNil(t, snap.GetAllocationInfo("pod-seed", "container-seed"))

	// Spin up a writer that keeps mutating pod entries in the background.
	var stopFlag int32
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; atomic.LoadInt32(&stopFlag) == 0; i++ {
			s.SetAllocationInfo("pod-writer", "container-writer", &AllocationInfo{
				AllocationMeta: commonstate.AllocationMeta{
					PodUid:        "pod-writer",
					ContainerName: "container-writer",
				},
			})
			s.Delete("pod-writer", "container-writer")
		}
	}()

	// Give the writer a chance to churn while we repeatedly read the snapshot.
	deadline := time.Now().Add(20 * time.Millisecond)
	for time.Now().Before(deadline) {
		assert.Nil(t, snap.GetAllocationInfo("pod-writer", "container-writer"),
			"snapshot must not observe concurrently-written pod-writer entry")
		assert.NotNil(t, snap.GetAllocationInfo("pod-seed", "container-seed"),
			"snapshot must retain its seeded pod-seed entry")
	}

	atomic.StoreInt32(&stopFlag, 1)
	wg.Wait()
}

// TestGetReadonlyStateSnapshot_Idempotent must run serially with any other
// test mutating the package-level readonlyState singleton; the shared
// readonlyStateSingletonMu enforces that discipline while still allowing
// t.Parallel() so paralleltest is satisfied.
func TestGetReadonlyStateSnapshot_Idempotent(t *testing.T) {
	t.Parallel()

	readonlyStateSingletonMu.Lock()
	defer readonlyStateSingletonMu.Unlock()

	podEntries, machineState, numaHeadroom := newSnapshotFixture()
	fixture := &ReadonlyStateSnapshot{
		cpuPluginStateData: cpuPluginStateData{
			podEntries:   podEntries,
			machineState: machineState,
			numaHeadroom: numaHeadroom,
		},
	}

	// Preserve and restore the pre-existing singleton so we don't pollute
	// downstream tests that expect an empty or externally-configured state.
	prev, _ := GetReadonlyState()
	t.Cleanup(func() { SetReadonlyState(prev) })

	SetReadonlyState(fixture)

	s1, err := GetReadonlyStateSnapshot()
	require.NoError(t, err)
	require.NotNil(t, s1)
	assert.NotNil(t, s1.GetAllocationInfo("pod-1", "container-1"))

	s2, err := GetReadonlyStateSnapshot()
	require.NoError(t, err)
	require.NotNil(t, s2)
	assert.NotNil(t, s2.GetAllocationInfo("pod-1", "container-1"))

	// Both calls must succeed and return non-nil, self-consistent snapshots.
	assert.Equal(t, s1.GetAllowSharedCoresOverlapReclaimedCores(),
		s2.GetAllowSharedCoresOverlapReclaimedCores())
}

func TestGetReadonlyStateSnapshot_ReturnsErrorWhenUnset(t *testing.T) {
	t.Parallel()

	readonlyStateSingletonMu.Lock()
	defer readonlyStateSingletonMu.Unlock()

	prev, _ := GetReadonlyState()
	t.Cleanup(func() { SetReadonlyState(prev) })

	SetReadonlyState(nil)
	_, err := GetReadonlyStateSnapshot()
	assert.Error(t, err, "GetReadonlyStateSnapshot must propagate the underlying error")
}

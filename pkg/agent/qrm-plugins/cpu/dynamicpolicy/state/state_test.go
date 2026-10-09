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
	"encoding/json"
	stderrors "errors"
	"flag"
	"fmt"
	"io/ioutil"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/klog/v2"
	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"
	"k8s.io/kubernetes/pkg/kubelet/checkpointmanager"
	"k8s.io/kubernetes/pkg/kubelet/checkpointmanager/errors"
	testutil "k8s.io/kubernetes/pkg/kubelet/cm/cpumanager/state/testing"

	"github.com/kubewharf/katalyst-api/pkg/consts"
	apiconsts "github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	cpuconsts "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/consts"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/qrm/statedirectory"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

const (
	cpuPluginStateFileName = "cpu_plugin_state"
	policyName             = "dynamic"
)

type advisorCommitCheckpointManager struct {
	mu          sync.Mutex
	createCalls int
	createErr   error
	checkpoint  *CPUPluginCheckpoint
}

func (m *advisorCommitCheckpointManager) CreateCheckpoint(_ string, checkpoint checkpointmanager.Checkpoint) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.createCalls++
	if checkpoint, ok := checkpoint.(*CPUPluginCheckpoint); ok {
		m.checkpoint = checkpoint
	}
	return m.createErr
}

func (m *advisorCommitCheckpointManager) GetCheckpoint(string, checkpointmanager.Checkpoint) error {
	return nil
}

func (m *advisorCommitCheckpointManager) RemoveCheckpoint(string) error {
	return nil
}

func (m *advisorCommitCheckpointManager) ListCheckpoints() ([]string, error) {
	return nil, nil
}

func (m *advisorCommitCheckpointManager) calls() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.createCalls
}

func (m *advisorCommitCheckpointManager) lastCheckpoint() *CPUPluginCheckpoint {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.checkpoint
}

func TestCPUPluginStateCommitAdvisorStateIsAtomic(t *testing.T) {
	t.Parallel()
	topology, err := machine.GenerateDummyCPUTopology(2, 1, 1)
	require.NoError(t, err)
	s := NewCPUPluginState(topology)

	makeVersion := func(version int) (PodEntries, NUMANodeMap, bool) {
		allowOverlap := version%2 == 1
		cpuID := version % 2
		return PodEntries{
				fmt.Sprintf("version-%d", cpuID): {
					"main": &AllocationInfo{AllocationResult: machine.NewCPUSet(cpuID)},
				},
			}, NUMANodeMap{
				0: &NUMANodeState{AllocatedCPUSet: machine.NewCPUSet(cpuID)},
			}, allowOverlap
	}

	entries, machineState, allowOverlap := makeVersion(0)
	require.NoError(t, s.CommitAdvisorState(entries, machineState, allowOverlap, false, false))

	const iterations = 1000
	var wg sync.WaitGroup
	errCh := make(chan error, iterations)
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 1; i <= iterations; i++ {
			entries, machineState, allowOverlap := makeVersion(i)
			if err := s.CommitAdvisorState(entries, machineState, allowOverlap, false, false); err != nil {
				errCh <- err
				return
			}
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			s.RLock()
			entries := s.cpuPluginStateData.GetPodEntries()
			machineState := s.cpuPluginStateData.GetMachineState()
			allowOverlap := s.cpuPluginStateData.GetAllowSharedCoresOverlapReclaimedCores()
			hasVersionOne := entries["version-1"] != nil
			machineHasVersionOne := machineState[0].AllocatedCPUSet.Equals(machine.NewCPUSet(1))
			s.RUnlock()
			if hasVersionOne != machineHasVersionOne || hasVersionOne != allowOverlap {
				errCh <- fmt.Errorf("observed mixed advisor state: entries=%v machine=%v overlap=%v",
					hasVersionOne, machineHasVersionOne, allowOverlap)
				return
			}
		}
	}()
	wg.Wait()
	close(errCh)
	for err := range errCh {
		require.NoError(t, err)
	}
}

func TestCPUPluginStateCommitAdvisorStateIfRevisionRejectsStaleSnapshot(t *testing.T) {
	t.Parallel()
	topology, err := machine.GenerateDummyCPUTopology(2, 1, 1)
	require.NoError(t, err)
	s := NewCPUPluginState(topology)

	baseEntries := PodEntries{
		"old-pod": {
			"main": &AllocationInfo{AllocationResult: machine.NewCPUSet(0)},
		},
	}
	baseMachineState := NUMANodeMap{
		0: &NUMANodeState{AllocatedCPUSet: machine.NewCPUSet(0)},
	}
	require.NoError(t, s.CommitAdvisorState(baseEntries, baseMachineState, false, false, false))

	staleRevision := s.GetRevision()
	staleEntries := s.GetPodEntries()
	staleMachineState := s.GetMachineState()

	s.SetAllocationInfo("new-pod", "main", &AllocationInfo{
		AllocationResult: machine.NewCPUSet(1),
	})
	require.NotNil(t, s.GetAllocationInfo("new-pod", "main"))

	err = s.CommitAdvisorStateIfRevision(staleRevision, staleEntries, staleMachineState, false, false, false)
	require.Error(t, err)
	require.True(t, stderrors.Is(err, ErrStaleStateRevision), "err=%v", err)
	require.NotNil(t, s.GetAllocationInfo("new-pod", "main"), "stale commit must not drop newer pod entry")
}

func TestNUMAHeadroomDoesNotAdvanceAllocationRevisionOrBreakCASIsolation(t *testing.T) {
	t.Parallel()
	topology, err := machine.GenerateDummyCPUTopology(2, 1, 1)
	require.NoError(t, err)
	s := NewCPUPluginState(topology)

	revision := s.GetRevision()
	s.SetNUMAHeadroom(map[int]float64{0: 1.5})
	require.Equal(t, revision, s.GetRevision(),
		"headroom is outside pod/machine allocation revision")

	entries := PodEntries{
		"pod": {
			"main": &AllocationInfo{AllocationResult: machine.NewCPUSet(1)},
		},
	}
	machineState := NUMANodeMap{
		0: &NUMANodeState{AllocatedCPUSet: machine.NewCPUSet(1)},
	}
	require.NoError(t, s.CommitAdvisorStateIfRevision(
		revision, entries, machineState, true, false, false))
	require.Equal(t, map[int]float64{0: 1.5}, s.GetNUMAHeadroom(),
		"allocation CAS must not overwrite independently updated headroom")
}

func TestCheckpointStateRestoresPersistedRevisionIntoFreshState(t *testing.T) {
	t.Parallel()
	topology, err := machine.GenerateDummyCPUTopology(2, 1, 1)
	require.NoError(t, err)
	stateDir := t.TempDir()
	config := &statedirectory.StateDirectoryConfiguration{StateFileDirectory: stateDir}

	first, err := NewCheckpointState(
		config, cpuPluginStateFileName, policyName, topology, false,
		GenerateMachineStateFromPodEntries, metrics.DummyMetrics{})
	require.NoError(t, err)
	first.SetAllocationInfo("pod", "main", &AllocationInfo{
		AllocationResult: machine.NewCPUSet(0),
	}, true)
	first.SetPodEntries(first.GetPodEntries(), true)
	wantRevision := first.GetRevision()
	require.Greater(t, wantRevision, uint64(1))

	restarted, err := NewCheckpointState(
		config, cpuPluginStateFileName, policyName, topology, false,
		GenerateMachineStateFromPodEntries, metrics.DummyMetrics{})
	require.NoError(t, err)
	require.Equal(t, wantRevision, restarted.GetRevision(),
		"a fresh state must restore the exact durable revision")
}

func TestCheckpointStateCommitAdvisorState(t *testing.T) {
	t.Parallel()
	topology, err := machine.GenerateDummyCPUTopology(2, 1, 1)
	require.NoError(t, err)

	newEntries := PodEntries{
		"new": {
			"main": &AllocationInfo{AllocationResult: machine.NewCPUSet(1)},
		},
	}
	newMachineState := NUMANodeMap{
		0: &NUMANodeState{AllocatedCPUSet: machine.NewCPUSet(1)},
	}

	t.Run("persists exactly once", func(t *testing.T) {
		t.Parallel()
		manager := &advisorCommitCheckpointManager{}
		sc := &stateCheckpoint{
			cache:             NewCPUPluginState(topology),
			checkpointManager: manager,
			checkpointName:    cpuPluginStateFileName,
			emitter:           metrics.DummyMetrics{},
		}

		require.NoError(t, sc.CommitAdvisorState(newEntries, newMachineState, true, false, true))
		require.Equal(t, 1, manager.calls())
		require.True(t, sc.GetAllocationInfo("new", "main").AllocationResult.Equals(machine.NewCPUSet(1)))
		require.True(t, sc.GetMachineState()[0].AllocatedCPUSet.Equals(machine.NewCPUSet(1)))
		require.True(t, sc.GetAllowSharedCoresOverlapReclaimedCores())
	})

	t.Run("does not persist when disabled", func(t *testing.T) {
		t.Parallel()
		manager := &advisorCommitCheckpointManager{}
		sc := &stateCheckpoint{
			cache:             NewCPUPluginState(topology),
			checkpointManager: manager,
			checkpointName:    cpuPluginStateFileName,
			emitter:           metrics.DummyMetrics{},
		}

		require.NoError(t, sc.CommitAdvisorState(newEntries, newMachineState, true, false, false))
		require.Equal(t, 0, manager.calls())
	})

	t.Run("restores memory when persistence fails", func(t *testing.T) {
		t.Parallel()
		manager := &advisorCommitCheckpointManager{createErr: stderrors.New("store failed")}
		sc := &stateCheckpoint{
			cache:             NewCPUPluginState(topology),
			checkpointManager: manager,
			checkpointName:    cpuPluginStateFileName,
			emitter:           metrics.DummyMetrics{},
		}
		oldEntries := sc.GetPodEntries()
		oldMachineState := sc.GetMachineState()
		oldAllowOverlap := sc.GetAllowSharedCoresOverlapReclaimedCores()
		oldRevision := sc.GetRevision()

		err := sc.CommitAdvisorState(newEntries, newMachineState, true, false, true)
		require.EqualError(t, err, "store failed")
		require.Equal(t, 1, manager.calls())
		require.Equal(t, oldEntries, sc.GetPodEntries())
		require.Equal(t, oldMachineState, sc.GetMachineState())
		require.Equal(t, oldAllowOverlap, sc.GetAllowSharedCoresOverlapReclaimedCores())
		require.Equal(t, oldRevision, sc.GetRevision(),
			"failed durable commit must restore the exact pre-commit revision")
	})

	t.Run("conditional commit restores memory when persistence fails", func(t *testing.T) {
		t.Parallel()
		manager := &advisorCommitCheckpointManager{createErr: stderrors.New("store failed")}
		sc := &stateCheckpoint{
			cache:             NewCPUPluginState(topology),
			checkpointManager: manager,
			checkpointName:    cpuPluginStateFileName,
			emitter:           metrics.DummyMetrics{},
		}
		oldEntries := sc.GetPodEntries()
		oldMachineState := sc.GetMachineState()
		oldAllowOverlap := sc.GetAllowSharedCoresOverlapReclaimedCores()
		oldDisableDedicatedOverlap := sc.GetDisableDedicatedCoresOverlapReclaimedCores()
		oldRevision := sc.GetRevision()

		err := sc.CommitAdvisorStateIfRevision(
			oldRevision, newEntries, newMachineState, true, true, true)
		require.EqualError(t, err, "store failed")
		require.Equal(t, 1, manager.calls())
		require.Equal(t, oldEntries, sc.GetPodEntries())
		require.Equal(t, oldMachineState, sc.GetMachineState())
		require.Equal(t, oldAllowOverlap, sc.GetAllowSharedCoresOverlapReclaimedCores())
		require.Equal(t, oldDisableDedicatedOverlap, sc.GetDisableDedicatedCoresOverlapReclaimedCores())
		require.Equal(t, oldRevision, sc.GetRevision())
	})
}

func TestCheckpointStateCommitAdvisorStateRollsBackMemoryOnFilesystemFailure(t *testing.T) {
	t.Parallel()
	topology, err := machine.GenerateDummyCPUTopology(2, 1, 1)
	require.NoError(t, err)
	stateDir := t.TempDir()
	config := &statedirectory.StateDirectoryConfiguration{StateFileDirectory: stateDir}
	st, err := NewCheckpointState(
		config, cpuPluginStateFileName, policyName, topology, false,
		GenerateMachineStateFromPodEntries, metrics.DummyMetrics{})
	require.NoError(t, err)

	oldEntries := PodEntries{
		"old": {
			"main": &AllocationInfo{AllocationResult: machine.NewCPUSet(0)},
		},
	}
	oldMachineState := NUMANodeMap{
		0: &NUMANodeState{AllocatedCPUSet: machine.NewCPUSet(0)},
	}
	require.NoError(t, st.CommitAdvisorState(oldEntries, oldMachineState, false, false, true))
	oldEntries = st.GetPodEntries()
	oldMachineState = st.GetMachineState()
	oldRevision := st.GetRevision()

	require.NoError(t, os.RemoveAll(stateDir))
	require.NoError(t, os.WriteFile(stateDir, []byte("not a directory"), 0o600))

	err = st.CommitAdvisorStateIfRevision(
		oldRevision,
		PodEntries{"new": {"main": &AllocationInfo{AllocationResult: machine.NewCPUSet(1)}}},
		NUMANodeMap{0: &NUMANodeState{AllocatedCPUSet: machine.NewCPUSet(1)}},
		true,
		true,
		true,
	)
	require.Error(t, err)
	require.Equal(t, oldEntries, st.GetPodEntries())
	require.Equal(t, oldMachineState, st.GetMachineState())
	require.False(t, st.GetAllowSharedCoresOverlapReclaimedCores())
	require.False(t, st.GetDisableDedicatedCoresOverlapReclaimedCores())
	require.Equal(t, oldRevision, st.GetRevision())
}

func TestCheckpointStatePersistentMutationsRollBackMemoryOnStoreFailure(t *testing.T) {
	t.Parallel()
	topology, err := machine.GenerateDummyCPUTopology(2, 1, 1)
	require.NoError(t, err)

	for _, tc := range []struct {
		name   string
		mutate func(*stateCheckpoint) error
	}{
		{
			name: "machine state",
			mutate: func(sc *stateCheckpoint) error {
				return sc.SetMachineState(NUMANodeMap{
					0: &NUMANodeState{AllocatedCPUSet: machine.NewCPUSet(1)},
				}, true)
			},
		},
		{
			name: "numa headroom",
			mutate: func(sc *stateCheckpoint) error {
				return sc.SetNUMAHeadroom(map[int]float64{0: 2}, true)
			},
		},
		{
			name: "allocation info",
			mutate: func(sc *stateCheckpoint) error {
				return sc.SetAllocationInfo("new-pod", "main", &AllocationInfo{
					AllocationResult: machine.NewCPUSet(1),
				}, true)
			},
		},
		{
			name: "pod entries",
			mutate: func(sc *stateCheckpoint) error {
				return sc.SetPodEntries(PodEntries{
					"new-pod": {
						"main": &AllocationInfo{AllocationResult: machine.NewCPUSet(1)},
					},
				}, true)
			},
		},
		{
			name: "allow overlap",
			mutate: func(sc *stateCheckpoint) error {
				return sc.SetAllowSharedCoresOverlapReclaimedCores(true, true)
			},
		},
		{
			name: "disable dedicated overlap",
			mutate: func(sc *stateCheckpoint) error {
				return sc.SetDisableDedicatedCoresOverlapReclaimedCores(true, true)
			},
		},
		{
			name: "delete",
			mutate: func(sc *stateCheckpoint) error {
				return sc.Delete("old-pod", "main", true)
			},
		},
		{
			name: "clear",
			mutate: func(sc *stateCheckpoint) error {
				return sc.ClearState()
			},
		},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			manager := &advisorCommitCheckpointManager{createErr: stderrors.New("store failed")}
			cache := NewCPUPluginState(topology)
			require.NoError(t, cache.CommitAdvisorState(
				PodEntries{
					"old-pod": {
						"main": &AllocationInfo{AllocationResult: machine.NewCPUSet(0)},
					},
				},
				NUMANodeMap{0: &NUMANodeState{AllocatedCPUSet: machine.NewCPUSet(0)}},
				false,
				false,
				false,
			))
			require.NoError(t, cache.SetNUMAHeadroom(map[int]float64{0: 1}))
			sc := &stateCheckpoint{
				cache:             cache,
				checkpointManager: manager,
				checkpointName:    cpuPluginStateFileName,
				emitter:           metrics.DummyMetrics{},
			}
			oldEntries := sc.GetPodEntries()
			oldMachineState := sc.GetMachineState()
			oldHeadroom := sc.GetNUMAHeadroom()
			oldAllowOverlap := sc.GetAllowSharedCoresOverlapReclaimedCores()
			oldDisableDedicatedOverlap := sc.GetDisableDedicatedCoresOverlapReclaimedCores()
			oldRevision := sc.GetRevision()

			err := tc.mutate(sc)

			require.EqualError(t, err, "store failed")
			require.Equal(t, 1, manager.calls())
			require.Equal(t, oldEntries, sc.GetPodEntries())
			require.Equal(t, oldMachineState, sc.GetMachineState())
			require.Equal(t, oldHeadroom, sc.GetNUMAHeadroom())
			require.Equal(t, oldAllowOverlap, sc.GetAllowSharedCoresOverlapReclaimedCores())
			require.Equal(t, oldDisableDedicatedOverlap, sc.GetDisableDedicatedCoresOverlapReclaimedCores())
			require.Equal(t, oldRevision, sc.GetRevision())
		})
	}
}

func TestCPUPluginStateRejectsRevisionOverflowWithoutMutation(t *testing.T) {
	t.Parallel()
	topology, err := machine.GenerateDummyCPUTopology(2, 1, 1)
	require.NoError(t, err)
	s := NewCPUPluginState(topology)
	s.revision = math.MaxUint64
	oldEntries := s.GetPodEntries()
	oldMachineState := s.GetMachineState()

	err = s.CommitAdvisorStateIfRevision(
		math.MaxUint64,
		PodEntries{"new": {"main": &AllocationInfo{AllocationResult: machine.NewCPUSet(1)}}},
		NUMANodeMap{0: &NUMANodeState{AllocatedCPUSet: machine.NewCPUSet(1)}},
		true,
		false,
		false,
	)
	require.ErrorContains(t, err, "revision overflow")
	require.Equal(t, uint64(math.MaxUint64), s.GetRevision())
	require.Equal(t, oldEntries, s.GetPodEntries())
	require.Equal(t, oldMachineState, s.GetMachineState())
}

func TestCPUPluginStateMutationsReturnRevisionOverflowWithoutMutation(t *testing.T) {
	t.Parallel()
	topology, err := machine.GenerateDummyCPUTopology(2, 1, 1)
	require.NoError(t, err)

	for _, tc := range []struct {
		name   string
		mutate func(*cpuPluginState) error
	}{
		{
			name: "machine state",
			mutate: func(s *cpuPluginState) error {
				return s.SetMachineState(NUMANodeMap{
					0: &NUMANodeState{AllocatedCPUSet: machine.NewCPUSet(1)},
				})
			},
		},
		{
			name:   "numa headroom",
			mutate: func(s *cpuPluginState) error { return s.SetNUMAHeadroom(map[int]float64{0: 1}) },
		},
		{
			name: "allocation info",
			mutate: func(s *cpuPluginState) error {
				return s.SetAllocationInfo("new-pod", "main", &AllocationInfo{
					AllocationResult: machine.NewCPUSet(1),
				})
			},
		},
		{
			name:   "pod entries",
			mutate: func(s *cpuPluginState) error { return s.SetPodEntries(PodEntries{}) },
		},
		{
			name: "allow overlap",
			mutate: func(s *cpuPluginState) error {
				return s.SetAllowSharedCoresOverlapReclaimedCores(true)
			},
		},
		{
			name: "disable dedicated overlap",
			mutate: func(s *cpuPluginState) error {
				return s.SetDisableDedicatedCoresOverlapReclaimedCores(true)
			},
		},
		{
			name:   "delete",
			mutate: func(s *cpuPluginState) error { return s.Delete("pod", "main") },
		},
		{
			name:   "clear",
			mutate: func(s *cpuPluginState) error { return s.ClearState() },
		},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := NewCPUPluginState(topology)
			s.podEntries = PodEntries{
				"pod": {"main": &AllocationInfo{AllocationResult: machine.NewCPUSet(0)}},
			}
			s.revision = math.MaxUint64

			require.ErrorIs(t, tc.mutate(s), ErrStateRevisionOverflow)
			require.Equal(t, uint64(math.MaxUint64), s.GetRevision())
			require.NotNil(t, s.GetAllocationInfo("pod", "main"))
		})
	}
}

func TestCPUPluginStateSetAllocationInfoRejectsNil(t *testing.T) {
	t.Parallel()
	topology, err := machine.GenerateDummyCPUTopology(2, 1, 1)
	require.NoError(t, err)
	s := NewCPUPluginState(topology)

	revision := s.GetRevision()
	err = s.SetAllocationInfo("pod", "main", nil)

	require.Error(t, err)
	require.ErrorContains(t, err, "allocation info is nil")
	require.Equal(t, revision, s.GetRevision())
	require.Nil(t, s.GetAllocationInfo("pod", "main"))
}

func TestCheckpointStateWritePermitRejectsMutationBeforeStateChange(t *testing.T) {
	t.Parallel()
	topology, err := machine.GenerateDummyCPUTopology(2, 1, 1)
	require.NoError(t, err)
	sc := &stateCheckpoint{cache: NewCPUPluginState(topology)}
	denied := stderrors.New("write denied")
	var gotRevision uint64
	var gotOperation string
	sc.SetWritePermit(func(revision uint64, operation string, _ *WritePermit) error {
		gotRevision = revision
		gotOperation = operation
		return denied
	})
	oldState := sc.GetMachineState()
	oldRevision := sc.GetRevision()

	err = sc.SetMachineState(NUMANodeMap{
		0: &NUMANodeState{AllocatedCPUSet: machine.NewCPUSet(1)},
	}, false)

	require.ErrorIs(t, err, denied)
	require.Equal(t, oldRevision, gotRevision)
	require.Equal(t, "SetMachineState", gotOperation)
	require.Equal(t, oldRevision, sc.GetRevision())
	require.Equal(t, oldState, sc.GetMachineState())
}

func TestCheckpointStateStoreStateBypassesMutationPermit(t *testing.T) {
	t.Parallel()
	topology, err := machine.GenerateDummyCPUTopology(2, 1, 1)
	require.NoError(t, err)
	manager := &advisorCommitCheckpointManager{}
	sc := &stateCheckpoint{
		cache:             NewCPUPluginState(topology),
		checkpointManager: manager,
		checkpointName:    cpuPluginStateFileName,
		emitter:           metrics.DummyMetrics{},
	}
	require.NoError(t, sc.cache.SetAllocationInfo("pod", "main", &AllocationInfo{
		AllocationResult: machine.NewCPUSet(0),
	}))
	oldEntries := sc.GetPodEntries()
	oldMachineState := sc.GetMachineState()
	oldRevision := sc.GetRevision()
	denied := stderrors.New("write denied")
	sc.SetWritePermit(func(uint64, string, *WritePermit) error {
		return denied
	})

	require.NoError(t, sc.StoreState())
	require.Equal(t, oldRevision, sc.GetRevision())
	require.Equal(t, oldEntries, sc.GetPodEntries())
	require.Equal(t, oldMachineState, sc.GetMachineState())
	require.Equal(t, 1, manager.calls())
	checkpoint := manager.lastCheckpoint()
	require.NotNil(t, checkpoint)
	require.Equal(t, oldRevision, checkpoint.Revision)
	require.Equal(t, oldEntries, checkpoint.PodEntries)
	require.Equal(t, oldMachineState, checkpoint.MachineState)

	err = sc.SetMachineState(NUMANodeMap{
		0: &NUMANodeState{AllocatedCPUSet: machine.NewCPUSet(1)},
	}, false)
	require.ErrorIs(t, err, denied)
	require.Equal(t, oldRevision, sc.GetRevision())
	require.Equal(t, oldMachineState, sc.GetMachineState())
}

func TestTransientStateStoreStateBypassesMutationPermit(t *testing.T) {
	t.Parallel()
	topology, err := machine.GenerateDummyCPUTopology(2, 1, 1)
	require.NoError(t, err)
	s := NewTransientState(topology)
	oldEntries := s.GetPodEntries()
	oldMachineState := s.GetMachineState()
	oldRevision := s.GetRevision()
	denied := stderrors.New("write denied")
	s.SetWritePermit(func(uint64, string, *WritePermit) error {
		return denied
	})

	require.NoError(t, s.StoreState())
	require.Equal(t, oldRevision, s.GetRevision())
	require.Equal(t, oldEntries, s.GetPodEntries())
	require.Equal(t, oldMachineState, s.GetMachineState())

	err = s.SetMachineState(NUMANodeMap{
		0: &NUMANodeState{AllocatedCPUSet: machine.NewCPUSet(1)},
	}, false)
	require.ErrorIs(t, err, denied)
	require.Equal(t, oldRevision, s.GetRevision())
	require.Equal(t, oldMachineState, s.GetMachineState())
}

func TestCheckpointStateWritePermitGuardsAdvisorCommits(t *testing.T) {
	t.Parallel()
	topology, err := machine.GenerateDummyCPUTopology(2, 1, 1)
	require.NoError(t, err)
	denied := stderrors.New("write denied")

	for _, tc := range []struct {
		name      string
		operation string
		commit    func(*stateCheckpoint, PodEntries, NUMANodeMap) error
	}{
		{
			name:      "unconditional",
			operation: "CommitAdvisorState",
			commit: func(sc *stateCheckpoint, entries PodEntries, machineState NUMANodeMap) error {
				return sc.CommitAdvisorState(entries, machineState, true, false, false)
			},
		},
		{
			name:      "revision conditional",
			operation: "CommitAdvisorStateIfRevision",
			commit: func(sc *stateCheckpoint, entries PodEntries, machineState NUMANodeMap) error {
				return sc.CommitAdvisorStateIfRevision(
					sc.GetRevision(), entries, machineState, true, false, false)
			},
		},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			sc := &stateCheckpoint{cache: NewCPUPluginState(topology)}
			oldEntries := sc.GetPodEntries()
			oldMachineState := sc.GetMachineState()
			oldRevision := sc.GetRevision()
			var gotRevision uint64
			var gotOperation string
			sc.SetWritePermit(func(revision uint64, operation string, _ *WritePermit) error {
				gotRevision = revision
				gotOperation = operation
				return denied
			})

			err := tc.commit(
				sc,
				PodEntries{"new": {"main": &AllocationInfo{AllocationResult: machine.NewCPUSet(1)}}},
				NUMANodeMap{0: &NUMANodeState{AllocatedCPUSet: machine.NewCPUSet(1)}},
			)

			require.ErrorIs(t, err, denied)
			require.Equal(t, oldRevision, gotRevision)
			require.Equal(t, tc.operation, gotOperation)
			require.Equal(t, oldRevision, sc.GetRevision())
			require.Equal(t, oldEntries, sc.GetPodEntries())
			require.Equal(t, oldMachineState, sc.GetMachineState())
		})
	}
}

func TestCheckpointStateRejectsExhaustedRevisionDuringRestore(t *testing.T) {
	t.Parallel()
	topology, err := machine.GenerateDummyCPUTopology(2, 1, 1)
	require.NoError(t, err)
	sc := &stateCheckpoint{
		cache:                              NewCPUPluginState(topology),
		policyName:                         "dynamic",
		topology:                           topology,
		GenerateMachineStateFromPodEntries: GenerateMachineStateFromPodEntries,
	}
	checkpoint := NewCPUPluginCheckpoint()
	checkpoint.PolicyName = "dynamic"
	checkpoint.MachineState = GetDefaultMachineState(topology)
	checkpoint.Revision = math.MaxUint64

	changed, err := sc.RestoreState(checkpoint)
	require.False(t, changed)
	require.ErrorIs(t, err, ErrStateRevisionOverflow)
	require.Zero(t, sc.GetRevision())
}

func TestStateSkipsNilAllocationInfo(t *testing.T) {
	t.Parallel()

	as := require.New(t)
	cpuTopology, err := machine.GenerateDummyCPUTopology(16, 2, 4)
	as.Nil(err)

	podEntries := PodEntries{
		"pod-with-nil-entry": {
			"container-with-nil-allocation": nil,
		},
	}
	var nilContainerEntries ContainerEntries
	as.False(nilContainerEntries.IsPoolEntry())
	as.False(ContainerEntries{}.IsPoolEntry())
	as.False(ContainerEntries{commonstate.FakedContainerName: nil}.IsPoolEntry())

	as.NotPanics(func() {
		cloned := podEntries.Clone()
		as.Contains(cloned, "pod-with-nil-entry")
		as.Nil(cloned["pod-with-nil-entry"]["container-with-nil-allocation"])
	})

	stateImpl := NewCPUPluginState(cpuTopology)
	as.NotPanics(func() {
		stateImpl.SetPodEntries(podEntries)
	})
	as.Nil(stateImpl.GetAllocationInfo("pod-with-nil-entry", "container-with-nil-allocation"))
	as.NotPanics(func() {
		stateImpl.SetAllocationInfo("pod-with-nil-entry", "another-nil-container", nil)
	})

	numaState := &NUMANodeState{
		DefaultCPUSet: machine.NewCPUSet(0, 1),
		PodEntries:    podEntries,
	}
	as.NotPanics(func() {
		_ = numaState.GetFilteredDefaultCPUSet(func(ai *AllocationInfo) bool {
			return ai.CheckDedicated()
		}, func(ai *AllocationInfo) bool {
			return ai.CheckNumaExclusive()
		})
	})
	as.NotPanics(func() {
		_ = numaState.ExistMatchedAllocationInfo(func(ai *AllocationInfo) bool {
			return ai.CheckDedicated()
		})
	})
	as.NotPanics(func() {
		_ = numaState.ExistMatchedAllocationInfoWithAnnotations(func(ai *AllocationInfo, annotations map[string]string) bool {
			return ai.CheckDedicated()
		}, nil)
	})
	as.NotPanics(func() {
		numaState.SetAllocationInfo("pod-with-nil-entry", "nil-container", nil)
		numaState.SetPreOccAllocationInfo("pod-with-nil-entry", "nil-container", nil)
	})

	as.NotPanics(func() {
		_, err = GenerateMachineStateFromPodEntries(cpuTopology, podEntries, nil)
	})
	as.Nil(err)
}

// assertStateEqual marks provided test as failed if provided states differ
func assertStateEqual(t *testing.T, restoredState, expectedState State) {
	as := require.New(t)

	expectedMachineState := expectedState.GetMachineState()
	restoredMachineState := restoredState.GetMachineState()
	as.Equalf(expectedMachineState, restoredMachineState, "machineState mismatches")

	expectedPodEntries := expectedState.GetPodEntries()
	restoredPodEntries := restoredState.GetPodEntries()
	as.Equalf(expectedPodEntries, restoredPodEntries, "podEntries mismatch")
}

// generateSharedNumaBindingPoolAllocationMeta generates a generic allocation metadata for a pool.
func generateSharedNumaBindingPoolAllocationMeta(poolName string) commonstate.AllocationMeta {
	meta := commonstate.GenerateGenericPoolAllocationMeta(poolName)
	if meta.Annotations == nil {
		meta.Annotations = make(map[string]string)
	}
	meta.Annotations[consts.PodAnnotationMemoryEnhancementNumaBinding] = consts.PodAnnotationMemoryEnhancementNumaBindingEnable
	meta.QoSLevel = consts.PodAnnotationQoSLevelSharedCores
	return meta
}

func TestNewCheckpointState(t *testing.T) {
	t.Parallel()

	testName := "test"
	cpuTopology, _ := machine.GenerateDummyCPUTopology(16, 2, 4)

	testCases := []struct {
		description       string
		checkpointContent string
		expectedError     string
		expectedState     *cpuPluginState
	}{
		{
			"Restore non-existing checkpoint",
			"",
			"",
			&cpuPluginState{
				cpuPluginStateData: cpuPluginStateData{
					podEntries:   make(PodEntries),
					machineState: GetDefaultMachineState(cpuTopology),
				},
				socketTopology: cpuTopology.GetSocketTopology(),
				cpuTopology:    cpuTopology,
			},
		},
		{
			"Restore valid checkpoint",
			"",
			"",
			&cpuPluginState{
				cpuPluginStateData: cpuPluginStateData{
					podEntries: PodEntries{
						"373d08e4-7a6b-4293-aaaf-b135ff8123bf": ContainerEntries{
							testName: &AllocationInfo{
								AllocationMeta: commonstate.AllocationMeta{
									PodUid:         "373d08e4-7a6b-4293-aaaf-b135ff8123bf",
									PodNamespace:   testName,
									PodName:        testName,
									ContainerName:  testName,
									ContainerType:  pluginapi.ContainerType_MAIN.String(),
									ContainerIndex: 0,
									OwnerPoolName:  commonstate.PoolNameShare,
									Labels: map[string]string{
										consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
									},
									Annotations: map[string]string{
										consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
									},
									QoSLevel: consts.PodAnnotationQoSLevelSharedCores,
								},
								RampUp:                   false,
								AllocationResult:         machine.MustParse("1,3-4,9,11-12"),
								OriginalAllocationResult: machine.MustParse("1,3-4,9,11-12"),
								TopologyAwareAssignments: map[int]machine.CPUSet{
									0: machine.NewCPUSet(1, 9),
									1: machine.NewCPUSet(3, 11),
									2: machine.NewCPUSet(4, 12),
								},
								OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
									0: machine.NewCPUSet(1, 9),
									1: machine.NewCPUSet(3, 11),
									2: machine.NewCPUSet(4, 12),
								},
								RequestQuantity: 2,
							},
						},
						"ec6e2f30-c78a-4bc4-9576-c916db5281a3": ContainerEntries{
							testName: &AllocationInfo{
								AllocationMeta: commonstate.AllocationMeta{
									PodUid:         "ec6e2f30-c78a-4bc4-9576-c916db5281a3",
									PodNamespace:   testName,
									PodName:        testName,
									ContainerName:  testName,
									ContainerType:  pluginapi.ContainerType_MAIN.String(),
									ContainerIndex: 0,
									OwnerPoolName:  commonstate.PoolNameShare,
									Labels: map[string]string{
										consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
									},
									Annotations: map[string]string{
										consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
									},
									QoSLevel: consts.PodAnnotationQoSLevelSharedCores,
								},
								RampUp:                   false,
								AllocationResult:         machine.MustParse("1,3-4,9,11-12"),
								OriginalAllocationResult: machine.MustParse("1,3-4,9,11-12"),
								TopologyAwareAssignments: map[int]machine.CPUSet{
									0: machine.NewCPUSet(1, 9),
									1: machine.NewCPUSet(3, 11),
									2: machine.NewCPUSet(4, 12),
								},
								OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
									0: machine.NewCPUSet(1, 9),
									1: machine.NewCPUSet(3, 11),
									2: machine.NewCPUSet(4, 12),
								},
								RequestQuantity: 2,
							},
						},
						"2432d068-c5a0-46ba-a7bd-b69d9bd16961": ContainerEntries{
							testName: &AllocationInfo{
								AllocationMeta: commonstate.AllocationMeta{
									PodUid:         "2432d068-c5a0-46ba-a7bd-b69d9bd16961",
									PodNamespace:   testName,
									PodName:        testName,
									ContainerName:  testName,
									ContainerType:  pluginapi.ContainerType_MAIN.String(),
									ContainerIndex: 0,
									OwnerPoolName:  commonstate.PoolNameReclaim,
									Labels: map[string]string{
										consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
									},
									Annotations: map[string]string{
										consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
									},
									QoSLevel: consts.PodAnnotationQoSLevelReclaimedCores,
								},
								RampUp:                   false,
								AllocationResult:         machine.MustParse("5-8,10,13-15"),
								OriginalAllocationResult: machine.MustParse("5-8,10,13-15"),
								TopologyAwareAssignments: map[int]machine.CPUSet{
									0: machine.NewCPUSet(8),
									1: machine.NewCPUSet(10),
									2: machine.NewCPUSet(5, 13),
									3: machine.NewCPUSet(6, 7, 14, 15),
								},
								OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
									0: machine.NewCPUSet(8),
									1: machine.NewCPUSet(10),
									2: machine.NewCPUSet(5, 13),
									3: machine.NewCPUSet(6, 7, 14, 15),
								},
								RequestQuantity: 2,
							},
						},
						commonstate.PoolNameReclaim: ContainerEntries{
							"": &AllocationInfo{
								AllocationMeta:           commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
								AllocationResult:         machine.MustParse("5-8,10,13-15"),
								OriginalAllocationResult: machine.MustParse("5-8,10,13-15"),
								TopologyAwareAssignments: map[int]machine.CPUSet{
									0: machine.NewCPUSet(8),
									1: machine.NewCPUSet(10),
									2: machine.NewCPUSet(5, 13),
									3: machine.NewCPUSet(6, 7, 14, 15),
								},
								OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
									0: machine.NewCPUSet(8),
									1: machine.NewCPUSet(10),
									2: machine.NewCPUSet(5, 13),
									3: machine.NewCPUSet(6, 7, 14, 15),
								},
							},
						},
						commonstate.PoolNameShare: ContainerEntries{
							"": &AllocationInfo{
								AllocationMeta:           commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameShare),
								AllocationResult:         machine.MustParse("1,3-4,9,11-12"),
								OriginalAllocationResult: machine.MustParse("1,3-4,9,11-12"),
								TopologyAwareAssignments: map[int]machine.CPUSet{
									0: machine.NewCPUSet(1, 9),
									1: machine.NewCPUSet(3, 11),
									2: machine.NewCPUSet(4, 12),
								},
								OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
									0: machine.NewCPUSet(1, 9),
									1: machine.NewCPUSet(3, 11),
									2: machine.NewCPUSet(4, 12),
								},
							},
						},
					},
					machineState: NUMANodeMap{
						0: &NUMANodeState{
							DefaultCPUSet:   cpuTopology.CPUDetails.CPUsInNUMANodes(0).Clone(),
							AllocatedCPUSet: machine.NewCPUSet(),
							PodEntries: PodEntries{
								"373d08e4-7a6b-4293-aaaf-b135ff8123bf": ContainerEntries{
									testName: &AllocationInfo{
										AllocationMeta: commonstate.AllocationMeta{
											PodUid:         "373d08e4-7a6b-4293-aaaf-b135ff8123bf",
											PodNamespace:   testName,
											PodName:        testName,
											ContainerName:  testName,
											ContainerType:  pluginapi.ContainerType_MAIN.String(),
											ContainerIndex: 0,
											OwnerPoolName:  commonstate.PoolNameShare,
											Labels: map[string]string{
												consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
											},
											Annotations: map[string]string{
												consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
											},
											QoSLevel: consts.PodAnnotationQoSLevelSharedCores,
										},
										RampUp:                   false,
										AllocationResult:         machine.NewCPUSet(1, 9),
										OriginalAllocationResult: machine.NewCPUSet(1, 9),
										TopologyAwareAssignments: map[int]machine.CPUSet{
											0: machine.NewCPUSet(1, 9),
										},
										OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
											0: machine.NewCPUSet(1, 9),
										},
										RequestQuantity: 2,
									},
								},
								"ec6e2f30-c78a-4bc4-9576-c916db5281a3": ContainerEntries{
									testName: &AllocationInfo{
										AllocationMeta: commonstate.AllocationMeta{
											PodUid:         "ec6e2f30-c78a-4bc4-9576-c916db5281a3",
											PodNamespace:   testName,
											PodName:        testName,
											ContainerName:  testName,
											ContainerType:  pluginapi.ContainerType_MAIN.String(),
											ContainerIndex: 0,
											OwnerPoolName:  commonstate.PoolNameShare,
											Labels: map[string]string{
												consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
											},
											Annotations: map[string]string{
												consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
											},
											QoSLevel: consts.PodAnnotationQoSLevelSharedCores,
										},
										RampUp:                   false,
										AllocationResult:         machine.NewCPUSet(1, 9),
										OriginalAllocationResult: machine.NewCPUSet(1, 9),
										TopologyAwareAssignments: map[int]machine.CPUSet{
											0: machine.NewCPUSet(1, 9),
										},
										OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
											0: machine.NewCPUSet(1, 9),
										},
										RequestQuantity: 2,
									},
								},
								"2432d068-c5a0-46ba-a7bd-b69d9bd16961": ContainerEntries{
									testName: &AllocationInfo{
										AllocationMeta: commonstate.AllocationMeta{
											PodUid:         "2432d068-c5a0-46ba-a7bd-b69d9bd16961",
											PodNamespace:   testName,
											PodName:        testName,
											ContainerName:  testName,
											ContainerType:  pluginapi.ContainerType_MAIN.String(),
											ContainerIndex: 0,
											OwnerPoolName:  commonstate.PoolNameReclaim,
											Labels: map[string]string{
												consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
											},
											Annotations: map[string]string{
												consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
											},
											QoSLevel: consts.PodAnnotationQoSLevelReclaimedCores,
										},
										RampUp:                   false,
										AllocationResult:         machine.MustParse("8"),
										OriginalAllocationResult: machine.MustParse("8"),
										TopologyAwareAssignments: map[int]machine.CPUSet{
											0: machine.NewCPUSet(8),
										},
										OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
											0: machine.NewCPUSet(8),
										},
										RequestQuantity: 2,
									},
								},
							},
						},
						1: &NUMANodeState{
							DefaultCPUSet:   cpuTopology.CPUDetails.CPUsInNUMANodes(1).Clone(),
							AllocatedCPUSet: machine.NewCPUSet(),
							PodEntries: PodEntries{
								"373d08e4-7a6b-4293-aaaf-b135ff8123bf": ContainerEntries{
									testName: &AllocationInfo{
										AllocationMeta: commonstate.AllocationMeta{
											PodUid:         "373d08e4-7a6b-4293-aaaf-b135ff8123bf",
											PodNamespace:   testName,
											PodName:        testName,
											ContainerName:  testName,
											ContainerType:  pluginapi.ContainerType_MAIN.String(),
											ContainerIndex: 0,
											OwnerPoolName:  commonstate.PoolNameShare,
											Labels: map[string]string{
												consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
											},
											Annotations: map[string]string{
												consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
											},
											QoSLevel: consts.PodAnnotationQoSLevelSharedCores,
										},
										RampUp:                   false,
										AllocationResult:         machine.MustParse("3,11"),
										OriginalAllocationResult: machine.MustParse("3,11"),
										TopologyAwareAssignments: map[int]machine.CPUSet{
											1: machine.NewCPUSet(3, 11),
										},
										OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
											1: machine.NewCPUSet(3, 11),
										},
										RequestQuantity: 2,
									},
								},
								"ec6e2f30-c78a-4bc4-9576-c916db5281a3": ContainerEntries{
									testName: &AllocationInfo{
										AllocationMeta: commonstate.AllocationMeta{
											PodUid:         "ec6e2f30-c78a-4bc4-9576-c916db5281a3",
											PodNamespace:   testName,
											PodName:        testName,
											ContainerName:  testName,
											ContainerType:  pluginapi.ContainerType_MAIN.String(),
											ContainerIndex: 0,
											OwnerPoolName:  commonstate.PoolNameShare,
											Labels: map[string]string{
												consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
											},
											Annotations: map[string]string{
												consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
											},
											QoSLevel: consts.PodAnnotationQoSLevelSharedCores,
										},
										RampUp:                   false,
										AllocationResult:         machine.MustParse("3,11"),
										OriginalAllocationResult: machine.MustParse("3,11"),
										TopologyAwareAssignments: map[int]machine.CPUSet{
											1: machine.NewCPUSet(3, 11),
										},
										OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
											1: machine.NewCPUSet(3, 11),
										},
										RequestQuantity: 2,
									},
								},
								"2432d068-c5a0-46ba-a7bd-b69d9bd16961": ContainerEntries{
									testName: &AllocationInfo{
										AllocationMeta: commonstate.AllocationMeta{
											PodUid:         "2432d068-c5a0-46ba-a7bd-b69d9bd16961",
											PodNamespace:   testName,
											PodName:        testName,
											ContainerName:  testName,
											ContainerType:  pluginapi.ContainerType_MAIN.String(),
											ContainerIndex: 0,
											OwnerPoolName:  commonstate.PoolNameReclaim,
											Labels: map[string]string{
												consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
											},
											Annotations: map[string]string{
												consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
											},
											QoSLevel: consts.PodAnnotationQoSLevelReclaimedCores,
										},
										RampUp:                   false,
										AllocationResult:         machine.MustParse("10"),
										OriginalAllocationResult: machine.MustParse("10"),
										TopologyAwareAssignments: map[int]machine.CPUSet{
											1: machine.NewCPUSet(10),
										},
										OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
											1: machine.NewCPUSet(10),
										},
										RequestQuantity: 2,
									},
								},
							},
						},
						2: &NUMANodeState{
							DefaultCPUSet:   cpuTopology.CPUDetails.CPUsInNUMANodes(2).Clone(),
							AllocatedCPUSet: machine.NewCPUSet(),
							PodEntries: PodEntries{
								"373d08e4-7a6b-4293-aaaf-b135ff8123bf": ContainerEntries{
									testName: &AllocationInfo{
										AllocationMeta: commonstate.AllocationMeta{
											PodUid:         "373d08e4-7a6b-4293-aaaf-b135ff8123bf",
											PodNamespace:   testName,
											PodName:        testName,
											ContainerName:  testName,
											ContainerType:  pluginapi.ContainerType_MAIN.String(),
											ContainerIndex: 0,
											OwnerPoolName:  commonstate.PoolNameShare,
											Labels: map[string]string{
												consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
											},
											Annotations: map[string]string{
												consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
											},
											QoSLevel: consts.PodAnnotationQoSLevelSharedCores,
										},
										RampUp:                   false,
										AllocationResult:         machine.MustParse("4,12"),
										OriginalAllocationResult: machine.MustParse("4,12"),
										TopologyAwareAssignments: map[int]machine.CPUSet{
											2: machine.NewCPUSet(4, 12),
										},
										OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
											2: machine.NewCPUSet(4, 12),
										},
										RequestQuantity: 2,
									},
								},
								"ec6e2f30-c78a-4bc4-9576-c916db5281a3": ContainerEntries{
									testName: &AllocationInfo{
										AllocationMeta: commonstate.AllocationMeta{
											PodUid:         "ec6e2f30-c78a-4bc4-9576-c916db5281a3",
											PodNamespace:   testName,
											PodName:        testName,
											ContainerName:  testName,
											ContainerType:  pluginapi.ContainerType_MAIN.String(),
											ContainerIndex: 0,
											OwnerPoolName:  commonstate.PoolNameShare,
											Labels: map[string]string{
												consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
											},
											Annotations: map[string]string{
												consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
											},
											QoSLevel: consts.PodAnnotationQoSLevelSharedCores,
										},
										RampUp:                   false,
										AllocationResult:         machine.MustParse("4,12"),
										OriginalAllocationResult: machine.MustParse("4,12"),
										TopologyAwareAssignments: map[int]machine.CPUSet{
											2: machine.NewCPUSet(4, 12),
										},
										OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
											2: machine.NewCPUSet(4, 12),
										},
										RequestQuantity: 2,
									},
								},
								"2432d068-c5a0-46ba-a7bd-b69d9bd16961": ContainerEntries{
									testName: &AllocationInfo{
										AllocationMeta: commonstate.AllocationMeta{
											PodUid:         "2432d068-c5a0-46ba-a7bd-b69d9bd16961",
											PodNamespace:   testName,
											PodName:        testName,
											ContainerName:  testName,
											ContainerType:  pluginapi.ContainerType_MAIN.String(),
											ContainerIndex: 0,
											OwnerPoolName:  commonstate.PoolNameReclaim,
											Labels: map[string]string{
												consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
											},
											Annotations: map[string]string{
												consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
											},
											QoSLevel: consts.PodAnnotationQoSLevelReclaimedCores,
										},
										RampUp:                   false,
										AllocationResult:         machine.MustParse("5,13"),
										OriginalAllocationResult: machine.MustParse("5,13"),
										TopologyAwareAssignments: map[int]machine.CPUSet{
											2: machine.NewCPUSet(5, 13),
										},
										OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
											2: machine.NewCPUSet(5, 13),
										},
										RequestQuantity: 2,
									},
								},
							},
						},
						3: &NUMANodeState{
							DefaultCPUSet:   cpuTopology.CPUDetails.CPUsInNUMANodes(3).Clone(),
							AllocatedCPUSet: machine.NewCPUSet(),
							PodEntries: PodEntries{
								"2432d068-c5a0-46ba-a7bd-b69d9bd16961": ContainerEntries{
									testName: &AllocationInfo{
										AllocationMeta: commonstate.AllocationMeta{
											PodUid:         "2432d068-c5a0-46ba-a7bd-b69d9bd16961",
											PodNamespace:   testName,
											PodName:        testName,
											ContainerName:  testName,
											ContainerType:  pluginapi.ContainerType_MAIN.String(),
											ContainerIndex: 0,
											OwnerPoolName:  commonstate.PoolNameReclaim,
											Labels: map[string]string{
												consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
											},
											Annotations: map[string]string{
												consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
											},
											QoSLevel: consts.PodAnnotationQoSLevelReclaimedCores,
										},
										RampUp:                   false,
										AllocationResult:         machine.MustParse("6,7,14,15"),
										OriginalAllocationResult: machine.MustParse("6,7,14,15"),
										TopologyAwareAssignments: map[int]machine.CPUSet{
											3: machine.NewCPUSet(6, 7, 14, 15),
										},
										OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
											3: machine.NewCPUSet(6, 7, 14, 15),
										},
										RequestQuantity: 2,
									},
								},
							},
						},
					},
				},
			},
		},
		{
			"Restore checkpoint with invalid checksum",
			"",
			"checkpoint is corrupted",
			&cpuPluginState{},
		},
		{
			"Restore checkpoint with invalid JSON",
			`{`,
			"unexpected end of JSON input",
			&cpuPluginState{},
		},
	}

	validCheckpoint := &CPUPluginCheckpoint{
		PolicyName:                            policyName,
		MachineState:                          testCases[1].expectedState.machineState,
		NUMAHeadroom:                          testCases[1].expectedState.numaHeadroom,
		PodEntries:                            testCases[1].expectedState.podEntries,
		AllowSharedCoresOverlapReclaimedCores: testCases[1].expectedState.allowSharedCoresOverlapReclaimedCores,
		DisableDedicatedCoresOverlapReclaimedCores: testCases[1].expectedState.disableDedicatedCoresOverlapReclaimedCores,
		Revision: testCases[1].expectedState.revision,
	}
	validCheckpointContent, err := validCheckpoint.MarshalCheckpoint()
	require.NoError(t, err)
	testCases[1].checkpointContent = string(validCheckpointContent)

	validCheckpoint.Checksum ^= 1
	corruptedCheckpointContent, err := json.Marshal(validCheckpoint)
	require.NoError(t, err)
	testCases[2].checkpointContent = string(corruptedCheckpointContent)

	// create temp dir
	testingDir := t.TempDir()
	stateDirectoryConfig := &statedirectory.StateDirectoryConfiguration{
		StateFileDirectory: testingDir,
	}

	// create checkpoint manager for testing
	cpm, err := checkpointmanager.NewCheckpointManager(testingDir)
	assert.NoError(t, err, "could not create testing checkpoint manager")

	for _, tc := range testCases {
		// ensure there is no previous checkpoint
		require.NoError(t, cpm.RemoveCheckpoint(cpuPluginStateFileName), "could not remove testing checkpoint")

		// prepare checkpoint for testing
		if strings.TrimSpace(tc.checkpointContent) != "" {
			checkpoint := &testutil.MockCheckpoint{Content: tc.checkpointContent}
			require.NoError(t, cpm.CreateCheckpoint(cpuPluginStateFileName, checkpoint), "could not create testing checkpoint")
		}

		restoredState, err := NewCheckpointState(stateDirectoryConfig, cpuPluginStateFileName, policyName, cpuTopology, false, GenerateMachineStateFromPodEntries, metrics.DummyMetrics{})
		if strings.TrimSpace(tc.expectedError) != "" {
			require.Error(t, err)
			require.Contains(t, err.Error(), "could not restore state from checkpoint:")
			require.Contains(t, err.Error(), tc.expectedError)

			// test skip corruption
			if strings.Contains(err.Error(), "checkpoint is corrupted") {
				_, err = NewCheckpointState(stateDirectoryConfig, cpuPluginStateFileName, policyName, cpuTopology, true, GenerateMachineStateFromPodEntries, metrics.DummyMetrics{})
				require.Nil(t, err)
			}
		} else {
			require.NoError(t, err, "unexpected error while creating checkpointState, case: %s", tc.description)
			// compare state after restoration with the one expected
			assertStateEqual(t, restoredState, &stateCheckpoint{cache: tc.expectedState})
		}
	}
}

func TestClearState(t *testing.T) {
	t.Parallel()
	// klog configuration is process-global and InitFlags has no reset semantics.
	// The verbosity level is an atomic, so bumping it here does not race with the
	// rest of the package; we keep a dedicated FlagSet (never flag.CommandLine)
	// and do not call InitFlags(nil) as cleanup because that re-registers klog
	// flags on flag.CommandLine when the test runs with -count.
	flagSet := flag.NewFlagSet("test", flag.ExitOnError)
	klog.InitFlags(flagSet)
	_ = flagSet.Parse([]string{"--v", "6"})

	as := require.New(t)

	testName := "test"
	cpuTopology, err := machine.GenerateDummyCPUTopology(16, 2, 4)
	as.Nil(err)

	testCases := []struct {
		description  string
		cpuTopology  *machine.CPUTopology
		podEntries   PodEntries
		machineState NUMANodeMap
	}{
		{
			description: "valid state cleaning",
			cpuTopology: cpuTopology,
			podEntries: PodEntries{
				"373d08e4-7a6b-4293-aaaf-b135ff8123bf": ContainerEntries{
					testName: &AllocationInfo{
						AllocationMeta: commonstate.AllocationMeta{
							PodUid:         "373d08e4-7a6b-4293-aaaf-b135ff8123bf",
							PodNamespace:   testName,
							PodName:        testName,
							ContainerName:  testName,
							ContainerType:  pluginapi.ContainerType_MAIN.String(),
							ContainerIndex: 0,
							OwnerPoolName:  commonstate.PoolNameShare,
							Labels: map[string]string{
								consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
							},
							Annotations: map[string]string{
								consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
							},
							QoSLevel: consts.PodAnnotationQoSLevelSharedCores,
						},
						RampUp:                   false,
						AllocationResult:         machine.MustParse("1,3-4,9,11-12"),
						OriginalAllocationResult: machine.MustParse("1,3-4,9,11-12"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(1, 9),
							1: machine.NewCPUSet(3, 11),
							2: machine.NewCPUSet(4, 12),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(1, 9),
							1: machine.NewCPUSet(3, 11),
							2: machine.NewCPUSet(4, 12),
						},
						RequestQuantity: 2,
					},
				},
				"ec6e2f30-c78a-4bc4-9576-c916db5281a3": ContainerEntries{
					testName: &AllocationInfo{
						AllocationMeta: commonstate.AllocationMeta{
							PodUid:         "ec6e2f30-c78a-4bc4-9576-c916db5281a3",
							PodNamespace:   testName,
							PodName:        testName,
							ContainerName:  testName,
							ContainerType:  pluginapi.ContainerType_MAIN.String(),
							ContainerIndex: 0,
							OwnerPoolName:  commonstate.PoolNameShare,
							Labels: map[string]string{
								consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
							},
							Annotations: map[string]string{
								consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
							},
							QoSLevel: consts.PodAnnotationQoSLevelSharedCores,
						},
						RampUp:                   false,
						AllocationResult:         machine.MustParse("1,3-4,9,11-12"),
						OriginalAllocationResult: machine.MustParse("1,3-4,9,11-12"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(1, 9),
							1: machine.NewCPUSet(3, 11),
							2: machine.NewCPUSet(4, 12),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(1, 9),
							1: machine.NewCPUSet(3, 11),
							2: machine.NewCPUSet(4, 12),
						},
						RequestQuantity: 2,
					},
				},
				"2432d068-c5a0-46ba-a7bd-b69d9bd16961": ContainerEntries{
					testName: &AllocationInfo{
						AllocationMeta: commonstate.AllocationMeta{
							PodUid:         "2432d068-c5a0-46ba-a7bd-b69d9bd16961",
							PodNamespace:   testName,
							PodName:        testName,
							ContainerName:  testName,
							ContainerType:  pluginapi.ContainerType_MAIN.String(),
							ContainerIndex: 0,
							OwnerPoolName:  commonstate.PoolNameReclaim,
							Labels: map[string]string{
								consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
							},
							Annotations: map[string]string{
								consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
							},
							QoSLevel: consts.PodAnnotationQoSLevelReclaimedCores,
						},
						RampUp:                   false,
						AllocationResult:         machine.MustParse("5-8,10,13-15"),
						OriginalAllocationResult: machine.MustParse("5-8,10,13-15"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(8),
							1: machine.NewCPUSet(10),
							2: machine.NewCPUSet(5, 13),
							3: machine.NewCPUSet(6, 7, 14, 15),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(8),
							1: machine.NewCPUSet(10),
							2: machine.NewCPUSet(5, 13),
							3: machine.NewCPUSet(6, 7, 14, 15),
						},
						RequestQuantity: 2,
					},
				},
				commonstate.PoolNameReclaim: ContainerEntries{
					"": &AllocationInfo{
						AllocationMeta:           commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
						AllocationResult:         machine.MustParse("5-8,10,13-15"),
						OriginalAllocationResult: machine.MustParse("5-8,10,13-15"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(8),
							1: machine.NewCPUSet(10),
							2: machine.NewCPUSet(5, 13),
							3: machine.NewCPUSet(6, 7, 14, 15),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(8),
							1: machine.NewCPUSet(10),
							2: machine.NewCPUSet(5, 13),
							3: machine.NewCPUSet(6, 7, 14, 15),
						},
					},
				},
				commonstate.PoolNameShare: ContainerEntries{
					"": &AllocationInfo{
						AllocationMeta:           commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameShare),
						AllocationResult:         machine.MustParse("1,3-4,9,11-12"),
						OriginalAllocationResult: machine.MustParse("1,3-4,9,11-12"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(1, 9),
							1: machine.NewCPUSet(3, 11),
							2: machine.NewCPUSet(4, 12),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(1, 9),
							1: machine.NewCPUSet(3, 11),
							2: machine.NewCPUSet(4, 12),
						},
					},
				},
			},
			machineState: NUMANodeMap{
				0: &NUMANodeState{
					DefaultCPUSet:   cpuTopology.CPUDetails.CPUsInNUMANodes(0).Clone(),
					AllocatedCPUSet: machine.NewCPUSet(),
					PodEntries: PodEntries{
						"373d08e4-7a6b-4293-aaaf-b135ff8123bf": ContainerEntries{
							testName: &AllocationInfo{
								AllocationMeta: commonstate.AllocationMeta{
									PodUid:         "373d08e4-7a6b-4293-aaaf-b135ff8123bf",
									PodNamespace:   testName,
									PodName:        testName,
									ContainerName:  testName,
									ContainerType:  pluginapi.ContainerType_MAIN.String(),
									ContainerIndex: 0,
									OwnerPoolName:  commonstate.PoolNameShare,
									Labels: map[string]string{
										consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
									},
									Annotations: map[string]string{
										consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
									},
									QoSLevel: consts.PodAnnotationQoSLevelSharedCores,
								},
								RampUp:                   false,
								AllocationResult:         machine.NewCPUSet(1, 9),
								OriginalAllocationResult: machine.NewCPUSet(1, 9),
								TopologyAwareAssignments: map[int]machine.CPUSet{
									0: machine.NewCPUSet(1, 9),
								},
								OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
									0: machine.NewCPUSet(1, 9),
								},
								RequestQuantity: 2,
							},
						},
						"ec6e2f30-c78a-4bc4-9576-c916db5281a3": ContainerEntries{
							testName: &AllocationInfo{
								AllocationMeta: commonstate.AllocationMeta{
									PodUid:         "ec6e2f30-c78a-4bc4-9576-c916db5281a3",
									PodNamespace:   testName,
									PodName:        testName,
									ContainerName:  testName,
									ContainerType:  pluginapi.ContainerType_MAIN.String(),
									ContainerIndex: 0,
									OwnerPoolName:  commonstate.PoolNameShare,
									Labels: map[string]string{
										consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
									},
									Annotations: map[string]string{
										consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
									},
									QoSLevel: consts.PodAnnotationQoSLevelSharedCores,
								},
								RampUp:                   false,
								AllocationResult:         machine.NewCPUSet(1, 9),
								OriginalAllocationResult: machine.NewCPUSet(1, 9),
								TopologyAwareAssignments: map[int]machine.CPUSet{
									0: machine.NewCPUSet(1, 9),
								},
								OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
									0: machine.NewCPUSet(1, 9),
								},
								RequestQuantity: 2,
							},
						},
						"2432d068-c5a0-46ba-a7bd-b69d9bd16961": ContainerEntries{
							testName: &AllocationInfo{
								AllocationMeta: commonstate.AllocationMeta{
									PodUid:         "2432d068-c5a0-46ba-a7bd-b69d9bd16961",
									PodNamespace:   testName,
									PodName:        testName,
									ContainerName:  testName,
									ContainerType:  pluginapi.ContainerType_MAIN.String(),
									ContainerIndex: 0,
									OwnerPoolName:  commonstate.PoolNameReclaim,
									Labels: map[string]string{
										consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
									},
									Annotations: map[string]string{
										consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
									},
									QoSLevel: consts.PodAnnotationQoSLevelReclaimedCores,
								},
								RampUp:                   false,
								AllocationResult:         machine.MustParse("8"),
								OriginalAllocationResult: machine.MustParse("8"),
								TopologyAwareAssignments: map[int]machine.CPUSet{
									0: machine.NewCPUSet(8),
								},
								OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
									0: machine.NewCPUSet(8),
								},
								RequestQuantity: 2,
							},
						},
					},
				},
				1: &NUMANodeState{
					DefaultCPUSet:   cpuTopology.CPUDetails.CPUsInNUMANodes(1).Clone(),
					AllocatedCPUSet: machine.NewCPUSet(),
					PodEntries: PodEntries{
						"373d08e4-7a6b-4293-aaaf-b135ff8123bf": ContainerEntries{
							testName: &AllocationInfo{
								AllocationMeta: commonstate.AllocationMeta{
									PodUid:         "373d08e4-7a6b-4293-aaaf-b135ff8123bf",
									PodNamespace:   testName,
									PodName:        testName,
									ContainerName:  testName,
									ContainerType:  pluginapi.ContainerType_MAIN.String(),
									ContainerIndex: 0,
									OwnerPoolName:  commonstate.PoolNameShare,
									Labels: map[string]string{
										consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
									},
									Annotations: map[string]string{
										consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
									},
									QoSLevel: consts.PodAnnotationQoSLevelSharedCores,
								},
								RampUp:                   false,
								AllocationResult:         machine.MustParse("3,11"),
								OriginalAllocationResult: machine.MustParse("3,11"),
								TopologyAwareAssignments: map[int]machine.CPUSet{
									1: machine.NewCPUSet(3, 11),
								},
								OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
									1: machine.NewCPUSet(3, 11),
								},
								RequestQuantity: 2,
							},
						},
						"ec6e2f30-c78a-4bc4-9576-c916db5281a3": ContainerEntries{
							testName: &AllocationInfo{
								AllocationMeta: commonstate.AllocationMeta{
									PodUid:         "ec6e2f30-c78a-4bc4-9576-c916db5281a3",
									PodNamespace:   testName,
									PodName:        testName,
									ContainerName:  testName,
									ContainerType:  pluginapi.ContainerType_MAIN.String(),
									ContainerIndex: 0,
									OwnerPoolName:  commonstate.PoolNameShare,
									Labels: map[string]string{
										consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
									},
									Annotations: map[string]string{
										consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
									},
									QoSLevel: consts.PodAnnotationQoSLevelSharedCores,
								},

								RampUp:                   false,
								AllocationResult:         machine.MustParse("3,11"),
								OriginalAllocationResult: machine.MustParse("3,11"),
								TopologyAwareAssignments: map[int]machine.CPUSet{
									1: machine.NewCPUSet(3, 11),
								},
								OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
									1: machine.NewCPUSet(3, 11),
								},
								RequestQuantity: 2,
							},
						},
						"2432d068-c5a0-46ba-a7bd-b69d9bd16961": ContainerEntries{
							testName: &AllocationInfo{
								AllocationMeta: commonstate.AllocationMeta{
									PodUid:         "2432d068-c5a0-46ba-a7bd-b69d9bd16961",
									PodNamespace:   testName,
									PodName:        testName,
									ContainerName:  testName,
									ContainerType:  pluginapi.ContainerType_MAIN.String(),
									ContainerIndex: 0,
									OwnerPoolName:  commonstate.PoolNameReclaim,
									Labels: map[string]string{
										consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
									},
									Annotations: map[string]string{
										consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
									},
									QoSLevel: consts.PodAnnotationQoSLevelReclaimedCores,
								},
								RampUp:                   false,
								AllocationResult:         machine.MustParse("10"),
								OriginalAllocationResult: machine.MustParse("10"),
								TopologyAwareAssignments: map[int]machine.CPUSet{
									1: machine.NewCPUSet(10),
								},
								OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
									1: machine.NewCPUSet(10),
								},
								RequestQuantity: 2,
							},
						},
					},
				},
				2: &NUMANodeState{
					DefaultCPUSet:   cpuTopology.CPUDetails.CPUsInNUMANodes(2).Clone(),
					AllocatedCPUSet: machine.NewCPUSet(),
					PodEntries: PodEntries{
						"373d08e4-7a6b-4293-aaaf-b135ff8123bf": ContainerEntries{
							testName: &AllocationInfo{
								AllocationMeta: commonstate.AllocationMeta{
									PodUid:         "373d08e4-7a6b-4293-aaaf-b135ff8123bf",
									PodNamespace:   testName,
									PodName:        testName,
									ContainerName:  testName,
									ContainerType:  pluginapi.ContainerType_MAIN.String(),
									ContainerIndex: 0,
									OwnerPoolName:  commonstate.PoolNameShare,
									Labels: map[string]string{
										consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
									},
									Annotations: map[string]string{
										consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
									},
									QoSLevel: consts.PodAnnotationQoSLevelSharedCores,
								},
								RampUp:                   false,
								AllocationResult:         machine.MustParse("4,12"),
								OriginalAllocationResult: machine.MustParse("4,12"),
								TopologyAwareAssignments: map[int]machine.CPUSet{
									2: machine.NewCPUSet(4, 12),
								},
								OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
									2: machine.NewCPUSet(4, 12),
								},
								RequestQuantity: 2,
							},
						},
						"ec6e2f30-c78a-4bc4-9576-c916db5281a3": ContainerEntries{
							testName: &AllocationInfo{
								AllocationMeta: commonstate.AllocationMeta{
									PodUid:         "ec6e2f30-c78a-4bc4-9576-c916db5281a3",
									PodNamespace:   testName,
									PodName:        testName,
									ContainerName:  testName,
									ContainerType:  pluginapi.ContainerType_MAIN.String(),
									ContainerIndex: 0,
									OwnerPoolName:  commonstate.PoolNameShare,
									Labels: map[string]string{
										consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
									},
									Annotations: map[string]string{
										consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
									},
									QoSLevel: consts.PodAnnotationQoSLevelSharedCores,
								},
								RampUp:                   false,
								AllocationResult:         machine.MustParse("4,12"),
								OriginalAllocationResult: machine.MustParse("4,12"),
								TopologyAwareAssignments: map[int]machine.CPUSet{
									2: machine.NewCPUSet(4, 12),
								},
								OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
									2: machine.NewCPUSet(4, 12),
								},
								RequestQuantity: 2,
							},
						},
						"2432d068-c5a0-46ba-a7bd-b69d9bd16961": ContainerEntries{
							testName: &AllocationInfo{
								AllocationMeta: commonstate.AllocationMeta{
									PodUid:         "2432d068-c5a0-46ba-a7bd-b69d9bd16961",
									PodNamespace:   testName,
									PodName:        testName,
									ContainerName:  testName,
									ContainerType:  pluginapi.ContainerType_MAIN.String(),
									ContainerIndex: 0,
									OwnerPoolName:  commonstate.PoolNameReclaim,
									Labels: map[string]string{
										consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
									},
									Annotations: map[string]string{
										consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
									},
									QoSLevel: consts.PodAnnotationQoSLevelReclaimedCores,
								},
								RampUp:                   false,
								AllocationResult:         machine.MustParse("5,13"),
								OriginalAllocationResult: machine.MustParse("5,13"),
								TopologyAwareAssignments: map[int]machine.CPUSet{
									2: machine.NewCPUSet(5, 13),
								},
								OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
									2: machine.NewCPUSet(5, 13),
								},
								RequestQuantity: 2,
							},
						},
					},
				},
				3: &NUMANodeState{
					DefaultCPUSet:   cpuTopology.CPUDetails.CPUsInNUMANodes(3).Clone(),
					AllocatedCPUSet: machine.NewCPUSet(),
					PodEntries: PodEntries{
						"2432d068-c5a0-46ba-a7bd-b69d9bd16961": ContainerEntries{
							testName: &AllocationInfo{
								AllocationMeta: commonstate.AllocationMeta{
									PodUid:         "2432d068-c5a0-46ba-a7bd-b69d9bd16961",
									PodNamespace:   testName,
									PodName:        testName,
									ContainerName:  testName,
									ContainerType:  pluginapi.ContainerType_MAIN.String(),
									ContainerIndex: 0,
									OwnerPoolName:  commonstate.PoolNameReclaim,
									Labels: map[string]string{
										consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
									},
									Annotations: map[string]string{
										consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
									},
									QoSLevel: consts.PodAnnotationQoSLevelReclaimedCores,
								},
								RampUp:                   false,
								AllocationResult:         machine.MustParse("6,7,14,15"),
								OriginalAllocationResult: machine.MustParse("6,7,14,15"),
								TopologyAwareAssignments: map[int]machine.CPUSet{
									3: machine.NewCPUSet(6, 7, 14, 15),
								},
								OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
									3: machine.NewCPUSet(6, 7, 14, 15),
								},
								RequestQuantity: 2,
							},
						},
					},
				},
			},
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.description, func(t *testing.T) {
			t.Parallel()

			// create temp dir
			testingDir := t.TempDir()
			stateDirectoryConfig := &statedirectory.StateDirectoryConfiguration{
				StateFileDirectory: testingDir,
			}

			state1, err := NewCheckpointState(stateDirectoryConfig, cpuPluginStateFileName, policyName, tc.cpuTopology, false, GenerateMachineStateFromPodEntries, metrics.DummyMetrics{})
			as.Nil(err)

			state1.ClearState()

			state1.SetMachineState(tc.machineState, true)
			state1.SetPodEntries(tc.podEntries, true)

			state2, err := NewCheckpointState(stateDirectoryConfig, cpuPluginStateFileName, policyName, tc.cpuTopology, false, GenerateMachineStateFromPodEntries, metrics.DummyMetrics{})
			as.Nil(err)
			assertStateEqual(t, state2, state1)
		})
	}
}

func TestCheckpointStateHelpers(t *testing.T) {
	t.Parallel()

	as := require.New(t)

	testName := "test"

	cpuTopology, err := machine.GenerateDummyCPUTopology(16, 2, 4)
	as.Nil(err)

	testCases := []struct {
		description  string
		cpuTopology  *machine.CPUTopology
		podEntries   PodEntries
		machineState NUMANodeMap
	}{
		{
			description: "valid state cleaning",
			cpuTopology: cpuTopology,
			podEntries: PodEntries{
				"373d08e4-7a6b-4293-aaaf-b135ff8123bf": ContainerEntries{
					testName: &AllocationInfo{
						AllocationMeta: commonstate.AllocationMeta{
							PodUid:         "373d08e4-7a6b-4293-aaaf-b135ff8123bf",
							PodNamespace:   testName,
							PodName:        testName,
							ContainerName:  testName,
							ContainerType:  pluginapi.ContainerType_MAIN.String(),
							ContainerIndex: 0,
							OwnerPoolName:  commonstate.PoolNameShare,
							Labels: map[string]string{
								consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
							},
							Annotations: map[string]string{
								consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
							},
							QoSLevel: consts.PodAnnotationQoSLevelSharedCores,
						},
						RampUp:                   false,
						AllocationResult:         machine.MustParse("1,3-4,9,11-12"),
						OriginalAllocationResult: machine.MustParse("1,3-4,9,11-12"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(1, 9),
							1: machine.NewCPUSet(3, 11),
							2: machine.NewCPUSet(4, 12),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(1, 9),
							1: machine.NewCPUSet(3, 11),
							2: machine.NewCPUSet(4, 12),
						},
						RequestQuantity: 2,
					},
				},
				"ec6e2f30-c78a-4bc4-9576-c916db5281a3": ContainerEntries{
					testName: &AllocationInfo{
						AllocationMeta: commonstate.AllocationMeta{
							PodUid:         "ec6e2f30-c78a-4bc4-9576-c916db5281a3",
							PodNamespace:   testName,
							PodName:        testName,
							ContainerName:  testName,
							ContainerType:  pluginapi.ContainerType_MAIN.String(),
							ContainerIndex: 0,
							OwnerPoolName:  commonstate.PoolNameShare,
							Labels: map[string]string{
								consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
							},
							Annotations: map[string]string{
								consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
							},
							QoSLevel: consts.PodAnnotationQoSLevelSharedCores,
						},
						RampUp:                   false,
						AllocationResult:         machine.MustParse("1,3-4,9,11-12"),
						OriginalAllocationResult: machine.MustParse("1,3-4,9,11-12"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(1, 9),
							1: machine.NewCPUSet(3, 11),
							2: machine.NewCPUSet(4, 12),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(1, 9),
							1: machine.NewCPUSet(3, 11),
							2: machine.NewCPUSet(4, 12),
						},
						RequestQuantity: 2,
					},
				},
				"2432d068-c5a0-46ba-a7bd-b69d9bd16961": ContainerEntries{
					testName: &AllocationInfo{
						AllocationMeta: commonstate.AllocationMeta{
							PodUid:         "2432d068-c5a0-46ba-a7bd-b69d9bd16961",
							PodNamespace:   testName,
							PodName:        testName,
							ContainerName:  testName,
							ContainerType:  pluginapi.ContainerType_MAIN.String(),
							ContainerIndex: 0,
							OwnerPoolName:  commonstate.PoolNameReclaim,
							Labels: map[string]string{
								consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
							},
							Annotations: map[string]string{
								consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
							},
							QoSLevel: consts.PodAnnotationQoSLevelReclaimedCores,
						},
						RampUp:                   false,
						AllocationResult:         machine.MustParse("5-8,10,13-15"),
						OriginalAllocationResult: machine.MustParse("5-8,10,13-15"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(8),
							1: machine.NewCPUSet(10),
							2: machine.NewCPUSet(5, 13),
							3: machine.NewCPUSet(6, 7, 14, 15),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(8),
							1: machine.NewCPUSet(10),
							2: machine.NewCPUSet(5, 13),
							3: machine.NewCPUSet(6, 7, 14, 15),
						},
						RequestQuantity: 2,
					},
				},
				commonstate.PoolNameReclaim: ContainerEntries{
					"": &AllocationInfo{
						AllocationMeta:           commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
						AllocationResult:         machine.MustParse("5-8,10,13-15"),
						OriginalAllocationResult: machine.MustParse("5-8,10,13-15"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(8),
							1: machine.NewCPUSet(10),
							2: machine.NewCPUSet(5, 13),
							3: machine.NewCPUSet(6, 7, 14, 15),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(8),
							1: machine.NewCPUSet(10),
							2: machine.NewCPUSet(5, 13),
							3: machine.NewCPUSet(6, 7, 14, 15),
						},
					},
				},
				commonstate.PoolNameShare: ContainerEntries{
					"": &AllocationInfo{
						AllocationMeta:           commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameShare),
						AllocationResult:         machine.MustParse("1,3-4,9,11-12"),
						OriginalAllocationResult: machine.MustParse("1,3-4,9,11-12"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(1, 9),
							1: machine.NewCPUSet(3, 11),
							2: machine.NewCPUSet(4, 12),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(1, 9),
							1: machine.NewCPUSet(3, 11),
							2: machine.NewCPUSet(4, 12),
						},
					},
				},
			},
			machineState: NUMANodeMap{
				0: &NUMANodeState{
					DefaultCPUSet:   cpuTopology.CPUDetails.CPUsInNUMANodes(0).Clone(),
					AllocatedCPUSet: machine.NewCPUSet(),
					PodEntries: PodEntries{
						"373d08e4-7a6b-4293-aaaf-b135ff8123bf": ContainerEntries{
							testName: &AllocationInfo{
								AllocationMeta: commonstate.AllocationMeta{
									PodUid:         "373d08e4-7a6b-4293-aaaf-b135ff8123bf",
									PodNamespace:   testName,
									PodName:        testName,
									ContainerName:  testName,
									ContainerType:  pluginapi.ContainerType_MAIN.String(),
									ContainerIndex: 0,
									OwnerPoolName:  commonstate.PoolNameShare,
									Labels: map[string]string{
										consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
									},
									Annotations: map[string]string{
										consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
									},
									QoSLevel: consts.PodAnnotationQoSLevelSharedCores,
								},
								RampUp:                   false,
								AllocationResult:         machine.NewCPUSet(1, 9),
								OriginalAllocationResult: machine.NewCPUSet(1, 9),
								TopologyAwareAssignments: map[int]machine.CPUSet{
									0: machine.NewCPUSet(1, 9),
								},
								OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
									0: machine.NewCPUSet(1, 9),
								},
								RequestQuantity: 2,
							},
						},
						"ec6e2f30-c78a-4bc4-9576-c916db5281a3": ContainerEntries{
							testName: &AllocationInfo{
								AllocationMeta: commonstate.AllocationMeta{
									PodUid:         "ec6e2f30-c78a-4bc4-9576-c916db5281a3",
									PodNamespace:   testName,
									PodName:        testName,
									ContainerName:  testName,
									ContainerType:  pluginapi.ContainerType_MAIN.String(),
									ContainerIndex: 0,
									OwnerPoolName:  commonstate.PoolNameShare,
									Labels: map[string]string{
										consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
									},
									Annotations: map[string]string{
										consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
									},
									QoSLevel: consts.PodAnnotationQoSLevelSharedCores,
								},
								RampUp:                   false,
								AllocationResult:         machine.NewCPUSet(1, 9),
								OriginalAllocationResult: machine.NewCPUSet(1, 9),
								TopologyAwareAssignments: map[int]machine.CPUSet{
									0: machine.NewCPUSet(1, 9),
								},
								OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
									0: machine.NewCPUSet(1, 9),
								},
								RequestQuantity: 2,
							},
						},
						"2432d068-c5a0-46ba-a7bd-b69d9bd16961": ContainerEntries{
							testName: &AllocationInfo{
								AllocationMeta: commonstate.AllocationMeta{
									PodUid:         "2432d068-c5a0-46ba-a7bd-b69d9bd16961",
									PodNamespace:   testName,
									PodName:        testName,
									ContainerName:  testName,
									ContainerType:  pluginapi.ContainerType_MAIN.String(),
									ContainerIndex: 0,
									OwnerPoolName:  commonstate.PoolNameReclaim,
									Labels: map[string]string{
										consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
									},
									Annotations: map[string]string{
										consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
									},
									QoSLevel: consts.PodAnnotationQoSLevelReclaimedCores,
								},
								RampUp:                   false,
								AllocationResult:         machine.MustParse("8"),
								OriginalAllocationResult: machine.MustParse("8"),
								TopologyAwareAssignments: map[int]machine.CPUSet{
									0: machine.NewCPUSet(8),
								},
								OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
									0: machine.NewCPUSet(8),
								},
								RequestQuantity: 2,
							},
						},
					},
				},
				1: &NUMANodeState{
					DefaultCPUSet:   cpuTopology.CPUDetails.CPUsInNUMANodes(1).Clone(),
					AllocatedCPUSet: machine.NewCPUSet(),
					PodEntries: PodEntries{
						"373d08e4-7a6b-4293-aaaf-b135ff8123bf": ContainerEntries{
							testName: &AllocationInfo{
								AllocationMeta: commonstate.AllocationMeta{
									PodUid:         "373d08e4-7a6b-4293-aaaf-b135ff8123bf",
									PodNamespace:   testName,
									PodName:        testName,
									ContainerName:  testName,
									ContainerType:  pluginapi.ContainerType_MAIN.String(),
									ContainerIndex: 0,
									OwnerPoolName:  commonstate.PoolNameShare,
									Labels: map[string]string{
										consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
									},
									Annotations: map[string]string{
										consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
									},
									QoSLevel: consts.PodAnnotationQoSLevelSharedCores,
								},
								RampUp:                   false,
								AllocationResult:         machine.MustParse("3,11"),
								OriginalAllocationResult: machine.MustParse("3,11"),
								TopologyAwareAssignments: map[int]machine.CPUSet{
									1: machine.NewCPUSet(3, 11),
								},
								OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
									1: machine.NewCPUSet(3, 11),
								},
								RequestQuantity: 2,
							},
						},
						"ec6e2f30-c78a-4bc4-9576-c916db5281a3": ContainerEntries{
							testName: &AllocationInfo{
								AllocationMeta: commonstate.AllocationMeta{
									PodUid:         "ec6e2f30-c78a-4bc4-9576-c916db5281a3",
									PodNamespace:   testName,
									PodName:        testName,
									ContainerName:  testName,
									ContainerType:  pluginapi.ContainerType_MAIN.String(),
									ContainerIndex: 0,
									OwnerPoolName:  commonstate.PoolNameShare,
									Labels: map[string]string{
										consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
									},
									Annotations: map[string]string{
										consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
									},
									QoSLevel: consts.PodAnnotationQoSLevelSharedCores,
								},
								RampUp:                   false,
								AllocationResult:         machine.MustParse("3,11"),
								OriginalAllocationResult: machine.MustParse("3,11"),
								TopologyAwareAssignments: map[int]machine.CPUSet{
									1: machine.NewCPUSet(3, 11),
								},
								OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
									1: machine.NewCPUSet(3, 11),
								},
								RequestQuantity: 2,
							},
						},
						"2432d068-c5a0-46ba-a7bd-b69d9bd16961": ContainerEntries{
							testName: &AllocationInfo{
								AllocationMeta: commonstate.AllocationMeta{
									PodUid:         "2432d068-c5a0-46ba-a7bd-b69d9bd16961",
									PodNamespace:   testName,
									PodName:        testName,
									ContainerName:  testName,
									ContainerType:  pluginapi.ContainerType_MAIN.String(),
									ContainerIndex: 0,
									OwnerPoolName:  commonstate.PoolNameReclaim,
									Labels: map[string]string{
										consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
									},
									Annotations: map[string]string{
										consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
									},
									QoSLevel: consts.PodAnnotationQoSLevelReclaimedCores,
								},
								RampUp:                   false,
								AllocationResult:         machine.MustParse("10"),
								OriginalAllocationResult: machine.MustParse("10"),
								TopologyAwareAssignments: map[int]machine.CPUSet{
									1: machine.NewCPUSet(10),
								},
								OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
									1: machine.NewCPUSet(10),
								},
								RequestQuantity: 2,
							},
						},
					},
				},
				2: &NUMANodeState{
					DefaultCPUSet:   cpuTopology.CPUDetails.CPUsInNUMANodes(2).Clone(),
					AllocatedCPUSet: machine.NewCPUSet(),
					PodEntries: PodEntries{
						"373d08e4-7a6b-4293-aaaf-b135ff8123bf": ContainerEntries{
							testName: &AllocationInfo{
								AllocationMeta: commonstate.AllocationMeta{
									PodUid:         "373d08e4-7a6b-4293-aaaf-b135ff8123bf",
									PodNamespace:   testName,
									PodName:        testName,
									ContainerName:  testName,
									ContainerType:  pluginapi.ContainerType_MAIN.String(),
									ContainerIndex: 0,
									OwnerPoolName:  commonstate.PoolNameShare,
									Labels: map[string]string{
										consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
									},
									Annotations: map[string]string{
										consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
									},
									QoSLevel: consts.PodAnnotationQoSLevelSharedCores,
								},
								RampUp:                   false,
								AllocationResult:         machine.MustParse("4,12"),
								OriginalAllocationResult: machine.MustParse("4,12"),
								TopologyAwareAssignments: map[int]machine.CPUSet{
									2: machine.NewCPUSet(4, 12),
								},
								OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
									2: machine.NewCPUSet(4, 12),
								},
								RequestQuantity: 2,
							},
						},
						"ec6e2f30-c78a-4bc4-9576-c916db5281a3": ContainerEntries{
							testName: &AllocationInfo{
								AllocationMeta: commonstate.AllocationMeta{
									PodUid:         "ec6e2f30-c78a-4bc4-9576-c916db5281a3",
									PodNamespace:   testName,
									PodName:        testName,
									ContainerName:  testName,
									ContainerType:  pluginapi.ContainerType_MAIN.String(),
									ContainerIndex: 0,
									OwnerPoolName:  commonstate.PoolNameShare,
									Labels: map[string]string{
										consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
									},
									Annotations: map[string]string{
										consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
									},
									QoSLevel: consts.PodAnnotationQoSLevelSharedCores,
								},
								RampUp:                   false,
								AllocationResult:         machine.MustParse("4,12"),
								OriginalAllocationResult: machine.MustParse("4,12"),
								TopologyAwareAssignments: map[int]machine.CPUSet{
									2: machine.NewCPUSet(4, 12),
								},
								OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
									2: machine.NewCPUSet(4, 12),
								},
								RequestQuantity: 2,
							},
						},
						"2432d068-c5a0-46ba-a7bd-b69d9bd16961": ContainerEntries{
							testName: &AllocationInfo{
								AllocationMeta: commonstate.AllocationMeta{
									PodUid:         "2432d068-c5a0-46ba-a7bd-b69d9bd16961",
									PodNamespace:   testName,
									PodName:        testName,
									ContainerName:  testName,
									ContainerType:  pluginapi.ContainerType_MAIN.String(),
									ContainerIndex: 0,
									OwnerPoolName:  commonstate.PoolNameReclaim,
									Labels: map[string]string{
										consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
									},
									Annotations: map[string]string{
										consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
									},
									QoSLevel: consts.PodAnnotationQoSLevelReclaimedCores,
								},
								RampUp:                   false,
								AllocationResult:         machine.MustParse("5,13"),
								OriginalAllocationResult: machine.MustParse("5,13"),
								TopologyAwareAssignments: map[int]machine.CPUSet{
									2: machine.NewCPUSet(5, 13),
								},
								OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
									2: machine.NewCPUSet(5, 13),
								},
								RequestQuantity: 2,
							},
						},
					},
				},
				3: &NUMANodeState{
					DefaultCPUSet:   cpuTopology.CPUDetails.CPUsInNUMANodes(3).Clone(),
					AllocatedCPUSet: machine.NewCPUSet(),
					PodEntries: PodEntries{
						"2432d068-c5a0-46ba-a7bd-b69d9bd16961": ContainerEntries{
							testName: &AllocationInfo{
								AllocationMeta: commonstate.AllocationMeta{
									PodUid:         "2432d068-c5a0-46ba-a7bd-b69d9bd16961",
									PodNamespace:   testName,
									PodName:        testName,
									ContainerName:  testName,
									ContainerType:  pluginapi.ContainerType_MAIN.String(),
									ContainerIndex: 0,
									OwnerPoolName:  commonstate.PoolNameReclaim,
									Labels: map[string]string{
										consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
									},
									Annotations: map[string]string{
										consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
									},
									QoSLevel: consts.PodAnnotationQoSLevelReclaimedCores,
								},
								RampUp:                   false,
								AllocationResult:         machine.MustParse("6,7,14,15"),
								OriginalAllocationResult: machine.MustParse("6,7,14,15"),
								TopologyAwareAssignments: map[int]machine.CPUSet{
									3: machine.NewCPUSet(6, 7, 14, 15),
								},
								OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
									3: machine.NewCPUSet(6, 7, 14, 15),
								},
								RequestQuantity: 2,
							},
						},
					},
				},
			},
		},
	}

	// create temp dir
	testingDir := t.TempDir()
	stateDirectoryConfig := &statedirectory.StateDirectoryConfiguration{
		StateFileDirectory: testingDir,
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.description, func(t *testing.T) {
			t.Parallel()

			state, err := NewCheckpointState(stateDirectoryConfig, cpuPluginStateFileName, policyName, tc.cpuTopology, false, GenerateMachineStateFromPodEntries, metrics.DummyMetrics{})
			as.Nil(err)

			state.ClearState()

			state.SetMachineState(tc.machineState, true)
			as.Equalf(tc.machineState, state.GetMachineState(), "failed in test case: %s", tc.description)

			state.SetPodEntries(tc.podEntries, true)
			as.Equalf(tc.podEntries, state.GetPodEntries(), "failed in test case: %s", tc.description)

			state.ClearState()

			as.NotEqualf(tc.podEntries, state.GetPodEntries(), "failed in test case: %s", tc.description)
			for podUID, containerEntries := range tc.podEntries {
				for containerName, allocationInfo := range containerEntries {
					state.SetAllocationInfo(podUID, containerName, allocationInfo, true)
				}
			}
			as.Equalf(tc.podEntries, state.GetPodEntries(), "failed in test case: %s", tc.description)
		})
	}
}

func TestGetDefaultMachineState(t *testing.T) {
	t.Parallel()

	as := require.New(t)

	cpuTopology, err := machine.GenerateDummyCPUTopology(16, 2, 4)
	as.Nil(err)

	testCases := []struct {
		description          string
		cpuTopology          *machine.CPUTopology
		expectedMachineState NUMANodeMap
	}{
		{
			description: "nil cpuTopology",
		},
		{
			description: "non-nil cpuTopology",
			cpuTopology: cpuTopology,
			expectedMachineState: NUMANodeMap{
				0: &NUMANodeState{
					DefaultCPUSet:   machine.NewCPUSet(0, 1, 8, 9),
					AllocatedCPUSet: machine.NewCPUSet(),
					PodEntries:      make(PodEntries),
				},
				1: &NUMANodeState{
					DefaultCPUSet:   machine.NewCPUSet(2, 3, 10, 11),
					AllocatedCPUSet: machine.NewCPUSet(),
					PodEntries:      make(PodEntries),
				},
				2: &NUMANodeState{
					DefaultCPUSet:   machine.NewCPUSet(4, 5, 12, 13),
					AllocatedCPUSet: machine.NewCPUSet(),
					PodEntries:      make(PodEntries),
				},
				3: &NUMANodeState{
					DefaultCPUSet:   machine.NewCPUSet(6, 7, 14, 15),
					AllocatedCPUSet: machine.NewCPUSet(),
					PodEntries:      make(PodEntries),
				},
			},
		},
	}

	for _, tc := range testCases {
		actualMachineState := GetDefaultMachineState(tc.cpuTopology)
		as.Equalf(actualMachineState, tc.expectedMachineState, "failed in test case: %s", tc.description)
	}
}

func TestGetSocketTopology(t *testing.T) {
	t.Parallel()

	as := require.New(t)

	cpuTopology, err := machine.GenerateDummyCPUTopology(16, 2, 4)
	as.Nil(err)

	testCases := []struct {
		description            string
		cpuTopology            *machine.CPUTopology
		expectedSocketTopology map[int]string
	}{
		{
			description: "nil cpuTopology",
		},
		{
			description: "non-nil cpuTopology",
			cpuTopology: cpuTopology,
			expectedSocketTopology: map[int]string{
				0: "0-1",
				1: "2-3",
			},
		},
	}

	for _, tc := range testCases {
		actualSocketToplogy := tc.cpuTopology.GetSocketTopology()
		as.Equalf(tc.expectedSocketTopology, actualSocketToplogy, "failed in test case: %s", tc.description)
	}
}

func TestAllocationInfo_GetSpecifiedNUMABindingPoolName(t *testing.T) {
	t.Parallel()
	testName := "test"

	type fields struct {
		PodUid                           string
		PodNamespace                     string
		PodName                          string
		ContainerName                    string
		ContainerType                    string
		ContainerIndex                   uint64
		RampUp                           bool
		OwnerPoolName                    string
		PodRole                          string
		PodType                          string
		AllocationResult                 machine.CPUSet
		OriginalAllocationResult         machine.CPUSet
		TopologyAwareAssignments         map[int]machine.CPUSet
		OriginalTopologyAwareAssignments map[int]machine.CPUSet
		InitTimestamp                    string
		Labels                           map[string]string
		Annotations                      map[string]string
		QoSLevel                         string
		RequestQuantity                  float64
	}
	tests := []struct {
		name    string
		fields  fields
		want    string
		wantErr bool
	}{
		{
			name: "shared_cores with numa_binding pod get pool name normally",
			fields: fields{
				PodUid:                   "373d08e4-7a6b-4293-aaaf-b135ff8123bf",
				PodNamespace:             testName,
				PodName:                  testName,
				ContainerName:            testName,
				ContainerType:            pluginapi.ContainerType_MAIN.String(),
				ContainerIndex:           0,
				RampUp:                   false,
				OwnerPoolName:            commonstate.PoolNameShare,
				AllocationResult:         machine.MustParse("1"),
				OriginalAllocationResult: machine.MustParse("1"),
				TopologyAwareAssignments: map[int]machine.CPUSet{
					0: machine.NewCPUSet(1),
				},
				OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
					0: machine.NewCPUSet(1),
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:                  consts.PodAnnotationQoSLevelSharedCores,
					consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
					cpuconsts.CPUStateAnnotationKeyNUMAHint:          "0",
				},
				QoSLevel:        consts.PodAnnotationQoSLevelSharedCores,
				RequestQuantity: 1,
			},
			want:    "share-NUMA0",
			wantErr: false,
		},
		{
			name: "dedicated_cores with numa_binding pod get pool name failed",
			fields: fields{
				PodUid:                   "373d08e4-7a6b-4293-aaaf-b135ff8123bf",
				PodNamespace:             testName,
				PodName:                  testName,
				ContainerName:            testName,
				ContainerType:            pluginapi.ContainerType_MAIN.String(),
				ContainerIndex:           0,
				RampUp:                   false,
				OwnerPoolName:            commonstate.PoolNameShare,
				AllocationResult:         machine.MustParse("1"),
				OriginalAllocationResult: machine.MustParse("1"),
				TopologyAwareAssignments: map[int]machine.CPUSet{
					0: machine.NewCPUSet(1),
				},
				OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
					0: machine.NewCPUSet(1),
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:                  consts.PodAnnotationQoSLevelSharedCores,
					consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
				},
				QoSLevel:        consts.PodAnnotationQoSLevelDedicatedCores,
				RequestQuantity: 1,
			},
			want:    "",
			wantErr: true,
		},
		{
			name: "shared_cores with numa_binding pod without hint annotation get pool name failed",
			fields: fields{
				PodUid:                   "373d08e4-7a6b-4293-aaaf-b135ff8123bf",
				PodNamespace:             testName,
				PodName:                  testName,
				ContainerName:            testName,
				ContainerType:            pluginapi.ContainerType_MAIN.String(),
				ContainerIndex:           0,
				RampUp:                   false,
				OwnerPoolName:            commonstate.PoolNameShare,
				AllocationResult:         machine.MustParse("1"),
				OriginalAllocationResult: machine.MustParse("1"),
				TopologyAwareAssignments: map[int]machine.CPUSet{
					0: machine.NewCPUSet(1),
				},
				OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
					0: machine.NewCPUSet(1),
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:                  consts.PodAnnotationQoSLevelSharedCores,
					consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
				},
				QoSLevel:        consts.PodAnnotationQoSLevelSharedCores,
				RequestQuantity: 1,
			},
			want:    "",
			wantErr: true,
		},
		{
			name: "shared_cores with numa_binding pod with invalid hint annotation get pool name failed",
			fields: fields{
				PodUid:                   "373d08e4-7a6b-4293-aaaf-b135ff8123bf",
				PodNamespace:             testName,
				PodName:                  testName,
				ContainerName:            testName,
				ContainerType:            pluginapi.ContainerType_MAIN.String(),
				ContainerIndex:           0,
				RampUp:                   false,
				OwnerPoolName:            commonstate.PoolNameShare,
				AllocationResult:         machine.MustParse("1"),
				OriginalAllocationResult: machine.MustParse("1"),
				TopologyAwareAssignments: map[int]machine.CPUSet{
					0: machine.NewCPUSet(1),
				},
				OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
					0: machine.NewCPUSet(1),
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:                  consts.PodAnnotationQoSLevelSharedCores,
					consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
					cpuconsts.CPUStateAnnotationKeyNUMAHint:          "0-1",
				},
				QoSLevel:        consts.PodAnnotationQoSLevelSharedCores,
				RequestQuantity: 1,
			},
			want:    "",
			wantErr: true,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ai := &AllocationInfo{
				AllocationMeta: commonstate.AllocationMeta{
					PodUid:         tt.fields.PodUid,
					PodNamespace:   tt.fields.PodNamespace,
					PodName:        tt.fields.PodName,
					ContainerName:  tt.fields.ContainerName,
					ContainerType:  tt.fields.ContainerType,
					ContainerIndex: tt.fields.ContainerIndex,
					OwnerPoolName:  tt.fields.OwnerPoolName,
					PodRole:        tt.fields.PodRole,
					PodType:        tt.fields.PodType,
					Labels:         tt.fields.Labels,
					Annotations:    tt.fields.Annotations,
					QoSLevel:       tt.fields.QoSLevel,
				},
				RampUp:                           tt.fields.RampUp,
				AllocationResult:                 tt.fields.AllocationResult,
				OriginalAllocationResult:         tt.fields.OriginalAllocationResult,
				TopologyAwareAssignments:         tt.fields.TopologyAwareAssignments,
				OriginalTopologyAwareAssignments: tt.fields.OriginalTopologyAwareAssignments,
				InitTimestamp:                    tt.fields.InitTimestamp,
				RequestQuantity:                  tt.fields.RequestQuantity,
			}
			got, err := ai.GetSpecifiedNUMABindingPoolName()
			if (err != nil) != tt.wantErr {
				t.Errorf("AllocationInfo.GetSpecifiedNUMABindingPoolName() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("AllocationInfo.GetSpecifiedNUMABindingPoolName() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestPodEntries_GetFilteredPoolsCPUSetMap(t *testing.T) {
	t.Parallel()
	testName := "test"
	type args struct {
		poolFilter func(name string) bool
	}
	tests := []struct {
		name    string
		pe      PodEntries
		args    args
		want    map[string]map[int]machine.CPUSet
		wantErr bool
	}{
		{
			name: "get filtered pools cpuset map normally",
			pe: PodEntries{
				"373d08e4-7a6b-4293-aaaf-b135ff8123bf": ContainerEntries{
					testName: &AllocationInfo{
						AllocationMeta: commonstate.AllocationMeta{
							PodUid:         "373d08e4-7a6b-4293-aaaf-b135ff8123bf",
							PodNamespace:   testName,
							PodName:        testName,
							ContainerName:  testName,
							ContainerType:  pluginapi.ContainerType_MAIN.String(),
							ContainerIndex: 0,
							OwnerPoolName:  commonstate.PoolNameShare,
							Labels: map[string]string{
								consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
							},
							Annotations: map[string]string{
								consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
							},
							QoSLevel: consts.PodAnnotationQoSLevelSharedCores,
						},
						RampUp:                   false,
						AllocationResult:         machine.MustParse("1,3-4,9,11-12"),
						OriginalAllocationResult: machine.MustParse("1,3-4,9,11-12"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(1, 9),
							1: machine.NewCPUSet(3, 11),
							2: machine.NewCPUSet(4, 12),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(1, 9),
							1: machine.NewCPUSet(3, 11),
							2: machine.NewCPUSet(4, 12),
						},
						RequestQuantity: 2,
					},
				},
				"ec6e2f30-c78a-4bc4-9576-c916db5281a3": ContainerEntries{
					testName: &AllocationInfo{
						AllocationMeta: commonstate.AllocationMeta{
							PodUid:         "ec6e2f30-c78a-4bc4-9576-c916db5281a3",
							PodNamespace:   testName,
							PodName:        testName,
							ContainerName:  testName,
							ContainerType:  pluginapi.ContainerType_MAIN.String(),
							ContainerIndex: 0,
							OwnerPoolName:  commonstate.PoolNameShare,
							Labels: map[string]string{
								consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
							},
							Annotations: map[string]string{
								consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
							},
							QoSLevel: consts.PodAnnotationQoSLevelSharedCores,
						},
						RampUp:                   false,
						AllocationResult:         machine.MustParse("1,3-4,9,11-12"),
						OriginalAllocationResult: machine.MustParse("1,3-4,9,11-12"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(1, 9),
							1: machine.NewCPUSet(3, 11),
							2: machine.NewCPUSet(4, 12),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(1, 9),
							1: machine.NewCPUSet(3, 11),
							2: machine.NewCPUSet(4, 12),
						},
						RequestQuantity: 2,
					},
				},
				"2432d068-c5a0-46ba-a7bd-b69d9bd16961": ContainerEntries{
					testName: &AllocationInfo{
						AllocationMeta: commonstate.AllocationMeta{
							PodUid:         "2432d068-c5a0-46ba-a7bd-b69d9bd16961",
							PodNamespace:   testName,
							PodName:        testName,
							ContainerName:  testName,
							ContainerType:  pluginapi.ContainerType_MAIN.String(),
							ContainerIndex: 0,
							OwnerPoolName:  commonstate.PoolNameReclaim,
							Labels: map[string]string{
								consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
							},
							Annotations: map[string]string{
								consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
							},
							QoSLevel: consts.PodAnnotationQoSLevelReclaimedCores,
						},
						RampUp:                   false,
						AllocationResult:         machine.MustParse("5,8,10,13,15"),
						OriginalAllocationResult: machine.MustParse("5,8,10,13,15"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(8),
							1: machine.NewCPUSet(10),
							2: machine.NewCPUSet(5, 13),
							3: machine.NewCPUSet(15),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(8),
							1: machine.NewCPUSet(10),
							2: machine.NewCPUSet(5, 13),
							3: machine.NewCPUSet(15),
						},
						RequestQuantity: 2,
					},
				},
				commonstate.PoolNameReclaim: ContainerEntries{
					"": &AllocationInfo{
						AllocationMeta:           commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
						AllocationResult:         machine.MustParse("5,8,10,13,15"),
						OriginalAllocationResult: machine.MustParse("5,8,10,13,15"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(8),
							1: machine.NewCPUSet(10),
							2: machine.NewCPUSet(5, 13),
							3: machine.NewCPUSet(15),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(8),
							1: machine.NewCPUSet(10),
							2: machine.NewCPUSet(5, 13),
							3: machine.NewCPUSet(15),
						},
					},
				},
				commonstate.PoolNameShare: ContainerEntries{
					"": &AllocationInfo{
						AllocationMeta:           commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameShare),
						AllocationResult:         machine.MustParse("1,3-4,9,11-12"),
						OriginalAllocationResult: machine.MustParse("1,3-4,9,11-12"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(1, 9),
							1: machine.NewCPUSet(3, 11),
							2: machine.NewCPUSet(4, 12),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(1, 9),
							1: machine.NewCPUSet(3, 11),
							2: machine.NewCPUSet(4, 12),
						},
					},
				},
				"share-NUMA3": ContainerEntries{
					"": &AllocationInfo{
						AllocationMeta:           generateSharedNumaBindingPoolAllocationMeta("share-NUMA3"),
						AllocationResult:         machine.MustParse("6,7,14"),
						OriginalAllocationResult: machine.MustParse("6,7,14"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							3: machine.NewCPUSet(6, 7, 14),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							3: machine.NewCPUSet(6, 7, 14),
						},
					},
				},
				"373d08e4-7a6b-4293-aaaf-b135ff812aaa": ContainerEntries{
					testName: &AllocationInfo{
						AllocationMeta: commonstate.AllocationMeta{
							PodUid:         "373d08e4-7a6b-4293-aaaf-b135ff812aaa",
							PodNamespace:   testName,
							PodName:        testName,
							ContainerName:  testName,
							ContainerType:  pluginapi.ContainerType_MAIN.String(),
							ContainerIndex: 0,
							OwnerPoolName:  "share-NUMA3",
							Labels: map[string]string{
								consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
							},
							Annotations: map[string]string{
								consts.PodAnnotationQoSLevelKey:                  consts.PodAnnotationQoSLevelSharedCores,
								consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
							},
							QoSLevel: consts.PodAnnotationQoSLevelSharedCores,
						},
						RampUp:                   false,
						AllocationResult:         machine.MustParse("6,7,14"),
						OriginalAllocationResult: machine.MustParse("6,7,14"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							3: machine.NewCPUSet(6, 7, 14),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							3: machine.NewCPUSet(6, 7, 14),
						},
						RequestQuantity: 2,
					},
				},
			},
			args: args{
				poolFilter: func(name string) bool {
					return ResidentPools.Has(name)
				},
			},
			want: map[string]map[int]machine.CPUSet{
				"share": {
					commonstate.FakedNUMAID: machine.MustParse("1,3-4,9,11-12"),
				},
				"share-NUMA3": {
					3: machine.MustParse("6,7,14"),
				},
			},
			wantErr: false,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := tt.pe.GetFilteredPoolsCPUSetMap(tt.args.poolFilter)
			if (err != nil) != tt.wantErr {
				t.Errorf("PodEntries.GetFilteredPoolsCPUSetMap() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("PodEntries.GetFilteredPoolsCPUSetMap() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestGetAggregatedRequest(t *testing.T) {
	t.Parallel()

	allocation := &AllocationInfo{
		AllocationMeta: commonstate.AllocationMeta{},
	}
	_, ok := allocation.GetPodAggregatedRequest()
	require.Equal(t, false, ok)

	allocation.Annotations = map[string]string{}
	_, ok = allocation.GetPodAggregatedRequest()
	require.Equal(t, false, ok)

	allocation.Annotations = map[string]string{
		apiconsts.PodAnnotationAggregatedRequestsKey: "",
	}
	_, ok = allocation.GetPodAggregatedRequest()
	require.Equal(t, false, ok)

	require.Equal(t, false, ok)
	allocation.Annotations = map[string]string{
		apiconsts.PodAnnotationAggregatedRequestsKey: "{\"cpu\": \"\"}",
	}
	_, ok = allocation.GetPodAggregatedRequest()
	require.Equal(t, false, ok)

	allocation.Annotations = map[string]string{
		apiconsts.PodAnnotationAggregatedRequestsKey: "{\"cpu\": \"6\"}",
	}
	req, ok := allocation.GetPodAggregatedRequest()
	require.Equal(t, true, ok)
	require.Equal(t, req, float64(6))
}

func TestGetAvailableCPUQuantity(t *testing.T) {
	t.Parallel()

	cpuTopology, err := machine.GenerateDummyCPUTopology(48, 1, 2)
	require.NoError(t, err)

	testName := "test"
	sidecarName := "sidecar"
	nodeState := &NUMANodeState{
		DefaultCPUSet:   cpuTopology.CPUDetails.CPUsInNUMANodes(1).Clone(),
		AllocatedCPUSet: machine.NewCPUSet(),
		PodEntries: PodEntries{
			"373d08e4-7a6b-4293-aaaf-b135ff8123bf": ContainerEntries{
				testName: &AllocationInfo{
					AllocationMeta: commonstate.AllocationMeta{
						PodUid:         "373d08e4-7a6b-4293-aaaf-b135ff8123bf",
						PodNamespace:   testName,
						PodName:        testName,
						ContainerName:  testName,
						ContainerType:  pluginapi.ContainerType_MAIN.String(),
						ContainerIndex: 0,
						OwnerPoolName:  commonstate.PoolNameShare,
						Labels: map[string]string{
							consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
						},
						Annotations: map[string]string{
							consts.PodAnnotationQoSLevelKey:                  consts.PodAnnotationQoSLevelSharedCores,
							consts.PodAnnotationAggregatedRequestsKey:        "{\"cpu\":\"5\"}",
							consts.PodAnnotationMemoryEnhancementNumaBinding: "true",
						},
						QoSLevel: consts.PodAnnotationQoSLevelSharedCores,
					},
					RampUp:                   false,
					AllocationResult:         machine.MustParse("3,11"),
					OriginalAllocationResult: machine.MustParse("3,11"),
					TopologyAwareAssignments: map[int]machine.CPUSet{
						1: machine.NewCPUSet(3, 11),
					},
					OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
						1: machine.NewCPUSet(3, 11),
					},
					RequestQuantity: 2,
				},
				sidecarName: &AllocationInfo{
					AllocationMeta: commonstate.AllocationMeta{
						PodUid:         "ec6e2f30-c78a-4bc4-9576-c916db5281a3",
						PodNamespace:   testName,
						PodName:        testName,
						ContainerName:  sidecarName,
						ContainerType:  pluginapi.ContainerType_SIDECAR.String(),
						ContainerIndex: 0,
						OwnerPoolName:  commonstate.PoolNameShare,
						Labels: map[string]string{
							consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
						},
						Annotations: map[string]string{
							consts.PodAnnotationQoSLevelKey:                  consts.PodAnnotationQoSLevelSharedCores,
							consts.PodAnnotationMemoryEnhancementNumaBinding: "true",
						},
						QoSLevel: consts.PodAnnotationQoSLevelSharedCores,
					},
					RampUp:                   false,
					AllocationResult:         machine.MustParse("3,11"),
					OriginalAllocationResult: machine.MustParse("3,11"),
					TopologyAwareAssignments: map[int]machine.CPUSet{
						1: machine.NewCPUSet(3, 11),
					},
					OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
						1: machine.NewCPUSet(3, 11),
					},
					RequestQuantity: 2,
				},
			},
			"ec6e2f30-c78a-4bc4-9576-c916db5281a3": ContainerEntries{
				testName: &AllocationInfo{
					AllocationMeta: commonstate.AllocationMeta{
						PodUid:         "ec6e2f30-c78a-4bc4-9576-c916db5281a3",
						PodNamespace:   testName,
						PodName:        testName,
						ContainerName:  testName,
						ContainerType:  pluginapi.ContainerType_MAIN.String(),
						ContainerIndex: 0,
						OwnerPoolName:  commonstate.PoolNameShare,
						Labels: map[string]string{
							consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
						},
						Annotations: map[string]string{
							consts.PodAnnotationQoSLevelKey:                  consts.PodAnnotationQoSLevelSharedCores,
							consts.PodAnnotationMemoryEnhancementNumaBinding: "true",
						},
						QoSLevel: consts.PodAnnotationQoSLevelSharedCores,
					},
					RampUp:                   false,
					AllocationResult:         machine.MustParse("3,11"),
					OriginalAllocationResult: machine.MustParse("3,11"),
					TopologyAwareAssignments: map[int]machine.CPUSet{
						1: machine.NewCPUSet(3, 11),
					},
					OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
						1: machine.NewCPUSet(3, 11),
					},
					RequestQuantity: 2,
				},
				sidecarName: &AllocationInfo{
					AllocationMeta: commonstate.AllocationMeta{
						PodUid:         "ec6e2f30-c78a-4bc4-9576-c916db5281a3",
						PodNamespace:   testName,
						PodName:        testName,
						ContainerName:  sidecarName,
						ContainerType:  pluginapi.ContainerType_SIDECAR.String(),
						ContainerIndex: 0,
						OwnerPoolName:  commonstate.PoolNameShare,
						Labels: map[string]string{
							consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
						},
						Annotations: map[string]string{
							consts.PodAnnotationQoSLevelKey:                  consts.PodAnnotationQoSLevelSharedCores,
							consts.PodAnnotationMemoryEnhancementNumaBinding: "true",
						},
						QoSLevel: consts.PodAnnotationQoSLevelSharedCores,
					},
					RampUp:                   false,
					AllocationResult:         machine.MustParse("3,11"),
					OriginalAllocationResult: machine.MustParse("3,11"),
					TopologyAwareAssignments: map[int]machine.CPUSet{
						1: machine.NewCPUSet(3, 11),
					},
					OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
						1: machine.NewCPUSet(3, 11),
					},
					RequestQuantity: 2,
				},
			},
		},
	}
	cpuQuantity := int(math.Ceil(nodeState.GetAvailableCPUQuantity(machine.NewCPUSet())))
	require.Equal(t, 15, cpuQuantity)
}

func TestGetReadonlyState(t *testing.T) {
	t.Parallel()

	as := require.New(t)
	state, err := GetReadonlyState()
	as.NotNil(err)
	as.Nil(state)
}

func TestGetWriteOnlyState(t *testing.T) {
	t.Parallel()

	as := require.New(t)
	state, err := GetReadWriteState()
	if state == nil {
		as.NotNil(err)
	}
}

func generateTestMachineStateFromPodEntries(
	topology *machine.CPUTopology, _ PodEntries, _ NUMANodeMap,
) (NUMANodeMap, error) {
	return GetDefaultMachineState(topology), nil
}

func TestNewCPUPluginCheckpoint(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		corruptFile bool
		expectEqual bool
	}{
		{
			name:        "successful migration with pre-stop",
			corruptFile: false,
			expectEqual: true,
		},
		{
			name:        "corrupted checkpoint",
			corruptFile: true,
			expectEqual: false,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			tmpDir := t.TempDir()
			inMemoryTmpDir := t.TempDir()

			stateDir := filepath.Join(tmpDir, "state")
			err := os.MkdirAll(stateDir, 0o775)
			assert.NoError(t, err)

			cpuTopology, err := machine.GenerateDummyCPUTopology(16, 2, 4)
			assert.NoError(t, err)

			policyName := "test-policy"
			checkpointName := "test-checkpoint"

			// create old checkpoint manager to save the checkpoint
			oldCheckpointManager, err := checkpointmanager.NewCheckpointManager(stateDir)
			assert.NoError(t, err)

			oldCheckpoint := NewCPUPluginCheckpoint()
			if tt.corruptFile {
				// create a corrupted old checkpoint
				corruptedFile := filepath.Join(stateDir, fmt.Sprintf("%s", checkpointName))
				err = ioutil.WriteFile(corruptedFile, []byte("corrupted data"), 0o644)
				assert.NoError(t, err)
			} else {
				oldCheckpoint.PolicyName = policyName
				oldCheckpoint.MachineState = GetDefaultMachineState(cpuTopology)
				err = oldCheckpointManager.CreateCheckpoint(checkpointName, oldCheckpoint)
				assert.NoError(t, err)
			}

			stateDirectoryConfig := &statedirectory.StateDirectoryConfiguration{
				StateFileDirectory:         stateDir,
				InMemoryStateFileDirectory: inMemoryTmpDir,
				EnableInMemoryState:        true,
			}

			state, err := NewCheckpointState(stateDirectoryConfig, checkpointName, policyName, cpuTopology, false,
				generateTestMachineStateFromPodEntries, metrics.DummyMetrics{})

			if tt.corruptFile {
				assert.Error(t, err)
				return
			}
			assert.NoError(t, err)

			sc, ok := state.(*stateCheckpoint)
			assert.True(t, ok)

			newCheckpoint := NewCPUPluginCheckpoint()

			// check if new checkpoint is created and verify equality
			err = sc.checkpointManager.GetCheckpoint(checkpointName, newCheckpoint)
			assert.NoError(t, err)

			// verify old checkpoint file existence
			checkpoint := NewCPUPluginCheckpoint()
			err = oldCheckpointManager.GetCheckpoint(checkpointName, checkpoint)

			assert.Error(t, err)
			assert.Equal(t, err, errors.ErrCheckpointNotFound)

			if tt.expectEqual {
				assert.Equal(t, newCheckpoint, oldCheckpoint)
			} else {
				assert.NotEqual(t, newCheckpoint, oldCheckpoint)
			}
		})
	}
}

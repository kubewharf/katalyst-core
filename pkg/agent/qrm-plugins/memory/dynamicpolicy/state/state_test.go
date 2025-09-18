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
	"os"
	"path/filepath"
	"testing"

	"k8s.io/kubernetes/pkg/kubelet/checkpointmanager"

	info "github.com/google/cadvisor/info/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"

	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

func TestGetReadonlyState(t *testing.T) {
	t.Parallel()

	as := require.New(t)
	state, err := GetReadonlyState()
	if state == nil {
		as.NotNil(err)
	}
}

func TestGetWriteOnlyState(t *testing.T) {
	t.Parallel()

	as := require.New(t)
	state, err := GetReadWriteState()
	if state == nil {
		as.NotNil(err)
	}
}

func TestTryMigrateState(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	inMemoryTmpDir := t.TempDir()

	stateDir := filepath.Join(tmpDir, "state")
	err := os.MkdirAll(stateDir, 0o775)
	assert.NoError(t, err)

	cpuTopology, err := machine.GenerateDummyCPUTopology(16, 2, 4)
	assert.NoError(t, err)

	machineInfo := &info.MachineInfo{}
	reservedMemory := make(map[v1.ResourceName]map[int]uint64)
	defaultCache, err := NewMemoryPluginState(cpuTopology, machineInfo, reservedMemory)
	assert.NoError(t, err)

	policyName := "test-policy"
	checkpointName := "test-checkpoint"

	// create old checkpoint manager to save checkpoint
	oldCheckpointManager, err := checkpointmanager.NewCheckpointManager(stateDir)
	assert.NoError(t, err)

	oldCheckpoint := NewMemoryPluginCheckpoint()
	oldCheckpoint.PolicyName = policyName
	generatedResourcesMachineState, err := GenerateMachineStateFromPodEntries(machineInfo, make(PodResourceEntries), reservedMemory)
	assert.NoError(t, err)

	oldCheckpoint.MachineState = generatedResourcesMachineState
	err = oldCheckpointManager.CreateCheckpoint(checkpointName, oldCheckpoint)
	assert.NoError(t, err)

	// create a new checkpoint with a new checkpoint manager
	sc := &stateCheckpoint{
		policyName:          policyName,
		checkpointName:      checkpointName,
		cache:               defaultCache,
		skipStateCorruption: false,
		emitter:             metrics.DummyMetrics{},
	}

	// current checkpoint is pointing to the in memory directory
	sc.checkpointManager, err = checkpointmanager.NewCheckpointManager(inMemoryTmpDir)
	assert.NoError(t, err)

	newCheckpoint := NewMemoryPluginCheckpoint()
	err = sc.tryMigrateState(machineInfo, reservedMemory, stateDir, newCheckpoint)
	assert.NoError(t, err)

	// check if new checkpoint is created and verify equality
	err = sc.checkpointManager.GetCheckpoint(checkpointName, newCheckpoint)
	assert.NoError(t, err)
	assert.Equal(t, newCheckpoint, oldCheckpoint)
}

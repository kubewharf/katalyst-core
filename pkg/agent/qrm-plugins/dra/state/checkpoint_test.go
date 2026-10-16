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

package state

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/kubewharf/katalyst-core/pkg/config/agent/qrm/statedirectory"
)

func TestCheckpointStateHonorsPersistFlag(t *testing.T) {
	stateDir := t.TempDir()
	conf := &statedirectory.StateDirectoryConfiguration{StateFileDirectory: stateDir}

	checkpointState, err := NewCheckpointState(conf)
	require.NoError(t, err)
	require.NoError(t, checkpointState.SetPreparedClaim(
		PreparedClaim{UID: "memory-only", Phase: PrepareStarted},
		false,
	))

	beforeStore, err := NewCheckpointState(conf)
	require.NoError(t, err)
	_, found := beforeStore.GetPreparedClaim("memory-only")
	require.False(t, found)

	require.NoError(t, checkpointState.StoreState())
	afterStore, err := NewCheckpointState(conf)
	require.NoError(t, err)
	restored, found := afterStore.GetPreparedClaim("memory-only")
	require.True(t, found)
	require.Equal(t, PrepareStarted, restored.Phase)
}

func TestCheckpointStatePersistsMutation(t *testing.T) {
	conf := &statedirectory.StateDirectoryConfiguration{
		StateFileDirectory: t.TempDir(),
	}

	checkpointState, err := NewCheckpointState(conf)
	require.NoError(t, err)
	require.NoError(t, checkpointState.SetPreparedClaim(
		PreparedClaim{UID: "uid-a", Phase: PrepareCompleted},
		true,
	))

	restoredState, err := NewCheckpointState(conf)
	require.NoError(t, err)
	restored, found := restoredState.GetPreparedClaim("uid-a")
	require.True(t, found)
	require.Equal(t, PrepareCompleted, restored.Phase)

	require.NoError(t, checkpointState.DeletePreparedClaim("uid-a", true))
	restoredState, err = NewCheckpointState(conf)
	require.NoError(t, err)
	_, found = restoredState.GetPreparedClaim("uid-a")
	require.False(t, found)
}

func TestCheckpointStateMigratesPreviousDirectory(t *testing.T) {
	diskDir := t.TempDir()
	memoryDir := t.TempDir()
	diskConfig := &statedirectory.StateDirectoryConfiguration{
		StateFileDirectory:         diskDir,
		InMemoryStateFileDirectory: memoryDir,
	}

	diskState, err := NewCheckpointState(diskConfig)
	require.NoError(t, err)
	require.NoError(t, diskState.SetPreparedClaim(
		PreparedClaim{UID: "uid-a", Phase: PrepareCompleted},
		true,
	))

	memoryConfig := &statedirectory.StateDirectoryConfiguration{
		StateFileDirectory:         diskDir,
		InMemoryStateFileDirectory: memoryDir,
		EnableInMemoryState:        true,
	}
	memoryState, err := NewCheckpointState(memoryConfig)
	require.NoError(t, err)
	restored, found := memoryState.GetPreparedClaim("uid-a")
	require.True(t, found)
	require.Equal(t, PrepareCompleted, restored.Phase)
	require.FileExists(t, filepath.Join(memoryDir, checkpointName))
}

func TestDRAPluginCheckpointChecksum(t *testing.T) {
	checkpoint := NewDRAPluginCheckpoint()
	checkpoint.PreparedClaims["uid-a"] = PreparedClaim{UID: "uid-a"}

	data, err := checkpoint.MarshalCheckpoint()
	require.NoError(t, err)

	restored := NewDRAPluginCheckpoint()
	require.NoError(t, restored.UnmarshalCheckpoint(data))
	require.NoError(t, restored.VerifyChecksum())

	restored.PreparedClaims["uid-b"] = PreparedClaim{UID: "uid-b"}
	require.Error(t, restored.VerifyChecksum())
}

func TestCheckpointStateRejectsUnsupportedVersion(t *testing.T) {
	checkpointState := &stateCheckpoint{cache: NewMemoryState()}
	checkpoint := NewDRAPluginCheckpoint()
	checkpoint.Version++

	_, err := checkpointState.RestoreState(checkpoint)
	require.ErrorContains(t, err, "unsupported DRA checkpoint version")
}

func TestNewCheckpointStateRejectsNilConfiguration(t *testing.T) {
	_, err := NewCheckpointState(nil)
	require.ErrorContains(t, err, "state directory configuration is required")
}

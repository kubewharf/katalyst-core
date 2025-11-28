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

package customcheckpointmanager

import (
	"encoding/json"
	"io/ioutil"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"k8s.io/kubernetes/pkg/kubelet/checkpointmanager"
	"k8s.io/kubernetes/pkg/kubelet/checkpointmanager/checksum"
	"k8s.io/kubernetes/pkg/kubelet/checkpointmanager/errors"

	"github.com/kubewharf/katalyst-core/pkg/util/state"
)

var _ checkpointmanager.Checkpoint = &mockCheckpoint{}

// mockCheckpoint struct is a simple checkpoint for testing purposes
type mockCheckpoint struct {
	Content  string            `json:"Content"`
	Checksum checksum.Checksum `json:"Checksum"`
}

func (mc *mockCheckpoint) MarshalCheckpoint() ([]byte, error) {
	mc.Checksum = 0
	mc.Checksum = checksum.New(mc)
	return json.Marshal(*mc)
}

func (mc *mockCheckpoint) UnmarshalCheckpoint(blob []byte) error {
	return json.Unmarshal(blob, mc)
}

func (mc *mockCheckpoint) VerifyChecksum() error {
	ck := mc.Checksum
	mc.Checksum = 0
	err := ck.Verify(mc)
	mc.Checksum = ck
	return err
}

var _ state.Storable = &mockStorable{}

type mockStorable struct{}

func (ms *mockStorable) InitNewCheckpoint(empty bool) checkpointmanager.Checkpoint {
	return &mockCheckpoint{
		Content: "test_content",
	}
}

func (ms *mockStorable) RestoreState(checkpoint checkpointmanager.Checkpoint) (bool, error) {
	return false, nil
}

func TestNewCustomCheckpointManager(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name                 string
		hasPreviousStateFile bool
		hasCurrentStateFile  bool
		adjustModTime        func(string, string)
		prevContent          string
		currContent          string
		expectedContent      string
		// isNewCheckpoint checks if a new checkpoint file is created
		isNewCheckpoint bool
		isCorrupt       bool
	}{
		{
			name:                 "no previous and current state file, create a new checkpoint file",
			hasPreviousStateFile: false,
			hasCurrentStateFile:  false,
			isNewCheckpoint:      true,
		},
		{
			name:                "current state file is corrupted",
			hasCurrentStateFile: true,
			isCorrupt:           true,
		},
		{
			name:                 "has previous state directory, previous checkpoint should not exist",
			hasPreviousStateFile: true,
			isNewCheckpoint:      false,
		},
		{
			name:                 "has previous state directory and the current file is up to date",
			hasPreviousStateFile: true,
			adjustModTime: func(currentFilePath, previousFilePath string) {
				now := time.Now()
				err := os.Chtimes(previousFilePath, now, now)
				assert.NoError(t, err)
				updatedTime := now.Add(-2 * time.Second) // 2 seconds is the threshold of modification time difference between the two files
				err = os.Chtimes(currentFilePath, updatedTime, updatedTime)
				assert.NoError(t, err)
			},
			isNewCheckpoint: false,
		},
		{
			name:                 "has previous state directory but the current file is not up to date",
			hasPreviousStateFile: true,
			adjustModTime: func(currentFilePath, previousFilePath string) {
				now := time.Now()
				err := os.Chtimes(previousFilePath, now, now)
				assert.NoError(t, err)
				updatedTime := now.Add(-3 * time.Second) // 3 seconds is out of the threshold of modification time difference between the two files
				err = os.Chtimes(currentFilePath, updatedTime, updatedTime)
				assert.NoError(t, err)
			},
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			currentStateDir := t.TempDir()

			var previousStateDir string
			if tt.hasPreviousStateFile {
				previousStateDir = t.TempDir()
			}

			checkpoint := &mockCheckpoint{Content: "test_content"}
			if tt.hasPreviousStateFile {
				prevCm, err := checkpointmanager.NewCheckpointManager(previousStateDir)
				assert.NoError(t, err)
				err = prevCm.CreateCheckpoint("test_checkpoint", checkpoint)
				assert.NoError(t, err)
			}

			if tt.hasCurrentStateFile {
				currCm, err := checkpointmanager.NewCheckpointManager(currentStateDir)
				assert.NoError(t, err)
				err = currCm.CreateCheckpoint("test_checkpoint", checkpoint)
				assert.NoError(t, err)
			}

			if tt.isCorrupt {
				corruptedFile := filepath.Join(currentStateDir, "test_checkpoint")
				err := ioutil.WriteFile(corruptedFile, []byte("corrupted data"), 0o644)
				assert.NoError(t, err)
			}

			if tt.adjustModTime != nil && tt.hasPreviousStateFile && tt.hasCurrentStateFile {
				currCheckpointPath := filepath.Join(currentStateDir, "test_checkpoint")
				prevCheckpointPath := filepath.Join(previousStateDir, "test_checkpoint")
				tt.adjustModTime(currCheckpointPath, prevCheckpointPath)
			}

			checkpointManager, err := NewCustomCheckpointManager(currentStateDir, previousStateDir, "test_checkpoint", "test_plugin",
				&mockStorable{}, false)

			if tt.isCorrupt {
				assert.Error(t, err)
				return
			}

			assert.NoError(t, err)

			cm, ok := checkpointManager.(*customCheckpointManager)
			assert.True(t, ok)

			newCheckpoint := &mockCheckpoint{}
			if !tt.isNewCheckpoint {
				err = cm.getCurrentCheckpoint("test_checkpoint", newCheckpoint)
				assert.NoError(t, err)

				// Verify equality of checkpoints
				assert.Equal(t, checkpoint, newCheckpoint)
			}

			if tt.hasPreviousStateFile {
				// Ensure previous checkpoint does not exist
				err = cm.previousCheckpointManager.GetCheckpoint("test_checkpoint", newCheckpoint)
				assert.Error(t, err)
				assert.Equal(t, errors.ErrCheckpointNotFound, err)
			}
		})
	}
}

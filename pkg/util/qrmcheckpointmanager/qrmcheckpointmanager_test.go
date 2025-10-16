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

package qrmcheckpointmanager

import (
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"k8s.io/kubernetes/pkg/kubelet/checkpointmanager"
	"k8s.io/kubernetes/pkg/kubelet/checkpointmanager/checksum"
	"k8s.io/kubernetes/pkg/kubelet/checkpointmanager/errors"
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

func TestQRMCheckpointManager_GetCurrentCheckpoint(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name                string
		hasPreviousStateDir bool
		adjustModTime       func(string, string)
		isNotUpToDate       bool
	}{
		{
			name:                "no previous state directory",
			hasPreviousStateDir: false,
		},
		{
			name:                "has previous state directory, previous checkpoint should not exist",
			hasPreviousStateDir: true,
		},
		{
			name:                "has previous state directory and the current file is up to date",
			hasPreviousStateDir: true,
			adjustModTime: func(currentFilePath, previousFilePath string) {
				now := time.Now()
				err := os.Chtimes(previousFilePath, now, now)
				assert.NoError(t, err)
				updatedTime := now.Add(-2 * time.Second) // 2 seconds is the threshold of modification time difference between the two files
				err = os.Chtimes(currentFilePath, updatedTime, updatedTime)
				assert.NoError(t, err)
			},
		},
		{
			name:                "has previous state directory but the current file is not up to date",
			hasPreviousStateDir: true,
			adjustModTime: func(currentFilePath, previousFilePath string) {
				now := time.Now()
				err := os.Chtimes(previousFilePath, now, now)
				assert.NoError(t, err)
				updatedTime := now.Add(-3 * time.Second) // 3 seconds is out of the threshold of modification time difference between the two files
				err = os.Chtimes(currentFilePath, updatedTime, updatedTime)
				assert.NoError(t, err)
			},
			isNotUpToDate: true,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			currentStateDir := t.TempDir()

			var previousStateDir string
			if tt.hasPreviousStateDir {
				previousStateDir = t.TempDir()
			}

			qrmCheckpointManager, err := NewQRMCheckpointManager(currentStateDir, previousStateDir, "test_checkpoint", "test_plugin")
			assert.NoError(t, err)

			checkpoint := &mockCheckpoint{Content: "test_content"}
			if tt.hasPreviousStateDir {
				err := qrmCheckpointManager.previousCheckpointInfo.CreateCheckpoint("test_checkpoint", checkpoint)
				assert.NoError(t, err)
			}
			err = qrmCheckpointManager.currentCheckpointInfo.CreateCheckpoint("test_checkpoint", checkpoint)
			assert.NoError(t, err)

			if tt.adjustModTime != nil && tt.hasPreviousStateDir {
				tt.adjustModTime(qrmCheckpointManager.currentCheckpointInfo.checkpointPath, qrmCheckpointManager.previousCheckpointInfo.checkpointPath)
			}

			newCheckpoint := &mockCheckpoint{}
			err = qrmCheckpointManager.GetCurrentCheckpoint("test_checkpoint", newCheckpoint, true)

			if tt.isNotUpToDate {
				assert.Error(t, err)
				assert.Equal(t, errors.ErrCheckpointNotFound, err)
				return
			}

			assert.NoError(t, err)

			// Verify equality of checkpoints
			assert.Equal(t, checkpoint, newCheckpoint)

			if tt.hasPreviousStateDir {
				// Ensure previous checkpoint does not exist
				err = qrmCheckpointManager.previousCheckpointInfo.GetCheckpoint("test_checkpoint", newCheckpoint)
				assert.Error(t, err)
				assert.Equal(t, errors.ErrCheckpointNotFound, err)
			}
		})
	}
}

func TestQRMCheckpointManager_GetPreviousCheckpoint(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name                  string
		hasPreviousCheckpoint bool
	}{
		{
			name:                  "no previous checkpoint",
			hasPreviousCheckpoint: false,
		},
		{
			name:                  "has previous checkpoint",
			hasPreviousCheckpoint: true,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			previousStateDir := t.TempDir()
			qrmCheckpointManager, err := NewQRMCheckpointManager(t.TempDir(), previousStateDir, "test_checkpoint", "test_plugin")
			assert.NoError(t, err)

			checkpoint := &mockCheckpoint{Content: "test_content"}
			if tt.hasPreviousCheckpoint {
				err := qrmCheckpointManager.previousCheckpointInfo.CreateCheckpoint("test_checkpoint", checkpoint)
				assert.NoError(t, err)
			}

			newCheckpoint := &mockCheckpoint{}
			err = qrmCheckpointManager.GetPreviousCheckpoint("test_checkpoint", newCheckpoint)

			if !tt.hasPreviousCheckpoint {
				assert.Error(t, err)
				assert.Equal(t, errors.ErrCheckpointNotFound, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, checkpoint, newCheckpoint)
			}
		})
	}
}

func TestQRMCheckpointManager_ValidateCheckpointFilesMigration(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name            string
		isEqual         bool
		hasStateChanged bool
	}{
		{
			name:            "Checkpoints are not equal, fallback to previous checkpoint",
			isEqual:         false,
			hasStateChanged: false,
		},
		{
			name:            "Checkpoints are equal, previous checkpoint should not exist",
			isEqual:         true,
			hasStateChanged: false,
		},
		{
			name:            "Checkpoints are not equal but a state change is detected, previous checkpoint should not exist",
			isEqual:         false,
			hasStateChanged: true,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			currentStateDir := t.TempDir()
			previousStateDir := t.TempDir()

			qrmCheckpointManager, err := NewQRMCheckpointManager(currentStateDir, previousStateDir, "test_checkpoint", "test_plugin")
			assert.NoError(t, err)

			checkpoint1 := &mockCheckpoint{Content: "test_content"}
			err = qrmCheckpointManager.currentCheckpointInfo.CreateCheckpoint("test_checkpoint", checkpoint1)
			assert.NoError(t, err)

			if tt.isEqual {
				checkpoint2 := &mockCheckpoint{Content: "test_content"}
				err = qrmCheckpointManager.previousCheckpointInfo.CreateCheckpoint("test_checkpoint", checkpoint2)
				assert.NoError(t, err)
			} else {
				checkpoint2 := &mockCheckpoint{Content: "different_content"}
				err = qrmCheckpointManager.previousCheckpointInfo.CreateCheckpoint("test_checkpoint", checkpoint2)
				assert.NoError(t, err)
			}

			err = qrmCheckpointManager.ValidateCheckpointFilesMigration(tt.hasStateChanged)
			assert.NoError(t, err)
			newCheckpoint := &mockCheckpoint{}

			if tt.isEqual || tt.hasStateChanged {
				// Ensure previous checkpoint does not exist
				err = qrmCheckpointManager.previousCheckpointInfo.GetCheckpoint("test_checkpoint", newCheckpoint)
				assert.Error(t, err)
				assert.Equal(t, errors.ErrCheckpointNotFound, err)
			} else {
				assert.Equal(t, qrmCheckpointManager.currentCheckpointInfo, qrmCheckpointManager.previousCheckpointInfo)
			}
		})
	}
}

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
	"fmt"
	"path/filepath"

	"github.com/kubewharf/katalyst-core/pkg/util/file"
	"k8s.io/klog/v2"
	"k8s.io/kubernetes/pkg/kubelet/checkpointmanager"
)

type checkpointInfo struct {
	checkpointmanager.CheckpointManager
	checkpointPath string
}

// QRMCheckpointManager manages old and new checkpoints for all QRM plugins
// and ensures seamless transfer between them.
type QRMCheckpointManager struct {
	currentCheckpointInfo  *checkpointInfo
	previousCheckpointInfo *checkpointInfo
	checkpointName         string
	pluginName             string
}

func NewQRMCheckpointManager(currentStateDir, previousStateDir, checkpointName, pluginName string) (*QRMCheckpointManager, error) {
	currentCheckpointManager, err := checkpointmanager.NewCheckpointManager(currentStateDir)
	if err != nil {
		return nil, fmt.Errorf("error creating new checkpoint manager: %v", err)
	}
	var previousCheckpointManager checkpointmanager.CheckpointManager
	if previousStateDir != "" {
		previousCheckpointManager, err = checkpointmanager.NewCheckpointManager(previousStateDir)
		if err != nil {
			return nil, fmt.Errorf("error creating previous checkpoint manager: %v", err)
		}
	}
	return &QRMCheckpointManager{
		currentCheckpointInfo: &checkpointInfo{
			checkpointPath:    filepath.Join(currentStateDir, checkpointName),
			CheckpointManager: currentCheckpointManager,
		},
		previousCheckpointInfo: &checkpointInfo{
			checkpointPath:    filepath.Join(previousStateDir, checkpointName),
			CheckpointManager: previousCheckpointManager,
		},
		checkpointName: checkpointName,
		pluginName:     pluginName,
	}, err
}

// GetCurrentCheckpoint retrieves the current checkpoint and removes the previous checkpoint file if needed
func (cm *QRMCheckpointManager) GetCurrentCheckpoint(
	checkpointName string, checkpoint checkpointmanager.Checkpoint, isRemovePreviousCheckpoint bool,
) error {
	currentCheckpointManager := cm.currentCheckpointInfo.CheckpointManager
	if err := currentCheckpointManager.GetCheckpoint(checkpointName, checkpoint); err != nil {
		return err
	}

	if !isRemovePreviousCheckpoint {
		return nil
	}

	previousCheckpointManager := cm.previousCheckpointInfo.CheckpointManager
	if previousCheckpointManager == nil {
		return nil
	}
	if err := previousCheckpointManager.RemoveCheckpoint(checkpointName); err != nil {
		return fmt.Errorf("[%v] failed to remove checkpoint %v: %v", cm.pluginName, checkpointName, err)
	}
	return nil
}

// GetPreviousCheckpoint retrieves the previous checkpoint
func (cm *QRMCheckpointManager) GetPreviousCheckpoint(
	checkpointName string, checkpoint checkpointmanager.Checkpoint,
) error {
	previousCheckpointManager := cm.previousCheckpointInfo.CheckpointManager
	if previousCheckpointManager == nil {
		return fmt.Errorf("[%v] previous checkpoint manager is nil, which is unexpected", cm.pluginName)
	}

	if err := previousCheckpointManager.GetCheckpoint(checkpointName, checkpoint); err != nil {
		return fmt.Errorf("[%v] previous checkpoint %v not found: %v", cm.pluginName, checkpointName, err)
	}

	return nil
}

// ValidateCheckpointFilesMigration checks if the two checkpoint files are equal after migrating from previous checkpoint to current checkpoint
// If they are not equal, we fall back to the previous checkpoint and continue using it.
// If they are equal, we remove the previous checkpoint.
func (cm *QRMCheckpointManager) ValidateCheckpointFilesMigration() error {
	equal, err := cm.checkpointFilesEqual()
	if err != nil {
		return fmt.Errorf("[%v] failed to compare checkpoint files: %v", cm.pluginName, err)
	}
	if !equal {
		klog.Infof("[%v] checkpoint files are not equal, migration failed, fall back to previous checkpoint", cm.pluginName)
		cm.currentCheckpointInfo = cm.previousCheckpointInfo
	} else {
		klog.Infof("[%v] checkpoint files are equal, try to remove previous checkpoint", cm.pluginName)
		oldCheckpointManager := cm.previousCheckpointInfo.CheckpointManager
		if err := oldCheckpointManager.RemoveCheckpoint(cm.checkpointName); err != nil {
			return fmt.Errorf("[%v] failed to remove previous checkpoint %v: %v", cm.pluginName, cm.checkpointName, err)
		}
	}
	return nil
}

// CheckpointFilesEqual checks if the checkpoints are identical by comparing the 2 files' contents
func (cm *QRMCheckpointManager) checkpointFilesEqual() (bool, error) {
	currentFilePath := filepath.Join(cm.currentCheckpointInfo.checkpointPath)
	previousFilePath := filepath.Join(cm.previousCheckpointInfo.checkpointPath)
	return file.JSONFilesEqual(currentFilePath, previousFilePath)
}

func (cm *QRMCheckpointManager) removeOldCheckpoint() error {
	oldCheckpointManager := cm.previousCheckpointInfo.CheckpointManager
	return oldCheckpointManager.RemoveCheckpoint(cm.checkpointName)
}

// CreateCheckpoint creates a checkpoint only using the current checkpoint manager.
func (cm *QRMCheckpointManager) CreateCheckpoint(checkpointName string, checkpoint checkpointmanager.Checkpoint) error {
	currentCheckpointManager := cm.currentCheckpointInfo.CheckpointManager
	return currentCheckpointManager.CreateCheckpoint(checkpointName, checkpoint)
}

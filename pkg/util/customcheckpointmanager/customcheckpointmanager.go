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
	stdErrors "errors"
	"fmt"
	"path/filepath"
	"time"

	"k8s.io/klog/v2"
	"k8s.io/kubernetes/pkg/kubelet/checkpointmanager"
	"k8s.io/kubernetes/pkg/kubelet/checkpointmanager/errors"

	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/state"
)

// customCheckpointManager implements CheckpointManager interface and handles state logic between old and new checkpoints.
type customCheckpointManager struct {
	currentCheckpointManager  checkpointmanager.CheckpointManager
	previousCheckpointManager checkpointmanager.CheckpointManager
	prevStateDir              string
	currStateDir              string
	// restoreFunc knows how to restore a state cache with data from the checkpoint, and it returns whether the state has been changed.
	restoreFunc func(cp checkpointmanager.Checkpoint) (bool, error)
	// initCheckpointFunc creates a new checkpoint instance.
	initCheckpointFunc  func(empty bool) checkpointmanager.Checkpoint
	checkpointName      string
	pluginName          string
	skipStateCorruption bool
}

func NewCustomCheckpointManager(currentStateDir, previousStateDir,
	checkpointName, pluginName string, storable state.Storable, skipStateCorruption bool,
) (checkpointmanager.CheckpointManager, error) {
	currentCheckpointManager, err := checkpointmanager.NewCheckpointManager(currentStateDir)
	if err != nil {
		return nil, fmt.Errorf("error creating new checkpoint manager: %w", err)
	}
	var previousCheckpointManager checkpointmanager.CheckpointManager
	if previousStateDir != "" {
		previousCheckpointManager, err = checkpointmanager.NewCheckpointManager(previousStateDir)
		if err != nil {
			return nil, fmt.Errorf("error creating previous checkpoint manager: %w", err)
		}
	}

	cm := &customCheckpointManager{
		prevStateDir:              previousStateDir,
		currStateDir:              currentStateDir,
		currentCheckpointManager:  currentCheckpointManager,
		previousCheckpointManager: previousCheckpointManager,
		checkpointName:            checkpointName,
		pluginName:                pluginName,
		restoreFunc:               storable.RestoreState,
		initCheckpointFunc:        storable.InitNewCheckpoint,
		skipStateCorruption:       skipStateCorruption,
	}

	// Restore checkpoint from checkpoint manager
	checkpoint := cm.initCheckpointFunc(true)
	if err = cm.GetCheckpoint(checkpointName, checkpoint); err != nil {
		return nil, fmt.Errorf("[%v] error restoring checkpoint: %w", cm.pluginName, err)
	}

	return cm, nil
}

var _ checkpointmanager.CheckpointManager = &customCheckpointManager{}

// GetCheckpoint implements CheckpointManager interface. It tries to get the checkpoint from current checkpoint manager first,
// and if the checkpoint does not exist, it will try to migrate the checkpoint from previous checkpoint manager.
func (cm *customCheckpointManager) GetCheckpoint(checkpointKey string, checkpoint checkpointmanager.Checkpoint) error {
	var foundAndSkippedStateCorruption bool

	if err := cm.getCurrentCheckpoint(checkpointKey, checkpoint); err != nil {
		if stdErrors.Is(err, errors.ErrCheckpointNotFound) {
			// We cannot find checkpoint, so it is possible that previous checkpoint was stored in either disk or memory
			return cm.tryMigrateState(checkpoint)
		} else if stdErrors.Is(err, errors.ErrCorruptCheckpoint) {
			if !cm.skipStateCorruption {
				return err
			}

			foundAndSkippedStateCorruption = true
			klog.Warningf("[%v] restore checkpoint failed with err: %s, but we skip it", cm.pluginName, err)
		} else {
			return err
		}
	}

	_, err := cm.updateCacheAndReturnChanged(checkpoint, foundAndSkippedStateCorruption)
	return err
}

func (cm *customCheckpointManager) RemoveCheckpoint(checkpointKey string) error {
	return cm.currentCheckpointManager.RemoveCheckpoint(checkpointKey)
}

func (cm *customCheckpointManager) ListCheckpoints() ([]string, error) {
	return cm.currentCheckpointManager.ListCheckpoints()
}

// getCurrentCheckpoint retrieves the current checkpoint and removes the previous checkpoint file if needed
func (cm *customCheckpointManager) getCurrentCheckpoint(
	checkpointName string, checkpoint checkpointmanager.Checkpoint,
) error {
	currentCheckpointManager := cm.currentCheckpointManager

	isUpToDate, err := cm.isCheckpointUpToDate()
	if err != nil {
		return err
	}
	// When the current checkpoint is not up to date, we consider the checkpoint missing, so we propagate the checkpoint not found error upwards
	if !isUpToDate {
		klog.Infof("[%v] checkpoint %s is not up-to-date, we ignore it", cm.pluginName, checkpointName)
		return errors.ErrCheckpointNotFound
	}

	if err = currentCheckpointManager.GetCheckpoint(checkpointName, checkpoint); err != nil {
		return err
	}

	previousCheckpointManager := cm.previousCheckpointManager
	if previousCheckpointManager == nil {
		return nil
	}

	if err = previousCheckpointManager.RemoveCheckpoint(checkpointName); err != nil {
		return fmt.Errorf("[%v] failed to remove checkpoint %v: %w", cm.pluginName, checkpointName, err)
	}
	return nil
}

func (cm *customCheckpointManager) getPreviousCheckpoint(
	checkpointName string, checkpoint checkpointmanager.Checkpoint,
) error {
	previousCheckpointManager := cm.previousCheckpointManager
	if previousCheckpointManager == nil {
		return errors.ErrCheckpointNotFound
	}

	if err := previousCheckpointManager.GetCheckpoint(checkpointName, checkpoint); err != nil {
		return err
	}

	return nil
}

// tryMigrateState tries to migrate the state file from the other directory to current directory.
// If the other directory does not have a state file, then we build a new checkpoint.
func (cm *customCheckpointManager) tryMigrateState(checkpoint checkpointmanager.Checkpoint) error {
	var foundAndSkippedStateCorruption bool
	klog.Infof("[%s] trying to migrate state", cm.pluginName)

	if err := cm.getPreviousCheckpoint(cm.checkpointName, checkpoint); err != nil {
		if stdErrors.Is(err, errors.ErrCheckpointNotFound) {
			// Old checkpoint file is not found, so we just store state in new checkpoint
			general.Infof("[%s] checkpoint %v doesn't exist, create it", cm.pluginName, cm.checkpointName)
			return cm.storeState()
		} else if stdErrors.Is(err, errors.ErrCorruptCheckpoint) {
			if !cm.skipStateCorruption {
				return err
			}
			foundAndSkippedStateCorruption = true
			klog.Warningf("[%s] restore checkpoint failed with err: %s, but we skip it", cm.pluginName, err)
		} else {
			return err
		}
	}

	hasStateChanged, err := cm.updateCacheAndReturnChanged(checkpoint, foundAndSkippedStateCorruption)
	if err != nil {
		return fmt.Errorf("[%v] failed to populate checkpoint state during state migration: %w", cm.pluginName, err)
	}

	if !hasStateChanged {
		if err = cm.storeState(); err != nil {
			return fmt.Errorf("[%v] failed to store checkpoint state during end of migration: %w", cm.pluginName, err)
		}
	}

	if err = cm.validateCheckpointFilesMigration(hasStateChanged); err != nil {
		return fmt.Errorf("[%v] validateCheckpointFilesMigration failed with error: %w", cm.pluginName, err)
	}

	klog.Infof("[%v] migrate checkpoint succeeded", cm.pluginName)
	return nil
}

func (cm *customCheckpointManager) storeState() error {
	startTime := time.Now()
	general.InfoS("called")
	defer func() {
		elapsed := time.Since(startTime)
		general.InfoS("finished", "duration", elapsed)
	}()

	checkpoint := cm.initCheckpointFunc(false)
	err := cm.CreateCheckpoint(cm.checkpointName, checkpoint)
	if err != nil {
		klog.ErrorS(err, "Could not save checkpoint")
		return err
	}

	return nil
}

// updateCacheAndReturnChanged restores the cache using restoreFunc, stores the updated cache in checkpoint using storeState,
// and returns if the checkpoint state has changed.
func (cm *customCheckpointManager) updateCacheAndReturnChanged(
	cp checkpointmanager.Checkpoint, foundAndSkippedStateCorruption bool,
) (bool, error) {
	hasStateChanged, err := cm.restoreFunc(cp)
	if err != nil {
		return false, fmt.Errorf("[%v] failed to restore state: %w", cm.pluginName, err)
	}

	if hasStateChanged {
		err = cm.storeState()
		if err != nil {
			return false, fmt.Errorf("[%v] storeState when machine state changed failed with error: %v", cm.pluginName, err)
		}
	}

	if foundAndSkippedStateCorruption {
		klog.Infof("[%v] found and skipped state corruption, we should store to rectify the checksum", cm.pluginName)
		hasStateChanged = true
		err = cm.storeState()
		if err != nil {
			return hasStateChanged, fmt.Errorf("[%v] storeState failed with error after skipping corruption: %v", cm.pluginName, err)
		}
	}

	klog.Infof("[%v] State checkpoint: restored state from checkpoint", cm.pluginName)
	return hasStateChanged, nil
}

// validateCheckpointFilesMigration checks if the two checkpoint files are equal after migrating from previous checkpoint to current checkpoint
// If they are not equal, we fall back to the previous checkpoint and continue using it, and make sure we remove the current checkpoint
// If they are equal, we remove the previous checkpoint.
func (cm *customCheckpointManager) validateCheckpointFilesMigration(hasStateChanged bool) error {
	equal, err := cm.checkpointFilesEqual(hasStateChanged)
	if err != nil {
		return fmt.Errorf("[%v] failed to compare checkpoint files: %w", cm.pluginName, err)
	}
	if !equal {
		klog.Infof("[%v] checkpoint files are not equal, state failed, fall back to previous checkpoint", cm.pluginName)
		if err := cm.currentCheckpointManager.RemoveCheckpoint(cm.checkpointName); err != nil {
			return fmt.Errorf("[%v] failed to remove current checkpoint %v during fallback: %w", cm.pluginName, cm.checkpointName, err)
		}
		cm.currentCheckpointManager = cm.previousCheckpointManager
	} else {
		klog.Infof("[%v] checkpoint files are equal, try to remove previous checkpoint", cm.pluginName)
		oldCheckpointManager := cm.previousCheckpointManager
		if err := oldCheckpointManager.RemoveCheckpoint(cm.checkpointName); err != nil {
			return fmt.Errorf("[%v] failed to remove previous checkpoint %v: %w", cm.pluginName, cm.checkpointName, err)
		}
	}
	return nil
}

// checkpointFilesEqual checks if the checkpoints are identical by comparing the 2 files' contents
func (cm *customCheckpointManager) checkpointFilesEqual(hasStateChanged bool) (bool, error) {
	// A change in machine state is already detected, so we do not need to actually check if the files are identical
	if hasStateChanged {
		return true, nil
	}
	currentFilePath := filepath.Join(cm.currStateDir, cm.checkpointName)
	previousFilePath := filepath.Join(cm.prevStateDir, cm.checkpointName)
	return general.JSONFilesEqual(currentFilePath, previousFilePath)
}

// CreateCheckpoint creates a checkpoint only using the current checkpoint manager.
func (cm *customCheckpointManager) CreateCheckpoint(checkpointName string, checkpoint checkpointmanager.Checkpoint) error {
	currentCheckpointManager := cm.currentCheckpointManager
	return currentCheckpointManager.CreateCheckpoint(checkpointName, checkpoint)
}

// isCheckpointUpToDate checks if the current checkpoint is up to date
func (cm *customCheckpointManager) isCheckpointUpToDate() (bool, error) {
	currentCheckpointFilePath := filepath.Join(cm.currStateDir, cm.checkpointName)

	// Current checkpoint is not up to date as it does not exist
	if !general.IsPathExists(currentCheckpointFilePath) {
		return false, nil
	}

	previousCheckpointFilePath := filepath.Join(cm.prevStateDir, cm.checkpointName)

	// When there is no previous checkpoint, we consider the current checkpoint is up to date
	if !general.IsPathExists(previousCheckpointFilePath) {
		return true, nil
	}

	isUpToDate, err := general.IsFileUpToDate(currentCheckpointFilePath, previousCheckpointFilePath)
	if err != nil {
		return false, fmt.Errorf("[%v] failed to check if file up to date: %w", cm.pluginName, err)
	}
	return isUpToDate, nil
}

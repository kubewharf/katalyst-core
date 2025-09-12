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
	"fmt"
	"path"
	"path/filepath"
	"reflect"
	"sync"
	"time"

	"github.com/kubewharf/katalyst-core/pkg/util/file"

	"github.com/kubewharf/katalyst-core/pkg/config/agent/qrm/statedirectory"

	info "github.com/google/cadvisor/info/v1"
	v1 "k8s.io/api/core/v1"
	"k8s.io/klog/v2"
	"k8s.io/kubernetes/pkg/kubelet/checkpointmanager"
	"k8s.io/kubernetes/pkg/kubelet/checkpointmanager/errors"

	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

const (
	metricMetaCacheStoreStateDuration       = "metacache_store_state_duration"
	qrmMemoryStateCheckpointHealthCheckName = "qrm_memory_state_checkpoint"
)

var doOnce sync.Once

var _ State = &stateCheckpoint{}

// stateCheckpoint is an in-memory implementation of State;
// everytime we want to read or write states, those requests will always
// go to in-memory State, and then go to disk State, i.e. in write-back mode
type stateCheckpoint struct {
	sync.RWMutex
	cache             *memoryPluginState
	policyName        string
	checkpointManager checkpointmanager.CheckpointManager
	checkpointName    string
	// when we add new properties to checkpoint,
	// it will cause checkpoint corruption and we should skip it
	skipStateCorruption bool
	emitter             metrics.MetricEmitter
}

func NewCheckpointState(
	stateDirectoryConfig *statedirectory.StateDirectoryConfiguration, checkpointName, policyName string,
	topology *machine.CPUTopology, machineInfo *info.MachineInfo,
	reservedMemory map[v1.ResourceName]map[int]uint64, skipStateCorruption bool,
	emitter metrics.MetricEmitter,
) (State, error) {
	currentStateDir, otherStateDir := stateDirectoryConfig.GetCurrentAndOtherStateFileDirectory()
	hasPreStop := stateDirectoryConfig.HasPreStop
	checkpointManager, err := checkpointmanager.NewCheckpointManager(currentStateDir)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize checkpoint manager: %v", err)
	}

	defaultCache, err := NewMemoryPluginState(topology, machineInfo, reservedMemory)
	if err != nil {
		return nil, fmt.Errorf("NewMemoryPluginState failed with error: %v", err)
	}

	stateCheckpoint := &stateCheckpoint{
		cache:               defaultCache,
		policyName:          policyName,
		checkpointManager:   checkpointManager,
		checkpointName:      checkpointName,
		skipStateCorruption: skipStateCorruption,
		emitter:             emitter,
	}

	doOnce.Do(func() {
		general.RegisterHeartbeatCheck(qrmMemoryStateCheckpointHealthCheckName, 90*time.Second, general.HealthzCheckStateNotReady,
			90*time.Second)
	})

	if err := stateCheckpoint.restoreState(currentStateDir, otherStateDir, hasPreStop, machineInfo, reservedMemory); err != nil {
		return nil, fmt.Errorf("could not restore state from checkpoint: %v, please drain this node and delete the memory plugin checkpoint file %q before restarting Kubelet",
			err, path.Join(currentStateDir, checkpointName))
	}

	_ = general.UpdateHealthzStateByError(qrmMemoryStateCheckpointHealthCheckName, err)
	return stateCheckpoint, nil
}

// restoreState is first done by searching the current directory for the state file.
// If it does not exist, we search the other directory for the state file and try to migrate the state file over to the current directory.
func (sc *stateCheckpoint) restoreState(
	currentStateDir, otherStateDir string, hasPreStop bool, machineInfo *info.MachineInfo,
	reservedMemory map[v1.ResourceName]map[int]uint64,
) error {
	sc.Lock()
	defer sc.Unlock()
	var err error
	var foundAndSkippedStateCorruption bool

	checkpoint := NewMemoryPluginCheckpoint()
	if err = sc.checkpointManager.GetCheckpoint(sc.checkpointName, checkpoint); err != nil {
		if err == errors.ErrCheckpointNotFound {
			// We cannot find checkpoint, so it is possible that previous checkpoint was stored in either disk or memory
			return sc.tryMigrateState(machineInfo, reservedMemory, currentStateDir, otherStateDir, hasPreStop, checkpoint)
		} else if err == errors.ErrCorruptCheckpoint {
			if !sc.skipStateCorruption {
				return err
			}

			foundAndSkippedStateCorruption = true
			klog.Warningf("[memory_plugin] restore checkpoint failed with err: %s, but we skip it", err)
		} else {
			return err
		}
	}

	return sc.populateCacheAndState(machineInfo, reservedMemory, checkpoint, foundAndSkippedStateCorruption)
}

func (sc *stateCheckpoint) populateCacheAndState(
	machineInfo *info.MachineInfo, reservedMemory map[v1.ResourceName]map[int]uint64,
	checkpoint *MemoryPluginCheckpoint, foundAndSkippedStateCorruption bool,
) error {
	if sc.policyName != checkpoint.PolicyName && !sc.skipStateCorruption {
		return fmt.Errorf("[memory_plugin] configured policy %q differs from state checkpoint policy %q", sc.policyName, checkpoint.PolicyName)
	}

	generatedResourcesMachineState, err := GenerateMachineStateFromPodEntries(machineInfo, checkpoint.PodResourceEntries, checkpoint.MachineState, reservedMemory)
	if err != nil {
		return fmt.Errorf("GenerateMachineStateFromPodEntries failed with error: %v", err)
	}

	sc.cache.SetMachineState(generatedResourcesMachineState)
	sc.cache.SetNUMAHeadroom(checkpoint.NUMAHeadroom)
	sc.cache.SetPodResourceEntries(checkpoint.PodResourceEntries)

	if !reflect.DeepEqual(generatedResourcesMachineState, checkpoint.MachineState) {
		klog.Warningf("[memory_plugin] machine state changed: "+
			"generatedResourcesMachineState: %s; checkpointMachineState: %s",
			generatedResourcesMachineState.String(), checkpoint.MachineState.String())
		err = sc.storeState()
		if err != nil {
			return fmt.Errorf("storeState when machine state changed failed with error: %v", err)
		}
	}

	if foundAndSkippedStateCorruption {
		klog.Infof("[memory_plugin] found and skipped state corruption, we shoud store to rectify the checksum")
		err = sc.storeState()
		if err != nil {
			return fmt.Errorf("storeState failed with error: %v", err)
		}
	}

	klog.InfoS("[memory_plugin] state checkpoint: restored state from checkpoint")

	return nil
}

// tryMigrateState tries to migrate the state file from the other directory to current directory.
// If the other directory does not have a state file, then we build a new checkpoint.
func (sc *stateCheckpoint) tryMigrateState(
	machineInfo *info.MachineInfo, reservedMemory map[v1.ResourceName]map[int]uint64,
	currentStateDir, otherStateDir string,
	hasPreStop bool, checkpoint *MemoryPluginCheckpoint,
) error {
	var foundAndSkippedStateCorruption bool
	klog.Infof("[memory_plugin] trying to migrate state from disk to memory")

	// Build new checkpoint if the state directory that we want to migrate from is empty
	if !hasPreStop {
		return sc.storeState()
	}

	// Get the old checkpoint using the provided file directory
	oldCheckpointManager, err := checkpointmanager.NewCheckpointManager(otherStateDir)
	if err != nil {
		return fmt.Errorf("[memory_plugin] failed to initialize old checkpoint manager for migration: %v", err)
	}

	if err = oldCheckpointManager.GetCheckpoint(sc.checkpointName, checkpoint); err != nil {
		if err == errors.ErrCheckpointNotFound {
			// Old checkpoint file is not found, so we just store state in new checkpoint
			general.Infof("[memory_plugin] checkpoint %v doesn't exist in dir %v, create it", sc.checkpointName, otherStateDir)
			return sc.storeState()
		} else if err == errors.ErrCorruptCheckpoint {
			if !sc.skipStateCorruption {
				return err
			}
			foundAndSkippedStateCorruption = true
			klog.Warningf("[memory_plugin] restore checkpoint failed with err: %s, but we skip it", err)
		} else {
			return err
		}
	}

	if err = sc.populateCacheAndState(machineInfo, reservedMemory, checkpoint, foundAndSkippedStateCorruption); err != nil {
		return fmt.Errorf("[memory_plugin] populateCacheAndState failed with error: %v", err)
	}

	// always store state after migrating to new checkpoint
	if err = sc.storeState(); err != nil {
		return fmt.Errorf("[memory_plugin] failed to store state during end of migration: %v", err)
	}

	// validate that the two files are equal
	equal, err := sc.checkpointFilesEqual(currentStateDir, otherStateDir)
	if err != nil {
		return fmt.Errorf("[memory_plugin] failed to compare checkpoint files: %v", err)
	}
	if !equal {
		klog.Infof("[memory_plugin] checkpoint files are not equal, migration failed, fall back to old checkpoint")
		return sc.fallbackToOldCheckpoint(oldCheckpointManager)
	}

	// remove old checkpoint file
	if err = oldCheckpointManager.RemoveCheckpoint(sc.checkpointName); err != nil {
		return fmt.Errorf("[memory_plugin] failed to remove old checkpoint: %v", err)
	}

	klog.Infof("[memory_plugin] migrate checkpoint succeeded")
	return nil
}

func (sc *stateCheckpoint) checkpointFilesEqual(currentStateDir, otherStateDir string) (bool, error) {
	currentFilePath := filepath.Join(currentStateDir, sc.checkpointName)
	otherFilePath := filepath.Join(otherStateDir, sc.checkpointName)
	return file.FilesEqual(currentFilePath, otherFilePath)
}

func (sc *stateCheckpoint) fallbackToOldCheckpoint(oldCheckpointManager checkpointmanager.CheckpointManager) error {
	sc.checkpointManager = oldCheckpointManager
	_ = general.UpdateHealthzState(qrmMemoryStateCheckpointHealthCheckName, general.HealthzCheckStateReady, "Migration from old checkpoint to new checkpoint failed")
	return nil
}

func (sc *stateCheckpoint) StoreState() error {
	sc.Lock()
	defer sc.Unlock()
	return sc.storeState()
}

func (sc *stateCheckpoint) storeState() error {
	startTime := time.Now()
	general.InfoS("called")
	defer func() {
		elapsed := time.Since(startTime)
		general.InfoS("finished", "duration", elapsed)
		_ = sc.emitter.StoreFloat64(metricMetaCacheStoreStateDuration, float64(elapsed/time.Millisecond), metrics.MetricTypeNameRaw)
	}()

	checkpoint := NewMemoryPluginCheckpoint()
	checkpoint.PolicyName = sc.policyName
	checkpoint.MachineState = sc.cache.GetMachineState()
	checkpoint.NUMAHeadroom = sc.cache.GetNUMAHeadroom()
	checkpoint.PodResourceEntries = sc.cache.GetPodResourceEntries()

	err := sc.checkpointManager.CreateCheckpoint(sc.checkpointName, checkpoint)
	if err != nil {
		klog.ErrorS(err, "Could not save checkpoint")
		return err
	}
	return nil
}

func (sc *stateCheckpoint) GetReservedMemory() map[v1.ResourceName]map[int]uint64 {
	sc.RLock()
	defer sc.RUnlock()

	return sc.cache.GetReservedMemory()
}

func (sc *stateCheckpoint) GetMachineInfo() *info.MachineInfo {
	sc.RLock()
	defer sc.RUnlock()

	return sc.cache.GetMachineInfo()
}

func (sc *stateCheckpoint) GetMachineState() NUMANodeResourcesMap {
	sc.RLock()
	defer sc.RUnlock()

	return sc.cache.GetMachineState()
}

func (sc *stateCheckpoint) GetNUMAHeadroom() map[int]int64 {
	sc.RLock()
	defer sc.RUnlock()

	return sc.cache.GetNUMAHeadroom()
}

func (sc *stateCheckpoint) GetAllocationInfo(
	resourceName v1.ResourceName, podUID, containerName string,
) *AllocationInfo {
	sc.RLock()
	defer sc.RUnlock()

	return sc.cache.GetAllocationInfo(resourceName, podUID, containerName)
}

func (sc *stateCheckpoint) GetPodResourceEntries() PodResourceEntries {
	sc.RLock()
	defer sc.RUnlock()

	return sc.cache.GetPodResourceEntries()
}

func (sc *stateCheckpoint) SetMachineState(numaNodeResourcesMap NUMANodeResourcesMap, persist bool) {
	sc.Lock()
	defer sc.Unlock()

	sc.cache.SetMachineState(numaNodeResourcesMap)
	if persist {
		err := sc.storeState()
		if err != nil {
			klog.ErrorS(err, "[memory_plugin] store machineState to checkpoint error")
		}
	}
}

func (sc *stateCheckpoint) SetNUMAHeadroom(numaHeadroom map[int]int64, persist bool) {
	sc.Lock()
	defer sc.Unlock()

	sc.cache.SetNUMAHeadroom(numaHeadroom)
	if persist {
		err := sc.storeState()
		if err != nil {
			klog.ErrorS(err, "[memory_plugin] store numa headroom to checkpoint error")
		}
	}
}

func (sc *stateCheckpoint) SetAllocationInfo(
	resourceName v1.ResourceName, podUID, containerName string, allocationInfo *AllocationInfo, persist bool,
) {
	sc.Lock()
	defer sc.Unlock()

	sc.cache.SetAllocationInfo(resourceName, podUID, containerName, allocationInfo)
	if persist {
		err := sc.storeState()
		if err != nil {
			klog.ErrorS(err, "[memory_plugin] store allocationInfo to checkpoint error")
		}
	}
}

func (sc *stateCheckpoint) SetPodResourceEntries(podResourceEntries PodResourceEntries, persist bool) {
	sc.Lock()
	defer sc.Unlock()

	sc.cache.SetPodResourceEntries(podResourceEntries)
	if persist {
		err := sc.storeState()
		if err != nil {
			klog.ErrorS(err, "[memory_plugin] store pod entries to checkpoint error", "err")
		}
	}
}

func (sc *stateCheckpoint) Delete(resourceName v1.ResourceName, podUID, containerName string, persist bool) {
	sc.Lock()
	defer sc.Unlock()

	sc.cache.Delete(resourceName, podUID, containerName)
	if persist {
		err := sc.storeState()
		if err != nil {
			klog.ErrorS(err, "[memory_plugin] store state after delete operation to checkpoint error")
		}
	}
}

func (sc *stateCheckpoint) ClearState() {
	sc.Lock()
	defer sc.Unlock()

	sc.cache.ClearState()
	err := sc.storeState()
	if err != nil {
		klog.ErrorS(err, "[memory_plugin] store state after clear operation to checkpoint error")
	}
}

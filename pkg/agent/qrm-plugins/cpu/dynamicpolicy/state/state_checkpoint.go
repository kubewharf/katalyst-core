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

	"k8s.io/klog/v2"
	"k8s.io/kubernetes/pkg/kubelet/checkpointmanager"
	"k8s.io/kubernetes/pkg/kubelet/checkpointmanager/errors"

	"github.com/kubewharf/katalyst-core/pkg/config/agent/qrm/statedirectory"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/file"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

const (
	metricMetaCacheStoreStateDuration    = "metacache_store_state_duration"
	qrmCPUStateCheckpointHealthCheckName = "qrm_cpu_state_checkpoint"
)

var doOnce sync.Once

// stateCheckpoint is an in-memory implementation of State;
// everytime we want to read or write states, those requests will always
// go to in-memory State, and then go to disk State, i.e. in write-back mode
type stateCheckpoint struct {
	sync.RWMutex
	cache             *cpuPluginState
	policyName        string
	checkpointManager checkpointmanager.CheckpointManager
	checkpointName    string
	// when we add new properties to checkpoint,
	// it will cause checkpoint corruption, and we should skip it
	skipStateCorruption                bool
	GenerateMachineStateFromPodEntries GenerateMachineStateFromPodEntriesFunc
	emitter                            metrics.MetricEmitter
}

var _ State = &stateCheckpoint{}

func NewCheckpointState(
	stateDirectoryConfig *statedirectory.StateDirectoryConfiguration, checkpointName, policyName string,
	topology *machine.CPUTopology, skipStateCorruption bool,
	generateMachineStateFunc GenerateMachineStateFromPodEntriesFunc,
	emitter metrics.MetricEmitter,
) (State, error) {
	currentStateDir, otherStateDir := stateDirectoryConfig.GetCurrentAndOtherStateFileDirectory()
	hasPreStop := stateDirectoryConfig.HasPreStop
	checkpointManager, err := checkpointmanager.NewCheckpointManager(currentStateDir)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize checkpoint manager: %v", err)
	}

	sc := &stateCheckpoint{
		cache:                              NewCPUPluginState(topology),
		policyName:                         policyName,
		checkpointManager:                  checkpointManager,
		checkpointName:                     checkpointName,
		skipStateCorruption:                skipStateCorruption,
		GenerateMachineStateFromPodEntries: generateMachineStateFunc,
		emitter:                            emitter,
	}

	doOnce.Do(func() {
		general.RegisterHeartbeatCheck(qrmCPUStateCheckpointHealthCheckName, 90*time.Second, general.HealthzCheckStateNotReady,
			90*time.Second)
	})

	if err := sc.restoreState(currentStateDir, otherStateDir, hasPreStop, topology); err != nil {
		return nil, fmt.Errorf("could not restore state from checkpoint: %v, please drain this node and delete "+
			"the cpu plugin checkpoint file %q before restarting Kubelet", err, path.Join(currentStateDir, checkpointName))
	}

	_ = general.UpdateHealthzStateByError(qrmCPUStateCheckpointHealthCheckName, err)
	return sc, nil
}

// restoreState is first done by searching the current directory for the state file.
// If it does not exist, we search the other directory for the state file and try to migrate the state file over to the current directory.
func (sc *stateCheckpoint) restoreState(
	currentStateDir, otherStateDir string, hasPreStop bool, topology *machine.CPUTopology,
) error {
	sc.Lock()
	defer sc.Unlock()
	var err error
	var foundAndSkippedStateCorruption bool

	checkpoint := NewCPUPluginCheckpoint()
	if err = sc.checkpointManager.GetCheckpoint(sc.checkpointName, checkpoint); err != nil {
		if err == errors.ErrCheckpointNotFound {
			// We cannot find checkpoint, so it is possible that previous checkpoint was stored in either disk or memory
			return sc.tryMigrateState(topology, currentStateDir, otherStateDir, hasPreStop, checkpoint)
		} else if err == errors.ErrCorruptCheckpoint {
			if !sc.skipStateCorruption {
				return err
			}

			foundAndSkippedStateCorruption = true
			klog.Warningf("[cpu_plugin] restore checkpoint failed with err: %s, but we skip it", err)
		} else {
			return err
		}
	}

	return sc.populateCacheAndState(topology, checkpoint, foundAndSkippedStateCorruption)
}

func (sc *stateCheckpoint) populateCacheAndState(
	topology *machine.CPUTopology, checkpoint *CPUPluginCheckpoint, foundAndSkippedStateCorruption bool,
) error {
	generatedMachineState, err := sc.GenerateMachineStateFromPodEntries(topology, checkpoint.PodEntries, checkpoint.MachineState)
	if err != nil {
		return fmt.Errorf("GenerateMachineStateFromPodEntries failed with error: %v", err)
	}

	sc.cache.SetMachineState(generatedMachineState)
	sc.cache.SetPodEntries(checkpoint.PodEntries)
	sc.cache.SetNUMAHeadroom(checkpoint.NUMAHeadroom)
	sc.cache.SetAllowSharedCoresOverlapReclaimedCores(checkpoint.AllowSharedCoresOverlapReclaimedCores)

	if !reflect.DeepEqual(generatedMachineState, checkpoint.MachineState) {
		klog.Warningf("[cpu_plugin] machine state changed: generatedMachineState: %s; checkpointMachineState: %s",
			generatedMachineState.String(), checkpoint.MachineState.String())
		err = sc.storeState()
		if err != nil {
			return fmt.Errorf("storeState when machine state changed failed with error: %v", err)
		}
	}

	if foundAndSkippedStateCorruption {
		klog.Infof("[cpu_plugin] found and skipped state corruption, we should store to rectify the checksum")
		err = sc.storeState()
		if err != nil {
			return fmt.Errorf("storeState failed with error after skipping corruption: %v", err)
		}
	}

	klog.InfoS("[cpu_plugin] State checkpoint: restored state from checkpoint")
	return nil
}

// tryMigrateState tries to migrate the state file from the other directory to current directory.
// If the other directory does not have a state file, then we build a new checkpoint.
func (sc *stateCheckpoint) tryMigrateState(
	topology *machine.CPUTopology, currentStateDir, otherStateDir string, hasPreStop bool,
	checkpoint *CPUPluginCheckpoint,
) error {
	var foundAndSkippedStateCorruption bool
	klog.Infof("[cpu_plugin] trying to migrate state")

	// Do not migrate and build new checkpoint if there is no pre-stop script
	if !hasPreStop {
		return sc.storeState()
	}

	// Get the old checkpoint using the provided file directory
	oldCheckpointManager, err := checkpointmanager.NewCheckpointManager(otherStateDir)
	if err != nil {
		return fmt.Errorf("[cpu_plugin] failed to initialize old checkpoint manager for migration: %v", err)
	}

	if err = oldCheckpointManager.GetCheckpoint(sc.checkpointName, checkpoint); err != nil {
		if err == errors.ErrCheckpointNotFound {
			// Old checkpoint file is not found, so we just store state in new checkpoint
			general.Infof("[cpu_plugin] checkpoint %v doesn't exist in dir %v, create it", sc.checkpointName, otherStateDir)
			return sc.storeState()
		} else if err == errors.ErrCorruptCheckpoint {
			if !sc.skipStateCorruption {
				return err
			}
			foundAndSkippedStateCorruption = true
			klog.Warningf("[cpu_plugin] restore checkpoint failed with err: %s, but we skip it", err)
		} else {
			return err
		}
	}

	if err = sc.populateCacheAndState(topology, checkpoint, foundAndSkippedStateCorruption); err != nil {
		return fmt.Errorf("[cpu_plugin] failed to populate checkpoint state during state migration: %v", err)
	}

	// always store state after migrating to new checkpoint
	if err = sc.storeState(); err != nil {
		return fmt.Errorf("[cpu_plugin] failed to store checkpoint state during end of migration: %v", err)
	}

	// validate that the two files are equal
	equal, err := sc.checkpointFilesEqual(currentStateDir, otherStateDir)
	if err != nil {
		return fmt.Errorf("[cpu_plugin] failed to compare checkpoint files: %v", err)
	}
	if !equal {
		klog.Infof("[cpu_plugin] checkpoint files are not equal, migration failed, fall back to old checkpoint")
		return sc.fallbackToOldCheckpoint(oldCheckpointManager)
	}

	// remove old checkpoint file
	if err = oldCheckpointManager.RemoveCheckpoint(sc.checkpointName); err != nil {
		return fmt.Errorf("[cpu_plugin] failed to remove old checkpoint: %v", err)
	}

	klog.Infof("[cpu_plugin] migrate checkpoint succeeded")
	return nil
}

func (sc *stateCheckpoint) checkpointFilesEqual(currentStateDir, otherStateDir string) (bool, error) {
	currentFilePath := filepath.Join(currentStateDir, sc.checkpointName)
	otherFilePath := filepath.Join(otherStateDir, sc.checkpointName)
	return file.FilesEqual(currentFilePath, otherFilePath)
}

func (sc *stateCheckpoint) fallbackToOldCheckpoint(oldCheckpointManager checkpointmanager.CheckpointManager) error {
	sc.checkpointManager = oldCheckpointManager
	_ = general.UpdateHealthzState(qrmCPUStateCheckpointHealthCheckName, general.HealthzCheckStateNotReady, "Migration from old checkpoint to new checkpoint failed")
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
	checkpoint := NewCPUPluginCheckpoint()
	checkpoint.PolicyName = sc.policyName
	checkpoint.MachineState = sc.cache.GetMachineState()
	checkpoint.NUMAHeadroom = sc.cache.GetNUMAHeadroom()
	checkpoint.PodEntries = sc.cache.GetPodEntries()
	checkpoint.AllowSharedCoresOverlapReclaimedCores = sc.cache.GetAllowSharedCoresOverlapReclaimedCores()

	err := sc.checkpointManager.CreateCheckpoint(sc.checkpointName, checkpoint)
	if err != nil {
		klog.ErrorS(err, "Could not save checkpoint")
		return err
	}
	return nil
}

func (sc *stateCheckpoint) GetMachineState() NUMANodeMap {
	sc.RLock()
	defer sc.RUnlock()

	return sc.cache.GetMachineState()
}

func (sc *stateCheckpoint) GetNUMAHeadroom() map[int]float64 {
	sc.RLock()
	defer sc.RUnlock()

	return sc.cache.GetNUMAHeadroom()
}

func (sc *stateCheckpoint) GetAllocationInfo(podUID string, containerName string) *AllocationInfo {
	sc.RLock()
	defer sc.RUnlock()

	return sc.cache.GetAllocationInfo(podUID, containerName)
}

func (sc *stateCheckpoint) GetPodEntries() PodEntries {
	sc.RLock()
	defer sc.RUnlock()

	return sc.cache.GetPodEntries()
}

func (sc *stateCheckpoint) SetMachineState(numaNodeMap NUMANodeMap, persist bool) {
	sc.Lock()
	defer sc.Unlock()

	sc.cache.SetMachineState(numaNodeMap)
	if persist {
		err := sc.storeState()
		if err != nil {
			klog.ErrorS(err, "[cpu_plugin] store machineState to checkpoint error")
		}
	}
}

func (sc *stateCheckpoint) SetNUMAHeadroom(m map[int]float64, persist bool) {
	sc.Lock()
	defer sc.Unlock()

	sc.cache.SetNUMAHeadroom(m)
	if persist {
		err := sc.storeState()
		if err != nil {
			klog.ErrorS(err, "[cpu_plugin] store numa headroom to checkpoint error")
		}
	}
}

func (sc *stateCheckpoint) SetAllocationInfo(
	podUID string, containerName string, allocationInfo *AllocationInfo, persist bool,
) {
	sc.Lock()
	defer sc.Unlock()

	sc.cache.SetAllocationInfo(podUID, containerName, allocationInfo)
	if persist {
		err := sc.storeState()
		if err != nil {
			klog.ErrorS(err, "[cpu_plugin] store allocationInfo to checkpoint error")
		}
	}
}

func (sc *stateCheckpoint) SetPodEntries(podEntries PodEntries, persist bool) {
	sc.Lock()
	defer sc.Unlock()

	sc.cache.SetPodEntries(podEntries)
	if persist {
		err := sc.storeState()
		if err != nil {
			klog.ErrorS(err, "[cpu_plugin] store pod entries to checkpoint error")
		}
	}
}

func (sc *stateCheckpoint) SetAllowSharedCoresOverlapReclaimedCores(allowSharedCoresOverlapReclaimedCores, persist bool) {
	sc.Lock()
	defer sc.Unlock()

	sc.cache.SetAllowSharedCoresOverlapReclaimedCores(allowSharedCoresOverlapReclaimedCores)
	if persist {
		err := sc.storeState()
		if err != nil {
			klog.ErrorS(err, "[cpu_plugin] store allowSharedCoresOverlapReclaimedCores to checkpoint error")
		}
	}
}

func (sc *stateCheckpoint) GetAllowSharedCoresOverlapReclaimedCores() bool {
	sc.RLock()
	defer sc.RUnlock()

	return sc.cache.GetAllowSharedCoresOverlapReclaimedCores()
}

func (sc *stateCheckpoint) Delete(podUID string, containerName string, persist bool) {
	sc.Lock()
	defer sc.Unlock()

	sc.cache.Delete(podUID, containerName)
	if persist {
		err := sc.storeState()
		if err != nil {
			klog.ErrorS(err, "[cpu_plugin] store state after delete operation to checkpoint error")
		}
	}
}

func (sc *stateCheckpoint) ClearState() {
	sc.Lock()
	defer sc.Unlock()

	sc.cache.ClearState()
	err := sc.storeState()
	if err != nil {
		klog.ErrorS(err, "[cpu_plugin] store state after clear operation to checkpoint error")
	}
}

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
	stdErrors "errors"
	"fmt"
	"path"
	"reflect"
	"sync"
	"time"

	"k8s.io/klog/v2"
	"k8s.io/kubernetes/pkg/kubelet/checkpointmanager/errors"

	"github.com/kubewharf/katalyst-core/pkg/config/agent/qrm/statedirectory"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
	"github.com/kubewharf/katalyst-core/pkg/util/qrmcheckpointmanager"
)

const (
	metricMetaCacheStoreStateDuration = "metacache_store_state_duration"
)

// stateCheckpoint is an in-memory implementation of State;
// everytime we want to read or write states, those requests will always
// go to in-memory State, and then go to disk State, i.e. in write-back mode
type stateCheckpoint struct {
	sync.RWMutex
	cache                *cpuPluginState
	policyName           string
	qrmCheckpointManager *qrmcheckpointmanager.QRMCheckpointManager
	checkpointName       string
	// when we add new properties to checkpoint,
	// it will cause checkpoint corruption, and we should skip it
	skipStateCorruption                bool
	GenerateMachineStateFromPodEntries GenerateMachineStateFromPodEntriesFunc
	emitter                            metrics.MetricEmitter
	hasPreStop                         bool
}

var _ State = &stateCheckpoint{}

func NewCheckpointState(
	stateDirectoryConfig *statedirectory.StateDirectoryConfiguration, checkpointName, policyName string,
	topology *machine.CPUTopology, skipStateCorruption bool,
	generateMachineStateFunc GenerateMachineStateFromPodEntriesFunc,
	emitter metrics.MetricEmitter,
) (State, error) {
	currentStateDir, otherStateDir := stateDirectoryConfig.GetCurrentAndPreviousStateFileDirectory()
	hasPreStop := stateDirectoryConfig.HasPreStop

	qrmCheckpointManager, err := qrmcheckpointmanager.NewQRMCheckpointManager(currentStateDir, otherStateDir, checkpointName, "cpu_plugin")
	if err != nil {
		return nil, fmt.Errorf("failed to initialize checkpoint manager: %v", err)
	}
	sc := &stateCheckpoint{
		cache:                              NewCPUPluginState(topology),
		policyName:                         policyName,
		qrmCheckpointManager:               qrmCheckpointManager,
		checkpointName:                     checkpointName,
		skipStateCorruption:                skipStateCorruption,
		GenerateMachineStateFromPodEntries: generateMachineStateFunc,
		emitter:                            emitter,
		hasPreStop:                         hasPreStop,
	}

	if err := sc.restoreState(topology); err != nil {
		return nil, fmt.Errorf("could not restore state from checkpoint: %v, please drain this node and delete "+
			"the cpu plugin checkpoint file %q before restarting Kubelet", err, path.Join(currentStateDir, checkpointName))
	}

	return sc, nil
}

// restoreState is first done by searching the current directory for the state file.
// If it does not exist, we search the other directory for the state file and try to migrate the state file over to the current directory.
func (sc *stateCheckpoint) restoreState(topology *machine.CPUTopology) error {
	sc.Lock()
	defer sc.Unlock()
	var err error
	var foundAndSkippedStateCorruption bool

	checkpoint := NewCPUPluginCheckpoint()
	if err = sc.qrmCheckpointManager.GetCurrentCheckpoint(sc.checkpointName, checkpoint, true); err != nil {
		if stdErrors.Is(err, errors.ErrCheckpointNotFound) {
			// We cannot find checkpoint, so it is possible that previous checkpoint was stored in either disk or memory
			return sc.tryMigrateState(topology, checkpoint)
		} else if stdErrors.Is(err, errors.ErrCorruptCheckpoint) {
			if !sc.skipStateCorruption {
				return err
			}

			foundAndSkippedStateCorruption = true
			klog.Warningf("[cpu_plugin] restore checkpoint failed with err: %s, but we skip it", err)
		} else {
			return err
		}
	}

	_, err = sc.updateCacheAndReturnChanged(topology, checkpoint, foundAndSkippedStateCorruption)
	return err
}

// updateCacheAndReturnChanged updates the cache and returns whether the state has changed
func (sc *stateCheckpoint) updateCacheAndReturnChanged(
	topology *machine.CPUTopology, checkpoint *CPUPluginCheckpoint, foundAndSkippedStateCorruption bool,
) (bool, error) {
	var hasStateChanged bool
	if sc.policyName != checkpoint.PolicyName && !sc.skipStateCorruption {
		return hasStateChanged, fmt.Errorf("[cpu_plugin] configured policy %q differs from state checkpoint policy %q", sc.policyName, checkpoint.PolicyName)
	}

	generatedMachineState, err := sc.GenerateMachineStateFromPodEntries(topology, checkpoint.PodEntries)
	if err != nil {
		return hasStateChanged, fmt.Errorf("GenerateMachineStateFromPodEntries failed with error: %v", err)
	}

	sc.cache.SetMachineState(generatedMachineState)
	sc.cache.SetPodEntries(checkpoint.PodEntries)
	sc.cache.SetNUMAHeadroom(checkpoint.NUMAHeadroom)
	sc.cache.SetAllowSharedCoresOverlapReclaimedCores(checkpoint.AllowSharedCoresOverlapReclaimedCores)

	if !reflect.DeepEqual(generatedMachineState, checkpoint.MachineState) {
		klog.Warningf("[cpu_plugin] machine state changed: generatedMachineState: %s; checkpointMachineState: %s",
			generatedMachineState.String(), checkpoint.MachineState.String())
		hasStateChanged = true
		err = sc.storeState()
		if err != nil {
			return hasStateChanged, fmt.Errorf("storeState when machine state changed failed with error: %v", err)
		}
	}

	if foundAndSkippedStateCorruption {
		klog.Infof("[cpu_plugin] found and skipped state corruption, we should store to rectify the checksum")
		err = sc.storeState()
		if err != nil {
			return hasStateChanged, fmt.Errorf("storeState failed with error after skipping corruption: %v", err)
		}
	}

	klog.InfoS("[cpu_plugin] State checkpoint: restored state from checkpoint")
	return hasStateChanged, nil
}

// tryMigrateState tries to migrate the state file from the other directory to current directory.
// If the other directory does not have a state file, then we build a new checkpoint.
func (sc *stateCheckpoint) tryMigrateState(
	topology *machine.CPUTopology, checkpoint *CPUPluginCheckpoint,
) error {
	var foundAndSkippedStateCorruption bool
	klog.Infof("[cpu_plugin] trying to migrate state")

	// Do not migrate and build new checkpoint if there is no pre-stop script
	if !sc.hasPreStop {
		return sc.storeState()
	}

	if err := sc.qrmCheckpointManager.GetPreviousCheckpoint(sc.checkpointName, checkpoint); err != nil {
		if stdErrors.Is(err, errors.ErrCheckpointNotFound) {
			// Old checkpoint file is not found, so we just store state in new checkpoint
			general.Infof("[cpu_plugin] checkpoint %v doesn't exist, create it", sc.checkpointName)
			return sc.storeState()
		} else if stdErrors.Is(err, errors.ErrCorruptCheckpoint) {
			if !sc.skipStateCorruption {
				return err
			}
			foundAndSkippedStateCorruption = true
			klog.Warningf("[cpu_plugin] restore checkpoint failed with err: %s, but we skip it", err)
		} else {
			return err
		}
	}

	hasStateChanged, err := sc.updateCacheAndReturnChanged(topology, checkpoint, foundAndSkippedStateCorruption)
	if err != nil {
		return fmt.Errorf("[cpu_plugin] failed to populate checkpoint state during state migration: %v", err)
	}

	// always store state after migrating to new checkpoint
	if err := sc.storeState(); err != nil {
		return fmt.Errorf("[cpu_plugin] failed to store checkpoint state during end of migration: %v", err)
	}

	if err := sc.qrmCheckpointManager.ValidateCheckpointFilesMigration(hasStateChanged); err != nil {
		return fmt.Errorf("[cpu_plugin] ValidateCheckpointFilesMigration failed with error: %v", err)
	}

	klog.Infof("[cpu_plugin] migrate checkpoint succeeded")
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

	err := sc.qrmCheckpointManager.CreateCheckpoint(sc.checkpointName, checkpoint)
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

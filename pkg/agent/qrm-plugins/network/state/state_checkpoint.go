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
	"reflect"
	"sync"
	"time"

	"github.com/kubewharf/katalyst-core/pkg/config/agent/qrm/statedirectory"

	"k8s.io/klog/v2"

	info "github.com/google/cadvisor/info/v1"

	"github.com/kubewharf/katalyst-core/pkg/config/agent/qrm"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"

	"k8s.io/kubernetes/pkg/kubelet/checkpointmanager"
	"k8s.io/kubernetes/pkg/kubelet/checkpointmanager/errors"
)

const (
	metricMetaCacheStoreStateDuration = "metacache_store_state_duration"
)

var (
	_          State          = &stateCheckpoint{}
	generalLog general.Logger = general.LoggerWithPrefix("network_plugin", general.LoggingPKGFull)
)

// stateCheckpoint is an in-memory implementation of State;
// everytime we want to read or write states, those requests will always
// go to in-memory State, and then go to disk State, i.e. in write-back mode
type stateCheckpoint struct {
	sync.RWMutex
	cache             *networkPluginState
	policyName        string
	checkpointManager checkpointmanager.CheckpointManager
	checkpointName    string
	// when we add new properties to checkpoint,
	// it will cause checkpoint corruption and we should skip it
	skipStateCorruption bool
	emitter             metrics.MetricEmitter
}

func NewCheckpointState(conf *qrm.QRMPluginsConfiguration, stateDirectoryConfig *statedirectory.StateDirectoryConfiguration, checkpointName, policyName string,
	machineInfo *info.MachineInfo, nics []machine.InterfaceInfo, reservedBandwidth map[string]uint32,
	skipStateCorruption bool, emitter metrics.MetricEmitter,
) (State, error) {
	currentStateDir, otherStateDir := stateDirectoryConfig.GetCurrentAndOtherStateFileDirectory()
	checkpointManager, err := checkpointmanager.NewCheckpointManager(currentStateDir)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize checkpoint manager: %v", err)
	}

	defaultCache, err := NewNetworkPluginState(conf, machineInfo, nics, reservedBandwidth)
	if err != nil {
		return nil, fmt.Errorf("NewNetworkPluginState failed with error: %v", err)
	}

	stateCheckpoint := &stateCheckpoint{
		cache:               defaultCache,
		policyName:          policyName,
		checkpointManager:   checkpointManager,
		checkpointName:      checkpointName,
		skipStateCorruption: skipStateCorruption,
		emitter:             emitter,
	}

	if err := stateCheckpoint.restoreState(otherStateDir, conf, nics, reservedBandwidth); err != nil {
		return nil, fmt.Errorf("could not restore state from checkpoint: %v, please drain this node and delete the network plugin checkpoint file %q before restarting Kubelet",
			err, path.Join(currentStateDir, checkpointName))
	}

	return stateCheckpoint, nil
}

// restoreState is first done by searching the current directory for the state file.
// If it does not exist, we search the other directory for the state file and try to migrate the state file over to the current directory.
func (sc *stateCheckpoint) restoreState(otherStateDir string, conf *qrm.QRMPluginsConfiguration, nics []machine.InterfaceInfo, reservedBandwidth map[string]uint32) error {
	sc.Lock()
	defer sc.Unlock()
	var err error
	var foundAndSkippedStateCorruption bool

	checkpoint := NewNetworkPluginCheckpoint()
	if err = sc.checkpointManager.GetCheckpoint(sc.checkpointName, checkpoint); err != nil {
		if err == errors.ErrCheckpointNotFound {
			return sc.tryMigrateState(conf, nics, reservedBandwidth, otherStateDir, checkpoint)
		} else if err == errors.ErrCorruptCheckpoint {
			if !sc.skipStateCorruption {
				return err
			}

			foundAndSkippedStateCorruption = true
			generalLog.Infof("restore checkpoint failed with err: %s, but we skip it", err)
		} else {
			return err
		}
	}

	return sc.populateCacheAndState(conf, nics, reservedBandwidth, checkpoint, foundAndSkippedStateCorruption)
}

func (sc *stateCheckpoint) populateCacheAndState(conf *qrm.QRMPluginsConfiguration, nics []machine.InterfaceInfo, reservedBandwidth map[string]uint32, checkpoint *NetworkPluginCheckpoint, foundAndSkippedStateCorruption bool) error {
	if sc.policyName != checkpoint.PolicyName && !sc.skipStateCorruption {
		return fmt.Errorf("[network_plugin] configured policy %q differs from state checkpoint policy %q", sc.policyName, checkpoint.PolicyName)
	}

	generatedNetworkState, err := GenerateMachineStateFromPodEntries(conf, nics, checkpoint.PodEntries, reservedBandwidth)
	if err != nil {
		return fmt.Errorf("GenerateMachineStateFromPodEntries failed with error: %v", err)
	}

	sc.cache.SetMachineState(generatedNetworkState)
	sc.cache.SetPodEntries(checkpoint.PodEntries)

	if !reflect.DeepEqual(generatedNetworkState, checkpoint.MachineState) {
		generalLog.Warningf("machine state changed: "+
			"generatedNetworkState: %s; checkpointMachineState: %s",
			generatedNetworkState.String(), checkpoint.MachineState.String())

		err = sc.storeState()
		if err != nil {
			return fmt.Errorf("storeState when machine state changed failed with error: %v", err)
		}
	}

	if foundAndSkippedStateCorruption {
		generalLog.Infof("found and skipped state corruption, we shoud store to rectify the checksum")

		err = sc.storeState()
		if err != nil {
			return fmt.Errorf("storeState failed with error: %v", err)
		}
	}

	generalLog.InfoS("state checkpoint: restored state from checkpoint")

	return nil
}

// tryMigrateState tries to migrate the state file from the other directory to current directory.
// If the other directory does not have a state file, then we build a new checkpoint.
func (sc *stateCheckpoint) tryMigrateState(conf *qrm.QRMPluginsConfiguration, nics []machine.InterfaceInfo, reservedBandwidth map[string]uint32, otherStateDir string, checkpoint *NetworkPluginCheckpoint) error {
	var foundAndSkippedStateCorruption bool
	klog.Infof("[network_plugin] trying to migrate state")

	// Build new checkpoint if the state directory that we want to migrate from is empty
	if otherStateDir == "" {
		return sc.storeState()
	}

	// Get the old checkpoint using provided file directory
	oldCheckpointManager, err := checkpointmanager.NewCheckpointManager(otherStateDir)
	if err != nil {
		return fmt.Errorf("[network_plugin] failed to initialize old checkpoint manager for migration: %v", err)
	}

	if err = oldCheckpointManager.GetCheckpoint(sc.checkpointName, checkpoint); err != nil {
		if err == errors.ErrCheckpointNotFound {
			// Old checkpoint file is not found, so we just store state in new checkpoint
			general.InfoS("[network_plugin] checkpoint %v doesn't exist, create it", sc.checkpointName)
			return sc.storeState()
		} else if err == errors.ErrCorruptCheckpoint {
			if !sc.skipStateCorruption {
				return err
			}
			foundAndSkippedStateCorruption = true
			klog.Warningf("[network_plugin] restore checkpoint failed with err: %s, but we skip it", err)
		} else {
			return err
		}
	}

	if err = sc.populateCacheAndState(conf, nics, reservedBandwidth, checkpoint, foundAndSkippedStateCorruption); err != nil {
		return fmt.Errorf("[network_plugin] failed to populate checkpoint state during state migration: %v", err)
	}

	// always store state after migrating to new checkpoint
	if err = sc.storeState(); err != nil {
		return fmt.Errorf("[network_plugin] failed to store checkpoint state during end of migration: %v", err)
	}

	klog.Infof("[network_plugin] checkpoint migration succeeded")
	return nil
}

func (sc *stateCheckpoint) storeState() error {
	startTime := time.Now()
	general.InfoS("called")
	defer func() {
		elapsed := time.Since(startTime)
		general.InfoS("finished", "duration", elapsed)
		_ = sc.emitter.StoreFloat64(metricMetaCacheStoreStateDuration, float64(elapsed/time.Millisecond), metrics.MetricTypeNameRaw)
	}()
	checkpoint := NewNetworkPluginCheckpoint()
	checkpoint.PolicyName = sc.policyName
	checkpoint.MachineState = sc.cache.GetMachineState()
	checkpoint.PodEntries = sc.cache.GetPodEntries()

	err := sc.checkpointManager.CreateCheckpoint(sc.checkpointName, checkpoint)
	if err != nil {
		generalLog.ErrorS(err, "could not save checkpoint")
		return err
	}
	return nil
}

func (sc *stateCheckpoint) GetReservedBandwidth() map[string]uint32 {
	sc.RLock()
	defer sc.RUnlock()

	return sc.cache.GetReservedBandwidth()
}

func (sc *stateCheckpoint) GetMachineInfo() *info.MachineInfo {
	sc.RLock()
	defer sc.RUnlock()

	return sc.cache.GetMachineInfo()
}

func (sc *stateCheckpoint) GetEnabledNICs() []machine.InterfaceInfo {
	sc.RLock()
	defer sc.RUnlock()

	return sc.cache.GetEnabledNICs()
}

func (sc *stateCheckpoint) GetMachineState() NICMap {
	sc.RLock()
	defer sc.RUnlock()

	return sc.cache.GetMachineState()
}

func (sc *stateCheckpoint) GetAllocationInfo(podUID, containerName string) *AllocationInfo {
	sc.RLock()
	defer sc.RUnlock()

	return sc.cache.GetAllocationInfo(podUID, containerName)
}

func (sc *stateCheckpoint) GetPodEntries() PodEntries {
	sc.RLock()
	defer sc.RUnlock()

	return sc.cache.GetPodEntries()
}

func (sc *stateCheckpoint) SetMachineState(nicMap NICMap, persist bool) {
	sc.Lock()
	defer sc.Unlock()

	sc.cache.SetMachineState(nicMap)
	if persist {
		err := sc.storeState()
		if err != nil {
			generalLog.ErrorS(err, "store machineState to checkpoint error")
		}
	}
}

func (sc *stateCheckpoint) SetAllocationInfo(podUID, containerName string, allocationInfo *AllocationInfo, persist bool) {
	sc.Lock()
	defer sc.Unlock()

	sc.cache.SetAllocationInfo(podUID, containerName, allocationInfo)
	if persist {
		err := sc.storeState()
		if err != nil {
			generalLog.ErrorS(err, "store allocationInfo to checkpoint error")
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
			generalLog.ErrorS(err, "store pod entries to checkpoint error", "err")
		}
	}
}

func (sc *stateCheckpoint) Delete(podUID, containerName string, persist bool) {
	sc.Lock()
	defer sc.Unlock()

	sc.cache.Delete(podUID, containerName)
	if persist {
		err := sc.storeState()
		if err != nil {
			generalLog.ErrorS(err, "store state after delete operation to checkpoint error")
		}
	}
}

func (sc *stateCheckpoint) ClearState() {
	sc.Lock()
	defer sc.Unlock()

	sc.cache.ClearState()
	err := sc.storeState()
	if err != nil {
		generalLog.ErrorS(err, "store state after clear operation to checkpoint error")
	}
}

func (sc *stateCheckpoint) StoreState() error {
	sc.Lock()
	defer sc.Unlock()
	return sc.storeState()
}

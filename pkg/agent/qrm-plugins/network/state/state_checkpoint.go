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

	info "github.com/google/cadvisor/info/v1"
	"k8s.io/klog/v2"
	"k8s.io/kubernetes/pkg/kubelet/checkpointmanager/errors"

	"github.com/kubewharf/katalyst-core/pkg/config/agent/qrm"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/qrm/statedirectory"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
	"github.com/kubewharf/katalyst-core/pkg/util/qrmcheckpointmanager"
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
	cache                *networkPluginState
	policyName           string
	qrmCheckpointManager *qrmcheckpointmanager.QRMCheckpointManager
	checkpointName       string
	// when we add new properties to checkpoint,
	// it will cause checkpoint corruption and we should skip it
	skipStateCorruption bool
	emitter             metrics.MetricEmitter
	hasPreStop          bool
}

func NewCheckpointState(
	conf *qrm.QRMPluginsConfiguration, stateDirectoryConfig *statedirectory.StateDirectoryConfiguration,
	checkpointName, policyName string,
	machineInfo *info.MachineInfo, nics []machine.InterfaceInfo, reservedBandwidth map[string]uint32,
	skipStateCorruption bool, emitter metrics.MetricEmitter,
) (State, error) {
	currentStateDir, otherStateDir := stateDirectoryConfig.GetCurrentAndPreviousStateFileDirectory()
	hasPreStop := stateDirectoryConfig.HasPreStop

	qrmCheckpointManager, err := qrmcheckpointmanager.NewQRMCheckpointManager(currentStateDir, otherStateDir, checkpointName, "network_plugin")
	if err != nil {
		return nil, fmt.Errorf("failed to initialize checkpoint manager: %v", err)
	}

	defaultCache, err := NewNetworkPluginState(conf, machineInfo, nics, reservedBandwidth)
	if err != nil {
		return nil, fmt.Errorf("NewNetworkPluginState failed with error: %v", err)
	}

	stateCheckpoint := &stateCheckpoint{
		cache:                defaultCache,
		policyName:           policyName,
		qrmCheckpointManager: qrmCheckpointManager,
		checkpointName:       checkpointName,
		skipStateCorruption:  skipStateCorruption,
		emitter:              emitter,
		hasPreStop:           hasPreStop,
	}

	if err := stateCheckpoint.restoreState(conf, nics, reservedBandwidth); err != nil {
		return nil, fmt.Errorf("could not restore state from checkpoint: %v, please drain this node and delete the network plugin checkpoint file %q before restarting Kubelet",
			err, path.Join(currentStateDir, checkpointName))
	}

	return stateCheckpoint, nil
}

// restoreState is first done by searching the current directory for the state file.
// If it does not exist, we search the other directory for the state file and try to migrate the state file over to the current directory.
func (sc *stateCheckpoint) restoreState(
	conf *qrm.QRMPluginsConfiguration, nics []machine.InterfaceInfo, reservedBandwidth map[string]uint32,
) error {
	sc.Lock()
	defer sc.Unlock()
	var err error
	var foundAndSkippedStateCorruption bool

	checkpoint := NewNetworkPluginCheckpoint()
	if err = sc.qrmCheckpointManager.GetCurrentCheckpoint(sc.checkpointName, checkpoint, true); err != nil {
		if stdErrors.Is(err, errors.ErrCheckpointNotFound) {
			// We cannot find checkpoint, so it is possible that previous checkpoint was stored in either disk or memory
			return sc.tryMigrateState(conf, nics, reservedBandwidth, checkpoint)
		} else if stdErrors.Is(err, errors.ErrCorruptCheckpoint) {
			if !sc.skipStateCorruption {
				return err
			}

			foundAndSkippedStateCorruption = true
			generalLog.Infof("restore checkpoint failed with err: %s, but we skip it", err)
		} else {
			return err
		}
	}

	_, err = sc.updateCacheAndReturnChanged(conf, nics, reservedBandwidth, checkpoint, foundAndSkippedStateCorruption)
	return err
}

// updateCacheAndReturnChanged updates the cache and returns whether the state has changed
func (sc *stateCheckpoint) updateCacheAndReturnChanged(
	conf *qrm.QRMPluginsConfiguration, nics []machine.InterfaceInfo, reservedBandwidth map[string]uint32,
	checkpoint *NetworkPluginCheckpoint, foundAndSkippedStateCorruption bool,
) (bool, error) {
	var hasStateChanged bool
	if sc.policyName != checkpoint.PolicyName && !sc.skipStateCorruption {
		return hasStateChanged, fmt.Errorf("[network_plugin] configured policy %q differs from state checkpoint policy %q", sc.policyName, checkpoint.PolicyName)
	}

	generatedNetworkState, err := GenerateMachineStateFromPodEntries(conf, nics, checkpoint.PodEntries, reservedBandwidth)
	if err != nil {
		return hasStateChanged, fmt.Errorf("GenerateMachineStateFromPodEntries failed with error: %v", err)
	}

	sc.cache.SetMachineState(generatedNetworkState)
	sc.cache.SetPodEntries(checkpoint.PodEntries)

	if !reflect.DeepEqual(generatedNetworkState, checkpoint.MachineState) {
		generalLog.Warningf("machine state changed: "+
			"generatedNetworkState: %s; checkpointMachineState: %s",
			generatedNetworkState.String(), checkpoint.MachineState.String())

		hasStateChanged = true
		err = sc.storeState()
		if err != nil {
			return hasStateChanged, fmt.Errorf("storeState when machine state changed failed with error: %v", err)
		}
	}

	if foundAndSkippedStateCorruption {
		generalLog.Infof("found and skipped state corruption, we shoud store to rectify the checksum")

		err = sc.storeState()
		if err != nil {
			return hasStateChanged, fmt.Errorf("storeState failed with error: %v", err)
		}
	}

	generalLog.InfoS("state checkpoint: restored state from checkpoint")

	return hasStateChanged, nil
}

// tryMigrateState tries to migrate the state file from the other directory to current directory.
// If the other directory does not have a state file, then we build a new checkpoint.
func (sc *stateCheckpoint) tryMigrateState(
	conf *qrm.QRMPluginsConfiguration, nics []machine.InterfaceInfo, reservedBandwidth map[string]uint32,
	checkpoint *NetworkPluginCheckpoint,
) error {
	var foundAndSkippedStateCorruption bool
	klog.Infof("[network_plugin] trying to migrate state")

	// Build new checkpoint if the state directory that we want to migrate from is empty
	if !sc.hasPreStop {
		return sc.storeState()
	}

	if err := sc.qrmCheckpointManager.GetPreviousCheckpoint(sc.checkpointName, checkpoint); err != nil {
		if stdErrors.Is(err, errors.ErrCheckpointNotFound) {
			// Old checkpoint file is not found, so we just store state in new checkpoint
			general.InfoS("[network_plugin] checkpoint %v doesn't exist, create it", sc.checkpointName)
			return sc.storeState()
		} else if stdErrors.Is(err, errors.ErrCorruptCheckpoint) {
			if !sc.skipStateCorruption {
				return err
			}
			foundAndSkippedStateCorruption = true
			klog.Warningf("[network_plugin] restore checkpoint failed with err: %s, but we skip it", err)
		} else {
			return err
		}
	}

	hasStateChanged, err := sc.updateCacheAndReturnChanged(conf, nics, reservedBandwidth, checkpoint, foundAndSkippedStateCorruption)
	if err != nil {
		return fmt.Errorf("[network_plugin] failed to populate checkpoint state during state migration: %v", err)
	}

	// always store state after migrating to new checkpoint
	if err := sc.storeState(); err != nil {
		return fmt.Errorf("[network_plugin] failed to store checkpoint state during end of migration: %v", err)
	}

	if err := sc.qrmCheckpointManager.ValidateCheckpointFilesMigration(hasStateChanged); err != nil {
		return fmt.Errorf("[network_plugin] ValidateCheckpointFilesMigration failed with error: %v", err)
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

	err := sc.qrmCheckpointManager.CreateCheckpoint(sc.checkpointName, checkpoint)
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

func (sc *stateCheckpoint) SetAllocationInfo(
	podUID, containerName string, allocationInfo *AllocationInfo, persist bool,
) {
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

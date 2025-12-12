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
	"sync"
	"time"

	info "github.com/google/cadvisor/info/v1"
	"k8s.io/kubernetes/pkg/kubelet/checkpointmanager"

	"github.com/kubewharf/katalyst-core/pkg/config/agent/qrm"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/qrm/statedirectory"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/customcheckpointmanager"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/state"
)

const (
	metricMetaCacheStoreStateDuration = "metacache_store_state_duration"
)

var (
	_ State          = &stateCheckpoint{}
	_ state.Storable = &stateCheckpoint{}

	generalLog = general.LoggerWithPrefix("sriov_plugin", general.LoggingPKGFull)
)

type stateCheckpoint struct {
	sync.RWMutex
	cache               *sriovPluginState
	policyName          string
	checkpointManager   checkpointmanager.CheckpointManager
	checkpointName      string
	skipStateCorruption bool
	emitter             metrics.MetricEmitter
	conf                *qrm.QRMPluginsConfiguration
}

func NewCheckpointState(
	conf *qrm.QRMPluginsConfiguration, stateDirectoryConfig *statedirectory.StateDirectoryConfiguration,
	checkpointName, policyName string, machineInfo *info.MachineInfo,
	skipStateCorruption bool, emitter metrics.MetricEmitter,
) (State, error) {
	currentStateDir, otherStateDir := stateDirectoryConfig.GetCurrentAndPreviousStateFileDirectory()

	cache, err := NewSriovPluginState(machineInfo)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize sriov plugin state: %v", err)
	}

	sc := &stateCheckpoint{
		cache:               cache,
		policyName:          policyName,
		checkpointName:      checkpointName,
		skipStateCorruption: skipStateCorruption,
		emitter:             emitter,
		conf:                conf,
	}

	cm, err := customcheckpointmanager.NewCustomCheckpointManager(currentStateDir, otherStateDir, checkpointName,
		"sriov_plugin", sc, skipStateCorruption)
	if err != nil {
		return nil, fmt.Errorf("[sriov_plugin] failed to initialize custom checkpoint manager: %v", err)
	}

	sc.checkpointManager = cm

	return sc, nil
}

// RestoreState implements Storable interface and restores the cache from checkpoint and returns if the state has changed.
func (sc *stateCheckpoint) RestoreState(cp checkpointmanager.Checkpoint) (bool, error) {
	checkpoint, ok := cp.(*SriovPluginCheckpoint)
	if !ok {
		return false, fmt.Errorf("checkpoint type assertion failed, expect *SriovPluginCheckpoint, got %T", cp)
	}

	if sc.policyName != checkpoint.PolicyName && !sc.skipStateCorruption {
		return false, fmt.Errorf("[sriov_plugin] configured policy %q differs from state checkpoint policy %q",
			sc.policyName, checkpoint.PolicyName)
	}

	sc.cache.SetMachineState(checkpoint.MachineState)
	sc.cache.SetPodEntries(checkpoint.PodEntries)

	return false, nil
}

func (sc *stateCheckpoint) storeState() error {
	startTime := time.Now()
	general.InfoS("called")
	defer func() {
		elapsed := time.Since(startTime)
		general.InfoS("finished", "duration", elapsed)
		_ = sc.emitter.StoreFloat64(metricMetaCacheStoreStateDuration, float64(elapsed/time.Millisecond), metrics.MetricTypeNameRaw)
	}()
	checkpoint := sc.InitNewCheckpoint(false)

	err := sc.checkpointManager.CreateCheckpoint(sc.checkpointName, checkpoint)
	if err != nil {
		generalLog.ErrorS(err, "could not save checkpoint")
		return err
	}
	return nil
}

// InitNewCheckpoint implements Storable interface and initializes an empty or non-empty new checkpoint.
func (sc *stateCheckpoint) InitNewCheckpoint(empty bool) checkpointmanager.Checkpoint {
	checkpoint := NewSriovPluginCheckpoint()
	if empty {
		return checkpoint
	}
	checkpoint.PolicyName = sc.policyName
	checkpoint.MachineState = sc.cache.GetMachineState()
	checkpoint.PodEntries = sc.cache.GetPodEntries()
	return checkpoint
}

func (sc *stateCheckpoint) SetMachineState(state VFState, persist bool) {
	sc.Lock()
	defer sc.Unlock()

	sc.cache.SetMachineState(state)
	if persist {
		err := sc.storeState()
		if err != nil {
			generalLog.ErrorS(err, "store machineState to checkpoint error")
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

func (sc *stateCheckpoint) GetMachineState() VFState {
	sc.RLock()
	defer sc.RUnlock()

	return sc.cache.GetMachineState()
}

func (sc *stateCheckpoint) GetPodEntries() PodEntries {
	sc.RLock()
	defer sc.RUnlock()

	return sc.cache.GetPodEntries()
}

func (sc *stateCheckpoint) GetMachineInfo() *info.MachineInfo {
	sc.RLock()
	defer sc.RUnlock()

	return sc.cache.GetMachineInfo()
}

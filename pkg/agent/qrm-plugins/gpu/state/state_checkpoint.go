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

	v1 "k8s.io/api/core/v1"
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
	_          State          = &stateCheckpoint{}
	_          state.Storable = &stateCheckpoint{}
	generalLog                = general.LoggerWithPrefix("gpu_plugin", general.LoggingPKGFull)
)

// stateCheckpoint is an in-memory implementation of State;
// everytime we want to read or write states, those requests will always
// go to in-memory State, and then go to disk State, i.e. in write-back mode
type stateCheckpoint struct {
	sync.RWMutex
	cache             State
	policyName        string
	checkpointManager checkpointmanager.CheckpointManager
	checkpointName    string
	// when we add new properties to checkpoint,
	// it will cause checkpoint corruption and we should skip it
	skipStateCorruption            bool
	defaultResourceStateGenerators *DefaultResourceStateGeneratorRegistry
	emitter                        metrics.MetricEmitter
}

func (s *stateCheckpoint) SetMachineState(allocationResourcesMap AllocationResourcesMap, persist bool) {
	s.Lock()
	defer s.Unlock()

	s.cache.SetMachineState(allocationResourcesMap, persist)
	if persist {
		err := s.storeState()
		if err != nil {
			generalLog.ErrorS(err, "store machineState to checkpoint error")
		}
	}
}

func (s *stateCheckpoint) SetResourceState(resourceName v1.ResourceName, allocationMap AllocationMap, persist bool) {
	s.Lock()
	defer s.Unlock()

	s.cache.SetResourceState(resourceName, allocationMap, persist)
	if persist {
		err := s.storeState()
		if err != nil {
			generalLog.ErrorS(err, "store resource state to checkpoint error")
		}
	}
}

func (s *stateCheckpoint) SetPodResourceEntries(podResourceEntries PodResourceEntries, persist bool) {
	s.Lock()
	defer s.Unlock()

	s.cache.SetPodResourceEntries(podResourceEntries, persist)
	if persist {
		err := s.storeState()
		if err != nil {
			generalLog.ErrorS(err, "store pod entries to checkpoint error", "err")
		}
	}
}

func (s *stateCheckpoint) SetAllocationInfo(
	resourceName v1.ResourceName, podUID, containerName string, allocationInfo *AllocationInfo, persist bool,
) {
	s.Lock()
	defer s.Unlock()

	s.cache.SetAllocationInfo(resourceName, podUID, containerName, allocationInfo, persist)
	if persist {
		err := s.storeState()
		if err != nil {
			generalLog.ErrorS(err, "store allocationInfo to checkpoint error")
		}
	}
}

func (s *stateCheckpoint) Delete(resourceName v1.ResourceName, podUID, containerName string, persist bool) {
	s.Lock()
	defer s.Unlock()

	s.cache.Delete(resourceName, podUID, containerName, persist)
	if persist {
		err := s.storeState()
		if err != nil {
			generalLog.ErrorS(err, "store state after delete operation to checkpoint error")
		}
	}
}

func (s *stateCheckpoint) ClearState() {
	s.Lock()
	defer s.Unlock()

	s.cache.ClearState()
	err := s.storeState()
	if err != nil {
		generalLog.ErrorS(err, "store state after clear operation to checkpoint error")
	}
}

func (s *stateCheckpoint) StoreState() error {
	s.Lock()
	defer s.Unlock()
	return s.storeState()
}

func (s *stateCheckpoint) GetMachineState() AllocationResourcesMap {
	s.RLock()
	defer s.RUnlock()

	return s.cache.GetMachineState()
}

func (s *stateCheckpoint) GetPodResourceEntries() PodResourceEntries {
	s.RLock()
	defer s.RUnlock()

	return s.cache.GetPodResourceEntries()
}

func (s *stateCheckpoint) GetPodEntries(resourceName v1.ResourceName) PodEntries {
	s.RLock()
	defer s.RUnlock()

	return s.cache.GetPodEntries(resourceName)
}

func (s *stateCheckpoint) GetAllocationInfo(
	resourceName v1.ResourceName, podUID, containerName string,
) *AllocationInfo {
	s.RLock()
	defer s.RUnlock()

	return s.cache.GetAllocationInfo(resourceName, podUID, containerName)
}

func (s *stateCheckpoint) storeState() error {
	startTime := time.Now()
	general.InfoS("called")
	defer func() {
		elapsed := time.Since(startTime)
		general.InfoS("finished", "duration", elapsed)
		_ = s.emitter.StoreFloat64(metricMetaCacheStoreStateDuration, float64(elapsed/time.Millisecond), metrics.MetricTypeNameRaw)
	}()

	checkpoint := s.InitNewCheckpoint(false)

	err := s.checkpointManager.CreateCheckpoint(s.checkpointName, checkpoint)
	if err != nil {
		generalLog.ErrorS(err, "could not save checkpoint")
		return err
	}
	return nil
}

// InitNewCheckpoint implements Storable interface and initializes an empty or non-empty new checkpoint.
func (s *stateCheckpoint) InitNewCheckpoint(empty bool) checkpointmanager.Checkpoint {
	checkpoint := NewGPUPluginCheckpoint()
	if empty {
		return checkpoint
	}
	checkpoint.PolicyName = s.policyName
	checkpoint.MachineState = s.cache.GetMachineState()
	checkpoint.PodResourceEntries = s.cache.GetPodResourceEntries()
	return checkpoint
}

// RestoreState implements Storable interface and restores the cache from checkpoint and returns if the state has changed.
func (s *stateCheckpoint) RestoreState(cp checkpointmanager.Checkpoint) (bool, error) {
	checkpoint, ok := cp.(*GPUPluginCheckpoint)
	if !ok {
		return false, fmt.Errorf("checkpoint type assertion failed, expected *GPUPluginCheckpoint, got %T instead", cp)
	}

	if s.policyName != checkpoint.PolicyName && !s.skipStateCorruption {
		return false, fmt.Errorf("[gpu_plugin] configured policy %q differs from state checkpoint policy %q", s.policyName, checkpoint.PolicyName)
	}

	machineState, err := GenerateMachineStateFromPodEntries(checkpoint.PodResourceEntries, s.defaultResourceStateGenerators)
	if err != nil {
		return false, fmt.Errorf("GenerateMachineStateFromPodEntries failed with error: %v", err)
	}

	s.cache.SetMachineState(machineState, false)
	s.cache.SetPodResourceEntries(checkpoint.PodResourceEntries, false)

	if !reflect.DeepEqual(machineState, checkpoint.MachineState) {
		generalLog.Warningf("machine state changed: "+
			"machineState: %s; checkpointMachineState: %s",
			machineState.String(), checkpoint.MachineState.String())

		return true, nil
	}

	return false, nil
}

func NewCheckpointState(
	stateDirectoryConfig *statedirectory.StateDirectoryConfiguration,
	conf *qrm.QRMPluginsConfiguration, checkpointName, policyName string,
	defaultResourceStateGenerators *DefaultResourceStateGeneratorRegistry,
	skipStateCorruption bool, emitter metrics.MetricEmitter,
) (State, error) {
	currentStateDir, otherStateDir := stateDirectoryConfig.GetCurrentAndPreviousStateFileDirectory()

	defaultCache, err := NewGPUPluginState(conf, defaultResourceStateGenerators)
	if err != nil {
		return nil, fmt.Errorf("NewGPUPluginState failed with error: %v", err)
	}

	sc := &stateCheckpoint{
		cache:                          defaultCache,
		policyName:                     policyName,
		checkpointName:                 checkpointName,
		skipStateCorruption:            skipStateCorruption,
		emitter:                        emitter,
		defaultResourceStateGenerators: defaultResourceStateGenerators,
	}

	cm, err := customcheckpointmanager.NewCustomCheckpointManager(currentStateDir, otherStateDir, checkpointName,
		"gpu_plugin", sc, skipStateCorruption)
	if err != nil {
		return nil, fmt.Errorf("could not restore state from checkpoint: %v, please drain this node and delete "+
			"the gpu plugin checkpoint file %q before restarting Kubelet",
			err, path.Join(currentStateDir, checkpointName))
	}

	sc.checkpointManager = cm

	return sc, nil
}

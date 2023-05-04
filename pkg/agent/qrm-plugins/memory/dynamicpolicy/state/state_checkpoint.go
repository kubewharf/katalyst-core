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

	info "github.com/google/cadvisor/info/v1"
	v1 "k8s.io/api/core/v1"
	"k8s.io/klog/v2"
	"k8s.io/kubernetes/pkg/kubelet/checkpointmanager"
	"k8s.io/kubernetes/pkg/kubelet/checkpointmanager/errors"

	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

var _ State = &stateCheckpoint{}

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
	skipStateCorruption bool
}

func NewCheckpointState(stateDir, checkpointName, policyName string,
	topology *machine.CPUTopology, machineInfo *info.MachineInfo,
	reservedMemory map[v1.ResourceName]map[int]uint64, skipStateCorruption bool) (State, error) {

	checkpointManager, err := checkpointmanager.NewCheckpointManager(stateDir)
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
	}

	if err := stateCheckpoint.restoreState(machineInfo, reservedMemory); err != nil {
		return nil, fmt.Errorf("could not restore state from checkpoint: %v, please drain this node and delete the memory plugin checkpoint file %q before restarting Kubelet",
			err, path.Join(stateDir, checkpointName))
	}

	return stateCheckpoint, nil
}

func (sc *stateCheckpoint) restoreState(machineInfo *info.MachineInfo, reservedMemory map[v1.ResourceName]map[int]uint64) error {
	sc.Lock()
	defer sc.Unlock()
	var err error
	var foundAndSkippedStateCorruption bool

	checkpoint := NewMemoryPluginCheckpoint()
	if err = sc.checkpointManager.GetCheckpoint(sc.checkpointName, checkpoint); err != nil {
		if err == errors.ErrCheckpointNotFound {
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

	if sc.policyName != checkpoint.PolicyName && !sc.skipStateCorruption {
		return fmt.Errorf("[memory_plugin] configured policy %q differs from state checkpoint policy %q", sc.policyName, checkpoint.PolicyName)
	}

	generatedResourcesMachineState, err := GenerateMachineStateFromPodEntries(machineInfo, checkpoint.PodResourceEntries, reservedMemory)

	if err != nil {
		return fmt.Errorf("GenerateMachineStateFromPodEntries failed with error: %v", err)
	}

	sc.cache.SetMachineState(generatedResourcesMachineState)
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

func (sc *stateCheckpoint) storeState() error {
	checkpoint := NewMemoryPluginCheckpoint()
	checkpoint.PolicyName = sc.policyName
	checkpoint.MachineState = sc.cache.GetMachineState()
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

func (sc *stateCheckpoint) GetAllocationInfo(resourceName v1.ResourceName, podUID, containerName string) *AllocationInfo {
	sc.RLock()
	defer sc.RUnlock()

	return sc.cache.GetAllocationInfo(resourceName, podUID, containerName)
}

func (sc *stateCheckpoint) GetPodResourceEntries() PodResourceEntries {
	sc.RLock()
	defer sc.RUnlock()

	return sc.cache.GetPodResourceEntries()
}

func (sc *stateCheckpoint) SetMachineState(numaNodeResourcesMap NUMANodeResourcesMap) {
	sc.Lock()
	defer sc.Unlock()

	sc.cache.SetMachineState(numaNodeResourcesMap)
	err := sc.storeState()
	if err != nil {
		klog.ErrorS(err, "[memory_plugin] store machineState to checkpoint error")
	}
}

func (sc *stateCheckpoint) SetAllocationInfo(resourceName v1.ResourceName, podUID, containerName string, allocationInfo *AllocationInfo) {
	sc.Lock()
	defer sc.Unlock()

	sc.cache.SetAllocationInfo(resourceName, podUID, containerName, allocationInfo)
	err := sc.storeState()
	if err != nil {
		klog.ErrorS(err, "[memory_plugin] store allocationInfo to checkpoint error")
	}
}

func (sc *stateCheckpoint) SetPodResourceEntries(podResourceEntries PodResourceEntries) {
	sc.Lock()
	defer sc.Unlock()

	sc.cache.SetPodResourceEntries(podResourceEntries)
	err := sc.storeState()
	if err != nil {
		klog.ErrorS(err, "[memory_plugin] store pod entries to checkpoint error", "err")
	}
}

func (sc *stateCheckpoint) Delete(resourceName v1.ResourceName, podUID, containerName string) {
	sc.Lock()
	defer sc.Unlock()

	sc.cache.Delete(resourceName, podUID, containerName)
	err := sc.storeState()
	if err != nil {
		klog.ErrorS(err, "[memory_plugin] store state after delete operation to checkpoint error")
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

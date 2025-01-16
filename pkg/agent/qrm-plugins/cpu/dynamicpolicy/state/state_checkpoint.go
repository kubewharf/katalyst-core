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

	"k8s.io/klog/v2"
	"k8s.io/kubernetes/pkg/kubelet/checkpointmanager"
	"k8s.io/kubernetes/pkg/kubelet/checkpointmanager/errors"

	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
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
	// it will cause checkpoint corruption, and we should skip it
	skipStateCorruption                bool
	GenerateMachineStateFromPodEntries GenerateMachineStateFromPodEntriesFunc
}

var _ State = &stateCheckpoint{}

func NewCheckpointState(stateDir, checkpointName, policyName string,
	topology *machine.CPUTopology, skipStateCorruption bool, generateMachineStateFunc GenerateMachineStateFromPodEntriesFunc,
) (State, error) {
	checkpointManager, err := checkpointmanager.NewCheckpointManager(stateDir)
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
	}

	if err := sc.RestoreState(topology); err != nil {
		return nil, fmt.Errorf("could not restore state from checkpoint: %v, please drain this node and delete "+
			"the cpu plugin checkpoint file %q before restarting Kubelet", err, path.Join(stateDir, checkpointName))
	}
	return sc, nil
}

func (sc *stateCheckpoint) RestoreState(topology *machine.CPUTopology) error {
	sc.Lock()
	defer sc.Unlock()
	var err error
	var foundAndSkippedStateCorruption bool

	checkpoint := NewCPUPluginCheckpoint()
	if err = sc.checkpointManager.GetCheckpoint(sc.checkpointName, checkpoint); err != nil {
		if err == errors.ErrCheckpointNotFound {
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

	if sc.policyName != checkpoint.PolicyName && !sc.skipStateCorruption {
		return fmt.Errorf("[cpu_plugin] configured policy %q differs from state checkpoint policy %q", sc.policyName, checkpoint.PolicyName)
	}

	generatedMachineState, err := sc.GenerateMachineStateFromPodEntries(topology, checkpoint.PodEntries)
	if err != nil {
		return fmt.Errorf("GenerateMachineStateFromPodEntries failed with error: %v", err)
	}

	sc.cache.SetMachineState(generatedMachineState)
	sc.cache.SetPodEntries(checkpoint.PodEntries)
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
			return fmt.Errorf("storeState failed with error: %v", err)
		}
	}

	klog.InfoS("[cpu_plugin] State checkpoint: restored state from checkpoint")
	return nil
}

func (sc *stateCheckpoint) storeState() error {
	startTime := time.Now()
	general.InfoS("called")
	defer func() {
		general.InfoS("finished", "duration", time.Since(startTime))
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

func (sc *stateCheckpoint) SetMachineState(numaNodeMap NUMANodeMap) {
	sc.Lock()
	defer sc.Unlock()

	sc.cache.SetMachineState(numaNodeMap)
	err := sc.storeState()
	if err != nil {
		klog.ErrorS(err, "[cpu_plugin] store machineState to checkpoint error")
	}
}

func (sc *stateCheckpoint) SetNUMAHeadroom(m map[int]float64) {
	sc.Lock()
	defer sc.Unlock()

	sc.cache.SetNUMAHeadroom(m)
	err := sc.storeState()
	if err != nil {
		klog.ErrorS(err, "[cpu_plugin] store numa headroom to checkpoint error")
	}
}

func (sc *stateCheckpoint) SetAllocationInfo(podUID string, containerName string, allocationInfo *AllocationInfo) {
	sc.Lock()
	defer sc.Unlock()

	sc.cache.SetAllocationInfo(podUID, containerName, allocationInfo)
	err := sc.storeState()
	if err != nil {
		klog.ErrorS(err, "[cpu_plugin] store allocationInfo to checkpoint error")
	}
}

func (sc *stateCheckpoint) SetPodEntries(podEntries PodEntries) {
	sc.Lock()
	defer sc.Unlock()

	sc.cache.SetPodEntries(podEntries)
	err := sc.storeState()
	if err != nil {
		klog.ErrorS(err, "[cpu_plugin] store pod entries to checkpoint error")
	}
}

func (sc *stateCheckpoint) SetAllowSharedCoresOverlapReclaimedCores(allowSharedCoresOverlapReclaimedCores bool) {
	sc.Lock()
	defer sc.Unlock()

	sc.cache.SetAllowSharedCoresOverlapReclaimedCores(allowSharedCoresOverlapReclaimedCores)
	err := sc.storeState()
	if err != nil {
		klog.ErrorS(err, "[cpu_plugin] store allowSharedCoresOverlapReclaimedCores to checkpoint error")
	}
}

func (sc *stateCheckpoint) GetAllowSharedCoresOverlapReclaimedCores() bool {
	sc.RLock()
	defer sc.RUnlock()

	return sc.cache.GetAllowSharedCoresOverlapReclaimedCores()
}

func (sc *stateCheckpoint) Delete(podUID string, containerName string) {
	sc.Lock()
	defer sc.Unlock()

	sc.cache.Delete(podUID, containerName)
	err := sc.storeState()
	if err != nil {
		klog.ErrorS(err, "[cpu_plugin] store state after delete operation to checkpoint error")
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

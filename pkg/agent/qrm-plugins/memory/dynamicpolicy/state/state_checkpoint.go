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
	"reflect"
	"sync"
	"time"

	info "github.com/google/cadvisor/info/v1"
	v1 "k8s.io/api/core/v1"
	"k8s.io/klog/v2"
	"k8s.io/kubernetes/pkg/kubelet/checkpointmanager"

	"github.com/kubewharf/katalyst-core/pkg/config/agent/qrm/statedirectory"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/customcheckpointmanager"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
	"github.com/kubewharf/katalyst-core/pkg/util/state"
)

const (
	metricMetaCacheStoreStateDuration = "metacache_store_state_duration"
)

var (
	_ State          = &stateCheckpoint{}
	_ state.Storable = &stateCheckpoint{}
)

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
	machineInfo         *info.MachineInfo
	reservedMemory      map[v1.ResourceName]map[int]uint64
}

func NewCheckpointState(
	stateDirectoryConfig *statedirectory.StateDirectoryConfiguration, checkpointName, policyName string,
	topology *machine.CPUTopology, machineInfo *info.MachineInfo,
	reservedMemory map[v1.ResourceName]map[int]uint64, skipStateCorruption bool,
	emitter metrics.MetricEmitter,
) (State, error) {
	currentStateDir, otherStateDir := stateDirectoryConfig.GetCurrentAndPreviousStateFileDirectory()

	defaultCache, err := NewMemoryPluginState(topology, machineInfo, reservedMemory)
	if err != nil {
		return nil, fmt.Errorf("NewMemoryPluginState failed with error: %v", err)
	}

	sc := &stateCheckpoint{
		cache:               defaultCache,
		policyName:          policyName,
		checkpointName:      checkpointName,
		skipStateCorruption: skipStateCorruption,
		emitter:             emitter,
		machineInfo:         machineInfo,
		reservedMemory:      reservedMemory,
	}

	cm, err := customcheckpointmanager.NewCustomCheckpointManager(currentStateDir, otherStateDir, checkpointName,
		"memory_plugin", sc, skipStateCorruption)
	if err != nil {
		return nil, fmt.Errorf("[memory_plugin] failed to initialize custom checkpoint manager: %v", err)
	}

	sc.checkpointManager = cm

	return sc, nil
}

// RestoreState implements Storable interface and restores the cache from checkpoint and returns if the state has changed.
func (sc *stateCheckpoint) RestoreState(cp checkpointmanager.Checkpoint) (bool, error) {
	checkpoint, ok := cp.(*MemoryPluginCheckpoint)
	if !ok {
		return false, fmt.Errorf("checkpoint type assertion failed, expect *MemoryPluginCheckpoint, but got %T", cp)
	}

	if sc.policyName != checkpoint.PolicyName && !sc.skipStateCorruption {
		return false, fmt.Errorf("[memory_plugin] configured policy %q differs from state checkpoint policy %q", sc.policyName, checkpoint.PolicyName)
	}

	generatedResourcesMachineState, err := GenerateMachineStateFromPodEntries(sc.machineInfo, checkpoint.PodResourceEntries, checkpoint.MachineState, sc.reservedMemory)
	if err != nil {
		return false, fmt.Errorf("GenerateMachineStateFromPodEntries failed with error: %v", err)
	}

	sc.cache.SetMachineState(generatedResourcesMachineState)
	sc.cache.SetNUMAHeadroom(checkpoint.NUMAHeadroom)
	sc.cache.SetPodResourceEntries(checkpoint.PodResourceEntries)

	if !reflect.DeepEqual(generatedResourcesMachineState, checkpoint.MachineState) {
		klog.Warningf("[memory_plugin] machine state changed: "+
			"generatedResourcesMachineState: %s; checkpointMachineState: %s",
			generatedResourcesMachineState.String(), checkpoint.MachineState.String())
		return true, nil
	}

	return false, nil
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

	checkpoint := sc.InitNewCheckpoint(false)

	err := sc.checkpointManager.CreateCheckpoint(sc.checkpointName, checkpoint)
	if err != nil {
		klog.ErrorS(err, "Could not save checkpoint")
		return err
	}
	return nil
}

// InitNewCheckpoint implements Storable interface and initializes an empty or non-empty new checkpoint.
func (sc *stateCheckpoint) InitNewCheckpoint(empty bool) checkpointmanager.Checkpoint {
	checkpoint := NewMemoryPluginCheckpoint()
	if empty {
		return checkpoint
	}
	checkpoint.PolicyName = sc.policyName
	checkpoint.MachineState = sc.cache.GetMachineState()
	checkpoint.NUMAHeadroom = sc.cache.GetNUMAHeadroom()
	checkpoint.PodResourceEntries = sc.cache.GetPodResourceEntries()
	return checkpoint
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

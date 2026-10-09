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
	"math"
	"path"
	"reflect"
	"sync"
	"time"

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
	topology                           *machine.CPUTopology
	writeGate                          WriteGate
}

var (
	_ State          = &stateCheckpoint{}
	_ state.Storable = &stateCheckpoint{}
)

func NewCheckpointState(
	stateDirectoryConfig *statedirectory.StateDirectoryConfiguration, checkpointName, policyName string,
	topology *machine.CPUTopology, skipStateCorruption bool,
	generateMachineStateFunc GenerateMachineStateFromPodEntriesFunc,
	emitter metrics.MetricEmitter,
) (State, error) {
	currentStateDir, otherStateDir := stateDirectoryConfig.GetCurrentAndPreviousStateFileDirectory()

	sc := &stateCheckpoint{
		cache:                              NewCPUPluginState(topology),
		policyName:                         policyName,
		checkpointName:                     checkpointName,
		skipStateCorruption:                skipStateCorruption,
		GenerateMachineStateFromPodEntries: generateMachineStateFunc,
		emitter:                            emitter,
		topology:                           topology,
	}

	cm, err := customcheckpointmanager.NewCustomCheckpointManager(currentStateDir, otherStateDir, checkpointName,
		"cpu_plugin", sc, skipStateCorruption)
	if err != nil {
		return nil, fmt.Errorf("could not restore state from checkpoint: %v, please drain this node and delete "+
			"the cpu plugin checkpoint file %q before restarting Kubelet", err, path.Join(currentStateDir, checkpointName))
	}

	sc.checkpointManager = cm

	return sc, nil
}

// RestoreState implements Storable interface and restores the cache from checkpoint and returns if the state has changed.
func (sc *stateCheckpoint) RestoreState(cp checkpointmanager.Checkpoint) (bool, error) {
	checkpoint, ok := cp.(*CPUPluginCheckpoint)
	if !ok {
		return false, fmt.Errorf("checkpoint type assertion failed, expect *CPUPluginCheckpoint, got %T", cp)
	}

	if sc.policyName != checkpoint.PolicyName && !sc.skipStateCorruption {
		return false, fmt.Errorf("[cpu_plugin] configured policy %q differs from state checkpoint policy %q", sc.policyName, checkpoint.PolicyName)
	}
	if checkpoint.Revision == math.MaxUint64 {
		return false, fmt.Errorf("%w: checkpoint revision=%d", ErrStateRevisionOverflow, checkpoint.Revision)
	}

	generatedMachineState, err := sc.GenerateMachineStateFromPodEntries(sc.topology, checkpoint.PodEntries, checkpoint.MachineState)
	if err != nil {
		return false, fmt.Errorf("GenerateMachineStateFromPodEntries failed with error: %v", err)
	}

	sc.cache.Lock()
	sc.cache.machineState = generatedMachineState.Clone()
	sc.cache.podEntries = checkpoint.PodEntries.Clone()
	sc.cache.numaHeadroom = general.DeepCopyIntToFloat64Map(checkpoint.NUMAHeadroom)
	sc.cache.allowSharedCoresOverlapReclaimedCores = checkpoint.AllowSharedCoresOverlapReclaimedCores
	sc.cache.disableDedicatedCoresOverlapReclaimedCores = checkpoint.DisableDedicatedCoresOverlapReclaimedCores
	sc.cache.revision = checkpoint.Revision
	sc.cache.Unlock()

	if !reflect.DeepEqual(generatedMachineState, checkpoint.MachineState) {
		klog.Warningf("[cpu_plugin] machine state changed: generatedMachineState: %s; checkpointMachineState: %s",
			generatedMachineState.String(), checkpoint.MachineState.String())

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
	checkpoint := NewCPUPluginCheckpoint()
	if empty {
		return checkpoint
	}
	checkpoint.PolicyName = sc.policyName
	checkpoint.MachineState = sc.cache.GetMachineState()
	checkpoint.NUMAHeadroom = sc.cache.GetNUMAHeadroom()
	checkpoint.PodEntries = sc.cache.GetPodEntries()
	checkpoint.AllowSharedCoresOverlapReclaimedCores = sc.cache.GetAllowSharedCoresOverlapReclaimedCores()
	checkpoint.DisableDedicatedCoresOverlapReclaimedCores = sc.cache.GetDisableDedicatedCoresOverlapReclaimedCores()
	checkpoint.Revision = sc.cache.GetRevision()
	return checkpoint
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

func (sc *stateCheckpoint) GetAdvisorStateSnapshot() (PodEntries, NUMANodeMap, uint64) {
	sc.RLock()
	defer sc.RUnlock()

	return sc.cache.GetAdvisorStateSnapshot()
}

func (sc *stateCheckpoint) GetRevision() uint64 {
	sc.RLock()
	defer sc.RUnlock()

	return sc.cache.GetRevision()
}

func (sc *stateCheckpoint) SetWritePermit(gate WriteGate) {
	sc.Lock()
	defer sc.Unlock()
	sc.writeGate = gate
}

func (sc *stateCheckpoint) permitWrite(operation string, permits ...*WritePermit) error {
	if len(permits) > 1 {
		return fmt.Errorf("%s received %d write permits", operation, len(permits))
	}
	var permit *WritePermit
	if len(permits) == 1 {
		permit = permits[0]
	}
	if permit != nil && permit.consumed {
		return fmt.Errorf("%s write permit was already consumed", operation)
	}
	if sc.writeGate == nil {
		if permit != nil {
			return fmt.Errorf("%s write permit has no installed gate", operation)
		}
		return nil
	}
	if err := sc.writeGate(sc.cache.GetRevision(), operation, permit); err != nil {
		return err
	}
	if permit != nil {
		permit.consumed = true
	}
	return nil
}

func (sc *stateCheckpoint) storeStateOrRestore(snapshot cpuPluginStateData) error {
	if err := sc.storeState(); err != nil {
		sc.cache.restoreData(snapshot)
		return err
	}
	return nil
}

func (sc *stateCheckpoint) SetMachineState(numaNodeMap NUMANodeMap, persist bool) error {
	sc.Lock()
	defer sc.Unlock()

	if err := sc.permitWrite("SetMachineState"); err != nil {
		return err
	}
	var oldState cpuPluginStateData
	if persist {
		oldState = sc.cache.snapshotData()
	}
	if err := sc.cache.SetMachineState(numaNodeMap); err != nil {
		return err
	}
	if persist {
		return sc.storeStateOrRestore(oldState)
	}
	return nil
}

func (sc *stateCheckpoint) SetNUMAHeadroom(m map[int]float64, persist bool) error {
	return sc.SetNUMAHeadroomWithPermit(m, persist, nil)
}

func (sc *stateCheckpoint) SetNUMAHeadroomWithPermit(
	m map[int]float64,
	persist bool,
	permit *WritePermit,
) error {
	sc.Lock()
	defer sc.Unlock()

	if err := sc.permitWrite("SetNUMAHeadroom", permit); err != nil {
		return err
	}
	var oldState cpuPluginStateData
	if persist {
		oldState = sc.cache.snapshotData()
	}
	if err := sc.cache.SetNUMAHeadroom(m); err != nil {
		return err
	}
	if persist {
		return sc.storeStateOrRestore(oldState)
	}
	return nil
}

func (sc *stateCheckpoint) SetAllocationInfo(
	podUID string, containerName string, allocationInfo *AllocationInfo, persist bool,
) error {
	sc.Lock()
	defer sc.Unlock()

	if err := sc.permitWrite("SetAllocationInfo"); err != nil {
		return err
	}
	var oldState cpuPluginStateData
	if persist {
		oldState = sc.cache.snapshotData()
	}
	if err := sc.cache.SetAllocationInfo(podUID, containerName, allocationInfo); err != nil {
		return err
	}
	if persist {
		return sc.storeStateOrRestore(oldState)
	}
	return nil
}

func (sc *stateCheckpoint) SetPodEntries(podEntries PodEntries, persist bool) error {
	sc.Lock()
	defer sc.Unlock()

	if err := sc.permitWrite("SetPodEntries"); err != nil {
		return err
	}
	var oldState cpuPluginStateData
	if persist {
		oldState = sc.cache.snapshotData()
	}
	if err := sc.cache.SetPodEntries(podEntries); err != nil {
		return err
	}
	if persist {
		return sc.storeStateOrRestore(oldState)
	}
	return nil
}

func (sc *stateCheckpoint) SetAllowSharedCoresOverlapReclaimedCores(allowSharedCoresOverlapReclaimedCores, persist bool) error {
	sc.Lock()
	defer sc.Unlock()

	if err := sc.permitWrite("SetAllowSharedCoresOverlapReclaimedCores"); err != nil {
		return err
	}
	var oldState cpuPluginStateData
	if persist {
		oldState = sc.cache.snapshotData()
	}
	if err := sc.cache.SetAllowSharedCoresOverlapReclaimedCores(allowSharedCoresOverlapReclaimedCores); err != nil {
		return err
	}
	if persist {
		return sc.storeStateOrRestore(oldState)
	}
	return nil
}

func (sc *stateCheckpoint) SetDisableDedicatedCoresOverlapReclaimedCores(disableDedicatedCoresOverlapReclaimedCores, persist bool) error {
	sc.Lock()
	defer sc.Unlock()

	if err := sc.permitWrite("SetDisableDedicatedCoresOverlapReclaimedCores"); err != nil {
		return err
	}
	var oldState cpuPluginStateData
	if persist {
		oldState = sc.cache.snapshotData()
	}
	if err := sc.cache.SetDisableDedicatedCoresOverlapReclaimedCores(disableDedicatedCoresOverlapReclaimedCores); err != nil {
		return err
	}
	if persist {
		return sc.storeStateOrRestore(oldState)
	}
	return nil
}

// CommitAdvisorState atomically updates all state derived from one advisor response.
func (sc *stateCheckpoint) CommitAdvisorState(
	podEntries PodEntries,
	machineState NUMANodeMap,
	allowSharedCoresOverlapReclaimedCores bool,
	disableDedicatedCoresOverlapReclaimedCores bool,
	persist bool,
	permits ...*WritePermit,
) error {
	sc.Lock()
	defer sc.Unlock()

	if err := sc.permitWrite("CommitAdvisorState", permits...); err != nil {
		return err
	}
	oldPodEntries := sc.cache.GetPodEntries()
	oldMachineState := sc.cache.GetMachineState()
	oldAllowOverlap := sc.cache.GetAllowSharedCoresOverlapReclaimedCores()
	oldDisableDedicatedOverlap := sc.cache.GetDisableDedicatedCoresOverlapReclaimedCores()
	oldRevision := sc.cache.GetRevision()

	if err := sc.cache.CommitAdvisorState(
		podEntries,
		machineState,
		allowSharedCoresOverlapReclaimedCores,
		disableDedicatedCoresOverlapReclaimedCores,
		false,
	); err != nil {
		return err
	}
	if !persist {
		return nil
	}
	if err := sc.storeState(); err != nil {
		sc.cache.restoreAdvisorState(
			oldPodEntries, oldMachineState, oldAllowOverlap, oldDisableDedicatedOverlap, oldRevision)
		return err
	}
	return nil
}

func (sc *stateCheckpoint) CommitAdvisorStateIfRevision(
	expectedRevision uint64,
	podEntries PodEntries,
	machineState NUMANodeMap,
	allowSharedCoresOverlapReclaimedCores bool,
	disableDedicatedCoresOverlapReclaimedCores bool,
	persist bool,
	permits ...*WritePermit,
) error {
	sc.Lock()
	defer sc.Unlock()

	if err := sc.permitWrite("CommitAdvisorStateIfRevision", permits...); err != nil {
		return err
	}
	oldPodEntries := sc.cache.GetPodEntries()
	oldMachineState := sc.cache.GetMachineState()
	oldAllowOverlap := sc.cache.GetAllowSharedCoresOverlapReclaimedCores()
	oldDisableDedicatedOverlap := sc.cache.GetDisableDedicatedCoresOverlapReclaimedCores()
	oldRevision := sc.cache.GetRevision()

	if err := sc.cache.CommitAdvisorStateIfRevision(
		expectedRevision,
		podEntries,
		machineState,
		allowSharedCoresOverlapReclaimedCores,
		disableDedicatedCoresOverlapReclaimedCores,
		false,
	); err != nil {
		return err
	}
	if !persist {
		return nil
	}
	if err := sc.storeState(); err != nil {
		sc.cache.restoreAdvisorState(
			oldPodEntries, oldMachineState, oldAllowOverlap, oldDisableDedicatedOverlap, oldRevision)
		return err
	}
	return nil
}

func (sc *stateCheckpoint) GetAllowSharedCoresOverlapReclaimedCores() bool {
	sc.RLock()
	defer sc.RUnlock()

	return sc.cache.GetAllowSharedCoresOverlapReclaimedCores()
}

func (sc *stateCheckpoint) GetDisableDedicatedCoresOverlapReclaimedCores() bool {
	sc.RLock()
	defer sc.RUnlock()

	return sc.cache.GetDisableDedicatedCoresOverlapReclaimedCores()
}

func (sc *stateCheckpoint) Delete(podUID string, containerName string, persist bool) error {
	sc.Lock()
	defer sc.Unlock()

	if err := sc.permitWrite("Delete"); err != nil {
		return err
	}
	var oldState cpuPluginStateData
	if persist {
		oldState = sc.cache.snapshotData()
	}
	if err := sc.cache.Delete(podUID, containerName); err != nil {
		return err
	}
	if persist {
		return sc.storeStateOrRestore(oldState)
	}
	return nil
}

func (sc *stateCheckpoint) ClearState() error {
	sc.Lock()
	defer sc.Unlock()

	if err := sc.permitWrite("ClearState"); err != nil {
		return err
	}
	oldState := sc.cache.snapshotData()
	if err := sc.cache.ClearState(); err != nil {
		return err
	}
	return sc.storeStateOrRestore(oldState)
}

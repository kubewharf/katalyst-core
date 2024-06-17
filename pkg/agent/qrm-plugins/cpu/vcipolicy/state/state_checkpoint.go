/*
Copyright 2018 The Kubernetes Authors.

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
	"sync"

	"github.com/kubewharf/katalyst-core/pkg/util/machine"
	"k8s.io/klog/v2"
	"k8s.io/kubernetes/pkg/kubelet/checkpointmanager"
	"k8s.io/kubernetes/pkg/kubelet/checkpointmanager/errors"
)

var _ State = &stateCheckpoint{}

type stateCheckpoint struct {
	mux               sync.RWMutex
	policyName        string
	cache             State
	checkpointManager checkpointmanager.CheckpointManager
	checkpointName    string
}

// NewCheckpointState creates new State for keeping track of cpu/pod assignment with checkpoint backend
func NewCheckpointState(stateDir, checkpointName, policyName string) (State, error) {
	checkpointManager, err := checkpointmanager.NewCheckpointManager(stateDir)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize checkpoint manager: %v", err)
	}
	stateCheckpoint := &stateCheckpoint{
		cache:             NewMemoryState(),
		policyName:        policyName,
		checkpointManager: checkpointManager,
		checkpointName:    checkpointName,
	}

	if err := stateCheckpoint.restoreState(); err != nil {
		//lint:ignore ST1005 user-facing error message
		return nil, fmt.Errorf("could not restore state from checkpoint: %v, please drain this node and delete the CPU manager checkpoint file %q before restarting Kubelet",
			err, path.Join(stateDir, checkpointName))
	}

	return stateCheckpoint, nil
}

// restores state from a checkpoint and creates it if it doesn't exist
func (sc *stateCheckpoint) restoreState() error {
	sc.mux.Lock()
	defer sc.mux.Unlock()
	var err error

	checkpoint := NewCPUManagerCheckpoint()

	if err = sc.checkpointManager.GetCheckpoint(sc.checkpointName, checkpoint); err != nil {
		if err == errors.ErrCheckpointNotFound {
			return sc.storeState()
		}
		return err
	}

	if sc.policyName != checkpoint.PolicyName {
		return fmt.Errorf("configured policy %q differs from state checkpoint policy %q", sc.policyName, checkpoint.PolicyName)
	}

	var tmpDefaultCPUSet machine.CPUSet
	if tmpDefaultCPUSet, err = machine.Parse(checkpoint.DefaultCPUSet); err != nil {
		return fmt.Errorf("could not parse default cpu set %q: %v", checkpoint.DefaultCPUSet, err)
	}

	var tmpContainerCPUSet machine.CPUSet
	tmpAssignments := ContainerCPUAssignments{}
	for pod, cpuString := range checkpoint.Entries {
		if tmpContainerCPUSet, err = machine.Parse(cpuString); err != nil {
			return fmt.Errorf("could not parse cpuset %q for pod %q: %v", cpuString, pod, err)
		}
		tmpAssignments[pod] = tmpContainerCPUSet

	}

	sc.cache.SetDefaultCPUSet(tmpDefaultCPUSet)
	sc.cache.SetCPUAssignments(tmpAssignments)

	klog.V(2).Info("[cpumanager] state checkpoint: restored state from checkpoint")
	klog.V(2).Infof("[cpumanager] state checkpoint: defaultCPUSet: %s", tmpDefaultCPUSet.String())

	return nil
}

// saves state to a checkpoint, caller is responsible for locking
func (sc *stateCheckpoint) storeState() error {
	checkpoint := NewCPUManagerCheckpoint()
	checkpoint.PolicyName = sc.policyName
	checkpoint.DefaultCPUSet = sc.cache.GetDefaultCPUSet().String()

	assignments := sc.cache.GetCPUAssignments()
	for pod, cset := range assignments {
		checkpoint.Entries[pod] = cset.String()
	}

	err := sc.checkpointManager.CreateCheckpoint(sc.checkpointName, checkpoint)
	if err != nil {
		klog.Errorf("[cpumanager] could not save checkpoint: %v", err)
		return err
	}
	return nil
}

// GetCPUSet returns current CPU set
func (sc *stateCheckpoint) GetCPUSet(podUID string) (machine.CPUSet, bool) {
	sc.mux.RLock()
	defer sc.mux.RUnlock()

	res, ok := sc.cache.GetCPUSet(podUID)
	return res, ok
}

// GetDefaultCPUSet returns default CPU set
func (sc *stateCheckpoint) GetDefaultCPUSet() machine.CPUSet {
	sc.mux.RLock()
	defer sc.mux.RUnlock()

	return sc.cache.GetDefaultCPUSet()
}

// GetCPUSetOrDefault returns current CPU set, or default one if it wasn't changed
func (sc *stateCheckpoint) GetCPUSetOrDefault(podUID string) machine.CPUSet {
	sc.mux.RLock()
	defer sc.mux.RUnlock()

	return sc.cache.GetCPUSetOrDefault(podUID)
}

// GetCPUAssignments returns current CPU to pod assignments
func (sc *stateCheckpoint) GetCPUAssignments() ContainerCPUAssignments {
	sc.mux.RLock()
	defer sc.mux.RUnlock()

	return sc.cache.GetCPUAssignments()
}

// SetCPUSet sets CPU set
func (sc *stateCheckpoint) SetCPUSet(podUID string, cset machine.CPUSet) {
	sc.mux.Lock()
	defer sc.mux.Unlock()
	sc.cache.SetCPUSet(podUID, cset)
	err := sc.storeState()
	if err != nil {
		klog.Warningf("store state to checkpoint error: %v", err)
	}
}

// SetDefaultCPUSet sets default CPU set
func (sc *stateCheckpoint) SetDefaultCPUSet(cset machine.CPUSet) {
	sc.mux.Lock()
	defer sc.mux.Unlock()
	sc.cache.SetDefaultCPUSet(cset)
	err := sc.storeState()
	if err != nil {
		klog.Warningf("store state to checkpoint error: %v", err)
	}
}

// SetCPUAssignments sets CPU to pod assignments
func (sc *stateCheckpoint) SetCPUAssignments(a ContainerCPUAssignments) {
	sc.mux.Lock()
	defer sc.mux.Unlock()
	sc.cache.SetCPUAssignments(a)
	err := sc.storeState()
	if err != nil {
		klog.Warningf("store state to checkpoint error: %v", err)
	}
}

// Delete deletes assignment for specified pod
func (sc *stateCheckpoint) Delete(podUID string) {
	sc.mux.Lock()
	defer sc.mux.Unlock()
	sc.cache.Delete(podUID)
	err := sc.storeState()
	if err != nil {
		klog.Warningf("store state to checkpoint error: %v", err)
	}
}

// ClearState clears the state and saves it in a checkpoint
func (sc *stateCheckpoint) ClearState() {
	sc.mux.Lock()
	defer sc.mux.Unlock()
	sc.cache.ClearState()
	err := sc.storeState()
	if err != nil {
		klog.Warningf("store state to checkpoint error: %v", err)
	}
}

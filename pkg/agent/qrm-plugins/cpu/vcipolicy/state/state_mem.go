/*
Copyright 2017 The Kubernetes Authors.

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
	"sync"

	"github.com/kubewharf/katalyst-core/pkg/util/machine"
	"k8s.io/klog/v2"
)

type stateMemory struct {
	sync.RWMutex
	assignments   ContainerCPUAssignments
	defaultCPUSet machine.CPUSet
}

var _ State = &stateMemory{}

// NewMemoryState creates new State for keeping track of cpu/pod assignment
func NewMemoryState() State {
	klog.Infof("[cpumanager] initializing new in-memory state store")
	return &stateMemory{
		assignments:   ContainerCPUAssignments{},
		defaultCPUSet: machine.NewCPUSet(),
	}
}

func (s *stateMemory) GetCPUSet(podUID string) (machine.CPUSet, bool) {
	s.RLock()
	defer s.RUnlock()

	res, ok := s.assignments[podUID]
	return res.Clone(), ok
}

func (s *stateMemory) GetDefaultCPUSet() machine.CPUSet {
	s.RLock()
	defer s.RUnlock()

	return s.defaultCPUSet.Clone()
}

func (s *stateMemory) GetCPUSetOrDefault(podUID string) machine.CPUSet {
	if res, ok := s.GetCPUSet(podUID); ok {
		return res
	}
	return s.GetDefaultCPUSet()
}

func (s *stateMemory) GetCPUAssignments() ContainerCPUAssignments {
	s.RLock()
	defer s.RUnlock()
	return s.assignments.Clone()
}

func (s *stateMemory) SetCPUSet(podUID string, cset machine.CPUSet) {
	s.Lock()
	defer s.Unlock()

	s.assignments[podUID] = cset
	klog.Infof("[cpumanager] updated desired cpuset (pod: %s, cpuset: \"%s\")", podUID, cset)
}

func (s *stateMemory) SetDefaultCPUSet(cset machine.CPUSet) {
	s.Lock()
	defer s.Unlock()

	s.defaultCPUSet = cset
	klog.Infof("[cpumanager] updated default cpuset: \"%s\"", cset)
}

func (s *stateMemory) SetCPUAssignments(a ContainerCPUAssignments) {
	s.Lock()
	defer s.Unlock()

	s.assignments = a.Clone()
	klog.Infof("[cpumanager] updated cpuset assignments: \"%v\"", a)
}

func (s *stateMemory) Delete(podUID string) {
	s.Lock()
	defer s.Unlock()
	delete(s.assignments, podUID)

	klog.V(2).InfoS("Deleted CPUSet assignment", "podUID", podUID)
}

func (s *stateMemory) ClearState() {
	s.Lock()
	defer s.Unlock()

	s.defaultCPUSet = machine.CPUSet{}
	s.assignments = make(ContainerCPUAssignments)
	klog.V(2).Infof("[cpumanager] cleared state")
}

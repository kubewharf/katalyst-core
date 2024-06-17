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
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

// ContainerCPUAssignments type used in cpu manager state
type ContainerCPUAssignments map[string]machine.CPUSet

// Clone returns a copy of ContainerCPUAssignments
func (as ContainerCPUAssignments) Clone() ContainerCPUAssignments {
	ret := make(ContainerCPUAssignments)
	for pod, cset := range as {
		ret[pod] = cset.Clone()
	}
	return ret
}

// Reader interface used to read current cpu/pod assignment state
type Reader interface {
	GetCPUSet(podUID string) (machine.CPUSet, bool)
	GetDefaultCPUSet() machine.CPUSet
	GetCPUSetOrDefault(podUID string) machine.CPUSet
	GetCPUAssignments() ContainerCPUAssignments
}

type writer interface {
	SetCPUSet(podUID string, cpuset machine.CPUSet)
	SetDefaultCPUSet(cpuset machine.CPUSet)
	SetCPUAssignments(ContainerCPUAssignments)
	Delete(podUID string)
	ClearState()
}

// State interface provides methods for tracking and setting cpu/pod assignment
type State interface {
	Reader
	writer
}

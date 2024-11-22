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

	v1 "k8s.io/api/core/v1"

	cpustate "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	memorystate "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/memory/dynamicpolicy/state"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/util/allocation"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

type Resource struct {
	CPU    int64
	Memory int64
}

type NUMAResource map[int]*Resource

func (c NUMAResource) Clone() NUMAResource {
	n := make(NUMAResource, len(c))
	for k, v := range c {
		n[k] = v.Clone()
	}
	return n
}

func (c *Resource) Clone() *Resource {
	return &Resource{
		CPU:    c.CPU,
		Memory: c.Memory,
	}
}

func (c *Resource) String() string {
	if c == nil {
		return ""
	}
	return fmt.Sprintf("%d/%d", c.CPU, c.Memory)
}

func (c *Resource) AddResource(request *Resource) {
	if request == nil || c == nil {
		return
	}
	c.CPU += request.CPU
	c.Memory += request.Memory
}

func (c *Resource) SubAllocation(request *allocation.Allocation) {
	if request == nil || c == nil {
		return
	}
	c.CPU -= request.CPUMilli / 1000
	c.Memory -= request.Memory
}

func (c *Resource) IsSatisfied(request *allocation.Allocation) bool {
	if request == nil || c == nil {
		return false
	}
	return c.CPU >= request.CPUMilli/1000 && c.Memory >= request.Memory
}

func GetCPUMemoryReadonlyState() (cpustate.ReadonlyState, memorystate.ReadonlyState, error) {
	cpuState, err := cpustate.GetReadonlyState()
	if err != nil {
		return nil, nil, err
	}
	memoryState, err := memorystate.GetReadonlyState()
	if err != nil {
		return nil, nil, err
	}
	return cpuState, memoryState, nil
}

func GetSharedNUMAAllocatable(
	numaNodes []int,
	reservedCPUs machine.CPUSet,
	cpuState cpustate.ReadonlyState,
	memoryState memorystate.ReadonlyState,
) (NUMAResource, error) {
	cpuNUMAState := cpuState.GetMachineState()
	memoryNUMAState := memoryState.GetMachineState()[v1.ResourceMemory]

	numaAllocatable := make(NUMAResource)
	for _, numaID := range numaNodes {
		sharedCPUNUMAAllocatable := int64(cpuNUMAState[numaID].GetAvailableCPUSet(reservedCPUs).Size())
		// TODO: currently we don't consider dedicated_cores with numa_binding and without numa exclusive
		//  pod's request memory
		SharedMemoryNUMAAllocatable := int64(0)
		if !memoryNUMAState[numaID].HasDedicatedNUMABindingAndNUMAExclusivePods() {
			SharedMemoryNUMAAllocatable = int64(memoryNUMAState[numaID].Allocatable)
		}
		numaAllocatable[numaID] = &Resource{
			CPU:    sharedCPUNUMAAllocatable,
			Memory: SharedMemoryNUMAAllocatable,
		}
	}
	return numaAllocatable, nil
}

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

package helper

import (
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

// SplitCPUByBindingNUMAs scales an estimated container CPU quantity by the
// fraction of the container's assigned CPUs that live in the region's binding
// NUMAs.
//
// FIXME: metric server doesn't support to report cpu usage in numa granularity,
// so we split cpu usage evenly across the binding numas of container.
//
// The estimation is returned unchanged when the region has no binding NUMAs.
// When the container has no cross-NUMA CPU assignment at all, the scaled
// result is zero and zeroAssignment reports that condition so the caller can
// keep its own warning message.
func SplitCPUByBindingNUMAs(estimation float64, bindingNumas machine.CPUSet, ci *types.ContainerInfo) (result float64, zeroAssignment bool) {
	if bindingNumas.Size() == 0 {
		return estimation, false
	}

	cpuSize := 0
	for _, numaID := range bindingNumas.ToSliceInt() {
		cpuSize += ci.TopologyAwareAssignments[numaID].Size()
	}
	cpuAssignmentCPUs := machine.CountCPUAssignmentCPUs(ci.TopologyAwareAssignments)
	if cpuAssignmentCPUs == 0 {
		return 0, true
	}
	return estimation * float64(cpuSize) / float64(cpuAssignmentCPUs), false
}

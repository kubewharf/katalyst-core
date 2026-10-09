/*
Copyright 2026 The Katalyst Authors.

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

package dynamicpolicy

import (
	cgroupcommon "github.com/kubewharf/katalyst-core/pkg/util/cgroup/common"
	cgroupmgr "github.com/kubewharf/katalyst-core/pkg/util/cgroup/manager"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

// cgroupOps bundles the cgroup filesystem calls the advisor handlers make.
// Production code wires the real cgroupmgr functions via defaultCgroupOps; tests
// inject fakes so they no longer need mockey (which is incompatible with -race).
type cgroupOps struct {
	setSwapMaxRecursive     func(string) error
	disableSwapMaxRecursive func(string) error
	getEffectiveCPUSet      func(string) (machine.CPUSet, machine.CPUSet, error)
	isCgroup2UnifiedMode    func() bool
	applyMemory             func(string, *cgroupcommon.MemoryData) error
}

// defaultCgroupOps wires the cross-platform cgroupmgr functions. The
// isCgroup2UnifiedMode field is filled in by the platform-specific constructor
// (linux uses libcontainer; non-linux returns false).
func defaultCgroupOps() *cgroupOps {
	return &cgroupOps{
		setSwapMaxRecursive:     cgroupmgr.SetSwapMaxWithAbsolutePathRecursive,
		disableSwapMaxRecursive: cgroupmgr.DisableSwapMaxWithAbsolutePathRecursive,
		getEffectiveCPUSet:      cgroupmgr.GetEffectiveCPUSetWithAbsolutePath,
		applyMemory:             cgroupmgr.ApplyMemoryWithRelativePath,
	}
}

// newCgroupOps wires the default cgroup ops including the platform-specific
// cgroup v2 detector.
func newCgroupOps() *cgroupOps {
	ops := defaultCgroupOps()
	setDefaultIsUnified(ops)
	return ops
}

// SetCgroupOpsForTest replaces the cgroup ops. Test-only.
func (p *DynamicPolicy) SetCgroupOpsForTest(ops *cgroupOps) {
	p.cgroupOps = ops
}

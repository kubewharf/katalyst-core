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

package manager

import (
	"sync"

	"github.com/kubewharf/katalyst-core/pkg/util/cgroup/common"
	v1 "github.com/kubewharf/katalyst-core/pkg/util/cgroup/manager/v1"
	v2 "github.com/kubewharf/katalyst-core/pkg/util/cgroup/manager/v2"
)

var (
	initManagerOnce sync.Once
	manager         Manager
)

// Manager cgroup operation interface for different sub-systems.
// if v1 and v2 versioned cgroup system will perform different
// actions, it should be organized in manager; otherwise, implement in
// general util file instead.
type Manager interface {
	ApplyMemory(absCgroupPath string, data *common.MemoryData) error
	ApplyCPU(absCgroupPath string, data *common.CPUData) error
	ApplyCPUSet(absCgroupPath string, data *common.CPUSetData) error
	ApplyNetCls(absCgroupPath string, data *common.NetClsData) error
	ApplyUnifiedData(absCgroupPath, cgroupFileName, data string) error

	GetMemory(absCgroupPath string) (*common.MemoryStats, error)
	GetCPU(absCgroupPath string) (*common.CPUStats, error)
	GetCPUSet(absCgroupPath string) (*common.CPUSetStats, error)
	GetMetrics(relCgroupPath string, subsystems map[string]struct{}) (*common.CgroupMetrics, error)

	GetPids(absCgroupPath string) ([]string, error)
	GetTasks(absCgroupPath string) ([]string, error)
}

// GetManager returns a cgroup instance for both v1/v2 version
func GetManager() Manager {
	initManagerOnce.Do(func() {
		if common.CheckCgroup2UnifiedMode() {
			manager = v2.NewManager()
		} else {
			manager = v1.NewManager()
		}
	})
	return manager
}

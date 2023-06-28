//go:build !linux && !windows
// +build !linux,!windows

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

package v1

import (
	"fmt"

	"github.com/kubewharf/katalyst-core/pkg/util/cgroup/common"
)

type unsupportedManager struct{}

// NewManager return a manager for cgroupv1
func NewManager() *unsupportedManager {
	return &unsupportedManager{}
}

func (m *unsupportedManager) ApplyMemory(_ string, _ *common.MemoryData) error {
	return fmt.Errorf("unsupported manager v1")
}

func (m *unsupportedManager) ApplyCPU(_ string, _ *common.CPUData) error {
	return fmt.Errorf("unsupported manager v1")
}

func (m *unsupportedManager) ApplyCPUSet(_ string, _ *common.CPUSetData) error {
	return fmt.Errorf("unsupported manager v1")
}

func (m *unsupportedManager) ApplyNetCls(_ string, _ *common.NetClsData) error {
	return fmt.Errorf("unsupported manager v1")
}

func (m *unsupportedManager) ApplyUnifiedData(absCgroupPath, cgroupFileName, data string) error {
	return fmt.Errorf("unsupported manager v1")
}

func (m *unsupportedManager) GetMemory(_ string) (*common.MemoryStats, error) {
	return nil, fmt.Errorf("unsupported manager v1")
}

func (m *unsupportedManager) GetCPU(_ string) (*common.CPUStats, error) {
	return nil, fmt.Errorf("unsupported manager v1")
}

func (m *unsupportedManager) GetCPUSet(_ string) (*common.CPUSetStats, error) {
	return nil, fmt.Errorf("unsupported manager v2")
}

func (m *unsupportedManager) GetMetrics(_ string, _ map[string]struct{}) (*common.CgroupMetrics, error) {
	return nil, fmt.Errorf("unsupported manager v1")
}

func (m *unsupportedManager) GetPids(_ string) ([]string, error) {
	return nil, fmt.Errorf("unsupported manager v1")
}

func (m *unsupportedManager) GetTasks(_ string) ([]string, error) {
	return nil, fmt.Errorf("unsupported manager v1")
}

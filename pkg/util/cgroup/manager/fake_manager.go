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

import "github.com/kubewharf/katalyst-core/pkg/util/cgroup/common"

type FakeCgroupManager struct{}

func (f *FakeCgroupManager) ApplyMemory(absCgroupPath string, data *common.MemoryData) error {
	return nil
}

func (f *FakeCgroupManager) ApplyCPU(absCgroupPath string, data *common.CPUData) error {
	return nil
}

func (f *FakeCgroupManager) ApplyCPUSet(absCgroupPath string, data *common.CPUSetData) error {
	return nil
}

func (f *FakeCgroupManager) ApplyNetCls(absCgroupPath string, data *common.NetClsData) error {
	return nil
}

func (f *FakeCgroupManager) ApplyIOCostQoS(absCgroupPath string, devID string, data *common.IOCostQoSData) error {
	return nil
}

func (f *FakeCgroupManager) ApplyIOCostModel(absCgroupPath string, devID string, data *common.IOCostModelData) error {
	return nil
}

func (f *FakeCgroupManager) ApplyIOWeight(absCgroupPath string, devID string, weight uint64) error {
	return nil
}

func (f *FakeCgroupManager) ApplyUnifiedData(absCgroupPath, cgroupFileName, data string) error {
	return nil
}

func (f *FakeCgroupManager) GetMemory(absCgroupPath string) (*common.MemoryStats, error) {
	return nil, nil
}

func (f *FakeCgroupManager) GetNumaMemory(absCgroupPath string) (map[int]*common.MemoryNumaMetrics, error) {
	return nil, nil
}

func (f *FakeCgroupManager) GetCPU(absCgroupPath string) (*common.CPUStats, error) {
	return nil, nil
}

func (f *FakeCgroupManager) GetCPUSet(absCgroupPath string) (*common.CPUSetStats, error) {
	return nil, nil
}

func (f *FakeCgroupManager) GetIOCostQoS(absCgroupPath string) (map[string]*common.IOCostQoSData, error) {
	return nil, nil
}

func (f *FakeCgroupManager) GetIOCostModel(absCgroupPath string) (map[string]*common.IOCostModelData, error) {
	return nil, nil
}

func (f *FakeCgroupManager) GetDeviceIOWeight(absCgroupPath string, devID string) (uint64, bool, error) {
	return 0, false, nil
}

func (f *FakeCgroupManager) GetIOStat(absCgroupPath string) (map[string]map[string]string, error) {
	return nil, nil
}

func (f *FakeCgroupManager) GetMetrics(relCgroupPath string, subsystems map[string]struct{}) (*common.CgroupMetrics, error) {
	return nil, nil
}

func (f *FakeCgroupManager) GetPids(absCgroupPath string) ([]string, error) {
	return nil, nil
}

func (f *FakeCgroupManager) GetTasks(absCgroupPath string) ([]string, error) {
	return nil, nil
}

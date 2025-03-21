//go:build linux
// +build linux

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

package machine

import (
	"fmt"

	"github.com/google/cadvisor/fs"
	info "github.com/google/cadvisor/info/v1"
	"github.com/google/cadvisor/machine"
	"github.com/google/cadvisor/utils/sysfs"

	"github.com/kubewharf/katalyst-core/pkg/config/agent/global"
)

// GetKatalystMachineInfo returns KatalystMachineInfo by collecting machine info
// actually, this function should be only called in initial processes
func GetKatalystMachineInfo(conf *global.MachineInfoConfiguration) (*KatalystMachineInfo, error) {
	machineInfo, err := getMachineInfo()
	if err != nil {
		return nil, err
	}

	cpuTopology, memoryTopology, err := Discover(machineInfo)
	if err != nil {
		return nil, err
	}

	extraCPUInfo, err := GetExtraCPUInfo()
	if err != nil {
		return nil, err
	}

	extraNetworkInfo, err := GetExtraNetworkInfo(conf)
	if err != nil {
		return nil, err
	}

	extraTopologyInfo, err := GetExtraTopologyInfo(conf, cpuTopology, extraNetworkInfo)
	if err != nil {
		return nil, err
	}

	return &KatalystMachineInfo{
		MachineInfo:       machineInfo,
		CPUTopology:       cpuTopology,
		MemoryTopology:    memoryTopology,
		ExtraCPUInfo:      extraCPUInfo,
		ExtraNetworkInfo:  extraNetworkInfo,
		ExtraTopologyInfo: extraTopologyInfo,
	}, nil
}

// getMachineInfo is used to construct info.MachineInfo in cadvisor
func getMachineInfo() (*info.MachineInfo, error) {
	fsInfo, err := fs.NewFsInfo(fs.Context{})
	if err != nil {
		return nil, fmt.Errorf("NewFsInfo failed with error: %v", err)
	}
	return machine.Info(sysfs.NewRealSysFs(), fsInfo, true)
}

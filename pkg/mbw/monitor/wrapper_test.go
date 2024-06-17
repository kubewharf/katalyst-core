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

package monitor

import (
	"errors"
	"sync"
	"time"

	v1 "github.com/google/cadvisor/info/v1"
	"k8s.io/apimachinery/pkg/util/sets"

	"github.com/kubewharf/katalyst-core/pkg/config/agent/global"
	utilstest "github.com/kubewharf/katalyst-core/pkg/mbw/test"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

// this test file sets up mocker of wrapper in TEST runs only

var onceTest sync.Once

func init() {
	onceTest.Do(func() {
		// need mockers for mbw utils module
		utilstest.SetupTestSyscaller()
		utilstest.SetupTestOS()
		utilstest.SetupTestFiler()

		machineWrapper = &machineMock{}
		msrSingleton = &msrMocker{}
	})
}

type msrMocker struct{}

/*************************************************************
 * mock test setup case:
 *   read WrapperMSR:  error on cpu 99999; 210 returned otherwise
 *   write WrapperMSR: error on cpu 77777; ok otherwise
 ************************************************************/
func (m msrMocker) WriteMSR(cpu uint32, msr int64, value uint64) error {
	if cpu == 77777 {
		return errors.New("mock 77777 write error")
	}
	return nil
}

func (m msrMocker) ReadMSR(cpu uint32, msr int64) (uint64, error) {
	if cpu == 99999 {
		return 0, errors.New("mock 99999 check error")
	}
	return 210, nil
}

type machineMock struct {
	WrapperMachine
}

func (m machineMock) GetKatalystMachineInfo(conf *global.MachineInfoConfiguration) (*machine.KatalystMachineInfo, error) {
	return &machine.KatalystMachineInfo{
		MachineInfo: &v1.MachineInfo{
			Timestamp:        time.Time{},
			CPUVendorID:      "GenuineIntel",
			NumCores:         2,
			NumPhysicalCores: 1,
			NumSockets:       1,
			CpuFrequency:     2399998,
			MemoryCapacity:   2048,
			MemoryByType:     nil,
			NVMInfo:          v1.NVMInfo{},
			HugePages:        nil,
			MachineID:        "t001",
			SystemUUID:       "0753d4e1f1be4f28948de17b0d937001",
			BootID:           "",
			Filesystems:      nil,
			DiskMap:          nil,
			NetworkDevices:   nil,
			Topology:         nil,
			CloudProvider:    "Unknown",
			InstanceType:     "Unknown",
			InstanceID:       "None",
		},
		CPUTopology: &machine.CPUTopology{
			NumCPUs:      1,
			NumCores:     2,
			NumSockets:   1,
			NumNUMANodes: 1,
			CPUDetails: map[int]machine.CPUInfo{
				0: {
					NUMANodeID: 0,
					SocketID:   0,
					CoreID:     0,
				},
				1: {
					NUMANodeID: 0,
					SocketID:   0,
					CoreID:     0,
				},
			},
		},
		MemoryTopology: &machine.MemoryTopology{
			PMU: machine.PMUInfo{},
		},
		DieTopology: &machine.DieTopology{
			NumPackages: 1,
		},
		ExtraCPUInfo: &machine.ExtraCPUInfo{
			Vendor: "mocked",
		},
		ExtraNetworkInfo: nil,
		ExtraTopologyInfo: &machine.ExtraTopologyInfo{
			NumaDistanceMap: map[int][]machine.NumaDistanceInfo{
				0: {{
					NumaID:   0,
					Distance: 10,
				}},
			},
			SiblingNumaInfo: &machine.SiblingNumaInfo{
				SiblingNumaMap:                  map[int]sets.Int{0: sets.NewInt()},
				SiblingNumaAvgMBWAllocatableMap: nil,
				SiblingNumaAvgMBWCapacityMap:    nil,
			},
		},
	}, nil
}

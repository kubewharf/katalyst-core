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
	"math"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

func TestVFState(t *testing.T) {
	t.Parallel()

	var (
		eth0_0 = VFInfo{
			RepName:  "eth0_0",
			Index:    0,
			NumaNode: 0,
			PCIAddr:  "0000:40:00.1",
			ExtraVFInfo: &ExtraVFInfo{
				QueueCount: 8,
				IBDevices:  []string{"umad0", "uverbs0"},
			},
		}
		eth0_1 = VFInfo{
			RepName:  "eth0_1",
			Index:    1,
			NumaNode: 0,
			PCIAddr:  "0000:40:00.2",
			ExtraVFInfo: &ExtraVFInfo{
				QueueCount: 8,
				IBDevices:  []string{"umad1", "uverbs1"},
			},
		}
		eth1_0 = VFInfo{
			RepName:  "eth1_0",
			Index:    0,
			NumaNode: 1,
			PCIAddr:  "0000:41:00.1",
			ExtraVFInfo: &ExtraVFInfo{
				QueueCount: 24,
			},
		}
		eth1_1 = VFInfo{
			RepName:  "eth1_1",
			Index:    1,
			NumaNode: 1,
			PCIAddr:  "0000:41:00.2",
			ExtraVFInfo: &ExtraVFInfo{
				QueueCount: 24,
			},
		}
		eth2_0 = VFInfo{
			RepName:  "eth2_0",
			Index:    0,
			NumaNode: 2,
			PCIAddr:  "0000:c1:00.1",
			ExtraVFInfo: &ExtraVFInfo{
				QueueCount: 32,
				IBDevices:  []string{"umad20", "uverbs20"},
			},
		}
		eth2_1 = VFInfo{
			RepName:  "eth2_1",
			Index:    1,
			NumaNode: 2,
			PCIAddr:  "0000:c1:00.2",
			ExtraVFInfo: &ExtraVFInfo{
				QueueCount: 32,
				IBDevices:  []string{"umad21", "uverbs21"},
			},
		}

		vfState = VFState{eth0_0, eth0_1, eth1_0, eth1_1, eth2_0, eth2_1}
	)

	Convey("sort", t, func() {
		vfStateToSort := vfState.Clone()

		vfStateToSort.Sort()

		So(vfStateToSort, ShouldResemble, VFState{eth0_0, eth1_0, eth2_0, eth0_1, eth1_1, eth2_1})
	})

	Convey("FilterByPodAllocated", t, func() {
		podEntries := PodEntries{
			"pod1": {
				"container1": &AllocationInfo{
					VFInfo: eth0_0,
				},
			},
			"pod2": {
				"container2": &AllocationInfo{
					VFInfo: eth1_0,
				},
			},
		}

		Convey("allocated=true", func() {
			filtered := vfState.Filter(FilterByPodAllocated(podEntries, true))
			So(filtered, ShouldResemble, VFState{eth0_0, eth1_0})
		})

		Convey("allocated=false", func() {
			filtered := vfState.Filter(FilterByPodAllocated(podEntries, false))
			So(filtered, ShouldResemble, VFState{eth0_1, eth1_1, eth2_0, eth2_1})
		})
	})

	Convey("FilterByCount", t, func() {
		filtered := vfState.Filter(FilterByQueueCount(24, math.MaxUint32))
		So(filtered, ShouldResemble, VFState{eth1_0, eth1_1, eth2_0, eth2_1})
	})

	Convey("FilterByPCIAddr", t, func() {
		filtered := vfState.Filter(FilterByPCIAddr("0000:41:00.2"))
		So(filtered, ShouldResemble, VFState{eth1_1})
	})

	Convey("FilterByNumaSet", t, func() {
		filtered := vfState.Filter(FilterByNumaSet(machine.NewCPUSet(2)))
		So(filtered, ShouldResemble, VFState{eth2_0, eth2_1})
	})

	Convey("FilterByRDMA", t, func() {
		Convey("supported=true", func() {
			filtered := vfState.Filter(FilterByRDMA(true))
			So(filtered, ShouldResemble, VFState{eth0_0, eth0_1, eth2_0, eth2_1})
		})

		Convey("supported=false", func() {
			filtered := vfState.Filter(FilterByRDMA(false))
			So(filtered, ShouldResemble, VFState{eth1_0, eth1_1})
		})
	})
}

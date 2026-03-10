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

package controller

import (
	"errors"
	"fmt"
	"testing"

	. "github.com/bytedance/mockey"
	. "github.com/smartystreets/goconvey/convey"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/irqtuner/config"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

func Test_controller_linux(t *testing.T) {
	t.Parallel()
	PatchConvey("Test_clearNicXPS", t, func() {
		ic := &IrqTuningController{}
		nic := &NicInfo{
			NicBasicInfo: &machine.NicBasicInfo{
				InterfaceInfo: machine.InterfaceInfo{
					NetNSInfo: machine.NetNSInfo{
						NSName: "test-ns",
					},
					Name: "eth0",
				},
				Irq2Queue: map[int]int{
					100: 0,
				},
				Queue2Irq: map[int]int{
					0: 100,
				},
				QueueNum: 1,
			},
			Irq2Core: map[int]int64{
				100: 2,
			},
			SocketIrqCores: map[int][]int64{
				0: {2},
			},
		}

		PatchConvey("Scenario 1: clear NIC XPS successfully", func() {
			Mock(machine.GetNicTxQueuesXpsConf).Return(map[int]string{
				0: "ffff",
			}, nil).Build()

			Mock(machine.ClearNicTxQueueXPS).Return(nil).Build()

			err := ic.clearNicXPS(nic)

			So(err, ShouldBeNil)
		})

		PatchConvey("Scenario 2: IsZeroBitmap", func() {
			Mock(machine.GetNicTxQueuesXpsConf).Return(map[int]string{
				0: "ffff",
			}, nil).Build()

			Mock(machine.IsZeroBitmap).Return(true).Build()

			err := ic.clearNicXPS(nic)

			So(err, ShouldBeNil)
		})

		PatchConvey("Scenario 3: GetNicTxQueuesXpsConf failed", func() {
			nic.QueueNum = 0
			expectedErr := fmt.Errorf("invalid queue number %d", nic.QueueNum)

			err := ic.clearNicXPS(nic)

			So(err, ShouldNotBeNil)
			So(err, ShouldResemble, expectedErr)
		})

		PatchConvey("Scenario 4: ClearNicTxQueueXPS failed", func() {
			nic.QueueNum = 1
			expectedErr := errors.New("failed to clear tx queue xps")

			Mock(machine.GetNicTxQueuesXpsConf).Return(map[int]string{
				0: "ffff",
			}, nil).Build()

			Mock(machine.ClearNicTxQueueXPS).Return(expectedErr).Build()

			err := ic.clearNicXPS(nic)

			So(err, ShouldBeNil)
		})
	})

	PatchConvey("Test_classifyNicsByThroughputFirstTime", t, func() {
		ic := &IrqTuningController{
			conf: &config.IrqTuningConfig{
				NormalThroughputNics: []config.NicInfo{
					{
						NicName: "eth0",
					},
					{
						NicName:   "eth2",
						NetNSName: "ns2",
					},
				},
				ThroughputClassSwitchConf: config.ThroughputClassSwitchConfig{
					LowThroughputThresholds: config.LowThroughputThresholds{
						RxPPSThreshold:  3000,
						SuccessiveCount: 30,
					},
					NormalThroughputThresholds: config.NormalThroughputThresholds{
						RxPPSThreshold:  6000,
						SuccessiveCount: 10,
					},
				},
			},
		}

		PatchConvey("Scenario 1: succeed to classify nics by static configured normal throughput nics", func() {
			nics := []*machine.NicBasicInfo{
				{
					InterfaceInfo: machine.InterfaceInfo{
						Name: "eth0",
					},
				},
				{
					InterfaceInfo: machine.InterfaceInfo{
						Name: "eth5",
					},
				},
			}

			Mock((*IrqTuningController).emitErrMetric).To(func(reason string, level int64, tags ...metrics.MetricTag) {
			}).Build()

			normalThroughtputNics, lowThroughputBasicNics := ic.classifyNicsByThroughputFirstTime(nics, 1)

			So(normalThroughtputNics, ShouldResemble, []*machine.NicBasicInfo{
				{
					InterfaceInfo: machine.InterfaceInfo{
						Name: "eth0",
					},
				},
			})
			So(lowThroughputBasicNics, ShouldResemble, []*machine.NicBasicInfo{
				{
					InterfaceInfo: machine.InterfaceInfo{
						Name: "eth5",
					},
				},
			})
		})

		PatchConvey("Scenario 2: no nic classified to normal throughput nic according to static configured normal throughput nics", func() {
			nics := []*machine.NicBasicInfo{
				{
					InterfaceInfo: machine.InterfaceInfo{
						Name: "eth1",
					},
				},
				{
					InterfaceInfo: machine.InterfaceInfo{
						Name: "eth5",
					},
				},
			}

			Mock((*IrqTuningController).emitErrMetric).To(func(reason string, level int64, tags ...metrics.MetricTag) {
			}).Build()

			normalThroughtputNics, lowThroughputBasicNics := ic.classifyNicsByThroughputFirstTime(nics, 1)

			So(normalThroughtputNics, ShouldResemble, []*machine.NicBasicInfo{
				{
					InterfaceInfo: machine.InterfaceInfo{
						Name: "eth1",
					},
				},
			})
			So(lowThroughputBasicNics, ShouldResemble, []*machine.NicBasicInfo{
				{
					InterfaceInfo: machine.InterfaceInfo{
						Name: "eth5",
					},
				},
			})
		})

		PatchConvey("Scenario 3: succeed to classify nics by dynamic check nic rx pps", func() {
			ic.conf.NormalThroughputNics = []config.NicInfo{}

			nics := []*machine.NicBasicInfo{
				{
					InterfaceInfo: machine.InterfaceInfo{
						Name:    "eth0",
						IfIndex: 2,
					},
				},
				{
					InterfaceInfo: machine.InterfaceInfo{
						Name:    "eth2",
						IfIndex: 11,
					},
				},
			}

			First := true

			Mock(machine.GetNetDevRxPackets).To(func(nic *machine.NicBasicInfo) (uint64, error) {
				if First {
					if nic.Name == "eth0" {
						return 10000, nil
					}

					if nic.Name == "eth2" {
						return 10000, nil
					}
					First = false
				} else {
					if nic.Name == "eth0" {
						return 1000000, nil
					}

					if nic.Name == "eth2" {
						return 11000, nil
					}
				}
				return 0, nil
			}).Build()

			normalThroughtputNics, lowThroughputBasicNics := ic.classifyNicsByThroughputFirstTime(nics, 2)

			So(normalThroughtputNics, ShouldResemble, []*machine.NicBasicInfo{
				{
					InterfaceInfo: machine.InterfaceInfo{
						Name:    "eth0",
						IfIndex: 2,
					},
				},
			})
			So(lowThroughputBasicNics, ShouldResemble, []*machine.NicBasicInfo{
				{
					InterfaceInfo: machine.InterfaceInfo{
						Name:    "eth2",
						IfIndex: 11,
					},
				},
			})
		})
	})

	PatchConvey("Test_periodicTuning", t, func() {
		ic := &IrqTuningController{
			conf: &config.IrqTuningConfig{
				NormalThroughputNics: []config.NicInfo{
					{
						NicName: "eth0",
					},
					{
						NicName:   "eth2",
						NetNSName: "ns2",
					},
				},
				ThroughputClassSwitchConf: config.ThroughputClassSwitchConfig{
					LowThroughputThresholds: config.LowThroughputThresholds{
						RxPPSThreshold:  3000,
						SuccessiveCount: 30,
					},
					NormalThroughputThresholds: config.NormalThroughputThresholds{
						RxPPSThreshold:  6000,
						SuccessiveCount: 10,
					},
				},
			},
		}

		PatchConvey("Scenario 1: failed to syncNics", func() {
			Mock((*IrqTuningController).syncNics).Return(nil, false, fmt.Errorf("sync nic failed")).Build()
			Mock((*IrqTuningController).emitErrMetric).To(func(reason string, level int64, tags ...metrics.MetricTag) {
			}).Build()

			ic.periodicTuning(nil)

			So(ic.Nics, ShouldResemble, []*NicIrqTuningManager(nil))
			So(ic.LowThroughputNics, ShouldResemble, []*NicIrqTuningManager(nil))
		})

		PatchConvey("Scenario 1: static normal throughput nics changed", func() {
			ic.Nics = append(ic.Nics, &NicIrqTuningManager{
				NicInfo: &NicInfo{
					NicBasicInfo: &machine.NicBasicInfo{
						InterfaceInfo: machine.InterfaceInfo{
							Name: "eth0",
						},
					},
				},
			})
			oldConf := &config.IrqTuningConfig{}

			Mock((*IrqTuningController).syncNics).Return(nil, false, nil).Build()
			Mock((*IrqTuningController).updateNicIrqTuningManagers).Return(fmt.Errorf("updateNicIrqTuningManagers failed")).Build()
			Mock((*IrqTuningController).emitErrMetric).To(func(reason string, level int64, tags ...metrics.MetricTag) {
			}).Build()

			ic.periodicTuning(oldConf)

			So(ic.Nics, ShouldResemble, []*NicIrqTuningManager{{
				NicInfo: &NicInfo{
					NicBasicInfo: &machine.NicBasicInfo{
						InterfaceInfo: machine.InterfaceInfo{
							Name: "eth0",
						},
					},
				},
			}})
			So(ic.LowThroughputNics, ShouldResemble, []*NicIrqTuningManager(nil))
		})
	})
}

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

	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

func Test_clearNicXPS(t *testing.T) {
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
}

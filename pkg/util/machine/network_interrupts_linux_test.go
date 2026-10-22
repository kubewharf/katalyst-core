//go:build linux
// +build linux

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

package machine

import (
	"errors"
	"os"
	"testing"

	. "github.com/bytedance/mockey"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetNicInterruptsInfo(t *testing.T) {
	PatchConvey("parse IRQ numbers and queue strings in file order", t, func() {
		readFile := Mock(os.ReadFile).To(func(name string) ([]byte, error) {
			assert.Equal(t, InterruptsFile, name)
			return []byte(`
			           CPU0       CPU1
			 29:         10         20   IR-PCI-MSI-edge   eth0-rx-0
			 28:         30         40   IR-PCI-MSI-edge   eth0-rx-0
			NMI:          0          0   Non-maskable interrupts
			bad:          0          0   IR-PCI-MSI-edge   eth0-rx-1
			 -1:          0          0   IR-PCI-MSI-edge   eth0-rx-2
			 30
			 31:
			 32           0          0   IR-PCI-MSI-edge   eth0-rx-3
			100:          0          0   IR-PCI-MSI-edge   gve-ntfy-blk0@pci:0000:01:00.0
			200:          0          0   IR-PCI-MSI-edge   virtio0-input.0
			`), nil
		}).Build()

		info, err := GetNicInterruptsInfo()

		require.NoError(t, err)
		assert.Equal(t, []NicInterruptInfo{
			{Irq: 29, QueueStr: "eth0-rx-0"},
			{Irq: 28, QueueStr: "eth0-rx-0"},
			{Irq: 100, QueueStr: "gve-ntfy-blk0@pci:0000:01:00.0"},
			{Irq: 200, QueueStr: "virtio0-input.0"},
		}, info)

		// Duplicate queue matches must continue to use the first IRQ in file order.
		queues, err := GetNicQueue2IrqWithQueueFilter(&NicBasicInfo{
			InterfaceInfo: InterfaceInfo{Name: "eth0"},
			QueueNum:      1,
		}, []string{"eth0"}, "-", info)
		require.NoError(t, err)
		assert.Equal(t, map[int]int{0: 29}, queues)
		assert.Equal(t, 1, readFile.Times())
	})

	PatchConvey("propagate interrupts file read errors", t, func() {
		readErr := errors.New("read interrupts failed")
		Mock(os.ReadFile).Return(nil, readErr).Build()

		info, err := GetNicInterruptsInfo()

		assert.Nil(t, info)
		assert.ErrorIs(t, err, readErr)
		assert.Contains(t, err.Error(), InterruptsFile)
	})

	PatchConvey("empty interrupts file", t, func() {
		Mock(os.ReadFile).Return([]byte{}, nil).Build()

		info, err := GetNicInterruptsInfo()

		assert.NoError(t, err)
		assert.Empty(t, info)
	})
}

func TestListActiveUplinkNicsSharesInterruptsInfo(t *testing.T) {
	PatchConvey("one interrupts read for multiple namespaces and NIC drivers", t, func() {
		hostNICs := []*NicBasicInfo{{
			InterfaceInfo: InterfaceInfo{Name: "eth0", IfIndex: 1},
			QueueNum:      2,
			Irqs:          []int{28, 29},
		}}
		otherNICs := []*NicBasicInfo{
			{
				InterfaceInfo: InterfaceInfo{NetNSInfo: NetNSInfo{NSName: "ns1"}, Name: "eth0", IfIndex: 2},
				QueueNum:      1,
				Irqs:          []int{30},
			},
			{
				InterfaceInfo:  InterfaceInfo{Name: "virtio-nic", IfIndex: 3},
				QueueNum:       1,
				IsVirtioNetDev: true,
				VirtioNetName:  "virtio0",
			},
			{
				InterfaceInfo: InterfaceInfo{Name: "gve-nic", IfIndex: 4},
				Driver:        NicDriverGVE,
				QueueNum:      1,
				Irqs:          []int{100, 101, 102, 103, 104},
			},
			{
				InterfaceInfo: InterfaceInfo{Name: "renamed", IfIndex: 5, PCIAddr: "0000:81:00.0"},
				QueueNum:      2,
			},
			{
				InterfaceInfo: InterfaceInfo{Name: "eth2_1", IfIndex: 6, PCIAddr: "0000:82:00.0"},
				QueueNum:      1,
				Irqs:          []int{400},
			},
			{
				InterfaceInfo: InterfaceInfo{Name: "bytenic0", IfIndex: 7},
				Driver:        "bytenic",
				QueueNum:      1,
			},
		}
		Mock(ListNetNS).Return([]NetNSInfo{{}, {NSName: "ns1"}}, nil).Build()
		Mock(ListActiveUplinkNicsFromNetNS).To(func(ns NetNSInfo) ([]*NicBasicInfo, error) {
			if ns.NSName == "" {
				return hostNICs, nil
			}
			return otherNICs, nil
		}).Build()
		readFile := Mock(os.ReadFile).To(func(name string) ([]byte, error) {
			assert.Equal(t, InterruptsFile, name)
			return []byte(`
			           CPU0       CPU1
			 28:         10         20   IR-PCI-MSI-edge   eth0-rx-0
			 29:         30         40   IR-PCI-MSI-edge   eth0-rx-1
			 30:         50         60   IR-PCI-MSI-edge   eth0-rx-0
			100:          0          0   IR-PCI-MSI-edge   gve-ntfy-blk0@pci:0000:01:00.0
			101:          0          0   IR-PCI-MSI-edge   gve-ntfy-blk1@pci:0000:01:00.0
			102:          0          0   IR-PCI-MSI-edge   gve-ntfy-blk2@pci:0000:01:00.0
			103:          0          0   IR-PCI-MSI-edge   gve-ntfy-blk3@pci:0000:01:00.0
			104:          0          0   IR-PCI-MSI-edge   gve-mgmnt@pci:0000:01:00.0
			200:          0          0   IR-PCI-MSI-edge   virtio0-input.0
			201:          0          0   IR-PCI-MSI-edge   virtio0-output.0
			300:          0          0   IR-PCI-MSI-edge   mlx5_comp0@pci:0000:81:00.0
			301:          0          0   IR-PCI-MSI-edge   mlx5_comp1@pci:0000:81:00.0
			400:          0          0   IR-PCI-MSI-edge   eth2-rx-0
			`), nil
		}).Build()

		expectedRx := []map[int]int{
			{0: 28, 1: 29}, {0: 30}, {0: 200}, {0: 102},
			{0: 300, 1: 301}, {0: 400},
		}
		expectedTx := []map[int]int{
			{}, {}, {0: 201}, {0: 100}, {}, {},
		}
		// Each scan reads a fresh snapshot, while all NICs in that scan share it.
		for scan := 1; scan <= 2; scan++ {
			nics, err := ListActiveUplinkNics(DefaultNetNSDir, []string{ContainerNetNSPrefix})
			require.NoError(t, err)
			require.Len(t, nics, len(expectedRx))
			assert.Equal(t, scan, readFile.Times())
			for i, nic := range nics {
				assert.NotEqual(t, "bytenic0", nic.Name)
				assert.Equal(t, expectedRx[i], nic.Queue2Irq)
				assert.Equal(t, expectedTx[i], nic.TxQueue2Irq)
				assert.Len(t, nic.Irq2Queue, len(expectedRx[i]))
				assert.Len(t, nic.TxIrq2Queue, len(expectedTx[i]))
				for queue, irq := range expectedRx[i] {
					assert.Equal(t, queue, nic.Irq2Queue[irq])
				}
				for queue, irq := range expectedTx[i] {
					assert.Equal(t, queue, nic.TxIrq2Queue[irq])
				}
			}
		}
	})
}

func TestListActiveUplinkNicsInterruptsReadFailures(t *testing.T) {
	PatchConvey("read interrupts once before discovery in a later namespace fails", t, func() {
		Mock(ListNetNS).Return([]NetNSInfo{{}, {NSName: "ns1"}}, nil).Build()
		Mock(ListActiveUplinkNicsFromNetNS).To(func(ns NetNSInfo) ([]*NicBasicInfo, error) {
			if ns.NSName == "ns1" {
				return nil, errors.New("netns discovery failed")
			}
			return []*NicBasicInfo{{InterfaceInfo: InterfaceInfo{Name: "eth0"}}}, nil
		}).Build()
		Mock(NetNSExist).Return(true).Build()
		readFile := Mock(os.ReadFile).Return([]byte{}, nil).Build()

		nics, err := ListActiveUplinkNics(DefaultNetNSDir, nil)

		assert.Nil(t, nics)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "netns discovery failed")
		assert.Equal(t, 1, readFile.Times())
	})

	PatchConvey("read interrupts once when no NICs are found", t, func() {
		Mock(ListNetNS).Return([]NetNSInfo{{}, {NSName: ContainerNetNSPrefix + "test"}}, nil).Build()
		discovery := Mock(ListActiveUplinkNicsFromNetNS).Return([]*NicBasicInfo{}, nil).Build()
		readFile := Mock(os.ReadFile).Return([]byte{}, nil).Build()

		nics, err := ListActiveUplinkNics(DefaultNetNSDir, []string{ContainerNetNSPrefix})

		assert.NoError(t, err)
		assert.Empty(t, nics)
		assert.Equal(t, 1, discovery.Times())
		assert.Equal(t, 1, readFile.Times())
	})

	PatchConvey("return interrupts read errors before discovering NICs", t, func() {
		Mock(ListNetNS).Return([]NetNSInfo{{}}, nil).Build()
		discovery := Mock(ListActiveUplinkNicsFromNetNS).Return([]*NicBasicInfo{{InterfaceInfo: InterfaceInfo{Name: "eth0"}}}, nil).Build()
		readErr := errors.New("read interrupts failed")
		readFile := Mock(os.ReadFile).Return(nil, readErr).Build()
		lookup := Mock(GetNicQueue2Irq).Return(nil, nil, nil).Build()

		nics, err := ListActiveUplinkNics(DefaultNetNSDir, nil)

		assert.Nil(t, nics)
		assert.ErrorIs(t, err, readErr)
		assert.Contains(t, err.Error(), "failed to GetNicInterruptsInfo")
		assert.Equal(t, 1, readFile.Times())
		assert.Zero(t, discovery.Times())
		assert.Zero(t, lookup.Times())
	})
}

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

package amd

import (
	"fmt"
	"k8s.io/klog/v2"
	"time"

	utils "github.com/kubewharf/katalyst-core/pkg/util/lowlevel"
)

const (
	DRV_IS_PCI_VENDOR_ID_AMD = 0x1022
	AMD_ROME_IOHC            = 0x1480
	AMD_GENOA_IOHC           = 0x14a4

	SMN_BASE_ADDRESS    = 0x3B00000
	SMN_MSG_ID_OFFSET   = 0x10534
	SMN_RESPONSE_OFFSET = 0x10980
	SMN_ARG_OFFSET      = 0x109E0

	SMN_MAX_RETRY      = 30
	SMN_RETRY_INTERVAL = 5
	SMN_READ_INTERVAL  = 5

	AMD_SOCKET_POWER_READ           = 0x04
	AMD_SOCKET_POWER_LIMIT_WRITE    = 0x05
	AMD_SOCKET_POWER_LIMIT_READ     = 0x06
	AMD_SOCKET_MAX_POWER_LIMIT_READ = 0x07
)

func (o Operation) InitIOHCs(printIOHC bool) error {
	// get all IOHC devs on PCI
	devs := utils.ScanDevices(uint16(DRV_IS_PCI_VENDOR_ID_AMD), o.MachineInfo.PciDevID)

	if len(devs) == 0 {
		return fmt.Errorf("failed to load pci devs - confirm if the libpci-dev is installed")
	}

	for i := range o.IOHCs {
		// any PCI IOHC dev can read all UMCs on a socket,
		// so only need to find out the first IOHC on each socket
		o.IOHCs[i] = utils.GetFirstIOHC(i*o.MachineInfo.NumaPerSocket, devs)
		if o.IOHCs[i] == nil {
			return fmt.Errorf("failed to find iohc dev for socket %d", i)
		}

		if printIOHC {
			fmt.Printf("socket %d got iohc dev %s on its first node (numa node %d)", i, o.IOHCs[i].BDFString(), i*o.MachineInfo.NumaPerSocket)
		}
	}

	return nil
}

func (o Operation) GetSocketPower(skt int) uint32 {
	if len(o.IOHCs) == 0 || o.IOHCs[skt] == nil {
		fmt.Printf("please init IOHCs first")
		return 0
	}

	power, err := Mailbox(o.IOHCs[skt], AMD_SOCKET_POWER_READ, 0)
	if err != nil {
		fmt.Printf("failed to call mailbox for reading current power on socket %d - %v\n", skt, err)
		return 0
	}

	return power
}

func (o Operation) GetSocketPowerLimit(skt int) uint32 {
	if len(o.IOHCs) == 0 || o.IOHCs[skt] == nil {
		fmt.Printf("please init IOHCs first")
		return 0
	}

	power, err := Mailbox(o.IOHCs[skt], AMD_SOCKET_POWER_LIMIT_READ, 0)
	if err != nil {
		fmt.Printf("failed to call mailbox for reading current power limit on socket %d - %v\n", skt, err)
		return 0
	}

	return power
}

func (o Operation) GetSocketPowerMaxLimit(skt int) uint32 {
	if len(o.IOHCs) == 0 || o.IOHCs[skt] == nil {
		fmt.Printf("please init IOHCs first")
		return 0
	}

	power, err := Mailbox(o.IOHCs[skt], AMD_SOCKET_MAX_POWER_LIMIT_READ, 0)
	if err != nil {
		fmt.Printf("failed to call mailbox for reading max power limit on socket %d - %v\n", skt, err)
		return 0
	}

	return power
}

func (o Operation) SetSocketPowerLimit(skt int, powerLimit uint32) error {
	if len(o.IOHCs) == 0 || o.IOHCs[skt] == nil {
		return fmt.Errorf("please init IOHCs first")
	}

	if powerLimit > o.MachineInfo.PowerMaxLimit[skt] {
		klog.Infof("invalid power limit %d, the max power limit allowed is %d\n", powerLimit, o.MachineInfo.PowerMaxLimit[skt])
		klog.Infof("overwrite the power limit to %d", o.MachineInfo.PowerMaxLimit[skt])
		powerLimit = o.MachineInfo.PowerMaxLimit[skt]
	}

	_, err := Mailbox(o.IOHCs[skt], AMD_SOCKET_POWER_LIMIT_WRITE, powerLimit)
	if err != nil {
		fmt.Printf("failed to call mailbox to set power limit on socket %d - %v\n", skt, err)
		return err
	}

	return nil
}

func Mailbox(dev *utils.PCIDev, msg_id, msg_argument uint32) (uint32, error) {
	var retry int

	// wait until getting non-zero
	for retry = 0; retry < SMN_MAX_RETRY; retry++ {
		ret := utils.ReadSMN(dev, SMN_BASE_ADDRESS+SMN_RESPONSE_OFFSET)
		if ret == 0 {
			if retry == SMN_MAX_RETRY-1 {
				return 0, fmt.Errorf("fail to occupy mailbox")
			}

			time.Sleep(time.Microsecond * SMN_RETRY_INTERVAL)
		} else {
			break
		}

	}

	// set zero to occpy smn mailbox control
	utils.WriteSMN(dev, SMN_BASE_ADDRESS+SMN_RESPONSE_OFFSET, 0)

	// send msg
	utils.WriteSMN(dev, SMN_BASE_ADDRESS+SMN_ARG_OFFSET, msg_argument)
	utils.WriteSMN(dev, SMN_BASE_ADDRESS+SMN_MSG_ID_OFFSET, msg_id)

	// wait until getting 1
	for retry = 0; retry < SMN_MAX_RETRY; retry++ {
		ret := utils.ReadSMN(dev, SMN_BASE_ADDRESS+SMN_RESPONSE_OFFSET)
		if ret == 0 {
			if retry == SMN_MAX_RETRY-1 {
				return 0, fmt.Errorf("fail to occupy mailbox")
			}

			time.Sleep(time.Microsecond * SMN_RETRY_INTERVAL)
		} else if ret == 1 {
			break
		} else {
			return 0, fmt.Errorf("fail to set mailbox because of firmware error")
		}

	}

	// Read the argument
	result := utils.ReadSMN(dev, SMN_BASE_ADDRESS+SMN_ARG_OFFSET)
	return result, nil
}

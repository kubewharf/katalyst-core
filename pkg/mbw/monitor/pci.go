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

	"github.com/kubewharf/katalyst-core/pkg/mbw/utils/pci"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

const (
	DRV_IS_PCI_VENDOR_ID_AMD = 0x1022
	AMD_ROME_IOHC            = 0x1480
	AMD_GENOA_IOHC           = 0x14a4
)

func (m MBMonitor) InitPCIAccess() error {
	// init the PCI access to read UMC/IMC counters
	pci.PCIDevInit()

	var pciDevID uint16
	if m.Is_Genoa() {
		pciDevID = AMD_GENOA_IOHC
	} else if m.Is_Milan() || m.Is_Rome() {
		pciDevID = AMD_ROME_IOHC
	}

	numaPerSocket, err := m.NUMAsPerSocket()
	if err != nil {
		general.Errorf("failed to get numa per socket - %v", err)
		return err
	}

	// get all IOHC devs on PCI; 0 device usually caused by absence of libpci-dev
	devs := pci.ScanDevices(uint16(DRV_IS_PCI_VENDOR_ID_AMD), pciDevID)
	if len(devs) == 0 {
		return errors.New("no IOHC devive found, likely caused by absence of libpci-dev")
	}

	for i := range m.KatalystMachineInfo.PMU.SktIOHC {
		// any PCI IOHC dev can read all UMCs on a socket,
		// so only need to find out the first IOHC on each socket
		m.KatalystMachineInfo.PMU.SktIOHC[i] = pci.GetFirstIOHC(i*numaPerSocket, devs)
		general.Infof("socket %d got iohc dev %s on its first node (numa node %d)", i, m.KatalystMachineInfo.PMU.SktIOHC[i].BDFString(), i*numaPerSocket)
	}

	return nil
}

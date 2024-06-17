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
	"fmt"

	"github.com/kubewharf/katalyst-core/pkg/mbw/utils/msr"
	"github.com/kubewharf/katalyst-core/pkg/mbw/utils/rdt"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

func (m *MBMonitor) InitRDT() error {
	m.RDTScalar = rdt.GetRDTScalar()

	if m.Vendor == machine.CPU_VENDOR_INTEL || m.Vendor == machine.CPU_VENDOR_AMD {
		for _, cores := range m.KatalystMachineInfo.CCDMap {
			// Ensure each ccd uses a unique rmid range
			// each CCD is a separate Die on AMD Genoa
			for i, core := range cores {
				m.Controller.RMIDMap[core] = i
				if err := m.Assign_Uniq_RMID(uint32(core), uint64(i)); err != nil {
					return fmt.Errorf("failed to assign unique rmid to each core %v", err)
				}
			}
		}
	} else {
		return fmt.Errorf("unsupported processor for memory-bandwidth control - %s", m.Vendor)
	}

	general.Infof("initial rdt successfully")

	return nil
}

func (m MBMonitor) Assign_Uniq_RMID(core uint32, rmid uint64) error {
	assoc, err := msr.ReadMSR(core, rdt.PQR_ASSOC)
	if err != nil {
		return fmt.Errorf("failed to assign rmid to core %d - faild to read msr %v", core, err.Error())
	}

	assoc = (assoc & 0xffffffff00000000) + rmid

	err = msr.WriteMSR(core, rdt.PQR_ASSOC, assoc)
	if err != nil {
		return fmt.Errorf("failed to assign rmid to core %d - faild to write msr %v", core, err.Error())
	}

	// amd genoa init
	if m.Is_Genoa() {
		msr.WriteMSR(core, 0xc0000400, 0x3f)
		msr.WriteMSR(core, 0xc0000401, 0x15)
	}

	return nil
}

func (m MBMonitor) ReadCoreMB() (map[machine.CORE_MB_EVENT_TYPE][]uint64, error) {
	var err error
	mbMap := map[machine.CORE_MB_EVENT_TYPE][]uint64{}
	lrmb := make([]uint64, len(m.MemoryBandwidth.Cores))
	trmb := make([]uint64, len(m.MemoryBandwidth.Cores))

	for i := 0; i < len(m.MemoryBandwidth.Cores); i++ {
		// get local read mb
		lrmb[i], err = rdt.GetRDTValue(uint32(i), rdt.PQOS_MON_EVENT_LMEM_BW)
		if err != nil {
			general.Errorf("failed to read lrmb on core %d: %v", i, err)
		}

		// get total read mb
		trmb[i], err = rdt.GetRDTValue(uint32(i), rdt.PQOS_MON_EVENT_TMEM_BW)
		if err != nil {
			general.Errorf("failed to read rrmb on core %d: %v", i, err)
		}
	}

	mbMap[machine.CORE_MB_READ_LOCAL] = lrmb
	mbMap[machine.CORE_MB_READ_TOTAL] = trmb

	return mbMap, err
}

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
	"github.com/kubewharf/katalyst-core/pkg/mbw/utils"
	"github.com/kubewharf/katalyst-core/pkg/mbw/utils/pci"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

const (
	UMC_PERF_BASE   uint32 = 0x50000
	UMC_CH_SIZE     uint32 = 0x100000
	UMC_CTL_CAS_RD  uint32 = 0x8000010A
	UMC_CTL_CAS_WR  uint32 = 0x8000020A
	UMC_CLKCTL_G_EN uint32 = 0x83000000
	UMC_CLKCTL_BASE uint32 = 0xD00

	UMC_CTL_BASE          = 0xD04
	UMC_CTL_BASE_GENOA    = 0xD10
	UMC_CTL_SIZE          = 0x04
	UMC_CTL_SIZE_GENOA    = 0x0C
	UMC_CTR_LO_BASE       = 0xD28
	UMC_CTR_HI_BASE       = 0xD2C
	UMC_CTR_LO_BASE_GENOA = 0xD14
	UMC_CTR_HI_BASE_GENOA = 0xD18
	UMC_CTR_SIZE          = 0x08
	UMC_CTR_SIZE_GENOA    = 0x0C
	UMC_PER_PKG           = 8
	UMC_PER_PKG_GENOA     = 12
	UMC_PMC_MASK          = (1 << 48) - 1
	UMC_IOHC_READ         = 0
	UMC_IOHC_WRITE        = 1
)

func (m MBMonitor) StartUMCMonitor() {
	var ctl uint32
	for skt := 0; skt < m.SysInfo.MachineInfo.NumSockets; skt++ {
		for umc := 0; umc < m.PMU.UMC.NumPerSocket; umc++ {
			// init read umc
			ctl = UMC_PERF_BASE + uint32(umc)*UMC_CH_SIZE + m.PMU.UMC.CtlBase + 0*m.PMU.UMC.CtlSize
			pci.WriteSMNApp(m.PMU.SktIOHC[skt], ctl, UMC_CTL_CAS_RD)

			// init write umc
			ctl = UMC_PERF_BASE + uint32(umc)*UMC_CH_SIZE + m.PMU.UMC.CtlBase + 1*m.PMU.UMC.CtlSize
			pci.WriteSMNApp(m.PMU.SktIOHC[skt], ctl, UMC_CTL_CAS_WR)

			ctl = UMC_PERF_BASE + uint32(umc)*UMC_CH_SIZE + UMC_CLKCTL_BASE
			pci.WriteSMNApp(m.PMU.SktIOHC[skt], ctl, UMC_CLKCTL_G_EN)

			general.Infof("start UMC monitoring on socket %d and umc %d", skt, umc)
		}
	}

	general.Infof("start monitoring the UMC")
}

func (m MBMonitor) StopUMCMonitor() {
	var ctl uint32
	for skt := 0; skt < m.SysInfo.MachineInfo.NumSockets; skt++ {
		for umc := 0; umc < m.PMU.UMC.NumPerSocket; umc++ {
			// init read umc
			ctl = UMC_PERF_BASE + uint32(umc)*UMC_CH_SIZE + m.PMU.UMC.CtlBase + 0*m.PMU.UMC.CtlSize
			pci.WriteSMNApp(m.PMU.SktIOHC[skt], ctl, UMC_CTL_CAS_RD)

			// init write umc
			ctl = UMC_PERF_BASE + uint32(umc)*UMC_CH_SIZE + m.PMU.UMC.CtlBase + 1*m.PMU.UMC.CtlSize
			pci.WriteSMNApp(m.PMU.SktIOHC[skt], ctl, UMC_CTL_CAS_WR)

			ctl = UMC_PERF_BASE + uint32(umc)*UMC_CH_SIZE + UMC_CLKCTL_BASE
			pci.WriteSMNApp(m.PMU.SktIOHC[skt], ctl, 0)

			general.Infof("stop UMC monitoring on socket %d and umc %d", skt, umc)
		}
	}

	general.Infof("stop monitoring the UMC")
}

func (m MBMonitor) ClearUMCMonitor() {
	var ctl uint32
	for skt := 0; skt < m.SysInfo.MachineInfo.NumSockets; skt++ {
		for umc := 0; umc < m.PMU.UMC.NumPerSocket; umc++ {
			ctl = UMC_PERF_BASE + uint32(umc)*UMC_CH_SIZE + UMC_CLKCTL_BASE
			pci.WriteSMNApp(m.PMU.SktIOHC[skt], ctl, UMC_CLKCTL_G_EN)

			general.Infof("clear UMC setting on socket %d and umc %d", skt, umc)
		}
	}

	general.Infof("all UMC settings has been cleared")
}

// read the UMC by pci iohc dev
func (m MBMonitor) ReadUMCCounter(iohc *pci.PCIDev, umc_idx, ctl_idx uint32) uint64 {
	var val1, val2 uint64
	var offset uint32

	offset = UMC_PERF_BASE + umc_idx*UMC_CH_SIZE + m.PMU.UMC.CtrLowBase + ctl_idx*m.PMU.UMC.CtlSize
	val1 = uint64(pci.ReadSMNApp(iohc, offset))

	offset = UMC_PERF_BASE + umc_idx*UMC_CH_SIZE + m.PMU.UMC.CtrHighBase + ctl_idx*m.PMU.UMC.CtlSize
	val2 = uint64(pci.ReadSMNApp(iohc, offset))

	return ((val2 << 32) + val1) & UMC_PMC_MASK
}

// collect the memory-bandwidth for each package by reading UMC
func (m MBMonitor) ReadPackageUMC() {
	var cas_rd, cas_wr uint64 = 0, 0

	for skt := 0; skt < m.SysInfo.MachineInfo.NumSockets; skt++ {
		for umc := 0; umc < m.PMU.UMC.NumPerSocket; umc++ {
			cas_rd += m.ReadUMCCounter(m.PMU.SktIOHC[skt], uint32(umc), UMC_IOHC_READ)
			cas_wr += m.ReadUMCCounter(m.PMU.SktIOHC[skt], uint32(umc), UMC_IOHC_WRITE)

			// general.Infof("get the umc reading on socket %d and umc %d: (%d, %d)", skt, umc, cas_rd, cas_wr)
			// accumulate the UMC result for each package (i.e. physical Numa)
			if (umc+1)%m.PMU.UMC.NumPerPackage == 0 {
				pkgIdx := (skt*m.PMU.UMC.NumPerSocket + umc) / m.PMU.UMC.NumPerPackage
				m.MemoryBandwidth.Packages[pkgIdx].RMB_Delta = utils.Delta(48, cas_rd, m.MemoryBandwidth.Packages[pkgIdx].RMB)
				m.MemoryBandwidth.Packages[pkgIdx].WMB_Delta = utils.Delta(48, cas_wr, m.MemoryBandwidth.Packages[pkgIdx].WMB)
				m.MemoryBandwidth.Packages[pkgIdx].Total = utils.PMUToMB(m.MemoryBandwidth.Packages[pkgIdx].WMB_Delta+m.MemoryBandwidth.Packages[pkgIdx].RMB_Delta, m.Interval)

				m.MemoryBandwidth.Packages[pkgIdx].RMB = cas_rd
				m.MemoryBandwidth.Packages[pkgIdx].WMB = cas_wr
				// general.Infof("get the mb result for physical node %d: (%d, %d)", (skt*m.UMC.NumPerSocket+umc)/m.UMC.NumPerPackage, cas_rd, cas_wr)

				cas_rd = 0
				cas_wr = 0
			}
		}
	}
}

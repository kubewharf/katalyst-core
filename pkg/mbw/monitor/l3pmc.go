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
	"github.com/kubewharf/katalyst-core/pkg/mbw/utils/msr"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

const (
	L3PMCCTL_2     = 0xC0010234
	L3PMCCTL_3     = 0xC0010236
	L3PMCCTR_2     = 0xC0010235
	L3PMCCTR_3     = 0xC0010237
	L3PMCLAT1      = 0xFF0F000000400090
	L3PMCLAT2      = 0xFF0F000000401F9A
	L3PMCLAT1_M    = 0x0300C00000400090
	L3PMCLAT2_M    = 0x0300C00000401F9A
	L3PMCLAT1_G    = 0x0303C00000400090 // total l3 miss
	L3PMCLAT2_G    = 0x0303C00000401F9A // total l3 miss
	L3PMC_EVE_LAT1 = 3
	L3PMC_EVE_LAT2 = 4
)

func (m MBMonitor) StartL3PMCMonitor() {
	// each AMD Die/CCD/CCX has a separate L3 cache
	// only need to collect L3 miss latency on the 1st core of each CCD
	for i, ccd := range m.SysInfo.CCDMap {
		m.StartL3PMCEvent(ccd[0], L3PMC_EVE_LAT1)
		m.StartL3PMCEvent(ccd[0], L3PMC_EVE_LAT2)

		general.Infof("start monitoring the L3PMC on ccd %d", i)
	}
}

func (m MBMonitor) StopL3PMCMonitor() {
	for i, ccd := range m.SysInfo.CCDMap {
		StopL3PMCEvent(ccd[0], L3PMC_EVE_LAT1)
		StopL3PMCEvent(ccd[0], L3PMC_EVE_LAT2)

		general.Infof("stop monitoring the L3PMC on ccd %d", i)
	}
}

func (m MBMonitor) ClearL3PMCSetting() {
	for i, ccd := range m.SysInfo.CCDMap {
		ClearL3PMCEvent(ccd[0], L3PMC_EVE_LAT1)
		ClearL3PMCEvent(ccd[0], L3PMC_EVE_LAT2)

		general.Infof("clear the L3PMC event on ccd %d", i)
	}
}

func (m *MBMonitor) ReadL3MissLatency() error {
	m.MemoryLatency.CCDLocker.Lock()
	defer m.MemoryLatency.CCDLocker.Unlock()

	for i, ccd := range m.SysInfo.CCDMap {
		l3lat1, err := ReadL3PMCEvent(ccd[0], L3PMC_EVE_LAT1)
		if err != nil {
			general.Errorf("failed to read L3 miss latency on ccd %d / core %d lat 1 - %v", i, ccd[0], err)
			return err
		}
		l3lat2, err := ReadL3PMCEvent(ccd[0], L3PMC_EVE_LAT2)
		if err != nil {
			general.Errorf("failed to read L3 miss latency on ccd %d / core %d lat 2 - %v", i, ccd[0], err)
			return err
		}

		m.MemoryLatency.L3Latency[i].L3PMCLatency1_Delta = utils.Delta(48, l3lat1, m.MemoryLatency.L3Latency[i].L3PMCLatency1)
		m.MemoryLatency.L3Latency[i].L3PMCLatency2_Delta = utils.Delta(48, l3lat2, m.MemoryLatency.L3Latency[i].L3PMCLatency2)
		m.MemoryLatency.L3Latency[i].L3PMCLatency = utils.L3PMCToLatency(m.MemoryLatency.L3Latency[i].L3PMCLatency1_Delta,
			m.MemoryLatency.L3Latency[i].L3PMCLatency2_Delta, m.Interval) * utils.GetCPUClock(ccd[0], string(m.SysInfo.Vendor))

		m.MemoryLatency.L3Latency[i].L3PMCLatency1 = l3lat1
		m.MemoryLatency.L3Latency[i].L3PMCLatency2 = l3lat2
	}

	return nil
}

func (m MBMonitor) StartL3PMCEvent(cpu, event int) {
	switch event {
	case L3PMC_EVE_LAT1:
		if m.SysInfo.Family >= 0x19 && m.SysInfo.Model >= AMD_ZEN4_GENOA_A {
			msr.WriteMSR(uint32(cpu), L3PMCCTL_2, L3PMCLAT1_G)
		} else if m.SysInfo.Family >= 0x19 {
			msr.WriteMSR(uint32(cpu), L3PMCCTL_2, L3PMCLAT1_M)
		} else {
			msr.WriteMSR(uint32(cpu), L3PMCCTL_2, L3PMCLAT1)
		}
	case L3PMC_EVE_LAT2:
		if m.SysInfo.Family >= 0x19 && m.SysInfo.Model >= AMD_ZEN4_GENOA_A {
			msr.WriteMSR(uint32(cpu), L3PMCCTL_3, L3PMCLAT2_G)
		} else if m.SysInfo.Family >= 0x19 {
			msr.WriteMSR(uint32(cpu), L3PMCCTL_3, L3PMCLAT2_M)
		} else {
			msr.WriteMSR(uint32(cpu), L3PMCCTL_3, L3PMCLAT2)
		}
	}

	general.Infof("start monitoring the L3PMC on core %d", cpu)
}

func ReadL3PMCEvent(cpu, event int) (uint64, error) {
	var ctr uint64
	var err error
	switch event {
	case L3PMC_EVE_LAT1:
		ctr, err = msr.ReadMSR(uint32(cpu), L3PMCCTR_2)
	case L3PMC_EVE_LAT2:
		ctr, err = msr.ReadMSR(uint32(cpu), L3PMCCTR_3)
	}

	if err != nil {
		general.Errorf("failed to read L3PMC event on core %d - %v", cpu, err)
		return 0, err
	}

	return ctr & 0xffffffffffff, err
}

func StopL3PMCEvent(cpu, event int) {
	switch event {
	case L3PMC_EVE_LAT1:
		msr.WriteMSR(uint32(cpu), L3PMCCTL_2, 0)
	case L3PMC_EVE_LAT2:
		msr.WriteMSR(uint32(cpu), L3PMCCTL_3, 0)
	}

	general.Infof("stop monitoring the L3PMC on core %d", cpu)
}

func ClearL3PMCEvent(cpu, event int) {
	switch event {
	case L3PMC_EVE_LAT1:
		msr.WriteMSR(uint32(cpu), L3PMCCTR_2, 0)
	case L3PMC_EVE_LAT2:
		msr.WriteMSR(uint32(cpu), L3PMCCTR_3, 0)
	}

	general.Infof("clear the L3PMC event on core %d", cpu)
}

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
	"context"
	"fmt"
	"sync"

	"github.com/kubewharf/katalyst-core/pkg/config/agent/global"
	"github.com/kubewharf/katalyst-core/pkg/mbw/utils"
	"github.com/kubewharf/katalyst-core/pkg/mbw/utils/pci"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

const (
	MBM_MONITOR_INTERVAL = 1000

	MAX_NUMA_DISTANCE = 32 // this is the inter-socket distance on AMD, intel inter-socket distance < 32
	MIN_NUMA_DISTANCE = 11 // the distance to local numa is 10 in Linux, thus 11 is the minimum inter-numa distance

	MBA_COS_MAX = 16 // AMD supports up to 16 cos per CCD, while Intel supports up to 16 cos per socket
)

// newExtKatalystMachineInfo arguments the normal KatalystMachineInfo with memory bandwidth related information
// for now it could not be unified into normal machine info as it would err at unsupported hardware
// todo: merge this argumentation into the 'normal' info so that the unified info exists only
func newExtKatalystMachineInfo(machineInfoConfig *global.MachineInfoConfiguration) (
	*machine.KatalystMachineInfo, error,
) {
	// ensure max numa distance make sense, as it is critical for mbw monitor
	if machineInfoConfig.SiblingNumaMaxDistance < MIN_NUMA_DISTANCE {
		machineInfoConfig.SiblingNumaMaxDistance = MAX_NUMA_DISTANCE
	}
	info, err := machineWrapper.GetKatalystMachineInfo(machineInfoConfig)
	if err != nil {
		fmt.Println("Failed to initialize the katalyst machine info")
		return nil, err
	}

	if info.FakeNumaConfigured() {
		info.FakeNUMAEnabled = true
	}

	// ExtraTopologyInfo handling is still under development
	numasPerPackage := info.ExtraTopologyInfo.SiblingNumaMap[0].Len() + 1
	info.NumPackages = info.NumNUMANodes / numasPerPackage
	info.PackagePerSocket = info.NumPackages / info.MachineInfo.NumSockets
	info.PackageMap = info.GetPackageMap()

	info.CCDMap, err = utils.GetCCDTopology(info.NumNUMANodes)
	if err != nil {
		return nil, fmt.Errorf("failed to get CCD topology - %v", err)
	}
	info.NumCCDs = len(info.CCDMap)
	info.DieSize = len(info.CCDMap[0])

	// calculate the mapping from numa node to ccds, suppose that each numa node already aligns with ccds
	info.NumaMap, err = info.GetNumaCCDMap()
	if err != nil {
		return nil, fmt.Errorf("failed to get mapping from numa node to CCDs - %v", err)
	}

	info.MemoryBandwidth.Cores = make([]machine.CoreMB, info.MachineInfo.NumCores)
	for i := range info.MemoryBandwidth.Cores {
		// the physical NUMA ID equals to "node ID / number of node per physical NUMA"
		info.MemoryBandwidth.Cores[i].Package = info.CPUTopology.CPUDetails[i].NUMANodeID / (info.ExtraTopologyInfo.SiblingNumaMap[info.CPUTopology.CPUDetails[i].NUMANodeID].Len() + 1)
	}

	info.MemoryBandwidth.Numas = make([]machine.NumaMB, info.NumNUMANodes)
	for i := range info.MemoryBandwidth.Numas {
		info.MemoryBandwidth.Numas[i].Package = i / (info.ExtraTopologyInfo.SiblingNumaMap[info.CPUTopology.CPUDetails[i].NUMANodeID].Len() + 1)
	}

	info.MemoryBandwidth.Packages = make([]machine.PackageMB, info.NumPackages)
	info.PMU.SktIOHC = make([]*pci.PCIDev, info.MachineInfo.NumSockets)

	// each package rmid is initialized to 0
	info.RMIDPerPackage = make([]uint32, info.NumPackages)

	// initialize the AMD UMC if needed
	if info.Is_Genoa() {
		info.PMU.UMC.CtlBase = UMC_CTL_BASE_GENOA
		info.PMU.UMC.CtlSize = UMC_CTL_SIZE_GENOA
		info.PMU.UMC.CtrLowBase = UMC_CTR_LO_BASE_GENOA
		info.PMU.UMC.CtrHighBase = UMC_CTR_HI_BASE_GENOA
		info.PMU.UMC.CtrSize = UMC_CTR_SIZE_GENOA
		info.PMU.UMC.NumPerSocket = UMC_PER_PKG_GENOA
	} else if info.Is_Milan() || info.Is_Rome() {
		info.PMU.UMC.CtlBase = UMC_CTL_BASE
		info.PMU.UMC.CtlSize = UMC_CTL_SIZE
		info.PMU.UMC.CtrLowBase = UMC_CTR_LO_BASE
		info.PMU.UMC.CtrHighBase = UMC_CTR_HI_BASE
		info.PMU.UMC.CtrSize = UMC_CTR_SIZE
		info.PMU.UMC.NumPerSocket = UMC_PER_PKG
	}

	info.PMU.UMC.NumPerPackage = info.PMU.UMC.NumPerSocket / info.PackagePerSocket

	return info, nil
}

func NewMonitor(machineInfoConfig *global.MachineInfoConfiguration) (*MBMonitor, error) {
	sysInfo, err := newExtKatalystMachineInfo(machineInfoConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create the sysInfo - %v", err)
	}
	return newMonitor(sysInfo)
}

func newMonitor(info *machine.KatalystMachineInfo) (*MBMonitor, error) {
	general.Infof("Vendor: %s", info.Vendor)
	general.Infof("CPU cores: %d", info.MachineInfo.NumCores)
	general.Infof("CPU per NUMA: %d", info.CPUsPerNuma())
	general.Infof("NUMAs: %d", info.NumNUMANodes)
	general.Infof("Packages: %d", info.NumPackages)
	general.Infof("PackageMap: %v", info.PackageMap)
	general.Infof("UMC per pakcage: %d", info.PMU.UMC.NumPerPackage)
	general.Infof("Sockets: %d", info.MachineInfo.NumSockets)
	general.Infof("UMC per socket: %d", info.PMU.UMC.NumPerSocket)
	general.Infof("CCDs: %d", len(info.CCDMap))
	general.Infof("Cores per CCD: %d", info.CCDMap)
	general.Infof("CCDs per Numa: %v", info.NumaMap)
	general.Infof("FakeNuma Enabled: %t", info.FakeNUMAEnabled)
	general.Infof("SiblingNumaMap: %v", info.ExtraTopologyInfo.SiblingNumaMap)

	monitor := &MBMonitor{
		KatalystMachineInfo: info,
		Interval:            MBM_MONITOR_INTERVAL,
		Controller:          MBController{},
	}

	monitor.Controller.Instances = make([]Instance, 0)
	monitor.Controller.PackageThrottled = make(map[int]bool, info.NumPackages)
	monitor.Controller.NumaLowPriThrottled = make(map[int]bool, info.NumPackages)
	monitor.Controller.NumaThrottled = make(map[int]bool, info.NumNUMANodes)
	monitor.Controller.RMIDMap = make(map[int]int, info.MachineInfo.NumCores)
	monitor.Controller.CCDCosMap = make(map[int][]CosEntry, info.NumCCDs)
	for i := 0; i < info.NumCCDs; i++ {
		monitor.Controller.CCDCosMap[i] = make([]CosEntry, MBA_COS_MAX)
		// we actually only need the first cos on each ccd before supporting workloads hybird deployment
	}

	if err := monitor.ResetMBACos(); err != nil {
		general.Errorf("failed to initialize the MBA cos - %v", err)
		return nil, err
	}

	return monitor, nil
}

func (m MBMonitor) Init() error {
	// init RDT for per-core monitoring
	if err := m.InitRDT(); err != nil {
		general.Errorf("failed to init rdt - %v", err)
		return fmt.Errorf("failed to init rdt - %v", err)
	}

	if err := m.InitPCIAccess(); err != nil {
		general.Errorf("failed to init PCI access - %v", err)
		return fmt.Errorf("failed to init PCI access - %v", err)
	}

	// start PMC monitoring
	switch m.Vendor {
	case machine.CPU_VENDOR_AMD:
		m.StartPMC()
	case machine.CPU_VENDOR_INTEL:
		// not support yet
	case machine.CPU_VENDOR_ARM:
		// not support yet
	}

	return nil
}

func (m MBMonitor) Stop() {
	// stop PMC monitoring
	switch m.Vendor {
	case machine.CPU_VENDOR_AMD:
		m.StopPMC()
	case machine.CPU_VENDOR_INTEL:
		// not support yet
	case machine.CPU_VENDOR_ARM:
		// not support yet
	}

	// tear down the PCI dev access
	pci.PCIDevCleanup()

	m.Started = false
}

type serveFunc func() error

// GlobalStats gets stats about the mem and the CPUs
func (m MBMonitor) GlobalStats(ctx context.Context, refreshRate uint64) error {
	serveFuncs := []serveFunc{
		// measure the per-package (i.e. a physical NUMA) memory bandwidth by reading PMC
		m.ServePackageMB,
		// measure the per-core memory bandwidth by RDT
		m.ServeCoreMB,
		// measure the per-die (i.e. a CCD on AMD Genoa) memory access latency by reading L3 PMC
		m.ServeL3Latency,
	}

	return utils.TickUntilDone(ctx, refreshRate, func() error {
		var wg sync.WaitGroup

		errCh := make(chan error, len(serveFuncs))

		for _, sf := range serveFuncs {
			wg.Add(1)
			go func(sf serveFunc) {
				defer wg.Done()
				errCh <- sf()
			}(sf)
		}

		wg.Wait()
		close(errCh)
		for err := range errCh {
			if err != nil {
				return err
			}
		}

		return nil
	})
}

// ServeL3Latency() collects the latency between that a L3 cache line is missed to that it is loaded from memory to L3
func (m MBMonitor) ServeL3Latency() error {
	var err error = nil
	switch m.Vendor {
	case machine.CPU_VENDOR_AMD:
		err = m.ReadL3MissLatency()
	case machine.CPU_VENDOR_INTEL:
		// not support yet
	case machine.CPU_VENDOR_ARM:
		// not support yet
	}

	return err
}

// ServeCoreMB provides per-core memory-bandwidth and calculates the per-numa results
func (m MBMonitor) ServeCoreMB() error {
	// ensure the integrity of data collection and calculation
	m.MemoryBandwidth.CoreLocker.Lock()
	defer m.MemoryBandwidth.CoreLocker.Unlock()

	coreMBMap, err := m.ReadCoreMB()
	if err != nil {
		return err
	}

	// reset the memory-bandwidth of each NUMA node
	for i := 0; i < m.NumNUMANodes; i++ {
		m.MemoryBandwidth.Numas[i].LRMB = 0
		m.MemoryBandwidth.Numas[i].TRMB = 0
		m.MemoryBandwidth.Numas[i].RRMB = 0
	}

	// collect the per-core memory-bandwidth by RDT event and calculate the per-NUMA memory-bandwidth
	for i := 0; i < len(m.MemoryBandwidth.Cores); i++ {
		// we only have the accurate read bw on AMD Genoa for now
		m.MemoryBandwidth.Cores[i].LRMB_Delta = utils.Delta(24, coreMBMap[machine.CORE_MB_READ_LOCAL][i], m.MemoryBandwidth.Cores[i].LRMB)
		m.MemoryBandwidth.Cores[i].LRMB = coreMBMap[machine.CORE_MB_READ_LOCAL][i]
		m.MemoryBandwidth.Numas[m.CPUTopology.CPUDetails[i].NUMANodeID].LRMB += m.MemoryBandwidth.Cores[i].LRMB_Delta

		m.MemoryBandwidth.Cores[i].TRMB_Delta = utils.Delta(24, coreMBMap[machine.CORE_MB_READ_TOTAL][i], m.MemoryBandwidth.Cores[i].TRMB)
		if m.MemoryBandwidth.Cores[i].TRMB_Delta > m.MemoryBandwidth.Cores[i].LRMB_Delta {
			m.MemoryBandwidth.Cores[i].RRMB_Delta = m.MemoryBandwidth.Cores[i].TRMB_Delta - m.MemoryBandwidth.Cores[i].LRMB_Delta
			m.MemoryBandwidth.Numas[m.CPUTopology.CPUDetails[i].NUMANodeID].RRMB += m.MemoryBandwidth.Cores[i].RRMB_Delta
		} else {
			m.MemoryBandwidth.Cores[i].RRMB_Delta = 0
		}

		m.MemoryBandwidth.Cores[i].TRMB = coreMBMap[machine.CORE_MB_READ_TOTAL][i]
		m.MemoryBandwidth.Numas[m.CPUTopology.CPUDetails[i].NUMANodeID].TRMB += m.MemoryBandwidth.Cores[i].TRMB_Delta

		// to-do: record the write mb from counter as well, we do not have the accurate write data from counters for now
	}

	// estimate the write mb by read/write ration on the physical node, and calculate the total mb
	for i := 0; i < m.NumNUMANodes; i++ {
		pkg := m.KatalystMachineInfo.GetPkgByNuma(i)
		if pkg == -1 {
			continue
		}

		wrRatio := float64(m.MemoryBandwidth.Packages[pkg].WMB_Delta) / float64(m.MemoryBandwidth.Packages[pkg].RMB_Delta)
		m.MemoryBandwidth.Numas[i].Total = utils.RDTEventToMB(m.MemoryBandwidth.Numas[i].TRMB+uint64(wrRatio*float64(m.MemoryBandwidth.Numas[i].TRMB)), m.Interval, uint64(m.RDTScalar))
	}

	return nil
}

// ServePackageMB provides information about memory-bandwidth per package
func (m MBMonitor) ServePackageMB() error {
	m.MemoryBandwidth.PackageLocker.Lock()
	defer m.MemoryBandwidth.PackageLocker.Unlock()

	switch m.Vendor {
	case machine.CPU_VENDOR_AMD:
		m.ReadPackageUMC()
	case machine.CPU_VENDOR_INTEL:
		// not support yet
	case machine.CPU_VENDOR_ARM:
		// not support yet
	}

	return nil
}

func (m MBMonitor) StartPMC() {
	// start AMD L3 PMC
	m.StartL3PMCMonitor()
	m.ClearL3PMCSetting()

	// start AMD UMC monitoring
	m.StartUMCMonitor()
	m.ClearUMCMonitor()

	// TODO: Intel IMC
}

func (m MBMonitor) StopPMC() {
	// stop AMD L3 PMC
	m.StopL3PMCMonitor()

	// stop AMD UMC monitoring
	m.StopUMCMonitor()

	// TODO: Intel IMC
}

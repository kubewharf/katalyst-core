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

	"github.com/kubewharf/katalyst-core/pkg/mbw/utils"
	"github.com/kubewharf/katalyst-core/pkg/mbw/utils/pci"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

type CPU_VENDOR_NAME string
type CORE_MB_EVENT_TYPE int
type PACKAGE_MB_EVENT_TYPE int

const (
	CPU_VENDOR_INTEL   CPU_VENDOR_NAME = "Intel"
	CPU_VENDOR_AMD     CPU_VENDOR_NAME = "AMD"
	CPU_VENDOR_ARM     CPU_VENDOR_NAME = "ARM"
	CPU_VENDOR_UNKNOWN CPU_VENDOR_NAME = "UNKNOWN"

	INTEL_FAM6_SKYLAKE_X        = 0x55
	INTEL_FAM6_ICELAKE_X        = 0x6A
	INTEL_FAM6_SAPPHIRERAPIDS_X = 0x8F
	INTEL_FAM6_EMERALDRAPIDS_X  = 0xCF

	AMD_ZEN2_ROME    = 0x31
	AMD_ZEN3_MILAN   = 0x01
	AMD_ZEN4_GENOA_A = 0x10
	AMD_ZEN4_GENOA_B = 0x11

	CORE_MB_READ_LOCAL CORE_MB_EVENT_TYPE = 1
	CORE_MB_READ_TOTAL CORE_MB_EVENT_TYPE = 2

	PACKAGE_MB_READ  PACKAGE_MB_EVENT_TYPE = 1
	PACKAGE_MB_WRITE PACKAGE_MB_EVENT_TYPE = 2
)

type MBMonitor struct {
	*SysInfo
	Controller  MBController
	Interval    uint64
	Started     bool
	MonitorOnly bool // skip the control process if set to true
	mctx        context.Context
	done        context.CancelFunc
}

type SysInfo struct {
	machine.KatalystMachineInfo
	Vendor           CPU_VENDOR_NAME
	Family           int
	Model            int
	FakeNUMAEnabled  bool          // if the fake NUMA is configured on this server
	NumPackages      int           // physical NUMA amount on this server
	PackageMap       map[int][]int // mapping from Package to Numa nodes
	PackagePerSocket int           // pakcage amount on a socket
	RDTScalar        uint32        // get the per-core memory bandwidth by multiplying rdt events with this coefficient
	DieSize          int           // how many cores on a die (i.e. a CCD)
	NumCCDs          int           // the number of CCDs on this machine
	CCDMap           map[int][]int // mapping from CCD to CPU cores on AMD
	NumaMap          map[int][]int // mapping from Numa to CCDs on AMD
	MemoryBandwidth  MemoryBandwidthInfo
	MemoryLatency    MemoryLatencyInfo
	RMIDPerPackage   []uint32
	PMU              PMUInfo
}

type MBController struct {
	MBThresholdPerNUMA  uint64             // MB throttling starts if a physical node's MB bythonds a threshold
	IncreaseStep        uint64             // how much to boost each time when releasing the MB throttling
	DecreaseStep        uint64             // how much to reduce each time when throttling the MB
	SweetPoint          float64            // the point where we start releasing memory bandwidth throttling
	PainPoint           float64            // the point where we start throttling the memory bandwidth
	UnthrottlePoint     float64            // the point where we unthrottle all numas on a package
	CCDCosMap           map[int][]CosEntry // mapping from CCD to its Cos and usage
	RMIDMap             map[int]int        // mapping from core to RMID
	PackageThrottled    map[int]bool       // if a physical numa node is MB throttled
	NumaLowPriThrottled map[int]bool       // if the low priority instances on a physical numa has been throttled
	NumaThrottled       map[int]bool       // if a numa is MB throttled
	Instances           []Instance
}

type CosEntry struct {
	Used    bool   // if used by a instance
	Cap     uint64 // the memory-bandwidth upper-bound
	InsName string // the instance using this cos on a CCD
}

// computing instance for socket container spec
type Instance struct {
	Name        string
	Priority    int              // 1: high; 2: low
	Request     uint64           // min memory bandwidth
	Limit       uint64           // max memory bandwidth
	SoftLimit   bool             // throttle the high-priority instances based on their real-time throughput if all have the same priority
	Nodes       map[int]struct{} // the Numa nodes where it is running on
	CosTracking map[int]int      // track the cos usage on each CCD it is running on
}

type PMUInfo struct {
	SktIOHC []*pci.PCIDev // we can use the first PCI IOHC dev to read all PMUs (i.e. Intel IMC or AMD UMC) on each socket. The umc and memory channel is 1:1 mapping on AMD CPU
	UMC     UMCInfo
	/* support Intel IMC later */
}

type UMCInfo struct {
	CtlBase       uint32
	CtlSize       uint32
	CtrLowBase    uint32
	CtrHighBase   uint32
	CtrSize       uint32
	NumPerSocket  int
	NumPerPackage int
}

type MemoryBandwidthInfo struct {
	CoreLocker    sync.RWMutex
	PackageLocker sync.RWMutex
	Cores         []CoreMB
	Numas         []NumaMB
	Packages      []PackageMB
}

type MemoryLatencyInfo struct {
	CCDLocker sync.RWMutex
	L3Latency []L3PMCLatencyInfo
}

type L3PMCLatencyInfo struct {
	Package             int
	L3PMCLatency1       uint64
	L3PMCLatency1_Delta uint64
	L3PMCLatency2       uint64
	L3PMCLatency2_Delta uint64
	L3PMCLatency        float64
}

type CoreMB struct {
	Package    int    // physical NUMA ID
	LRMB       uint64 // local mb for read
	LRMB_Delta uint64
	RRMB_Delta uint64 // there is no remote read event on the hardware, we can calculate it by total_read - local_read
	TRMB       uint64 // total read
	TRMB_Delta uint64
}

type NumaMB struct {
	Package int    // physical NUMA ID
	LRMB    uint64 // total local read mb of all cores on this NUMA node
	RRMB    uint64 // total remote read mb of all cores on this NUMA node
	TRMB    uint64 // total read mb of all cores on this NUMA node
	Total   uint64 // estimated total mb since we do not have the accurate write for now, unit is MBps
}

type PackageMB struct {
	RMB       uint64
	RMB_Delta uint64 // total local read mb of all cores on this physical NUMA node
	WMB       uint64
	WMB_Delta uint64 // total local write mb of all cores on this physical NUMA node
	Total     uint64 // total mb in MBps: (RMB_Delta + WMB_Delta) / interval
}

type SocketPMU struct {
	IOHC *pci.PCIDev // we can use the first PCI IOHC dev to read all PMUs (i.e. Intel IMC or AMD UMC) on each socket, and the umc and memory channel is 1:1 mapping on AMD CPU
}

func (s SysInfo) Is_Intel() bool {
	return s.Vendor == CPU_VENDOR_INTEL
}

func (s SysInfo) Is_AMD() bool {
	return s.Vendor == CPU_VENDOR_AMD
}

func (s SysInfo) Is_Rome() bool {
	if s.Vendor != CPU_VENDOR_AMD {
		return false
	}

	if s.Family >= 0x17 && s.Model == AMD_ZEN2_ROME {
		return true
	}

	return false
}

func (s SysInfo) Is_Milan() bool {
	if s.Vendor != CPU_VENDOR_AMD {
		return false
	}

	if s.Family == 0x19 && s.Model == AMD_ZEN3_MILAN {
		return true
	}

	return false
}

func (s SysInfo) Is_Genoa() bool {
	if s.Vendor != CPU_VENDOR_AMD {
		return false
	}

	if s.Family == 0x19 && (s.Model == AMD_ZEN4_GENOA_A || s.Model == AMD_ZEN4_GENOA_B) {
		// 0x10 A0 A1 0x11 B0
		return true
	}

	return false
}

func (s SysInfo) Is_SPR() bool {
	if s.Vendor != CPU_VENDOR_INTEL {
		return false
	}

	if s.Model == INTEL_FAM6_SAPPHIRERAPIDS_X {
		return true
	}

	return false
}

func (s SysInfo) Is_EMR() bool {
	if s.Vendor != CPU_VENDOR_INTEL {
		return false
	}

	if s.Model == INTEL_FAM6_EMERALDRAPIDS_X {
		return true
	}

	return false
}

func (s SysInfo) Is_ICX() bool {
	if s.Vendor != CPU_VENDOR_INTEL {
		return false
	}

	if s.Model == INTEL_FAM6_ICELAKE_X {
		return true
	}

	return false
}

func (s SysInfo) Is_SKX() bool {
	if s.Vendor != CPU_VENDOR_INTEL {
		return false
	}

	if s.Model == INTEL_FAM6_SKYLAKE_X {
		return true
	}

	return false
}

func (s SysInfo) GetPkgByNuma(numa int) int {
	for i, v := range s.PackageMap {
		for _, n := range v {
			if n == numa {
				return i
			}
		}
	}

	general.Errorf("failed to find the corresponding package for numa %d", numa)
	return -1
}

func (s SysInfo) GetPackageMap(numasPerPackage int) map[int][]int {
	pMap := make(map[int][]int, s.NumPackages)
	for i := 0; i < s.NumPackages; i++ {
		numas := []int{}
		for j := 0; j < numasPerPackage; j++ {
			numas = append(numas, i*numasPerPackage+j)
		}
		pMap[i] = numas
	}

	return pMap
}

func (s SysInfo) GetNumaCCDMap() (map[int][]int, error) {
	numaMap := make(map[int][]int)

	for idx, cores := range s.CCDMap {
		cpuset := machine.NewCPUSet(cores...)

		for i := 0; i < s.CPUTopology.NumNUMANodes; i++ {
			if cpuset.IsSubsetOf(s.CPUTopology.CPUDetails.CPUsInNUMANodes(i)) {
				numaMap[i] = append(numaMap[i], idx)
			}
		}
	}

	if len(numaMap) != s.CPUTopology.NumNUMANodes {
		general.Errorf("invalide mapping from numa node to ccds - map len: %d, num of numa: %d", len(numaMap), s.CPUTopology.NumNUMANodes)
		return nil, fmt.Errorf("invalide mapping from numa node to ccds - map len: %d, num of numa: %d", len(numaMap), s.CPUTopology.NumNUMANodes)
	}

	return numaMap, nil
}

func (s SysInfo) GetCCDByCoreID(core int) (int, error) {
	for ccd, cores := range s.CCDMap {
		if utils.Contains(cores, core) {
			return ccd, nil
		}
	}

	return -1, fmt.Errorf("unable to get CCD ID for core %d", core)
}

func (c MBController) GetInstancesByCCD(ccd int) []Instance {
	instances := make([]Instance, 0)
	for _, ins := range c.Instances {
		if _, ok := ins.CosTracking[ccd]; ok {
			instances = append(instances, ins)
		}
	}

	return instances
}

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

package machine

import (
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/klauspost/cpuid/v2"
	"github.com/pkg/errors"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/klog/v2"

	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

const (
	cpuInfoPath = "/proc/cpuinfo"
	cpuSysDir   = "/sys/devices/system/cpu"
	nodeSysDir  = "/sys/devices/system/node"
	cpuStatFile = "/proc/stat"
)

var (
	avx2RegExp   = regexp.MustCompile(`^flags\s*:.* (avx2 .*|avx2)$`)
	avx512RegExp = regexp.MustCompile(`^flags\s*:.* (avx512 .*|avx512)$`)

	smtActive = false
	checkOnce = sync.Once{}
)

type ExtraCPUInfo struct {
	// SupportInstructionSet instructions all cpus support.
	SupportInstructionSet sets.String
}

// GetExtraCPUInfo get extend cpu info from proc system
func GetExtraCPUInfo() (*ExtraCPUInfo, error) {
	cpuInfo, err := ioutil.ReadFile(cpuInfoPath)
	if err != nil {
		return nil, errors.Wrapf(err, "could not read file %s", cpuInfoPath)
	}

	return &ExtraCPUInfo{
		SupportInstructionSet: getCPUInstructionInfo(string(cpuInfo)),
	}, nil
}

// getCPUInstructionInfo get cpu instruction info by parsing flags with "avx2", "avx512".
func getCPUInstructionInfo(cpuInfo string) sets.String {
	supportInstructionSet := make(sets.String)

	for _, line := range strings.Split(cpuInfo, "\n") {
		if line == "" {
			continue
		}

		// whether flags match avx2, if matched we think this
		// machine is support avx2 instruction
		if avx2RegExp.MatchString(line) {
			supportInstructionSet.Insert("avx2")
		}

		// whether flags match avx2, if matched we think this
		// machine is support avx512 instruction
		if avx512RegExp.MatchString(line) {
			supportInstructionSet.Insert("avx512")
		}
	}

	return supportInstructionSet
}

// GetCoreNumReservedForReclaim generates per numa reserved for reclaim resource value map.
// per numa reserved resource is taken in a fair way with even step, e.g.
// 4 -> 1 1 1 1; 2 -> 1 1 1 1; 8 -> 2 2 2 2;
func GetCoreNumReservedForReclaim(numReservedCores, numNumaNodes int) map[int]int {
	if numNumaNodes <= 0 {
		numNumaNodes = 1
	}

	if numReservedCores < numNumaNodes {
		numReservedCores = numNumaNodes
	}

	reservedPerNuma := numReservedCores / numNumaNodes
	step := numNumaNodes / numReservedCores

	if reservedPerNuma < 1 {
		reservedPerNuma = 1
	}
	if step < 1 {
		step = 1
	}

	reservedForReclaim := make(map[int]int)
	for id := 0; id < numNumaNodes; id++ {
		if id%step == 0 {
			reservedForReclaim[id] = reservedPerNuma
		} else {
			reservedForReclaim[id] = 0
		}
	}

	return reservedForReclaim
}

func SmtActive() bool {
	checkOnce.Do(func() {
		data, err := ioutil.ReadFile("/sys/devices/system/cpu/smt/active")
		if err != nil {
			klog.ErrorS(err, "failed to check SmtActive")
			return
		}
		active, err := strconv.Atoi(strings.TrimSpace(string(data)))
		if err != nil {
			klog.ErrorS(err, "failed to parse smt active file")
			return
		}
		klog.Infof("smt active %v", active)
		smtActive = active == 1
	})
	return smtActive
}

// PhyCore is the physical core, if contains only one hyper-thread, means another one offline
// threads sorted in ascending order
// some CPU arch's one physical core may has more than 2 hyper-threads
type PhyCore struct {
	CPUs []int64
}

// LLCDomain last level cache domain, subnuma/numa for Intel, CCD for AMD
type LLCDomain struct {
	PhyCores []PhyCore
}

type AMDNuma struct {
	CCDs []*LLCDomain
}

type CPUSocket struct {
	NumaIDs    []int
	CPUs       []int64
	IntelNumas map[int]*LLCDomain // numa id as map key
	AMDNumas   map[int]*AMDNuma   // numa id as map key
}

// CPUInfo is the cpu info, generally all cpus shown in below files are online, i.e. CPUInfo managed all CPUs are online,
// CPUOnline is needless, keep it temporarily
// * /sys/devices/system/node/node{nodeid}/cpulist
// * /sys/devices/system/cpu/cpu{cpuid}/topology/{thread_siblings_list,core_cpus_list,core_siblings_list,package_cpus_list}
// * /sys/devices/system/cpu/cpu{cpuid}/cache/index{0,1,2,3}/shared_cpu_list
type CPUInfo struct {
	CPUVendor  cpuid.Vendor
	Sockets    map[int]*CPUSocket
	CPU2Socket map[int64]int  // cpu id as map key, socket id as map value
	CPUOnline  map[int64]bool // cpu id as map key, CPUOnline contains all online cpus, but not contains any offline cpu
}

// CPUStat is the cpu stat info
// ActiveTime = %user + %nice + %system + %irq + %softirq + %steal + %guest + %guest_nice
// IrqTime = %irq + %softirq
// TotalTime = ActiveTime + %idle + %iowait
// cpu util = (ActiveTime diff) / (TotalTime diff)
// irq (hardirq + softirq) cpu util = (SoftirqTime diff) / (TotalTime diff)
type CPUStat struct {
	User      uint64
	Nice      uint64
	System    uint64
	Idle      uint64
	Iowait    uint64
	Irq       uint64
	Softirq   uint64
	Steal     uint64 // %steal time means vcpu corresponding vcpu thread's sched-wait time in host, so %steal has meaningful value only in VM
	Guest     uint64 // %guest means vcpu thread's running time after enter guest, so %guest have meagningful value only in host
	GuestNice uint64 // %guest_nice means vcpu thread's running time after enter niced guest, so %guest_nice have meagningful value only in host
}

func GetCPUPackageID(cpuID int64) (int, error) {
	cpuName := fmt.Sprintf("cpu%d", cpuID)
	cpuTopoDir := filepath.Join(cpuSysDir, cpuName, "topology")
	if _, err := os.Stat(cpuTopoDir); err != nil && os.IsNotExist(err) {
		return -1, fmt.Errorf("%s is not exist", cpuTopoDir)
	}

	b, err := os.ReadFile(filepath.Join(cpuTopoDir, "physical_package_id"))
	if err != nil {
		return -1, err
	}
	return strconv.Atoi(strings.TrimRight(string(b), "\n"))
}

func GetSocketCount() (int, error) {
	dirEnts, err := os.ReadDir(cpuSysDir)
	if err != nil {
		return -1, fmt.Errorf("failed to ReadDir(%s), err %v", cpuSysDir, err)
	}

	physicalPackages := make(map[string]interface{})

	for _, d := range dirEnts {
		if !d.IsDir() {
			continue
		}

		if !strings.HasPrefix(d.Name(), "cpu") {
			continue
		}

		cpuTopoDir := filepath.Join(cpuSysDir, d.Name(), "topology")
		if _, err := os.Stat(cpuTopoDir); err != nil && os.IsNotExist(err) {
			continue
		}

		physicalPackagetIDPath := filepath.Join(cpuTopoDir, "physical_package_id")
		b, err := os.ReadFile(physicalPackagetIDPath)
		if err != nil {
			return -1, fmt.Errorf("failed to ReadFile(%s), err %v", physicalPackagetIDPath, err)
		}

		physicalPackageIDStr := strings.TrimSpace(strings.TrimRight(string(b), "\n"))
		physicalPackages[physicalPackageIDStr] = nil
	}

	return len(physicalPackages), nil
}

func GetNodeCount() (int, error) {
	dirEnts, err := os.ReadDir(nodeSysDir)
	if err != nil {
		return -1, fmt.Errorf("failed to ReadDir(%s), err %v", nodeSysDir, err)
	}

	nodeCount := 0
	for _, d := range dirEnts {
		if !d.IsDir() {
			continue
		}

		if !strings.HasPrefix(d.Name(), "node") {
			continue
		}

		_, err := strconv.Atoi(strings.TrimPrefix(d.Name(), "node"))
		if err != nil {
			continue
		}
		nodeCount++
	}

	return nodeCount, nil
}

func GetNumaPackageID(nodeID int) (int, error) {
	nodeName := fmt.Sprintf("node%d", nodeID)
	nodeCPUListFile := filepath.Join(nodeSysDir, nodeName, "cpulist")
	if _, err := os.Stat(nodeCPUListFile); err != nil && os.IsNotExist(err) {
		return -1, fmt.Errorf("%s not exists", nodeCPUListFile)
	}

	cpuList, err := general.ParseLinuxListFormatFromFile(nodeCPUListFile)
	if err != nil {
		return -1, fmt.Errorf("failed to ParseLinuxListFormatFromFile(%s), err %v", nodeCPUListFile, err)
	}

	if len(cpuList) == 0 {
		socketCount, err := GetSocketCount()
		if err != nil {
			return -1, fmt.Errorf("failed to GetSocketCount, err %v", err)
		}

		if socketCount == 0 {
			return -1, fmt.Errorf("socket count is 0")
		}

		if socketCount == 1 {
			return 0, nil
		}

		numaCount, err := GetNodeCount()
		if err != nil {
			return -1, fmt.Errorf("failed to GetNodeCount, err %v", err)
		}

		nodeCountPerSocket := numaCount / socketCount

		return nodeID / nodeCountPerSocket, nil
	}

	phyPackageId, err := GetCPUPackageID(cpuList[0])
	if err != nil {
		return -1, fmt.Errorf("failed to GetCPUPackageID(%d), err %v", cpuList[0], err)
	}

	return phyPackageId, nil
}

func GetCPUOnlineStatus(cpuID int64) (bool, error) {
	cpuName := fmt.Sprintf("cpu%d", cpuID)

	// /sys/devices/system/cpu/cpu0/online not exists, because cpu0 cannot be offline
	if cpuID == 0 {
		return true, nil
	}

	if _, err := os.Stat(filepath.Join(cpuSysDir, cpuName)); err != nil && os.IsNotExist(err) {
		return false, fmt.Errorf("cpu %d not exists", cpuID)
	}

	cpuOnlineFile := filepath.Join(cpuSysDir, cpuName, "online")
	// /sys/devices/system/cpu/cpuX/online not exists in some vm
	if _, err := os.Stat(cpuOnlineFile); err != nil && os.IsNotExist(err) {
		return true, nil
	}

	b, err := os.ReadFile(cpuOnlineFile)
	if err != nil {
		return false, err
	}

	online, err := strconv.Atoi(strings.TrimRight(string(b), "\n"))
	if err != nil {
		return false, err
	}

	if online == 1 {
		return true, nil
	}
	return false, nil
}

func getLLCDomain(cpuListFile string) (*LLCDomain, error) {
	cpuList, err := general.ParseLinuxListFormatFromFile(cpuListFile)
	if err != nil {
		return nil, fmt.Errorf("failed to ParseLinuxListFormatFromFile(%s), err %v", cpuListFile, err)
	}

	var llcDomain LLCDomain

	parsedCPUs := make(map[int64]interface{})
	for _, cpuID := range cpuList {
		if _, ok := parsedCPUs[cpuID]; ok {
			continue
		}

		// https://www.kernel.org/doc/Documentation/cputopology.txt
		coreCPUsFile := filepath.Join(cpuSysDir, fmt.Sprintf("cpu%d", cpuID), "topology/thread_siblings_list")
		if _, err := os.Stat(coreCPUsFile); err != nil && os.IsNotExist(err) {
			fmt.Printf("%s is not exist\n", coreCPUsFile)
			continue
		}

		cpus, err := general.ParseLinuxListFormatFromFile(coreCPUsFile)
		if err != nil {
			return nil, fmt.Errorf("failed to getLLCDomain(%s), err %v", coreCPUsFile, err)
		}

		if len(cpus) == 0 {
			return nil, fmt.Errorf("%s has 0 cpu", coreCPUsFile)
		}

		var phyCore PhyCore
		phyCore.CPUs = append(phyCore.CPUs, cpus...)
		for _, cpuID := range cpus {
			parsedCPUs[cpuID] = nil
		}

		llcDomain.PhyCores = append(llcDomain.PhyCores, phyCore)
	}

	return &llcDomain, nil
}

func getIntelNumaTopo(nodeCPUListFile string) (*LLCDomain, error) {
	return getLLCDomain(nodeCPUListFile)
}

func getAMDNumaTopo(nodeCPUListFile string) (*AMDNuma, error) {
	nodeCPUList, err := general.ParseLinuxListFormatFromFile(nodeCPUListFile)
	if err != nil {
		return nil, fmt.Errorf("failed to ParseLinuxListFormatFromFile(%s), err %v", nodeCPUListFile, err)
	}

	var numa AMDNuma
	parsedCPUs := make(map[int64]interface{})
	for _, cpuID := range nodeCPUList {
		if _, ok := parsedCPUs[cpuID]; ok {
			continue
		}

		l3SharedCpuListPath := filepath.Join(cpuSysDir, fmt.Sprintf("cpu%d", cpuID), "cache/index3/shared_cpu_list")
		if _, err := os.Stat(l3SharedCpuListPath); err != nil && os.IsNotExist(err) {
			fmt.Printf("%s is not exist\n", l3SharedCpuListPath)
			continue
		}

		llcDomain, err := getLLCDomain(l3SharedCpuListPath)
		if err != nil {
			return nil, fmt.Errorf("failed to getLLCDomain(%s), err %v", l3SharedCpuListPath, err)
		}
		numa.CCDs = append(numa.CCDs, llcDomain)

		for _, phyCore := range llcDomain.PhyCores {
			for _, cpuID := range phyCore.CPUs {
				parsedCPUs[cpuID] = nil
			}
		}
	}

	return &numa, nil
}

func getSocketCPUList(socket *CPUSocket, cpuVendor cpuid.Vendor) []int64 {
	var cpuList []int64
	if cpuVendor == cpuid.Intel {
		for _, numaID := range socket.NumaIDs {
			numa := socket.IntelNumas[numaID]
			for _, phyCore := range numa.PhyCores {
				cpuList = append(cpuList, phyCore.CPUs...)
			}
		}
	} else if cpuVendor == cpuid.AMD {
		for _, numaID := range socket.NumaIDs {
			numa := socket.AMDNumas[numaID]
			for _, ccd := range numa.CCDs {
				for _, phyCore := range ccd.PhyCores {
					cpuList = append(cpuList, phyCore.CPUs...)
				}
			}
		}
	}

	general.SortInt64Slice(cpuList)
	return cpuList
}

// GetCPUInfoWithTopo get cpu info with topo
// https://www.kernel.org/doc/Documentation/ABI/stable/sysfs-devices-system-cpu
func GetCPUInfoWithTopo() (*CPUInfo, error) {
	cpuInfo := &CPUInfo{
		CPUVendor:  cpuid.CPU.VendorID,
		Sockets:    make(map[int]*CPUSocket),
		CPU2Socket: make(map[int64]int),
		CPUOnline:  make(map[int64]bool),
	}

	// TODO: arm will be supported in the future
	if cpuInfo.CPUVendor != cpuid.Intel && cpuInfo.CPUVendor != cpuid.AMD {
		general.Infof("unsupported cpu arch: %s", cpuInfo.CPUVendor)
		return nil, nil
	}

	dirEnts, err := os.ReadDir(nodeSysDir)
	if err != nil {
		return nil, fmt.Errorf("failed to ReadDir(%s), err %v", nodeSysDir, err)
	}

	for _, d := range dirEnts {
		if !d.IsDir() {
			continue
		}

		if !strings.HasPrefix(d.Name(), "node") {
			continue
		}

		nodeID, err := strconv.Atoi(strings.TrimPrefix(d.Name(), "node"))
		if err != nil {
			continue
		}

		nodeCPUListFile := filepath.Join(nodeSysDir, d.Name(), "cpulist")
		if _, err := os.Stat(nodeCPUListFile); err != nil && os.IsNotExist(err) {
			return nil, fmt.Errorf("%s not exists", nodeCPUListFile)
		}

		if cpuInfo.CPUVendor == cpuid.Intel {
			numa, err := getIntelNumaTopo(nodeCPUListFile)
			if err != nil {
				return nil, fmt.Errorf("getIntelNumaTopo(%d), err %v", nodeID, err)
			}
			if len(numa.PhyCores) > 0 && len(numa.PhyCores[0].CPUs) > 0 {
				phyPackageId, err := GetCPUPackageID(numa.PhyCores[0].CPUs[0])
				if err != nil {
					return nil, fmt.Errorf("failed to GetCPUPackageID(%d), err %v", numa.PhyCores[0].CPUs[0], err)
				}
				if _, ok := cpuInfo.Sockets[phyPackageId]; !ok {
					cpuInfo.Sockets[phyPackageId] = &CPUSocket{
						IntelNumas: make(map[int]*LLCDomain),
					}
				}
				socket := cpuInfo.Sockets[phyPackageId]
				socket.IntelNumas[nodeID] = numa
				socket.NumaIDs = append(socket.NumaIDs, nodeID)
			}

		} else if cpuInfo.CPUVendor == cpuid.AMD {
			numa, err := getAMDNumaTopo(nodeCPUListFile)
			if err != nil {
				return nil, fmt.Errorf("getAMDNumaTopo(%d), err %v", nodeID, err)
			}

			if len(numa.CCDs) > 0 && len(numa.CCDs[0].PhyCores) > 0 && len(numa.CCDs[0].PhyCores[0].CPUs) > 0 {
				phyPackageId, err := GetCPUPackageID(numa.CCDs[0].PhyCores[0].CPUs[0])
				if err != nil {
					return nil, fmt.Errorf("failed to GetCPUPackageID(%d), err %v", numa.CCDs[0].PhyCores[0].CPUs[0], err)
				}
				if _, ok := cpuInfo.Sockets[phyPackageId]; !ok {
					cpuInfo.Sockets[phyPackageId] = &CPUSocket{
						AMDNumas: make(map[int]*AMDNuma),
					}
				}
				socket := cpuInfo.Sockets[phyPackageId]
				socket.AMDNumas[nodeID] = numa
				socket.NumaIDs = append(socket.NumaIDs, nodeID)
			}
		}
	}

	for socketID, socket := range cpuInfo.Sockets {
		sort.Ints(socket.NumaIDs)
		socketCPUList := getSocketCPUList(socket, cpuInfo.CPUVendor)
		socket.CPUs = socketCPUList

		for _, cpuID := range socketCPUList {
			cpuInfo.CPU2Socket[cpuID] = socketID
			online, err := GetCPUOnlineStatus(cpuID)
			if err != nil {
				return nil, fmt.Errorf("failed to GetCPUOnlineStatus(%d), err %v", cpuID, err)
			}

			if !online {
				return nil, fmt.Errorf("offline cpu %d exists in /sys/devices/system/node/nodeX/cpu_list", cpuID)
			}
			cpuInfo.CPUOnline[cpuID] = online
		}
	}

	return cpuInfo, nil
}

func CollectCpuStats() (map[int64]*CPUStat, error) {
	lines, err := general.ReadLines(cpuStatFile)
	if err != nil {
		return nil, err
	}

	cpuStats := make(map[int64]*CPUStat)

	for _, line := range lines {
		if !strings.HasPrefix(line, "cpu") {
			continue
		}

		cols := strings.Fields(line)

		cpuStr := strings.TrimPrefix(cols[0], "cpu")
		if cpuStr == "" {
			continue
		}

		cpu, err := strconv.ParseInt(cpuStr, 10, 64)
		if err != nil {
			klog.Warningf("failed to ParseInt(%s) in cpu line: %s, err %s", cpuStr, line, err)
			continue
		}

		if len(cols) < 8 {
			klog.Warningf("invalid cpu line: %s", line)
			continue
		}

		user, err := strconv.ParseUint(cols[1], 10, 64)
		if err != nil {
			klog.Warningf("failed to ParseUint %s in cpu line %s, err %s", cols[1], line, err)
			continue
		}

		nice, err := strconv.ParseUint(cols[2], 10, 64)
		if err != nil {
			klog.Warningf("failed to ParseUint %s in cpu line %s, err %s", cols[2], line, err)
			continue
		}

		system, err := strconv.ParseUint(cols[3], 10, 64)
		if err != nil {
			klog.Warningf("failed to ParseUint %s in cpu line %s, err %s", cols[3], line, err)
			continue
		}

		idle, err := strconv.ParseUint(cols[4], 10, 64)
		if err != nil {
			klog.Warningf("failed to ParseUint %s in cpu line %s, err %s", cols[4], line, err)
			continue
		}

		iowait, err := strconv.ParseUint(cols[5], 10, 64)
		if err != nil {
			klog.Warningf("failed to ParseUint %s in cpu line %s, err %s", cols[5], line, err)
			continue
		}

		irq, err := strconv.ParseUint(cols[6], 10, 64)
		if err != nil {
			klog.Warningf("failed to ParseUint %s in cpu line %s, err %s", cols[6], line, err)
			continue
		}

		softirq, err := strconv.ParseUint(cols[7], 10, 64)
		if err != nil {
			klog.Warningf("failed to ParseUint %s in cpu line %s, err %s", cols[7], line, err)
			continue
		}

		var steal uint64
		if len(cols) >= 9 {
			steal, err = strconv.ParseUint(cols[8], 10, 64)
			if err != nil {
				klog.Warningf("failed to ParseUint %s in cpu line %s, err %s", cols[8], line, err)
				continue
			}
		}

		var guest uint64
		if len(cols) >= 10 {
			guest, err = strconv.ParseUint(cols[9], 10, 64)
			if err != nil {
				klog.Warningf("failed to ParseUint %s in cpu line %s, err %s", cols[9], line, err)
				continue
			}
		}

		var guestNice uint64
		if len(cols) >= 11 {
			guestNice, err = strconv.ParseUint(cols[10], 10, 64)
			if err != nil {
				klog.Warningf("failed to ParseUint %s in cpu line %s, err %s", cols[10], line, err)
				continue
			}
		}

		cpuStats[cpu] = &CPUStat{
			User:      user,
			Nice:      nice,
			System:    system,
			Idle:      idle,
			Iowait:    iowait,
			Irq:       irq,
			Softirq:   softirq,
			Steal:     steal,
			Guest:     guest,
			GuestNice: guestNice,
		}
	}

	return cpuStats, nil
}

func getAMDSocketPhysicalCores(socket *CPUSocket) []PhyCore {
	var phyCores []PhyCore

	// socket.NumaIDs is sorted by ascending order
	for _, numaID := range socket.NumaIDs {
		numa := socket.AMDNumas[numaID]
		for _, ccd := range numa.CCDs {
			phyCores = append(phyCores, ccd.PhyCores...)
		}
	}

	return phyCores
}

func getIntelSocketPhysicalCores(socket *CPUSocket) []PhyCore {
	var phyCores []PhyCore

	// socket.NumaIDs is sorted by ascending order
	for _, numaID := range socket.NumaIDs {
		numa := socket.IntelNumas[numaID]
		phyCores = append(phyCores, numa.PhyCores...)

	}

	return phyCores
}

func (c *CPUInfo) GetSocketSlice() []int {
	var sockets []int
	for socket := range c.Sockets {
		sockets = append(sockets, socket)
	}

	sort.Ints(sockets)

	return sockets
}

func (c *CPUInfo) GetSocketPhysicalCores(socketID int) []PhyCore {
	if socketID < 0 || socketID > len(c.Sockets) {
		return nil
	}

	if c.CPUVendor == cpuid.AMD {
		return getAMDSocketPhysicalCores(c.Sockets[socketID])
	} else if c.CPUVendor == cpuid.Intel {
		return getIntelSocketPhysicalCores(c.Sockets[socketID])
	} else {
		return nil
	}
}

func (c *CPUInfo) GetNodeCPUList(nodeID int) []int64 {
	var socket *CPUSocket
	for _, s := range c.Sockets {
		for _, numa := range s.NumaIDs {
			if numa == nodeID {
				socket = s
				break
			}
		}

		if socket == nil {
			continue
		}
	}

	if socket == nil {
		return nil
	}

	var cpuList []int64
	if c.CPUVendor == cpuid.Intel {
		numa := socket.IntelNumas[nodeID]
		for _, phyCore := range numa.PhyCores {
			cpuList = append(cpuList, phyCore.CPUs...)
		}
	} else if c.CPUVendor == cpuid.AMD {
		numa := socket.AMDNumas[nodeID]
		for _, ccd := range numa.CCDs {
			for _, phyCore := range ccd.PhyCores {
				cpuList = append(cpuList, phyCore.CPUs...)
			}
		}
	}

	general.SortInt64Slice(cpuList)
	return cpuList
}

func (c *CPUInfo) GetAMDNumaCCDs(nodeID int) ([]*LLCDomain, error) {
	if c.CPUVendor != cpuid.AMD {
		return nil, fmt.Errorf("cpu arch is not amd")
	}

	var socket *CPUSocket
	for _, s := range c.Sockets {
		for _, numa := range s.NumaIDs {
			if numa == nodeID {
				socket = s
				break
			}
		}

		if socket == nil {
			continue
		}
	}

	if socket == nil {
		return nil, fmt.Errorf("failed to find node %d", nodeID)
	}

	var ccds []*LLCDomain
	numa := socket.AMDNumas[nodeID]
	ccds = append(ccds, numa.CCDs...)

	return ccds, nil
}

func GetLLCDomainCPUList(llcDomain *LLCDomain) []int64 {
	var cpuList []int64
	for _, phyCore := range llcDomain.PhyCores {
		cpuList = append(cpuList, phyCore.CPUs...)
	}

	general.SortInt64Slice(cpuList)
	return cpuList
}

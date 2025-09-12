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

package machine

import (
	"fmt"
	"io/ioutil"
	"net"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"

	"github.com/moby/sys/mountinfo"
	"github.com/safchain/ethtool"
	"github.com/vishvananda/netns"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/klog/v2"

	"github.com/kubewharf/katalyst-core/pkg/config/agent/global"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/process"
	procm "github.com/kubewharf/katalyst-core/pkg/util/procfs/manager"
	sysm "github.com/kubewharf/katalyst-core/pkg/util/sysfs/manager"
)

const (
	sysFSDirNormal   = "/sys"
	sysFSDirNetNSTmp = "/tmp/net_ns_sysfs"
)

const (
	nicPathNameDeviceFormatPCI = "devices/pci"
	nicPathNAMEBaseDir         = "class/net"
	bondingMasterPath          = "bonding_masters"
)

const (
	netFileNameSpeed    = "speed"
	netFileNameNUMANode = "device/numa_node"
	netFileNameEnable   = "device/enable"
	nicFileNameIfindex  = "ifindex"
	netOperstate        = "operstate"
	netUP               = "up"
	netEnable           = 1
)

const (
	DefaultNetNSDir    = "/var/run/netns"
	DefaultNetNSSysDir = "/sys"
	TmpNetNSSysDir     = "/tmp/net_ns_sysfs"
	ClassNetBasePath   = "class/net"
	ClassNetFulPath    = "/sys/class/net"
	VirtioNetDriver    = "virtio_net"
	IrqRootPath        = "/proc/irq"
	InterruptsFile     = "/proc/interrupts"
	NetDevProcFile     = "/proc/net/dev"
	SoftnetStatFile    = "/proc/net/softnet_stat"
	SoftIrqsFile       = "/proc/softirqs"
)

var netnsMutex sync.Mutex

// GetExtraNetworkInfo collects network interface information from /sys/class/net and net.Interfaces.
// When multiple network namespaces are enabled, it executes in each namespace to gather NIC information.
// Returns ExtraNetworkInfo containing all discovered network interfaces and their attributes.
func GetExtraNetworkInfo(conf *global.MachineInfoConfiguration) (*ExtraNetworkInfo, error) {
	networkInfo := &ExtraNetworkInfo{}

	nsList := []string{DefaultNICNamespace}
	if conf.NetMultipleNS {
		if conf.NetNSDirAbsPath == "" {
			return nil, fmt.Errorf("GetNetworkInterfaces got nil netNSDirAbsPath")
		}

		if dirs, err := ioutil.ReadDir(conf.NetNSDirAbsPath); err != nil {
			return nil, err
		} else {
			for _, dir := range dirs {
				if !dir.IsDir() {
					nsList = append(nsList, dir.Name())
				}
			}
		}

		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
	}
	general.Infof("namespace list: %v", nsList)

	for _, ns := range nsList {
		nicsInNs, err := getNSNetworkHardwareTopology(ns, conf.NetNSDirAbsPath)
		if err != nil {
			// if multiple ns is disabled, we should block agent start
			// todo: to discuss if block agent start when any ns is parsed failed
			if !conf.NetMultipleNS {
				return nil, fmt.Errorf("failed to get network topology: %v", err)
			}

			general.Errorf("get network topology for ns %s failed: %v", ns, err)
			continue
		}

		networkInfo.Interface = append(networkInfo.Interface, nicsInNs...)
	}
	return networkInfo, nil
}

// DoNetNS executes a callback function within the specified network namespace.
// For the default namespace, runs callback in current namespace without modification.
// For other namespaces:
// 1. Locks OS thread to maintain namespace context
// 2. Creates temporary sysfs mount to isolate from host
// 3. Executes callback with isolated sysfs path
// 4. Cleans up mount and restores original namespace
func DoNetNS(nsName, netNSDirAbsPath string, cb func(sysFsDir string) error) error {
	netnsInfo := NetNSInfo{
		NSName:   nsName,
		NSAbsDir: netNSDirAbsPath,
	}

	nsc, err := netnsEnter(netnsInfo)
	if err != nil {
		return fmt.Errorf("failed to netnsEnter(%s), err %v", netnsInfo.NSName, err)
	}
	defer nsc.netnsExit()

	return cb(nsc.sysMountDir)
}

// getNSNetworkHardwareTopology discovers network interfaces and their hardware attributes
// within a specified network namespace. Handles both regular and bonded interfaces.
// Returns slice of InterfaceInfo containing NIC details like speed, NUMA node, etc.
func getNSNetworkHardwareTopology(nsName, netNSDirAbsPath string) ([]InterfaceInfo, error) {
	var nics []InterfaceInfo

	err := DoNetNS(nsName, netNSDirAbsPath, func(sysFsDir string) error {
		nicsBaseDirPath := path.Join(sysFsDir, nicPathNAMEBaseDir)
		nicDirs, err := os.ReadDir(nicsBaseDirPath)
		if err != nil {
			return err
		}

		bondingNICs := getBondingNetworkInterfaces(path.Join(nicsBaseDirPath, bondingMasterPath))

		nicsAddrMap, err := getInterfaceAddr()
		if err != nil {
			return err
		}

		for _, nicDir := range nicDirs {
			nicName := nicDir.Name()
			nicPath := path.Join(nicsBaseDirPath, nicName)

			devPath, err := filepath.EvalSymlinks(nicPath)
			if err != nil {
				general.Warningf("eval sym link: %s failed with error: %v", nicPath, err)
				continue
			}

			// only return PCI NIC
			if !strings.Contains(devPath, nicPathNameDeviceFormatPCI) && !bondingNICs.Has(nicName) {
				general.Warningf("skip nic: %s with devPath: %s which isn't pci device", nicName, devPath)
				continue
			}

			nic := InterfaceInfo{
				NetNSInfo: NetNSInfo{
					NSName:   nsName,
					NSAbsDir: netNSDirAbsPath,
				},
				Name: nicName,
			}
			if nicAddr, exist := nicsAddrMap[nicName]; exist {
				nic.Addr = nicAddr
			}

			getInterfaceIndex(nicsBaseDirPath, &nic)
			getInterfaceAttr(&nic, nicPath)

			general.Infof("discover nic: %#v", nic)
			nics = append(nics, nic)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	return nics, nil
}

func getInterfaceIndex(nicsBaseDirPath string, info *InterfaceInfo) {
	if info == nil {
		return
	}

	nicName := info.Name
	iface, err := net.InterfaceByName(nicName)
	if err != nil {
		general.Warningf("failed to InterfaceByName(%s), err %v", nicName, err)
		ifIndex, err := getIfIndexFromSys(nicsBaseDirPath, nicName)
		if err != nil {
			general.Errorf("failed to get %s interface index from sys: %v", nicName, err)
			return
		}
		info.IfIndex = ifIndex
		return
	}
	info.IfIndex = iface.Index
}

// getInterfaceAttr parses key information from system files
func getInterfaceAttr(info *InterfaceInfo, nicPath string) {
	if nicNUMANode, err := general.ReadFileIntoInt(path.Join(nicPath, netFileNameNUMANode)); err != nil {
		general.Errorf("ns %v name %v, read NUMA node failed with error: %v. Suppose it's associated with NUMA node 0", info.NSName, info.Name, err)
		// some net device files are missed on VMs (e.g. "device/numanode")
		info.NumaNode = -1
	} else {
		info.NumaNode = nicNUMANode
		if info.NumaNode == -1 {
			// the "device/numanode" file is filled with -1 on some VMs (e.g. byte-vm), we should return 0 instead
			general.Errorf("Invalid NUMA node %v for interface %v. Suppose it's associated with NUMA node 0", info.NumaNode, info.Name)
		}
	}

	if general.IsPathExists(path.Join(nicPath, netFileNameEnable)) {
		if nicEnabledStatus, err := general.ReadFileIntoInt(path.Join(nicPath, netFileNameEnable)); err != nil {
			general.Errorf("ns %v name %v, read enable status failed with error: %v", info.NSName, info.Name, err)
			info.Enable = false
		} else {
			info.Enable = nicEnabledStatus == netEnable
		}
	} else {
		// some VMs do not have "device/enable" file under nicPath, we can read "operstate" for nic status instead
		if nicUPStatus, err := general.ReadFileIntoLines(path.Join(nicPath, netOperstate)); err != nil || len(nicUPStatus) == 0 {
			general.Errorf("ns %v name %v, read operstate failed with error: %v", info.NSName, info.Name, err)
			info.Enable = false
		} else {
			info.Enable = nicUPStatus[0] == netUP
		}
	}

	if nicSpeed, err := general.ReadFileIntoInt(path.Join(nicPath, netFileNameSpeed)); err != nil {
		general.Errorf("ns %v name %v, read speed failed with error: %v", info.NSName, info.Name, err)
		info.Speed = -1
	} else {
		info.Speed = nicSpeed
	}
}

func getIfIndexFromSys(nicsBaseDirPath, nicName string) (int, error) {
	ifIndexPath := path.Join(nicsBaseDirPath, nicName, nicFileNameIfindex)
	b, err := os.ReadFile(ifIndexPath)
	if err != nil {
		return 0, err
	}

	ifindex, err := strconv.Atoi(strings.TrimSpace(strings.TrimRight(string(b), "\n")))
	if err != nil {
		return 0, err
	}

	return ifindex, nil
}

// getInterfaceAddr get interface address which is map of interface name to
// its interface address which includes both ipv6 and ipv4 address.
func getInterfaceAddr() (map[string]*IfaceAddr, error) {
	var err error

	ias := make(map[string]*IfaceAddr)

	interfaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}

	for _, i := range interfaces {
		addr, err := GetInterfaceAddr(i)
		if err != nil {
			general.Warningf("failed to get interface addr from %v, err %v", i, err)
			continue
		}

		if addr != nil {
			ias[i.Name] = addr
		}
	}

	return ias, nil
}

func getBondingNetworkInterfaces(bondingMasterPath string) sets.String {
	bondingNICs := sets.String{}
	lines, err := general.ReadFileIntoLines(bondingMasterPath)
	if err != nil {
		return bondingNICs
	}
	for _, line := range lines {
		nics := strings.Split(line, " ")
		for _, nic := range nics {
			bondingNICs.Insert(nic)
		}
	}
	return bondingNICs
}

func SetIrqAffinityCPUs(irq int, cpus []int64) error {
	if irq <= 0 {
		return fmt.Errorf("invalid irq: %d", irq)
	}
	if len(cpus) == 0 {
		return fmt.Errorf("empty cpus")
	}

	cpusets, err := NewCPUSetInt64(cpus...)
	if err != nil {
		return fmt.Errorf("failed to new cpu set int64(%v): %v", cpus, err)
	}

	if err = procm.ApplyProcInterrupts(irq, cpusets.String()); err != nil {
		return fmt.Errorf("failed to apply proc interrupts, err %v", err)
	}

	return nil
}

func SetIrqAffinity(irq int, cpu int64) error {
	if irq <= 0 {
		return fmt.Errorf("invalid irq: %d", irq)
	}

	if cpu < 0 {
		return fmt.Errorf("invalid cpu: %d", cpu)
	}

	cs := CPUSet{true, make(map[int]struct{})}
	err := cs.AddInt64(cpu)
	if err != nil {
		return fmt.Errorf("failed to add cpu: %d to cpuset, err %v", cpu, err)
	}

	if err = procm.ApplyProcInterrupts(irq, cs.String()); err != nil {
		return fmt.Errorf("failed to apply proc interrupts, err %v", err)
	}

	return nil
}

func GetIrqAffinityCPUs(irq int) ([]int64, error) {
	if irq <= 0 {
		return nil, fmt.Errorf("invalid irq: %d", irq)
	}

	irqProcDir := fmt.Sprintf("%s/%d", IrqRootPath, irq)
	if _, err := os.Stat(irqProcDir); err != nil && os.IsNotExist(err) {
		return nil, fmt.Errorf("%d is not exist", irq)
	}

	smpAffinityListPath := path.Join(irqProcDir, "smp_affinity_list")
	cpuList, err := general.ParseLinuxListFormatFromFile(smpAffinityListPath)
	if err != nil {
		return nil, fmt.Errorf("failed to ParseLinuxListFormatFromFile(%s), err %v", smpAffinityListPath, err)
	}
	return cpuList, nil
}

func setNicRxQueueRPS(nic *NicBasicInfo, queue int, rpsConf string) error {
	if queue < 0 {
		return fmt.Errorf("invalid rx queue %d", queue)
	}

	nsc, err := netnsEnter(nic.NetNSInfo)
	if err != nil {
		return fmt.Errorf("failed to netnsEnter(%s), err %v", nic.NSName, err)
	}
	defer nsc.netnsExit()

	if err = sysm.SetNicRxQueueRPS(nsc.sysMountDir, nic.Name, queue, rpsConf); err != nil {
		return fmt.Errorf("failed to set nic %s rx queue %d rps to %v, sys mount dir: %v, err %v", nic.Name, queue, rpsConf, nsc.sysMountDir, err)
	}

	return nil
}

func SetNicRxQueueRPS(nic *NicBasicInfo, queue int, destCpus []int64) error {
	// calculate rps
	rpsConf, _ := general.ConvertIntSliceToBitmapString(destCpus)

	return setNicRxQueueRPS(nic, queue, rpsConf)
}

func ClearNicRxQueueRPS(nic *NicBasicInfo, queue int) error {
	return setNicRxQueueRPS(nic, queue, "0")
}

func IsZeroBitmap(bitmapStr string) bool {
	fields := strings.Split(bitmapStr, ",")
	for _, field := range fields {
		val, err := strconv.ParseInt(field, 16, 64)
		if err != nil {
			return false
		}
		if val != 0 {
			return false
		}
	}

	return true
}

func ComparesHexBitmapStrings(a string, b string) bool {
	bitmapAFields := strings.Split(a, ",")
	bitmapBFields := strings.Split(b, ",")

	cmpLen := len(bitmapAFields)
	if len(bitmapBFields) < cmpLen {
		cmpLen = len(bitmapBFields)
	}

	aLastIndex := len(bitmapAFields) - 1
	bLastIndex := len(bitmapBFields) - 1
	for i := 0; i < cmpLen; i++ {
		if bitmapAFields[aLastIndex-i] != bitmapBFields[bLastIndex-i] {
			return false
		}
	}

	if len(bitmapAFields) > cmpLen {
		remainder := len(bitmapAFields) - cmpLen
		for i := 0; i < remainder; i++ {
			val, err := strconv.ParseInt(bitmapAFields[i], 16, 64)
			if err != nil {
				return false
			}
			if val != 0 {
				return false
			}
		}
	}

	if len(bitmapBFields) > cmpLen {
		remainder := len(bitmapBFields) - cmpLen
		for i := 0; i < remainder; i++ {
			val, err := strconv.ParseInt(bitmapBFields[i], 16, 64)
			if err != nil {
				return false
			}
			if val != 0 {
				return false
			}
		}
	}

	return true
}

func GetNicRxQueueRpsConf(nic *NicBasicInfo, queue int) (string, error) {
	nsc, err := netnsEnter(nic.NetNSInfo)
	if err != nil {
		return "", fmt.Errorf("failed to netnsEnter(%s), err %v", nic.NSName, err)
	}
	defer nsc.netnsExit()

	return sysm.GetNicRxQueueRPS(nsc.sysMountDir, nic.Name, queue)
}

func GetNicRxQueuesRpsConf(nic *NicBasicInfo) (map[int]string, error) {
	if nic.QueueNum <= 0 {
		return nil, fmt.Errorf("invalid queue number %d", nic.QueueNum)
	}

	nsc, err := netnsEnter(nic.NetNSInfo)
	if err != nil {
		return nil, fmt.Errorf("failed to netnsEnter(%s), err %v", nic.NSName, err)
	}
	defer nsc.netnsExit()

	rxQueuesRpsConf := make(map[int]string)
	for i := 0; i < nic.QueueNum; i++ {
		conf, err := sysm.GetNicRxQueueRPS(nsc.sysMountDir, nic.Name, i)
		if err != nil {
			return nil, err
		}
		rxQueuesRpsConf[i] = conf
	}

	return rxQueuesRpsConf, nil
}

// GetNicQueue2IrqWithQueueFilter get the queue to irq map with queue filter
// the "same" queue may match multiple irqs, e.g., nics with the same name from different netns and sriov vfs
func GetNicQueue2IrqWithQueueFilter(nicInfo *NicBasicInfo, queueFilter string, queueDelimeter string) (map[int]int, error) {
	nicAllIrqs := make(map[int]interface{})
	for _, irq := range nicInfo.Irqs {
		nicAllIrqs[irq] = nil
	}

	b, err := os.ReadFile(InterruptsFile)
	if err != nil {
		return nil, fmt.Errorf("failed to ReadFile(%s), err %s", InterruptsFile, err)
	}

	lines := strings.Split(string(b), "\n")
	queue2Irq := make(map[int]int)

	for _, line := range lines {
		if len(line) == 0 {
			continue
		}

		cols := strings.Fields(line)
		if len(cols) == 0 {
			continue
		}

		queueStr := cols[len(cols)-1]

		if !strings.Contains(queueStr, queueFilter) {
			continue
		}

		irq, err := strconv.Atoi(strings.TrimSuffix(cols[0], ":"))
		if err != nil {
			klog.Warningf("failed to parse irq number, err %s", err)
			continue
		}

		// some nics may not have /sys/class/net/<xxx>/device/msi_irqs, so its corresponding NicBasicInfo.Irqs is empty
		if len(nicAllIrqs) > 0 {
			if _, ok := nicAllIrqs[irq]; !ok {
				continue
			}
		}

		queueCols := strings.Split(queueStr, queueDelimeter)
		if len(queueCols) < 2 {
			continue
		}

		findQueue := -1
		for _, col := range queueCols {
			queue, err := strconv.Atoi(col)
			if err == nil {
				findQueue = queue
				break
			}
		}
		if findQueue >= 0 {
			if i, ok := queue2Irq[findQueue]; ok {
				klog.Errorf("%s: %d: %s queue %d has multiple irqs {%d, %d}", nicInfo.NSName, nicInfo.IfIndex, nicInfo.Name, findQueue, i, irq)
				continue
			}
			queue2Irq[findQueue] = irq
			continue
		}

		if queueFilter == nicInfo.PCIAddr {
			if !strings.HasPrefix(queueCols[0], "mlx5_comp") {
				continue
			}

			var numArray []byte
			byteArray := []byte(queueCols[0])

			for i := len(byteArray) - 1; i >= 0; i-- {
				b := byteArray[i]
				if b >= '0' && b <= '9' {
					numArray = append([]byte{b}, numArray...)
				} else {
					break
				}
			}

			if len(numArray) == 0 {
				continue
			}

			queue, err := strconv.Atoi(string(numArray))
			if err != nil {
				continue
			}

			if i, ok := queue2Irq[queue]; ok {
				klog.Errorf("%s: %d: %s queue %d has multiple irqs {%d, %d}", nicInfo.NSName, nicInfo.IfIndex, nicInfo.Name, queue, i, irq)
				continue
			}
			queue2Irq[queue] = irq
		}
	}

	queueCount := len(queue2Irq)
	for queue := range queue2Irq {
		if queue >= queueCount {
			return nil, fmt.Errorf("%s: %d: %s queue %d greater-equal queue count %d", nicInfo.NSName, nicInfo.IfIndex, nicInfo.Name, queue, queueCount)
		}
	}

	return queue2Irq, nil
}

// GetNicQueue2Irq get nic queue naming in /proc/interrrupts
func GetNicQueue2Irq(nicInfo *NicBasicInfo) (map[int]int, map[int]int, error) {
	if nicInfo.IsVirtioNetDev {
		queueFilter := fmt.Sprintf("%s-input", nicInfo.VirtioNetName)
		queueDelimeter := "."

		queue2Irq, err := GetNicQueue2IrqWithQueueFilter(nicInfo, queueFilter, queueDelimeter)
		if err != nil {
			return nil, nil, fmt.Errorf("GetNicQueue2Irqs(%s, %s), err %v", queueFilter, queueDelimeter, err)
		}

		if len(queue2Irq) <= 0 {
			return nil, nil, fmt.Errorf("failed to find matched input queue in %s for virtio nic %d: %s, virtio name: %s", InterruptsFile, nicInfo.IfIndex, nicInfo.Name, nicInfo.VirtioNetName)
		}

		queueFilter = fmt.Sprintf("%s-output", nicInfo.VirtioNetName)
		txQueue2Irq, err := GetNicQueue2IrqWithQueueFilter(nicInfo, queueFilter, queueDelimeter)
		if err != nil {
			return nil, nil, fmt.Errorf("GetNicQueue2Irqs(%s, %s), err %v", queueFilter, queueDelimeter, err)
		}

		if len(txQueue2Irq) <= 0 {
			return nil, nil, fmt.Errorf("failed to find matched output queue in %s for virtio nic %d: %s, virtio name: %s", InterruptsFile, nicInfo.IfIndex, nicInfo.Name, nicInfo.VirtioNetName)
		}

		return queue2Irq, txQueue2Irq, nil
	}

	txQueue2Irq := make(map[int]int)
	queueFilter := nicInfo.Name
	queueDelimeter := "-"

	queue2Irq, err := GetNicQueue2IrqWithQueueFilter(nicInfo, queueFilter, queueDelimeter)
	if err != nil {
		return nil, nil, fmt.Errorf("GetNicQueue2Irqs(%s, %s), err %v", queueFilter, queueDelimeter, err)
	}

	if len(queue2Irq) > 0 {
		return queue2Irq, txQueue2Irq, nil
	}

	// some mlx driver version naming queue in /proc/interrupts as format mlx5_comp<queue number>@pci:<pci addr>, like mlx5_comp52@pci:0000:5e:00.0
	if nicInfo.PCIAddr != "" {
		queueFilter = nicInfo.PCIAddr
		queueDelimeter = "@"

		queue2Irq, err := GetNicQueue2IrqWithQueueFilter(nicInfo, queueFilter, queueDelimeter)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to GetNicQueue2IrqWithQueueFilter(%s, %s), err %v", queueFilter, queueDelimeter, err)
		}

		if len(queue2Irq) > 0 {
			return queue2Irq, txQueue2Irq, nil
		}
	}

	// mlx/bnx sriov naming vf name as ethx_y, like eth0_6, vf's queue name still use ethx, but the same PF's all VFs share the same queue name in /proc/interrupts
	cols := strings.Split(nicInfo.Name, "_")
	if len(cols) == 2 {
		queueFilter = cols[0]
		queueDelimeter = "-"

		queue2Irq, err := GetNicQueue2IrqWithQueueFilter(nicInfo, queueFilter, queueDelimeter)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to GetNicQueue2IrqWithQueueFilter(%s, %s), err %v", queueFilter, queueDelimeter, err)
		}

		if len(queue2Irq) > 0 {
			return queue2Irq, txQueue2Irq, nil
		}
	}

	return nil, nil, fmt.Errorf("failed to find matched queue in %s for %d: %s", InterruptsFile, nicInfo.IfIndex, nicInfo.Name)
}

// GetIrqsAffinityCPUs before irq tuning initialization, one irq's smp_affinity_list may has multiple cpus
// after irq tuning initialization, one irq only affinity one cpu.
func GetIrqsAffinityCPUs(irqs []int) (map[int][]int64, error) {
	irq2CPUs := make(map[int][]int64)

	for _, irq := range irqs {
		cpus, err := GetIrqAffinityCPUs(irq)
		if err != nil {
			return nil, fmt.Errorf("failed to GetIrqAffinityCPUs(%d), err %v", irq, err)
		}
		irq2CPUs[irq] = cpus
	}
	return irq2CPUs, nil
}

// TidyUpNicIrqsAffinityCPUs re-configure irqs's affinity according to irq's cpu assignment(kernel apic_set_affinity -> irq_matrix_alloc ->  matrix_find_best_cpu)
// to make one irq affinity only one cpu.
// TidyUpNicIrqsAffinityCPUs may change irqs's actual affinitied cpu, because we do not know these cpus's affinitied irqs of other devices, include irqs of other nics,
// it's corner case that need to tidy up irqs affinity.
func TidyUpNicIrqsAffinityCPUs(irq2CPUs map[int][]int64) (map[int]int64, error) {
	var irqs []int
	for irq := range irq2CPUs {
		irqs = append(irqs, irq)
	}
	// irq-balance service configure irqs's affinity generally in irq's ascending order
	sort.Ints(irqs)

	cpuAffinitiedIrqsCount := make(map[int64]int)
	irq2CPU := make(map[int]int64)

	for _, irq := range irqs {
		affinityCPU := int64(-1)
		affinityCPUIrqsCount := -1
		cpus := irq2CPUs[irq]
		for _, cpu := range cpus {
			count, ok := cpuAffinitiedIrqsCount[cpu]
			if !ok {
				affinityCPU = cpu
				break
			}
			if affinityCPUIrqsCount == -1 || count < affinityCPUIrqsCount {
				affinityCPU = cpu
				affinityCPUIrqsCount = count
			}
		}

		err := SetIrqAffinity(irq, affinityCPU)
		if err != nil {
			return nil, fmt.Errorf("failed to SetIrqAffinity(%d, %d), err: %v", irq, affinityCPU, err)
		}

		if _, ok := cpuAffinitiedIrqsCount[affinityCPU]; ok {
			cpuAffinitiedIrqsCount[affinityCPU]++
		} else {
			cpuAffinitiedIrqsCount[affinityCPU] = 1
		}
		irq2CPU[irq] = affinityCPU
	}

	return irq2CPU, nil
}

func getNicDriver(nicSysPath string) (NicDriver, error) {
	linkPath := filepath.Join(nicSysPath, "device/driver")

	target, err := os.Readlink(linkPath)
	if err != nil {
		return NicDriverUnknown, fmt.Errorf("failed to read symlink for %s: err %v", linkPath, err)
	}

	driverName := filepath.Base(target)

	var driver NicDriver
	if strings.HasPrefix(driverName, string(NicDriverMLX)) {
		driver = NicDriverMLX
	} else if strings.HasPrefix(driverName, string(NicDriverBNX)) {
		driver = NicDriverBNX
	} else if strings.HasPrefix(driverName, string(NicDriverVirtioNet)) {
		driver = NicDriverVirtioNet
	} else if strings.HasPrefix(driverName, string(NicDriverI40E)) {
		driver = NicDriverI40E
	} else if strings.HasPrefix(driverName, string(NicDriverIXGBE)) {
		driver = NicDriverIXGBE
	} else {
		driver = NicDriverUnknown
	}

	return driver, nil
}

// GetNicIrqs get nic's all irqs, including rx-tx irqs, and some irqs used for control
func GetNicIrqs(nicSysPath string) ([]int, error) {
	msiIrqsDir := filepath.Join(nicSysPath, "device/msi_irqs")

	dirEnts, err := os.ReadDir(msiIrqsDir)
	if err != nil {
		return nil, fmt.Errorf("failed to ReadDir(%s), err %v", msiIrqsDir, err)
	}

	var irqs []int
	for _, d := range dirEnts {
		irq, err := strconv.Atoi(d.Name())
		if err != nil {
			klog.Warningf("failed to Atoi(%s), err %v", d.Name(), err)
			continue
		}
		irqs = append(irqs, irq)
	}
	sort.Ints(irqs)
	return irqs, nil
}

// IsPCIDevice nicSysPath like "/sys/class/net/eth0"
// N.B., bnx sriov vf dose not hava corresponding /sys/class/net/ethx_y/device file
func IsPCIDevice(nicSysPath string) bool {
	devRealPath, err := filepath.EvalSymlinks(nicSysPath)
	if err != nil {
		klog.Warningf("failed to EvalSymlinks(%s), err %v", nicSysPath, err)
		return false
	}

	if strings.Contains(devRealPath, "devices/pci") {
		return true
	}
	return false
}

func GetNicPCIAddr(nicSysPath string) (string, error) {
	nicDevSysPath := filepath.Join(nicSysPath, "device")
	devRealPath, err := filepath.EvalSymlinks(nicDevSysPath)
	if err != nil {
		return "", fmt.Errorf("failed to EvalSymlinks(%s), err %v", nicDevSysPath, err)
	}
	if IsVirtioNetDevice(nicSysPath) {
		return filepath.Base(filepath.Dir(devRealPath)), nil
	}
	return filepath.Base(devRealPath), nil
}

func IsVirtioNetDevice(nicSysPath string) bool {
	nicDrvSysPath := filepath.Join(nicSysPath, "device/driver")
	nicDrvSysPath, err := filepath.EvalSymlinks(nicDrvSysPath)
	if err != nil {
		klog.Warningf("failed to EvalSymlinks(%s), err %v", nicDrvSysPath, err)
		return false
	}
	if filepath.Base(nicDrvSysPath) == VirtioNetDriver {
		return true
	}

	return false
}

func GetNicVirtioName(nicSysPath string) (string, error) {
	nicDevSysPath := filepath.Join(nicSysPath, "device")
	devRealPath, err := filepath.EvalSymlinks(nicDevSysPath)
	if err != nil {
		return "", fmt.Errorf("failed to EvalSymlinks(%s), err %v", nicDevSysPath, err)
	}
	return filepath.Base(devRealPath), nil
}

// GetNicNumaNode get nic's numa node, /sys/class/net/ethX/device/numa_node not exist, or contains negative value, then return -1 as unknown socket bind
func GetNicNumaNode(nicSysPath string) (int, error) {
	nicBindNumaPath := filepath.Join(nicSysPath, "device/numa_node")
	if _, err := os.Stat(nicBindNumaPath); err != nil {
		// no /sys/class/net/ethX/device/numa_node file for virtio-net device, but numa_node can be acquired by general method
		// that read pci device's numa_node, like "/sys/devices/pci0000:16/0000:16:02.0/numa_node", which is available for all PCI devices.
		if os.IsNotExist(err) {
			nicDevPath := filepath.Join(nicSysPath, "device")
			devRealPath, err := filepath.EvalSymlinks(nicDevPath)
			if err != nil {
				return UnknownNumaNode, fmt.Errorf("failed to EvalSymlinks(%s), err %s", nicDevPath, err)
			}

			nicBindNumaPath = filepath.Join(filepath.Dir(devRealPath), "numa_node")
			if _, err := os.Stat(nicBindNumaPath); err != nil {
				return UnknownNumaNode, err
			}
		} else {
			return UnknownNumaNode, fmt.Errorf("failed to Stat(%s), err %v", nicBindNumaPath, err)
		}
	}

	b, err := os.ReadFile(nicBindNumaPath)
	if err != nil {
		return UnknownNumaNode, fmt.Errorf("failed to ReadFile(%s), err %v", nicBindNumaPath, err)
	}

	numaStr := strings.TrimSpace(strings.TrimRight(string(b), "\n"))
	numa, err := strconv.Atoi(numaStr)
	if err != nil {
		return UnknownNumaNode, fmt.Errorf("failed to Atoi(%s), err %v", numaStr, err)
	}

	// -1 storeed in /sys/class/net/device/numa_node for passthrough non-virtio device (e.g. mellanox SRIOV VF)
	if numa < 0 {
		return UnknownNumaNode, nil
	}

	return numa, nil
}

func GetIfIndex(nicName string, nicSysPath string) (int, error) {
	iface, err := net.InterfaceByName(nicName)
	if err != nil {
		klog.Warningf("failed to InterfaceByName(%s), err %v", nicName, err)
		ifIndex, err := GetIfIndexFromSys(nicSysPath)
		if err != nil {
			return -1, fmt.Errorf("failed to GetIfIndexFromSys(%s), err %v", nicSysPath, err)
		}
		return ifIndex, err
	}
	return iface.Index, nil
}

func GetIfIndexFromSys(nicSysPath string) (int, error) {
	ifIndexPath := filepath.Join(nicSysPath, "ifindex")
	b, err := os.ReadFile(ifIndexPath)
	if err != nil {
		return 0, err
	}

	ifindex, err := strconv.Atoi(strings.TrimSpace(strings.TrimRight(string(b), "\n")))
	if err != nil {
		return 0, err
	}

	return ifindex, nil
}

func IsBondingNetDevice(nicSysPath string) bool {
	if stat, err := os.Stat(filepath.Join(nicSysPath, "bonding")); err == nil && stat.IsDir() {
		return true
	}
	return false
}

func IsNetDevLinkUp(nicSysPath string) bool {
	nicLinkStatusFile := filepath.Join(nicSysPath, "operstate")
	b, err := os.ReadFile(nicLinkStatusFile)
	if err != nil {
		klog.Warningf("failed to ReadFile(%s), err %v", nicLinkStatusFile, err)
		return false
	}

	status := strings.TrimSpace(strings.TrimRight(string(b), "\n"))

	return status == "up"
}

func ListBondNetDevSlaves(nicSysPath string) ([]string, error) {
	if !IsBondingNetDevice(nicSysPath) {
		return nil, fmt.Errorf("%s is not bonding netdev", nicSysPath)
	}

	bondSlavesPath := filepath.Join(nicSysPath, "bonding/slaves")
	b, err := os.ReadFile(bondSlavesPath)
	if err != nil {
		return nil, fmt.Errorf("failed to ReadFile(%s), err %v", bondSlavesPath, err)
	}

	slaves := strings.Fields(strings.TrimRight(string(b), "\n"))

	return slaves, nil
}

func ListNetNS(netNSDir string) ([]NetNSInfo, error) {
	hostNetNSInode, err := process.GetProcessNameSpaceInode(1, process.NetNS)
	if err != nil {
		return nil, fmt.Errorf("failed to GetProcessNameSpaceInode(1, %s), err %v", process.NetNS, err)
	}

	if netNSDir == "" {
		netNSDir = DefaultNetNSDir
	}

	nsList := []NetNSInfo{
		{
			NSName:   DefaultNICNamespace,
			NSInode:  hostNetNSInode,
			NSAbsDir: netNSDir,
		},
	}

	dirEnts, err := os.ReadDir(netNSDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nsList, nil
		}
		return nil, err
	}

	for _, d := range dirEnts {
		if !d.IsDir() {
			netnsPath := filepath.Join(netNSDir, d.Name())
			inode, err := general.GetFileInode(netnsPath)
			if err != nil {
				return nil, fmt.Errorf("failed to GetFileInode(%s), err %v", netnsPath, err)
			}

			nsList = append(nsList, NetNSInfo{
				NSName:   d.Name(),
				NSInode:  inode,
				NSAbsDir: netNSDir,
			})
		}
	}

	return nsList, nil
}

func netnsEnter(netnsInfo NetNSInfo) (*netnsSwitchContext, error) {
	var (
		originalNetNSHdl netns.NsHandle
		newNetNSHdl      netns.NsHandle
		err              error
	)

	// Must lock the OS thread when switching netns
	runtime.LockOSThread()

	defer func() {
		if err != nil {
			runtime.UnlockOSThread()
		}
	}()

	if netnsInfo.NSName == DefaultNICNamespace {
		return &netnsSwitchContext{
			newNetNSName: netnsInfo.NSName,
			sysMountDir:  DefaultNetNSSysDir,
		}, nil
	}

	netnsMutex.Lock()
	defer func() {
		if err != nil {
			netnsMutex.Unlock()
		}
	}()

	originalNetNSHdl, err = netns.Get()
	if err != nil {
		return nil, fmt.Errorf("failed to netns.Get, err %v", err)
	}

	defer func() {
		if err != nil {
			originalNetNSHdl.Close()
		}
	}()

	newNetNSPath := netnsInfo.GetNetNSAbsPath()
	newNetNSHdl, err = netns.GetFromPath(newNetNSPath)
	if err != nil {
		return nil, fmt.Errorf("failed to GetFromPath(%s), err %v", newNetNSPath, err)
	}

	defer func() {
		if err != nil {
			newNetNSHdl.Close()
		}
	}()

	if err = netns.Set(newNetNSHdl); err != nil {
		return nil, fmt.Errorf("failed to setns to %s, err %v", netnsInfo.NSName, err)
	}

	// Ensure we switch back to original netns on failure
	defer func() {
		if err != nil {
			if restoreErr := netns.Set(originalNetNSHdl); restoreErr != nil {
				klog.Fatalf("failed to restore original netns: %v", restoreErr)
			}
		}
	}()

	// Ensure /tmp/netns_sys exists
	if _, err = os.Stat(TmpNetNSSysDir); err != nil {
		if os.IsNotExist(err) {
			if err = os.MkdirAll(TmpNetNSSysDir, os.FileMode(0o755)); err != nil {
				return nil, fmt.Errorf("failed to create %s, err %v", TmpNetNSSysDir, err)
			}
		} else {
			return nil, fmt.Errorf("failed to stat %s, err %v", TmpNetNSSysDir, err)
		}
	}

	// Mount sysfs into the new netns
	var mounted bool
	mounted, err = mountinfo.Mounted(TmpNetNSSysDir)
	if err != nil {
		return nil, fmt.Errorf("check mounted dir: %s failed with error: %v", TmpNetNSSysDir, err)
	}
	if mounted {
		netSysDir := filepath.Join(TmpNetNSSysDir, ClassNetBasePath)
		if _, statErr := os.Stat(netSysDir); statErr == nil {
			klog.Warningf("sysfs already mounted at %s, maybe leaked", TmpNetNSSysDir)
		} else {
			klog.Warningf("failed to stat %s, err %v", netSysDir, statErr)
			err = fmt.Errorf("other fs has mounted at %s", TmpNetNSSysDir)
			return nil, err
		}
	} else {
		if err = syscall.Mount("sysfs", TmpNetNSSysDir, "sysfs", 0, ""); err != nil {
			return nil, fmt.Errorf("failed to mount sysfs at %s, err %v", TmpNetNSSysDir, err)
		}
	}

	return &netnsSwitchContext{
		originalNetNSHdl: originalNetNSHdl,
		newNetNSName:     netnsInfo.NSName,
		newNetNSHdl:      newNetNSHdl,
		sysMountDir:      TmpNetNSSysDir,
		sysDirRemounted:  true,
		locked:           true,
	}, nil
}

// netnsExit MUST be called after netnsEnter susccessfully
func (nsc *netnsSwitchContext) netnsExit() {
	defer runtime.UnlockOSThread()

	defer func() {
		if nsc.locked {
			netnsMutex.Unlock()
			nsc.locked = false
		}
	}()

	if nsc.newNetNSName == DefaultNICNamespace {
		return
	}

	// umount tmp sysfs in the new netns
	if nsc.sysDirRemounted && nsc.sysMountDir != "" {
		if err := syscall.Unmount(nsc.sysMountDir, 0); err != nil {
			klog.Fatalf("failed to Unmount(%s), err %v", nsc.sysMountDir, err)
		}
	}

	nsc.newNetNSHdl.Close()

	if err := netns.Set(nsc.originalNetNSHdl); err != nil {
		klog.Fatalf("failed to set netns to host netns, err %v", err)
	}
	nsc.originalNetNSHdl.Close()
}

func ListActiveUplinkNicsFromNetNS(netnsInfo NetNSInfo) ([]*NicBasicInfo, error) {
	nsc, err := netnsEnter(netnsInfo)
	if err != nil {
		return nil, fmt.Errorf("failed to netnsEnter(%s), err %v", netnsInfo.NSName, err)
	}
	defer nsc.netnsExit()

	interfacesWithIPAddr, err := ListInterfacesWithIPAddr()
	interfacesWithIPAddrMap := make(map[string]interface{})
	for _, i := range interfacesWithIPAddr {
		interfacesWithIPAddrMap[i] = nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed ListInterfacesWithIPAddr, err %v", err)
	}

	netSysDir := filepath.Join(nsc.sysMountDir, ClassNetBasePath)
	dirEnts, err := os.ReadDir(netSysDir)
	if err != nil {
		return nil, fmt.Errorf("failed to ReadDir(%s), err %v", netSysDir, err)
	}

	var activeUplinkNics []string
	for _, d := range dirEnts {
		nicName := d.Name()
		nicSysPath := filepath.Join(netSysDir, nicName)

		if _, ok := interfacesWithIPAddrMap[nicName]; !ok {
			continue
		}

		if IsPCIDevice(nicSysPath) && IsNetDevLinkUp(nicSysPath) {
			activeUplinkNics = append(activeUplinkNics, nicName)
			continue
		}

		if IsBondingNetDevice(nicSysPath) {
			slaves, err := ListBondNetDevSlaves(nicSysPath)
			if err != nil {
				klog.Warningf("failed to ListBondNetDevSlaves(%s), err %v", nicSysPath, err)
				continue
			}

			// bonding's all link-up slaves are considered as avtive uplink nics regardless of bonding's mode
			for _, slave := range slaves {
				slaveSysPath := filepath.Join(netSysDir, slave)
				if IsPCIDevice(slaveSysPath) && IsNetDevLinkUp(slaveSysPath) {
					activeUplinkNics = append(activeUplinkNics, slave)
				}
			}
			continue
		}
	}

	var nics []*NicBasicInfo
	for _, n := range activeUplinkNics {
		nicSysPath := filepath.Join(netSysDir, n)

		ifIndex, err := GetIfIndex(n, nicSysPath)
		if err != nil {
			return nil, fmt.Errorf("failed to GetIfIndex(%s), err %v", n, err)
		}

		pciAddr, err := GetNicPCIAddr(nicSysPath)
		if err != nil {
			return nil, fmt.Errorf("failed to GetNicPCIAddr(%s), err %v", nicSysPath, err)
		}

		driver, err := getNicDriver(nicSysPath)
		if err != nil {
			return nil, fmt.Errorf("failed to getNicDriver(%s), err %v", nicSysPath, err)
		}

		numaNode, err := GetNicNumaNode(nicSysPath)
		if err != nil {
			return nil, fmt.Errorf("failed to GetNicNumaNode(%s), err %v", nicSysPath, err)
		}

		isVirtioNetDev := IsVirtioNetDevice(nicSysPath)
		virtioNetDevName := ""
		if isVirtioNetDev {
			virtioNetDevName, err = GetNicVirtioName(nicSysPath)
			if err != nil {
				return nil, fmt.Errorf("failed to GetNicVirtioName(%s), err %v", nicSysPath, err)
			}
		}

		irqs, err := GetNicIrqs(nicSysPath)
		if err != nil {
			// if there are two active uplink nics from different netns with the same nic name,
			// then we cannot tell apart two nic's irqs in /proc/interrupts,
			// we can use irqs from /sys/class/net/ethX/device/msi_irqs to resolve above confilicts in /proc/interrupts,
			// e.g., two nics with the same name "eth0", we can filter all eth0 irqs in /proc/interrupts, then further filter irq based on nic's msi irqs.
			// so even though get nic's msi irqs failed, needless to exit, print warning log is enough
			if !isVirtioNetDev {
				klog.Warningf("failed to GetNicIrqs(%s), err %v", nicSysPath, err)
			}
		}

		nicInfo := &NicBasicInfo{
			InterfaceInfo: InterfaceInfo{
				NetNSInfo: netnsInfo,
				Name:      n,
				IfIndex:   ifIndex,
				PCIAddr:   pciAddr,
				NumaNode:  numaNode,
			},
			Driver:         driver,
			IsVirtioNetDev: isVirtioNetDev,
			VirtioNetName:  virtioNetDevName,
			Irqs:           irqs,
		}
		nics = append(nics, nicInfo)
	}

	return nics, nil
}

// ListInterfacesWithIPAddr list interfaces with ip address
func ListInterfacesWithIPAddr() ([]string, error) {
	interfaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}

	var intfs []string

	for _, i := range interfaces {
		// if the interface is down or a loopback interface, we just skip it
		if i.Flags&net.FlagUp == 0 || i.Flags&net.FlagLoopback > 0 {
			continue
		}

		addrs, err := i.Addrs()
		if err != nil {
			continue
		}

		if len(addrs) == 0 {
			continue
		}

		for _, addr := range addrs {
			var ip net.IP
			switch v := addr.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			default:
				continue
			}

			// filter out ips that are not global uni-cast
			if !ip.IsGlobalUnicast() {
				continue
			}

			intfs = append(intfs, i.Name)
			break
		}
	}

	return intfs, nil
}

func ListActiveUplinkNics(netNSDir string) ([]*NicBasicInfo, error) {
	netnsList, err := ListNetNS(netNSDir)
	if err != nil {
		return nil, fmt.Errorf("failed to ListNetNS, err %v", err)
	}

	var nics []*NicBasicInfo
	for _, ns := range netnsList {
		netnsNics, err := ListActiveUplinkNicsFromNetNS(ns)
		if err != nil {
			return nil, fmt.Errorf("failed to ListActiveUplinkNicsFromNetNS(%s), err %v", ns.NSName, err)
		}

		for _, n := range netnsNics {
			queue2Irq, txQueue2irq, err := GetNicQueue2Irq(n)
			if err != nil {
				return nil, fmt.Errorf("failed to GetNicQueue2Irq for %d: %s, err %v", n.IfIndex, n.Name, err)
			}

			irq2Queue := make(map[int]int)
			for queue, irq := range queue2Irq {
				irq2Queue[irq] = queue
			}

			txIrq2Queue := make(map[int]int)
			for queue, irq := range queue2Irq {
				txIrq2Queue[irq] = queue
			}

			n.QueueNum = len(queue2Irq)
			n.Queue2Irq = queue2Irq
			n.Irq2Queue = irq2Queue
			n.TxQueue2Irq = txQueue2irq
			n.TxIrq2Queue = txIrq2Queue
			nics = append(nics, n)
		}
	}

	return nics, nil
}

func GetNetDevRxPackets(nic *NicBasicInfo) (uint64, error) {
	nsc, err := netnsEnter(nic.NetNSInfo)
	if err != nil {
		return 0, fmt.Errorf("failed to netnsEnter(%s), err %v", nic.NSName, err)
	}
	defer nsc.netnsExit()

	sysRxPacketsFile := filepath.Join(nsc.sysMountDir, ClassNetBasePath, nic.Name, "statistics/rx_packets")
	if _, err := os.Stat(sysRxPacketsFile); err == nil {
		rxPacket, err := general.ReadUint64FromFile(sysRxPacketsFile)
		if err == nil {
			return rxPacket, nil
		}
		klog.Errorf("failed to ReadUint64FromFile(%s), err %v", sysRxPacketsFile, err)
	} else {
		klog.Warningf("failed to stat(%s), err %v", sysRxPacketsFile, err)
	}

	// read rx packets from /proc/net/dev
	netDevs, err := procm.GetNetDev()
	if err != nil {
		return 0, fmt.Errorf("failed to GetNetDev(%s), err %v", nic.NSName, err)
	}
	rxPacket, ok := netDevs[nic.Name]
	if !ok {
		return 0, fmt.Errorf("failed to find %s in %s", nic.Name, NetDevProcFile)
	}

	return rxPacket.RxPackets, nil
}

func GetNicRxQueuePackets(nic *NicBasicInfo) (map[int]uint64, error) {
	driver := nic.Driver
	if driver != NicDriverMLX && driver != NicDriverBNX && driver != NicDriverVirtioNet &&
		driver != NicDriverI40E && driver != NicDriverIXGBE {
		return nil, fmt.Errorf("unknow driver: %s", driver)
	}

	nsc, err := netnsEnter(nic.NetNSInfo)
	if err != nil {
		return nil, fmt.Errorf("failed to netnsEnter(%s), err %v", nic.NSName, err)
	}
	defer nsc.netnsExit()

	// Create a new Ethtool instance
	ethHandler, err := ethtool.NewEthtool()
	if err != nil {
		return nil, err
	}
	defer ethHandler.Close()

	// Get statistics for the interface
	stats, err := ethHandler.Stats(nic.Name)
	if err != nil {
		return nil, err
	}

	rxQueuePackets := make(map[int]uint64)

	for key, val := range stats {
		var queueStr string
		key = strings.TrimSpace(key)

		if driver == NicDriverMLX {
			// mlx nic rx queue packets key:val format
			// key: rx60_packets
			// val: 89462756888
			if !strings.HasPrefix(key, "rx") {
				continue
			}

			if !strings.HasSuffix(key, "_packets") {
				continue
			}

			queueStr = strings.TrimSuffix(strings.TrimPrefix(key, "rx"), "_packets")
		} else if driver == NicDriverBNX {
			// bnxt nic rx queue packets key:val format
			// key: [6]: rx_ucast_packets
			// val: 8304
			cols := strings.Fields(key)
			if len(cols) != 2 {
				continue
			}

			if cols[1] != "rx_ucast_packets" {
				continue
			}

			queueStr = strings.TrimSuffix(strings.TrimPrefix(cols[0], "["), "]:")
		} else if driver == NicDriverVirtioNet {
			// virtio_net nic rx queue packets key:val format
			// key: rx_queue_6_packets
			// val: 51991567584
			if !strings.HasPrefix(key, "rx_queue_") {
				continue
			}

			if !strings.HasSuffix(key, "_packets") {
				continue
			}

			queueStr = strings.TrimSuffix(strings.TrimPrefix(key, "rx_queue_"), "_packets")
		} else if driver == NicDriverI40E {
			// i40e nic rx queue packets key:val format
			// key: rx-39.packets
			// val: 2769830722
			if !strings.HasPrefix(key, "rx-") {
				continue
			}

			if !strings.HasSuffix(key, ".packets") {
				continue
			}

			queueStr = strings.TrimSuffix(strings.TrimPrefix(key, "rx-"), ".packets")
		} else if driver == NicDriverIXGBE {
			// ixgbe nic rx queue packets key:val format
			// key: rx_queue_19_packets
			// val: 3994706599
			if !strings.HasPrefix(key, "rx_queue_") {
				continue
			}

			if !strings.HasSuffix(key, "_packets") {
				continue
			}

			queueStr = strings.TrimSuffix(strings.TrimPrefix(key, "rx_queue_"), "_packets")
		}

		queue, err := strconv.Atoi(queueStr)
		if err != nil {
			continue
		}

		if queue < 0 || queue >= nic.QueueNum {
			continue
		}
		rxQueuePackets[queue] = val
	}

	return rxQueuePackets, nil
}

func (n *NicBasicInfo) Equal(other *NicBasicInfo) bool {
	if other == nil {
		return false
	}

	if n.NSName != other.NSName || n.NSInode != other.NSInode || n.Name != other.Name ||
		n.IfIndex != other.IfIndex || n.PCIAddr != other.PCIAddr || n.NumaNode != other.NumaNode ||
		n.IsVirtioNetDev != other.IsVirtioNetDev || n.VirtioNetName != other.VirtioNetName || n.QueueNum != other.QueueNum {
		return false
	}

	if len(n.Irqs) != len(other.Irqs) {
		return false
	}

	sort.Ints(n.Irqs)
	sort.Ints(other.Irqs)
	for i := range n.Irqs {
		if n.Irqs[i] != other.Irqs[i] {
			return false
		}
	}

	if len(n.Queue2Irq) != len(other.Queue2Irq) {
		return false
	}

	for queue, irq := range other.Queue2Irq {
		if i, ok := n.Queue2Irq[queue]; !ok || i != irq {
			return false
		}
	}

	if len(n.Irq2Queue) != len(other.Irq2Queue) {
		return false
	}

	for irq, queue := range other.Irq2Queue {
		if q, ok := n.Irq2Queue[irq]; !ok || q != queue {
			return false
		}
	}

	return true
}

func CompareNics(a, b []*NicBasicInfo) bool {
	if len(a) != len(b) {
		return false
	}

	for i := range a {
		if !a[i].Equal(b[i]) {
			return false
		}
	}
	return true
}

func CollectSoftNetStats(onlineCpus map[int64]bool) (map[int64]*SoftNetStat, error) {
	softnetStats := make(map[int64]*SoftNetStat)

	stats, err := procm.GetNetSoftnetStat()
	if err != nil {
		return nil, err
	}

	onlineCpuIDMax := int64(-1)
	for cpu := range onlineCpus {
		if onlineCpuIDMax == -1 || cpu > onlineCpuIDMax {
			onlineCpuIDMax = cpu
		}
	}

	cpu := int64(0)
	for _, stat := range stats {
		processed := stat.Processed
		timeSqueeze := stat.TimeSqueezed

		matchedCpu := int64(-1)
		for ; cpu <= onlineCpuIDMax; cpu++ {
			if _, ok := onlineCpus[cpu]; ok {
				matchedCpu = cpu
				cpu++
				break
			}
		}

		if matchedCpu == -1 {
			klog.Warningf("failed to find matched cpu")
			continue
		}

		softnetStats[matchedCpu] = &SoftNetStat{
			ProcessedPackets:   uint64(processed),
			TimeSqueezePackets: uint64(timeSqueeze),
		}
	}

	return softnetStats, nil
}

func CollectNetRxSoftirqStats() (map[int64]uint64, error) {
	cpuSoftirqCount := make(map[int64]uint64)

	softirqs, err := procm.GetSoftirqs()
	if err != nil {
		return nil, err
	}

	if len(softirqs.NetRx) == 0 {
		return nil, fmt.Errorf("failed to find NET_RX softirq stats in %s", SoftIrqsFile)
	}

	for cpu, softirq := range softirqs.NetRx {
		cpuSoftirqCount[int64(cpu)] = softirq
	}

	return cpuSoftirqCount, nil
}

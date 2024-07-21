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
	"math"
	"sort"
	"strings"

	info "github.com/google/cadvisor/info/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/klog/v2"

	"github.com/kubewharf/katalyst-core/pkg/config/agent/global"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

// NUMANodeInfo is a map from NUMANode ID to a list of
// CPU IDs associated with that NUMANode.
type NUMANodeInfo map[int]CPUSet

// CPUDetails is a map from CPU ID to Core ID, Socket ID, and NUMA ID.
type CPUDetails map[int]CPUInfo

// CPUTopology contains details of node cpu, where :
// CPU  - logical CPU, cadvisor - thread
// Core - physical CPU, cadvisor - Core
// Socket - socket, cadvisor - Socket
// NUMA Node - NUMA cell, cadvisor - Node
type CPUTopology struct {
	NumCPUs      int
	NumCores     int
	NumDies      int
	NumSockets   int
	NumNUMANodes int
	CPUDetails   CPUDetails
}

type MemoryDetails map[int]uint64

// Equal returns true if the MemoryDetails map is equal to the supplied MemoryDetails
func (d MemoryDetails) Equal(want MemoryDetails) bool {
	if len(d) != len(want) {
		return false
	}

	for k, v := range d {
		if v != want[k] {
			return false
		}
	}

	return true
}

// Clone creates a new MemoryDetails instance with the same content.
func (d MemoryDetails) Clone() MemoryDetails {
	if d == nil {
		return nil
	}

	clone := make(MemoryDetails)
	for key, value := range d {
		clone[key] = value
	}

	return clone
}

// FillNUMANodesWithZero takes a CPUSet containing NUMA node IDs and ensures that each ID is present in MemoryDetails.
// If a NUMA node ID from the CPUSet is not present in the MemoryDetails map, it is added with a value of 0.
// The method returns an updated MemoryDetails map with these changes.
func (d MemoryDetails) FillNUMANodesWithZero(allNUMAs CPUSet) MemoryDetails {
	// Clone the original MemoryDetails map
	updatedDetails := d.Clone()

	// Iterate through all NUMA IDs and ensure they are in the map
	for numaID := range allNUMAs.ToSliceInt() {
		if _, exists := updatedDetails[numaID]; !exists {
			// Add the NUMA ID with a value of 0 if it doesn't exist in the map
			updatedDetails[numaID] = 0
		}
	}

	// Return the updated MemoryDetails map
	return updatedDetails
}

type MemoryTopology struct {
	MemoryDetails MemoryDetails
}

// CPUsPerCore returns the number of logical CPUs
// associated with each core.
func (topo *CPUTopology) CPUsPerCore() int {
	if topo.NumCores == 0 {
		return 0
	}
	return topo.NumCPUs / topo.NumCores
}

// CPUsPerSocket returns the number of logical CPUs
// associated with each socket.
func (topo *CPUTopology) CPUsPerSocket() int {
	if topo.NumSockets == 0 {
		return 0
	}
	return topo.NumCPUs / topo.NumSockets
}

// CPUsPerDie returns the number of logical CPUs are associated with
// each die.
func (topo *CPUTopology) CPUsPerDie() int {
	if topo.NumDies == 0 {
		return 0
	}
	return topo.NumCPUs / topo.NumDies
}

// CPUsPerNuma returns the number of logical CPUs
// associated with each numa node.
func (topo *CPUTopology) CPUsPerNuma() int {
	if topo.NumNUMANodes == 0 {
		return 0
	}
	return topo.NumCPUs / topo.NumNUMANodes
}

// NUMAsPerSocket returns the the number of NUMA
// are associated with each socket.
func (topo *CPUTopology) NUMAsPerSocket() (int, error) {
	numasCount := topo.CPUDetails.NUMANodes().Size()

	if numasCount%topo.NumSockets != 0 {
		return 0, fmt.Errorf("invalid numasCount: %d and socketsCount: %d", numasCount, topo.NumSockets)
	}

	return numasCount / topo.NumSockets, nil
}

// GetSocketTopology parses the given CPUTopology to a mapping
// from socket id to cpu id lists
func (topo *CPUTopology) GetSocketTopology() map[int]string {
	if topo == nil {
		return nil
	}

	socketTopology := make(map[int]string)
	for _, socketID := range topo.CPUDetails.Sockets().ToSliceInt() {
		socketTopology[socketID] = topo.CPUDetails.NUMANodesInSockets(socketID).String()
	}

	return socketTopology
}

func GenerateDummyMachineInfo(numaNum int, memoryCapacityGB int) (*info.MachineInfo, error) {
	machineInfo := &info.MachineInfo{}

	if memoryCapacityGB%numaNum != 0 {
		return nil, fmt.Errorf("invalid memoryCapacityGB: %d and NUMA number: %d", memoryCapacityGB, numaNum)
	}

	perNumaCapacityGB := uint64(memoryCapacityGB / numaNum)
	perNumaCapacityQuantity := resource.MustParse(fmt.Sprintf("%dGi", perNumaCapacityGB))

	machineInfo.Topology = make([]info.Node, 0, numaNum)
	for i := 0; i < numaNum; i++ {
		machineInfo.Topology = append(machineInfo.Topology, info.Node{
			Id:     i,
			Memory: uint64(perNumaCapacityQuantity.Value()),
		})
	}

	return machineInfo, nil
}

func GenerateDummyCPUTopology(cpuNum, socketNum, numaNum int) (*CPUTopology, error) {
	if numaNum%socketNum != 0 {
		return nil, fmt.Errorf("invalid NUMA number: %d and socket number: %d", numaNum, socketNum)
	} else if cpuNum%numaNum != 0 {
		return nil, fmt.Errorf("invalid cpu number: %d and NUMA number: %d", cpuNum, numaNum)
	} else if cpuNum%2 != 0 {
		// assume that we should use hyper-threads
		return nil, fmt.Errorf("invalid cpu number: %d and NUMA number: %d", cpuNum, numaNum)
	}

	cpuTopology := new(CPUTopology)
	cpuTopology.CPUDetails = make(map[int]CPUInfo)
	cpuTopology.NumCPUs = cpuNum
	cpuTopology.NumCores = cpuNum / 2
	cpuTopology.NumSockets = socketNum
	cpuTopology.NumNUMANodes = numaNum

	numaPerSocket := numaNum / socketNum
	cpusPerNUMA := cpuNum / numaNum

	for i := 0; i < socketNum; i++ {
		for j := i * numaPerSocket; j < (i+1)*numaPerSocket; j++ {
			for k := j * (cpusPerNUMA / 2); k < (j+1)*(cpusPerNUMA/2); k++ {
				cpuTopology.CPUDetails[k] = CPUInfo{
					NUMANodeID: j,
					SocketID:   i,
					CoreID:     k,
				}

				cpuTopology.CPUDetails[k+cpuNum/2] = CPUInfo{
					NUMANodeID: j,
					SocketID:   i,
					CoreID:     k,
				}
			}
		}
	}

	return cpuTopology, nil
}

func GenerateDummyMemoryTopology(numaNum int, memoryCapacity uint64) (*MemoryTopology, error) {
	memoryTopology := &MemoryTopology{map[int]uint64{}}
	for i := 0; i < numaNum; i++ {
		memoryTopology.MemoryDetails[i] = memoryCapacity / uint64(numaNum)
	}
	return memoryTopology, nil
}

func GenerateDummyExtraTopology(numaNum int) (*ExtraTopologyInfo, error) {
	var (
		socketNum                 = 2
		distanceNumaInSameSocket  = 11
		distanceNumaInOtherSocket = 21
	)

	extraTopology := &ExtraTopologyInfo{
		NumaDistanceMap: make(map[int][]NumaDistanceInfo),
		SiblingNumaInfo: &SiblingNumaInfo{
			SiblingNumaMap:                  make(map[int]sets.Int),
			SiblingNumaAvgMBWAllocatableMap: make(map[int]int64),
			SiblingNumaAvgMBWCapacityMap:    make(map[int]int64),
		},
	}

	for i := 0; i < numaNum; i++ {
		numaDistanceInfos := make([]NumaDistanceInfo, 0)
		for j := 0; j < numaNum; j++ {
			if i == j {
				continue
			} else if i/socketNum == j/socketNum {
				numaDistanceInfos = append(numaDistanceInfos, NumaDistanceInfo{
					Distance: distanceNumaInSameSocket,
					NumaID:   j,
				})
			} else {
				numaDistanceInfos = append(numaDistanceInfos, NumaDistanceInfo{
					Distance: distanceNumaInOtherSocket,
					NumaID:   j,
				})
			}
		}

		extraTopology.NumaDistanceMap[i] = numaDistanceInfos
		extraTopology.SiblingNumaMap[i] = make(sets.Int)
	}
	return extraTopology, nil
}

// CPUInfo contains the NUMA, socket, and core IDs associated with a CPU.
type CPUInfo struct {
	NUMANodeID int
	SocketID   int
	CoreID     int
	DieID      int
}

// KeepOnly returns a new CPUDetails object with only the supplied cpus.
func (d CPUDetails) KeepOnly(cpus CPUSet) CPUDetails {
	result := CPUDetails{}
	for cpu, info := range d {
		if cpus.Contains(cpu) {
			result[cpu] = info
		}
	}
	return result
}

// NUMANodes returns all NUMANode IDs associated with the CPUs in this CPUDetails.
func (d CPUDetails) NUMANodes() CPUSet {
	b := NewCPUSet()
	for _, info := range d {
		b.Add(info.NUMANodeID)
	}
	return b
}

// NUMANodesInSockets returns all logical NUMANode IDs associated with
// the given socket IDs in this CPUDetails.
func (d CPUDetails) NUMANodesInSockets(ids ...int) CPUSet {
	b := NewCPUSet()
	for _, id := range ids {
		for _, info := range d {
			if info.SocketID == id {
				b.Add(info.NUMANodeID)
			}
		}
	}
	return b
}

// Sockets returns all socket IDs associated with the CPUs in this CPUDetails.
func (d CPUDetails) Sockets() CPUSet {
	b := NewCPUSet()
	for _, info := range d {
		b.Add(info.SocketID)
	}
	return b
}

// CPUsInSockets returns all logical CPU IDs associated with the given
// socket IDs in this CPUDetails.
func (d CPUDetails) CPUsInSockets(ids ...int) CPUSet {
	b := NewCPUSet()
	for _, id := range ids {
		for cpu, info := range d {
			if info.SocketID == id {
				b.Add(cpu)
			}
		}
	}
	return b
}

// SocketsInNUMANodes returns all logical Socket IDs associated with the
// given NUMANode IDs in this CPUDetails.
func (d CPUDetails) SocketsInNUMANodes(ids ...int) CPUSet {
	b := NewCPUSet()
	for _, id := range ids {
		for _, info := range d {
			if info.NUMANodeID == id {
				b.Add(info.SocketID)
			}
		}
	}
	return b
}

// Cores returns all core IDs associated with the CPUs in this CPUDetails.
func (d CPUDetails) Cores() CPUSet {
	b := NewCPUSet()
	for _, info := range d {
		b.Add(info.CoreID)
	}
	return b
}

// CoresInNUMANodes returns all core IDs associated with the given
// NUMANode IDs in this CPUDetails.
func (d CPUDetails) CoresInNUMANodes(ids ...int) CPUSet {
	b := NewCPUSet()
	for _, id := range ids {
		for _, info := range d {
			if info.NUMANodeID == id {
				b.Add(info.CoreID)
			}
		}
	}
	return b
}

// CoresInSockets returns all core IDs associated with the given socket
// IDs in this CPUDetails.
func (d CPUDetails) CoresInSockets(ids ...int) CPUSet {
	b := NewCPUSet()
	for _, id := range ids {
		for _, info := range d {
			if info.SocketID == id {
				b.Add(info.CoreID)
			}
		}
	}
	return b
}

// CoresInDies returns all of the core IDs associated with the given die
// IDs in this CPUDetails.
func (d CPUDetails) CoresInDies(ids ...int) CPUSet {
	b := NewCPUSet()
	for _, id := range ids {
		for _, info := range d {
			if info.DieID == id {
				b.Add(info.CoreID)
			}
		}
	}
	return b
}

// Dies returns all of the die IDs associated with the CPUs in this
// CPUDetails.
func (d CPUDetails) Dies() CPUSet {
	b := NewCPUSet()
	for _, info := range d {
		b.Add(info.DieID)
	}
	return b
}

// DiesInSockets returns all of the die IDs associated with the given socket
// IDs in this CPUDetails.
func (d CPUDetails) DiesInSockets(ids ...int) CPUSet {
	b := NewCPUSet()
	for _, id := range ids {
		for _, info := range d {
			if info.SocketID == id {
				b.Add(info.DieID)
			}
		}
	}
	return b
}

// CPUsInDies returns all of the logical CPU IDs associated with the given
// die IDs in this CPUDetails.
func (d CPUDetails) CPUsInDies(ids ...int) CPUSet {
	b := NewCPUSet()
	for _, id := range ids {
		for cpu, info := range d {
			if info.DieID == id {
				b.Add(cpu)
			}
		}
	}
	return b
}

// CPUs returns all logical CPU IDs in this CPUDetails.
func (d CPUDetails) CPUs() CPUSet {
	b := NewCPUSet()
	for cpuID := range d {
		b.Add(cpuID)
	}
	return b
}

// CPUsInNUMANodes returns all logical CPU IDs associated with the given
// NUMANode IDs in this CPUDetails.
func (d CPUDetails) CPUsInNUMANodes(ids ...int) CPUSet {
	b := NewCPUSet()
	for _, id := range ids {
		for cpu, info := range d {
			if info.NUMANodeID == id {
				b.Add(cpu)
			}
		}
	}
	return b
}

// CPUsInCores returns all logical CPU IDs associated with the given
// core IDs in this CPUDetails.
func (d CPUDetails) CPUsInCores(ids ...int) CPUSet {
	b := NewCPUSet()
	for _, id := range ids {
		for cpu, info := range d {
			if info.CoreID == id {
				b.Add(cpu)
			}
		}
	}
	return b
}

// DieInfo is a map from Die ID to a list of CPU IDs associated with that Die.
type DieInfo map[int]CPUSet

func (d CPUDetails) discoverDie() (DieInfo, error) {
	dieInfo := DieInfo{}

	constructDieInfo := func(thread int) (err error) {
		for _, cpuIDs := range dieInfo {
			if cpuIDs.Contains(thread) {
				return nil
			}
		}
		path := fmt.Sprintf("/sys/devices/system/cpu/cpu%d", thread) + "/cache/index3/shared_cpu_list"
		cpusetByte, err := ioutil.ReadFile(path)
		if err != nil {
			return err
		}

		cpus, err := Parse(strings.TrimSpace(string(cpusetByte)))
		if err != nil {
			return err
		}

		sharedThreads := cpus.ToSliceInt()
		if len(sharedThreads) == 0 {
			return fmt.Errorf("The index3 shared_cpu_list is empty with cpuset:%v for cpu:%v", cpus.String(), thread)
		}
		min := sharedThreads[0]
		dieInfo[min] = cpus
		return nil
	}

	threads := d.CPUs().ToSliceInt()
	for _, thread := range threads {
		err := constructDieInfo(thread)
		if err != nil {
			return nil, err
		}
	}

	all := NewCPUSet()
	for _, v := range dieInfo {
		all = all.Union(v)
	}

	if !all.Equals(d.CPUs()) {
		return nil, fmt.Errorf("construct die info err")
	}
	klog.V(4).Infof("[cpumanager] discover die success")

	return dieInfo, nil
}

// CPUDistributed judge whether the cpu is evenly distributed
func (d CPUDetails) CPUDistributed() bool {
	if len(d) == 0 {
		return false
	}

	// 1. The number of thread on each core is equal
	cores := d.Cores().ToSliceInt()
	elems := map[int]struct{}{}
	for _, i := range cores {
		htNum := d.CPUsInCores(i).Size()
		elems[htNum] = struct{}{}
	}
	if len(elems) > 1 {
		return false
	}

	// 2. The number of core on each die is equal
	dies := d.Dies().ToSliceInt()
	elems = map[int]struct{}{}
	for _, i := range dies {
		coreNum := d.CoresInDies(i).Size()
		elems[coreNum] = struct{}{}
	}
	if len(elems) > 1 {
		return false
	}

	// 3. The number of die on each socket is equal
	sockets := d.Sockets().ToSliceInt()
	elems = map[int]struct{}{}
	for _, i := range sockets {
		dieNum := d.DiesInSockets(i).Size()
		elems[dieNum] = struct{}{}
	}
	if len(elems) > 1 {
		return false
	}

	return true
}

// SortCPUToVCPU sort pcpu， ascending order by vcpu binding orders
func (d CPUDetails) SortCPUToVCPU() []int {
	ids := d.CPUs().ToSliceInt()

	sort.Slice(ids,
		func(i, j int) bool {
			icpu := ids[i]
			jcpu := ids[j]

			iSocket := d[icpu].SocketID
			jSocket := d[jcpu].SocketID
			if iSocket < jSocket {
				return true
			}
			if iSocket > jSocket {
				return false
			}

			iDie := d[icpu].DieID
			jDie := d[jcpu].DieID
			if iDie < jDie {
				return true
			}
			if iDie > jDie {
				return false
			}

			iCore := d[icpu].CoreID
			jCore := d[jcpu].CoreID
			if iCore < jCore {
				return true
			}
			if iCore > jCore {
				return false
			}
			return ids[i] < ids[j]
		})

	return ids
}

// Discover returns CPUTopology based on cadvisor node info
func Discover(machineInfo *info.MachineInfo) (*CPUTopology, *MemoryTopology, error) {
	if machineInfo.NumCores == 0 {
		return nil, nil, fmt.Errorf("could not detect number of cpus")
	}

	CPUDetails := CPUDetails{}
	numPhysicalCores := 0

	memoryTopology := MemoryTopology{MemoryDetails: map[int]uint64{}}

	for _, node := range machineInfo.Topology {
		memoryTopology.MemoryDetails[node.Id] = node.Memory

		numPhysicalCores += len(node.Cores)
		for _, core := range node.Cores {
			if coreID, err := getUniqueCoreID(core.Threads); err == nil {
				for _, cpu := range core.Threads {
					CPUDetails[cpu] = CPUInfo{
						CoreID:     coreID,
						SocketID:   core.SocketID,
						NUMANodeID: node.Id,
						// Die id is equal to numa node id by default,such as intel cpu arch.
						DieID: node.Id,
					}
				}
			} else {
				klog.ErrorS(nil, "Could not get unique coreID for socket",
					"socket", core.SocketID, "core", core.Id, "threads", core.Threads)
				return nil, nil, err
			}
		}
	}

	dieInfo := DieInfo{}
	isAMD, err := CPUIsAMD()
	if err != nil {
		return nil, nil, fmt.Errorf("could not detect cpu type err:%v", err)
	}

	if isAMD {
		info, err := CPUDetails.discoverDie()
		if err != nil {
			return nil, nil, err
		}
		dieInfo = info
	}

	for dieID, threads := range dieInfo {
		for _, v := range threads.ToSliceInt() {
			if info, ok := CPUDetails[v]; ok {
				info.DieID = dieID
				CPUDetails[v] = info
			} else {
				return nil, nil, fmt.Errorf("Thread from die info is not exists in CPUDetails")
			}
		}
	}

	return &CPUTopology{
		NumCPUs:      machineInfo.NumCores,
		NumSockets:   machineInfo.NumSockets,
		NumCores:     numPhysicalCores,
		NumDies:      CPUDetails.Dies().Size(),
		NumNUMANodes: CPUDetails.NUMANodes().Size(),
		CPUDetails:   CPUDetails,
	}, &memoryTopology, nil
}

// getUniqueCoreID computes coreId as the lowest cpuID
// for a given Threads []int slice. This will assure that coreID's are
// platform unique (opposite to what cAdvisor reports)
func getUniqueCoreID(threads []int) (coreID int, err error) {
	if len(threads) == 0 {
		return 0, fmt.Errorf("no cpus provided")
	}

	if len(threads) != NewCPUSet(threads...).Size() {
		return 0, fmt.Errorf("cpus provided are not unique")
	}

	min := threads[0]
	for _, thread := range threads[1:] {
		if thread < min {
			min = thread
		}
	}

	return min, nil
}

// GetNumaAwareAssignments returns a mapping from NUMA id to cpu core
func GetNumaAwareAssignments(topology *CPUTopology, cset CPUSet) (map[int]CPUSet, error) {
	if topology == nil {
		return nil, fmt.Errorf("GetTopologyAwareAssignmentsByCPUSet got nil cpuset")
	}

	topologyAwareAssignments := make(map[int]CPUSet)
	numaNodes := topology.CPUDetails.NUMANodes()
	for _, numaNode := range numaNodes.ToSliceNoSortInt() {
		cs := cset.Intersection(topology.CPUDetails.CPUsInNUMANodes(numaNode).Clone())
		if cs.Size() > 0 {
			topologyAwareAssignments[numaNode] = cs
		}
	}

	return topologyAwareAssignments, nil
}

// CheckNUMACrossSockets judges whether the given NUMA nodes are located
// in different sockets
func CheckNUMACrossSockets(numaNodes []int, cpuTopology *CPUTopology) (bool, error) {
	if cpuTopology == nil {
		return false, fmt.Errorf("CheckNUMACrossSockets got nil cpuTopology")
	}

	if len(numaNodes) <= 1 {
		return false, nil
	}
	return cpuTopology.CPUDetails.SocketsInNUMANodes(numaNodes...).Size() > 1, nil
}

func GetSiblingNumaInfo(conf *global.MachineInfoConfiguration,
	numaDistanceMap map[int][]NumaDistanceInfo,
) *SiblingNumaInfo {
	siblingNumaMap := make(map[int]sets.Int)
	siblingNumaAvgMBWAllocatableMap := make(map[int]int64)
	siblingNumaAvgMBWCapacityMap := make(map[int]int64)

	// calculate the sibling NUMA allocatable memory bandwidth by the capacity multiplying the allocatable rate.
	// Now, all the NUMAs have the same memory bandwidth capacity and allocatable
	siblingNumaMBWCapacity := conf.SiblingNumaMemoryBandwidthCapacity
	siblingNumaMBWAllocatable := int64(float64(siblingNumaMBWCapacity) * conf.SiblingNumaMemoryBandwidthAllocatableRate)

	for numaID, distanceMap := range numaDistanceMap {
		var selfNumaDistance int
		// calculate self NUMA distance and the minimum cross-NUMA distance.
		minCrossNumaDistance := math.MaxInt
		for _, distance := range distanceMap {
			if distance.NumaID == numaID {
				selfNumaDistance = distance.Distance
			} else {
				minCrossNumaDistance = general.Min(distance.Distance, minCrossNumaDistance)
			}
		}
		// the sibling NUMA distance must be no smaller than the distance to itself
		// and no larger than the minimum cross-NUMA distance.
		siblingNumaDistance := general.Min(general.Max(selfNumaDistance, conf.SiblingNumaMaxDistance),
			minCrossNumaDistance)

		siblingSet := sets.NewInt()
		for _, distance := range distanceMap {
			if distance.NumaID == numaID {
				continue
			}

			// the distance between two different NUMAs is equal to the sibling
			// numa distance
			if distance.Distance == siblingNumaDistance {
				siblingSet.Insert(distance.NumaID)
			}
		}

		siblingNumaMap[numaID] = siblingSet
		siblingNumaAvgMBWAllocatableMap[numaID] = siblingNumaMBWAllocatable / int64(len(siblingSet)+1)
		siblingNumaAvgMBWCapacityMap[numaID] = siblingNumaMBWCapacity / int64(len(siblingSet)+1)
	}

	return &SiblingNumaInfo{
		SiblingNumaMap:                  siblingNumaMap,
		SiblingNumaAvgMBWCapacityMap:    siblingNumaAvgMBWCapacityMap,
		SiblingNumaAvgMBWAllocatableMap: siblingNumaAvgMBWAllocatableMap,
	}
}

type NumaDistanceInfo struct {
	NumaID   int
	Distance int
}

type ExtraTopologyInfo struct {
	NumaDistanceMap map[int][]NumaDistanceInfo
	*SiblingNumaInfo
}

type SiblingNumaInfo struct {
	SiblingNumaMap map[int]sets.Int

	// SiblingNumaAvgMBWAllocatableMap maps NUMA IDs to the allocatable memory bandwidth,
	// averaged across each NUMA node and its siblings.
	// SiblingNumaAvgMBWCapacityMap maps NUMA IDs to the capacity memory bandwidth,
	// averaged similarly.
	SiblingNumaAvgMBWAllocatableMap map[int]int64
	SiblingNumaAvgMBWCapacityMap    map[int]int64
}

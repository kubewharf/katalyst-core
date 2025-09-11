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
	"math"
	"sort"

	info "github.com/google/cadvisor/info/v1"
	"golang.org/x/sys/unix"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/klog/v2"

	"github.com/kubewharf/katalyst-core/pkg/config/agent/global"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

// NUMANodeInfo is a map from NUMANode ID to a list of
// CPU IDs associated with that NUMANode.
type NUMANodeInfo map[int]CPUSet

// CPUSizeInNUMAs returns the number of logical CPUs
// associated with each numa nodes.
func (i NUMANodeInfo) CPUSizeInNUMAs(nodes ...int) int {
	cpus := 0
	for _, n := range nodes {
		cpus += i[n].Size()
	}
	return cpus
}

// CPUDetails is a map from CPU ID to Core ID, Socket ID, and NUMA ID.
type CPUDetails map[int]CPUTopoInfo

// CPUTopology contains details of node cpu, where :
// CPU  - logical CPU, cadvisor - thread
// Core - physical CPU, cadvisor - Core
// Socket - socket, cadvisor - Socket
// NUMA Node - NUMA cell, cadvisor - Node
type CPUTopology struct {
	NumCPUs              int
	NumCores             int
	NumSockets           int
	NumNUMANodes         int
	NUMANodeIDToSocketID map[int]int
	NUMAToCPUs           NUMANodeInfo
	CPUDetails           CPUDetails
	CPUInfo              *CPUInfo
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
	PageSize      int
}

// AlignToPageSize returns the page numbers from mem numbers.
func (memTopo *MemoryTopology) AlignToPageSize(memBytes int64) int64 {
	pageSize := int64(memTopo.PageSize)
	return (memBytes + pageSize - 1) / pageSize
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
	cpuTopology.CPUDetails = make(map[int]CPUTopoInfo)
	cpuTopology.NumCPUs = cpuNum
	cpuTopology.NumCores = cpuNum / 2
	cpuTopology.NumSockets = socketNum
	cpuTopology.NumNUMANodes = numaNum
	cpuTopology.NUMANodeIDToSocketID = make(map[int]int, numaNum)

	numaPerSocket := numaNum / socketNum
	cpusPerNUMA := cpuNum / numaNum

	for i := 0; i < socketNum; i++ {
		for j := i * numaPerSocket; j < (i+1)*numaPerSocket; j++ {
			for k := j * (cpusPerNUMA / 2); k < (j+1)*(cpusPerNUMA/2); k++ {
				cpuTopology.CPUDetails[k] = CPUTopoInfo{
					NUMANodeID: j,
					SocketID:   i,
					CoreID:     k,
				}

				cpuTopology.CPUDetails[k+cpuNum/2] = CPUTopoInfo{
					NUMANodeID: j,
					SocketID:   i,
					CoreID:     k,
				}

				cpuTopology.NUMANodeIDToSocketID[j] = i
			}
		}
	}

	numaToCPUs := make(NUMANodeInfo, numaNum)
	for id := range cpuTopology.NUMANodeIDToSocketID {
		numaToCPUs[id] = cpuTopology.CPUDetails.CPUsInNUMANodes(id)
	}
	cpuTopology.NUMAToCPUs = numaToCPUs

	return cpuTopology, nil
}

func GenerateDummyMemoryTopology(numaNum int, memoryCapacity uint64) (*MemoryTopology, error) {
	memoryTopology := &MemoryTopology{map[int]uint64{}, 4096}
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
			SiblingNumaMap:                      make(map[int]sets.Int),
			SiblingNumaAvgMBWAllocatableRateMap: make(map[string]float64),
			SiblingNumaAvgMBWCapacityMap:        make(map[int]int64),
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

// CPUTopoInfo contains the NUMA, socket, and core IDs associated with a CPU.
type CPUTopoInfo struct {
	NUMANodeID int
	SocketID   int
	CoreID     int
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

// Discover returns CPUTopology based on cadvisor node info
func Discover(machineInfo *info.MachineInfo) (*CPUTopology, *MemoryTopology, error) {
	if machineInfo.NumCores == 0 {
		return nil, nil, fmt.Errorf("could not detect number of cpus")
	}

	cpuDetails := CPUDetails{}
	numaNodeIDToSocketID := make(map[int]int, len(machineInfo.Topology))
	numPhysicalCores := 0

	memoryTopology := MemoryTopology{
		MemoryDetails: map[int]uint64{},
		PageSize:      unix.Getpagesize(),
	}

	for _, node := range machineInfo.Topology {
		memoryTopology.MemoryDetails[node.Id] = node.Memory

		numPhysicalCores += len(node.Cores)
		for _, core := range node.Cores {
			if coreID, err := getUniqueCoreID(core.Threads); err == nil {
				for _, cpu := range core.Threads {
					cpuDetails[cpu] = CPUTopoInfo{
						CoreID:     coreID,
						SocketID:   core.SocketID,
						NUMANodeID: node.Id,
					}

					numaNodeIDToSocketID[node.Id] = core.SocketID
				}
			} else {
				klog.ErrorS(nil, "Could not get unique coreID for socket",
					"socket", core.SocketID, "core", core.Id, "threads", core.Threads)
				return nil, nil, err
			}
		}
	}

	cpuInfo, err := GetCPUInfoWithTopo()
	if err != nil {
		return nil, nil, fmt.Errorf("failed to GetCPUInfoWithTopo, err %s", err)
	}

	numNUMANodes := cpuDetails.NUMANodes().Size()
	numaToCPUs := make(NUMANodeInfo, numNUMANodes)
	for id := range numaNodeIDToSocketID {
		numaToCPUs[id] = cpuDetails.CPUsInNUMANodes(id)
	}

	return &CPUTopology{
		NumCPUs:              machineInfo.NumCores,
		NumSockets:           machineInfo.NumSockets,
		NumCores:             numPhysicalCores,
		NumNUMANodes:         numNUMANodes,
		NUMANodeIDToSocketID: numaNodeIDToSocketID,
		NUMAToCPUs:           numaToCPUs,
		CPUDetails:           cpuDetails,
		CPUInfo:              cpuInfo,
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
		cs := cset.Intersection(topology.CPUDetails.CPUsInNUMANodes(numaNode))
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

	visSocketID := -1
	for _, numaNode := range numaNodes {
		socketID, found := cpuTopology.NUMANodeIDToSocketID[numaNode]

		if !found {
			return false, fmt.Errorf("no corresponding SocketID for NUMA: %d", numaNode)
		}

		if visSocketID != -1 && socketID != visSocketID {
			return true, nil
		}

		visSocketID = socketID
	}

	return false, nil
}

func GetExtraTopologyInfo(conf *global.MachineInfoConfiguration, cpuTopology *CPUTopology, extraNetworkInfo *ExtraNetworkInfo) (*ExtraTopologyInfo, error) {
	numaDistanceArray, err := getNUMADistanceMap()
	if err != nil {
		return nil, err
	}

	interfaceSocketInfo, err := GetInterfaceSocketInfo(extraNetworkInfo.GetAllocatableNICs(conf), cpuTopology.CPUDetails.Sockets().ToSliceInt())
	if err != nil {
		return nil, err
	}

	return &ExtraTopologyInfo{
		NumaDistanceMap:                numaDistanceArray,
		SiblingNumaInfo:                GetSiblingNumaInfo(conf, numaDistanceArray),
		AllocatableInterfaceSocketInfo: interfaceSocketInfo,
	}, nil
}

func GetSiblingNumaInfo(
	conf *global.MachineInfoConfiguration,
	numaDistanceMap map[int][]NumaDistanceInfo,
) *SiblingNumaInfo {
	siblingNumaMap := make(map[int]sets.Int)
	siblingNumaAvgMBWCapacityMap := make(map[int]int64)

	siblingNumaMBWCapacity := conf.SiblingNumaMemoryBandwidthCapacity
	siblingNumaMBWAllocatableRateMap := conf.SiblingNumaMemoryBandwidthAllocatableRateMap
	siblingNumaDefaultMBWAllocatableRate := conf.SiblingNumaMemoryBandwidthAllocatableRate

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
		siblingNumaAvgMBWCapacityMap[numaID] = siblingNumaMBWCapacity / int64(len(siblingSet)+1)
	}

	return &SiblingNumaInfo{
		SiblingNumaMap:                       siblingNumaMap,
		SiblingNumaAvgMBWCapacityMap:         siblingNumaAvgMBWCapacityMap,
		SiblingNumaAvgMBWAllocatableRateMap:  siblingNumaMBWAllocatableRateMap,
		SiblingNumaDefaultMBWAllocatableRate: siblingNumaDefaultMBWAllocatableRate,
	}
}

type NumaDistanceInfo struct {
	NumaID   int
	Distance int
}

type ExtraTopologyInfo struct {
	NumaDistanceMap map[int][]NumaDistanceInfo
	*SiblingNumaInfo
	*AllocatableInterfaceSocketInfo
}

type SiblingNumaInfo struct {
	SiblingNumaMap map[int]sets.Int

	// SiblingNumaAvgMBWAllocatableRateMap maps cpu codename to the according memory bandwidth allocatable rate
	// SiblingNumaAvgMBWCapacityMap maps NUMA IDs to the capacity memory bandwidth,
	// averaged across each NUMA node and its siblings.
	SiblingNumaAvgMBWAllocatableRateMap  map[string]float64
	SiblingNumaAvgMBWCapacityMap         map[int]int64
	SiblingNumaDefaultMBWAllocatableRate float64
}

type AllocatableInterfaceSocketInfo struct {
	// IfIndex2Sockets maps allocatable network interface indexes to
	// the sockets they belong to.
	// Socket2IfIndexes maps sockets to the allocatable network interface indexes
	// they contain.
	IfIndex2Sockets  map[int][]int
	Socket2IfIndexes map[int][]int
}

// GetInterfaceSocketInfo assigns network interfaces (NICs) to CPU sockets based on NUMA topology.
//
// It takes a list of available network interfaces (`nics`) and a `cpuTopology` structure.
// The function attempts to distribute NICs evenly across sockets while considering NUMA affinity.
//
// The resulting mappings are:
// - `IfIndex2Sockets`: Maps each NIC index to the socket(s) it is assigned to.
// - `Socket2IfIndexes`: Maps each socket to the NIC indices assigned to it.
//
// The logic follows these steps:
// 1. If there are no sockets, return an error.
// 2. Retrieve available sockets from `cpuTopology`.
// 3. Initialize mappings and track socket usage.
// 4. Compute an ideal maximum number of NICs per socket to balance the distribution.
// 5. Assign NICs to sockets based on:
//   - NUMA affinity when possible.
//   - Least-used socket when NUMA binding is unavailable or overloaded.
//
// 6. Populate the mappings and return them.
func GetInterfaceSocketInfo(nics []InterfaceInfo, sockets []int) (*AllocatableInterfaceSocketInfo, error) {
	// Check if there are available sockets
	if len(sockets) == 0 {
		return nil, fmt.Errorf("no sockets available")
	}

	sort.Ints(sockets)

	// Map NIC indices to their assigned sockets
	ifIndex2Sockets := make(map[int][]int)
	// Map sockets to the NIC indices assigned to them
	socket2IfIndexes := make(map[int][]int)

	getSocketBind := func(numaNode int) int {
		var socketBind int
		if numaNode != UnknownNumaNode {
			if socket, err := GetNumaPackageID(numaNode); err == nil {
				socketBind = socket
			} else {
				klog.Errorf("failed to GetNumaPackageID(%d), err %s", numaNode, err)
				socketBind = -1
			}
		} else {
			socketBind = -1
		}

		return socketBind
	}

	// Partition NICs into two distinct groups, one group contains NICs with known NUMA node is
	// placed in the front, the other group contains NICs without known NUMA node is placed in the back,
	// then sort each group individually by ifindex.
	// This ensures sockets allocation for NICs with known NUMA node takes precedence over
	// those without known numa node.
	sort.SliceStable(nics, func(i, j int) bool {
		iNicSocketBind := getSocketBind(nics[i].NumaNode)
		jNicSocketBind := getSocketBind(nics[j].NumaNode)

		if iNicSocketBind == jNicSocketBind {
			return nics[i].IfIndex < nics[j].IfIndex
		}

		if iNicSocketBind == -1 {
			return false
		}

		if jNicSocketBind == -1 {
			return true
		}

		return nics[i].IfIndex < nics[j].IfIndex
	})

	// Calculate the maximum ideal NICs per socket (rounded up division)
	idealMax := (len(nics) + len(sockets) - 1) / len(sockets)
	// Function to select socket with least nics, preferring lower-numbered sockets in case of ties
	selectSocketWithLeastNics := func() int {
		targetSocket, targetSocketNicsCount := -1, -1
		// sockets has been sorted
		for _, socket := range sockets {
			if targetSocket == -1 || len(socket2IfIndexes[socket]) < targetSocketNicsCount {
				targetSocket, targetSocketNicsCount = socket, len(socket2IfIndexes[socket])
			}
		}
		return targetSocket
	}

	// Assign sockets to each NIC, and make sure no socket assigned nics number more than idealMax
	for _, nic := range nics {
		var assignedSockets []int
		socketBind := getSocketBind(nic.NumaNode)

		if len(nics) == 1 {
			// If there is only one NIC, assign all available sockets to it
			assignedSockets = append(assignedSockets, sockets...)
		} else if socketBind != -1 && len(socket2IfIndexes[socketBind]) < idealMax {
			// If NIC has a valid socket bind and the socket isn't overloaded, use it
			assignedSockets = []int{socketBind}
		} else {
			// Otherwise, assign the least-used socket
			least := selectSocketWithLeastNics()
			assignedSockets = []int{least}
		}
		// Store NIC to socket assignment
		ifIndex2Sockets[nic.IfIndex] = assignedSockets
		for _, socket := range assignedSockets {
			// Store socket to NIC mapping and update usage count
			socket2IfIndexes[socket] = append(socket2IfIndexes[socket], nic.IfIndex)
		}
	}

	// Calculate the mininum ideal NICs per socket (rounded down division)
	idealMin := len(nics) / len(sockets)

	// Function to select socket with most nics, preferring lower-numbered sockets in case of ties
	selectSocketWithMostNics := func() int {
		targetSocket, targetSocketNicsCount := -1, -1
		// sockets has been sorted
		for _, socket := range sockets {
			if targetSocket == -1 || len(socket2IfIndexes[socket]) > targetSocketNicsCount {
				targetSocket, targetSocketNicsCount = socket, len(socket2IfIndexes[socket])
			}
		}
		return targetSocket
	}

	for _, socket := range sockets {
		if len(socket2IfIndexes[socket]) < idealMin {
			if idealMin-len(socket2IfIndexes[socket]) >= len(sockets) {
				klog.Errorf("it's impossible that socket %d has %d nics, round down socket nics count is %d, diff is greater-than socket count %d",
					socket, len(socket2IfIndexes[socket]), idealMin, len(sockets))
			}

			targetSocket := selectSocketWithMostNics()

			targetSocketNicsCount := len(socket2IfIndexes[targetSocket])
			if targetSocketNicsCount <= 1 {
				klog.Errorf("it's impossible that target socket %d has %d nics, less-equal 1", socket, targetSocketNicsCount)
				continue
			}

			if targetSocketNicsCount-len(socket2IfIndexes[socket]) < 2 {
				klog.Errorf("it's impossible that target socket %d with %d nics, socket %d with %d nics, diff less than 2",
					targetSocket, targetSocketNicsCount, socket, len(socket2IfIndexes[socket]))
				continue
			}

			targetIfIndex := socket2IfIndexes[targetSocket][targetSocketNicsCount-1]

			ifIndex2Sockets[targetIfIndex] = []int{socket}
			socket2IfIndexes[targetSocket] = socket2IfIndexes[targetSocket][:targetSocketNicsCount-1]
			socket2IfIndexes[socket] = append(socket2IfIndexes[socket], targetIfIndex)
		}
	}

	return &AllocatableInterfaceSocketInfo{
		IfIndex2Sockets:  ifIndex2Sockets,
		Socket2IfIndexes: socket2IfIndexes,
	}, nil
}

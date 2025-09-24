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

	info "github.com/google/cadvisor/info/v1"
	"golang.org/x/sys/unix"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/klog/v2"

	"github.com/kubewharf/katalyst-core/pkg/config/agent/global"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

const (
	// L3CacheLevel represents the cache level for L3 cache
	L3CacheLevel = 3
	L3CacheType  = "Unified"
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

// L3CacheTopology contains the mapping relationships between L3 caches, CPUs, and NUMA nodes
type L3CacheTopology struct {
	// L3CacheToCPUs maps L3 cache ID to the set of CPUs that share this L3 cache
	L3CacheToCPUs map[int]CPUSet
	// NUMAToL3Caches maps NUMA node ID to the set of L3 cache IDs in this NUMA node
	NUMAToL3Caches map[int]CPUSet
	// L3CacheToNUMANodes maps L3 cache ID to the set of NUMA nodes that share this L3 cache
	L3CacheToNUMANodes map[int]CPUSet
}

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
	L3CacheTopology      L3CacheTopology
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

// NUMAsPerSocket returns the number of NUMA
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
					L3CacheID:  j,
				}

				cpuTopology.CPUDetails[k+cpuNum/2] = CPUTopoInfo{
					NUMANodeID: j,
					SocketID:   i,
					CoreID:     k,
					L3CacheID:  j,
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

	// Generate dummy L3 cache topology
	cpuTopology.L3CacheTopology = BuildDummyL3CacheTopology(cpuTopology.CPUDetails)

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
	L3CacheID  int
}

// KeepOnly returns a new CPUDetails object with only the supplied cpus.
func (d CPUDetails) KeepOnly(cpus CPUSet) CPUDetails {
	result := CPUDetails{}
	for cpu, cpuInfo := range d {
		if cpus.Contains(cpu) {
			result[cpu] = cpuInfo
		}
	}
	return result
}

// NUMANodes returns all NUMANode IDs associated with the CPUs in this CPUDetails.
func (d CPUDetails) NUMANodes() CPUSet {
	b := NewCPUSet()
	for _, cpuInfo := range d {
		b.Add(cpuInfo.NUMANodeID)
	}
	return b
}

// NUMANodesInSockets returns all logical NUMANode IDs associated with
// the given socket IDs in this CPUDetails.
func (d CPUDetails) NUMANodesInSockets(ids ...int) CPUSet {
	b := NewCPUSet()
	for _, id := range ids {
		for _, cpuInfo := range d {
			if cpuInfo.SocketID == id {
				b.Add(cpuInfo.NUMANodeID)
			}
		}
	}
	return b
}

// Sockets returns all socket IDs associated with the CPUs in this CPUDetails.
func (d CPUDetails) Sockets() CPUSet {
	b := NewCPUSet()
	for _, cpuInfo := range d {
		b.Add(cpuInfo.SocketID)
	}
	return b
}

// L3Caches returns all l3Cache IDs associated with the CPUs in this CPUDetails.
func (d CPUDetails) L3Caches() CPUSet {
	b := NewCPUSet()
	for _, cpuInfo := range d {
		b.Add(cpuInfo.L3CacheID)
	}
	return b
}

// CPUsInSockets returns all logical CPU IDs associated with the given
// socket IDs in this CPUDetails.
func (d CPUDetails) CPUsInSockets(ids ...int) CPUSet {
	b := NewCPUSet()
	for _, id := range ids {
		for cpu, cpuInfo := range d {
			if cpuInfo.SocketID == id {
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
		for _, cpuInfo := range d {
			if cpuInfo.NUMANodeID == id {
				b.Add(cpuInfo.SocketID)
			}
		}
	}
	return b
}

// Cores returns all core IDs associated with the CPUs in this CPUDetails.
func (d CPUDetails) Cores() CPUSet {
	b := NewCPUSet()
	for _, cpuInfo := range d {
		b.Add(cpuInfo.CoreID)
	}
	return b
}

// CoresInNUMANodes returns all core IDs associated with the given
// NUMANode IDs in this CPUDetails.
func (d CPUDetails) CoresInNUMANodes(ids ...int) CPUSet {
	b := NewCPUSet()
	for _, id := range ids {
		for _, cpuInfo := range d {
			if cpuInfo.NUMANodeID == id {
				b.Add(cpuInfo.CoreID)
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
		for _, cpuInfo := range d {
			if cpuInfo.SocketID == id {
				b.Add(cpuInfo.CoreID)
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
		for cpu, cpuInfo := range d {
			if cpuInfo.NUMANodeID == id {
				b.Add(cpu)
			}
		}
	}
	return b
}

// CPUsInL3Caches returns all logical CPU IDs associated with the given
// l3Cache IDs in this CPUDetails.
func (d CPUDetails) CPUsInL3Caches(ids ...int) CPUSet {
	b := NewCPUSet()
	for _, id := range ids {
		for cpu, cpuInfo := range d {
			if cpuInfo.L3CacheID == id {
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
		for cpu, cpuInfo := range d {
			if cpuInfo.CoreID == id {
				b.Add(cpu)
			}
		}
	}
	return b
}

// BuildL3CacheTopologyFromMachineInfo builds L3 cache topology from cpu details
func BuildL3CacheTopologyFromMachineInfo(cpuDetails CPUDetails) L3CacheTopology {
	l3CacheToCPUs := make(map[int]CPUSet)
	numaToL3Caches := make(map[int]CPUSet)
	l3CacheToNUMANodes := make(map[int]CPUSet)

	// Process cache topology information
	for cpuID, cpu := range cpuDetails {
		l3CacheID := cpu.L3CacheID
		// Map L3 cache ID to CPUs
		if _, exists := l3CacheToCPUs[l3CacheID]; !exists {
			l3CacheToCPUs[l3CacheID] = NewCPUSet()
		}
		l3CacheToCPUs[l3CacheID].Add(cpuID)

		// Map NUMA node to L3 cache IDs
		if _, exists := numaToL3Caches[cpu.NUMANodeID]; !exists {
			numaToL3Caches[cpu.NUMANodeID] = NewCPUSet()
		}
		numaToL3Caches[cpu.NUMANodeID].Add(l3CacheID)

		// Map L3 cache ID to NUMA nodes
		if _, exists := l3CacheToNUMANodes[l3CacheID]; !exists {
			l3CacheToNUMANodes[l3CacheID] = NewCPUSet()
		}
		l3CacheToNUMANodes[l3CacheID].Add(cpu.NUMANodeID)
	}

	return L3CacheTopology{
		L3CacheToCPUs:      l3CacheToCPUs,
		NUMAToL3Caches:     numaToL3Caches,
		L3CacheToNUMANodes: l3CacheToNUMANodes,
	}
}

// BuildDummyL3CacheTopology builds dummy L3 cache topology for testing
func BuildDummyL3CacheTopology(cpuDetails CPUDetails) L3CacheTopology {
	l3CacheToCPUs := make(map[int]CPUSet)
	numaToL3Caches := make(map[int]CPUSet)
	l3CacheToNUMANodes := make(map[int]CPUSet)

	// In a dummy topology, map each NUMA node to one L3 cache
	numaNodes := cpuDetails.NUMANodes()
	for numaID := range numaNodes.ToSliceInt() {
		l3CacheID := numaID // Simple mapping: one L3 cache per NUMA node

		// Map L3 cache ID to CPUs in this NUMA node
		l3CacheToCPUs[l3CacheID] = cpuDetails.CPUsInNUMANodes(numaID)

		// Map NUMA node to L3 cache IDs
		numaToL3Caches[numaID] = NewCPUSet(l3CacheID)

		// Map L3 cache ID to NUMA nodes
		l3CacheToNUMANodes[l3CacheID] = NewCPUSet(numaID)
	}

	return L3CacheTopology{
		L3CacheToCPUs:      l3CacheToCPUs,
		NUMAToL3Caches:     numaToL3Caches,
		L3CacheToNUMANodes: l3CacheToNUMANodes,
	}
}

// CPUsInL3Caches returns all logical CPU IDs associated with the given
// L3 cache IDs in this CPUTopology.
func (topo L3CacheTopology) CPUsInL3Caches(ids ...int) CPUSet {
	b := NewCPUSet()
	for _, id := range ids {
		if cpus, exists := topo.L3CacheToCPUs[id]; exists {
			b = b.Union(cpus)
		}
	}
	return b
}

// L3CachesInNUMANodes returns all L3 cache IDs associated with the given
// NUMA node IDs in this L3CacheTopology.
func (topo L3CacheTopology) L3CachesInNUMANodes(ids ...int) CPUSet {
	b := NewCPUSet()
	for _, id := range ids {
		if l3Caches, exists := topo.NUMAToL3Caches[id]; exists {
			b = b.Union(l3Caches)
		}
	}
	return b
}

// NUMANodesInL3Caches returns all NUMA node IDs associated with the given
// L3 cache IDs in this CPUTopology.
func (topo L3CacheTopology) NUMANodesInL3Caches(ids ...int) CPUSet {
	b := NewCPUSet()
	for _, id := range ids {
		if numaNodes, exists := topo.L3CacheToNUMANodes[id]; exists {
			b = b.Union(numaNodes)
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
			l3CacheID := getUniqueL3CacheID(core)
			if coreID, err := getUniqueCoreID(core.Threads); err == nil {
				for _, cpu := range core.Threads {
					cpuDetails[cpu] = CPUTopoInfo{
						CoreID:     coreID,
						SocketID:   core.SocketID,
						NUMANodeID: node.Id,
						L3CacheID:  l3CacheID,
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

	// Build L3 cache topology based on cache information from machineInfo
	l3CacheTopology := BuildL3CacheTopologyFromMachineInfo(cpuDetails)

	return &CPUTopology{
		NumCPUs:              machineInfo.NumCores,
		NumSockets:           machineInfo.NumSockets,
		NumCores:             numPhysicalCores,
		NumNUMANodes:         numNUMANodes,
		NUMANodeIDToSocketID: numaNodeIDToSocketID,
		NUMAToCPUs:           numaToCPUs,
		CPUDetails:           cpuDetails,
		CPUInfo:              cpuInfo,
		L3CacheTopology:      l3CacheTopology,
	}, &memoryTopology, nil
}

// getUniqueL3CacheID returns the unique L3 cache ID for the given core.
// If no L3 cache is found, it returns -1.
// todo: Intel multi-die machine can not get unique L3 Cache ID from UncoreCaches now.
func getUniqueL3CacheID(core info.Core) int {
	for _, cache := range core.UncoreCaches {
		if cache.Level == L3CacheLevel && cache.Type == L3CacheType {
			return cache.Id
		}
	}
	return -1
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

	m := threads[0]
	for _, thread := range threads[1:] {
		if thread < m {
			m = thread
		}
	}

	return m, nil
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

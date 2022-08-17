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
	"sort"

	info "github.com/google/cadvisor/info/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/klog/v2"
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
	NumSockets   int
	NumNUMANodes int
	CPUDetails   CPUDetails
}

// CPUsPerCore returns the number of logical CPUs
// are associated with each core.
func (topo *CPUTopology) CPUsPerCore() int {
	if topo.NumCores == 0 {
		return 0
	}
	return topo.NumCPUs / topo.NumCores
}

// CPUsPerSocket returns the number of logical CPUs
// are associated with each socket.
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
	machineInfo, err := getMachineInfo()

	if err != nil {
		return nil, err
	}

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

// CPUInfo contains the NUMA, socket, and core IDs associated with a CPU.
type CPUInfo struct {
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
func Discover(machineInfo *info.MachineInfo) (*CPUTopology, error) {
	if machineInfo.NumCores == 0 {
		return nil, fmt.Errorf("could not detect number of cpus")
	}

	CPUDetails := CPUDetails{}
	numPhysicalCores := 0

	for _, node := range machineInfo.Topology {
		numPhysicalCores += len(node.Cores)
		for _, core := range node.Cores {
			if coreID, err := getUniqueCoreID(core.Threads); err == nil {
				for _, cpu := range core.Threads {
					CPUDetails[cpu] = CPUInfo{
						CoreID:     coreID,
						SocketID:   core.SocketID,
						NUMANodeID: node.Id,
					}
				}
			} else {
				klog.ErrorS(nil, "Could not get unique coreID for socket",
					"socket", core.SocketID, "core", core.Id, "threads", core.Threads)
				return nil, err
			}
		}
	}

	return &CPUTopology{
		NumCPUs:      machineInfo.NumCores,
		NumSockets:   machineInfo.NumSockets,
		NumCores:     numPhysicalCores,
		NumNUMANodes: CPUDetails.NUMANodes().Size(),
		CPUDetails:   CPUDetails,
	}, nil
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

// GetQuantityMap is used to generate cpu resource counting map
// based on the given CPUSet map
func GetQuantityMap(csetMap map[string]CPUSet) map[string]int {
	ret := make(map[string]int)

	for name, cset := range csetMap {
		ret[name] = cset.Size()
	}

	return ret
}

// GetSortedQuantityMapKeys is used to generate cpu resource counting map
// based on the given CPUSet map with sorting logic
func GetSortedQuantityMapKeys(quantityMap map[string]int) []string {
	ret := make([]string, 0, len(quantityMap))

	for key := range quantityMap {
		ret = append(ret, key)
	}

	sort.Strings(ret)

	return ret
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

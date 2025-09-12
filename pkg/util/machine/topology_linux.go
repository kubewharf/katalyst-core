//go:build linux

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
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"k8s.io/klog/v2"

	"github.com/kubewharf/katalyst-core/pkg/config/agent/global"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

const (
	sysNodeDirectory = "/sys/devices/system/node"
)

func getNUMADistanceMap() (map[int][]NumaDistanceInfo, error) {
	fInfos, err := ioutil.ReadDir(sysNodeDirectory)
	if err != nil {
		return nil, fmt.Errorf("faield to ReadDir /sys/devices/system/node, err %s", err)
	}

	numaDistanceArray := make(map[int][]NumaDistanceInfo)
	for _, fi := range fInfos {
		if !fi.IsDir() {
			continue
		}
		if !strings.HasPrefix(fi.Name(), "node") {
			continue
		}

		nodeID, err := strconv.Atoi(fi.Name()[len("node"):])
		if err != nil {
			general.Infof("/sys/devices/system/node/%s is not a node directory", fi.Name())
			continue
		}

		b, err := ioutil.ReadFile(filepath.Join("/sys/devices/system/node", fi.Name(), "distance"))
		if err != nil {
			return nil, err
		}
		s := strings.TrimSpace(strings.TrimRight(string(b), "\n"))
		distances := strings.Fields(s)
		var distanceArray []NumaDistanceInfo
		for id, distanceStr := range distances {
			distance, err := strconv.Atoi(distanceStr)
			if err != nil {
				return nil, err
			}
			distanceArray = append(distanceArray, NumaDistanceInfo{
				NumaID:   id,
				Distance: distance,
			})
		}
		numaDistanceArray[nodeID] = distanceArray
	}

	return numaDistanceArray, nil
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

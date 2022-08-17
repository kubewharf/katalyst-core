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

package topology

import (
	"sort"

	v1 "k8s.io/api/core/v1"
	"k8s.io/klog/v2"

	info "github.com/google/cadvisor/info/v1"
	nodev1alpha1 "github.com/kubewharf/katalyst-api/pkg/apis/node/v1alpha1"
)

// GenerateSocketStatus generate a map of socket id to socket status,
// which includes numa id and numa capacity of each socket.
func GenerateSocketStatus(
	numaCapacity map[int]*v1.ResourceList,
	numaAllocatable map[int]*v1.ResourceList,
	numa2socket map[int]int,
) map[int]*nodev1alpha1.SocketStatus {
	socketStatusMap := make(map[int]*nodev1alpha1.SocketStatus)

	// build numa status and set NumaID, Allocatable, Capacity
	for numaID, capacity := range numaCapacity {
		socketID, ok := numa2socket[numaID]
		if !ok {
			klog.Errorf("[GenerateSocketStatus] not found socketID for node %d", numaID)
			continue
		}

		socket, ok := socketStatusMap[socketID]
		if !ok {
			socketStatusMap[socketID] = &nodev1alpha1.SocketStatus{
				SocketID: socketID,
			}
			socket = socketStatusMap[socketID]
		}

		allocatable, ok := numaAllocatable[numaID]
		if !ok {
			klog.Errorf("[GenerateSocketStatus] not found allocatable for numa %d", numaID)
		}

		numaStatus := &nodev1alpha1.NumaStatus{
			NumaID:      numaID,
			Allocatable: allocatable,
			Capacity:    capacity,
		}
		socket.Numas = append(socket.Numas, numaStatus)
	}

	return socketStatusMap
}

// GenerateNumaTopologyStatus generate numa topology status by merging socket status and
// numa status according to the numa id, and sort them to get stable result at the end.
func GenerateNumaTopologyStatus(
	socketStatusMap map[int]*nodev1alpha1.SocketStatus,
	numaStatusMap map[int]*nodev1alpha1.NumaStatus,
) *nodev1alpha1.TopologyStatus {
	result := &nodev1alpha1.TopologyStatus{}
	for _, socket := range socketStatusMap {
		for _, numa := range socket.Numas {
			if numa == nil {
				continue
			}

			numaStatus, ok := numaStatusMap[numa.NumaID]
			if !ok || numaStatus == nil {
				continue
			}

			numa.Allocations = numaStatus.Allocations
		}
		result.Sockets = append(result.Sockets, socket)
	}

	// we need to sort numa and socket by id
	// to get stable result.
	for _, socket := range result.Sockets {
		for _, numa := range socket.Numas {
			sort.SliceStable(numa.Allocations, func(i, j int) bool {
				return numa.Allocations[i].Consumer < numa.Allocations[j].Consumer
			})
		}

		sort.SliceStable(socket.Numas, func(i, j int) bool {
			return socket.Numas[i].NumaID < socket.Numas[j].NumaID
		})
	}

	sort.SliceStable(result.Sockets, func(i, j int) bool {
		return result.Sockets[i].SocketID < result.Sockets[j].SocketID
	})
	return result
}

// GetNumaToSocketMap parse numa info to get the map of numa id to socket id
func GetNumaToSocketMap(nodes []info.Node) map[int]int {
	numaToSocketMap := make(map[int]int)

	for _, node := range nodes {
		// CAUTION: CNR design doesn't consider singer NUMA and multi sockets platform.
		// So here we think all cores in the same NUMA has the same socket ID.
		if len(node.Cores) > 0 {
			numaToSocketMap[node.Id] = node.Cores[0].SocketID
		}
	}

	return numaToSocketMap
}

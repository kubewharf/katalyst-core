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
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestGetInterfaceSocketInfo(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name        string
		nics        []InterfaceInfo
		cpuTopology *CPUTopology
		expectErr   bool
		want        *AllocatableInterfaceSocketInfo
	}{
		{
			name:        "No sockets available",
			nics:        []InterfaceInfo{{IfIndex: 1, NumaNode: 0}},
			cpuTopology: &CPUTopology{NumSockets: 0},
			expectErr:   true,
			want:        nil,
		},
		{
			name: "Single NIC, valid socket binding",
			nics: []InterfaceInfo{{IfIndex: 1, NumaNode: 0}},
			cpuTopology: &CPUTopology{
				NumSockets:           2,
				CPUDetails:           CPUDetails{0: CPUTopoInfo{NUMANodeID: 0, SocketID: 1}},
				NUMANodeIDToSocketID: map[int]int{0: 1},
			},
			expectErr: false,
			want: &AllocatableInterfaceSocketInfo{
				IfIndex2Sockets:  map[int][]int{1: {1}},
				Socket2IfIndexes: map[int][]int{1: {1}},
			},
		},
		{
			name: "Multiple NICs, evenly distributed",
			nics: []InterfaceInfo{
				{IfIndex: 1, NumaNode: 0},
				{IfIndex: 2, NumaNode: 1},
			},
			cpuTopology: &CPUTopology{
				NumSockets:           2,
				CPUDetails:           CPUDetails{0: CPUTopoInfo{NUMANodeID: 0, SocketID: 0}, 1: CPUTopoInfo{NUMANodeID: 1, SocketID: 1}},
				NUMANodeIDToSocketID: map[int]int{0: 0, 1: 1},
			},
			expectErr: false,
			want: &AllocatableInterfaceSocketInfo{
				IfIndex2Sockets:  map[int][]int{1: {0}, 2: {1}},
				Socket2IfIndexes: map[int][]int{0: {1}, 1: {2}},
			},
		},
		{
			name: "NIC with unknown NUMA node",
			nics: []InterfaceInfo{
				{IfIndex: 1, NumaNode: UnknownNumaNode},
				{IfIndex: 2, NumaNode: UnknownNumaNode},
			},
			cpuTopology: &CPUTopology{
				NumSockets:           2,
				CPUDetails:           CPUDetails{0: CPUTopoInfo{NUMANodeID: 0, SocketID: 0}, 1: CPUTopoInfo{NUMANodeID: 1, SocketID: 1}},
				NUMANodeIDToSocketID: map[int]int{0: 0, 1: 1},
			},
			expectErr: false,
			want: &AllocatableInterfaceSocketInfo{
				IfIndex2Sockets:  map[int][]int{1: {0}, 2: {1}},
				Socket2IfIndexes: map[int][]int{0: {1}, 1: {2}},
			},
		},
		{
			name: "Socket overloaded, use least used socket",
			nics: []InterfaceInfo{
				{IfIndex: 1, NumaNode: 0},
				{IfIndex: 2, NumaNode: 0},
				{IfIndex: 3, NumaNode: UnknownNumaNode},
			},
			cpuTopology: &CPUTopology{
				NumSockets:           2,
				CPUDetails:           CPUDetails{0: CPUTopoInfo{NUMANodeID: 0, SocketID: 0}, 1: CPUTopoInfo{NUMANodeID: 1, SocketID: 1}},
				NUMANodeIDToSocketID: map[int]int{0: 0, 1: 1},
			},
			expectErr: false,
			want: &AllocatableInterfaceSocketInfo{
				IfIndex2Sockets:  map[int][]int{1: {0}, 2: {0}, 3: {1}},
				Socket2IfIndexes: map[int][]int{0: {1, 2}, 1: {3}},
			},
		},
		{
			name: "Need to rebalance NICs across sockets",
			nics: []InterfaceInfo{
				{IfIndex: 1, NumaNode: 0},
				{IfIndex: 2, NumaNode: 0},
				{IfIndex: 3, NumaNode: 0},
				{IfIndex: 4, NumaNode: 1},
			},
			cpuTopology: &CPUTopology{
				NumSockets:           2,
				CPUDetails:           CPUDetails{0: CPUTopoInfo{NUMANodeID: 0, SocketID: 0}, 1: CPUTopoInfo{NUMANodeID: 1, SocketID: 1}},
				NUMANodeIDToSocketID: map[int]int{0: 0, 1: 1},
			},
			expectErr: false,
			want: &AllocatableInterfaceSocketInfo{
				IfIndex2Sockets:  map[int][]int{1: {0}, 2: {0}, 3: {1}, 4: {1}},
				Socket2IfIndexes: map[int][]int{0: {1, 2}, 1: {3, 4}},
			},
		},
		{
			name: "Need to rebalance NICs across sockets",
			nics: []InterfaceInfo{
				{IfIndex: 1, NumaNode: 0},
				{IfIndex: 2, NumaNode: 0},
				{IfIndex: 3, NumaNode: 0},
				{IfIndex: 4, NumaNode: 1},
			},
			cpuTopology: &CPUTopology{
				NumSockets:           2,
				CPUDetails:           CPUDetails{0: CPUTopoInfo{NUMANodeID: 0, SocketID: 0}, 1: CPUTopoInfo{NUMANodeID: 1, SocketID: 1}},
				NUMANodeIDToSocketID: map[int]int{0: 0, 1: 1},
			},
			expectErr: false,
			want: &AllocatableInterfaceSocketInfo{
				IfIndex2Sockets:  map[int][]int{1: {0}, 2: {0}, 3: {1}, 4: {1}},
				Socket2IfIndexes: map[int][]int{0: {1, 2}, 1: {3, 4}},
			},
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			mockLock.Lock()
			defer mockLock.Unlock()
			result, err := GetInterfaceSocketInfo(tc.nics, tc.cpuTopology.CPUDetails.Sockets().ToSliceInt())
			if tc.expectErr {
				assert.Error(t, err)
				assert.Nil(t, result)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, result)
				assert.Equal(t, tc.want.IfIndex2Sockets, result.IfIndex2Sockets)
				assert.Equal(t, tc.want.Socket2IfIndexes, result.Socket2IfIndexes)
			}
		})
	}
}

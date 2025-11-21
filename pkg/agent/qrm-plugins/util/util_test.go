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

package util

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	"github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

func TestGetQuantityFromResourceReq(t *testing.T) {
	t.Parallel()

	as := require.New(t)

	testCases := []struct {
		req    *pluginapi.ResourceRequest
		result int
		err    error
	}{
		{
			req: &pluginapi.ResourceRequest{
				ResourceName: string(v1.ResourceCPU),
				ResourceRequests: map[string]float64{
					string(v1.ResourceCPU): 123,
				},
			},
			result: 123,
		},
		{
			req: &pluginapi.ResourceRequest{
				ResourceName: string(consts.ReclaimedResourceMilliCPU),
				ResourceRequests: map[string]float64{
					string(consts.ReclaimedResourceMilliCPU): 234001,
				},
			},
			result: 235,
		},
		{
			req: &pluginapi.ResourceRequest{
				ResourceName: string(v1.ResourceMemory),
				ResourceRequests: map[string]float64{
					string(v1.ResourceMemory): 256,
				},
			},
			result: 256,
		},
		{
			req: &pluginapi.ResourceRequest{
				ResourceName: string(consts.ReclaimedResourceMemory),
				ResourceRequests: map[string]float64{
					string(consts.ReclaimedResourceMemory): 1345,
				},
			},
			result: 1345,
		},
		{
			req: &pluginapi.ResourceRequest{
				ResourceName: string(v1.ResourceCPU),
				ResourceRequests: map[string]float64{
					"test": 1345,
				},
			},
			err: errors.NewNotFound(schema.GroupResource{}, string(v1.ResourceCPU)),
		},
	}

	for _, tc := range testCases {
		res, _, err := GetQuantityFromResourceReq(tc.req)
		if tc.err != nil {
			as.Equal(tc.err, err)
		} else {
			as.EqualValues(tc.result, res)
		}
	}
}

func TestDeepCopyTopologyAwareAssignments(t *testing.T) {
	t.Parallel()

	as := require.New(t)

	testCases := []struct {
		description              string
		topologyAwareAssignments map[int]machine.CPUSet
	}{
		{
			description: "nil topologyAwareAssignments",
		},
		{
			description: "non-nil topologyAwareAssignments",
			topologyAwareAssignments: map[int]machine.CPUSet{
				0: machine.NewCPUSet(0, 1, 8, 9),
				1: machine.NewCPUSet(2, 3, 10, 11),
				2: machine.NewCPUSet(4, 5, 12, 13),
				3: machine.NewCPUSet(6, 7, 14, 15),
			},
		},
	}

	for _, tc := range testCases {
		copiedAssignments := machine.DeepcopyCPUAssignment(tc.topologyAwareAssignments)
		as.Equalf(tc.topologyAwareAssignments, copiedAssignments, "failed in test case: %s", tc.description)
	}
}

func TestHintToIntArray(t *testing.T) {
	t.Parallel()

	as := require.New(t)

	testCases := []struct {
		description   string
		hint          *pluginapi.TopologyHint
		expectedArray []int
	}{
		{
			description:   "nil hint",
			expectedArray: []int{},
		},
		{
			description: "empty nodes in hint",
			hint: &pluginapi.TopologyHint{
				Nodes:     []uint64{},
				Preferred: false,
			},
			expectedArray: []int{},
		},
		{
			description: "nil nodes in hint",
			hint: &pluginapi.TopologyHint{
				Nodes:     nil,
				Preferred: false,
			},
			expectedArray: []int{},
		},
		{
			description: "non-empty nodes in hint",
			hint: &pluginapi.TopologyHint{
				Nodes:     []uint64{0, 1, 2, 3},
				Preferred: false,
			},
			expectedArray: []int{0, 1, 2, 3},
		},
	}

	for _, tc := range testCases {
		actualArray := HintToIntArray(tc.hint)
		as.Equalf(tc.expectedArray, actualArray, "failed in test case: %s", tc.description)
	}
}

func TestMaskToUInt64Array(t *testing.T) {
	t.Parallel()

	as := require.New(t)

	nonEmptyMask, err := machine.NewBitMask(0, 1, 2, 3)
	as.Nil(err)

	testCases := []struct {
		description   string
		mask          machine.BitMask
		expectedArray []uint64
	}{
		{
			description:   "empty mask",
			mask:          machine.NewEmptyBitMask(),
			expectedArray: []uint64{},
		},
		{
			description:   "non-empty mask",
			mask:          nonEmptyMask,
			expectedArray: []uint64{0, 1, 2, 3},
		},
	}

	for _, tc := range testCases {
		actualArray := machine.MaskToUInt64Array(tc.mask)
		as.Equalf(tc.expectedArray, actualArray, "failed in test case: %s", tc.description)
	}
}

func TestTransformTopologyAwareQuantity(t *testing.T) {
	t.Parallel()

	as := require.New(t)

	testCases := []struct {
		description          string
		assignments          map[int]machine.CPUSet
		expectedQuantityList []*pluginapi.TopologyAwareQuantity
	}{
		{
			description: "nil assignments",
		},
		{
			description: "singe numa",
			assignments: map[int]machine.CPUSet{0: machine.NewCPUSet(0, 1, 8, 9)},
			expectedQuantityList: []*pluginapi.TopologyAwareQuantity{
				{ResourceValue: 4, Node: 0},
			},
		},
		{
			description: "multi-numas",
			assignments: map[int]machine.CPUSet{
				0: machine.NewCPUSet(0, 1, 8, 9),
				1: machine.NewCPUSet(2, 10),
			},
			expectedQuantityList: []*pluginapi.TopologyAwareQuantity{
				{ResourceValue: 4, Node: 0},
				{ResourceValue: 2, Node: 1},
			},
		},
		{
			description: "full numas",
			assignments: map[int]machine.CPUSet{
				0: machine.NewCPUSet(0, 1, 8, 9),
				1: machine.NewCPUSet(2, 3, 10, 11),
				2: machine.NewCPUSet(4, 5, 12, 13),
				3: machine.NewCPUSet(6, 7, 14, 15),
			},
			expectedQuantityList: []*pluginapi.TopologyAwareQuantity{
				{ResourceValue: 4, Node: 0},
				{ResourceValue: 4, Node: 1},
				{ResourceValue: 4, Node: 2},
				{ResourceValue: 4, Node: 3},
			},
		},
	}

	for _, tc := range testCases {
		actualQuantityList := GetTopologyAwareQuantityFromAssignments(tc.assignments)
		as.Equalf(tc.expectedQuantityList, actualQuantityList, "failed in test case: %s", tc.description)
	}
}

func TestGetNUMANodesCountToFitCPUReq(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		cpuReq        float64
		cpuTopology   *machine.CPUTopology
		expectedNodes int
		expectedCPUs  int
		expectedErr   string
	}{
		{
			name:        "nil cpu topology",
			cpuReq:      1.0,
			cpuTopology: nil,
			expectedErr: "GetNumaNodesToFitCPUReq got nil cpuTopology",
		},
		{
			name:   "no numa nodes",
			cpuReq: 1.0,
			cpuTopology: &machine.CPUTopology{
				CPUDetails: make(machine.CPUDetails),
			},
			expectedErr: "there is no NUMA in cpuTopology",
		},
		{
			name:   "can not be divided evenly",
			cpuReq: 1.0,
			cpuTopology: &machine.CPUTopology{
				NumCPUs: 5,
				CPUDetails: machine.CPUDetails{
					0: {NUMANodeID: 0},
					1: {NUMANodeID: 0},
					2: {NUMANodeID: 1},
					3: {NUMANodeID: 1},
					4: {NUMANodeID: 2},
				},
			},
			expectedNodes: 1,
			expectedCPUs:  1,
		},
		{
			name:   "cpu req too large",
			cpuReq: 10.0,
			cpuTopology: &machine.CPUTopology{
				NumCPUs: 4,
				CPUDetails: machine.CPUDetails{
					0: {NUMANodeID: 0},
					1: {NUMANodeID: 0},
					2: {NUMANodeID: 1},
					3: {NUMANodeID: 1},
				},
			},
			expectedErr: "invalid cpu req: 10.000 in topology with NUMAs count: 2 and CPUs count: 4",
		},
		{
			name:   "single numa node required",
			cpuReq: 1.5,
			cpuTopology: &machine.CPUTopology{
				NumCPUs: 4,
				CPUDetails: machine.CPUDetails{
					0: {NUMANodeID: 0},
					1: {NUMANodeID: 0},
					2: {NUMANodeID: 1},
					3: {NUMANodeID: 1},
				},
			},
			expectedNodes: 1,
			expectedCPUs:  2,
		},
		{
			name:   "multiple numa nodes required",
			cpuReq: 3.0,
			cpuTopology: &machine.CPUTopology{
				NumCPUs: 8,
				CPUDetails: machine.CPUDetails{
					0: {NUMANodeID: 0},
					1: {NUMANodeID: 0},
					2: {NUMANodeID: 1},
					3: {NUMANodeID: 1},
					4: {NUMANodeID: 2},
					5: {NUMANodeID: 2},
					6: {NUMANodeID: 3},
					7: {NUMANodeID: 3},
				},
			},
			expectedNodes: 2,
			expectedCPUs:  2,
		},
		{
			name:   "minimum numa nodes when cpu req is 0",
			cpuReq: 0.1,
			cpuTopology: &machine.CPUTopology{
				NumCPUs: 8,
				CPUDetails: machine.CPUDetails{
					0: {NUMANodeID: 0},
					1: {NUMANodeID: 0},
					2: {NUMANodeID: 1},
					3: {NUMANodeID: 1},
					4: {NUMANodeID: 2},
					5: {NUMANodeID: 2},
					6: {NUMANodeID: 3},
					7: {NUMANodeID: 3},
				},
			},
			expectedNodes: 1,
			expectedCPUs:  1,
		},
		{
			name:        "cpu req is 0 with nil cpu topology",
			cpuReq:      0,
			cpuTopology: nil,
			expectedErr: "GetNumaNodesToFitCPUReq got nil cpuTopology",
		},
		{
			name:   "cpu req is 0 with no numa nodes",
			cpuReq: 0,
			cpuTopology: &machine.CPUTopology{
				CPUDetails: make(machine.CPUDetails),
			},
			expectedErr: "there is no NUMA in cpuTopology",
		},
		{
			name:   "cpu req is 0 with single numa node",
			cpuReq: 0,
			cpuTopology: &machine.CPUTopology{
				NumCPUs: 4,
				CPUDetails: machine.CPUDetails{
					0: {NUMANodeID: 0},
					1: {NUMANodeID: 0},
					2: {NUMANodeID: 0},
					3: {NUMANodeID: 0},
				},
			},
			expectedNodes: 1,
			expectedCPUs:  0,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			nodes, cpus, err := GetNUMANodesCountToFitCPUReq(tt.cpuReq, tt.cpuTopology)

			if tt.expectedErr != "" {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), tt.expectedErr)
				return
			}

			assert.NoError(t, err)
			assert.Equal(t, tt.expectedNodes, nodes)
			assert.Equal(t, tt.expectedCPUs, cpus)
		})
	}
}

func TestGetNUMANodesCountToFitMemoryReq(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		memoryReq     uint64
		bytesPerNUMA  uint64
		numaCount     int
		expectedNodes int
		expectedBytes uint64
		expectedError string
	}{
		{
			name:          "zero bytesPerNUMA should error",
			memoryReq:     100,
			bytesPerNUMA:  0,
			numaCount:     4,
			expectedError: "zero bytesPerNUMA",
		},
		{
			name:          "memory requirement exceeds available NUMA nodes",
			memoryReq:     500,
			bytesPerNUMA:  100,
			numaCount:     4,
			expectedError: "invalid memory req: 500 in topology with NUMAs count: 4 and bytesPerNUMA: 100",
		},
		{
			name:          "memory requirement exactly fits one NUMA node",
			memoryReq:     100,
			bytesPerNUMA:  100,
			numaCount:     4,
			expectedNodes: 1,
			expectedBytes: 100,
		},
		{
			name:          "memory requirement less than one NUMA node",
			memoryReq:     50,
			bytesPerNUMA:  100,
			numaCount:     4,
			expectedNodes: 1,
			expectedBytes: 50,
		},
		{
			name:          "memory requirement spans two NUMA nodes",
			memoryReq:     150,
			bytesPerNUMA:  100,
			numaCount:     4,
			expectedNodes: 2,
			expectedBytes: 75,
		},
		{
			name:          "memory requirement spans three NUMA nodes with uneven distribution",
			memoryReq:     250,
			bytesPerNUMA:  100,
			numaCount:     4,
			expectedNodes: 3,
			expectedBytes: 84, // ceil(250/3) = 84
		},
		{
			name:          "memory requirement equals total available NUMA memory",
			memoryReq:     400,
			bytesPerNUMA:  100,
			numaCount:     4,
			expectedNodes: 4,
			expectedBytes: 100,
		},
		{
			name:          "very large memory requirement with large NUMA nodes",
			memoryReq:     1_000_000_000,
			bytesPerNUMA:  500_000_000,
			numaCount:     4,
			expectedNodes: 2,
			expectedBytes: 500_000_000,
		},
		{
			name:          "zero memoryReq",
			memoryReq:     0,
			bytesPerNUMA:  1000,
			numaCount:     4,
			expectedNodes: 1,
			expectedBytes: 0,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			nodes, bytes, err := GetNUMANodesCountToFitMemoryReq(tt.memoryReq, tt.bytesPerNUMA, tt.numaCount)

			if tt.expectedError != "" {
				assert.Error(t, err)
				assert.Equal(t, tt.expectedError, err.Error())
				return
			}

			assert.NoError(t, err)
			assert.Equal(t, tt.expectedNodes, nodes)
			assert.Equal(t, tt.expectedBytes, bytes)
		})
	}
}

func TestCeilEdgeCases(t *testing.T) {
	t.Parallel()
	// Test that our ceil calculations work correctly with edge cases
	tests := []struct {
		name          string
		memoryReq     uint64
		bytesPerNUMA  uint64
		numaCount     int
		expectedNodes int
		expectedBytes uint64
	}{
		{
			name:          "ceil calculation with remainder",
			memoryReq:     101,
			bytesPerNUMA:  100,
			numaCount:     2,
			expectedNodes: 2,
			expectedBytes: 51, // ceil(101/2) = 51
		},
		{
			name:          "ceil calculation with exact division",
			memoryReq:     200,
			bytesPerNUMA:  100,
			numaCount:     2,
			expectedNodes: 2,
			expectedBytes: 100,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			nodes, bytes, err := GetNUMANodesCountToFitMemoryReq(tt.memoryReq, tt.bytesPerNUMA, tt.numaCount)
			assert.NoError(t, err)
			assert.Equal(t, tt.expectedNodes, nodes)
			assert.Equal(t, tt.expectedBytes, bytes)
		})
	}
}

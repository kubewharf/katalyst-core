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

	"github.com/stretchr/testify/require"
	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"
	"k8s.io/kubernetes/pkg/kubelet/cm/topologymanager/bitmask"

	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

func TestDeepCopyTopologyAwareAssignments(t *testing.T) {
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
	as := require.New(t)

	nonEmptyMask, err := bitmask.NewBitMask(0, 1, 2, 3)
	as.Nil(err)

	testCases := []struct {
		description   string
		mask          bitmask.BitMask
		expectedArray []uint64
	}{
		{
			description:   "empty mask",
			mask:          bitmask.NewEmptyBitMask(),
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

/*
Copyright 2022 The Katalyst Authors.
Copyright 2017 The Kubernetes Authors.

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

package calculator

import (
	"reflect"
	"sort"
	"testing"

	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

var (
	topoSingleSocketHT = &machine.CPUTopology{
		NumCPUs:    8,
		NumSockets: 1,
		NumCores:   4,
		CPUDetails: map[int]machine.CPUTopoInfo{
			0: {CoreID: 0, SocketID: 0, NUMANodeID: 0},
			1: {CoreID: 1, SocketID: 0, NUMANodeID: 0},
			2: {CoreID: 2, SocketID: 0, NUMANodeID: 0},
			3: {CoreID: 3, SocketID: 0, NUMANodeID: 0},
			4: {CoreID: 0, SocketID: 0, NUMANodeID: 0},
			5: {CoreID: 1, SocketID: 0, NUMANodeID: 0},
			6: {CoreID: 2, SocketID: 0, NUMANodeID: 0},
			7: {CoreID: 3, SocketID: 0, NUMANodeID: 0},
		},
	}

	topoDualSocketHT = &machine.CPUTopology{
		NumCPUs:    12,
		NumSockets: 2,
		NumCores:   6,
		CPUDetails: map[int]machine.CPUTopoInfo{
			0:  {CoreID: 0, SocketID: 0, NUMANodeID: 0},
			1:  {CoreID: 1, SocketID: 1, NUMANodeID: 1},
			2:  {CoreID: 2, SocketID: 0, NUMANodeID: 0},
			3:  {CoreID: 3, SocketID: 1, NUMANodeID: 1},
			4:  {CoreID: 4, SocketID: 0, NUMANodeID: 0},
			5:  {CoreID: 5, SocketID: 1, NUMANodeID: 1},
			6:  {CoreID: 0, SocketID: 0, NUMANodeID: 0},
			7:  {CoreID: 1, SocketID: 1, NUMANodeID: 1},
			8:  {CoreID: 2, SocketID: 0, NUMANodeID: 0},
			9:  {CoreID: 3, SocketID: 1, NUMANodeID: 1},
			10: {CoreID: 4, SocketID: 0, NUMANodeID: 0},
			11: {CoreID: 5, SocketID: 1, NUMANodeID: 1},
		},
	}

	// fake topology for testing purposes only
	topoTripleSocketHT = &machine.CPUTopology{
		NumCPUs:    18,
		NumSockets: 3,
		NumCores:   9,
		CPUDetails: map[int]machine.CPUTopoInfo{
			0:  {CoreID: 0, SocketID: 1, NUMANodeID: 1},
			1:  {CoreID: 0, SocketID: 1, NUMANodeID: 1},
			2:  {CoreID: 1, SocketID: 1, NUMANodeID: 1},
			3:  {CoreID: 1, SocketID: 1, NUMANodeID: 1},
			4:  {CoreID: 2, SocketID: 1, NUMANodeID: 1},
			5:  {CoreID: 2, SocketID: 1, NUMANodeID: 1},
			6:  {CoreID: 3, SocketID: 0, NUMANodeID: 0},
			7:  {CoreID: 3, SocketID: 0, NUMANodeID: 0},
			8:  {CoreID: 4, SocketID: 0, NUMANodeID: 0},
			9:  {CoreID: 4, SocketID: 0, NUMANodeID: 0},
			10: {CoreID: 5, SocketID: 0, NUMANodeID: 0},
			11: {CoreID: 5, SocketID: 0, NUMANodeID: 0},
			12: {CoreID: 6, SocketID: 2, NUMANodeID: 2},
			13: {CoreID: 6, SocketID: 2, NUMANodeID: 2},
			14: {CoreID: 7, SocketID: 2, NUMANodeID: 2},
			15: {CoreID: 7, SocketID: 2, NUMANodeID: 2},
			16: {CoreID: 8, SocketID: 2, NUMANodeID: 2},
			17: {CoreID: 8, SocketID: 2, NUMANodeID: 2},
		},
	}

	/*
		Topology from dual xeon gold 6230; lscpu excerpt
		CPU(s):              80
		On-line CPU(s) list: 0-79
		Thread(s) per core:  2
		Core(s) per socket:  20
		Socket(s):           2
		NUMA node(s):        4
		NUMA node0 CPU(s):   0-9,40-49
		NUMA node1 CPU(s):   10-19,50-59
		NUMA node2 CPU(s):   20-29,60-69
		NUMA node3 CPU(s):   30-39,70-79
	*/
	topoDualSocketMultiNumaPerSocketHT = &machine.CPUTopology{
		NumCPUs:      80,
		NumSockets:   2,
		NumCores:     40,
		NumNUMANodes: 4,
		CPUDetails: map[int]machine.CPUTopoInfo{
			0:  {CoreID: 0, SocketID: 0, NUMANodeID: 0},
			1:  {CoreID: 1, SocketID: 0, NUMANodeID: 0},
			2:  {CoreID: 2, SocketID: 0, NUMANodeID: 0},
			3:  {CoreID: 3, SocketID: 0, NUMANodeID: 0},
			4:  {CoreID: 4, SocketID: 0, NUMANodeID: 0},
			5:  {CoreID: 5, SocketID: 0, NUMANodeID: 0},
			6:  {CoreID: 6, SocketID: 0, NUMANodeID: 0},
			7:  {CoreID: 7, SocketID: 0, NUMANodeID: 0},
			8:  {CoreID: 8, SocketID: 0, NUMANodeID: 0},
			9:  {CoreID: 9, SocketID: 0, NUMANodeID: 0},
			10: {CoreID: 10, SocketID: 0, NUMANodeID: 1},
			11: {CoreID: 11, SocketID: 0, NUMANodeID: 1},
			12: {CoreID: 12, SocketID: 0, NUMANodeID: 1},
			13: {CoreID: 13, SocketID: 0, NUMANodeID: 1},
			14: {CoreID: 14, SocketID: 0, NUMANodeID: 1},
			15: {CoreID: 15, SocketID: 0, NUMANodeID: 1},
			16: {CoreID: 16, SocketID: 0, NUMANodeID: 1},
			17: {CoreID: 17, SocketID: 0, NUMANodeID: 1},
			18: {CoreID: 18, SocketID: 0, NUMANodeID: 1},
			19: {CoreID: 19, SocketID: 0, NUMANodeID: 1},
			20: {CoreID: 20, SocketID: 1, NUMANodeID: 2},
			21: {CoreID: 21, SocketID: 1, NUMANodeID: 2},
			22: {CoreID: 22, SocketID: 1, NUMANodeID: 2},
			23: {CoreID: 23, SocketID: 1, NUMANodeID: 2},
			24: {CoreID: 24, SocketID: 1, NUMANodeID: 2},
			25: {CoreID: 25, SocketID: 1, NUMANodeID: 2},
			26: {CoreID: 26, SocketID: 1, NUMANodeID: 2},
			27: {CoreID: 27, SocketID: 1, NUMANodeID: 2},
			28: {CoreID: 28, SocketID: 1, NUMANodeID: 2},
			29: {CoreID: 29, SocketID: 1, NUMANodeID: 2},
			30: {CoreID: 30, SocketID: 1, NUMANodeID: 3},
			31: {CoreID: 31, SocketID: 1, NUMANodeID: 3},
			32: {CoreID: 32, SocketID: 1, NUMANodeID: 3},
			33: {CoreID: 33, SocketID: 1, NUMANodeID: 3},
			34: {CoreID: 34, SocketID: 1, NUMANodeID: 3},
			35: {CoreID: 35, SocketID: 1, NUMANodeID: 3},
			36: {CoreID: 36, SocketID: 1, NUMANodeID: 3},
			37: {CoreID: 37, SocketID: 1, NUMANodeID: 3},
			38: {CoreID: 38, SocketID: 1, NUMANodeID: 3},
			39: {CoreID: 39, SocketID: 1, NUMANodeID: 3},
			40: {CoreID: 0, SocketID: 0, NUMANodeID: 0},
			41: {CoreID: 1, SocketID: 0, NUMANodeID: 0},
			42: {CoreID: 2, SocketID: 0, NUMANodeID: 0},
			43: {CoreID: 3, SocketID: 0, NUMANodeID: 0},
			44: {CoreID: 4, SocketID: 0, NUMANodeID: 0},
			45: {CoreID: 5, SocketID: 0, NUMANodeID: 0},
			46: {CoreID: 6, SocketID: 0, NUMANodeID: 0},
			47: {CoreID: 7, SocketID: 0, NUMANodeID: 0},
			48: {CoreID: 8, SocketID: 0, NUMANodeID: 0},
			49: {CoreID: 9, SocketID: 0, NUMANodeID: 0},
			50: {CoreID: 10, SocketID: 0, NUMANodeID: 1},
			51: {CoreID: 11, SocketID: 0, NUMANodeID: 1},
			52: {CoreID: 12, SocketID: 0, NUMANodeID: 1},
			53: {CoreID: 13, SocketID: 0, NUMANodeID: 1},
			54: {CoreID: 14, SocketID: 0, NUMANodeID: 1},
			55: {CoreID: 15, SocketID: 0, NUMANodeID: 1},
			56: {CoreID: 16, SocketID: 0, NUMANodeID: 1},
			57: {CoreID: 17, SocketID: 0, NUMANodeID: 1},
			58: {CoreID: 18, SocketID: 0, NUMANodeID: 1},
			59: {CoreID: 19, SocketID: 0, NUMANodeID: 1},
			60: {CoreID: 20, SocketID: 1, NUMANodeID: 2},
			61: {CoreID: 21, SocketID: 1, NUMANodeID: 2},
			62: {CoreID: 22, SocketID: 1, NUMANodeID: 2},
			63: {CoreID: 23, SocketID: 1, NUMANodeID: 2},
			64: {CoreID: 24, SocketID: 1, NUMANodeID: 2},
			65: {CoreID: 25, SocketID: 1, NUMANodeID: 2},
			66: {CoreID: 26, SocketID: 1, NUMANodeID: 2},
			67: {CoreID: 27, SocketID: 1, NUMANodeID: 2},
			68: {CoreID: 28, SocketID: 1, NUMANodeID: 2},
			69: {CoreID: 29, SocketID: 1, NUMANodeID: 2},
			70: {CoreID: 30, SocketID: 1, NUMANodeID: 3},
			71: {CoreID: 31, SocketID: 1, NUMANodeID: 3},
			72: {CoreID: 32, SocketID: 1, NUMANodeID: 3},
			73: {CoreID: 33, SocketID: 1, NUMANodeID: 3},
			74: {CoreID: 34, SocketID: 1, NUMANodeID: 3},
			75: {CoreID: 35, SocketID: 1, NUMANodeID: 3},
			76: {CoreID: 36, SocketID: 1, NUMANodeID: 3},
			77: {CoreID: 37, SocketID: 1, NUMANodeID: 3},
			78: {CoreID: 38, SocketID: 1, NUMANodeID: 3},
			79: {CoreID: 39, SocketID: 1, NUMANodeID: 3},
		},
	}
	/*
		FAKE Topology from dual xeon gold 6230
		(see: topoDualSocketMultiNumaPerSocketHT).
		We flip NUMA cells and Sockets to exercise the code.
		TODO(fromanirh): replace with a real-world topology
		once we find a suitable one.
	*/
	fakeTopoMultiSocketDualSocketPerNumaHT = &machine.CPUTopology{
		NumCPUs:      80,
		NumSockets:   4,
		NumCores:     40,
		NumNUMANodes: 2,
		CPUDetails: map[int]machine.CPUTopoInfo{
			0:  {CoreID: 0, SocketID: 0, NUMANodeID: 0},
			1:  {CoreID: 1, SocketID: 0, NUMANodeID: 0},
			2:  {CoreID: 2, SocketID: 0, NUMANodeID: 0},
			3:  {CoreID: 3, SocketID: 0, NUMANodeID: 0},
			4:  {CoreID: 4, SocketID: 0, NUMANodeID: 0},
			5:  {CoreID: 5, SocketID: 0, NUMANodeID: 0},
			6:  {CoreID: 6, SocketID: 0, NUMANodeID: 0},
			7:  {CoreID: 7, SocketID: 0, NUMANodeID: 0},
			8:  {CoreID: 8, SocketID: 0, NUMANodeID: 0},
			9:  {CoreID: 9, SocketID: 0, NUMANodeID: 0},
			10: {CoreID: 10, SocketID: 1, NUMANodeID: 0},
			11: {CoreID: 11, SocketID: 1, NUMANodeID: 0},
			12: {CoreID: 12, SocketID: 1, NUMANodeID: 0},
			13: {CoreID: 13, SocketID: 1, NUMANodeID: 0},
			14: {CoreID: 14, SocketID: 1, NUMANodeID: 0},
			15: {CoreID: 15, SocketID: 1, NUMANodeID: 0},
			16: {CoreID: 16, SocketID: 1, NUMANodeID: 0},
			17: {CoreID: 17, SocketID: 1, NUMANodeID: 0},
			18: {CoreID: 18, SocketID: 1, NUMANodeID: 0},
			19: {CoreID: 19, SocketID: 1, NUMANodeID: 0},
			20: {CoreID: 20, SocketID: 2, NUMANodeID: 1},
			21: {CoreID: 21, SocketID: 2, NUMANodeID: 1},
			22: {CoreID: 22, SocketID: 2, NUMANodeID: 1},
			23: {CoreID: 23, SocketID: 2, NUMANodeID: 1},
			24: {CoreID: 24, SocketID: 2, NUMANodeID: 1},
			25: {CoreID: 25, SocketID: 2, NUMANodeID: 1},
			26: {CoreID: 26, SocketID: 2, NUMANodeID: 1},
			27: {CoreID: 27, SocketID: 2, NUMANodeID: 1},
			28: {CoreID: 28, SocketID: 2, NUMANodeID: 1},
			29: {CoreID: 29, SocketID: 2, NUMANodeID: 1},
			30: {CoreID: 30, SocketID: 3, NUMANodeID: 1},
			31: {CoreID: 31, SocketID: 3, NUMANodeID: 1},
			32: {CoreID: 32, SocketID: 3, NUMANodeID: 1},
			33: {CoreID: 33, SocketID: 3, NUMANodeID: 1},
			34: {CoreID: 34, SocketID: 3, NUMANodeID: 1},
			35: {CoreID: 35, SocketID: 3, NUMANodeID: 1},
			36: {CoreID: 36, SocketID: 3, NUMANodeID: 1},
			37: {CoreID: 37, SocketID: 3, NUMANodeID: 1},
			38: {CoreID: 38, SocketID: 3, NUMANodeID: 1},
			39: {CoreID: 39, SocketID: 3, NUMANodeID: 1},
			40: {CoreID: 0, SocketID: 0, NUMANodeID: 0},
			41: {CoreID: 1, SocketID: 0, NUMANodeID: 0},
			42: {CoreID: 2, SocketID: 0, NUMANodeID: 0},
			43: {CoreID: 3, SocketID: 0, NUMANodeID: 0},
			44: {CoreID: 4, SocketID: 0, NUMANodeID: 0},
			45: {CoreID: 5, SocketID: 0, NUMANodeID: 0},
			46: {CoreID: 6, SocketID: 0, NUMANodeID: 0},
			47: {CoreID: 7, SocketID: 0, NUMANodeID: 0},
			48: {CoreID: 8, SocketID: 0, NUMANodeID: 0},
			49: {CoreID: 9, SocketID: 0, NUMANodeID: 0},
			50: {CoreID: 10, SocketID: 1, NUMANodeID: 0},
			51: {CoreID: 11, SocketID: 1, NUMANodeID: 0},
			52: {CoreID: 12, SocketID: 1, NUMANodeID: 0},
			53: {CoreID: 13, SocketID: 1, NUMANodeID: 0},
			54: {CoreID: 14, SocketID: 1, NUMANodeID: 0},
			55: {CoreID: 15, SocketID: 1, NUMANodeID: 0},
			56: {CoreID: 16, SocketID: 1, NUMANodeID: 0},
			57: {CoreID: 17, SocketID: 1, NUMANodeID: 0},
			58: {CoreID: 18, SocketID: 1, NUMANodeID: 0},
			59: {CoreID: 19, SocketID: 1, NUMANodeID: 0},
			60: {CoreID: 20, SocketID: 2, NUMANodeID: 1},
			61: {CoreID: 21, SocketID: 2, NUMANodeID: 1},
			62: {CoreID: 22, SocketID: 2, NUMANodeID: 1},
			63: {CoreID: 23, SocketID: 2, NUMANodeID: 1},
			64: {CoreID: 24, SocketID: 2, NUMANodeID: 1},
			65: {CoreID: 25, SocketID: 2, NUMANodeID: 1},
			66: {CoreID: 26, SocketID: 2, NUMANodeID: 1},
			67: {CoreID: 27, SocketID: 2, NUMANodeID: 1},
			68: {CoreID: 28, SocketID: 2, NUMANodeID: 1},
			69: {CoreID: 29, SocketID: 2, NUMANodeID: 1},
			70: {CoreID: 30, SocketID: 3, NUMANodeID: 1},
			71: {CoreID: 31, SocketID: 3, NUMANodeID: 1},
			72: {CoreID: 32, SocketID: 3, NUMANodeID: 1},
			73: {CoreID: 33, SocketID: 3, NUMANodeID: 1},
			74: {CoreID: 34, SocketID: 3, NUMANodeID: 1},
			75: {CoreID: 35, SocketID: 3, NUMANodeID: 1},
			76: {CoreID: 36, SocketID: 3, NUMANodeID: 1},
			77: {CoreID: 37, SocketID: 3, NUMANodeID: 1},
			78: {CoreID: 38, SocketID: 3, NUMANodeID: 1},
			79: {CoreID: 39, SocketID: 3, NUMANodeID: 1},
		},
	}

	/*
		Topology from dual AMD EPYC 7742 64-Core Processor; lscpu excerpt
		CPU(s):              256
		On-line CPU(s) list: 0-255
		Thread(s) per core:  2
		Core(s) per socket:  64
		Socket(s):           2
		NUMA node(s):        8 (NPS=4)
		NUMA node0 CPU(s):   0-15,128-143
		NUMA node1 CPU(s):   16-31,144-159
		NUMA node2 CPU(s):   32-47,160-175
		NUMA node3 CPU(s):   48-63,176-191
		NUMA node4 CPU(s):   64-79,192-207
		NUMA node5 CPU(s):   80-95,208-223
		NUMA node6 CPU(s):   96-111,224-239
		NUMA node7 CPU(s):   112-127,240-255
	*/
	topoDualSocketMultiNumaPerSocketHTLarge = &machine.CPUTopology{
		NumCPUs:      256,
		NumSockets:   2,
		NumCores:     128,
		NumNUMANodes: 8,
		CPUDetails: map[int]machine.CPUTopoInfo{
			0:   {CoreID: 0, SocketID: 0, NUMANodeID: 0},
			1:   {CoreID: 1, SocketID: 0, NUMANodeID: 0},
			2:   {CoreID: 2, SocketID: 0, NUMANodeID: 0},
			3:   {CoreID: 3, SocketID: 0, NUMANodeID: 0},
			4:   {CoreID: 4, SocketID: 0, NUMANodeID: 0},
			5:   {CoreID: 5, SocketID: 0, NUMANodeID: 0},
			6:   {CoreID: 6, SocketID: 0, NUMANodeID: 0},
			7:   {CoreID: 7, SocketID: 0, NUMANodeID: 0},
			8:   {CoreID: 8, SocketID: 0, NUMANodeID: 0},
			9:   {CoreID: 9, SocketID: 0, NUMANodeID: 0},
			10:  {CoreID: 10, SocketID: 0, NUMANodeID: 0},
			11:  {CoreID: 11, SocketID: 0, NUMANodeID: 0},
			12:  {CoreID: 12, SocketID: 0, NUMANodeID: 0},
			13:  {CoreID: 13, SocketID: 0, NUMANodeID: 0},
			14:  {CoreID: 14, SocketID: 0, NUMANodeID: 0},
			15:  {CoreID: 15, SocketID: 0, NUMANodeID: 0},
			16:  {CoreID: 16, SocketID: 0, NUMANodeID: 1},
			17:  {CoreID: 17, SocketID: 0, NUMANodeID: 1},
			18:  {CoreID: 18, SocketID: 0, NUMANodeID: 1},
			19:  {CoreID: 19, SocketID: 0, NUMANodeID: 1},
			20:  {CoreID: 20, SocketID: 0, NUMANodeID: 1},
			21:  {CoreID: 21, SocketID: 0, NUMANodeID: 1},
			22:  {CoreID: 22, SocketID: 0, NUMANodeID: 1},
			23:  {CoreID: 23, SocketID: 0, NUMANodeID: 1},
			24:  {CoreID: 24, SocketID: 0, NUMANodeID: 1},
			25:  {CoreID: 25, SocketID: 0, NUMANodeID: 1},
			26:  {CoreID: 26, SocketID: 0, NUMANodeID: 1},
			27:  {CoreID: 27, SocketID: 0, NUMANodeID: 1},
			28:  {CoreID: 28, SocketID: 0, NUMANodeID: 1},
			29:  {CoreID: 29, SocketID: 0, NUMANodeID: 1},
			30:  {CoreID: 30, SocketID: 0, NUMANodeID: 1},
			31:  {CoreID: 31, SocketID: 0, NUMANodeID: 1},
			32:  {CoreID: 32, SocketID: 0, NUMANodeID: 2},
			33:  {CoreID: 33, SocketID: 0, NUMANodeID: 2},
			34:  {CoreID: 34, SocketID: 0, NUMANodeID: 2},
			35:  {CoreID: 35, SocketID: 0, NUMANodeID: 2},
			36:  {CoreID: 36, SocketID: 0, NUMANodeID: 2},
			37:  {CoreID: 37, SocketID: 0, NUMANodeID: 2},
			38:  {CoreID: 38, SocketID: 0, NUMANodeID: 2},
			39:  {CoreID: 39, SocketID: 0, NUMANodeID: 2},
			40:  {CoreID: 40, SocketID: 0, NUMANodeID: 2},
			41:  {CoreID: 41, SocketID: 0, NUMANodeID: 2},
			42:  {CoreID: 42, SocketID: 0, NUMANodeID: 2},
			43:  {CoreID: 43, SocketID: 0, NUMANodeID: 2},
			44:  {CoreID: 44, SocketID: 0, NUMANodeID: 2},
			45:  {CoreID: 45, SocketID: 0, NUMANodeID: 2},
			46:  {CoreID: 46, SocketID: 0, NUMANodeID: 2},
			47:  {CoreID: 47, SocketID: 0, NUMANodeID: 2},
			48:  {CoreID: 48, SocketID: 0, NUMANodeID: 3},
			49:  {CoreID: 49, SocketID: 0, NUMANodeID: 3},
			50:  {CoreID: 50, SocketID: 0, NUMANodeID: 3},
			51:  {CoreID: 51, SocketID: 0, NUMANodeID: 3},
			52:  {CoreID: 52, SocketID: 0, NUMANodeID: 3},
			53:  {CoreID: 53, SocketID: 0, NUMANodeID: 3},
			54:  {CoreID: 54, SocketID: 0, NUMANodeID: 3},
			55:  {CoreID: 55, SocketID: 0, NUMANodeID: 3},
			56:  {CoreID: 56, SocketID: 0, NUMANodeID: 3},
			57:  {CoreID: 57, SocketID: 0, NUMANodeID: 3},
			58:  {CoreID: 58, SocketID: 0, NUMANodeID: 3},
			59:  {CoreID: 59, SocketID: 0, NUMANodeID: 3},
			60:  {CoreID: 60, SocketID: 0, NUMANodeID: 3},
			61:  {CoreID: 61, SocketID: 0, NUMANodeID: 3},
			62:  {CoreID: 62, SocketID: 0, NUMANodeID: 3},
			63:  {CoreID: 63, SocketID: 0, NUMANodeID: 3},
			64:  {CoreID: 64, SocketID: 1, NUMANodeID: 4},
			65:  {CoreID: 65, SocketID: 1, NUMANodeID: 4},
			66:  {CoreID: 66, SocketID: 1, NUMANodeID: 4},
			67:  {CoreID: 67, SocketID: 1, NUMANodeID: 4},
			68:  {CoreID: 68, SocketID: 1, NUMANodeID: 4},
			69:  {CoreID: 69, SocketID: 1, NUMANodeID: 4},
			70:  {CoreID: 70, SocketID: 1, NUMANodeID: 4},
			71:  {CoreID: 71, SocketID: 1, NUMANodeID: 4},
			72:  {CoreID: 72, SocketID: 1, NUMANodeID: 4},
			73:  {CoreID: 73, SocketID: 1, NUMANodeID: 4},
			74:  {CoreID: 74, SocketID: 1, NUMANodeID: 4},
			75:  {CoreID: 75, SocketID: 1, NUMANodeID: 4},
			76:  {CoreID: 76, SocketID: 1, NUMANodeID: 4},
			77:  {CoreID: 77, SocketID: 1, NUMANodeID: 4},
			78:  {CoreID: 78, SocketID: 1, NUMANodeID: 4},
			79:  {CoreID: 79, SocketID: 1, NUMANodeID: 4},
			80:  {CoreID: 80, SocketID: 1, NUMANodeID: 5},
			81:  {CoreID: 81, SocketID: 1, NUMANodeID: 5},
			82:  {CoreID: 82, SocketID: 1, NUMANodeID: 5},
			83:  {CoreID: 83, SocketID: 1, NUMANodeID: 5},
			84:  {CoreID: 84, SocketID: 1, NUMANodeID: 5},
			85:  {CoreID: 85, SocketID: 1, NUMANodeID: 5},
			86:  {CoreID: 86, SocketID: 1, NUMANodeID: 5},
			87:  {CoreID: 87, SocketID: 1, NUMANodeID: 5},
			88:  {CoreID: 88, SocketID: 1, NUMANodeID: 5},
			89:  {CoreID: 89, SocketID: 1, NUMANodeID: 5},
			90:  {CoreID: 90, SocketID: 1, NUMANodeID: 5},
			91:  {CoreID: 91, SocketID: 1, NUMANodeID: 5},
			92:  {CoreID: 92, SocketID: 1, NUMANodeID: 5},
			93:  {CoreID: 93, SocketID: 1, NUMANodeID: 5},
			94:  {CoreID: 94, SocketID: 1, NUMANodeID: 5},
			95:  {CoreID: 95, SocketID: 1, NUMANodeID: 5},
			96:  {CoreID: 96, SocketID: 1, NUMANodeID: 6},
			97:  {CoreID: 97, SocketID: 1, NUMANodeID: 6},
			98:  {CoreID: 98, SocketID: 1, NUMANodeID: 6},
			99:  {CoreID: 99, SocketID: 1, NUMANodeID: 6},
			100: {CoreID: 100, SocketID: 1, NUMANodeID: 6},
			101: {CoreID: 101, SocketID: 1, NUMANodeID: 6},
			102: {CoreID: 102, SocketID: 1, NUMANodeID: 6},
			103: {CoreID: 103, SocketID: 1, NUMANodeID: 6},
			104: {CoreID: 104, SocketID: 1, NUMANodeID: 6},
			105: {CoreID: 105, SocketID: 1, NUMANodeID: 6},
			106: {CoreID: 106, SocketID: 1, NUMANodeID: 6},
			107: {CoreID: 107, SocketID: 1, NUMANodeID: 6},
			108: {CoreID: 108, SocketID: 1, NUMANodeID: 6},
			109: {CoreID: 109, SocketID: 1, NUMANodeID: 6},
			110: {CoreID: 110, SocketID: 1, NUMANodeID: 6},
			111: {CoreID: 111, SocketID: 1, NUMANodeID: 6},
			112: {CoreID: 112, SocketID: 1, NUMANodeID: 7},
			113: {CoreID: 113, SocketID: 1, NUMANodeID: 7},
			114: {CoreID: 114, SocketID: 1, NUMANodeID: 7},
			115: {CoreID: 115, SocketID: 1, NUMANodeID: 7},
			116: {CoreID: 116, SocketID: 1, NUMANodeID: 7},
			117: {CoreID: 117, SocketID: 1, NUMANodeID: 7},
			118: {CoreID: 118, SocketID: 1, NUMANodeID: 7},
			119: {CoreID: 119, SocketID: 1, NUMANodeID: 7},
			120: {CoreID: 120, SocketID: 1, NUMANodeID: 7},
			121: {CoreID: 121, SocketID: 1, NUMANodeID: 7},
			122: {CoreID: 122, SocketID: 1, NUMANodeID: 7},
			123: {CoreID: 123, SocketID: 1, NUMANodeID: 7},
			124: {CoreID: 124, SocketID: 1, NUMANodeID: 7},
			125: {CoreID: 125, SocketID: 1, NUMANodeID: 7},
			126: {CoreID: 126, SocketID: 1, NUMANodeID: 7},
			127: {CoreID: 127, SocketID: 1, NUMANodeID: 7},
			128: {CoreID: 0, SocketID: 0, NUMANodeID: 0},
			129: {CoreID: 1, SocketID: 0, NUMANodeID: 0},
			130: {CoreID: 2, SocketID: 0, NUMANodeID: 0},
			131: {CoreID: 3, SocketID: 0, NUMANodeID: 0},
			132: {CoreID: 4, SocketID: 0, NUMANodeID: 0},
			133: {CoreID: 5, SocketID: 0, NUMANodeID: 0},
			134: {CoreID: 6, SocketID: 0, NUMANodeID: 0},
			135: {CoreID: 7, SocketID: 0, NUMANodeID: 0},
			136: {CoreID: 8, SocketID: 0, NUMANodeID: 0},
			137: {CoreID: 9, SocketID: 0, NUMANodeID: 0},
			138: {CoreID: 10, SocketID: 0, NUMANodeID: 0},
			139: {CoreID: 11, SocketID: 0, NUMANodeID: 0},
			140: {CoreID: 12, SocketID: 0, NUMANodeID: 0},
			141: {CoreID: 13, SocketID: 0, NUMANodeID: 0},
			142: {CoreID: 14, SocketID: 0, NUMANodeID: 0},
			143: {CoreID: 15, SocketID: 0, NUMANodeID: 0},
			144: {CoreID: 16, SocketID: 0, NUMANodeID: 1},
			145: {CoreID: 17, SocketID: 0, NUMANodeID: 1},
			146: {CoreID: 18, SocketID: 0, NUMANodeID: 1},
			147: {CoreID: 19, SocketID: 0, NUMANodeID: 1},
			148: {CoreID: 20, SocketID: 0, NUMANodeID: 1},
			149: {CoreID: 21, SocketID: 0, NUMANodeID: 1},
			150: {CoreID: 22, SocketID: 0, NUMANodeID: 1},
			151: {CoreID: 23, SocketID: 0, NUMANodeID: 1},
			152: {CoreID: 24, SocketID: 0, NUMANodeID: 1},
			153: {CoreID: 25, SocketID: 0, NUMANodeID: 1},
			154: {CoreID: 26, SocketID: 0, NUMANodeID: 1},
			155: {CoreID: 27, SocketID: 0, NUMANodeID: 1},
			156: {CoreID: 28, SocketID: 0, NUMANodeID: 1},
			157: {CoreID: 29, SocketID: 0, NUMANodeID: 1},
			158: {CoreID: 30, SocketID: 0, NUMANodeID: 1},
			159: {CoreID: 31, SocketID: 0, NUMANodeID: 1},
			160: {CoreID: 32, SocketID: 0, NUMANodeID: 2},
			161: {CoreID: 33, SocketID: 0, NUMANodeID: 2},
			162: {CoreID: 34, SocketID: 0, NUMANodeID: 2},
			163: {CoreID: 35, SocketID: 0, NUMANodeID: 2},
			164: {CoreID: 36, SocketID: 0, NUMANodeID: 2},
			165: {CoreID: 37, SocketID: 0, NUMANodeID: 2},
			166: {CoreID: 38, SocketID: 0, NUMANodeID: 2},
			167: {CoreID: 39, SocketID: 0, NUMANodeID: 2},
			168: {CoreID: 40, SocketID: 0, NUMANodeID: 2},
			169: {CoreID: 41, SocketID: 0, NUMANodeID: 2},
			170: {CoreID: 42, SocketID: 0, NUMANodeID: 2},
			171: {CoreID: 43, SocketID: 0, NUMANodeID: 2},
			172: {CoreID: 44, SocketID: 0, NUMANodeID: 2},
			173: {CoreID: 45, SocketID: 0, NUMANodeID: 2},
			174: {CoreID: 46, SocketID: 0, NUMANodeID: 2},
			175: {CoreID: 47, SocketID: 0, NUMANodeID: 2},
			176: {CoreID: 48, SocketID: 0, NUMANodeID: 3},
			177: {CoreID: 49, SocketID: 0, NUMANodeID: 3},
			178: {CoreID: 50, SocketID: 0, NUMANodeID: 3},
			179: {CoreID: 51, SocketID: 0, NUMANodeID: 3},
			180: {CoreID: 52, SocketID: 0, NUMANodeID: 3},
			181: {CoreID: 53, SocketID: 0, NUMANodeID: 3},
			182: {CoreID: 54, SocketID: 0, NUMANodeID: 3},
			183: {CoreID: 55, SocketID: 0, NUMANodeID: 3},
			184: {CoreID: 56, SocketID: 0, NUMANodeID: 3},
			185: {CoreID: 57, SocketID: 0, NUMANodeID: 3},
			186: {CoreID: 58, SocketID: 0, NUMANodeID: 3},
			187: {CoreID: 59, SocketID: 0, NUMANodeID: 3},
			188: {CoreID: 60, SocketID: 0, NUMANodeID: 3},
			189: {CoreID: 61, SocketID: 0, NUMANodeID: 3},
			190: {CoreID: 62, SocketID: 0, NUMANodeID: 3},
			191: {CoreID: 63, SocketID: 0, NUMANodeID: 3},
			192: {CoreID: 64, SocketID: 1, NUMANodeID: 4},
			193: {CoreID: 65, SocketID: 1, NUMANodeID: 4},
			194: {CoreID: 66, SocketID: 1, NUMANodeID: 4},
			195: {CoreID: 67, SocketID: 1, NUMANodeID: 4},
			196: {CoreID: 68, SocketID: 1, NUMANodeID: 4},
			197: {CoreID: 69, SocketID: 1, NUMANodeID: 4},
			198: {CoreID: 70, SocketID: 1, NUMANodeID: 4},
			199: {CoreID: 71, SocketID: 1, NUMANodeID: 4},
			200: {CoreID: 72, SocketID: 1, NUMANodeID: 4},
			201: {CoreID: 73, SocketID: 1, NUMANodeID: 4},
			202: {CoreID: 74, SocketID: 1, NUMANodeID: 4},
			203: {CoreID: 75, SocketID: 1, NUMANodeID: 4},
			204: {CoreID: 76, SocketID: 1, NUMANodeID: 4},
			205: {CoreID: 77, SocketID: 1, NUMANodeID: 4},
			206: {CoreID: 78, SocketID: 1, NUMANodeID: 4},
			207: {CoreID: 79, SocketID: 1, NUMANodeID: 4},
			208: {CoreID: 80, SocketID: 1, NUMANodeID: 5},
			209: {CoreID: 81, SocketID: 1, NUMANodeID: 5},
			210: {CoreID: 82, SocketID: 1, NUMANodeID: 5},
			211: {CoreID: 83, SocketID: 1, NUMANodeID: 5},
			212: {CoreID: 84, SocketID: 1, NUMANodeID: 5},
			213: {CoreID: 85, SocketID: 1, NUMANodeID: 5},
			214: {CoreID: 86, SocketID: 1, NUMANodeID: 5},
			215: {CoreID: 87, SocketID: 1, NUMANodeID: 5},
			216: {CoreID: 88, SocketID: 1, NUMANodeID: 5},
			217: {CoreID: 89, SocketID: 1, NUMANodeID: 5},
			218: {CoreID: 90, SocketID: 1, NUMANodeID: 5},
			219: {CoreID: 91, SocketID: 1, NUMANodeID: 5},
			220: {CoreID: 92, SocketID: 1, NUMANodeID: 5},
			221: {CoreID: 93, SocketID: 1, NUMANodeID: 5},
			222: {CoreID: 94, SocketID: 1, NUMANodeID: 5},
			223: {CoreID: 95, SocketID: 1, NUMANodeID: 5},
			224: {CoreID: 96, SocketID: 1, NUMANodeID: 6},
			225: {CoreID: 97, SocketID: 1, NUMANodeID: 6},
			226: {CoreID: 98, SocketID: 1, NUMANodeID: 6},
			227: {CoreID: 99, SocketID: 1, NUMANodeID: 6},
			228: {CoreID: 100, SocketID: 1, NUMANodeID: 6},
			229: {CoreID: 101, SocketID: 1, NUMANodeID: 6},
			230: {CoreID: 102, SocketID: 1, NUMANodeID: 6},
			231: {CoreID: 103, SocketID: 1, NUMANodeID: 6},
			232: {CoreID: 104, SocketID: 1, NUMANodeID: 6},
			233: {CoreID: 105, SocketID: 1, NUMANodeID: 6},
			234: {CoreID: 106, SocketID: 1, NUMANodeID: 6},
			235: {CoreID: 107, SocketID: 1, NUMANodeID: 6},
			236: {CoreID: 108, SocketID: 1, NUMANodeID: 6},
			237: {CoreID: 109, SocketID: 1, NUMANodeID: 6},
			238: {CoreID: 110, SocketID: 1, NUMANodeID: 6},
			239: {CoreID: 111, SocketID: 1, NUMANodeID: 6},
			240: {CoreID: 112, SocketID: 1, NUMANodeID: 7},
			241: {CoreID: 113, SocketID: 1, NUMANodeID: 7},
			242: {CoreID: 114, SocketID: 1, NUMANodeID: 7},
			243: {CoreID: 115, SocketID: 1, NUMANodeID: 7},
			244: {CoreID: 116, SocketID: 1, NUMANodeID: 7},
			245: {CoreID: 117, SocketID: 1, NUMANodeID: 7},
			246: {CoreID: 118, SocketID: 1, NUMANodeID: 7},
			247: {CoreID: 119, SocketID: 1, NUMANodeID: 7},
			248: {CoreID: 120, SocketID: 1, NUMANodeID: 7},
			249: {CoreID: 121, SocketID: 1, NUMANodeID: 7},
			250: {CoreID: 122, SocketID: 1, NUMANodeID: 7},
			251: {CoreID: 123, SocketID: 1, NUMANodeID: 7},
			252: {CoreID: 124, SocketID: 1, NUMANodeID: 7},
			253: {CoreID: 125, SocketID: 1, NUMANodeID: 7},
			254: {CoreID: 126, SocketID: 1, NUMANodeID: 7},
			255: {CoreID: 127, SocketID: 1, NUMANodeID: 7},
		},
	}
)

func TestCPUAccumulatorFreeSockets(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		description   string
		topo          *machine.CPUTopology
		availableCPUs machine.CPUSet
		expect        []int
	}{
		{
			"single socket HT, 1 socket free",
			topoSingleSocketHT,
			machine.NewCPUSet(0, 1, 2, 3, 4, 5, 6, 7),
			[]int{0},
		},
		{
			"single socket HT, 0 sockets free",
			topoSingleSocketHT,
			machine.NewCPUSet(1, 2, 3, 4, 5, 6, 7),
			[]int{},
		},
		{
			"dual socket HT, 2 sockets free",
			topoDualSocketHT,
			machine.NewCPUSet(0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11),
			[]int{0, 1},
		},
		{
			"dual socket HT, 1 socket free",
			topoDualSocketHT,
			machine.NewCPUSet(0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 11),
			[]int{1},
		},
		{
			"dual socket HT, 0 sockets free",
			topoDualSocketHT,
			machine.NewCPUSet(0, 2, 3, 4, 5, 6, 7, 8, 9, 11),
			[]int{},
		},
		{
			"dual socket, multi numa per socket, HT, 2 sockets free",
			topoDualSocketMultiNumaPerSocketHT,
			mustParseCPUSet(t, "0-79"),
			[]int{0, 1},
		},
		{
			"dual socket, multi numa per socket, HT, 1 sockets free",
			topoDualSocketMultiNumaPerSocketHT,
			mustParseCPUSet(t, "1-79"),
			[]int{1},
		},
		{
			"dual socket, multi numa per socket, HT, 0 sockets free",
			topoDualSocketMultiNumaPerSocketHT,
			mustParseCPUSet(t, "1-78"),
			[]int{},
		},
		{
			"dual numa, multi socket per per socket, HT, 4 sockets free",
			fakeTopoMultiSocketDualSocketPerNumaHT,
			mustParseCPUSet(t, "0-79"),
			[]int{0, 1, 2, 3},
		},
		{
			"dual numa, multi socket per per socket, HT, 3 sockets free",
			fakeTopoMultiSocketDualSocketPerNumaHT,
			mustParseCPUSet(t, "0-19,21-79"),
			[]int{0, 1, 3},
		},
		{
			"dual numa, multi socket per per socket, HT, 2 sockets free",
			fakeTopoMultiSocketDualSocketPerNumaHT,
			mustParseCPUSet(t, "0-59,61-78"),
			[]int{0, 1},
		},
		{
			"dual numa, multi socket per per socket, HT, 1 sockets free",
			fakeTopoMultiSocketDualSocketPerNumaHT,
			mustParseCPUSet(t, "1-19,21-38,41-60,61-78"),
			[]int{1},
		},
		{
			"dual numa, multi socket per per socket, HT, 0 sockets free",
			fakeTopoMultiSocketDualSocketPerNumaHT,
			mustParseCPUSet(t, "0-40,42-49,51-68,71-79"),
			[]int{},
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.description, func(t *testing.T) {
			t.Parallel()

			acc := newCPUAccumulator(tc.topo, tc.availableCPUs, 0)
			result := acc.freeSockets()
			sort.Ints(result)
			if !reflect.DeepEqual(result, tc.expect) {
				t.Errorf("expected %v to equal %v", result, tc.expect)
			}
		})
	}
}

func TestCPUAccumulatorFreeNUMANodes(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		description   string
		topo          *machine.CPUTopology
		availableCPUs machine.CPUSet
		expect        []int
	}{
		{
			"single socket HT, 1 NUMA node free",
			topoSingleSocketHT,
			machine.NewCPUSet(0, 1, 2, 3, 4, 5, 6, 7),
			[]int{0},
		},
		{
			"single socket HT, 0 NUMA Node free",
			topoSingleSocketHT,
			machine.NewCPUSet(1, 2, 3, 4, 5, 6, 7),
			[]int{},
		},
		{
			"dual socket HT, 2 NUMA Node free",
			topoDualSocketHT,
			machine.NewCPUSet(0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11),
			[]int{0, 1},
		},
		{
			"dual socket HT, 1 NUMA Node free",
			topoDualSocketHT,
			machine.NewCPUSet(0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 11),
			[]int{1},
		},
		{
			"dual socket HT, 0 NUMA node free",
			topoDualSocketHT,
			machine.NewCPUSet(0, 2, 3, 4, 5, 6, 7, 8, 9, 11),
			[]int{},
		},
		{
			"dual socket, multi numa per socket, HT, 4 NUMA Node free",
			topoDualSocketMultiNumaPerSocketHT,
			mustParseCPUSet(t, "0-79"),
			[]int{0, 1, 2, 3},
		},
		{
			"dual socket, multi numa per socket, HT, 3 NUMA node free",
			topoDualSocketMultiNumaPerSocketHT,
			mustParseCPUSet(t, "1-79"),
			[]int{1, 2, 3},
		},
		{
			"dual socket, multi numa per socket, HT, 2 NUMA node free",
			topoDualSocketMultiNumaPerSocketHT,
			mustParseCPUSet(t, "1-9,11-79"),
			[]int{2, 3},
		},
		{
			"dual socket, multi numa per socket, HT, 1 NUMA node free",
			topoDualSocketMultiNumaPerSocketHT,
			mustParseCPUSet(t, "1-9,11-59,61-79"),
			[]int{3},
		},
		{
			"dual socket, multi numa per socket, HT, 0 NUMA node free",
			topoDualSocketMultiNumaPerSocketHT,
			mustParseCPUSet(t, "1-9,11-59,61-78"),
			[]int{},
		},
		{
			"dual numa, multi socket per per socket, HT, 2 NUMA node free",
			fakeTopoMultiSocketDualSocketPerNumaHT,
			mustParseCPUSet(t, "0-79"),
			[]int{0, 1},
		},
		{
			"dual numa, multi socket per per socket, HT, 1 NUMA node free",
			fakeTopoMultiSocketDualSocketPerNumaHT,
			mustParseCPUSet(t, "0-9,11-79"),
			[]int{1},
		},
		{
			"dual numa, multi socket per per socket, HT, 0 sockets free",
			fakeTopoMultiSocketDualSocketPerNumaHT,
			mustParseCPUSet(t, "0-9,11-59,61-79"),
			[]int{},
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.description, func(t *testing.T) {
			t.Parallel()

			acc := newCPUAccumulator(tc.topo, tc.availableCPUs, 0)
			result := acc.freeNUMANodes()
			if !reflect.DeepEqual(result, tc.expect) {
				t.Errorf("expected %v to equal %v", result, tc.expect)
			}
		})
	}
}

func TestCPUAccumulatorFreeSocketsAndNUMANodes(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		description     string
		topo            *machine.CPUTopology
		availableCPUs   machine.CPUSet
		expectSockets   []int
		expectNUMANodes []int
	}{
		{
			"dual socket, multi numa per socket, HT, 2 Socket/4 NUMA Node free",
			topoDualSocketMultiNumaPerSocketHT,
			mustParseCPUSet(t, "0-79"),
			[]int{0, 1},
			[]int{0, 1, 2, 3},
		},
		{
			"dual socket, multi numa per socket, HT, 1 Socket/3 NUMA node free",
			topoDualSocketMultiNumaPerSocketHT,
			mustParseCPUSet(t, "1-79"),
			[]int{1},
			[]int{1, 2, 3},
		},
		{
			"dual socket, multi numa per socket, HT, 1 Socket/ 2 NUMA node free",
			topoDualSocketMultiNumaPerSocketHT,
			mustParseCPUSet(t, "1-9,11-79"),
			[]int{1},
			[]int{2, 3},
		},
		{
			"dual socket, multi numa per socket, HT, 0 Socket/ 2 NUMA node free",
			topoDualSocketMultiNumaPerSocketHT,
			mustParseCPUSet(t, "1-59,61-79"),
			[]int{},
			[]int{1, 3},
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.description, func(t *testing.T) {
			t.Parallel()

			acc := newCPUAccumulator(tc.topo, tc.availableCPUs, 0)
			resultNUMANodes := acc.freeNUMANodes()
			if !reflect.DeepEqual(resultNUMANodes, tc.expectNUMANodes) {
				t.Errorf("expected NUMA Nodes %v to equal %v", resultNUMANodes, tc.expectNUMANodes)
			}
			resultSockets := acc.freeSockets()
			if !reflect.DeepEqual(resultSockets, tc.expectSockets) {
				t.Errorf("expected Sockets %v to equal %v", resultSockets, tc.expectSockets)
			}
		})
	}
}

func TestCPUAccumulatorFreeCores(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		description   string
		topo          *machine.CPUTopology
		availableCPUs machine.CPUSet
		expect        []int
	}{
		{
			"single socket HT, 4 cores free",
			topoSingleSocketHT,
			machine.NewCPUSet(0, 1, 2, 3, 4, 5, 6, 7),
			[]int{0, 1, 2, 3},
		},
		{
			"single socket HT, 3 cores free",
			topoSingleSocketHT,
			machine.NewCPUSet(0, 1, 2, 4, 5, 6),
			[]int{0, 1, 2},
		},
		{
			"single socket HT, 3 cores free (1 partially consumed)",
			topoSingleSocketHT,
			machine.NewCPUSet(0, 1, 2, 3, 4, 5, 6),
			[]int{0, 1, 2},
		},
		{
			"single socket HT, 0 cores free",
			topoSingleSocketHT,
			machine.NewCPUSet(),
			[]int{},
		},
		{
			"single socket HT, 0 cores free (4 partially consumed)",
			topoSingleSocketHT,
			machine.NewCPUSet(0, 1, 2, 3),
			[]int{},
		},
		{
			"dual socket HT, 6 cores free",
			topoDualSocketHT,
			machine.NewCPUSet(0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11),
			[]int{0, 2, 4, 1, 3, 5},
		},
		{
			"dual socket HT, 5 cores free (1 consumed from socket 0)",
			topoDualSocketHT,
			machine.NewCPUSet(2, 1, 3, 4, 5, 7, 8, 9, 10, 11),
			[]int{2, 4, 1, 3, 5},
		},
		{
			"dual socket HT, 4 cores free (1 consumed from each socket)",
			topoDualSocketHT,
			machine.NewCPUSet(2, 3, 4, 5, 8, 9, 10, 11),
			[]int{2, 4, 3, 5},
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.description, func(t *testing.T) {
			t.Parallel()

			acc := newCPUAccumulator(tc.topo, tc.availableCPUs, 0)
			result := acc.freeCores()
			if !reflect.DeepEqual(result, tc.expect) {
				t.Errorf("expected %v to equal %v", result, tc.expect)
			}
		})
	}
}

func TestCPUAccumulatorFreeCPUs(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		description   string
		topo          *machine.CPUTopology
		availableCPUs machine.CPUSet
		expect        []int
	}{
		{
			"single socket HT, 8 cpus free",
			topoSingleSocketHT,
			machine.NewCPUSet(0, 1, 2, 3, 4, 5, 6, 7),
			[]int{0, 4, 1, 5, 2, 6, 3, 7},
		},
		{
			"single socket HT, 5 cpus free",
			topoSingleSocketHT,
			machine.NewCPUSet(3, 4, 5, 6, 7),
			[]int{4, 5, 6, 3, 7},
		},
		{
			"dual socket HT, 12 cpus free",
			topoDualSocketHT,
			machine.NewCPUSet(0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11),
			[]int{0, 6, 2, 8, 4, 10, 1, 7, 3, 9, 5, 11},
		},
		{
			"dual socket HT, 11 cpus free",
			topoDualSocketHT,
			machine.NewCPUSet(1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11),
			[]int{6, 2, 8, 4, 10, 1, 7, 3, 9, 5, 11},
		},
		{
			"dual socket HT, 10 cpus free",
			topoDualSocketHT,
			machine.NewCPUSet(1, 2, 3, 4, 5, 7, 8, 9, 10, 11),
			[]int{2, 8, 4, 10, 1, 7, 3, 9, 5, 11},
		},
		{
			"triple socket HT, 12 cpus free",
			topoTripleSocketHT,
			machine.NewCPUSet(0, 1, 2, 3, 6, 7, 8, 9, 10, 11, 12, 13),
			[]int{12, 13, 0, 1, 2, 3, 6, 7, 8, 9, 10, 11},
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.description, func(t *testing.T) {
			t.Parallel()

			acc := newCPUAccumulator(tc.topo, tc.availableCPUs, 0)
			result := acc.freeCPUs()
			if !reflect.DeepEqual(result, tc.expect) {
				t.Errorf("expected %v to equal %v", result, tc.expect)
			}
		})
	}
}

func TestCPUAccumulatorTake(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		description     string
		topo            *machine.CPUTopology
		availableCPUs   machine.CPUSet
		takeCPUs        []machine.CPUSet
		numCPUs         int
		expectSatisfied bool
		expectFailed    bool
	}{
		{
			"take 0 cpus from a single socket HT, require 1",
			topoSingleSocketHT,
			machine.NewCPUSet(0, 1, 2, 3, 4, 5, 6, 7),
			[]machine.CPUSet{machine.NewCPUSet()},
			1,
			false,
			false,
		},
		{
			"take 0 cpus from a single socket HT, require 1, none available",
			topoSingleSocketHT,
			machine.NewCPUSet(),
			[]machine.CPUSet{machine.NewCPUSet()},
			1,
			false,
			true,
		},
		{
			"take 1 cpu from a single socket HT, require 1",
			topoSingleSocketHT,
			machine.NewCPUSet(0, 1, 2, 3, 4, 5, 6, 7),
			[]machine.CPUSet{machine.NewCPUSet(0)},
			1,
			true,
			false,
		},
		{
			"take 1 cpu from a single socket HT, require 2",
			topoSingleSocketHT,
			machine.NewCPUSet(0, 1, 2, 3, 4, 5, 6, 7),
			[]machine.CPUSet{machine.NewCPUSet(0)},
			2,
			false,
			false,
		},
		{
			"take 2 cpu from a single socket HT, require 4, expect failed",
			topoSingleSocketHT,
			machine.NewCPUSet(0, 1, 2),
			[]machine.CPUSet{machine.NewCPUSet(0), machine.NewCPUSet(1)},
			4,
			false,
			true,
		},
		{
			"take all cpus one at a time from a single socket HT, require 8",
			topoSingleSocketHT,
			machine.NewCPUSet(0, 1, 2, 3, 4, 5, 6, 7),
			[]machine.CPUSet{
				machine.NewCPUSet(0),
				machine.NewCPUSet(1),
				machine.NewCPUSet(2),
				machine.NewCPUSet(3),
				machine.NewCPUSet(4),
				machine.NewCPUSet(5),
				machine.NewCPUSet(6),
				machine.NewCPUSet(7),
			},
			8,
			true,
			false,
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.description, func(t *testing.T) {
			t.Parallel()

			acc := newCPUAccumulator(tc.topo, tc.availableCPUs, tc.numCPUs)
			totalTaken := 0
			for _, cpus := range tc.takeCPUs {
				acc.take(cpus)
				totalTaken += cpus.Size()
			}
			if tc.expectSatisfied != acc.isSatisfied() {
				t.Errorf("expected acc.isSatisfied() to be %t", tc.expectSatisfied)
			}
			if tc.expectFailed != acc.isFailed() {
				t.Errorf("expected acc.isFailed() to be %t", tc.expectFailed)
			}
			for _, cpus := range tc.takeCPUs {
				availableCPUs := acc.details.CPUs()
				if cpus.Intersection(availableCPUs).Size() > 0 {
					t.Errorf("expected intersection of taken cpus [%s] and acc.details.CPUs() [%s] to be empty", cpus, availableCPUs)
				}
				if !cpus.IsSubsetOf(acc.result) {
					t.Errorf("expected [%s] to be a subset of acc.result [%s]", cpus, acc.result)
				}
			}
			expNumCPUsNeeded := tc.numCPUs - totalTaken
			if acc.numCPUsNeeded != expNumCPUsNeeded {
				t.Errorf("expected acc.numCPUsNeeded to be %d (got %d)", expNumCPUsNeeded, acc.numCPUsNeeded)
			}
		})
	}
}

type takeByTopologyTestCase struct {
	description   string
	topo          *machine.CPUTopology
	availableCPUs machine.CPUSet
	numCPUs       int
	expErr        string
	expResult     machine.CPUSet
}

func commonTakeByTopologyTestCases(t *testing.T) []takeByTopologyTestCase {
	return []takeByTopologyTestCase{
		{
			"take more cpus than are available from single socket with HT",
			topoSingleSocketHT,
			machine.NewCPUSet(0, 2, 4, 6),
			5,
			"not enough cpus available to satisfy request",
			machine.NewCPUSet(),
		},
		{
			"take zero cpus from single socket with HT",
			topoSingleSocketHT,
			machine.NewCPUSet(0, 1, 2, 3, 4, 5, 6, 7),
			0,
			"",
			machine.NewCPUSet(),
		},
		{
			"take one cpu from single socket with HT",
			topoSingleSocketHT,
			machine.NewCPUSet(0, 1, 2, 3, 4, 5, 6, 7),
			1,
			"",
			machine.NewCPUSet(0),
		},
		{
			"take one cpu from single socket with HT, some cpus are taken",
			topoSingleSocketHT,
			machine.NewCPUSet(1, 3, 5, 6, 7),
			1,
			"",
			machine.NewCPUSet(6),
		},
		{
			"take two cpus from single socket with HT",
			topoSingleSocketHT,
			machine.NewCPUSet(0, 1, 2, 3, 4, 5, 6, 7),
			2,
			"",
			machine.NewCPUSet(0, 4),
		},
		{
			"take all cpus from single socket with HT",
			topoSingleSocketHT,
			machine.NewCPUSet(0, 1, 2, 3, 4, 5, 6, 7),
			8,
			"",
			machine.NewCPUSet(0, 1, 2, 3, 4, 5, 6, 7),
		},
		{
			"take two cpus from single socket with HT, only one core totally free",
			topoSingleSocketHT,
			machine.NewCPUSet(0, 1, 2, 3, 6),
			2,
			"",
			machine.NewCPUSet(2, 6),
		},
		{
			"take a socket of cpus from dual socket with HT",
			topoDualSocketHT,
			machine.NewCPUSet(0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11),
			6,
			"",
			machine.NewCPUSet(0, 2, 4, 6, 8, 10),
		},
		{
			"take a socket of cpus from dual socket with multi-numa-per-socket with HT",
			topoDualSocketMultiNumaPerSocketHT,
			mustParseCPUSet(t, "0-79"),
			40,
			"",
			mustParseCPUSet(t, "0-19,40-59"),
		},
		{
			"take a NUMA node of cpus from dual socket with multi-numa-per-socket with HT",
			topoDualSocketMultiNumaPerSocketHT,
			mustParseCPUSet(t, "0-79"),
			20,
			"",
			mustParseCPUSet(t, "0-9,40-49"),
		},
		{
			"take a NUMA node of cpus from dual socket with multi-numa-per-socket with HT, with 1 NUMA node already taken",
			topoDualSocketMultiNumaPerSocketHT,
			mustParseCPUSet(t, "10-39,50-79"),
			20,
			"",
			mustParseCPUSet(t, "10-19,50-59"),
		},
		{
			"take a socket and a NUMA node of cpus from dual socket with multi-numa-per-socket with HT",
			topoDualSocketMultiNumaPerSocketHT,
			mustParseCPUSet(t, "0-79"),
			60,
			"",
			mustParseCPUSet(t, "0-29,40-69"),
		},
		{
			"take a socket and a NUMA node of cpus from dual socket with multi-numa-per-socket with HT, a core taken",
			topoDualSocketMultiNumaPerSocketHT,
			mustParseCPUSet(t, "1-39,41-79"), // reserve the first (phys) core (0,40)
			60,
			"",
			mustParseCPUSet(t, "10-39,50-79"),
		},
	}
}

func TestTakeByTopologyNUMAPacked(t *testing.T) {
	t.Parallel()

	testCases := commonTakeByTopologyTestCases(t)
	testCases = append(testCases, []takeByTopologyTestCase{
		{
			"take one cpu from dual socket with HT - core from Socket 0",
			topoDualSocketHT,
			machine.NewCPUSet(1, 2, 3, 4, 5, 7, 8, 9, 10, 11),
			1,
			"",
			machine.NewCPUSet(2),
		},
		{
			"allocate 4 full cores with 3 coming from the first NUMA node (filling it up) and 1 coming from the second NUMA node",
			topoDualSocketHT,
			mustParseCPUSet(t, "0-11"),
			8,
			"",
			mustParseCPUSet(t, "0,6,2,8,4,10,1,7"),
		},
		{
			"allocate 32 full cores with 30 coming from the first 3 NUMA nodes (filling them up) and 2 coming from the fourth NUMA node",
			topoDualSocketMultiNumaPerSocketHT,
			mustParseCPUSet(t, "0-79"),
			64,
			"",
			mustParseCPUSet(t, "0-29,40-69,30,31,70,71"),
		},
	}...)

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.description, func(t *testing.T) {
			t.Parallel()

			result, err := TakeByTopologyNUMAPacked(tc.topo, tc.availableCPUs, tc.numCPUs)
			if tc.expErr != "" && err.Error() != tc.expErr {
				t.Errorf("expected error to be [%v] but it was [%v]", tc.expErr, err)
			}
			if !result.Equals(tc.expResult) {
				t.Errorf("expected result [%s] to equal [%s]", result, tc.expResult)
			}
		})
	}
}

type takeByTopologyExtendedTestCase struct {
	description   string
	topo          *machine.CPUTopology
	availableCPUs machine.CPUSet
	numCPUs       int
	cpuGroupSize  int
	expErr        string
	expResult     machine.CPUSet
}

func commonTakeByTopologyExtendedTestCases(t *testing.T) []takeByTopologyExtendedTestCase {
	var extendedTestCases []takeByTopologyExtendedTestCase

	testCases := commonTakeByTopologyTestCases(t)
	for _, tc := range testCases {
		extendedTestCases = append(extendedTestCases, takeByTopologyExtendedTestCase{
			tc.description,
			tc.topo,
			tc.availableCPUs,
			tc.numCPUs,
			1,
			tc.expErr,
			tc.expResult,
		})
	}

	extendedTestCases = append(extendedTestCases, []takeByTopologyExtendedTestCase{
		{
			"allocate 4 full cores with 2 distributed across each NUMA node",
			topoDualSocketHT,
			mustParseCPUSet(t, "0-11"),
			8,
			1,
			"",
			mustParseCPUSet(t, "0,6,2,8,1,7,3,9"),
		},
		{
			"allocate 32 full cores with 8 distributed across each NUMA node",
			topoDualSocketMultiNumaPerSocketHT,
			mustParseCPUSet(t, "0-79"),
			64,
			1,
			"",
			mustParseCPUSet(t, "0-7,10-17,20-27,30-37,40-47,50-57,60-67,70-77"),
		},
		{
			"allocate 24 full cores with 8 distributed across the first 3 NUMA nodes",
			topoDualSocketMultiNumaPerSocketHT,
			mustParseCPUSet(t, "0-79"),
			48,
			1,
			"",
			mustParseCPUSet(t, "0-7,10-17,20-27,40-47,50-57,60-67"),
		},
		{
			"allocate 24 full cores with 8 distributed across the first 3 NUMA nodes (taking all but 2 from the first NUMA node)",
			topoDualSocketMultiNumaPerSocketHT,
			mustParseCPUSet(t, "1-29,32-39,41-69,72-79"),
			48,
			1,
			"",
			mustParseCPUSet(t, "1-8,10-17,20-27,41-48,50-57,60-67"),
		},
		{
			"allocate 24 full cores with 8 distributed across the last 3 NUMA nodes (even though all 8 could be allocated from the first NUMA node)",
			topoDualSocketMultiNumaPerSocketHT,
			mustParseCPUSet(t, "2-29,31-39,42-69,71-79"),
			48,
			1,
			"",
			mustParseCPUSet(t, "10-17,20-27,31-38,50-57,60-67,71-78"),
		},
		{
			"allocate 8 full cores with 2 distributed across each NUMA node",
			topoDualSocketMultiNumaPerSocketHT,
			mustParseCPUSet(t, "0-2,10-12,20-22,30-32,40-41,50-51,60-61,70-71"),
			16,
			1,
			"",
			mustParseCPUSet(t, "0-1,10-11,20-21,30-31,40-41,50-51,60-61,70-71"),
		},
		{
			"allocate 8 full cores with 2 distributed across each NUMA node",
			topoDualSocketMultiNumaPerSocketHT,
			mustParseCPUSet(t, "0-2,10-12,20-22,30-32,40-41,50-51,60-61,70-71"),
			16,
			1,
			"",
			mustParseCPUSet(t, "0-1,10-11,20-21,30-31,40-41,50-51,60-61,70-71"),
		},
	}...)

	return extendedTestCases
}

func TestTakeByTopologyNUMADistributed(t *testing.T) {
	t.Parallel()

	testCases := commonTakeByTopologyExtendedTestCases(t)
	testCases = append(testCases, []takeByTopologyExtendedTestCase{
		{
			"take one cpu from dual socket with HT - core from Socket 0",
			topoDualSocketHT,
			machine.NewCPUSet(1, 2, 3, 4, 5, 7, 8, 9, 10, 11),
			1,
			1,
			"",
			machine.NewCPUSet(1),
		},
		{
			"take one cpu from dual socket with HT - core from Socket 0 - cpuGroupSize 2",
			topoDualSocketHT,
			machine.NewCPUSet(1, 2, 3, 4, 5, 7, 8, 9, 10, 11),
			1,
			2,
			"",
			machine.NewCPUSet(2),
		},
		{
			"allocate 13 full cores distributed across the first 2 NUMA nodes",
			topoDualSocketMultiNumaPerSocketHT,
			mustParseCPUSet(t, "0-79"),
			26,
			1,
			"",
			mustParseCPUSet(t, "0-6,10-16,40-45,50-55"),
		},
		{
			"allocate 13 full cores distributed across the first 2 NUMA nodes (cpuGroupSize 2)",
			topoDualSocketMultiNumaPerSocketHT,
			mustParseCPUSet(t, "0-79"),
			26,
			2,
			"",
			mustParseCPUSet(t, "0-6,10-15,40-46,50-55"),
		},
		{
			"allocate 31 full cores with 15 CPUs distributed across each NUMA node and 1 CPU spilling over to each of NUMA 0, 1",
			topoDualSocketMultiNumaPerSocketHT,
			mustParseCPUSet(t, "0-79"),
			62,
			1,
			"",
			mustParseCPUSet(t, "0-7,10-17,20-27,30-37,40-47,50-57,60-66,70-76"),
		},
		{
			"allocate 31 full cores with 14 CPUs distributed across each NUMA node and 2 CPUs spilling over to each of NUMA 0, 1, 2 (cpuGroupSize 2)",
			topoDualSocketMultiNumaPerSocketHT,
			mustParseCPUSet(t, "0-79"),
			62,
			2,
			"",
			mustParseCPUSet(t, "0-7,10-17,20-27,30-36,40-47,50-57,60-67,70-76"),
		},
		{
			"allocate 31 full cores with 15 CPUs distributed across each NUMA node and 1 CPU spilling over to each of NUMA 2, 3 (to keep balance)",
			topoDualSocketMultiNumaPerSocketHT,
			mustParseCPUSet(t, "0-8,10-18,20-39,40-48,50-58,60-79"),
			62,
			1,
			"",
			mustParseCPUSet(t, "0-7,10-17,20-27,30-37,40-46,50-56,60-67,70-77"),
		},
		{
			"allocate 31 full cores with 14 CPUs distributed across each NUMA node and 2 CPUs spilling over to each of NUMA 0, 2, 3 (to keep balance with cpuGroupSize 2)",
			topoDualSocketMultiNumaPerSocketHT,
			mustParseCPUSet(t, "0-8,10-18,20-39,40-48,50-58,60-79"),
			62,
			2,
			"",
			mustParseCPUSet(t, "0-7,10-16,20-27,30-37,40-47,50-56,60-67,70-77"),
		},
		{
			"ensure bestRemainder chosen with NUMA nodes that have enough CPUs to satisfy the request",
			topoDualSocketMultiNumaPerSocketHT,
			mustParseCPUSet(t, "0-3,10-13,20-23,30-36,40-43,50-53,60-63,70-76"),
			34,
			1,
			"",
			mustParseCPUSet(t, "0-3,10-13,20-23,30-34,40-43,50-53,60-63,70-74"),
		},
		{
			"ensure previous failure encountered on live machine has been fixed (1/1)",
			topoDualSocketMultiNumaPerSocketHTLarge,
			mustParseCPUSet(t, "0,128,30,31,158,159,43-47,171-175,62,63,190,191,75-79,203-207,94,96,222,223,101-111,229-239,126,127,254,255"),
			28,
			1,
			"",
			mustParseCPUSet(t, "43-47,75-79,96,101-105,171-174,203-206,229-232"),
		},
	}...)

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.description, func(t *testing.T) {
			t.Parallel()

			result, err := TakeByTopologyNUMADistributed(tc.topo, tc.availableCPUs, tc.numCPUs, tc.cpuGroupSize)
			if err != nil {
				if tc.expErr == "" {
					t.Errorf("unexpected error [%v]", err)
				}
				if tc.expErr != "" && err.Error() != tc.expErr {
					t.Errorf("expected error to be [%v] but it was [%v]", tc.expErr, err)
				}
				return
			}
			if !result.Equals(tc.expResult) {
				t.Errorf("expected result [%s] to equal [%s]", result, tc.expResult)
			}
		})
	}
}

func mustParseCPUSet(t *testing.T, s string) machine.CPUSet {
	cpus, err := machine.Parse(s)
	if err != nil {
		t.Errorf("parsing %q: %v", s, err)
	}
	return cpus
}

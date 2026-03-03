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

package reporter

import (
	"context"
	"encoding/json"
	"testing"

	cadvisorapi "github.com/google/cadvisor/info/v1"
	"github.com/stretchr/testify/assert"

	nodev1alpha1 "github.com/kubewharf/katalyst-api/pkg/apis/node/v1alpha1"
	"github.com/kubewharf/katalyst-api/pkg/plugins/skeleton"
	"github.com/kubewharf/katalyst-api/pkg/protocol/reporterplugin/v1alpha1"
	gpuconsts "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/consts"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/config/agent"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/global"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	metaagent "github.com/kubewharf/katalyst-core/pkg/metaserver/agent"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

func generateTestMetaServer(machineTopology []cadvisorapi.Node) *metaserver.MetaServer {
	return &metaserver.MetaServer{
		MetaAgent: &metaagent.MetaAgent{
			KatalystMachineInfo: &machine.KatalystMachineInfo{
				MachineInfo: &cadvisorapi.MachineInfo{
					Topology: machineTopology,
				},
			},
		},
	}
}

func TestGpuReporterPlugin_GetReportContent(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		deviceTopology  *machine.DeviceTopology
		machineTopology []cadvisorapi.Node
		expectedSpec    []*nodev1alpha1.Property
		expectedStatus  []*nodev1alpha1.TopologyZone
		expectedErr     bool
	}{
		{
			name: "Able to get report content for one dimension",
			// device topology of 1 dimension
			// gpu-0, gpu-1, gpu-2, gpu-3 are on numa-0
			// gpu-4, gpu-5, gpu-6, gpu-7 are on numa-1
			deviceTopology: &machine.DeviceTopology{
				PriorityDimensions: []string{"numa"},
				Devices: map[string]machine.DeviceInfo{
					"gpu-0": {
						NumaNodes: []int{0},
						DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
							{
								PriorityLevel: 0,
								Dimension: machine.Dimension{
									Name:  "numa",
									Value: "0",
								},
							}: {"gpu-1", "gpu-2", "gpu-3"},
						},
					},
					"gpu-1": {
						NumaNodes: []int{0},
						DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
							{
								PriorityLevel: 0,
								Dimension: machine.Dimension{
									Name:  "numa",
									Value: "0",
								},
							}: {"gpu-0", "gpu-2", "gpu-3"},
						},
					},
					"gpu-2": {
						NumaNodes: []int{0},
						DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
							{
								PriorityLevel: 0,
								Dimension: machine.Dimension{
									Name:  "numa",
									Value: "0",
								},
							}: {"gpu-0", "gpu-1", "gpu-3"},
						},
					},
					"gpu-3": {
						NumaNodes: []int{0},
						DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
							{
								PriorityLevel: 0,
								Dimension: machine.Dimension{
									Name:  "numa",
									Value: "0",
								},
							}: {"gpu-0", "gpu-1", "gpu-2"},
						},
					},
					"gpu-4": {
						NumaNodes: []int{1},
						DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
							{
								PriorityLevel: 0,
								Dimension: machine.Dimension{
									Name:  "numa",
									Value: "1",
								},
							}: {"gpu-5", "gpu-6", "gpu-7"},
						},
					},
					"gpu-5": {
						NumaNodes: []int{1},
						DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
							{
								PriorityLevel: 0,
								Dimension: machine.Dimension{
									Name:  "numa",
									Value: "1",
								},
							}: {"gpu-4", "gpu-6", "gpu-7"},
						},
					},
					"gpu-6": {
						NumaNodes: []int{1},
						DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
							{
								PriorityLevel: 0,
								Dimension: machine.Dimension{
									Name:  "numa",
									Value: "1",
								},
							}: {"gpu-4", "gpu-5", "gpu-7"},
						},
					},
					"gpu-7": {
						NumaNodes: []int{1},
						DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
							{
								PriorityLevel: 0,
								Dimension: machine.Dimension{
									Name:  "numa",
									Value: "1",
								},
							}: {"gpu-4", "gpu-5", "gpu-6"},
						},
					},
				},
			},
			machineTopology: []cadvisorapi.Node{
				{
					Id: 0,
					Cores: []cadvisorapi.Core{
						{SocketID: 0, Id: 0, Threads: []int{0, 4}},
						{SocketID: 0, Id: 1, Threads: []int{1, 5}},
						{SocketID: 0, Id: 2, Threads: []int{2, 6}},
						{SocketID: 0, Id: 3, Threads: []int{3, 7}},
					},
				},
				{
					Id: 1,
					Cores: []cadvisorapi.Core{
						{SocketID: 1, Id: 4, Threads: []int{8, 12}},
						{SocketID: 1, Id: 5, Threads: []int{9, 13}},
						{SocketID: 1, Id: 6, Threads: []int{10, 14}},
						{SocketID: 1, Id: 7, Threads: []int{11, 15}},
					},
				},
			},
			expectedSpec: []*nodev1alpha1.Property{
				{
					PropertyName:   propertyNameGPUTopology,
					PropertyValues: []string{"numa"},
				},
			},
			expectedStatus: []*nodev1alpha1.TopologyZone{
				{
					Type: nodev1alpha1.TopologyTypeSocket,
					Name: "0",
					Children: []*nodev1alpha1.TopologyZone{
						{
							Type: nodev1alpha1.TopologyTypeNuma,
							Name: "0",
							Children: []*nodev1alpha1.TopologyZone{
								{
									Type: nodev1alpha1.TopologyTypeGPU,
									Name: "gpu-0",
									Attributes: []nodev1alpha1.Attribute{
										{
											Name:  "numa",
											Value: "0",
										},
									},
								},
								{
									Type: nodev1alpha1.TopologyTypeGPU,
									Name: "gpu-1",
									Attributes: []nodev1alpha1.Attribute{
										{
											Name:  "numa",
											Value: "0",
										},
									},
								},
								{
									Type: nodev1alpha1.TopologyTypeGPU,
									Name: "gpu-2",
									Attributes: []nodev1alpha1.Attribute{
										{
											Name:  "numa",
											Value: "0",
										},
									},
								},
								{
									Type: nodev1alpha1.TopologyTypeGPU,
									Name: "gpu-3",
									Attributes: []nodev1alpha1.Attribute{
										{
											Name:  "numa",
											Value: "0",
										},
									},
								},
							},
						},
					},
				},
				{
					Type: nodev1alpha1.TopologyTypeSocket,
					Name: "1",
					Children: []*nodev1alpha1.TopologyZone{
						{
							Type: nodev1alpha1.TopologyTypeNuma,
							Name: "1",
							Children: []*nodev1alpha1.TopologyZone{
								{
									Type: nodev1alpha1.TopologyTypeGPU,
									Name: "gpu-4",
									Attributes: []nodev1alpha1.Attribute{
										{
											Name:  "numa",
											Value: "1",
										},
									},
								},
								{
									Type: nodev1alpha1.TopologyTypeGPU,
									Name: "gpu-5",
									Attributes: []nodev1alpha1.Attribute{
										{
											Name:  "numa",
											Value: "1",
										},
									},
								},
								{
									Type: nodev1alpha1.TopologyTypeGPU,
									Name: "gpu-6",
									Attributes: []nodev1alpha1.Attribute{
										{
											Name:  "numa",
											Value: "1",
										},
									},
								},
								{
									Type: nodev1alpha1.TopologyTypeGPU,
									Name: "gpu-7",
									Attributes: []nodev1alpha1.Attribute{
										{
											Name:  "numa",
											Value: "1",
										},
									},
								},
							},
						},
					},
				},
			},
		},
		{
			name: "Able to get report content for 2 dimensions",
			// device topology for 2 dimensions
			// gpu-0, gpu-1 in pcie 0
			// gpu-2, gpu-3 in pcie 1
			// gpu-0, gpu-1, gpu-2, gpu-3 in numa 0
			// gpu-4, gpu-5 in pcie 2
			// gpu-6, gpu-7 in pcie 3
			// gpu-4, gpu-5, gpu-6, gpu-7 in numa 1
			deviceTopology: &machine.DeviceTopology{
				PriorityDimensions: []string{"pcie", "numa"},
				Devices: map[string]machine.DeviceInfo{
					"gpu-0": {
						NumaNodes: []int{0},
						DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
							{
								PriorityLevel: 0,
								Dimension: machine.Dimension{
									Name:  "pcie",
									Value: "0",
								},
							}: {"gpu-1"},
							{
								PriorityLevel: 1,
								Dimension: machine.Dimension{
									Name:  "numa",
									Value: "0",
								},
							}: {"gpu-1", "gpu-2", "gpu-3"},
						},
					},
					"gpu-1": {
						NumaNodes: []int{0},
						DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
							{
								PriorityLevel: 0,
								Dimension: machine.Dimension{
									Name:  "pcie",
									Value: "0",
								},
							}: {"gpu-0"},
							{
								PriorityLevel: 1,
								Dimension: machine.Dimension{
									Name:  "numa",
									Value: "0",
								},
							}: {"gpu-0", "gpu-2", "gpu-3"},
						},
					},
					"gpu-2": {
						NumaNodes: []int{0},
						DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
							{
								PriorityLevel: 0,
								Dimension: machine.Dimension{
									Name:  "pcie",
									Value: "1",
								},
							}: {"gpu-3"},
							{
								PriorityLevel: 1,
								Dimension: machine.Dimension{
									Name:  "numa",
									Value: "0",
								},
							}: {"gpu-0", "gpu-1", "gpu-3"},
						},
					},
					"gpu-3": {
						NumaNodes: []int{0},
						DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
							{
								PriorityLevel: 0,
								Dimension: machine.Dimension{
									Name:  "pcie",
									Value: "1",
								},
							}: {"gpu-2"},
							{
								PriorityLevel: 1,
								Dimension: machine.Dimension{
									Name:  "numa",
									Value: "0",
								},
							}: {"gpu-0", "gpu-1", "gpu-2"},
						},
					},
					"gpu-4": {
						NumaNodes: []int{1},
						DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
							{
								PriorityLevel: 0,
								Dimension: machine.Dimension{
									Name:  "pcie",
									Value: "2",
								},
							}: {"gpu-5"},
							{
								PriorityLevel: 1,
								Dimension: machine.Dimension{
									Name:  "numa",
									Value: "1",
								},
							}: {"gpu-5", "gpu-6", "gpu-7"},
						},
					},
					"gpu-5": {
						NumaNodes: []int{1},
						DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
							{
								PriorityLevel: 0,
								Dimension: machine.Dimension{
									Name:  "pcie",
									Value: "2",
								},
							}: {"gpu-4"},
							{
								PriorityLevel: 1,
								Dimension: machine.Dimension{
									Name:  "numa",
									Value: "1",
								},
							}: {"gpu-4", "gpu-6", "gpu-7"},
						},
					},
					"gpu-6": {
						NumaNodes: []int{1},
						DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
							{
								PriorityLevel: 0,
								Dimension: machine.Dimension{
									Name:  "pcie",
									Value: "3",
								},
							}: {"gpu-7"},
							{
								PriorityLevel: 1,
								Dimension: machine.Dimension{
									Name:  "numa",
									Value: "1",
								},
							}: {"gpu-4", "gpu-5", "gpu-7"},
						},
					},
					"gpu-7": {
						NumaNodes: []int{1},
						DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
							{
								PriorityLevel: 0,
								Dimension: machine.Dimension{
									Name:  "pcie",
									Value: "3",
								},
							}: {"gpu-6"},
							{
								PriorityLevel: 1,
								Dimension: machine.Dimension{
									Name:  "numa",
									Value: "1",
								},
							}: {"gpu-4", "gpu-5", "gpu-6"},
						},
					},
				},
			},
			machineTopology: []cadvisorapi.Node{
				{
					Id: 0,
					Cores: []cadvisorapi.Core{
						{SocketID: 0, Id: 0, Threads: []int{0, 4}},
						{SocketID: 0, Id: 1, Threads: []int{1, 5}},
						{SocketID: 0, Id: 2, Threads: []int{2, 6}},
						{SocketID: 0, Id: 3, Threads: []int{3, 7}},
					},
				},
				{
					Id: 1,
					Cores: []cadvisorapi.Core{
						{SocketID: 1, Id: 4, Threads: []int{8, 12}},
						{SocketID: 1, Id: 5, Threads: []int{9, 13}},
						{SocketID: 1, Id: 6, Threads: []int{10, 14}},
						{SocketID: 1, Id: 7, Threads: []int{11, 15}},
					},
				},
			},
			expectedSpec: []*nodev1alpha1.Property{
				{
					PropertyName:   propertyNameGPUTopology,
					PropertyValues: []string{"pcie", "numa"},
				},
			},
			expectedStatus: []*nodev1alpha1.TopologyZone{
				{
					Type: nodev1alpha1.TopologyTypeSocket,
					Name: "0",
					Children: []*nodev1alpha1.TopologyZone{
						{
							Type: nodev1alpha1.TopologyTypeNuma,
							Name: "0",
							Children: []*nodev1alpha1.TopologyZone{
								{
									Type: nodev1alpha1.TopologyTypeGPU,
									Name: "gpu-0",
									Attributes: []nodev1alpha1.Attribute{
										{
											Name:  "numa",
											Value: "0",
										},
										{
											Name:  "pcie",
											Value: "0",
										},
									},
								},
								{
									Type: nodev1alpha1.TopologyTypeGPU,
									Name: "gpu-1",
									Attributes: []nodev1alpha1.Attribute{
										{
											Name:  "numa",
											Value: "0",
										},
										{
											Name:  "pcie",
											Value: "0",
										},
									},
								},
								{
									Type: nodev1alpha1.TopologyTypeGPU,
									Name: "gpu-2",
									Attributes: []nodev1alpha1.Attribute{
										{
											Name:  "numa",
											Value: "0",
										},
										{
											Name:  "pcie",
											Value: "1",
										},
									},
								},
								{
									Type: nodev1alpha1.TopologyTypeGPU,
									Name: "gpu-3",
									Attributes: []nodev1alpha1.Attribute{
										{
											Name:  "numa",
											Value: "0",
										},
										{
											Name:  "pcie",
											Value: "1",
										},
									},
								},
							},
						},
					},
				},
				{
					Type: nodev1alpha1.TopologyTypeSocket,
					Name: "1",
					Children: []*nodev1alpha1.TopologyZone{
						{
							Type: nodev1alpha1.TopologyTypeNuma,
							Name: "1",
							Children: []*nodev1alpha1.TopologyZone{
								{
									Type: nodev1alpha1.TopologyTypeGPU,
									Name: "gpu-4",
									Attributes: []nodev1alpha1.Attribute{
										{
											Name:  "numa",
											Value: "1",
										},
										{
											Name:  "pcie",
											Value: "2",
										},
									},
								},
								{
									Type: nodev1alpha1.TopologyTypeGPU,
									Name: "gpu-5",
									Attributes: []nodev1alpha1.Attribute{
										{
											Name:  "numa",
											Value: "1",
										},
										{
											Name:  "pcie",
											Value: "2",
										},
									},
								},
								{
									Type: nodev1alpha1.TopologyTypeGPU,
									Name: "gpu-6",
									Attributes: []nodev1alpha1.Attribute{
										{
											Name:  "numa",
											Value: "1",
										},
										{
											Name:  "pcie",
											Value: "3",
										},
									},
								},
								{
									Type: nodev1alpha1.TopologyTypeGPU,
									Name: "gpu-7",
									Attributes: []nodev1alpha1.Attribute{
										{
											Name:  "numa",
											Value: "1",
										},
										{
											Name:  "pcie",
											Value: "3",
										},
									},
								},
							},
						},
					},
				},
			},
		},
		{
			name: "Error reporting CNR when PriorityDimensions are nil",
			deviceTopology: &machine.DeviceTopology{
				PriorityDimensions: nil,
				Devices: map[string]machine.DeviceInfo{
					"gpu-0": {
						NumaNodes: []int{0},
						DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
							{
								PriorityLevel: 0,
								Dimension: machine.Dimension{
									Name:  "pcie",
									Value: "0",
								},
							}: {"gpu-1"},
							{
								PriorityLevel: 1,
								Dimension: machine.Dimension{
									Name:  "numa",
									Value: "0",
								},
							}: {"gpu-1", "gpu-2", "gpu-3"},
						},
					},
					"gpu-1": {
						NumaNodes: []int{0},
						DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
							{
								PriorityLevel: 0,
								Dimension: machine.Dimension{
									Name:  "pcie",
									Value: "0",
								},
							}: {"gpu-0"},
							{
								PriorityLevel: 1,
								Dimension: machine.Dimension{
									Name:  "numa",
									Value: "0",
								},
							}: {"gpu-0", "gpu-2", "gpu-3"},
						},
					},
					"gpu-2": {
						NumaNodes: []int{0},
						DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
							{
								PriorityLevel: 0,
								Dimension: machine.Dimension{
									Name:  "pcie",
									Value: "1",
								},
							}: {"gpu-3"},
							{
								PriorityLevel: 1,
								Dimension: machine.Dimension{
									Name:  "numa",
									Value: "0",
								},
							}: {"gpu-0", "gpu-1", "gpu-3"},
						},
					},
					"gpu-3": {
						NumaNodes: []int{0},
						DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
							{
								PriorityLevel: 0,
								Dimension: machine.Dimension{
									Name:  "pcie",
									Value: "1",
								},
							}: {"gpu-2"},
							{
								PriorityLevel: 1,
								Dimension: machine.Dimension{
									Name:  "numa",
									Value: "0",
								},
							}: {"gpu-0", "gpu-1", "gpu-2"},
						},
					},
					"gpu-4": {
						NumaNodes: []int{1},
						DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
							{
								PriorityLevel: 0,
								Dimension: machine.Dimension{
									Name:  "pcie",
									Value: "2",
								},
							}: {"gpu-5"},
							{
								PriorityLevel: 1,
								Dimension: machine.Dimension{
									Name:  "numa",
									Value: "1",
								},
							}: {"gpu-5", "gpu-6", "gpu-7"},
						},
					},
					"gpu-5": {
						NumaNodes: []int{1},
						DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
							{
								PriorityLevel: 0,
								Dimension: machine.Dimension{
									Name:  "pcie",
									Value: "2",
								},
							}: {"gpu-4"},
							{
								PriorityLevel: 1,
								Dimension: machine.Dimension{
									Name:  "numa",
									Value: "1",
								},
							}: {"gpu-4", "gpu-6", "gpu-7"},
						},
					},
					"gpu-6": {
						NumaNodes: []int{1},
						DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
							{
								PriorityLevel: 0,
								Dimension: machine.Dimension{
									Name:  "pcie",
									Value: "3",
								},
							}: {"gpu-7"},
							{
								PriorityLevel: 1,
								Dimension: machine.Dimension{
									Name:  "numa",
									Value: "1",
								},
							}: {"gpu-4", "gpu-5", "gpu-7"},
						},
					},
					"gpu-7": {
						NumaNodes: []int{1},
						DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
							{
								PriorityLevel: 0,
								Dimension: machine.Dimension{
									Name:  "pcie",
									Value: "3",
								},
							}: {"gpu-6"},
							{
								PriorityLevel: 1,
								Dimension: machine.Dimension{
									Name:  "numa",
									Value: "1",
								},
							}: {"gpu-4", "gpu-5", "gpu-6"},
						},
					},
				},
			},
			machineTopology: []cadvisorapi.Node{
				{
					Id: 0,
					Cores: []cadvisorapi.Core{
						{SocketID: 0, Id: 0, Threads: []int{0, 4}},
						{SocketID: 0, Id: 1, Threads: []int{1, 5}},
						{SocketID: 0, Id: 2, Threads: []int{2, 6}},
						{SocketID: 0, Id: 3, Threads: []int{3, 7}},
					},
				},
				{
					Id: 1,
					Cores: []cadvisorapi.Core{
						{SocketID: 1, Id: 4, Threads: []int{8, 12}},
						{SocketID: 1, Id: 5, Threads: []int{9, 13}},
						{SocketID: 1, Id: 6, Threads: []int{10, 14}},
						{SocketID: 1, Id: 7, Threads: []int{11, 15}},
					},
				},
			},
			expectedErr: true,
		},
		{
			name: "Error reporting CNR when device affinity dimensions are empty",
			// device topology is not populated with dimension names and values
			deviceTopology: &machine.DeviceTopology{
				PriorityDimensions: []string{"pcie", "numa"},
				Devices: map[string]machine.DeviceInfo{
					"gpu-0": {
						NumaNodes: []int{0},
						DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
							{
								PriorityLevel: 0,
								Dimension:     machine.Dimension{},
							}: {"gpu-1"},
							{
								PriorityLevel: 1,
								Dimension:     machine.Dimension{},
							}: {"gpu-1", "gpu-2", "gpu-3"},
						},
					},
					"gpu-1": {
						NumaNodes: []int{0},
						DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
							{
								PriorityLevel: 0,
								Dimension:     machine.Dimension{},
							}: {"gpu-0"},
							{
								PriorityLevel: 1,
								Dimension:     machine.Dimension{},
							}: {"gpu-0", "gpu-2", "gpu-3"},
						},
					},
					"gpu-2": {
						NumaNodes: []int{0},
						DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
							{
								PriorityLevel: 0,
								Dimension:     machine.Dimension{},
							}: {"gpu-3"},
							{
								PriorityLevel: 1,
								Dimension:     machine.Dimension{},
							}: {"gpu-0", "gpu-1", "gpu-3"},
						},
					},
					"gpu-3": {
						NumaNodes: []int{0},
						DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
							{
								PriorityLevel: 0,
								Dimension:     machine.Dimension{},
							}: {"gpu-2"},
							{
								PriorityLevel: 1,
								Dimension:     machine.Dimension{},
							}: {"gpu-0", "gpu-1", "gpu-2"},
						},
					},
					"gpu-4": {
						NumaNodes: []int{1},
						DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
							{
								PriorityLevel: 0,
								Dimension:     machine.Dimension{},
							}: {"gpu-5"},
							{
								PriorityLevel: 1,
								Dimension:     machine.Dimension{},
							}: {"gpu-5", "gpu-6", "gpu-7"},
						},
					},
					"gpu-5": {
						NumaNodes: []int{1},
						DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
							{
								PriorityLevel: 0,
								Dimension:     machine.Dimension{},
							}: {"gpu-4"},
							{
								PriorityLevel: 1,
								Dimension:     machine.Dimension{},
							}: {"gpu-4", "gpu-6", "gpu-7"},
						},
					},
					"gpu-6": {
						NumaNodes: []int{1},
						DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
							{
								PriorityLevel: 0,
								Dimension:     machine.Dimension{},
							}: {"gpu-7"},
							{
								PriorityLevel: 1,
								Dimension:     machine.Dimension{},
							}: {"gpu-4", "gpu-5", "gpu-7"},
						},
					},
					"gpu-7": {
						NumaNodes: []int{1},
						DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
							{
								PriorityLevel: 0,
								Dimension:     machine.Dimension{},
							}: {"gpu-6"},
							{
								PriorityLevel: 1,
								Dimension:     machine.Dimension{},
							}: {"gpu-4", "gpu-5", "gpu-6"},
						},
					},
				},
			},
			machineTopology: []cadvisorapi.Node{
				{
					Id: 0,
					Cores: []cadvisorapi.Core{
						{SocketID: 0, Id: 0, Threads: []int{0, 4}},
						{SocketID: 0, Id: 1, Threads: []int{1, 5}},
						{SocketID: 0, Id: 2, Threads: []int{2, 6}},
						{SocketID: 0, Id: 3, Threads: []int{3, 7}},
					},
				},
				{
					Id: 1,
					Cores: []cadvisorapi.Core{
						{SocketID: 1, Id: 4, Threads: []int{8, 12}},
						{SocketID: 1, Id: 5, Threads: []int{9, 13}},
						{SocketID: 1, Id: 6, Threads: []int{10, 14}},
						{SocketID: 1, Id: 7, Threads: []int{11, 15}},
					},
				},
			},
			expectedErr: true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			topologyRegistry := machine.NewDeviceTopologyRegistry()
			topologyProvider := machine.NewDeviceTopologyProvider([]string{"test_gpu"})
			topologyRegistry.RegisterDeviceTopologyProvider(gpuconsts.GPUDeviceType, topologyProvider)
			if tt.deviceTopology != nil {
				err := topologyRegistry.SetDeviceTopology(gpuconsts.GPUDeviceType, tt.deviceTopology)
				assert.NoError(t, err)
			}

			testConfig := &config.Configuration{
				AgentConfiguration: &agent.AgentConfiguration{
					GenericAgentConfiguration: &agent.GenericAgentConfiguration{
						PluginManagerConfiguration: &global.PluginManagerConfiguration{
							PluginRegistrationDir: "test",
						},
					},
				},
			}

			reporter, err := NewGPUReporter(metrics.DummyMetrics{}, generateTestMetaServer(tt.machineTopology), testConfig, topologyRegistry)
			assert.NoError(t, err)
			reporterImpl, ok := reporter.(*gpuReporterImpl)
			assert.True(t, ok)
			pluginWrapper, ok := reporterImpl.GenericPlugin.(*skeleton.PluginRegistrationWrapper)
			assert.True(t, ok)
			reporterPlugin, ok := pluginWrapper.GenericPlugin.(*gpuReporterPlugin)
			assert.True(t, ok)

			reportContent, err := reporterPlugin.GetReportContent(context.Background(), nil)

			if tt.expectedErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				expectedReportContent := generateExpectedReportContent(t, tt.expectedSpec, tt.expectedStatus)
				assert.Equal(t, expectedReportContent, reportContent)
			}
		})
	}
}

func generateExpectedReportContent(t *testing.T, expectedSpec []*nodev1alpha1.Property, expectedStatus []*nodev1alpha1.TopologyZone) *v1alpha1.GetReportContentResponse {
	specValue, err := json.Marshal(&expectedSpec)
	assert.NoError(t, err)

	statusValue, err := json.Marshal(&expectedStatus)
	assert.NoError(t, err)

	return &v1alpha1.GetReportContentResponse{
		Content: []*v1alpha1.ReportContent{
			{
				GroupVersionKind: &util.CNRGroupVersionKind,
				Field: []*v1alpha1.ReportField{
					{
						FieldType: v1alpha1.FieldType_Spec,
						FieldName: util.CNRFieldNameNodeResourceProperties,
						Value:     specValue,
					},
					{
						FieldType: v1alpha1.FieldType_Status,
						FieldName: util.CNRFieldNameTopologyZone,
						Value:     statusValue,
					},
				},
			},
		},
	}
}

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
	"fmt"
	"testing"
	"time"

	"github.com/gogo/protobuf/proto"
	cadvisorapi "github.com/google/cadvisor/info/v1"
	"github.com/stretchr/testify/assert"
	"google.golang.org/grpc/metadata"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"
	"k8s.io/kubernetes/pkg/kubelet/checkpointmanager"
	"k8s.io/kubernetes/pkg/kubelet/cm/devicemanager/checkpoint"

	nodev1alpha1 "github.com/kubewharf/katalyst-api/pkg/apis/node/v1alpha1"
	"github.com/kubewharf/katalyst-api/pkg/plugins/skeleton"
	"github.com/kubewharf/katalyst-api/pkg/protocol/reporterplugin/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/state"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	metaagent "github.com/kubewharf/katalyst-core/pkg/metaserver/agent"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/pod"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
	"github.com/kubewharf/katalyst-core/pkg/util/native"
)

type testGPUState struct {
	machineState state.AllocationResourcesMap
}

func cloneMachineState(src state.AllocationResourcesMap) state.AllocationResourcesMap {
	if src == nil {
		return nil
	}
	// stateGetter.AllocationState.Clone() doesn't copy Allocatable, so clone it manually in tests.
	dst := make(state.AllocationResourcesMap, len(src))
	for resourceName, allocMap := range src {
		if allocMap == nil {
			dst[resourceName] = nil
			continue
		}
		am := make(state.AllocationMap, len(allocMap))
		for id, as := range allocMap {
			if as == nil {
				am[id] = nil
				continue
			}
			am[id] = &state.AllocationState{Allocatable: as.Allocatable, PodEntries: as.PodEntries.Clone()}
		}
		dst[resourceName] = am
	}
	return dst
}

func (s *testGPUState) SetMachineState(allocationResourcesMap state.AllocationResourcesMap, _ bool) {
	s.machineState = cloneMachineState(allocationResourcesMap)
}

func (s *testGPUState) SetResourceState(resourceName v1.ResourceName, allocationMap state.AllocationMap, _ bool) {
	if s.machineState == nil {
		s.machineState = make(state.AllocationResourcesMap)
	}
	// preserve Allocatable when cloning AllocationState
	if allocationMap == nil {
		s.machineState[resourceName] = nil
		return
	}
	am := make(state.AllocationMap, len(allocationMap))
	for id, as := range allocationMap {
		if as == nil {
			am[id] = nil
			continue
		}
		am[id] = &state.AllocationState{Allocatable: as.Allocatable, PodEntries: as.PodEntries.Clone()}
	}
	s.machineState[resourceName] = am
}

func (s *testGPUState) SetPodResourceEntries(_ state.PodResourceEntries, _ bool) {}

func (s *testGPUState) SetAllocationInfo(_ v1.ResourceName, _, _ string, _ *state.AllocationInfo, _ bool) {
}

func (s *testGPUState) Delete(_ v1.ResourceName, _, _ string, _ bool) {}

func (s *testGPUState) ClearState() {}

func (s *testGPUState) AddMachineStateSyncNotifier(notifier func()) {}

func (s *testGPUState) StoreState() error {
	return nil
}

func (s *testGPUState) GetMachineState() state.AllocationResourcesMap {
	return cloneMachineState(s.machineState)
}

func (s *testGPUState) GetPodResourceEntries() state.PodResourceEntries { return nil }

func (s *testGPUState) GetPodEntries(_ v1.ResourceName) state.PodEntries { return nil }

func (s *testGPUState) GetAllocationInfo(_ v1.ResourceName, _, _ string) *state.AllocationInfo {
	return nil
}

func buildMachineState(resourceName v1.ResourceName, deviceIDs []string) state.AllocationResourcesMap {
	allocMap := make(state.AllocationMap)
	for _, id := range deviceIDs {
		allocMap[id] = &state.AllocationState{Allocatable: 1, PodEntries: nil}
	}
	return state.AllocationResourcesMap{resourceName: allocMap}
}

func buildMachineStateWithAllocatable(resourceName v1.ResourceName, deviceToAllocatable map[string]float64) state.AllocationResourcesMap {
	allocMap := make(state.AllocationMap)
	for id, alloc := range deviceToAllocatable {
		allocMap[id] = &state.AllocationState{Allocatable: alloc, PodEntries: nil}
	}
	return state.AllocationResourcesMap{resourceName: allocMap}
}

func assertReportContentMatches(t *testing.T, expectedSpec []*nodev1alpha1.Property, expectedStatus []*nodev1alpha1.TopologyZone, reportContent *v1alpha1.GetReportContentResponse) {
	if assert.NotNil(t, reportContent) {
		assert.Len(t, reportContent.Content, 1)
	}

	var statusField *v1alpha1.ReportField
	var specField *v1alpha1.ReportField
	for _, c := range reportContent.Content {
		for _, f := range c.Field {
			switch f.FieldType {
			case v1alpha1.FieldType_Status:
				if f.FieldName == util.CNRFieldNameTopologyZone {
					statusField = f
				}
			case v1alpha1.FieldType_Spec:
				if f.FieldName == util.CNRFieldNameNodeResourceProperties {
					specField = f
				}
			}
		}
	}

	if assert.NotNil(t, statusField) {
		var gotZones []*nodev1alpha1.TopologyZone
		assert.NoError(t, json.Unmarshal(statusField.Value, &gotZones))
		assertTopologyZonesMatch(t, expectedStatus, gotZones)
	}

	if expectedSpec == nil {
		assert.Nil(t, specField)
		return
	}

	if assert.NotNil(t, specField) {
		var gotSpec []*nodev1alpha1.Property
		assert.NoError(t, json.Unmarshal(specField.Value, &gotSpec))
		assert.Equal(t, expectedSpec, gotSpec)
	}
}

func assertTopologyZonesMatch(t *testing.T, expected, got []*nodev1alpha1.TopologyZone) {
	gotIndex := make(map[string]*nodev1alpha1.TopologyZone)
	for _, z := range got {
		gotIndex[zoneKey(z)] = z
	}

	for _, ez := range expected {
		gz, ok := gotIndex[zoneKey(ez)]
		if !assert.Truef(t, ok, "missing expected zone %s", zoneKey(ez)) {
			continue
		}
		assertZoneMatch(t, ez, gz)
	}
}

func zoneKey(z *nodev1alpha1.TopologyZone) string {
	if z == nil {
		return "<nil>"
	}
	return fmt.Sprintf("%s/%s", z.Type, z.Name)
}

func assertZoneMatch(t *testing.T, expected, got *nodev1alpha1.TopologyZone) {
	if !assert.NotNil(t, expected) || !assert.NotNil(t, got) {
		return
	}

	assert.Equal(t, expected.Type, got.Type)
	assert.Equal(t, expected.Name, got.Name)

	assertAttributesMatch(t, expected.Attributes, got.Attributes)
	assertAllocationsMatch(t, expected.Allocations, got.Allocations)
	assertResourcesMatch(t, expected.Resources, got.Resources)

	if expected.Children == nil {
		return
	}
	assert.Len(t, got.Children, len(expected.Children))
	childIndex := make(map[string]*nodev1alpha1.TopologyZone)
	for _, c := range got.Children {
		childIndex[zoneKey(c)] = c
	}
	for _, ec := range expected.Children {
		gc, ok := childIndex[zoneKey(ec)]
		if !assert.Truef(t, ok, "missing child zone %s", zoneKey(ec)) {
			continue
		}
		assertZoneMatch(t, ec, gc)
	}
}

func assertAllocationsMatch(t *testing.T, expected, got []*nodev1alpha1.Allocation) {
	if expected == nil {
		return
	}

	expectedByConsumer := make(map[string]*nodev1alpha1.Allocation, len(expected))
	for _, a := range expected {
		if a == nil {
			continue
		}
		expectedByConsumer[a.Consumer] = a
	}

	gotByConsumer := make(map[string]*nodev1alpha1.Allocation, len(got))
	for _, a := range got {
		if a == nil {
			continue
		}
		gotByConsumer[a.Consumer] = a
	}

	assert.Equal(t, len(expectedByConsumer), len(gotByConsumer))
	for consumer, ea := range expectedByConsumer {
		ga, ok := gotByConsumer[consumer]
		if !assert.Truef(t, ok, "missing allocation consumer %s", consumer) {
			continue
		}
		if ea.Requests == nil {
			assert.Nil(t, ga.Requests)
			continue
		}
		if assert.NotNil(t, ga.Requests) {
			assert.Equal(t, *ea.Requests, *ga.Requests)
		}
	}
}

func assertAttributesMatch(t *testing.T, expected, got []nodev1alpha1.Attribute) {
	if expected == nil {
		return
	}

	expMap := make(map[string]string, len(expected))
	for _, a := range expected {
		expMap[a.Name] = a.Value
	}
	gotMap := make(map[string]string, len(got))
	for _, a := range got {
		gotMap[a.Name] = a.Value
	}
	assert.Equal(t, expMap, gotMap)
}

func assertResourcesMatch(t *testing.T, expected, got nodev1alpha1.Resources) {
	if expected.Allocatable != nil {
		if assert.NotNil(t, got.Allocatable) {
			assert.Equal(t, *expected.Allocatable, *got.Allocatable)
		}
	}
	if expected.Capacity != nil {
		if assert.NotNil(t, got.Capacity) {
			assert.Equal(t, *expected.Capacity, *got.Capacity)
		}
	}
}

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
		name             string
		deviceTopology   *machine.DeviceTopology
		deviceTopologies map[string]*machine.DeviceTopology
		gpuDeviceNames   []string
		machineTopology  []cadvisorapi.Node
		machineState     state.AllocationResourcesMap
		expectedSpec     []*nodev1alpha1.Property
		expectedStatus   []*nodev1alpha1.TopologyZone
		expectedErr      bool
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
						Health:     pluginapi.Healthy,
						NumaNodes:  []int{0},
						Dimensions: map[string]string{"numa": "0"},
					},
					"gpu-1": {
						Health:     pluginapi.Healthy,
						NumaNodes:  []int{0},
						Dimensions: map[string]string{"numa": "0"},
					},
					"gpu-2": {
						Health:     pluginapi.Healthy,
						NumaNodes:  []int{0},
						Dimensions: map[string]string{"numa": "0"},
					},
					"gpu-3": {
						Health:     pluginapi.Healthy,
						NumaNodes:  []int{0},
						Dimensions: map[string]string{"numa": "0"},
					},
					"gpu-4": {
						Health:     pluginapi.Healthy,
						NumaNodes:  []int{1},
						Dimensions: map[string]string{"numa": "1"},
					},
					"gpu-5": {
						Health:     pluginapi.Healthy,
						NumaNodes:  []int{1},
						Dimensions: map[string]string{"numa": "1"},
					},
					"gpu-6": {
						Health:     pluginapi.Healthy,
						NumaNodes:  []int{1},
						Dimensions: map[string]string{"numa": "1"},
					},
					"gpu-7": {
						Health:     pluginapi.Healthy,
						NumaNodes:  []int{1},
						Dimensions: map[string]string{"numa": "1"},
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
									Resources: nodev1alpha1.Resources{
										Allocatable: &v1.ResourceList{
											"test-gpu": resource.MustParse("1"),
										},
										Capacity: &v1.ResourceList{
											"test-gpu": resource.MustParse("1"),
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
									Resources: nodev1alpha1.Resources{
										Allocatable: &v1.ResourceList{
											"test-gpu": resource.MustParse("1"),
										},
										Capacity: &v1.ResourceList{
											"test-gpu": resource.MustParse("1"),
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
									Resources: nodev1alpha1.Resources{
										Allocatable: &v1.ResourceList{
											"test-gpu": resource.MustParse("1"),
										},
										Capacity: &v1.ResourceList{
											"test-gpu": resource.MustParse("1"),
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
									Resources: nodev1alpha1.Resources{
										Allocatable: &v1.ResourceList{
											"test-gpu": resource.MustParse("1"),
										},
										Capacity: &v1.ResourceList{
											"test-gpu": resource.MustParse("1"),
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
									Resources: nodev1alpha1.Resources{
										Allocatable: &v1.ResourceList{
											"test-gpu": resource.MustParse("1"),
										},
										Capacity: &v1.ResourceList{
											"test-gpu": resource.MustParse("1"),
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
									Resources: nodev1alpha1.Resources{
										Allocatable: &v1.ResourceList{
											"test-gpu": resource.MustParse("1"),
										},
										Capacity: &v1.ResourceList{
											"test-gpu": resource.MustParse("1"),
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
									Resources: nodev1alpha1.Resources{
										Allocatable: &v1.ResourceList{
											"test-gpu": resource.MustParse("1"),
										},
										Capacity: &v1.ResourceList{
											"test-gpu": resource.MustParse("1"),
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
									Resources: nodev1alpha1.Resources{
										Allocatable: &v1.ResourceList{
											"test-gpu": resource.MustParse("1"),
										},
										Capacity: &v1.ResourceList{
											"test-gpu": resource.MustParse("1"),
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
						Health:    pluginapi.Healthy,
						NumaNodes: []int{0},
						Dimensions: map[string]string{
							"pcie": "0",
							"numa": "0",
						},
					},
					"gpu-1": {
						Health:    pluginapi.Healthy,
						NumaNodes: []int{0},
						Dimensions: map[string]string{
							"pcie": "0",
							"numa": "0",
						},
					},
					"gpu-2": {
						Health:    pluginapi.Healthy,
						NumaNodes: []int{0},
						Dimensions: map[string]string{
							"pcie": "1",
							"numa": "0",
						},
					},
					"gpu-3": {
						Health:    pluginapi.Healthy,
						NumaNodes: []int{0},
						Dimensions: map[string]string{
							"pcie": "1",
							"numa": "0",
						},
					},
					"gpu-4": {
						Health:    pluginapi.Healthy,
						NumaNodes: []int{1},
						Dimensions: map[string]string{
							"pcie": "2",
							"numa": "1",
						},
					},
					"gpu-5": {
						Health:    pluginapi.Healthy,
						NumaNodes: []int{1},
						Dimensions: map[string]string{
							"pcie": "2",
							"numa": "1",
						},
					},
					"gpu-6": {
						Health:    pluginapi.Healthy,
						NumaNodes: []int{1},
						Dimensions: map[string]string{
							"pcie": "3",
							"numa": "1",
						},
					},
					"gpu-7": {
						Health:    pluginapi.Healthy,
						NumaNodes: []int{1},
						Dimensions: map[string]string{
							"pcie": "3",
							"numa": "1",
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
									Resources: nodev1alpha1.Resources{
										Allocatable: &v1.ResourceList{
											"test-gpu": resource.MustParse("1"),
										},
										Capacity: &v1.ResourceList{
											"test-gpu": resource.MustParse("1"),
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
									Resources: nodev1alpha1.Resources{
										Allocatable: &v1.ResourceList{
											"test-gpu": resource.MustParse("1"),
										},
										Capacity: &v1.ResourceList{
											"test-gpu": resource.MustParse("1"),
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
									Resources: nodev1alpha1.Resources{
										Allocatable: &v1.ResourceList{
											"test-gpu": resource.MustParse("1"),
										},
										Capacity: &v1.ResourceList{
											"test-gpu": resource.MustParse("1"),
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
									Resources: nodev1alpha1.Resources{
										Allocatable: &v1.ResourceList{
											"test-gpu": resource.MustParse("1"),
										},
										Capacity: &v1.ResourceList{
											"test-gpu": resource.MustParse("1"),
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
									Resources: nodev1alpha1.Resources{
										Allocatable: &v1.ResourceList{
											"test-gpu": resource.MustParse("1"),
										},
										Capacity: &v1.ResourceList{
											"test-gpu": resource.MustParse("1"),
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
									Resources: nodev1alpha1.Resources{
										Allocatable: &v1.ResourceList{
											"test-gpu": resource.MustParse("1"),
										},
										Capacity: &v1.ResourceList{
											"test-gpu": resource.MustParse("1"),
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
									Resources: nodev1alpha1.Resources{
										Allocatable: &v1.ResourceList{
											"test-gpu": resource.MustParse("1"),
										},
										Capacity: &v1.ResourceList{
											"test-gpu": resource.MustParse("1"),
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
									Resources: nodev1alpha1.Resources{
										Allocatable: &v1.ResourceList{
											"test-gpu": resource.MustParse("1"),
										},
										Capacity: &v1.ResourceList{
											"test-gpu": resource.MustParse("1"),
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
			name: "Reporting of 0 value for resources if devices are not healthy",
			deviceTopology: &machine.DeviceTopology{
				PriorityDimensions: []string{"numa"},
				Devices: map[string]machine.DeviceInfo{
					"gpu-0": {
						Health:    pluginapi.Unhealthy,
						NumaNodes: []int{0},
						Dimensions: map[string]string{
							"numa": "0",
						},
					},
					"gpu-1": {
						Health:    pluginapi.Unhealthy,
						NumaNodes: []int{0},
						Dimensions: map[string]string{
							"numa": "0",
						},
					},
					"gpu-2": {
						Health:    pluginapi.Unhealthy,
						NumaNodes: []int{0},
						Dimensions: map[string]string{
							"numa": "0",
						},
					},
					"gpu-3": {
						Health:    pluginapi.Unhealthy,
						NumaNodes: []int{0},
						Dimensions: map[string]string{
							"numa": "0",
						},
					},
					"gpu-4": {
						Health:    pluginapi.Unhealthy,
						NumaNodes: []int{1},
						Dimensions: map[string]string{
							"numa": "1",
						},
					},
					"gpu-5": {
						Health:    pluginapi.Unhealthy,
						NumaNodes: []int{1},
						Dimensions: map[string]string{
							"numa": "1",
						},
					},
					"gpu-6": {
						Health:    pluginapi.Unhealthy,
						NumaNodes: []int{1},
						Dimensions: map[string]string{
							"numa": "1",
						},
					},
					"gpu-7": {
						Health:    pluginapi.Unhealthy,
						NumaNodes: []int{1},
						Dimensions: map[string]string{
							"numa": "1",
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
									Resources: nodev1alpha1.Resources{
										Allocatable: &v1.ResourceList{
											"test-gpu": resource.MustParse("0"),
										},
										Capacity: &v1.ResourceList{
											"test-gpu": resource.MustParse("1"),
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
									Resources: nodev1alpha1.Resources{
										Allocatable: &v1.ResourceList{
											"test-gpu": resource.MustParse("0"),
										},
										Capacity: &v1.ResourceList{
											"test-gpu": resource.MustParse("1"),
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
									Resources: nodev1alpha1.Resources{
										Allocatable: &v1.ResourceList{
											"test-gpu": resource.MustParse("0"),
										},
										Capacity: &v1.ResourceList{
											"test-gpu": resource.MustParse("1"),
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
									Resources: nodev1alpha1.Resources{
										Allocatable: &v1.ResourceList{
											"test-gpu": resource.MustParse("0"),
										},
										Capacity: &v1.ResourceList{
											"test-gpu": resource.MustParse("1"),
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
									Resources: nodev1alpha1.Resources{
										Allocatable: &v1.ResourceList{
											"test-gpu": resource.MustParse("0"),
										},
										Capacity: &v1.ResourceList{
											"test-gpu": resource.MustParse("1"),
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
									Resources: nodev1alpha1.Resources{
										Allocatable: &v1.ResourceList{
											"test-gpu": resource.MustParse("0"),
										},
										Capacity: &v1.ResourceList{
											"test-gpu": resource.MustParse("1"),
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
									Resources: nodev1alpha1.Resources{
										Allocatable: &v1.ResourceList{
											"test-gpu": resource.MustParse("0"),
										},
										Capacity: &v1.ResourceList{
											"test-gpu": resource.MustParse("1"),
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
									Resources: nodev1alpha1.Resources{
										Allocatable: &v1.ResourceList{
											"test-gpu": resource.MustParse("0"),
										},
										Capacity: &v1.ResourceList{
											"test-gpu": resource.MustParse("1"),
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
			name: "No reporting CNR when PriorityDimensions are nil",
			deviceTopology: &machine.DeviceTopology{
				PriorityDimensions: nil,
				Devices: map[string]machine.DeviceInfo{
					"gpu-0": {
						NumaNodes: []int{0},
						Dimensions: map[string]string{
							"pcie": "0",
							"numa": "0",
						},
					},
					"gpu-1": {
						NumaNodes: []int{0},
						Dimensions: map[string]string{
							"pcie": "0",
							"numa": "0",
						},
					},
					"gpu-2": {
						NumaNodes: []int{0},
						Dimensions: map[string]string{
							"pcie": "1",
							"numa": "0",
						},
					},
					"gpu-3": {
						NumaNodes: []int{0},
						Dimensions: map[string]string{
							"pcie": "1",
							"numa": "0",
						},
					},
					"gpu-4": {
						NumaNodes: []int{1},
						Dimensions: map[string]string{
							"pcie": "2",
							"numa": "1",
						},
					},
					"gpu-5": {
						NumaNodes: []int{1},
						Dimensions: map[string]string{
							"pcie": "2",
							"numa": "1",
						},
					},
					"gpu-6": {
						NumaNodes: []int{1},
						Dimensions: map[string]string{
							"pcie": "3",
							"numa": "1",
						},
					},
					"gpu-7": {
						NumaNodes: []int{1},
						Dimensions: map[string]string{
							"pcie": "3",
							"numa": "1",
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
			expectedErr: false,
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
									Resources: nodev1alpha1.Resources{
										Allocatable: &v1.ResourceList{
											"test-gpu": resource.MustParse("0"),
										},
										Capacity: &v1.ResourceList{
											"test-gpu": resource.MustParse("1"),
										},
									},
								},
								{
									Type: nodev1alpha1.TopologyTypeGPU,
									Name: "gpu-1",
									Resources: nodev1alpha1.Resources{
										Allocatable: &v1.ResourceList{
											"test-gpu": resource.MustParse("0"),
										},
										Capacity: &v1.ResourceList{
											"test-gpu": resource.MustParse("1"),
										},
									},
								},
								{
									Type: nodev1alpha1.TopologyTypeGPU,
									Name: "gpu-2",
									Resources: nodev1alpha1.Resources{
										Allocatable: &v1.ResourceList{
											"test-gpu": resource.MustParse("0"),
										},
										Capacity: &v1.ResourceList{
											"test-gpu": resource.MustParse("1"),
										},
									},
								},
								{
									Type: nodev1alpha1.TopologyTypeGPU,
									Name: "gpu-3",
									Resources: nodev1alpha1.Resources{
										Allocatable: &v1.ResourceList{
											"test-gpu": resource.MustParse("0"),
										},
										Capacity: &v1.ResourceList{
											"test-gpu": resource.MustParse("1"),
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
									Resources: nodev1alpha1.Resources{
										Allocatable: &v1.ResourceList{
											"test-gpu": resource.MustParse("0"),
										},
										Capacity: &v1.ResourceList{
											"test-gpu": resource.MustParse("1"),
										},
									},
								},
								{
									Type: nodev1alpha1.TopologyTypeGPU,
									Name: "gpu-5",
									Resources: nodev1alpha1.Resources{
										Allocatable: &v1.ResourceList{
											"test-gpu": resource.MustParse("0"),
										},
										Capacity: &v1.ResourceList{
											"test-gpu": resource.MustParse("1"),
										},
									},
								},
								{
									Type: nodev1alpha1.TopologyTypeGPU,
									Name: "gpu-6",
									Resources: nodev1alpha1.Resources{
										Allocatable: &v1.ResourceList{
											"test-gpu": resource.MustParse("0"),
										},
										Capacity: &v1.ResourceList{
											"test-gpu": resource.MustParse("1"),
										},
									},
								},
								{
									Type: nodev1alpha1.TopologyTypeGPU,
									Name: "gpu-7",
									Resources: nodev1alpha1.Resources{
										Allocatable: &v1.ResourceList{
											"test-gpu": resource.MustParse("0"),
										},
										Capacity: &v1.ResourceList{
											"test-gpu": resource.MustParse("1"),
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
			name: "No reporting CNR when device affinity dimensions are empty",
			// device topology is not populated with dimension names and values
			deviceTopology: &machine.DeviceTopology{
				PriorityDimensions: []string{"pcie", "numa"},
				Devices: map[string]machine.DeviceInfo{
					"gpu-0": {
						NumaNodes:  []int{0},
						Dimensions: map[string]string{},
					},
					"gpu-1": {
						NumaNodes:  []int{0},
						Dimensions: map[string]string{},
					},
					"gpu-2": {
						NumaNodes:  []int{0},
						Dimensions: map[string]string{},
					},
					"gpu-3": {
						NumaNodes:  []int{0},
						Dimensions: map[string]string{},
					},
					"gpu-4": {
						NumaNodes:  []int{1},
						Dimensions: map[string]string{},
					},
					"gpu-5": {
						NumaNodes:  []int{1},
						Dimensions: map[string]string{},
					},
					"gpu-6": {
						NumaNodes:  []int{1},
						Dimensions: map[string]string{},
					},
					"gpu-7": {
						NumaNodes:  []int{1},
						Dimensions: map[string]string{},
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
			expectedErr: false,
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
									Resources: nodev1alpha1.Resources{
										Allocatable: &v1.ResourceList{
											"test-gpu": resource.MustParse("0"),
										},
										Capacity: &v1.ResourceList{
											"test-gpu": resource.MustParse("1"),
										},
									},
								},
								{
									Type: nodev1alpha1.TopologyTypeGPU,
									Name: "gpu-1",
									Resources: nodev1alpha1.Resources{
										Allocatable: &v1.ResourceList{
											"test-gpu": resource.MustParse("0"),
										},
										Capacity: &v1.ResourceList{
											"test-gpu": resource.MustParse("1"),
										},
									},
								},
								{
									Type: nodev1alpha1.TopologyTypeGPU,
									Name: "gpu-2",
									Resources: nodev1alpha1.Resources{
										Allocatable: &v1.ResourceList{
											"test-gpu": resource.MustParse("0"),
										},
										Capacity: &v1.ResourceList{
											"test-gpu": resource.MustParse("1"),
										},
									},
								},
								{
									Type: nodev1alpha1.TopologyTypeGPU,
									Name: "gpu-3",
									Resources: nodev1alpha1.Resources{
										Allocatable: &v1.ResourceList{
											"test-gpu": resource.MustParse("0"),
										},
										Capacity: &v1.ResourceList{
											"test-gpu": resource.MustParse("1"),
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
									Resources: nodev1alpha1.Resources{
										Allocatable: &v1.ResourceList{
											"test-gpu": resource.MustParse("0"),
										},
										Capacity: &v1.ResourceList{
											"test-gpu": resource.MustParse("1"),
										},
									},
								},
								{
									Type: nodev1alpha1.TopologyTypeGPU,
									Name: "gpu-5",
									Resources: nodev1alpha1.Resources{
										Allocatable: &v1.ResourceList{
											"test-gpu": resource.MustParse("0"),
										},
										Capacity: &v1.ResourceList{
											"test-gpu": resource.MustParse("1"),
										},
									},
								},
								{
									Type: nodev1alpha1.TopologyTypeGPU,
									Name: "gpu-6",
									Resources: nodev1alpha1.Resources{
										Allocatable: &v1.ResourceList{
											"test-gpu": resource.MustParse("0"),
										},
										Capacity: &v1.ResourceList{
											"test-gpu": resource.MustParse("1"),
										},
									},
								},
								{
									Type: nodev1alpha1.TopologyTypeGPU,
									Name: "gpu-7",
									Resources: nodev1alpha1.Resources{
										Allocatable: &v1.ResourceList{
											"test-gpu": resource.MustParse("0"),
										},
										Capacity: &v1.ResourceList{
											"test-gpu": resource.MustParse("1"),
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
			name: "Reporting allocations when pods are assigned to devices",
			deviceTopology: &machine.DeviceTopology{
				PriorityDimensions: []string{"numa"},
				Devices: map[string]machine.DeviceInfo{
					"gpu-0": {
						Health:    pluginapi.Healthy,
						NumaNodes: []int{0},
						Dimensions: map[string]string{
							"numa": "0",
						},
					},
				},
			},
			machineTopology: []cadvisorapi.Node{
				{
					Id: 0,
					Cores: []cadvisorapi.Core{
						{SocketID: 0, Id: 0, Threads: []int{0, 4}},
					},
				},
			},
			machineState: state.AllocationResourcesMap{
				v1.ResourceName("test-gpu-resource"): state.AllocationMap{
					"gpu-0": {
						Allocatable: 1,
						PodEntries: state.PodEntries{
							"pod-uid-0": state.ContainerEntries{
								"c0": &state.AllocationInfo{
									AllocationMeta: commonstate.AllocationMeta{
										PodUid:        "pod-uid-0",
										PodNamespace:  "default",
										PodName:       "p0",
										ContainerName: "c0",
									},
									DeviceName: "test-gpu",
									AllocatedAllocation: state.Allocation{
										Quantity:  1,
										NUMANodes: []int{0},
									},
								},
							},
						},
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
										{Name: "numa", Value: "0"},
									},
									Resources: nodev1alpha1.Resources{
										Allocatable: &v1.ResourceList{"test-gpu": resource.MustParse("1")},
										Capacity:    &v1.ResourceList{"test-gpu": resource.MustParse("1")},
									},
									Allocations: []*nodev1alpha1.Allocation{
										{
											Consumer: native.GenerateNamespaceNameUIDKey("default", "p0", "pod-uid-0"),
											Requests: &v1.ResourceList{"test-gpu": resource.MustParse("1")},
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
			name: "Reporting allocatable equals allocState for healthy devices",
			deviceTopology: &machine.DeviceTopology{
				PriorityDimensions: []string{"numa"},
				Devices: map[string]machine.DeviceInfo{
					"gpu-0": {
						Health:    pluginapi.Healthy,
						NumaNodes: []int{0},
						Dimensions: map[string]string{
							"numa": "0",
						},
					},
					"gpu-1": {
						Health:    pluginapi.Healthy,
						NumaNodes: []int{1},
						Dimensions: map[string]string{
							"numa": "1",
						},
					},
				},
			},
			machineTopology: []cadvisorapi.Node{
				{
					Id:    0,
					Cores: []cadvisorapi.Core{{SocketID: 0, Id: 0, Threads: []int{0, 4}}},
				},
				{
					Id:    1,
					Cores: []cadvisorapi.Core{{SocketID: 0, Id: 1, Threads: []int{1, 5}}},
				},
			},
			machineState: buildMachineStateWithAllocatable(v1.ResourceName("test-gpu-resource"), map[string]float64{"gpu-0": 4, "gpu-1": 2}),
			expectedSpec: []*nodev1alpha1.Property{{PropertyName: propertyNameGPUTopology, PropertyValues: []string{"numa"}}},
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
									Type:       nodev1alpha1.TopologyTypeGPU,
									Name:       "gpu-0",
									Attributes: []nodev1alpha1.Attribute{{Name: "numa", Value: "0"}},
									Resources: nodev1alpha1.Resources{
										Allocatable: &v1.ResourceList{"test-gpu": resource.MustParse("4")},
										Capacity:    &v1.ResourceList{"test-gpu": resource.MustParse("4")},
									},
								},
							},
						},
						{
							Type: nodev1alpha1.TopologyTypeNuma,
							Name: "1",
							Children: []*nodev1alpha1.TopologyZone{
								{
									Type:       nodev1alpha1.TopologyTypeGPU,
									Name:       "gpu-1",
									Attributes: []nodev1alpha1.Attribute{{Name: "numa", Value: "1"}},
									Resources: nodev1alpha1.Resources{
										Allocatable: &v1.ResourceList{"test-gpu": resource.MustParse("2")},
										Capacity:    &v1.ResourceList{"test-gpu": resource.MustParse("2")},
									},
								},
							},
						},
					},
				},
			},
		},
		{
			name: "Allocations use resourceName when allocation DeviceName is empty",
			deviceTopology: &machine.DeviceTopology{
				PriorityDimensions: []string{"numa"},
				Devices: map[string]machine.DeviceInfo{
					"gpu-0": {
						Health:    pluginapi.Healthy,
						NumaNodes: []int{0},
						Dimensions: map[string]string{
							"numa": "0",
						},
					},
				},
			},
			machineTopology: []cadvisorapi.Node{{Id: 0, Cores: []cadvisorapi.Core{{SocketID: 0, Id: 0, Threads: []int{0, 4}}}}},
			machineState: state.AllocationResourcesMap{
				v1.ResourceName("test-gpu-resource"): state.AllocationMap{
					"gpu-0": {
						Allocatable: 1,
						PodEntries: state.PodEntries{
							"pod-uid-0": state.ContainerEntries{
								"c0": &state.AllocationInfo{
									AllocationMeta:      commonstate.AllocationMeta{PodUid: "pod-uid-0", PodNamespace: "default", PodName: "p0", ContainerName: "c0"},
									DeviceName:          "",
									AllocatedAllocation: state.Allocation{Quantity: 1, NUMANodes: []int{0}},
								},
							},
						},
					},
				},
			},
			expectedSpec: []*nodev1alpha1.Property{{PropertyName: propertyNameGPUTopology, PropertyValues: []string{"numa"}}},
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
									Type:       nodev1alpha1.TopologyTypeGPU,
									Name:       "gpu-0",
									Attributes: []nodev1alpha1.Attribute{{Name: "numa", Value: "0"}},
									Resources: nodev1alpha1.Resources{
										Allocatable: &v1.ResourceList{"test-gpu": resource.MustParse("1")},
										Capacity:    &v1.ResourceList{"test-gpu": resource.MustParse("1")},
									},
									Allocations: []*nodev1alpha1.Allocation{
										{Consumer: native.GenerateNamespaceNameUIDKey("default", "p0", "pod-uid-0"), Requests: &v1.ResourceList{"test-gpu-resource": resource.MustParse("1")}},
									},
								},
							},
						},
					},
				},
			},
		},
		{
			name:           "Merge resources across multiple device names for same device type",
			gpuDeviceNames: []string{"test-gpu-a", "test-gpu-b"},
			deviceTopologies: map[string]*machine.DeviceTopology{
				"test-gpu-a": {
					PriorityDimensions: []string{"numa"},
					UpdateTime:         100,
					Devices: map[string]machine.DeviceInfo{
						"gpu-0": {Health: pluginapi.Healthy, NumaNodes: []int{0}, Dimensions: map[string]string{"numa": "0"}},
					},
				},
				"test-gpu-b": {
					PriorityDimensions: []string{"numa"},
					UpdateTime:         200,
					Devices: map[string]machine.DeviceInfo{
						"gpu-0": {Health: pluginapi.Unhealthy, NumaNodes: []int{0}, Dimensions: map[string]string{"numa": "0"}},
					},
				},
			},
			machineTopology: []cadvisorapi.Node{{Id: 0, Cores: []cadvisorapi.Core{{SocketID: 0, Id: 0, Threads: []int{0, 4}}}}},
			machineState:    buildMachineStateWithAllocatable(v1.ResourceName("test-gpu-resource"), map[string]float64{"gpu-0": 3}),
			expectedSpec:    []*nodev1alpha1.Property{{PropertyName: propertyNameGPUTopology, PropertyValues: []string{"numa"}}},
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
									Type:       nodev1alpha1.TopologyTypeGPU,
									Name:       "gpu-0",
									Attributes: []nodev1alpha1.Attribute{{Name: "numa", Value: "0"}},
									Resources: nodev1alpha1.Resources{
										Allocatable: &v1.ResourceList{
											"test-gpu-a": resource.MustParse("3"),
											"test-gpu-b": resource.MustParse("0"),
										},
										Capacity: &v1.ResourceList{
											"test-gpu-a": resource.MustParse("3"),
											"test-gpu-b": resource.MustParse("3"),
										},
									},
								},
							},
						},
					},
				},
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			gpuDeviceNames := tt.gpuDeviceNames
			if len(gpuDeviceNames) == 0 {
				gpuDeviceNames = []string{"test-gpu"}
			}

			topologyRegistry := machine.NewDeviceTopologyRegistry()
			for _, dn := range gpuDeviceNames {
				topologyRegistry.RegisterDeviceTopologyProvider(dn, machine.NewDeviceTopologyProvider())
			}

			if tt.deviceTopologies != nil {
				for dn, topo := range tt.deviceTopologies {
					err := topologyRegistry.SetDeviceTopology(dn, topo)
					assert.NoError(t, err)
				}
			} else if tt.deviceTopology != nil {
				for _, dn := range gpuDeviceNames {
					err := topologyRegistry.SetDeviceTopology(dn, tt.deviceTopology)
					assert.NoError(t, err)
				}
			}

			testConfig := config.NewConfiguration()
			testConfig.PluginRegistrationDir = "test"
			testConfig.GPUDeviceNames = gpuDeviceNames

			resourceTypeName := v1.ResourceName("test-gpu-resource")
			deviceIDs := make([]string, 0)
			if tt.deviceTopology != nil {
				deviceIDs = make([]string, 0, len(tt.deviceTopology.Devices))
				for id := range tt.deviceTopology.Devices {
					deviceIDs = append(deviceIDs, id)
				}
			}

			ms := tt.machineState
			if ms == nil {
				ms = buildMachineState(resourceTypeName, deviceIDs)
			}

			testState := &testGPUState{machineState: ms}
			deviceTypeToNames := map[string]sets.String{string(resourceTypeName): sets.NewString(gpuDeviceNames...)}

			stateGetter := func() state.State { return testState }
			reporter, err := NewGPUReporter(metrics.DummyMetrics{}, generateTestMetaServer(tt.machineTopology), testConfig, topologyRegistry, stateGetter, deviceTypeToNames)
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
				return
			}
			assert.NoError(t, err)
			assertReportContentMatches(t, tt.expectedSpec, tt.expectedStatus, reportContent)
		})
	}
}

type mockReporterPluginServer struct {
	ctx      context.Context
	sendCh   chan *v1alpha1.GetReportContentResponse
	recvFunc func(*v1alpha1.GetReportContentResponse) error
}

func (m *mockReporterPluginServer) Send(resp *v1alpha1.GetReportContentResponse) error {
	if m.recvFunc != nil {
		return m.recvFunc(resp)
	}
	m.sendCh <- resp
	return nil
}

func (m *mockReporterPluginServer) Context() context.Context {
	return m.ctx
}

func (m *mockReporterPluginServer) Recv() (*v1alpha1.Empty, error) { return nil, nil }
func (m *mockReporterPluginServer) SetHeader(metadata.MD) error    { return nil }
func (m *mockReporterPluginServer) SendHeader(metadata.MD) error   { return nil }
func (m *mockReporterPluginServer) SetTrailer(metadata.MD)         {}
func (m *mockReporterPluginServer) SendMsg(m_ interface{}) error   { return nil }
func (m *mockReporterPluginServer) RecvMsg(m_ interface{}) error   { return nil }

func TestListAndWatchReportContent(t *testing.T) {
	t.Parallel()

	// 1. Setup minimal environment
	testConfig := config.NewConfiguration()
	testConfig.GPUDeviceNames = []string{"test-gpu"}
	deviceTypeToNames := map[string]sets.String{"test-gpu": sets.NewString("test-gpu")}

	topologyRegistry := machine.NewDeviceTopologyRegistry()
	mockProvider := machine.NewDeviceTopologyProviderStub()
	topologyRegistry.RegisterDeviceTopologyProvider("test-gpu", mockProvider)

	topology := &machine.DeviceTopology{
		Devices: map[string]machine.DeviceInfo{
			"gpu-0": {NumaNodes: []int{0}},
		},
	}
	_ = topologyRegistry.SetDeviceTopology("test-gpu", topology)

	mockState := &testGPUState{
		machineState: state.AllocationResourcesMap{
			"test-gpu": state.AllocationMap{
				"gpu-0": &state.AllocationState{Allocatable: 1},
			},
		},
	}
	stateGetter := func() state.State { return mockState }

	metaServer := generateTestMetaServer([]cadvisorapi.Node{
		{
			Id: 0,
			Cores: []cadvisorapi.Core{
				{SocketID: 0, Id: 0, Threads: []int{0, 4}},
			},
		},
	})

	reporter, err := NewGPUReporter(metrics.DummyMetrics{}, metaServer, testConfig, topologyRegistry, stateGetter, deviceTypeToNames)
	assert.NoError(t, err)
	reporterImpl := reporter.(*gpuReporterImpl)
	pluginWrapper := reporterImpl.GenericPlugin.(*skeleton.PluginRegistrationWrapper)
	reporterPlugin := pluginWrapper.GenericPlugin.(*gpuReporterPlugin)

	_ = reporterPlugin.Start()
	defer reporterPlugin.Stop()

	// 2. Setup server mock
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sendCh := make(chan *v1alpha1.GetReportContentResponse, 10)
	mockServer := &mockReporterPluginServer{
		ctx:    ctx,
		sendCh: sendCh,
	}

	// 3. Start ListAndWatch
	errCh := make(chan error, 1)
	go func() {
		errCh <- reporterPlugin.ListAndWatchReportContent(nil, mockServer)
	}()

	// 4. Verify initial report is sent
	var initialResp *v1alpha1.GetReportContentResponse
	select {
	case initialResp = <-sendCh:
		assert.NotNil(t, initialResp)
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for initial report")
	}

	// 5. Trigger without change should NOT send anything
	reporter.Trigger()
	select {
	case <-sendCh:
		t.Fatal("should not send identical report")
	case <-time.After(100 * time.Millisecond):
	}

	// 6. Change topology and trigger
	topology2 := &machine.DeviceTopology{
		Devices: map[string]machine.DeviceInfo{
			"gpu-0": {NumaNodes: []int{0}},
			"gpu-1": {NumaNodes: []int{0}}, // new device
		},
	}
	_ = topologyRegistry.SetDeviceTopology("test-gpu", topology2)
	mockState.machineState["test-gpu"]["gpu-1"] = &state.AllocationState{Allocatable: 1}

	reporter.Trigger()

	select {
	case resp := <-sendCh:
		assert.NotNil(t, resp)
		assert.False(t, proto.Equal(initialResp, resp))
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for updated report")
	}

	// 7. Test GetReportContent directly
	unaryResp, err := reporterPlugin.GetReportContent(context.Background(), nil)
	assert.NoError(t, err)
	assert.NotNil(t, unaryResp)

	// Close watch
	cancel()
	err = <-errCh
	assert.NoError(t, err)
}

func TestHasExistingPodAllocation(t *testing.T) {
	t.Parallel()

	allocations := util.ZoneAllocations{
		&nodev1alpha1.Allocation{
			Consumer: "default/pod1/uid1",
		},
		&nodev1alpha1.Allocation{
			Consumer: "default/pod2/uid2",
		},
		&nodev1alpha1.Allocation{
			Consumer: "invalid-consumer",
		},
	}

	tests := []struct {
		name   string
		podUID string
		want   bool
	}{
		{
			name:   "existing pod uid",
			podUID: "uid1",
			want:   true,
		},
		{
			name:   "another existing pod uid",
			podUID: "uid2",
			want:   true,
		},
		{
			name:   "non-existing pod uid",
			podUID: "uid3",
			want:   false,
		},
		{
			name:   "empty pod uid",
			podUID: "",
			want:   false,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := hasExistingPodAllocation(allocations, tt.podUID)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestAddStateAllocations(t *testing.T) {
	t.Parallel()

	p := &gpuReporterPlugin{}
	idToAllocations := make(map[string]util.ZoneAllocations)

	machineState := state.AllocationResourcesMap{
		"gpu": {
			"gpu-1": {
				Allocatable: 1,
				PodEntries: state.PodEntries{
					"uid1": {
						"container1": &state.AllocationInfo{
							AllocationMeta: commonstate.AllocationMeta{
								PodNamespace: "default",
								PodName:      "pod1",
								PodUid:       "uid1",
							},
							AllocatedAllocation: state.Allocation{
								Quantity: 1,
							},
						},
					},
				},
			},
		},
	}

	p.addStateAllocations(idToAllocations, machineState)

	assert.Contains(t, idToAllocations, "gpu-1")
	assert.Len(t, idToAllocations["gpu-1"], 1)
	assert.Equal(t, "default/pod1/uid1", idToAllocations["gpu-1"][0].Consumer)
}

func TestAddKubeletCheckpointAllocations(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		p                *gpuReporterPlugin
		expectedEmpty    bool
		expectedErr      bool
		expectedID       string
		expectedLen      int
		expectedConsumer string
	}{
		{
			name:          "nil checkpoint manager",
			p:             &gpuReporterPlugin{},
			expectedEmpty: true,
			expectedErr:   false,
		},
		{
			name: "get checkpoint fails",
			p: &gpuReporterPlugin{
				checkpointManager: &mockCheckpointManager{
					getErr: fmt.Errorf("mock get error"),
				},
			},
			expectedEmpty: true,
			expectedErr:   false,
		},
		{
			name: "get checkpoint succeeds",
			p: &gpuReporterPlugin{
				checkpointManager: &mockCheckpointManager{
					checkpointData: checkpoint.New([]checkpoint.PodDevicesEntry{
						{
							PodUID:        "test-pod-uid",
							ContainerName: "test-container",
							ResourceName:  "test-resource",
							DeviceIDs: map[int64][]string{
								0: {"test-device-1"},
							},
							AllocResp: []byte(""),
						},
					}, make(map[string][]string)),
				},
				metaServer: &metaserver.MetaServer{
					MetaAgent: &metaagent.MetaAgent{
						PodFetcher: &pod.PodFetcherStub{
							PodList: []*v1.Pod{
								{
									ObjectMeta: metav1.ObjectMeta{
										Name:      "test-pod",
										Namespace: "default",
										UID:       "test-pod-uid",
									},
								},
							},
						},
					},
				},
			},
			expectedEmpty:    false,
			expectedErr:      false,
			expectedID:       "test-device-1",
			expectedLen:      1,
			expectedConsumer: "default/test-pod/test-pod-uid",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			idToAllocations := make(map[string]util.ZoneAllocations)
			err := tt.p.addKubeletCheckpointAllocations(idToAllocations)

			if tt.expectedErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}

			if tt.expectedEmpty {
				assert.Empty(t, idToAllocations)
			} else {
				assert.NotEmpty(t, idToAllocations)
				assert.Contains(t, idToAllocations, tt.expectedID)
				assert.Len(t, idToAllocations[tt.expectedID], tt.expectedLen)
				assert.Equal(t, tt.expectedConsumer, idToAllocations[tt.expectedID][0].Consumer)
			}
		})
	}
}

type mockCheckpointManager struct {
	getErr         error
	checkpointData checkpoint.DeviceManagerCheckpoint
}

func (m *mockCheckpointManager) CreateCheckpoint(checkpointKey string, cp checkpointmanager.Checkpoint) error {
	return nil
}

func (m *mockCheckpointManager) GetCheckpoint(checkpointKey string, cp checkpointmanager.Checkpoint) error {
	if m.getErr != nil {
		return m.getErr
	}

	if m.checkpointData != nil {
		bytes, _ := m.checkpointData.MarshalCheckpoint()
		cp.UnmarshalCheckpoint(bytes)
	}

	return nil
}

func (m *mockCheckpointManager) RemoveCheckpoint(checkpointKey string) error {
	return nil
}

func (m *mockCheckpointManager) ListCheckpoints() ([]string, error) {
	return nil, nil
}

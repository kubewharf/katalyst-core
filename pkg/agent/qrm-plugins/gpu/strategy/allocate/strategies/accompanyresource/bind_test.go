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

package accompanyresource

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/state"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/strategy/allocate"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/strategy/allocate/strategies/canonical"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

// helper to create a pre-allocated allocation state for a specific pod/container
func preallocState(podUID, container string) *state.AllocationState {
	return &state.AllocationState{PodEntries: state.PodEntries{
		podUID: state.ContainerEntries{
			container: &state.AllocationInfo{},
		},
	}}
}

func TestBind(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name                                    string
		ctx                                     *allocate.AllocationContext
		sortedDevices                           []string
		expectedResult                          *allocate.AllocationResult
		expectedErr                             bool
		preAllocatedResourceToTargetDeviceRatio float64
	}{
		{
			name: "multi-priority: fallback to lower priority for single rdma",
			ctx: &allocate.AllocationContext{
				AccompanyResourceName: "rdma",
				ResourceName:          "gpu",
				ResourceReq:           &v1alpha1.ResourceRequest{PodUid: "p", ContainerName: "c"},
				MachineState: func() state.AllocationResourcesMap {
					// 1 rdma device, 1 gpu device in state -> ratio = 1; need 1 allocation
					return state.AllocationResourcesMap{
						"rdma": {
							"0": preallocState("p", "c"),
						},
						// Keep only one GPU in machine state to ensure ratio=1.
						"gpu": {
							"1": nil,
						},
					}
				}(),
				DeviceTopologyRegistry: buildMultiPriorityAffinityRegistry(map[string]map[int][]string{
					"0": {
						0: {"0"}, // preferred but unavailable
						1: {"1"}, // fallback
					},
				}),
				DeviceReq: &v1alpha1.DeviceRequest{},
			},
			sortedDevices: []string{"1", "2"}, // exclude "0" to force fallback
			expectedResult: &allocate.AllocationResult{
				AllocatedDevices: []string{"1"},
				Success:          true,
			},
		},
		{
			name: "multi-priority: mix across rdma devices with fallback",
			ctx: &allocate.AllocationContext{
				AccompanyResourceName: "rdma",
				ResourceName:          "gpu",
				ResourceReq:           &v1alpha1.ResourceRequest{PodUid: "p", ContainerName: "c"},
				MachineState: func() state.AllocationResourcesMap {
					// 2 rdma devices, 2 gpu devices in state -> ratio = 1; need 2 allocations
					return state.AllocationResourcesMap{
						"rdma": {
							"0": preallocState("p", "c"),
							"1": preallocState("p", "c"),
						},
						// Only two GPUs in machine state to keep ratio = 1
						"gpu": {
							"1": nil,
							"2": nil,
						},
					}
				}(),
				DeviceTopologyRegistry: buildMultiPriorityAffinityRegistry(map[string]map[int][]string{
					"0": {
						0: {"0"},
						1: {"1"},
					},
					"1": {
						0: {"2"},
						1: {"1"},
					},
				}),
				DeviceReq: &v1alpha1.DeviceRequest{},
			},
			sortedDevices: []string{"1", "2"}, // exclude "0" to force r0 to pick priority-1 device
			expectedResult: &allocate.AllocationResult{
				AllocatedDevices: sets.NewString("1", "2").UnsortedList(),
				Success:          true,
			},
		},
		{
			name: "empty affinity: allocate available devices until satisfied",
			ctx: &allocate.AllocationContext{
				AccompanyResourceName: "rdma",
				ResourceName:          "gpu",
				ResourceReq:           &v1alpha1.ResourceRequest{PodUid: "p", ContainerName: "c"},
				MachineState: func() state.AllocationResourcesMap {
					// 2 rdma devices, need 2 gpu devices
					return state.AllocationResourcesMap{
						"rdma": {
							"r0": preallocState("p", "c"),
							"r1": preallocState("p", "c"),
						},
						"gpu": {
							"g0": nil,
							"g1": nil,
							"g2": nil,
						},
					}
				}(),
				DeviceTopologyRegistry: buildNoAffinityRegistry(),
				DeviceReq:              &v1alpha1.DeviceRequest{},
			},
			sortedDevices: []string{"g0", "g1", "g2"},
			expectedResult: &allocate.AllocationResult{
				// ratio = 2/3 -> need 3 devices
				AllocatedDevices: sets.NewString("g0", "g1", "g2").UnsortedList(),
				Success:          true,
			},
		},
		{
			name: "empty affinity: reusable devices count toward target",
			ctx: &allocate.AllocationContext{
				AccompanyResourceName: "rdma",
				ResourceName:          "gpu",
				ResourceReq:           &v1alpha1.ResourceRequest{PodUid: "p", ContainerName: "c"},
				MachineState: func() state.AllocationResourcesMap {
					// 2 rdma devices -> need 2 gpu devices, with one reusable
					return state.AllocationResourcesMap{
						"rdma": {
							"r0": preallocState("p", "c"),
							"r1": preallocState("p", "c"),
						},
						"gpu": {
							"g0": nil,
							"g1": nil,
							"g2": nil,
						},
					}
				}(),
				DeviceTopologyRegistry: buildNoAffinityRegistry(),
				DeviceReq: &v1alpha1.DeviceRequest{
					ReusableDevices: []string{"g0"},
				},
			},
			sortedDevices: []string{"g0", "g1", "g2"},
			expectedResult: &allocate.AllocationResult{
				// ratio = 2/3 -> need 3 devices; one reusable + two selected
				AllocatedDevices: sets.NewString("g0", "g1", "g2").UnsortedList(),
				Success:          true,
			},
		},
		{
			name: "affinity missing for a preallocated device returns error",
			ctx: &allocate.AllocationContext{
				AccompanyResourceName: "rdma",
				ResourceName:          "gpu",
				ResourceReq:           &v1alpha1.ResourceRequest{PodUid: "p", ContainerName: "c"},
				MachineState: func() state.AllocationResourcesMap {
					return state.AllocationResourcesMap{
						"rdma": {
							"r0": preallocState("p", "c"),
							"r1": preallocState("p", "c"),
						},
						"gpu": {
							"g0": nil,
							"g1": nil,
						},
					}
				}(),
				// Provide affinity only for r0 so r1 is missing in returned map (forces error path)
				DeviceTopologyRegistry: buildMultiPriorityAffinityRegistry(map[string]map[int][]string{
					"r0": {0: {"g0"}},
					// r1 intentionally omitted
				}),
				DeviceReq: &v1alpha1.DeviceRequest{},
			},
			sortedDevices: []string{"g0", "g1"},
			expectedErr:   true,
		},
		{
			name: "ratio>1 distributes across accompany devices (limit=1 per device)",
			ctx: &allocate.AllocationContext{
				AccompanyResourceName: "rdma",
				ResourceName:          "gpu",
				ResourceReq:           &v1alpha1.ResourceRequest{PodUid: "p", ContainerName: "c"},
				MachineState: func() state.AllocationResourcesMap {
					// 4 rdma, 2 gpu -> ratio 2, need 2 allocations total
					return state.AllocationResourcesMap{
						"rdma": {
							"r0": preallocState("p", "c"),
							"r1": preallocState("p", "c"),
							"r2": preallocState("p", "c"),
							"r3": preallocState("p", "c"),
						},
						"gpu": {
							"g0": nil,
							"g1": nil,
						},
					}
				}(),
				DeviceTopologyRegistry: buildMultiPriorityAffinityRegistry(map[string]map[int][]string{
					"r0": {0: {"g0"}},
					"r1": {0: {"g1"}},
					"r2": {0: {"g3"}},
					"r3": {0: {"g4"}},
				}),
				DeviceReq: &v1alpha1.DeviceRequest{},
			},
			sortedDevices: []string{"g0", "g1"},
			expectedResult: &allocate.AllocationResult{
				AllocatedDevices: sets.NewString("g0", "g1").UnsortedList(),
				Success:          true,
			},
		},
		{
			name: "empty affinity: not enough devices causes error",
			ctx: &allocate.AllocationContext{
				AccompanyResourceName: "rdma",
				ResourceName:          "gpu",
				ResourceReq:           &v1alpha1.ResourceRequest{PodUid: "p", ContainerName: "c"},
				MachineState: func() state.AllocationResourcesMap {
					// Need 2 allocations (2 rdma, 2 gpu -> ratio 1), but only one device is available in sorted list
					return state.AllocationResourcesMap{
						"rdma": {
							"r0": preallocState("p", "c"),
							"r1": preallocState("p", "c"),
						},
						"gpu": {
							// Two GPUs in machine state so ratio computes to 1
							"g0": nil,
							"g1": nil,
						},
					}
				}(),
				DeviceTopologyRegistry: buildNoAffinityRegistry(),
				DeviceReq:              &v1alpha1.DeviceRequest{},
			},
			// Only g0 available; g1 unavailable -> not enough
			sortedDevices: []string{"g0"},
			expectedErr:   true,
		},
		{
			name: "no pre-allocate resource name",
			ctx: &allocate.AllocationContext{
				AccompanyResourceName: "",
				DeviceReq: &v1alpha1.DeviceRequest{
					DeviceRequest: 2,
				},
				// Non-nil registry to satisfy validation; canonical strategy doesn't use it further
				DeviceTopologyRegistry: machine.NewDeviceTopologyRegistry(),
				ResourceReq:            &v1alpha1.ResourceRequest{},
			},
			sortedDevices: []string{"0", "1", "2", "3"},
			expectedResult: &allocate.AllocationResult{
				AllocatedDevices: []string{"0", "1"},
				Success:          true,
			},
		},
		{
			name: "pre-allocate resource not found",
			ctx: &allocate.AllocationContext{
				AccompanyResourceName: "rdma",
				ResourceName:          "gpu",
				ResourceReq:           &v1alpha1.ResourceRequest{},
				// Empty machine state means no pre-allocated resource entry exists
				MachineState:           state.AllocationResourcesMap{},
				DeviceTopologyRegistry: machine.NewDeviceTopologyRegistry(),
				DeviceReq:              &v1alpha1.DeviceRequest{},
			},
			expectedErr: true,
		},
		{
			name: "not enough devices to allocate",
			ctx: &allocate.AllocationContext{
				AccompanyResourceName: "rdma",
				ResourceName:          "gpu",
				MachineState: func() state.AllocationResourcesMap {
					// 2 rdma devices, 2 gpu devices -> ratio 1
					return state.AllocationResourcesMap{
						"rdma": {
							"0": preallocState("p", "c"),
							"1": preallocState("p", "c"),
						},
						"gpu": {
							"0": nil,
							"1": nil,
						},
					}
				}(),
				DeviceTopologyRegistry: buildSimpleAffinityRegistry(map[string][]string{
					"0": {"0"},
					"1": {"1"},
				}),
				DeviceReq:   &v1alpha1.DeviceRequest{},
				ResourceReq: &v1alpha1.ResourceRequest{},
			},
			preAllocatedResourceToTargetDeviceRatio: 1,
			sortedDevices:                           []string{"0"},
			expectedErr:                             true,
		},
		{
			name: "successful allocation",
			ctx: &allocate.AllocationContext{
				AccompanyResourceName: "rdma",
				ResourceName:          "gpu",
				MachineState: func() state.AllocationResourcesMap {
					// 2 rdma devices, 2 gpu devices -> ratio 1
					return state.AllocationResourcesMap{
						"rdma": {
							"0": preallocState("p", "c"),
							"1": preallocState("p", "c"),
						},
						"gpu": {
							"0": nil,
							"1": nil,
						},
					}
				}(),
				DeviceTopologyRegistry: buildSimpleAffinityRegistry(map[string][]string{
					"0": {"0"},
					"1": {"1"},
				}),
				DeviceReq: &v1alpha1.DeviceRequest{
					ReusableDevices: []string{},
				},
				ResourceReq: &v1alpha1.ResourceRequest{PodUid: "p", ContainerName: "c"},
			},
			preAllocatedResourceToTargetDeviceRatio: 1,
			sortedDevices:                           []string{"0", "1", "2", "3"},
			expectedResult: &allocate.AllocationResult{
				AllocatedDevices: sets.NewString("0", "1").UnsortedList(),
				Success:          true,
			},
		},
		{
			name: "ratio is zero",
			ctx: &allocate.AllocationContext{
				AccompanyResourceName: "rdma",
				ResourceName:          "gpu",
				MachineState: func() state.AllocationResourcesMap {
					// 2 rdma devices, 2 gpu devices -> ratio 1
					return state.AllocationResourcesMap{
						"rdma": {
							"0": preallocState("p", "c"),
							"1": preallocState("p", "c"),
						},
						"gpu": {
							"0": nil,
							"1": nil,
						},
					}
				}(),
				DeviceTopologyRegistry: buildSimpleAffinityRegistry(map[string][]string{
					"0": {"0"},
					"1": {"1"},
				}),
				DeviceReq: &v1alpha1.DeviceRequest{
					ReusableDevices: []string{},
				},
				ResourceReq: &v1alpha1.ResourceRequest{PodUid: "p", ContainerName: "c"},
			},
			preAllocatedResourceToTargetDeviceRatio: 0.5,
			sortedDevices:                           []string{"0", "1", "2", "3"},
			expectedResult: &allocate.AllocationResult{
				AllocatedDevices: sets.NewString("0", "1").UnsortedList(),
				Success:          true,
			},
		},
		{
			name: "ratio 1/2 with affinity: allocate two from one accompany device",
			ctx: &allocate.AllocationContext{
				AccompanyResourceName: "rdma",
				ResourceName:          "gpu",
				ResourceReq:           &v1alpha1.ResourceRequest{PodUid: "p", ContainerName: "c"},
				MachineState: func() state.AllocationResourcesMap {
					// 1 rdma, 2 gpu -> ratio = 1/2 -> need 2 allocations; per-device limit = int(1/(1/2)) = 2
					return state.AllocationResourcesMap{
						"rdma": {
							"r0": preallocState("p", "c"),
						},
						"gpu": {
							"g0": nil,
							"g1": nil,
						},
					}
				}(),
				DeviceTopologyRegistry: buildMultiPriorityAffinityRegistry(map[string]map[int][]string{
					"r0": {0: {"g0", "g1"}},
				}),
				DeviceReq: &v1alpha1.DeviceRequest{},
			},
			sortedDevices: []string{"g0", "g1"},
			expectedResult: &allocate.AllocationResult{
				AllocatedDevices: sets.NewString("g0", "g1").UnsortedList(),
				Success:          true,
			},
		},
		{
			name: "ratio 2/3 with affinity: need 3 but limit 1 per device -> error",
			ctx: &allocate.AllocationContext{
				AccompanyResourceName: "rdma",
				ResourceName:          "gpu",
				ResourceReq:           &v1alpha1.ResourceRequest{PodUid: "p", ContainerName: "c"},
				MachineState: func() state.AllocationResourcesMap {
					// 2 rdma, 3 gpu -> ratio = 2/3 (~0.666) -> need floor(2/0.666)=3
					return state.AllocationResourcesMap{
						"rdma": {
							"r0": preallocState("p", "c"),
							"r1": preallocState("p", "c"),
						},
						"gpu": {
							"g0": nil,
							"g1": nil,
							"g2": nil,
						},
					}
				}(),
				DeviceTopologyRegistry: buildMultiPriorityAffinityRegistry(map[string]map[int][]string{
					"r0": {0: {"g0", "g1", "g2"}},
					"r1": {0: {"g0", "g1", "g2"}},
				}),
				DeviceReq: &v1alpha1.DeviceRequest{},
			},
			sortedDevices: []string{"g0", "g1", "g2"},
			expectedErr:   true,
		},
		{
			name: "ratio 2/5 with affinity: need 5 but total per-device capacity 4 -> error",
			ctx: &allocate.AllocationContext{
				AccompanyResourceName: "rdma",
				ResourceName:          "gpu",
				ResourceReq:           &v1alpha1.ResourceRequest{PodUid: "p", ContainerName: "c"},
				MachineState: func() state.AllocationResourcesMap {
					// 2 rdma, 5 gpu -> ratio = 2/5 = 0.4 -> need floor(2/0.4)=5
					return state.AllocationResourcesMap{
						"rdma": {
							"r0": preallocState("p", "c"),
							"r1": preallocState("p", "c"),
						},
						"gpu": {
							"g0": nil,
							"g1": nil,
							"g2": nil,
							"g3": nil,
							"g4": nil,
						},
					}
				}(),
				DeviceTopologyRegistry: buildMultiPriorityAffinityRegistry(map[string]map[int][]string{
					"r0": {0: {"g0", "g1", "g2"}},
					"r1": {0: {"g3", "g4"}},
				}),
				DeviceReq: &v1alpha1.DeviceRequest{},
			},
			sortedDevices: []string{"g0", "g1", "g2", "g3", "g4"},
			expectedErr:   true,
		},
		{
			name: "ratio 1/3 without affinity: allocate three devices",
			ctx: &allocate.AllocationContext{
				AccompanyResourceName: "rdma",
				ResourceName:          "gpu",
				ResourceReq:           &v1alpha1.ResourceRequest{PodUid: "p", ContainerName: "c"},
				MachineState: func() state.AllocationResourcesMap {
					// 1 rdma device, 3 gpu devices -> ratio = 1/3 -> need 3 allocations
					return state.AllocationResourcesMap{
						"rdma": {
							"r0": preallocState("p", "c"),
						},
						"gpu": {
							"g0": nil,
							"g1": nil,
							"g2": nil,
						},
					}
				}(),
				DeviceTopologyRegistry: buildNoAffinityRegistry(),
				DeviceReq:              &v1alpha1.DeviceRequest{},
			},
			sortedDevices: []string{"g0", "g1", "g2"},
			expectedResult: &allocate.AllocationResult{
				AllocatedDevices: sets.NewString("g0", "g1", "g2").UnsortedList(),
				Success:          true,
			},
		},
		{
			name: "ratio 1/3 with affinity: allocate all from one accompany device",
			ctx: &allocate.AllocationContext{
				AccompanyResourceName: "rdma",
				ResourceName:          "gpu",
				ResourceReq:           &v1alpha1.ResourceRequest{PodUid: "p", ContainerName: "c"},
				MachineState: func() state.AllocationResourcesMap {
					// 1 rdma device, 3 gpu devices -> ratio = 1/3 -> need 3 allocations
					return state.AllocationResourcesMap{
						"rdma": {
							"r0": preallocState("p", "c"),
						},
						"gpu": {
							"g0": nil,
							"g1": nil,
							"g2": nil,
						},
					}
				}(),
				DeviceTopologyRegistry: buildMultiPriorityAffinityRegistry(map[string]map[int][]string{
					"r0": {0: {"g0", "g1", "g2"}},
				}),
				DeviceReq: &v1alpha1.DeviceRequest{},
			},
			sortedDevices: []string{"g0", "g1", "g2"},
			expectedResult: &allocate.AllocationResult{
				AllocatedDevices: sets.NewString("g0", "g1", "g2").UnsortedList(),
				Success:          true,
			},
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			s := NewAccompanyResourceStrategy()
			s.CanonicalStrategy = *canonical.NewCanonicalStrategy()

			result, err := s.Bind(tc.ctx, tc.sortedDevices)

			if tc.expectedErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tc.expectedResult.Success, result.Success)
				assert.ElementsMatch(t, tc.expectedResult.AllocatedDevices, result.AllocatedDevices)
			}
		})
	}
}

// buildSimpleAffinityRegistry builds a real DeviceTopologyRegistry where each rdma device ID
// has affinity to the listed gpu device IDs under the same priority level.
func buildSimpleAffinityRegistry(rdmaToGPU map[string][]string) *machine.DeviceTopologyRegistry {
	reg := machine.NewDeviceTopologyRegistry()

	// Register topology providers for both devices
	reg.RegisterDeviceTopologyProvider("rdma", machine.NewDeviceTopologyProvider())
	reg.RegisterDeviceTopologyProvider("gpu", machine.NewDeviceTopologyProvider())

	// Construct RDMA topology
	rdmaTopo := &machine.DeviceTopology{Devices: map[string]machine.DeviceInfo{}}
	for rdmaID := range rdmaToGPU {
		ap := machine.AffinityPriority{PriorityLevel: 0, Dimension: machine.Dimension{Name: "link", Value: rdmaID}}
		rdmaTopo.Devices[rdmaID] = machine.DeviceInfo{NumaNodes: []int{}, DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{ap: {rdmaID}}}
	}
	_ = reg.SetDeviceTopology("rdma", rdmaTopo)

	// Construct GPU topology with matching affinity keys
	gpuTopo := &machine.DeviceTopology{Devices: map[string]machine.DeviceInfo{}}
	// Include at least devices referenced in tests
	for _, gpus := range rdmaToGPU {
		for _, gid := range gpus {
			if _, ok := gpuTopo.Devices[gid]; !ok {
				ap := machine.AffinityPriority{PriorityLevel: 0, Dimension: machine.Dimension{Name: "link", Value: gid}}
				gpuTopo.Devices[gid] = machine.DeviceInfo{NumaNodes: []int{}, DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{ap: {gid}}}
			}
		}
	}
	// Add a couple of extra GPUs to match sortedDevices lists when needed
	for _, extra := range []string{"2", "3"} {
		if _, ok := gpuTopo.Devices[extra]; !ok {
			ap := machine.AffinityPriority{PriorityLevel: 0, Dimension: machine.Dimension{Name: "link", Value: extra}}
			gpuTopo.Devices[extra] = machine.DeviceInfo{NumaNodes: []int{}, DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{ap: {extra}}}
		}
	}
	_ = reg.SetDeviceTopology("gpu", gpuTopo)

	return reg
}

// buildMultiPriorityAffinityRegistry builds a registry where each rdma device ID has multiple
// priority levels mapped to specific gpu device IDs. The generated topologies ensure that
// GetAffinityDevices returns a map with contiguous priority levels starting from 0.
func buildMultiPriorityAffinityRegistry(cfg map[string]map[int][]string) *machine.DeviceTopologyRegistry {
	reg := machine.NewDeviceTopologyRegistry()

	reg.RegisterDeviceTopologyProvider("rdma", machine.NewDeviceTopologyProvider())
	reg.RegisterDeviceTopologyProvider("gpu", machine.NewDeviceTopologyProvider())

	rdmaTopo := &machine.DeviceTopology{Devices: map[string]machine.DeviceInfo{}}
	for rdmaID, prios := range cfg {
		da := make(map[machine.AffinityPriority]machine.DeviceIDs)
		for p := range prios {
			ap := machine.AffinityPriority{PriorityLevel: p, Dimension: machine.Dimension{Name: "p", Value: rdmaID}}
			da[ap] = machine.DeviceIDs{rdmaID}
		}
		rdmaTopo.Devices[rdmaID] = machine.DeviceInfo{NumaNodes: []int{}, DeviceAffinity: da}
	}
	_ = reg.SetDeviceTopology("rdma", rdmaTopo)

	gpuTopo := &machine.DeviceTopology{Devices: map[string]machine.DeviceInfo{}}
	for rdmaID, prios := range cfg {
		for p, gpus := range prios {
			ap := machine.AffinityPriority{PriorityLevel: p, Dimension: machine.Dimension{Name: "p", Value: rdmaID}}
			for _, gid := range gpus {
				info := gpuTopo.Devices[gid]
				if info.DeviceAffinity == nil {
					info = machine.DeviceInfo{NumaNodes: []int{}, DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{}}
				}
				info.DeviceAffinity[ap] = append(info.DeviceAffinity[ap], gid)
				gpuTopo.Devices[gid] = info
			}
		}
	}
	_ = reg.SetDeviceTopology("gpu", gpuTopo)

	return reg
}

// buildNoAffinityRegistry builds a registry where RDMA and GPU topologies have disjoint affinity dimensions
// so that GetAffinityDevices returns an empty map without error.
func buildNoAffinityRegistry() *machine.DeviceTopologyRegistry {
	reg := machine.NewDeviceTopologyRegistry()

	reg.RegisterDeviceTopologyProvider("rdma", machine.NewDeviceTopologyProvider())
	reg.RegisterDeviceTopologyProvider("gpu", machine.NewDeviceTopologyProvider())

	// RDMA devices use dimension "rdma_link"
	rdmaTopo := &machine.DeviceTopology{Devices: map[string]machine.DeviceInfo{}}
	for _, id := range []string{"r0", "r1"} {
		ap := machine.AffinityPriority{PriorityLevel: 0, Dimension: machine.Dimension{Name: "rdma_link", Value: id}}
		rdmaTopo.Devices[id] = machine.DeviceInfo{NumaNodes: []int{}, DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{ap: {id}}}
	}
	_ = reg.SetDeviceTopology("rdma", rdmaTopo)

	// GPU devices use a different dimension "gpu_bus" so no affinity keys match RDMA
	gpuTopo := &machine.DeviceTopology{Devices: map[string]machine.DeviceInfo{}}
	for _, id := range []string{"g0", "g1", "g2"} {
		ap := machine.AffinityPriority{PriorityLevel: 0, Dimension: machine.Dimension{Name: "gpu_bus", Value: id}}
		gpuTopo.Devices[id] = machine.DeviceInfo{NumaNodes: []int{}, DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{ap: {id}}}
	}
	_ = reg.SetDeviceTopology("gpu", gpuTopo)

	return reg
}

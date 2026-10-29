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
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	gpuconsts "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/state"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/strategy/allocate"
	qrm "github.com/kubewharf/katalyst-core/pkg/config/agent/qrm"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
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

func resourceStateWithOwners(annotationKey string, annotationValues ...string) *state.AllocationState {
	podEntries := make(state.PodEntries, len(annotationValues))
	for i, value := range annotationValues {
		podEntries[fmt.Sprintf("pod-%d", i)] = state.ContainerEntries{
			"container": &state.AllocationInfo{
				AllocationMeta: commonstate.AllocationMeta{
					Annotations: map[string]string{annotationKey: value},
				},
			},
		}
	}
	return &state.AllocationState{PodEntries: podEntries}
}

func TestGetSharedResourceAllocationCandidates(t *testing.T) {
	t.Parallel()

	const annotationKey = "example.com/shared"
	resourceState := state.AllocationMap{
		"r0": resourceStateWithOwners(annotationKey, "true"),
		"r1": nil,
		"r2": resourceStateWithOwners(annotationKey, "false"),
		"r3": resourceStateWithOwners(annotationKey, "true", "false"),
	}
	eligible := sets.NewString("r3", "r2", "r1", "r0")
	available := sets.NewString("r1", "r2", "r3")

	assert.Equal(t, []string{"r0", "r1"}, getSharedResourceAllocationCandidates(
		eligible,
		resourceState,
		available,
		sets.NewString(),
		annotationKey,
	))
	assert.Equal(t, []string{"r1"}, getSharedResourceAllocationCandidates(
		eligible,
		resourceState,
		available,
		sets.NewString("r0"),
		annotationKey,
	))
}

func TestClassifyResourceOwnership(t *testing.T) {
	t.Parallel()

	const annotationKey = "example.com/shared"
	assert.Equal(t, resourceFree, classifyResourceOwnership(nil, annotationKey))
	assert.Equal(t, resourceFree, classifyResourceOwnership(&state.AllocationState{}, annotationKey))
	assert.Equal(t, resourceShared, classifyResourceOwnership(
		resourceStateWithOwners(annotationKey, "true", " TRUE "),
		annotationKey,
	))
	assert.Equal(t, resourceExclusive, classifyResourceOwnership(
		resourceStateWithOwners(annotationKey, "true", "false"),
		annotationKey,
	))
	assert.Equal(t, resourceExclusive, classifyResourceOwnership(
		resourceStateWithOwners("other", "true"),
		annotationKey,
	))
}

func TestResolveAffinityDeviceIDsForAccompanyResources(t *testing.T) {
	t.Parallel()

	affinity := map[string]map[string]machine.DeviceIDs{
		"g0": {"fine": {"r1", "r0"}},
		"g1": {"fine": {"r0"}},
	}

	affinityDeviceIDs, err := resolveAffinityDeviceIDsForAccompanyResources(
		[]string{"g0", "g1"},
		affinity,
		[]string{"fine"},
	)
	assert.NoError(t, err)
	assert.Equal(t, sets.NewString("r0"), affinityDeviceIDs)

	_, err = resolveAffinityDeviceIDsForAccompanyResources(
		[]string{"g0", "g2"},
		affinity,
		[]string{"fine"},
	)
	assert.Error(t, err)
}

func TestBindHybridReusesSharedDeviceWhenNoDeviceIsAvailable(t *testing.T) {
	t.Parallel()

	const annotationKey = "example.com/shared"
	ctx := &allocate.AllocationContext{
		AccompanyResourceName: "gpu",
		ResourceName:          "rdma",
		ResourceReq: &v1alpha1.ResourceRequest{
			PodUid:        "pod",
			ContainerName: "container",
			Annotations:   map[string]string{annotationKey: "true"},
		},
		DeviceReq: &v1alpha1.DeviceRequest{
			DeviceRequest: 1,
		},
		DeviceTopologyRegistry: buildNumaAffinityRegistry(
			map[string][]int{"r0": {0}},
			map[string][]int{"g0": {0}},
		),
		GPUQRMPluginConfig: &qrm.GPUQRMPluginConfig{
			GPUDeviceNames:                          []string{"gpu"},
			RDMADeviceNames:                         []string{"rdma"},
			EnableHybridAccompanyResourceAllocation: true,
			AccompanyResourceSharedAnnotationKey:    annotationKey,
		},
		Emitter: metrics.DummyMetrics{},
		MachineState: state.AllocationResourcesMap{
			gpuconsts.GPUDeviceType: {
				"g0": preallocState("pod", "container"),
			},
			gpuconsts.RDMADeviceType: {
				"r0": resourceStateWithOwners(annotationKey, "true"),
			},
		},
		DeviceNameToTypeMap: map[string]string{
			"gpu":  gpuconsts.GPUDeviceType,
			"rdma": gpuconsts.RDMADeviceType,
		},
	}

	result, err := NewAccompanyResourceStrategy().Bind(ctx, nil)

	assert.NoError(t, err)
	assert.Equal(t, []string{"r0"}, result.AllocatedDevices)
}

func TestBindHybridExclusiveChecksAvailableDeviceCount(t *testing.T) {
	t.Parallel()

	ctx := &allocate.AllocationContext{
		AccompanyResourceName: "gpu",
		ResourceName:          "rdma",
		ResourceReq: &v1alpha1.ResourceRequest{
			PodUid:        "pod",
			ContainerName: "container",
		},
		DeviceReq: &v1alpha1.DeviceRequest{
			DeviceRequest:    2,
			AvailableDevices: []string{"r0"},
		},
		DeviceTopologyRegistry: buildNumaAffinityRegistry(
			map[string][]int{"r0": {0}, "r1": {0}},
			map[string][]int{"g0": {0}, "g1": {0}},
		),
		GPUQRMPluginConfig: &qrm.GPUQRMPluginConfig{
			GPUDeviceNames:                          []string{"gpu"},
			RDMADeviceNames:                         []string{"rdma"},
			EnableHybridAccompanyResourceAllocation: true,
			AccompanyResourceSharedAnnotationKey:    "example.com/shared",
		},
		Emitter: metrics.DummyMetrics{},
		MachineState: state.AllocationResourcesMap{
			gpuconsts.GPUDeviceType: {
				"g0": preallocState("pod", "container"),
				"g1": preallocState("pod", "container"),
			},
			gpuconsts.RDMADeviceType: {
				"r0": nil,
				"r1": nil,
			},
		},
		DeviceNameToTypeMap: map[string]string{
			"gpu":  gpuconsts.GPUDeviceType,
			"rdma": gpuconsts.RDMADeviceType,
		},
	}

	result, err := NewAccompanyResourceStrategy().Bind(ctx, []string{"r0"})

	assert.ErrorContains(t, err, "not enough devices: need 2, have 1")
	assert.False(t, result.Success)
}

func TestBindExclusiveUsesCanonicalAffinityDeviceOnlyInHybridMode(t *testing.T) {
	t.Parallel()

	const annotationKey = "example.com/shared"
	testCases := []struct {
		name         string
		enableHybrid bool
		expected     []string
	}{
		{
			name:         "hybrid allocation does not use a sibling affinity device",
			enableHybrid: true,
			expected:     []string{},
		},
		{
			name:         "feature disabled preserves existing affinity allocation",
			enableHybrid: false,
			expected:     []string{"r1"},
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			ctx := &allocate.AllocationContext{
				AccompanyResourceName: "gpu",
				ResourceName:          "rdma",
				ResourceReq: &v1alpha1.ResourceRequest{
					PodUid:        "pod",
					ContainerName: "container",
				},
				DeviceReq: &v1alpha1.DeviceRequest{
					DeviceRequest:    1,
					AvailableDevices: []string{"r1"},
				},
				DeviceTopologyRegistry: buildSingleDimensionAffinityRegistry(
					"pcie",
					[]string{"r0", "r1"},
					[]string{"g0", "g1"},
				),
				GPUQRMPluginConfig: &qrm.GPUQRMPluginConfig{
					GPUDeviceNames:                          []string{"gpu"},
					RDMADeviceNames:                         []string{"rdma"},
					EnableHybridAccompanyResourceAllocation: tc.enableHybrid,
					AccompanyResourceSharedAnnotationKey:    annotationKey,
				},
				Emitter: metrics.DummyMetrics{},
				MachineState: state.AllocationResourcesMap{
					gpuconsts.GPUDeviceType: {
						"g0": preallocState("pod", "container"),
						"g1": nil,
					},
					gpuconsts.RDMADeviceType: {
						"r0": preallocState("other-pod", "container"),
						"r1": nil,
					},
				},
				DeviceNameToTypeMap: map[string]string{
					"gpu":  gpuconsts.GPUDeviceType,
					"rdma": gpuconsts.RDMADeviceType,
				},
			}

			result, err := NewAccompanyResourceStrategy().Bind(ctx, []string{"r1"})

			assert.NoError(t, err)
			assert.Equal(t, tc.expected, result.AllocatedDevices)
		})
	}
}

func TestBindHybridExclusiveRoundsUpFractionalDemand(t *testing.T) {
	t.Parallel()

	ctx := &allocate.AllocationContext{
		AccompanyResourceName: "gpu",
		ResourceName:          "rdma",
		ResourceReq: &v1alpha1.ResourceRequest{
			PodUid:        "pod",
			ContainerName: "container",
		},
		DeviceReq: &v1alpha1.DeviceRequest{
			DeviceRequest:    1,
			AvailableDevices: []string{"r0"},
		},
		DeviceTopologyRegistry: buildNumaAffinityRegistry(
			map[string][]int{"r0": {0}},
			map[string][]int{"g0": {0}, "g1": {0}, "g2": {0}, "g3": {0}},
		),
		GPUQRMPluginConfig: &qrm.GPUQRMPluginConfig{
			GPUDeviceNames:                          []string{"gpu"},
			RDMADeviceNames:                         []string{"rdma"},
			EnableHybridAccompanyResourceAllocation: true,
			AccompanyResourceSharedAnnotationKey:    "example.com/shared",
		},
		Emitter: metrics.DummyMetrics{},
		MachineState: state.AllocationResourcesMap{
			gpuconsts.GPUDeviceType: {
				"g0": preallocState("pod", "container"),
				"g1": preallocState("pod", "container"),
				"g2": nil,
				"g3": nil,
			},
			gpuconsts.RDMADeviceType: {
				"r0": nil,
			},
		},
		DeviceNameToTypeMap: map[string]string{
			"gpu":  gpuconsts.GPUDeviceType,
			"rdma": gpuconsts.RDMADeviceType,
		},
	}

	result, err := NewAccompanyResourceStrategy().Bind(ctx, []string{"r0"})

	assert.NoError(t, err)
	assert.Equal(t, []string{"r0"}, result.AllocatedDevices)
}

func TestAllocateSharedAffinityDevicesReturnsPartialAllocation(t *testing.T) {
	t.Parallel()

	ctx := &allocate.AllocationContext{
		AccompanyResourceName: "gpu",
		ResourceName:          "rdma",
		ResourceReq:           &v1alpha1.ResourceRequest{},
		DeviceReq: &v1alpha1.DeviceRequest{
			AvailableDevices: []string{"r0"},
		},
		GPUQRMPluginConfig: &qrm.GPUQRMPluginConfig{},
		Emitter:            metrics.DummyMetrics{},
		MachineState: state.AllocationResourcesMap{
			gpuconsts.RDMADeviceType: {
				"r0": nil,
			},
		},
	}

	result, err := NewAccompanyResourceStrategy().allocateSharedAffinityDevices(
		ctx,
		[]string{"g0", "g1"},
		map[string]map[string]machine.DeviceIDs{
			"g0": {"fine": {"r0"}},
			"g1": {"fine": {"r0"}},
		},
		[]string{"fine"},
		sets.NewString(),
		2,
		string(gpuconsts.RDMADeviceType),
	)

	assert.NoError(t, err)
	assert.Equal(t, &allocate.AllocationResult{
		AllocatedDevices: []string{"r0"},
		Success:          true,
	}, result)
}

func TestBindHybridReturnsSuccessWhenNoAffinityDevicesExist(t *testing.T) {
	t.Parallel()

	ctx := &allocate.AllocationContext{
		AccompanyResourceName: "gpu",
		ResourceName:          "rdma",
		ResourceReq: &v1alpha1.ResourceRequest{
			PodUid:        "pod",
			ContainerName: "container",
		},
		DeviceReq:              &v1alpha1.DeviceRequest{},
		DeviceTopologyRegistry: buildNoAffinityRegistry(),
		GPUQRMPluginConfig: &qrm.GPUQRMPluginConfig{
			GPUDeviceNames:                          []string{"gpu"},
			RDMADeviceNames:                         []string{"rdma"},
			EnableHybridAccompanyResourceAllocation: true,
		},
		Emitter: metrics.DummyMetrics{},
		MachineState: state.AllocationResourcesMap{
			gpuconsts.GPUDeviceType: {
				"g0": preallocState("pod", "container"),
			},
			gpuconsts.RDMADeviceType: {
				"r0": nil,
			},
		},
		DeviceNameToTypeMap: map[string]string{
			"gpu":  gpuconsts.GPUDeviceType,
			"rdma": gpuconsts.RDMADeviceType,
		},
	}

	result, err := NewAccompanyResourceStrategy().Bind(ctx, []string{"r0"})

	assert.NoError(t, err)
	assert.Equal(t, &allocate.AllocationResult{
		AllocatedDevices: []string{},
		Success:          true,
	}, result)
}

func TestBindHybridClassicTopologyMatrix(t *testing.T) {
	t.Parallel()

	const (
		podUID        = "pod"
		containerName = "container"
		annotationKey = "example.com/shared"
	)
	topologies := []struct {
		gpuCount  int
		rdmaCount int
	}{
		{gpuCount: 8, rdmaCount: 2},
		{gpuCount: 8, rdmaCount: 4},
		{gpuCount: 8, rdmaCount: 8},
		{gpuCount: 16, rdmaCount: 4},
		{gpuCount: 16, rdmaCount: 8},
	}
	gpuRequests := []int{1, 2, 4, 8}

	for _, topology := range topologies {
		topology := topology
		for _, gpuRequest := range gpuRequests {
			gpuRequest := gpuRequest
			for _, shared := range []bool{false, true} {
				shared := shared
				t.Run(fmt.Sprintf("%d-gpu-%d-rdma/request-%d/shared-%t",
					topology.gpuCount, topology.rdmaCount, gpuRequest, shared), func(t *testing.T) {
					t.Parallel()

					registry, machineState, expectedRDMAs := buildClassicTopologyAllocation(
						t,
						topology.gpuCount,
						topology.rdmaCount,
						gpuRequest,
						podUID,
						containerName,
					)
					annotations := map[string]string{}
					if shared {
						annotations[annotationKey] = "true"
					}
					expectedCount := (gpuRequest*topology.rdmaCount + topology.gpuCount - 1) / topology.gpuCount
					availableRDMAs := make([]string, topology.rdmaCount)
					for i := range availableRDMAs {
						availableRDMAs[i] = fmt.Sprintf("r%d", i)
					}

					result, err := NewAccompanyResourceStrategy().Bind(&allocate.AllocationContext{
						AccompanyResourceName: "gpu",
						ResourceName:          "rdma",
						ResourceReq: &v1alpha1.ResourceRequest{
							PodUid:        podUID,
							ContainerName: containerName,
							Annotations:   annotations,
						},
						DeviceReq: &v1alpha1.DeviceRequest{
							DeviceRequest:    uint64(expectedCount),
							AvailableDevices: availableRDMAs,
						},
						DeviceTopologyRegistry: registry,
						GPUQRMPluginConfig: &qrm.GPUQRMPluginConfig{
							GPUDeviceNames:                          []string{"gpu"},
							RDMADeviceNames:                         []string{"rdma"},
							EnableHybridAccompanyResourceAllocation: true,
							AccompanyResourceSharedAnnotationKey:    annotationKey,
						},
						Emitter:      metrics.DummyMetrics{},
						MachineState: machineState,
						DeviceNameToTypeMap: map[string]string{
							"gpu":  gpuconsts.GPUDeviceType,
							"rdma": gpuconsts.RDMADeviceType,
						},
					}, availableRDMAs)

					assert.NoError(t, err)
					assert.True(t, result.Success)
					assert.Len(t, result.AllocatedDevices, expectedCount)
					assert.Equal(t, expectedRDMAs, result.AllocatedDevices)
				})
			}
		}
	}
}

func TestBindDisabledReturnsReusableDeviceWithoutTopology(t *testing.T) {
	t.Parallel()

	ctx := &allocate.AllocationContext{
		AccompanyResourceName: "gpu",
		ResourceName:          "rdma",
		ResourceReq: &v1alpha1.ResourceRequest{
			PodUid:        "pod",
			ContainerName: "container",
		},
		DeviceReq: &v1alpha1.DeviceRequest{
			DeviceRequest:   1,
			ReusableDevices: []string{"r0"},
		},
		DeviceTopologyRegistry: machine.NewDeviceTopologyRegistry(metrics.DummyMetrics{}),
		GPUQRMPluginConfig: &qrm.GPUQRMPluginConfig{
			GPUDeviceNames:  []string{"gpu"},
			RDMADeviceNames: []string{"rdma"},
		},
		Emitter: metrics.DummyMetrics{},
		MachineState: state.AllocationResourcesMap{
			gpuconsts.GPUDeviceType: {
				"g0": preallocState("pod", "container"),
			},
			gpuconsts.RDMADeviceType: {
				"r0": nil,
			},
		},
		DeviceNameToTypeMap: map[string]string{
			"gpu":  gpuconsts.GPUDeviceType,
			"rdma": gpuconsts.RDMADeviceType,
		},
	}

	result, err := NewAccompanyResourceStrategy().Bind(ctx, []string{"r0"})

	assert.NoError(t, err)
	assert.ElementsMatch(t, []string{"r0"}, result.AllocatedDevices)
}

func TestBind(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name                                    string
		ctx                                     *allocate.AllocationContext
		sortedDevices                           []string
		expectedResult                          *allocate.AllocationResult
		expectedAllocatedDeviceCandidates       []string
		expectedAllocatedDeviceCount            int
		expectedErr                             bool
		preAllocatedResourceToTargetDeviceRatio float64
	}{
		{
			name: "ratio 2/1 with affinity: allocate one for two accompany",
			ctx: &allocate.AllocationContext{
				AccompanyResourceName: "rdma",
				ResourceName:          "gpu",
				ResourceReq:           &v1alpha1.ResourceRequest{PodUid: "p", ContainerName: "c"},
				MachineState: func() state.AllocationResourcesMap {
					// 2 rdma, 1 gpu -> ratio = 2 -> floor(2/2) = 1 allocation
					return state.AllocationResourcesMap{
						gpuconsts.RDMADeviceType: {
							"r0": preallocState("p", "c"),
							"r1": preallocState("p", "c"),
						},
						gpuconsts.GPUDeviceType: {
							"g0": nil,
						},
					}
				}(),
				DeviceTopologyRegistry: buildMultiPriorityAffinityRegistry(map[string]map[int][]string{
					"r0": {0: {"g0"}},
					"r1": {0: {"g0"}},
				}),
				DeviceReq:          &v1alpha1.DeviceRequest{},
				GPUQRMPluginConfig: &qrm.GPUQRMPluginConfig{GPUDeviceNames: []string{"gpu"}, RDMADeviceNames: []string{"rdma"}},
			},
			sortedDevices: []string{"g0"},
			expectedResult: &allocate.AllocationResult{
				AllocatedDevices: sets.NewString("g0").UnsortedList(),
				Success:          true,
			},
		},
		{
			name: "ratio 1/1 with affinity: allocate equal to accompany count",
			ctx: &allocate.AllocationContext{
				AccompanyResourceName: "rdma",
				ResourceName:          "gpu",
				ResourceReq:           &v1alpha1.ResourceRequest{PodUid: "p", ContainerName: "c"},
				MachineState: func() state.AllocationResourcesMap {
					// 1 rdma, 1 gpu -> ratio = 1 -> need 1 allocation
					return state.AllocationResourcesMap{
						gpuconsts.RDMADeviceType: {
							"r0": preallocState("p", "c"),
						},
						gpuconsts.GPUDeviceType: {
							"g0": nil,
						},
					}
				}(),
				DeviceTopologyRegistry: buildSimpleAffinityRegistry(map[string][]string{
					"r0": {"g0"},
				}),
				DeviceReq:          &v1alpha1.DeviceRequest{},
				GPUQRMPluginConfig: &qrm.GPUQRMPluginConfig{GPUDeviceNames: []string{"gpu"}, RDMADeviceNames: []string{"rdma"}},
			},
			sortedDevices: []string{"g0", "g1"},
			expectedResult: &allocate.AllocationResult{
				AllocatedDevices: sets.NewString("g0").UnsortedList(),
				Success:          true,
			},
		},
		{
			name: "multi-priority: fallback to lower priority for single rdma",
			ctx: &allocate.AllocationContext{
				AccompanyResourceName: "rdma",
				ResourceName:          "gpu",
				ResourceReq:           &v1alpha1.ResourceRequest{PodUid: "p", ContainerName: "c"},
				MachineState: func() state.AllocationResourcesMap {
					// 1 rdma device, 1 gpu device in state -> ratio = 1; need 1 allocation
					return state.AllocationResourcesMap{
						gpuconsts.RDMADeviceType: {
							"0": preallocState("p", "c"),
						},
						// Keep only one GPU in machine state to ensure ratio=1.
						gpuconsts.GPUDeviceType: {
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
				DeviceReq:          &v1alpha1.DeviceRequest{},
				GPUQRMPluginConfig: &qrm.GPUQRMPluginConfig{GPUDeviceNames: []string{"gpu"}, RDMADeviceNames: []string{"rdma"}},
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
						gpuconsts.RDMADeviceType: {
							"0": preallocState("p", "c"),
							"1": preallocState("p", "c"),
						},
						// Only two GPUs in machine state to keep ratio = 1
						gpuconsts.GPUDeviceType: {
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
				DeviceReq:          &v1alpha1.DeviceRequest{},
				GPUQRMPluginConfig: &qrm.GPUQRMPluginConfig{GPUDeviceNames: []string{"gpu"}, RDMADeviceNames: []string{"rdma"}},
			},
			sortedDevices: []string{"1", "2"}, // exclude "0" to force r0 to pick priority-1 device
			expectedResult: &allocate.AllocationResult{
				AllocatedDevices: sets.NewString("1", "2").UnsortedList(),
				Success:          true,
			},
		},
		{
			name: "empty affinity: returns success with empty allocation when no affinity devices",
			ctx: &allocate.AllocationContext{
				AccompanyResourceName: "rdma",
				ResourceName:          "gpu",
				ResourceReq:           &v1alpha1.ResourceRequest{PodUid: "p", ContainerName: "c"},
				MachineState: func() state.AllocationResourcesMap {
					return state.AllocationResourcesMap{
						gpuconsts.RDMADeviceType: {
							"r0": preallocState("p", "c"),
							"r1": preallocState("p", "c"),
						},
						gpuconsts.GPUDeviceType: {
							"g0": nil,
							"g1": nil,
							"g2": nil,
						},
					}
				}(),
				DeviceTopologyRegistry: buildNoAffinityRegistry(),
				DeviceReq:              &v1alpha1.DeviceRequest{},
				GPUQRMPluginConfig:     &qrm.GPUQRMPluginConfig{GPUDeviceNames: []string{"gpu"}, RDMADeviceNames: []string{"rdma"}},
			},
			sortedDevices: []string{"g0", "g1", "g2"},
			expectedResult: &allocate.AllocationResult{
				AllocatedDevices: []string{},
				Success:          true,
			},
		},
		{
			name: "empty affinity with reusable devices returns success with empty allocation",
			ctx: &allocate.AllocationContext{
				AccompanyResourceName: "rdma",
				ResourceName:          "gpu",
				ResourceReq:           &v1alpha1.ResourceRequest{PodUid: "p", ContainerName: "c"},
				MachineState: func() state.AllocationResourcesMap {
					return state.AllocationResourcesMap{
						gpuconsts.RDMADeviceType: {
							"r0": preallocState("p", "c"),
							"r1": preallocState("p", "c"),
						},
						gpuconsts.GPUDeviceType: {
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
				GPUQRMPluginConfig: &qrm.GPUQRMPluginConfig{GPUDeviceNames: []string{"gpu"}, RDMADeviceNames: []string{"rdma"}},
			},
			sortedDevices: []string{"g0", "g1", "g2"},
			expectedResult: &allocate.AllocationResult{
				AllocatedDevices: []string{},
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
						gpuconsts.RDMADeviceType: {
							"r0": preallocState("p", "c"),
							"r1": preallocState("p", "c"),
						},
						gpuconsts.GPUDeviceType: {
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
				DeviceReq:          &v1alpha1.DeviceRequest{},
				GPUQRMPluginConfig: &qrm.GPUQRMPluginConfig{GPUDeviceNames: []string{"gpu"}, RDMADeviceNames: []string{"rdma"}},
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
						gpuconsts.RDMADeviceType: {
							"r0": preallocState("p", "c"),
							"r1": preallocState("p", "c"),
							"r2": preallocState("p", "c"),
							"r3": preallocState("p", "c"),
						},
						gpuconsts.GPUDeviceType: {
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
				DeviceReq:          &v1alpha1.DeviceRequest{},
				GPUQRMPluginConfig: &qrm.GPUQRMPluginConfig{GPUDeviceNames: []string{"gpu"}, RDMADeviceNames: []string{"rdma"}},
			},
			sortedDevices: []string{"g0", "g1"},
			expectedResult: &allocate.AllocationResult{
				AllocatedDevices: sets.NewString("g0", "g1").UnsortedList(),
				Success:          true,
			},
		},
		{
			name: "empty affinity: returns success with empty allocation when not enough devices",
			ctx: &allocate.AllocationContext{
				AccompanyResourceName: "rdma",
				ResourceName:          "gpu",
				ResourceReq:           &v1alpha1.ResourceRequest{PodUid: "p", ContainerName: "c"},
				MachineState: func() state.AllocationResourcesMap {
					// Need 2 allocations (2 rdma, 2 gpu -> ratio 1), but only one device is available in sorted list
					return state.AllocationResourcesMap{
						gpuconsts.RDMADeviceType: {
							"r0": preallocState("p", "c"),
							"r1": preallocState("p", "c"),
						},
						gpuconsts.GPUDeviceType: {
							// Two GPUs in machine state so ratio computes to 1
							"g0": nil,
							"g1": nil,
						},
					}
				}(),
				DeviceTopologyRegistry: buildNoAffinityRegistry(),
				DeviceReq:              &v1alpha1.DeviceRequest{},
				GPUQRMPluginConfig:     &qrm.GPUQRMPluginConfig{GPUDeviceNames: []string{"gpu"}, RDMADeviceNames: []string{"rdma"}},
			},
			// Only g0 available; g1 unavailable -> not enough
			sortedDevices: []string{"g0"},
			expectedResult: &allocate.AllocationResult{
				AllocatedDevices: []string{},
				Success:          true,
			},
		},
		{
			name: "no pre-allocate resource name",
			ctx: &allocate.AllocationContext{
				AccompanyResourceName: "",
				DeviceReq: &v1alpha1.DeviceRequest{
					DeviceRequest: 2,
				},
				// Non-nil registry to satisfy validation; canonical strategy doesn't use it further
				DeviceTopologyRegistry: machine.NewDeviceTopologyRegistry(metrics.DummyMetrics{}),
				ResourceReq:            &v1alpha1.ResourceRequest{},
			},
			sortedDevices: []string{"0", "1", "2", "3"},
			expectedErr:   true,
		},
		{
			name: "pre-allocate resource not found",
			ctx: &allocate.AllocationContext{
				AccompanyResourceName: "rdma",
				ResourceName:          "gpu",
				ResourceReq:           &v1alpha1.ResourceRequest{},
				// Empty machine state means no pre-allocated resource entry exists
				MachineState:           state.AllocationResourcesMap{},
				DeviceTopologyRegistry: machine.NewDeviceTopologyRegistry(metrics.DummyMetrics{}),
				DeviceReq:              &v1alpha1.DeviceRequest{},
			},
			expectedErr: true,
		},
		{
			name: "not enough devices to allocate returns success with partial allocation",
			ctx: &allocate.AllocationContext{
				AccompanyResourceName: "rdma",
				ResourceName:          "gpu",
				MachineState: func() state.AllocationResourcesMap {
					// 2 rdma devices, 2 gpu devices -> ratio 1
					return state.AllocationResourcesMap{
						gpuconsts.RDMADeviceType: {
							"0": preallocState("p", "c"),
							"1": preallocState("p", "c"),
						},
						gpuconsts.GPUDeviceType: {
							"0": nil,
							"1": nil,
						},
					}
				}(),
				DeviceTopologyRegistry: buildSimpleAffinityRegistry(map[string][]string{
					"0": {"0"},
					"1": {"1"},
				}),
				DeviceReq:          &v1alpha1.DeviceRequest{},
				ResourceReq:        &v1alpha1.ResourceRequest{PodUid: "p", ContainerName: "c"},
				GPUQRMPluginConfig: &qrm.GPUQRMPluginConfig{GPUDeviceNames: []string{"gpu"}, RDMADeviceNames: []string{"rdma"}},
			},
			preAllocatedResourceToTargetDeviceRatio: 1,
			// Need 2 devices (2 accompany rdmas, ratio 1) but only "0" is available -> not enough.
			sortedDevices: []string{"0"},
			expectedResult: &allocate.AllocationResult{
				AllocatedDevices: sets.NewString("0").UnsortedList(),
				Success:          true,
			},
		},
		{
			name: "successful allocation",
			ctx: &allocate.AllocationContext{
				AccompanyResourceName: "rdma",
				ResourceName:          "gpu",
				MachineState: func() state.AllocationResourcesMap {
					// 2 rdma devices, 2 gpu devices -> ratio 1
					return state.AllocationResourcesMap{
						gpuconsts.RDMADeviceType: {
							"0": preallocState("p", "c"),
							"1": preallocState("p", "c"),
						},
						gpuconsts.GPUDeviceType: {
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
				ResourceReq:        &v1alpha1.ResourceRequest{PodUid: "p", ContainerName: "c"},
				GPUQRMPluginConfig: &qrm.GPUQRMPluginConfig{GPUDeviceNames: []string{"gpu"}, RDMADeviceNames: []string{"rdma"}},
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
						gpuconsts.RDMADeviceType: {
							"0": preallocState("p", "c"),
							"1": preallocState("p", "c"),
						},
						gpuconsts.GPUDeviceType: {
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
				ResourceReq:        &v1alpha1.ResourceRequest{PodUid: "p", ContainerName: "c"},
				GPUQRMPluginConfig: &qrm.GPUQRMPluginConfig{GPUDeviceNames: []string{"gpu"}, RDMADeviceNames: []string{"rdma"}},
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
						gpuconsts.RDMADeviceType: {
							"r0": preallocState("p", "c"),
						},
						gpuconsts.GPUDeviceType: {
							"g0": nil,
							"g1": nil,
						},
					}
				}(),
				DeviceTopologyRegistry: buildMultiPriorityAffinityRegistry(map[string]map[int][]string{
					"r0": {0: {"g0", "g1"}},
				}),
				DeviceReq:          &v1alpha1.DeviceRequest{},
				GPUQRMPluginConfig: &qrm.GPUQRMPluginConfig{GPUDeviceNames: []string{"gpu"}, RDMADeviceNames: []string{"rdma"}},
			},
			sortedDevices: []string{"g0", "g1"},
			expectedResult: &allocate.AllocationResult{
				AllocatedDevices: sets.NewString("g0", "g1").UnsortedList(),
				Success:          true,
			},
		},
		{
			name: "ratio 2/3 with affinity: need 3 but limit 1 per device -> success with partial allocation",
			ctx: &allocate.AllocationContext{
				AccompanyResourceName: "rdma",
				ResourceName:          "gpu",
				ResourceReq:           &v1alpha1.ResourceRequest{PodUid: "p", ContainerName: "c"},
				MachineState: func() state.AllocationResourcesMap {
					// 2 rdma, 3 gpu -> ratio = 2/3 (~0.666) -> need floor(2/0.666)=3
					return state.AllocationResourcesMap{
						gpuconsts.RDMADeviceType: {
							"r0": preallocState("p", "c"),
							"r1": preallocState("p", "c"),
						},
						gpuconsts.GPUDeviceType: {
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
				DeviceReq:          &v1alpha1.DeviceRequest{},
				GPUQRMPluginConfig: &qrm.GPUQRMPluginConfig{GPUDeviceNames: []string{"gpu"}, RDMADeviceNames: []string{"rdma"}},
			},
			sortedDevices: []string{"g0", "g1", "g2"},
			expectedResult: &allocate.AllocationResult{
				Success: true,
			},
			expectedAllocatedDeviceCandidates: []string{"g0", "g1", "g2"},
			expectedAllocatedDeviceCount:      2,
		},
		{
			name: "ratio 2/5 with affinity: need 5 but total per-device capacity 4 -> success with partial allocation",
			ctx: &allocate.AllocationContext{
				AccompanyResourceName: "rdma",
				ResourceName:          "gpu",
				ResourceReq:           &v1alpha1.ResourceRequest{PodUid: "p", ContainerName: "c"},
				MachineState: func() state.AllocationResourcesMap {
					// 2 rdma, 5 gpu -> ratio = 2/5 = 0.4 -> need floor(2/0.4)=5
					return state.AllocationResourcesMap{
						gpuconsts.RDMADeviceType: {
							"r0": preallocState("p", "c"),
							"r1": preallocState("p", "c"),
						},
						gpuconsts.GPUDeviceType: {
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
				DeviceReq:          &v1alpha1.DeviceRequest{},
				GPUQRMPluginConfig: &qrm.GPUQRMPluginConfig{GPUDeviceNames: []string{"gpu"}, RDMADeviceNames: []string{"rdma"}},
			},
			sortedDevices: []string{"g0", "g1", "g2", "g3", "g4"},
			expectedResult: &allocate.AllocationResult{
				Success: true,
			},
			expectedAllocatedDeviceCandidates: []string{"g0", "g1", "g2", "g3", "g4"},
			expectedAllocatedDeviceCount:      4,
		},
		{
			name: "fractional ratio returns error",
			ctx: &allocate.AllocationContext{
				AccompanyResourceName: "rdma",
				ResourceName:          "gpu",
				ResourceReq:           &v1alpha1.ResourceRequest{PodUid: "p", ContainerName: "c"},
				MachineState: func() state.AllocationResourcesMap {
					// 2 rdma devices, 3 gpu devices -> ratio = 2/3.
					// accompany allocated = 1
					// 1 / (2/3) = 1.5 -> fractional, returns error
					return state.AllocationResourcesMap{
						gpuconsts.RDMADeviceType: {
							"r0": preallocState("p", "c"), // 1 allocated
							"r1": nil,                     // total 2
						},
						gpuconsts.GPUDeviceType: {
							"g0": nil,
							"g1": nil,
							"g2": nil,
						},
					}
				}(),
				DeviceTopologyRegistry: buildMultiPriorityAffinityRegistry(map[string]map[int][]string{
					"r0": {0: {"g0"}},
					"r1": {0: {"g1", "g2"}},
				}),
				DeviceReq:          &v1alpha1.DeviceRequest{},
				GPUQRMPluginConfig: &qrm.GPUQRMPluginConfig{GPUDeviceNames: []string{"gpu"}, RDMADeviceNames: []string{"rdma"}},
			},
			sortedDevices: []string{"g0", "g1", "g2"},
			expectedErr:   true,
		},
		{
			name: "ratio 1/3 without affinity returns success with empty allocation",
			ctx: &allocate.AllocationContext{
				AccompanyResourceName: "rdma",
				ResourceName:          "gpu",
				ResourceReq:           &v1alpha1.ResourceRequest{PodUid: "p", ContainerName: "c"},
				MachineState: func() state.AllocationResourcesMap {
					return state.AllocationResourcesMap{
						gpuconsts.RDMADeviceType: {
							"r0": preallocState("p", "c"),
						},
						gpuconsts.GPUDeviceType: {
							"g0": nil,
							"g1": nil,
							"g2": nil,
						},
					}
				}(),
				DeviceTopologyRegistry: buildNoAffinityRegistry(),
				DeviceReq:              &v1alpha1.DeviceRequest{},
				GPUQRMPluginConfig:     &qrm.GPUQRMPluginConfig{GPUDeviceNames: []string{"gpu"}, RDMADeviceNames: []string{"rdma"}},
			},
			sortedDevices: []string{"g0", "g1", "g2"},
			expectedResult: &allocate.AllocationResult{
				AllocatedDevices: []string{},
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
						gpuconsts.RDMADeviceType: {
							"r0": preallocState("p", "c"),
						},
						gpuconsts.GPUDeviceType: {
							"g0": nil,
							"g1": nil,
							"g2": nil,
						},
					}
				}(),
				DeviceTopologyRegistry: buildMultiPriorityAffinityRegistry(map[string]map[int][]string{
					"r0": {0: {"g0", "g1", "g2"}},
				}),
				DeviceReq:          &v1alpha1.DeviceRequest{},
				GPUQRMPluginConfig: &qrm.GPUQRMPluginConfig{GPUDeviceNames: []string{"gpu"}, RDMADeviceNames: []string{"rdma"}},
			},
			sortedDevices: []string{"g0", "g1", "g2"},
			expectedResult: &allocate.AllocationResult{
				AllocatedDevices: sets.NewString("g0", "g1", "g2").UnsortedList(),
				Success:          true,
			},
		},
		{
			name: "numa affinity, ratio 1:1",
			ctx: &allocate.AllocationContext{
				AccompanyResourceName: "rdma",
				ResourceName:          "gpu",
				ResourceReq:           &v1alpha1.ResourceRequest{PodUid: "p", ContainerName: "c"},
				MachineState: func() state.AllocationResourcesMap {
					return state.AllocationResourcesMap{
						gpuconsts.RDMADeviceType: {
							"r0": preallocState("p", "c"),
							"r1": preallocState("p", "c"),
						},
						gpuconsts.GPUDeviceType: {
							"g0": nil,
							"g1": nil,
						},
					}
				}(),
				DeviceTopologyRegistry: buildNumaAffinityRegistry(
					map[string][]int{"r0": {0}, "r1": {1}},
					map[string][]int{"g0": {0}, "g1": {1}},
				),
				DeviceReq:          &v1alpha1.DeviceRequest{},
				GPUQRMPluginConfig: &qrm.GPUQRMPluginConfig{GPUDeviceNames: []string{"gpu"}, RDMADeviceNames: []string{"rdma"}},
			},
			sortedDevices: []string{"g0", "g1"},
			expectedResult: &allocate.AllocationResult{
				AllocatedDevices: sets.NewString("g0", "g1").UnsortedList(),
				Success:          true,
			},
		},
		{
			name: "numa affinity, ratio 2:1 (one gpu per two rdma)",
			ctx: &allocate.AllocationContext{
				AccompanyResourceName: "rdma",
				ResourceName:          "gpu",
				ResourceReq:           &v1alpha1.ResourceRequest{PodUid: "p", ContainerName: "c"},
				MachineState: func() state.AllocationResourcesMap {
					return state.AllocationResourcesMap{
						gpuconsts.RDMADeviceType: {
							"r0": preallocState("p", "c"),
							"r1": preallocState("p", "c"),
						},
						gpuconsts.GPUDeviceType: {
							"g0": nil,
						},
					}
				}(),
				DeviceTopologyRegistry: buildNumaAffinityRegistry(
					map[string][]int{"r0": {0}, "r1": {0}},
					map[string][]int{"g0": {0}},
				),
				DeviceReq:          &v1alpha1.DeviceRequest{},
				GPUQRMPluginConfig: &qrm.GPUQRMPluginConfig{GPUDeviceNames: []string{"gpu"}, RDMADeviceNames: []string{"rdma"}},
			},
			sortedDevices: []string{"g0"},
			expectedResult: &allocate.AllocationResult{
				AllocatedDevices: sets.NewString("g0").UnsortedList(),
				Success:          true,
			},
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			// Bind requires a mapping from resource name to device type in order to
			// find the corresponding resource name used in machine state.
			// In production this map is populated by the plugin; in unit tests we
			// construct it from the configured device names.
			if tc.ctx.DeviceNameToTypeMap == nil {
				tc.ctx.DeviceNameToTypeMap = make(map[string]string)
				if tc.ctx.GPUQRMPluginConfig != nil {
					for _, n := range tc.ctx.GPUQRMPluginConfig.GPUDeviceNames {
						tc.ctx.DeviceNameToTypeMap[n] = gpuconsts.GPUDeviceType
					}
					for _, n := range tc.ctx.GPUQRMPluginConfig.RDMADeviceNames {
						tc.ctx.DeviceNameToTypeMap[n] = gpuconsts.RDMADeviceType
					}
				}
			}

			if tc.ctx.Emitter == nil {
				tc.ctx.Emitter = metrics.DummyMetrics{}
			}

			s := NewAccompanyResourceStrategy()

			result, err := s.Bind(tc.ctx, tc.sortedDevices)

			if tc.expectedErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tc.expectedResult.Success, result.Success)
				if tc.expectedAllocatedDeviceCandidates != nil {
					assert.Len(t, result.AllocatedDevices, tc.expectedAllocatedDeviceCount)
					assert.Subset(t, tc.expectedAllocatedDeviceCandidates, result.AllocatedDevices)
				} else {
					assert.ElementsMatch(t, tc.expectedResult.AllocatedDevices, result.AllocatedDevices)
				}
			}
		})
	}
}

// buildSimpleAffinityRegistry builds a real DeviceTopologyRegistry where each rdma device ID
// has affinity to the listed gpu device IDs under the same priority level.
func buildSimpleAffinityRegistry(rdmaToGPU map[string][]string) *machine.DeviceTopologyRegistry {
	reg := machine.NewDeviceTopologyRegistry(metrics.DummyMetrics{})

	// Register topology providers for both devices
	reg.RegisterDeviceTopologyProvider("rdma", machine.NewDeviceTopologyProvider())
	reg.RegisterDeviceTopologyProvider("gpu", machine.NewDeviceTopologyProvider())

	// Construct RDMA topology
	rdmaTopo := &machine.DeviceTopology{PriorityDimensions: []string{"link"}, Devices: map[string]machine.DeviceInfo{}}
	for rdmaID := range rdmaToGPU {
		rdmaTopo.Devices[rdmaID] = machine.DeviceInfo{NumaNodes: []int{}, Dimensions: map[string]string{"link": rdmaID}}
	}
	_ = reg.SetDeviceTopology("rdma", rdmaTopo)

	// Construct GPU topology with matching affinity keys
	gpuTopo := &machine.DeviceTopology{PriorityDimensions: []string{"link"}, Devices: map[string]machine.DeviceInfo{}}
	// Include at least devices referenced in tests
	for rdmaID, gpus := range rdmaToGPU {
		for _, gid := range gpus {
			gpuTopo.Devices[gid] = machine.DeviceInfo{NumaNodes: []int{}, Dimensions: map[string]string{"link": rdmaID}}
		}
	}
	// Add a couple of extra GPUs to match sortedDevices lists when needed
	for _, extra := range []string{"2", "3"} {
		if _, ok := gpuTopo.Devices[extra]; !ok {
			gpuTopo.Devices[extra] = machine.DeviceInfo{NumaNodes: []int{}, Dimensions: map[string]string{"link": extra}}
		}
	}
	_ = reg.SetDeviceTopology("gpu", gpuTopo)

	return reg
}

// buildMultiPriorityAffinityRegistry builds a registry where each rdma device ID has multiple
// priority levels mapped to specific gpu device IDs. The generated topologies ensure that
// GetAffinityDevices returns a map with contiguous priority levels starting from 0.
func buildMultiPriorityAffinityRegistry(cfg map[string]map[int][]string) *machine.DeviceTopologyRegistry {
	reg := machine.NewDeviceTopologyRegistry(metrics.DummyMetrics{})

	reg.RegisterDeviceTopologyProvider("rdma", machine.NewDeviceTopologyProvider())
	reg.RegisterDeviceTopologyProvider("gpu", machine.NewDeviceTopologyProvider())

	// Collect all unique (p, rdmaID) pairs to build priority dimensions
	type priorityKey struct {
		p      int
		rdmaID string
	}
	var priorityKeys []priorityKey
	seen := make(map[priorityKey]bool)
	for rdmaID, prios := range cfg {
		for p := range prios {
			key := priorityKey{p: p, rdmaID: rdmaID}
			if !seen[key] {
				seen[key] = true
				priorityKeys = append(priorityKeys, key)
			}
		}
	}
	// Sort priority keys first by priority level, then by rdmaID to ensure consistent ordering
	for i := range priorityKeys {
		for j := i + 1; j < len(priorityKeys); j++ {
			if priorityKeys[i].p > priorityKeys[j].p || (priorityKeys[i].p == priorityKeys[j].p && priorityKeys[i].rdmaID > priorityKeys[j].rdmaID) {
				priorityKeys[i], priorityKeys[j] = priorityKeys[j], priorityKeys[i]
			}
		}
	}
	// Build priority dimensions list from sorted priority keys
	priorities := make([]string, len(priorityKeys))
	for i, key := range priorityKeys {
		priorities[i] = fmt.Sprintf("p-%d-%s", key.p, key.rdmaID)
	}

	// Build rdma topology
	rdmaTopo := &machine.DeviceTopology{PriorityDimensions: priorities, Devices: map[string]machine.DeviceInfo{}}
	for rdmaID, prios := range cfg {
		dims := map[string]string{}
		for p := range prios {
			dims[fmt.Sprintf("p-%d-%s", p, rdmaID)] = rdmaID
		}
		rdmaTopo.Devices[rdmaID] = machine.DeviceInfo{NumaNodes: []int{}, Dimensions: dims}
	}
	_ = reg.SetDeviceTopology("rdma", rdmaTopo)

	// Build gpu topology
	gpuTopo := &machine.DeviceTopology{PriorityDimensions: priorities, Devices: map[string]machine.DeviceInfo{}}
	for rdmaID, prios := range cfg {
		for p, gpus := range prios {
			for _, gid := range gpus {
				info := gpuTopo.Devices[gid]
				if info.Dimensions == nil {
					info.Dimensions = map[string]string{}
				}
				info.Dimensions[fmt.Sprintf("p-%d-%s", p, rdmaID)] = rdmaID
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
	reg := machine.NewDeviceTopologyRegistry(metrics.DummyMetrics{})

	reg.RegisterDeviceTopologyProvider("rdma", machine.NewDeviceTopologyProvider())
	reg.RegisterDeviceTopologyProvider("gpu", machine.NewDeviceTopologyProvider())

	// RDMA devices use dimension "rdma_link"
	rdmaTopo := &machine.DeviceTopology{PriorityDimensions: []string{"rdma_link"}, Devices: map[string]machine.DeviceInfo{}}
	for _, id := range []string{"r0", "r1"} {
		rdmaTopo.Devices[id] = machine.DeviceInfo{NumaNodes: []int{}, Dimensions: map[string]string{"rdma_link": id}}
	}
	_ = reg.SetDeviceTopology("rdma", rdmaTopo)

	// GPU devices use a different dimension "gpu_bus" so no affinity keys match RDMA
	gpuTopo := &machine.DeviceTopology{PriorityDimensions: []string{"gpu_bus"}, Devices: map[string]machine.DeviceInfo{}}
	for _, id := range []string{"g0", "g1", "g2"} {
		gpuTopo.Devices[id] = machine.DeviceInfo{NumaNodes: []int{}, Dimensions: map[string]string{"gpu_bus": id}}
	}
	_ = reg.SetDeviceTopology("gpu", gpuTopo)

	return reg
}

// buildNumaAffinityRegistry builds a registry where RDMA and GPU devices have affinity based on NUMA nodes
func buildNumaAffinityRegistry(rdmaToNuma map[string][]int, gpuToNuma map[string][]int) *machine.DeviceTopologyRegistry {
	reg := machine.NewDeviceTopologyRegistry(metrics.DummyMetrics{})

	reg.RegisterDeviceTopologyProvider("rdma", machine.NewDeviceTopologyProvider())
	reg.RegisterDeviceTopologyProvider("gpu", machine.NewDeviceTopologyProvider())

	// RDMA topology (no dimensions, only NUMA nodes and empty PriorityDimensions to trigger default "numa" priority)
	rdmaTopo := &machine.DeviceTopology{PriorityDimensions: []string{}, Devices: map[string]machine.DeviceInfo{}}
	for rdmaID, numaNodes := range rdmaToNuma {
		rdmaTopo.Devices[rdmaID] = machine.DeviceInfo{NumaNodes: numaNodes, Dimensions: map[string]string{}}
	}
	_ = reg.SetDeviceTopology("rdma", rdmaTopo)

	// GPU topology (no dimensions, only NUMA nodes)
	gpuTopo := &machine.DeviceTopology{PriorityDimensions: []string{}, Devices: map[string]machine.DeviceInfo{}}
	for gpuID, numaNodes := range gpuToNuma {
		gpuTopo.Devices[gpuID] = machine.DeviceInfo{NumaNodes: numaNodes, Dimensions: map[string]string{}}
	}
	_ = reg.SetDeviceTopology("gpu", gpuTopo)

	return reg
}

func buildSingleDimensionAffinityRegistry(
	dimension string,
	rdmaIDs, gpuIDs []string,
) *machine.DeviceTopologyRegistry {
	reg := machine.NewDeviceTopologyRegistry(metrics.DummyMetrics{})
	reg.RegisterDeviceTopologyProvider("rdma", machine.NewDeviceTopologyProvider())
	reg.RegisterDeviceTopologyProvider("gpu", machine.NewDeviceTopologyProvider())

	rdmaTopology := &machine.DeviceTopology{
		PriorityDimensions: []string{dimension},
		Devices:            map[string]machine.DeviceInfo{},
	}
	for _, id := range rdmaIDs {
		rdmaTopology.Devices[id] = machine.DeviceInfo{
			Dimensions: map[string]string{dimension: "group-0"},
		}
	}
	_ = reg.SetDeviceTopology("rdma", rdmaTopology)

	gpuTopology := &machine.DeviceTopology{
		PriorityDimensions: []string{dimension},
		Devices:            map[string]machine.DeviceInfo{},
	}
	for _, id := range gpuIDs {
		gpuTopology.Devices[id] = machine.DeviceInfo{
			Dimensions: map[string]string{dimension: "group-0"},
		}
	}
	_ = reg.SetDeviceTopology("gpu", gpuTopology)

	return reg
}

func buildClassicTopologyAllocation(
	t *testing.T,
	gpuCount, rdmaCount, gpuRequest int,
	podUID, containerName string,
) (*machine.DeviceTopologyRegistry, state.AllocationResourcesMap, []string) {
	t.Helper()

	assert.Zero(t, gpuCount%2)
	assert.Zero(t, rdmaCount%2)
	assert.Zero(t, gpuCount%rdmaCount)
	assert.LessOrEqual(t, gpuRequest, gpuCount)

	priorityDimensions := []string{"pcie", machine.DimensionNuma}
	gpuTopology := &machine.DeviceTopology{
		PriorityDimensions: append([]string(nil), priorityDimensions...),
		Devices:            make(map[string]machine.DeviceInfo, gpuCount),
	}
	rdmaTopology := &machine.DeviceTopology{
		PriorityDimensions: append([]string(nil), priorityDimensions...),
		Devices:            make(map[string]machine.DeviceInfo, rdmaCount),
	}
	machineState := state.AllocationResourcesMap{
		gpuconsts.GPUDeviceType:  make(state.AllocationMap, gpuCount),
		gpuconsts.RDMADeviceType: make(state.AllocationMap, rdmaCount),
	}
	gpusPerRDMA := gpuCount / rdmaCount
	gpusByPCIe := make(map[string]int, rdmaCount)
	gpusByNUMA := make(map[int]int, 2)
	rdmasByNUMA := make(map[int]int, 2)

	for i := 0; i < gpuCount; i++ {
		id := fmt.Sprintf("g%d", i)
		numa := i / (gpuCount / 2)
		pcie := fmt.Sprintf("pcie-%d", i/gpusPerRDMA)
		gpuTopology.Devices[id] = machine.DeviceInfo{
			NumaNodes: []int{numa},
			Dimensions: map[string]string{
				"pcie":                pcie,
				machine.DimensionNuma: fmt.Sprintf("numa-%d", numa),
			},
		}
		gpusByPCIe[pcie]++
		gpusByNUMA[numa]++
		if i < gpuRequest {
			machineState[gpuconsts.GPUDeviceType][id] = preallocState(podUID, containerName)
		} else {
			machineState[gpuconsts.GPUDeviceType][id] = nil
		}
	}

	for i := 0; i < rdmaCount; i++ {
		id := fmt.Sprintf("r%d", i)
		numa := i / (rdmaCount / 2)
		rdmaTopology.Devices[id] = machine.DeviceInfo{
			NumaNodes: []int{numa},
			Dimensions: map[string]string{
				"pcie":                fmt.Sprintf("pcie-%d", i),
				machine.DimensionNuma: fmt.Sprintf("numa-%d", numa),
			},
		}
		rdmasByNUMA[numa]++
		machineState[gpuconsts.RDMADeviceType][id] = nil
	}

	assert.Len(t, gpuTopology.Devices, gpuCount)
	assert.Len(t, rdmaTopology.Devices, rdmaCount)
	assert.Equal(t, priorityDimensions, gpuTopology.PriorityDimensions)
	assert.Equal(t, priorityDimensions, rdmaTopology.PriorityDimensions)
	assert.Equal(t, map[int]int{0: gpuCount / 2, 1: gpuCount / 2}, gpusByNUMA)
	assert.Equal(t, map[int]int{0: rdmaCount / 2, 1: rdmaCount / 2}, rdmasByNUMA)
	assert.Len(t, gpusByPCIe, rdmaCount)
	for _, count := range gpusByPCIe {
		assert.Equal(t, gpusPerRDMA, count)
	}

	registry := machine.NewDeviceTopologyRegistry(metrics.DummyMetrics{})
	registry.RegisterDeviceTopologyProvider("gpu", machine.NewDeviceTopologyProvider())
	registry.RegisterDeviceTopologyProvider("rdma", machine.NewDeviceTopologyProvider())
	assert.NoError(t, registry.SetDeviceTopology("gpu", gpuTopology))
	assert.NoError(t, registry.SetDeviceTopology("rdma", rdmaTopology))

	expectedRDMAs := sets.NewString()
	for i := 0; i < gpuRequest; i++ {
		expectedRDMAs.Insert(fmt.Sprintf("r%d", i/gpusPerRDMA))
	}
	return registry, machineState, expectedRDMAs.List()
}

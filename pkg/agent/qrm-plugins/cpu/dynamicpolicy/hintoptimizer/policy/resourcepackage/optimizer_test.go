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

package resourcepackage

import (
	"context"
	"testing"

	"github.com/smartystreets/goconvey/convey"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	nodev1alpha1 "github.com/kubewharf/katalyst-api/pkg/apis/node/v1alpha1"
	apiconsts "github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/hintoptimizer"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/hintoptimizer/policy"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/hintoptimizer/util"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
	pkgutil "github.com/kubewharf/katalyst-core/pkg/util/resource-package"
)

// MockState is a mock implementation of the state.State interface for testing purposes
type MockState struct {
	GetMachineStateFunc func() state.NUMANodeMap
}

func (m *MockState) GetMachineState() state.NUMANodeMap {
	if m.GetMachineStateFunc != nil {
		return m.GetMachineStateFunc()
	}
	return make(state.NUMANodeMap)
}

// Implement other required methods with empty implementations
func (m *MockState) GetNUMAHeadroom() map[int]float64 { return nil }
func (m *MockState) GetPodEntries() state.PodEntries  { return nil }
func (m *MockState) GetAllocationInfo(podUID string, containerName string) *state.AllocationInfo {
	return nil
}
func (m *MockState) GetAllowSharedCoresOverlapReclaimedCores() bool               { return false }
func (m *MockState) SetMachineState(numaNodeMap state.NUMANodeMap, persist bool)  {}
func (m *MockState) SetNUMAHeadroom(numaHeadroom map[int]float64, persist bool)   {}
func (m *MockState) SetPodEntries(podEntries state.PodEntries, writeThrough bool) {}
func (m *MockState) SetAllocationInfo(podUID string, containerName string, allocationInfo *state.AllocationInfo, persist bool) {
}

func (m *MockState) SetAllowSharedCoresOverlapReclaimedCores(allowSharedCoresOverlapReclaimedCores, persist bool) {
}
func (m *MockState) Delete(podUID string, containerName string, persist bool) {}
func (m *MockState) ClearState()                                              {}
func (m *MockState) StoreState() error                                        { return nil }

type resourcePackageManagerStub struct {
	nodeResourcePackagesMap pkgutil.NUMAResourcePackageItems
	err                     error
}

func (s *resourcePackageManagerStub) ConvertNPDResourcePackages(_ *nodev1alpha1.NodeProfileDescriptor) (pkgutil.NUMAResourcePackageItems, error) {
	return s.nodeResourcePackagesMap, nil
}

func (s *resourcePackageManagerStub) NodeResourcePackages(_ context.Context) (pkgutil.NUMAResourcePackageItems, error) {
	return s.nodeResourcePackagesMap, s.err
}

func TestNewResourcePackageHintOptimizer(t *testing.T) {
	t.Parallel()

	convey.Convey("Test NewResourcePackageHintOptimizer", t, func() {
		options := policy.HintOptimizerFactoryOptions{
			Conf:                   &config.Configuration{},
			MetaServer:             &metaserver.MetaServer{},
			ResourcePackageManager: &resourcePackageManagerStub{},
			Emitter:                metrics.DummyMetrics{},
			State:                  &MockState{},
		}

		optimizer, err := NewResourcePackageHintOptimizer(options)
		convey.So(err, convey.ShouldBeNil)
		convey.So(optimizer, convey.ShouldNotBeNil)
	})
}

func TestResourcePackageHintOptimizer_OptimizeHints(t *testing.T) {
	t.Parallel()

	convey.Convey("Test OptimizeHints", t, func() {
		convey.Convey("when request is nil", func() {
			optimizer := &resourcePackageHintOptimizer{
				rpm: &resourcePackageManagerStub{},
			}
			err := optimizer.OptimizeHints(hintoptimizer.Request{}, &pluginapi.ListOfTopologyHints{})
			convey.So(err, convey.ShouldNotBeNil)
			convey.So(err.Error(), convey.ShouldContainSubstring, "OptimizeHints got nil req")
		})

		convey.Convey("when hints is nil", func() {
			optimizer := &resourcePackageHintOptimizer{
				rpm: &resourcePackageManagerStub{},
			}
			err := optimizer.OptimizeHints(hintoptimizer.Request{
				ResourceRequest: &pluginapi.ResourceRequest{},
			}, nil)
			convey.So(err, convey.ShouldNotBeNil)
			convey.So(err.Error(), convey.ShouldContainSubstring, "hints cannot be nil")
		})

		convey.Convey("when pod is NUMA exclusive", func() {
			optimizer := &resourcePackageHintOptimizer{}
			err := optimizer.OptimizeHints(hintoptimizer.Request{
				ResourceRequest: &pluginapi.ResourceRequest{
					Annotations: map[string]string{
						apiconsts.PodAnnotationMemoryEnhancementNumaBinding:   "true",
						apiconsts.PodAnnotationMemoryEnhancementNumaExclusive: "true",
					},
				},
			}, &pluginapi.ListOfTopologyHints{})
			convey.So(err, convey.ShouldEqual, util.ErrHintOptimizerSkip)
		})

		convey.Convey("when resource package not found in annotation and no pinned rp", func() {
			optimizer := &resourcePackageHintOptimizer{
				state: &MockState{
					GetMachineStateFunc: func() state.NUMANodeMap {
						return state.NUMANodeMap{}
					},
				},
			}
			err := optimizer.OptimizeHints(hintoptimizer.Request{
				ResourceRequest: &pluginapi.ResourceRequest{
					Annotations: map[string]string{
						apiconsts.PodAnnotationMemoryEnhancementNumaBinding:   "true",
						apiconsts.PodAnnotationMemoryEnhancementNumaExclusive: "false",
					},
				},
			}, &pluginapi.ListOfTopologyHints{})
			convey.So(err, convey.ShouldEqual, util.ErrHintOptimizerSkip)
		})

		convey.Convey("success populate hints", func() {
			mockState := &MockState{
				GetMachineStateFunc: func() state.NUMANodeMap {
					return state.NUMANodeMap{
						0: &state.NUMANodeState{
							PodEntries: state.PodEntries{
								"pod-1": state.ContainerEntries{
									"container-1": nil,
								},
							},
						},
					}
				},
			}

			mockRPM := &resourcePackageManagerStub{
				nodeResourcePackagesMap: pkgutil.NUMAResourcePackageItems{
					0: {
						"test-package": {
							ResourcePackage: nodev1alpha1.ResourcePackage{
								PackageName: "test-package",
								Allocatable: &v1.ResourceList{
									"cpu": resource.MustParse("2"),
								},
							},
						},
					},
					1: {
						"test-package-1": {
							ResourcePackage: nodev1alpha1.ResourcePackage{
								PackageName: "test-package-1",
								Allocatable: &v1.ResourceList{
									"cpu": resource.MustParse("2"),
								},
							},
						},
					},
				},
			}

			optimizer := &resourcePackageHintOptimizer{
				rpm:   mockRPM,
				state: mockState,
			}

			hints := &pluginapi.ListOfTopologyHints{
				Hints: []*pluginapi.TopologyHint{
					{
						Nodes: []uint64{0},
					},
					{
						Nodes: []uint64{1},
					},
				},
			}
			err := optimizer.OptimizeHints(hintoptimizer.Request{
				ResourceRequest: &pluginapi.ResourceRequest{
					Annotations: map[string]string{
						apiconsts.PodAnnotationMemoryEnhancementNumaBinding:   "true",
						apiconsts.PodAnnotationMemoryEnhancementNumaExclusive: "false",
						apiconsts.PodAnnotationResourcePackageKey:             "test-package",
					},
				},
				CPURequest: 1,
			}, hints)
			convey.So(err, convey.ShouldBeNil)
			convey.So(len(hints.Hints), convey.ShouldEqual, 1)
			convey.So(hints.Hints[0].Nodes[0], convey.ShouldEqual, uint64(0))
		})
	})
}

func TestResourcePackageHintOptimizer_calculateNodeCPUMetrics(t *testing.T) {
	t.Parallel()

	convey.Convey("Test calculateNodeCPUMetrics", t, func() {
		optimizer := &resourcePackageHintOptimizer{
			reservedCPUs: machine.NewCPUSet(),
		}

		convey.Convey("ns is nil", func() {
			allocatedForRP, unpinnedAvailable := optimizer.calculateNodeCPUMetrics(nil, 0, "test-rp", true, nil)
			convey.So(allocatedForRP, convey.ShouldEqual, 0)
			convey.So(unpinnedAvailable, convey.ShouldEqual, 0)
		})

		convey.Convey("calculate metrics properly", func() {
			ns := &state.NUMANodeState{
				DefaultCPUSet:   machine.NewCPUSet(4, 5),
				AllocatedCPUSet: machine.NewCPUSet(0, 1, 2, 3),
				PodEntries: state.PodEntries{
					"pod-pool": state.ContainerEntries{
						"": &state.AllocationInfo{
							AllocationMeta: commonstate.AllocationMeta{
								OwnerPoolName: "pool",
							},
						},
					},
					"pod-pinned": state.ContainerEntries{
						"container-pinned": &state.AllocationInfo{
							AllocationMeta: commonstate.AllocationMeta{
								PodUid:        "pod-pinned",
								ContainerName: "container-pinned",
								ContainerType: pluginapi.ContainerType_MAIN.String(),
								Annotations: map[string]string{
									apiconsts.PodAnnotationResourcePackageKey: "pinned-rp",
								},
							},
							RequestQuantity:  2.0,
							AllocationResult: machine.NewCPUSet(0, 1),
						},
					},
					"pod-unpinned-dedicated": state.ContainerEntries{
						"container-unpinned-dedicated": &state.AllocationInfo{
							AllocationMeta: commonstate.AllocationMeta{
								PodUid:        "pod-unpinned-dedicated",
								ContainerName: "container-unpinned-dedicated",
								ContainerType: pluginapi.ContainerType_MAIN.String(),
								QoSLevel:      apiconsts.PodAnnotationQoSLevelDedicatedCores,
							},
							RequestQuantity:  2.0,                     // Cross-NUMA request 2.0 total
							AllocationResult: machine.NewCPUSet(3, 4), // Cross-NUMA: CPU 3 on Node 0, CPU 4 on Node 1
							OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
								0: machine.NewCPUSet(3),
								1: machine.NewCPUSet(4),
							},
						},
					},
					"pod-unpinned-shared": state.ContainerEntries{
						"container-unpinned": &state.AllocationInfo{
							AllocationMeta: commonstate.AllocationMeta{
								PodUid:        "pod-unpinned",
								ContainerName: "container-unpinned",
								ContainerType: pluginapi.ContainerType_MAIN.String(),
								QoSLevel:      apiconsts.PodAnnotationQoSLevelSharedCores,
								Annotations: map[string]string{
									apiconsts.PodAnnotationMemoryEnhancementNumaBinding: apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable,
									apiconsts.PodAnnotationResourcePackageKey:           "target-rp",
								},
							},
							RequestQuantity:  1.0,
							AllocationResult: machine.NewCPUSet(2),
						},
					},
				},
			}

			numaRPPinnedCPUSet := map[int]map[string]machine.CPUSet{
				0: {"pinned-rp": machine.NewCPUSet(0, 1)},
			}

			allocatedForRP, unpinnedAvailable := optimizer.calculateNodeCPUMetrics(ns, 0, "target-rp", true, numaRPPinnedCPUSet)

			// target-rp allocated is 1.0 (from pod-unpinned-shared)
			convey.So(allocatedForRP, convey.ShouldEqual, 1.0)

			// Node 0: Union(Default(4,5), Allocated(0,1,2,3))(6) - pinned(2) = 4 unpinned allocatable.
			// unpinnedPreciseAllocated = pod-unpinned-shared(1.0) + pod-unpinned-dedicated(1.0 since CPU 3) = 2.0
			// unpinnedAvailable = Max(4.0 - 2.0, 0.0) = 2.0.
			convey.So(unpinnedAvailable, convey.ShouldEqual, 2.0)
		})

		convey.Convey("calculate metrics properly with realistic disjoint cpu sets", func() {
			ns := &state.NUMANodeState{
				DefaultCPUSet:   machine.NewCPUSet(3, 4, 5),
				AllocatedCPUSet: machine.NewCPUSet(0, 1, 2),
				PodEntries: state.PodEntries{
					"pod-pinned": state.ContainerEntries{
						"container-pinned": &state.AllocationInfo{
							AllocationMeta: commonstate.AllocationMeta{
								PodUid:        "pod-pinned",
								ContainerName: "container-pinned",
								ContainerType: pluginapi.ContainerType_MAIN.String(),
								Annotations: map[string]string{
									apiconsts.PodAnnotationResourcePackageKey: "pinned-rp",
								},
							},
							RequestQuantity:  2.0,
							AllocationResult: machine.NewCPUSet(0, 1),
						},
					},
					"pod-unpinned-shared": state.ContainerEntries{
						"container-unpinned": &state.AllocationInfo{
							AllocationMeta: commonstate.AllocationMeta{
								PodUid:        "pod-unpinned",
								ContainerName: "container-unpinned",
								ContainerType: pluginapi.ContainerType_MAIN.String(),
								QoSLevel:      apiconsts.PodAnnotationQoSLevelSharedCores,
								Annotations: map[string]string{
									apiconsts.PodAnnotationMemoryEnhancementNumaBinding: apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable,
									apiconsts.PodAnnotationResourcePackageKey:           "target-rp",
								},
							},
							RequestQuantity:  1.0,
							AllocationResult: machine.NewCPUSet(2),
						},
					},
				},
			}

			numaRPPinnedCPUSet := map[int]map[string]machine.CPUSet{
				0: {"pinned-rp": machine.NewCPUSet(0, 1)},
			}

			allocatedForRP, unpinnedAvailable := optimizer.calculateNodeCPUMetrics(ns, 0, "target-rp", true, numaRPPinnedCPUSet)

			// target-rp allocated is 1.0 (from pod-unpinned-shared)
			convey.So(allocatedForRP, convey.ShouldEqual, 1.0)

			// Node 0: Union(Default(3,4,5), Allocated(0,1,2))(6) - pinned(2) = 4 unpinned allocatable.
			// unpinnedPreciseAllocated = pod-unpinned-shared(1.0) = 1.0
			// unpinnedAvailable = Max(4.0 - 1.0, 0.0) = 3.0.
			convey.So(unpinnedAvailable, convey.ShouldEqual, 3.0)
		})
	})
}

func TestResourcePackageHintOptimizer_fetchResourcePackageAllocatable(t *testing.T) {
	t.Parallel()

	convey.Convey("Test fetchResourcePackageAllocatable", t, func() {
		convey.Convey("when resourcePackageMap is nil", func() {
			mockRPM := &resourcePackageManagerStub{}

			optimizer := &resourcePackageHintOptimizer{
				rpm: mockRPM,
			}
			result, err := optimizer.fetchResourcePackageAllocatable("test-package")
			convey.So(result, convey.ShouldBeNil)
			convey.So(err, convey.ShouldNotBeNil)
			convey.So(err.Error(), convey.ShouldContainSubstring, "resourcePackageMap is nil")
		})

		convey.Convey("success case", func() {
			mockRPM := &resourcePackageManagerStub{
				nodeResourcePackagesMap: pkgutil.NUMAResourcePackageItems{
					0: {
						"test-package": {
							ResourcePackage: nodev1alpha1.ResourcePackage{
								PackageName: "test-package",
								Allocatable: &v1.ResourceList{
									"cpu": resource.MustParse("2"),
								},
							},
						},
						"test-package-1": {
							ResourcePackage: nodev1alpha1.ResourcePackage{
								PackageName: "test-package-1",
								Allocatable: &v1.ResourceList{
									"cpu": resource.MustParse("2"),
								},
							},
						},
					},
				},
			}

			optimizer := &resourcePackageHintOptimizer{
				rpm: mockRPM,
			}
			result, err := optimizer.fetchResourcePackageAllocatable("test-package")
			convey.So(err, convey.ShouldBeNil)
			convey.So(result, convey.ShouldNotBeNil)
			convey.So(result[0], convey.ShouldEqual, 2.0)
		})

		convey.Convey("when allocatable or cpu is nil", func() {
			mockRPM := &resourcePackageManagerStub{
				nodeResourcePackagesMap: pkgutil.NUMAResourcePackageItems{
					0: {
						"test-package": {
							ResourcePackage: nodev1alpha1.ResourcePackage{
								PackageName: "test-package",
								Allocatable: nil,
							},
						},
					},
					1: {
						"test-package": {
							ResourcePackage: nodev1alpha1.ResourcePackage{
								PackageName: "test-package",
								Allocatable: &v1.ResourceList{
									"memory": resource.MustParse("1Gi"),
								},
							},
						},
					},
				},
			}

			optimizer := &resourcePackageHintOptimizer{
				rpm: mockRPM,
			}
			result, err := optimizer.fetchResourcePackageAllocatable("test-package")
			convey.So(err, convey.ShouldBeNil)
			convey.So(len(result), convey.ShouldEqual, 0)
		})
	})
}

func TestResourcePackageHintOptimizer_Run(t *testing.T) {
	t.Parallel()

	convey.Convey("Test Run", t, func() {
		optimizer := &resourcePackageHintOptimizer{}
		stopCh := make(chan struct{})
		close(stopCh)
		optimizer.Run(stopCh)
	})
}

func TestResourcePackageHintOptimizer_populateHintsByResourcePackage(t *testing.T) {
	t.Parallel()

	convey.Convey("Test populateHintsByResourcePackage", t, func() {
		optimizer := &resourcePackageHintOptimizer{}

		testCases := []struct {
			name            string
			hints           *pluginapi.ListOfTopologyHints
			cpuRequest      float64
			resourcePackage string
			allocatableMap  map[int]float64
			allocatedMap    map[int]float64
			unpinnedMap     map[int]float64
			numaRPPinned    map[int]map[string]machine.CPUSet
			expectedHints   []*pluginapi.TopologyHint
			expectedError   error
		}{
			{
				name: "empty hints",
				hints: &pluginapi.ListOfTopologyHints{
					Hints: []*pluginapi.TopologyHint{},
				},
				cpuRequest:      1.0,
				resourcePackage: "test-package",
				allocatableMap:  map[int]float64{0: 2.0},
				allocatedMap:    map[int]float64{0: 0.5},
				expectedHints:   []*pluginapi.TopologyHint{},
			},
			{
				name: "hints with multi-node",
				hints: &pluginapi.ListOfTopologyHints{
					Hints: []*pluginapi.TopologyHint{
						{Nodes: []uint64{0, 1}},
					},
				},
				cpuRequest:      1.0,
				resourcePackage: "test-package",
				allocatableMap:  map[int]float64{0: 2.0},
				allocatedMap:    map[int]float64{0: 0.5},
				expectedHints:   []*pluginapi.TopologyHint{}, // Multi-node hints should be filtered out
			},
			{
				name: "hints with single node - sufficient resources",
				hints: &pluginapi.ListOfTopologyHints{
					Hints: []*pluginapi.TopologyHint{
						{Nodes: []uint64{0}},
						{Nodes: []uint64{1}},
					},
				},
				cpuRequest:      1.0,
				resourcePackage: "test-package",
				allocatableMap:  map[int]float64{0: 2.0, 1: 2.0},
				allocatedMap:    map[int]float64{0: 0.5, 1: 0.5},
				expectedHints: []*pluginapi.TopologyHint{
					{Nodes: []uint64{0}},
					{Nodes: []uint64{1}},
				},
			},
			{
				name: "hints with single node - insufficient resources",
				hints: &pluginapi.ListOfTopologyHints{
					Hints: []*pluginapi.TopologyHint{
						{Nodes: []uint64{0}},
						{Nodes: []uint64{1}},
					},
				},
				cpuRequest:      2.0,
				resourcePackage: "test-package",
				allocatableMap:  map[int]float64{0: 2.0, 1: 2.0},
				allocatedMap:    map[int]float64{0: 1.5, 1: 1.5},
				expectedHints:   []*pluginapi.TopologyHint{}, // No node has sufficient resources
			},
			{
				name: "no resource package, unpinned sufficient",
				hints: &pluginapi.ListOfTopologyHints{
					Hints: []*pluginapi.TopologyHint{
						{Nodes: []uint64{0}},
						{Nodes: []uint64{1}},
					},
				},
				cpuRequest:      2.0,
				resourcePackage: "",
				unpinnedMap:     map[int]float64{0: 2.0, 1: 1.0},
				expectedHints:   []*pluginapi.TopologyHint{{Nodes: []uint64{0}}}, // Node 1 is insufficient
			},
			{
				name: "resource package unpinned but sufficient",
				hints: &pluginapi.ListOfTopologyHints{
					Hints: []*pluginapi.TopologyHint{
						{Nodes: []uint64{0}},
					},
				},
				cpuRequest:      1.0,
				resourcePackage: "unpinned-rp",
				allocatableMap:  map[int]float64{0: 2.0},
				allocatedMap:    map[int]float64{0: 0.5},
				unpinnedMap:     map[int]float64{0: 1.0},
				numaRPPinned:    map[int]map[string]machine.CPUSet{0: {"other": machine.NewCPUSet(1, 2)}},
				expectedHints:   []*pluginapi.TopologyHint{{Nodes: []uint64{0}}},
			},
			{
				name: "resource package unpinned and insufficient",
				hints: &pluginapi.ListOfTopologyHints{
					Hints: []*pluginapi.TopologyHint{
						{Nodes: []uint64{0}},
					},
				},
				cpuRequest:      1.0,
				resourcePackage: "unpinned-rp",
				allocatableMap:  map[int]float64{0: 2.0},
				allocatedMap:    map[int]float64{0: 0.5},
				unpinnedMap:     map[int]float64{0: 0.5}, // Less than cpuRequest
				numaRPPinned:    map[int]map[string]machine.CPUSet{0: {"other": machine.NewCPUSet(1, 2)}},
				expectedHints:   []*pluginapi.TopologyHint{},
			},
		}

		for _, tc := range testCases {
			convey.Convey(tc.name, func() {
				err := optimizer.populateHintsByResourcePackage(tc.hints, tc.cpuRequest, tc.resourcePackage, tc.allocatableMap, tc.allocatedMap, tc.unpinnedMap, tc.numaRPPinned)
				convey.So(err, convey.ShouldEqual, tc.expectedError)
				convey.So(len(tc.hints.Hints), convey.ShouldEqual, len(tc.expectedHints))
				for i, hint := range tc.hints.Hints {
					if i < len(tc.expectedHints) {
						convey.So(hint.Nodes, convey.ShouldResemble, tc.expectedHints[i].Nodes)
					}
				}
			})
		}
	})
}

func TestResourcePackageHintOptimizer_calculateCPUMetrics(t *testing.T) {
	t.Parallel()

	convey.Convey("Test calculateCPUMetrics", t, func() {
		mockState := &MockState{
			GetMachineStateFunc: func() state.NUMANodeMap {
				return state.NUMANodeMap{
					0: &state.NUMANodeState{
						DefaultCPUSet:   machine.NewCPUSet(3),
						AllocatedCPUSet: machine.NewCPUSet(0, 1, 2),
						ResourcePackageStates: map[string]*state.ResourcePackageState{
							"pinned-rp": {
								PinnedCPUSet: machine.NewCPUSet(0, 1),
							},
						},
						PodEntries: state.PodEntries{
							"pod-pinned": state.ContainerEntries{
								"container-pinned": &state.AllocationInfo{
									AllocationMeta: commonstate.AllocationMeta{
										PodUid:        "pod-pinned",
										ContainerName: "container-pinned",
										ContainerType: pluginapi.ContainerType_MAIN.String(),
										Annotations: map[string]string{
											apiconsts.PodAnnotationResourcePackageKey: "pinned-rp",
										},
									},
									RequestQuantity:  2.0,
									AllocationResult: machine.NewCPUSet(0, 1),
								},
							},
							"pod-unpinned": state.ContainerEntries{
								"container-unpinned": &state.AllocationInfo{
									AllocationMeta: commonstate.AllocationMeta{
										PodUid:        "pod-unpinned",
										ContainerName: "container-unpinned",
										ContainerType: pluginapi.ContainerType_MAIN.String(),
										QoSLevel:      apiconsts.PodAnnotationQoSLevelSharedCores,
										Annotations: map[string]string{
											apiconsts.PodAnnotationMemoryEnhancementNumaBinding: apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable,
										},
									},
									RequestQuantity:  1.0,
									AllocationResult: machine.NewCPUSet(2), // unpinned allocated
								},
							},
						},
					},
					1: &state.NUMANodeState{
						DefaultCPUSet:   machine.NewCPUSet(4, 5, 6, 7),
						AllocatedCPUSet: machine.NewCPUSet(),
					},
				}
			},
		}

		mockRPM := &resourcePackageManagerStub{
			nodeResourcePackagesMap: pkgutil.NUMAResourcePackageItems{
				0: {
					"test-package": {
						ResourcePackage: nodev1alpha1.ResourcePackage{
							PackageName: "test-package",
							Allocatable: &v1.ResourceList{
								"cpu": resource.MustParse("2"),
							},
						},
					},
				},
				1: {
					"test-package": {
						ResourcePackage: nodev1alpha1.ResourcePackage{
							PackageName: "test-package",
							Allocatable: &v1.ResourceList{
								"cpu": resource.MustParse("2"),
							},
						},
					},
				},
			},
		}

		optimizer := &resourcePackageHintOptimizer{
			state: mockState,
			rpm:   mockRPM,
		}

		convey.Convey("when resourcePackageMap is nil", func() {
			optimizerWithNilRPM := &resourcePackageHintOptimizer{
				state: mockState,
				rpm:   &resourcePackageManagerStub{},
			}
			allocatableMap, allocatedMap, unpinnedMap, err := optimizerWithNilRPM.calculateCPUMetrics("test-package", true, mockState.GetMachineState(), map[int]map[string]machine.CPUSet{0: {"pinned-rp": machine.NewCPUSet(0, 1)}})
			convey.So(allocatableMap, convey.ShouldBeNil)
			convey.So(allocatedMap, convey.ShouldBeNil)
			convey.So(unpinnedMap, convey.ShouldBeNil)
			convey.So(err, convey.ShouldNotBeNil)
			convey.So(err.Error(), convey.ShouldContainSubstring, "resourcePackageMap is nil")
		})

		convey.Convey("success case for allocatable, allocated, and unpinned", func() {
			allocatableMap, allocatedMap, unpinnedMap, err := optimizer.calculateCPUMetrics(
				"test-package",
				true,
				mockState.GetMachineState(),
				map[int]map[string]machine.CPUSet{0: {"pinned-rp": machine.NewCPUSet(0, 1)}},
			)

			convey.So(err, convey.ShouldBeNil)

			// Allocatable assertions
			convey.So(allocatableMap, convey.ShouldNotBeNil)
			convey.So(allocatableMap[0], convey.ShouldEqual, 2.0)
			convey.So(allocatableMap[1], convey.ShouldEqual, 2.0)

			// Allocated assertions (no test-package pods in state)
			convey.So(allocatedMap, convey.ShouldNotBeNil)
			convey.So(allocatedMap[0], convey.ShouldEqual, 0.0)
			convey.So(allocatedMap[1], convey.ShouldEqual, 0.0)

			// Unpinned available assertions
			// Node 0: Union(Default, Allocated)(4) - pinned(2) = 2 unpinned allocatable.
			// Allocated: 1 unpinned shared_cores.
			// Remaining = 2 - 1 = 1.
			convey.So(unpinnedMap[0], convey.ShouldEqual, 1.0)

			// Node 1: Union(Default, Allocated)(4) - pinned(0) = 4 unpinned allocatable.
			// Allocated: 0.
			// Remaining = 4.
			convey.So(unpinnedMap[1], convey.ShouldEqual, 4.0)
		})
	})
}

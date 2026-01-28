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
	pkgutil "github.com/kubewharf/katalyst-core/pkg/util/resource-package"
	resourcepackage "github.com/kubewharf/katalyst-core/pkg/util/resource-package"
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
	nodeResourcePackagesMap map[int][]pkgutil.ResourcePackageItem
	err                     error
}

func (s *resourcePackageManagerStub) ConvertNPDResourcePackages(_ *nodev1alpha1.NodeProfileDescriptor) (map[int][]pkgutil.ResourcePackageItem, error) {
	return s.nodeResourcePackagesMap, nil
}

func (s *resourcePackageManagerStub) NodeResourcePackages(_ context.Context) (map[int][]pkgutil.ResourcePackageItem, error) {
	return s.nodeResourcePackagesMap, s.err
}

func TestNewResourcePackageHintOptimizer(t *testing.T) {
	t.Parallel()

	convey.Convey("Test NewResourcePackageHintOptimizer", t, func() {
		options := policy.HintOptimizerFactoryOptions{
			Conf: &config.Configuration{},
			MetaServer: &metaserver.MetaServer{
				ResourcePackageManager: &resourcePackageManagerStub{},
			},
			Emitter: metrics.DummyMetrics{},
			State:   &MockState{},
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
				metaServer: &metaserver.MetaServer{
					ResourcePackageManager: &resourcePackageManagerStub{},
				},
			}
			err := optimizer.OptimizeHints(hintoptimizer.Request{}, &pluginapi.ListOfTopologyHints{})
			convey.So(err, convey.ShouldNotBeNil)
			convey.So(err.Error(), convey.ShouldContainSubstring, "OptimizeHints got nil req")
		})

		convey.Convey("when hints is nil", func() {
			optimizer := &resourcePackageHintOptimizer{
				metaServer: &metaserver.MetaServer{
					ResourcePackageManager: &resourcePackageManagerStub{},
				},
			}
			err := optimizer.OptimizeHints(hintoptimizer.Request{
				ResourceRequest: &pluginapi.ResourceRequest{},
			}, nil)
			convey.So(err, convey.ShouldNotBeNil)
			convey.So(err.Error(), convey.ShouldContainSubstring, "hints cannot be nil")
		})

		convey.Convey("when pod is NUMA exclusive", func() {
			optimizer := &resourcePackageHintOptimizer{
				metaServer: &metaserver.MetaServer{},
			}
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

		convey.Convey("when resource package not found in annotation", func() {
			optimizer := &resourcePackageHintOptimizer{
				metaServer: &metaserver.MetaServer{},
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

			optimizer := &resourcePackageHintOptimizer{
				metaServer: &metaserver.MetaServer{},
				state:      mockState,
				resourcePackageMap: map[int][]pkgutil.ResourcePackageItem{
					0: {
						{
							ResourcePackage: nodev1alpha1.ResourcePackage{
								PackageName: "test-package",
								Allocatable: &v1.ResourceList{
									"cpu": resource.MustParse("2"),
								},
							},
						},
					},
					1: {
						{
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

func TestResourcePackageHintOptimizer_Run(t *testing.T) {
	t.Parallel()

	convey.Convey("Test Run", t, func() {
		optimizer := &resourcePackageHintOptimizer{
			metaServer: &metaserver.MetaServer{},
		}
		stopCh := make(chan struct{})
		close(stopCh)
		optimizer.Run(stopCh)
	})
}

func TestResourcePackageHintOptimizer_populateHintsByResourcePackage(t *testing.T) {
	t.Parallel()

	convey.Convey("Test populateHintsByResourcePackage", t, func() {
		optimizer := &resourcePackageHintOptimizer{
			metaServer: &metaserver.MetaServer{},
		}

		testCases := []struct {
			name           string
			hints          *pluginapi.ListOfTopologyHints
			cpuRequest     float64
			allocatableMap map[int]float64
			allocatedMap   map[int]float64
			expectedHints  []*pluginapi.TopologyHint
			expectedError  error
		}{
			{
				name: "empty hints",
				hints: &pluginapi.ListOfTopologyHints{
					Hints: []*pluginapi.TopologyHint{},
				},
				cpuRequest:     1.0,
				allocatableMap: map[int]float64{0: 2.0},
				allocatedMap:   map[int]float64{0: 0.5},
				expectedHints:  []*pluginapi.TopologyHint{},
			},
			{
				name: "hints with multi-node",
				hints: &pluginapi.ListOfTopologyHints{
					Hints: []*pluginapi.TopologyHint{
						{Nodes: []uint64{0, 1}},
					},
				},
				cpuRequest:     1.0,
				allocatableMap: map[int]float64{0: 2.0},
				allocatedMap:   map[int]float64{0: 0.5},
				expectedHints:  []*pluginapi.TopologyHint{}, // Multi-node hints should be filtered out
			},
			{
				name: "hints with single node - sufficient resources",
				hints: &pluginapi.ListOfTopologyHints{
					Hints: []*pluginapi.TopologyHint{
						{Nodes: []uint64{0}},
						{Nodes: []uint64{1}},
					},
				},
				cpuRequest:     1.0,
				allocatableMap: map[int]float64{0: 2.0, 1: 2.0},
				allocatedMap:   map[int]float64{0: 0.5, 1: 0.5},
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
				cpuRequest:     2.0,
				allocatableMap: map[int]float64{0: 2.0, 1: 2.0},
				allocatedMap:   map[int]float64{0: 1.5, 1: 1.5},
				expectedHints:  []*pluginapi.TopologyHint{}, // No node has sufficient resources
			},
		}

		for _, tc := range testCases {
			convey.Convey(tc.name, func() {
				err := optimizer.populateHintsByResourcePackage(tc.hints, tc.cpuRequest, tc.allocatableMap, tc.allocatedMap)
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

func TestResourcePackageHintOptimizer_getResourcePackageAllocatable(t *testing.T) {
	t.Parallel()

	convey.Convey("Test getResourcePackageAllocatable", t, func() {
		convey.Convey("when resourcePackageMap is nil", func() {
			mockMetaServer := &metaserver.MetaServer{
				ResourcePackageManager: &resourcePackageManagerStub{},
			}

			optimizer := &resourcePackageHintOptimizer{
				metaServer: mockMetaServer,
			}
			result, err := optimizer.getResourcePackageAllocatable("test-package")
			convey.So(result, convey.ShouldBeNil)
			convey.So(err, convey.ShouldNotBeNil)
			convey.So(err.Error(), convey.ShouldContainSubstring, "resourcePackageMap is nil")
		})

		convey.Convey("success case", func() {
			mockMetaServer := &metaserver.MetaServer{}

			optimizer := &resourcePackageHintOptimizer{
				metaServer: mockMetaServer,
				resourcePackageMap: map[int][]pkgutil.ResourcePackageItem{
					0: {
						{
							ResourcePackage: nodev1alpha1.ResourcePackage{
								PackageName: "test-package",
								Allocatable: &v1.ResourceList{
									"cpu": resource.MustParse("2"),
								},
							},
						},
						{
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
			result, err := optimizer.getResourcePackageAllocatable("test-package")
			convey.So(err, convey.ShouldBeNil)
			convey.So(result, convey.ShouldNotBeNil)
			convey.So(result[0], convey.ShouldEqual, 2.0)
		})

		convey.Convey("when allocatable or cpu is nil", func() {
			mockMetaServer := &metaserver.MetaServer{}

			optimizer := &resourcePackageHintOptimizer{
				metaServer: mockMetaServer,
				resourcePackageMap: map[int][]resourcepackage.ResourcePackageItem{
					0: {
						{
							ResourcePackage: nodev1alpha1.ResourcePackage{
								PackageName: "test-package",
								Allocatable: nil,
							},
						},
					},
					1: {
						{
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
			result, err := optimizer.getResourcePackageAllocatable("test-package")
			convey.So(err, convey.ShouldBeNil)
			convey.So(len(result), convey.ShouldEqual, 0)
		})
	})
}

func TestResourcePackageHintOptimizer_getResourcePackageAllocated(t *testing.T) {
	t.Parallel()

	convey.Convey("Test getResourcePackageAllocated", t, func() {
		convey.Convey("success case", func() {
			mockState := &MockState{
				GetMachineStateFunc: func() state.NUMANodeMap {
					return state.NUMANodeMap{
						0: &state.NUMANodeState{
							PodEntries: state.PodEntries{
								"pod-1": state.ContainerEntries{
									"container-1": &state.AllocationInfo{
										AllocationMeta: commonstate.AllocationMeta{
											PodUid:         "pod-1",
											PodNamespace:   "default",
											PodName:        "pod-1",
											ContainerName:  "container-1",
											ContainerType:  "main",
											ContainerIndex: 0,
											OwnerPoolName:  "share",
											Labels:         map[string]string{},
											Annotations: map[string]string{
												apiconsts.PodAnnotationResourcePackageKey: "test-package",
											},
											QoSLevel: "shared_cores",
										},
										RequestQuantity: 1.5,
									},
								},
							},
						},
						1: &state.NUMANodeState{
							PodEntries: state.PodEntries{
								"pod-2": state.ContainerEntries{
									"container-2": &state.AllocationInfo{
										AllocationMeta: commonstate.AllocationMeta{
											PodUid:         "pod-2",
											PodNamespace:   "default",
											PodName:        "pod-2",
											ContainerName:  "container-2",
											ContainerType:  "main",
											ContainerIndex: 0,
											OwnerPoolName:  "share",
											Labels:         map[string]string{},
											Annotations: map[string]string{
												apiconsts.PodAnnotationResourcePackageKey: "test-package",
											},
											QoSLevel: "shared_cores",
										},
										RequestQuantity: 2.0,
									},
								},
							},
						},
					}
				},
			}

			optimizer := &resourcePackageHintOptimizer{
				state:      mockState,
				metaServer: &metaserver.MetaServer{},
			}
			result, err := optimizer.getResourcePackageAllocated("test-package")
			convey.So(err, convey.ShouldBeNil)
			convey.So(result[0], convey.ShouldEqual, 1.5)
			convey.So(result[1], convey.ShouldEqual, 2.0)
		})

		convey.Convey("when entry is nil", func() {
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

			optimizer := &resourcePackageHintOptimizer{
				state:      mockState,
				metaServer: &metaserver.MetaServer{},
			}
			result, err := optimizer.getResourcePackageAllocated("test-package")
			convey.So(err, convey.ShouldBeNil)
			convey.So(len(result), convey.ShouldEqual, 0)
		})

		convey.Convey("when resource package name doesn't match", func() {
			mockState := &MockState{
				GetMachineStateFunc: func() state.NUMANodeMap {
					return state.NUMANodeMap{
						0: &state.NUMANodeState{
							PodEntries: state.PodEntries{
								"pod-1": state.ContainerEntries{
									"container-1": &state.AllocationInfo{
										AllocationMeta: commonstate.AllocationMeta{
											PodUid:         "pod-1",
											PodNamespace:   "default",
											PodName:        "pod-1",
											ContainerName:  "container-1",
											ContainerType:  "main",
											ContainerIndex: 0,
											OwnerPoolName:  "share",
											Labels:         map[string]string{},
											Annotations: map[string]string{
												apiconsts.PodAnnotationResourcePackageKey: "other-package",
											},
											QoSLevel: "shared_cores",
										},
										RequestQuantity: 1.5,
									},
								},
							},
						},
					}
				},
			}

			optimizer := &resourcePackageHintOptimizer{
				state:      mockState,
				metaServer: &metaserver.MetaServer{},
			}
			result, err := optimizer.getResourcePackageAllocated("test-package")
			convey.So(err, convey.ShouldBeNil)
			convey.So(len(result), convey.ShouldEqual, 0)
		})
	})
}

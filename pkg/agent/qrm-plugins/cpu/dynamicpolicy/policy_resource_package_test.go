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

package dynamicpolicy

import (
	"context"
	"io/ioutil"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	nodev1alpha1 "github.com/kubewharf/katalyst-api/pkg/apis/node/v1alpha1"
	"github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	cpuconsts "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/util"
	metaresourcepackage "github.com/kubewharf/katalyst-core/pkg/metaserver/resourcepackage"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
	utilresourcepackage "github.com/kubewharf/katalyst-core/pkg/util/resource-package"
)

type MockMetricsEmitter struct {
	metrics.DummyMetrics
	storedInt64 map[string][]int64
	storedTags  map[string][][]metrics.MetricTag
}

func NewMockMetricsEmitter() *MockMetricsEmitter {
	return &MockMetricsEmitter{
		storedInt64: make(map[string][]int64),
		storedTags:  make(map[string][][]metrics.MetricTag),
	}
}

func (m *MockMetricsEmitter) StoreInt64(key string, val int64, emitType metrics.MetricTypeName, tags ...metrics.MetricTag) error {
	m.storedInt64[key] = append(m.storedInt64[key], val)
	m.storedTags[key] = append(m.storedTags[key], tags)
	return nil
}

func (m *MockMetricsEmitter) WithTags(unit string, commonTags ...metrics.MetricTag) metrics.MetricEmitter {
	w := &metrics.MetricTagWrapper{MetricEmitter: m}
	return w.WithTags(unit, commonTags...)
}

type mockResourcePackageManager struct {
	metaresourcepackage.ResourcePackageManager
	items utilresourcepackage.NUMAResourcePackageItems
	err   error
}

func (m *mockResourcePackageManager) NodeResourcePackages(ctx context.Context) (utilresourcepackage.NUMAResourcePackageItems, error) {
	return m.items, m.err
}

func TestSyncResourcePackagePinnedCPUSet(t *testing.T) {
	t.Parallel()

	tmpDir, err := ioutil.TempDir("", "checkpoint-test")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	cpuTopology, err := machine.GenerateDummyCPUTopology(16, 2, 2)
	require.NoError(t, err)
	// NUMA 0: CPUs [0-7]
	// NUMA 1: CPUs [8-15]

	// Helper to create NUMAResourcePackageItems
	createPkgItems := func(config map[int]map[string]int) utilresourcepackage.NUMAResourcePackageItems {
		items := make(utilresourcepackage.NUMAResourcePackageItems)
		for numa, pkgs := range config {
			items[numa] = make(map[string]utilresourcepackage.ResourcePackageItem)
			for pkgName, size := range pkgs {
				pinned := true
				items[numa][pkgName] = utilresourcepackage.ResourcePackageItem{
					ResourcePackage: nodev1alpha1.ResourcePackage{
						PackageName: pkgName,
						Allocatable: &v1.ResourceList{
							v1.ResourceCPU: *resource.NewQuantity(int64(size), resource.DecimalSI),
						},
					},
					Config: &utilresourcepackage.ResourcePackageConfig{
						PinnedCPUSet: &pinned,
					},
				}
			}
		}
		return items
	}

	tests := []struct {
		name             string
		initialState     func(dp *DynamicPolicy) // Set up initial machine state and pods
		resourcePackages utilresourcepackage.NUMAResourcePackageItems
		verify           func(t *testing.T, machineState state.NUMANodeMap, podEntries state.PodEntries)
		checkMetrics     func(t *testing.T, emitter *MockMetricsEmitter)
		expectError      bool
	}{
		{
			name: "Expand Pinned CPUSet",
			initialState: func(dp *DynamicPolicy) {
				// Initial: NUMA 0 has pkg-a pinned to [2,3] (size 2)
				ms := dp.state.GetMachineState()
				if ms[0].ResourcePackagePinnedCPUSet == nil {
					ms[0].ResourcePackagePinnedCPUSet = make(map[string]machine.CPUSet)
				}
				ms[0].ResourcePackagePinnedCPUSet["pkg-a"] = machine.NewCPUSet(2, 3)
				dp.state.SetMachineState(ms, false)
			},
			resourcePackages: createPkgItems(map[int]map[string]int{
				0: {"pkg-a": 4}, // Request expansion to 4
			}),
			verify: func(t *testing.T, ms state.NUMANodeMap, pe state.PodEntries) {
				pinned := ms[0].ResourcePackagePinnedCPUSet["pkg-a"]
				assert.Equal(t, 4, pinned.Size())
				assert.True(t, machine.NewCPUSet(2, 3).IsSubsetOf(pinned), "Should keep original CPUs")
			},
			checkMetrics: func(t *testing.T, emitter *MockMetricsEmitter) {
				vals := emitter.storedInt64[util.MetricNameResourcePackagePinnedCPUSetSize]
				assert.NotEmpty(t, vals)
				assert.Contains(t, vals, int64(4))
			},
		},
		{
			name: "Shrink Pinned CPUSet with Shared Cores Constraint",
			initialState: func(dp *DynamicPolicy) {
				// Ensure deterministic reserved CPUs and clear existing pool
				dp.reservedCPUs = machine.NewCPUSet(0, 1)
				podEntries := dp.state.GetPodEntries()
				delete(podEntries, commonstate.PoolNameReserve)
				dp.state.SetPodEntries(podEntries, false)

				// Calculate valid CPUSet for pkg-b on NUMA 0 (need 4 CPUs)
				cpus0 := dp.machineInfo.CPUDetails.CPUsInNUMANodes(0).Difference(dp.reservedCPUs)
				pkgBCPUSet := machine.NewCPUSet(cpus0.ToSliceInt()[:4]...)

				// Pod constraint: Shared pod requesting 1 CPUs
				podID := "pod-shared"
				alloc := &state.AllocationInfo{
					AllocationMeta: commonstate.AllocationMeta{
						PodUid:        podID,
						PodName:       podID,
						ContainerName: "c1",
						ContainerType: pluginapi.ContainerType_MAIN.String(),
						QoSLevel:      consts.PodAnnotationQoSLevelSharedCores,
						Annotations: map[string]string{
							consts.PodAnnotationResourcePackageKey:           "pkg-b",
							consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
							cpuconsts.CPUStateAnnotationKeyNUMAHint:          "0",
						},
					},
					AllocationResult: pkgBCPUSet.Clone(),
					TopologyAwareAssignments: map[int]machine.CPUSet{
						0: pkgBCPUSet.Clone(),
					},
					RequestQuantity: 1.0,
				}
				dp.state.SetAllocationInfo(podID, "c1", alloc, false)

				mockPodInMetaServer(dp, alloc, "1")

				// Generate MS from Pods to ensure PodEntries are populated in NUMANodeState
				podEntries = dp.state.GetPodEntries()
				ms, err := state.GenerateMachineStateFromPodEntries(dp.machineInfo.CPUTopology, podEntries, dp.state.GetMachineState())
				require.NoError(t, err)

				// Initial: pkg-b pinned to pkgBCPUSet (size 4)
				if ms[0].ResourcePackagePinnedCPUSet == nil {
					ms[0].ResourcePackagePinnedCPUSet = make(map[string]machine.CPUSet)
				}
				ms[0].ResourcePackagePinnedCPUSet["pkg-b"] = pkgBCPUSet.Clone()
				dp.state.SetMachineState(ms, false)
			},
			resourcePackages: createPkgItems(map[int]map[string]int{
				0: {"pkg-b": 2}, // Request shrink to 2
			}),
			verify: func(t *testing.T, ms state.NUMANodeMap, pe state.PodEntries) {
				pinned := ms[0].ResourcePackagePinnedCPUSet["pkg-b"]
				assert.Equal(t, 2, pinned.Size(), "Should be limited by shared request (1*2=2)")

				// Verify pod allocation updated
				podAlloc := pe["pod-shared"]["c1"]
				assert.Equal(t, pinned.Size(), podAlloc.AllocationResult.Size())
				assert.True(t, podAlloc.AllocationResult.Equals(pinned))
			},
			checkMetrics: func(t *testing.T, emitter *MockMetricsEmitter) {
				vals := emitter.storedInt64[util.MetricNameResourcePackagePinnedCPUSetSize]
				assert.NotEmpty(t, vals)
				assert.Contains(t, vals, int64(2))
			},
		},
		{
			name: "Preserve Dedicated Cores",
			initialState: func(dp *DynamicPolicy) {
				// Pod constraint: Dedicated pod using [2,3]
				podID := "pod-dedicated"
				alloc := &state.AllocationInfo{
					AllocationMeta: commonstate.AllocationMeta{
						PodUid:        podID,
						PodName:       podID,
						ContainerName: "c1",
						QoSLevel:      consts.PodAnnotationQoSLevelDedicatedCores,
						Annotations: map[string]string{
							consts.PodAnnotationResourcePackageKey: "pkg-c",
						},
					},
					AllocationResult: machine.NewCPUSet(2, 3),
					TopologyAwareAssignments: map[int]machine.CPUSet{
						0: machine.NewCPUSet(2, 3),
					},
				}
				dp.state.SetAllocationInfo(podID, "c1", alloc, false)

				// Generate MS
				podEntries := dp.state.GetPodEntries()
				ms, err := state.GenerateMachineStateFromPodEntries(dp.machineInfo.CPUTopology, podEntries, dp.state.GetMachineState())
				require.NoError(t, err)

				// Initial: pkg-c pinned to [2,3,4,5] (size 4)
				if ms[0].ResourcePackagePinnedCPUSet == nil {
					ms[0].ResourcePackagePinnedCPUSet = make(map[string]machine.CPUSet)
				}
				ms[0].ResourcePackagePinnedCPUSet["pkg-c"] = machine.NewCPUSet(2, 3, 4, 5)
				dp.state.SetMachineState(ms, false)
			},
			resourcePackages: createPkgItems(map[int]map[string]int{
				0: {"pkg-c": 3}, // Request shrink to 3
			}),
			verify: func(t *testing.T, ms state.NUMANodeMap, pe state.PodEntries) {
				pinned := ms[0].ResourcePackagePinnedCPUSet["pkg-c"]
				assert.Equal(t, 3, pinned.Size())
				assert.True(t, machine.NewCPUSet(2, 3).IsSubsetOf(pinned), "Must contain dedicated cores")
			},
		},
		{
			name: "Remove Package",
			initialState: func(dp *DynamicPolicy) {
				// Initial: pkg-d pinned
				ms := dp.state.GetMachineState()
				if ms[0].ResourcePackagePinnedCPUSet == nil {
					ms[0].ResourcePackagePinnedCPUSet = make(map[string]machine.CPUSet)
				}
				ms[0].ResourcePackagePinnedCPUSet["pkg-d"] = machine.NewCPUSet(6, 7)
				dp.state.SetMachineState(ms, false)
			},
			resourcePackages: createPkgItems(map[int]map[string]int{
				0: {}, // Empty config
			}),
			verify: func(t *testing.T, ms state.NUMANodeMap, pe state.PodEntries) {
				_, exists := ms[0].ResourcePackagePinnedCPUSet["pkg-d"]
				assert.False(t, exists, "Should be removed")
			},
		},
		{
			name: "Remove Package with Active Pods (Keep)",
			initialState: func(dp *DynamicPolicy) {
				// Pod exists
				podID := "pod-e"
				alloc := &state.AllocationInfo{
					AllocationMeta: commonstate.AllocationMeta{
						PodUid:        podID,
						PodName:       podID,
						ContainerName: "c1",
						ContainerType: pluginapi.ContainerType_MAIN.String(),
						QoSLevel:      consts.PodAnnotationQoSLevelSharedCores,
						Annotations: map[string]string{
							consts.PodAnnotationResourcePackageKey: "pkg-e",
						},
					},
					AllocationResult: machine.NewCPUSet(6, 7),
					TopologyAwareAssignments: map[int]machine.CPUSet{
						0: machine.NewCPUSet(6, 7),
					},
				}
				dp.state.SetAllocationInfo(podID, "c1", alloc, false)

				// Generate MS
				podEntries := dp.state.GetPodEntries()
				ms, err := state.GenerateMachineStateFromPodEntries(dp.machineInfo.CPUTopology, podEntries, dp.state.GetMachineState())
				require.NoError(t, err)

				// Initial: pkg-e pinned
				if ms[0].ResourcePackagePinnedCPUSet == nil {
					ms[0].ResourcePackagePinnedCPUSet = make(map[string]machine.CPUSet)
				}
				ms[0].ResourcePackagePinnedCPUSet["pkg-e"] = machine.NewCPUSet(6, 7)
				dp.state.SetMachineState(ms, false)
			},
			resourcePackages: createPkgItems(map[int]map[string]int{
				0: {}, // Empty config
			}),
			verify: func(t *testing.T, ms state.NUMANodeMap, pe state.PodEntries) {
				_, exists := ms[0].ResourcePackagePinnedCPUSet["pkg-e"]
				assert.True(t, exists, "Should keep package because pods exist")
			},
		},
		{
			name: "Panic Prevention: Missing NUMA in Config",
			initialState: func(dp *DynamicPolicy) {
				// Initial: pkg-f pinned on NUMA 0
				ms := dp.state.GetMachineState()
				if ms[0].ResourcePackagePinnedCPUSet == nil {
					ms[0].ResourcePackagePinnedCPUSet = make(map[string]machine.CPUSet)
				}
				ms[0].ResourcePackagePinnedCPUSet["pkg-f"] = machine.NewCPUSet(0, 1)
				dp.state.SetMachineState(ms, false)
			},
			resourcePackages: func() utilresourcepackage.NUMAResourcePackageItems {
				// Create config ONLY for NUMA 1, missing NUMA 0
				items := createPkgItems(map[int]map[string]int{
					1: {"pkg-g": 4},
				})
				// Explicitly ensure NUMA 0 is nil in the map
				delete(items, 0)
				return items
			}(),
			verify: func(t *testing.T, ms state.NUMANodeMap, pe state.PodEntries) {
				// Should remove pkg-f from NUMA 0 because it's not in config (and no active pods)
				_, exists := ms[0].ResourcePackagePinnedCPUSet["pkg-f"]
				assert.False(t, exists, "Should be removed safely without panic")
			},
		},
		{
			name: "Error Handling: Calculator Fail (Expand)",
			initialState: func(dp *DynamicPolicy) {
				// Initial: pkg-fail pinned to [0,1]
				ms := dp.state.GetMachineState()
				if ms[0].ResourcePackagePinnedCPUSet == nil {
					ms[0].ResourcePackagePinnedCPUSet = make(map[string]machine.CPUSet)
				}
				ms[0].ResourcePackagePinnedCPUSet["pkg-fail"] = machine.NewCPUSet(0, 1)

				// Occupy all other CPUs to force allocation failure
				// We must use actual NUMA 0 CPUs from machineInfo because GenerateDummyCPUTopology might be interleaved.
				cpusInNuma0 := dp.machineInfo.CPUDetails.CPUsInNUMANodes(0)
				// Exclude reserved (if any) and current pinned
				available := cpusInNuma0.Difference(dp.reservedCPUs).Difference(machine.NewCPUSet(0, 1))

				// Set AllocatedCPUSet to occupy ALL available CPUs
				ms[0].AllocatedCPUSet = available
				dp.state.SetMachineState(ms, false)
			},
			resourcePackages: createPkgItems(map[int]map[string]int{
				0: {"pkg-fail": 4}, // Want to expand to 4, but 0 available
			}),
			verify: func(t *testing.T, ms state.NUMANodeMap, pe state.PodEntries) {
				pinned := ms[0].ResourcePackagePinnedCPUSet["pkg-fail"]
				assert.Equal(t, 2, pinned.Size(), "Should stay at original size due to allocation failure")
			},
			checkMetrics: func(t *testing.T, emitter *MockMetricsEmitter) {
				vals := emitter.storedInt64[util.MetricNameSyncNumaResourcePackageFailed]
				assert.NotEmpty(t, vals)
				// Check for "expand_failed" reason
				found := false
				for _, tags := range emitter.storedTags[util.MetricNameSyncNumaResourcePackageFailed] {
					for _, tag := range tags {
						if tag.Key == "reason" && tag.Val == "expand_failed" {
							found = true
							break
						}
					}
				}
				assert.True(t, found, "Should emit expand_failed metric")

				// Top level failure also emitted?
				// syncNumaResourcePackage returns error if failure?
				// The code swallows calculator error and returns newPinned = currentPinned.
				// So syncNumaResourcePackage returns nil error.
				// Thus MetricNameSyncResourcePackagePinnedCPUSetFailed is NOT emitted.
				// This is correct behavior based on my implementation (error is logged and metric emitted, but function continues).
			},
		},
		{
			name: "Capacity Check: Non-Pinned Shared Pods Fit",
			initialState: func(dp *DynamicPolicy) {
				// NUMA 0 has 8 CPUs. Use even numbers to be safe with interleaved topology.
				// Pkg-h pinned to [0,2] (size 2).
				// Remaining available: 6 CPUs.
				// Non-pinned shared pod requests 4 CPUs.
				podID := "pod-non-pinned-fit"
				alloc := &state.AllocationInfo{
					AllocationMeta: commonstate.AllocationMeta{
						PodUid:        podID,
						PodName:       podID,
						ContainerName: "c1",
						ContainerType: pluginapi.ContainerType_MAIN.String(),
						QoSLevel:      consts.PodAnnotationQoSLevelSharedCores,
						Annotations: map[string]string{
							consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
							// No package name => non-pinned
							cpuconsts.CPUStateAnnotationKeyNUMAHint: "0",
						},
					},
					RequestQuantity: 4.0,
					TopologyAwareAssignments: map[int]machine.CPUSet{
						0: machine.NewCPUSet(4, 5, 6, 7), // Just some assignment on NUMA 0
					},
				}
				dp.state.SetAllocationInfo(podID, "c1", alloc, false)
				mockPodInMetaServer(dp, alloc, "3")

				// Generate MS from Pods to ensure PodEntries are populated in NUMANodeState
				podEntries := dp.state.GetPodEntries()
				ms, err := state.GenerateMachineStateFromPodEntries(dp.machineInfo.CPUTopology, podEntries, dp.state.GetMachineState())
				require.NoError(t, err)

				if ms[0].ResourcePackagePinnedCPUSet == nil {
					ms[0].ResourcePackagePinnedCPUSet = make(map[string]machine.CPUSet)
				}
				ms[0].ResourcePackagePinnedCPUSet["pkg-h"] = machine.NewCPUSet(0, 2)
				dp.state.SetMachineState(ms, false)
			},
			resourcePackages: createPkgItems(map[int]map[string]int{
				0: {"pkg-h": 2}, // Keep size 2
			}),
			verify: func(t *testing.T, ms state.NUMANodeMap, pe state.PodEntries) {
				pinned := ms[0].ResourcePackagePinnedCPUSet["pkg-h"]
				assert.Equal(t, 2, pinned.Size())
			},
		},
		{
			name: "Capacity Check: Non-Pinned Shared Pods Exceed",
			initialState: func(dp *DynamicPolicy) {
				// NUMA 0 has 8 CPUs.
				// Pkg-i pinned to [0,2] (size 2).
				// Request expansion to 4.
				// Non-pinned shared pod requests 10 CPUs (surely exceeds).
				podID := "pod-non-pinned-exceed"
				alloc := &state.AllocationInfo{
					AllocationMeta: commonstate.AllocationMeta{
						PodUid:        podID,
						PodName:       podID,
						ContainerName: "c1",
						ContainerType: pluginapi.ContainerType_MAIN.String(),
						QoSLevel:      consts.PodAnnotationQoSLevelSharedCores,
						Annotations: map[string]string{
							consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
							// No package name => non-pinned
							cpuconsts.CPUStateAnnotationKeyNUMAHint: "0",
						},
					},
					RequestQuantity: 10.0,
					TopologyAwareAssignments: map[int]machine.CPUSet{
						0: machine.NewCPUSet(4, 5, 6, 7), // Just some assignment on NUMA 0
					},
				}
				dp.state.SetAllocationInfo(podID, "c1", alloc, false)
				mockPodInMetaServer(dp, alloc, "7")

				// Generate MS from Pods
				podEntries := dp.state.GetPodEntries()
				ms, err := state.GenerateMachineStateFromPodEntries(dp.machineInfo.CPUTopology, podEntries, dp.state.GetMachineState())
				require.NoError(t, err)

				if ms[0].ResourcePackagePinnedCPUSet == nil {
					ms[0].ResourcePackagePinnedCPUSet = make(map[string]machine.CPUSet)
				}
				ms[0].ResourcePackagePinnedCPUSet["pkg-i"] = machine.NewCPUSet(0, 2)
				dp.state.SetMachineState(ms, false)
			},
			resourcePackages: createPkgItems(map[int]map[string]int{
				0: {"pkg-i": 4}, // Request expansion to 4
			}),
			verify: func(t *testing.T, ms state.NUMANodeMap, pe state.PodEntries) {
				pinned := ms[0].ResourcePackagePinnedCPUSet["pkg-i"]
				assert.Equal(t, 2, pinned.Size(), "Should stay at original size due to validation failure")
			},
		},
		{
			name: "Capacity Check: No Package Pods Exceed",
			initialState: func(dp *DynamicPolicy) {
				// NUMA 0 has 8 CPUs.
				// Pkg-j pinned to [0,2] (size 2).
				// Config requests 6.
				// Pod requests 10.
				// Validation should fail.
				podID := "pod-no-pkg-exceed"
				alloc := &state.AllocationInfo{
					AllocationMeta: commonstate.AllocationMeta{
						PodUid:        podID,
						PodName:       podID,
						ContainerName: "c1",
						QoSLevel:      consts.PodAnnotationQoSLevelSharedCores,
						Annotations: map[string]string{
							consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
							consts.PodAnnotationResourcePackageKey:           "", // Explicit empty
							cpuconsts.CPUStateAnnotationKeyNUMAHint:          "0",
						},
					},
					RequestQuantity: 10.0,
					TopologyAwareAssignments: map[int]machine.CPUSet{
						0: machine.NewCPUSet(4, 5, 6, 7), // Just some assignment on NUMA 0
					},
				}
				dp.state.SetAllocationInfo(podID, "c1", alloc, false)
				mockPodInMetaServer(dp, alloc, "7")

				// Generate MS from Pods to ensure PodEntries are populated in NUMANodeState
				podEntries := dp.state.GetPodEntries()
				ms, err := state.GenerateMachineStateFromPodEntries(dp.machineInfo.CPUTopology, podEntries, dp.state.GetMachineState())
				require.NoError(t, err)

				if ms[0].ResourcePackagePinnedCPUSet == nil {
					ms[0].ResourcePackagePinnedCPUSet = make(map[string]machine.CPUSet)
				}
				ms[0].ResourcePackagePinnedCPUSet["pkg-j"] = machine.NewCPUSet(0, 2)
				dp.state.SetMachineState(ms, false)
			},
			resourcePackages: createPkgItems(map[int]map[string]int{
				0: {"pkg-j": 6}, // Request expansion to 6
			}),
			verify: func(t *testing.T, ms state.NUMANodeMap, pe state.PodEntries) {
				pinned := ms[0].ResourcePackagePinnedCPUSet["pkg-j"]
				assert.Equal(t, 2, pinned.Size(), "Should stay at original size due to validation failure")
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			tmpDir, err := ioutil.TempDir("", "checkpoint-test")
			require.NoError(t, err)
			defer os.RemoveAll(tmpDir)

			dp, err := getTestDynamicPolicyWithInitialization(cpuTopology, tmpDir)
			require.NoError(t, err)

			tt.initialState(dp)

			// Mock Metrics Emitter
			mockEmitter := NewMockMetricsEmitter()
			dp.emitter = mockEmitter

			// Mock Resource Package Manager
			mockMgr := &mockResourcePackageManager{
				items: tt.resourcePackages,
			}
			dp.resourcePackageManager = metaresourcepackage.NewCachedResourcePackageManager(mockMgr)
			stopCh := make(chan struct{})
			_ = dp.resourcePackageManager.Run(stopCh)
			defer close(stopCh)

			// Run Sync
			dp.syncResourcePackagePinnedCPUSet()

			// Verify
			ms := dp.state.GetMachineState()
			pe := dp.state.GetPodEntries()
			tt.verify(t, ms, pe)

			if tt.checkMetrics != nil {
				tt.checkMetrics(t, mockEmitter)
			}
		})
	}
}

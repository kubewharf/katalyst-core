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
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	v1 "k8s.io/api/core/v1"
	resource2 "k8s.io/apimachinery/pkg/api/resource"

	apiconsts "github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/advisorsvc"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	advisorapi "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/cpuadvisor"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/qrm/statedirectory"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/cgroup/common"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

// advisorTestMutex is retained for tests in other files that still serialize
// around shared state; the advisor handler tests no longer hold it.
var advisorTestMutex = &sync.Mutex{}

func TestDynamicPolicy_checkAndApplyIfCgroupV1(t *testing.T) {
	t.Parallel()

	resources := &common.CgroupResources{
		CpuQuota:  1000,
		CpuPeriod: 1000,
	}

	mockBG := &common.CPUStats{
		CpuQuota:  1000,
		CpuPeriod: 1000,
	}

	mockBG2 := &common.CPUStats{
		CpuQuota:  500,
		CpuPeriod: 1000,
	}

	mockBytes, _ := json.Marshal(resources)

	mockCal := &advisorsvc.CalculationInfo{
		CgroupPath: "test_cgroup_path",
		CalculationResult: &advisorsvc.CalculationResult{
			Values: map[string]string{
				string(advisorapi.ControlKnobKeyCgroupConfig): string(mockBytes),
			},
		},
	}

	newDeps := func(bg *common.CPUStats) cpuAdvisorDeps {
		return cpuAdvisorDeps{
			checkUnifiedMode:          func() bool { return false },
			getCPUWithRelativePath:    func(rel string) (*common.CPUStats, error) { return bg, nil },
			checkAndApplyAllPodsQuota: func(*advisorsvc.CalculationInfo, int64) error { return nil },
		}
	}

	assert.NoError(t, checkAndApplyIfCgroupV1(newDeps(mockBG), mockCal, resources))
	assert.NoError(t, checkAndApplyIfCgroupV1(newDeps(mockBG2), mockCal, resources))
}

func TestDynamicPolicy_getAllDirs(t *testing.T) {
	t.Parallel()

	t.Run("basic path", func(t *testing.T) {
		t.Parallel()

		deps := cpuAdvisorDeps{
			readDir: func(dirname string) ([]os.DirEntry, error) {
				return []os.DirEntry{
					mockDirEntry{"foo", true},
					mockDirEntry{"bar", true},
				}, nil
			},
		}

		dirs, err := getAllDirs(deps, "/fake/path")
		assert.NoError(t, err)
		assert.ElementsMatch(t, dirs, []string{"foo", "bar"})
	})
}

type mockDirEntry struct {
	name  string
	isDir bool
}

func (m mockDirEntry) Name() string               { return m.name }
func (m mockDirEntry) IsDir() bool                { return m.isDir }
func (m mockDirEntry) Type() os.FileMode          { return 0 }
func (m mockDirEntry) Info() (os.FileInfo, error) { return nil, nil }

func TestDynamic_getCurrentPathAllPodsDirAndMap(t *testing.T) {
	t.Parallel()

	mockPod := &v1.Pod{
		Spec: v1.PodSpec{
			Containers: []v1.Container{
				{
					Name: "test-container",
					Resources: v1.ResourceRequirements{
						Requests: v1.ResourceList{
							v1.ResourceCPU: resource2.MustParse("2"),
						},
					},
				},
			},
		},
	}

	deps := cpuAdvisorDeps{
		listActivePods:      func() ([]*v1.Pod, error) { return []*v1.Pod{mockPod}, nil },
		getPodAbsCgroupPath: func(subsys, podUID string) (string, error) { return "test-pod-1", nil },
		readDir: func(parentPath string) ([]os.DirEntry, error) {
			return []os.DirEntry{mockDirEntry{"advisor-test-pod-1", true}}, nil
		},
		getAbsCgroupPath: common.GetAbsCgroupPath,
	}

	resultMap, dirs, err := getCurrentPathAllPodsDirAndMap(deps, "test_group_path")
	assert.NoError(t, err)
	assert.NotNil(t, resultMap)
	assert.NotNil(t, dirs)
}

func TestDynamicPolicy_getPodAndRelativePath(t *testing.T) {
	t.Parallel()

	currentPath := "test"
	dirs := "test-dir"
	podPathMap := map[string]*v1.Pod{
		common.GetAbsCgroupPath(common.DefaultSelectedSubsys, filepath.Join(currentPath, dirs)): {
			Spec: v1.PodSpec{
				Containers: []v1.Container{
					{
						Name: "test-container",
						Resources: v1.ResourceRequirements{
							Requests: v1.ResourceList{
								v1.ResourceCPU: resource2.MustParse("2"),
							},
						},
					},
				},
			},
		},
	}

	deps := cpuAdvisorDeps{
		getAbsCgroupPath: common.GetAbsCgroupPath,
	}

	_, _, err := getPodAndRelativePath(deps, currentPath, dirs, podPathMap)
	assert.NoError(t, err)
}

func TestDynamicPolicy_getAllPodsPathMap(t *testing.T) {
	t.Parallel()

	mockPods := []*v1.Pod{
		{
			Spec: v1.PodSpec{
				Containers: []v1.Container{
					{
						Name: "test-container",
						Resources: v1.ResourceRequirements{
							Requests: v1.ResourceList{
								v1.ResourceCPU: resource2.MustParse("2"),
							},
						},
					},
				},
			},
		},
	}

	deps := cpuAdvisorDeps{
		listActivePods:      func() ([]*v1.Pod, error) { return mockPods, nil },
		getPodAbsCgroupPath: func(subsys, podUID string) (string, error) { return "test-pod-1-path", nil },
	}

	podPathMap, err := getAllPodsPathMap(deps)

	assert.NoError(t, err)
	assert.Equal(t, len(mockPods), len(podPathMap))
	assert.Equal(t, mockPods[0], podPathMap["test-pod-1-path"])
}

func TestDynamicPolicy_getAllContainersRelativePathMap(t *testing.T) {
	t.Parallel()

	mockPod := &v1.Pod{
		Spec: v1.PodSpec{
			Containers: []v1.Container{
				{
					Name: "test-container",
					Resources: v1.ResourceRequirements{
						Requests: v1.ResourceList{
							v1.ResourceCPU: resource2.MustParse("2"),
						},
					},
				},
			},
		},
	}

	deps := cpuAdvisorDeps{
		getContainerID:            func(pod *v1.Pod, name string) (string, error) { return "test-container-ID", nil },
		getContainerRelCgroupPath: func(podUID, containerID string) (string, error) { return "test-container-relative-path", nil },
	}

	testMap := getAllContainersRelativePathMap(deps, mockPod)

	assert.Equal(t, 1, len(testMap))
	assert.Equal(t, mockPod.Spec.Containers[0].Name, testMap["test-container-relative-path"].Name)
}

func TestDynamicPolicy_checkAllPodsQuota(t *testing.T) {
	t.Parallel()

	mockPod := &v1.Pod{
		Spec: v1.PodSpec{
			Containers: []v1.Container{
				{
					Name: "test-container",
					Resources: v1.ResourceRequirements{
						Requests: v1.ResourceList{
							v1.ResourceCPU: resource2.MustParse("2"),
						},
						Limits: v1.ResourceList{
							v1.ResourceCPU: resource2.MustParse("4"),
						},
					},
				},
			},
		},
	}

	mockPodPathMap := map[string]*v1.Pod{
		"test-pod-1": mockPod,
	}

	mockPodDirs := []string{
		"test-pod-1-dir",
	}

	resources := &common.CgroupResources{
		CpuQuota:  1000,
		CpuPeriod: 1000,
	}

	mockBG := &common.CPUStats{
		CpuQuota:  1000,
		CpuPeriod: 1000,
	}

	mockBG2 := &common.CPUStats{
		CpuQuota:  2000,
		CpuPeriod: 1000,
	}

	mockBG3 := &common.CPUStats{
		CpuQuota:  2000000,
		CpuPeriod: 1000,
	}

	mockBytes, _ := json.Marshal(resources)

	mockCal := &advisorsvc.CalculationInfo{
		CgroupPath: "test_cgroup_path",
		CalculationResult: &advisorsvc.CalculationResult{
			Values: map[string]string{
				string(advisorapi.ControlKnobKeyCgroupConfig): string(mockBytes),
			},
		},
	}

	mockErr := fmt.Errorf("mock error")

	successCurrent := func(path string) (map[string]*v1.Pod, []string, error) {
		return mockPodPathMap, mockPodDirs, nil
	}
	successPod := func(currentCgroupPath, podDir string, podsPathMap map[string]*v1.Pod) (*v1.Pod, string, error) {
		return mockPod, "test_relative_path", nil
	}

	t.Run("all quotas applied", func(t *testing.T) {
		t.Parallel()
		deps := cpuAdvisorDeps{
			getCurrentPathAllPodsDirAndMap: successCurrent,
			getPodAndRelativePath:          successPod,
			applyAllContainersQuota:        func(pod *v1.Pod, setToLimit bool) error { return nil },
			applyCPUWithRelativePath:       func(rel string, data *common.CPUData) error { return nil },
			getCPUWithRelativePath:         func(rel string) (*common.CPUStats, error) { return mockBG, nil },
		}

		assert.NoError(t, checkAndApplyAllPodsQuota(deps, mockCal, mockBG.CpuQuota))
		assert.NoError(t, checkAndApplyAllPodsQuota(deps, mockCal, mockBG2.CpuQuota))
		assert.NoError(t, checkAndApplyAllPodsQuota(deps, mockCal, mockBG3.CpuQuota))
	})

	t.Run("current path lookup fails", func(t *testing.T) {
		t.Parallel()
		deps := cpuAdvisorDeps{
			getCurrentPathAllPodsDirAndMap: func(path string) (map[string]*v1.Pod, []string, error) {
				return nil, nil, mockErr
			},
		}
		assert.Error(t, checkAndApplyAllPodsQuota(deps, mockCal, mockBG.CpuQuota))
	})

	t.Run("pod relative path lookup fails and skipped", func(t *testing.T) {
		t.Parallel()
		deps := cpuAdvisorDeps{
			getCurrentPathAllPodsDirAndMap: successCurrent,
			getPodAndRelativePath: func(currentCgroupPath, podDir string, podsPathMap map[string]*v1.Pod) (*v1.Pod, string, error) {
				return mockPod, "test_relative_path", mockErr
			},
		}
		assert.NoError(t, checkAndApplyAllPodsQuota(deps, mockCal, mockBG.CpuQuota))
	})

	t.Run("get pod cpu fails", func(t *testing.T) {
		t.Parallel()
		deps := cpuAdvisorDeps{
			getCurrentPathAllPodsDirAndMap: successCurrent,
			getPodAndRelativePath:          successPod,
			getCPUWithRelativePath:         func(rel string) (*common.CPUStats, error) { return mockBG, mockErr },
		}
		assert.Error(t, checkAndApplyAllPodsQuota(deps, mockCal, mockBG.CpuQuota))
	})

	t.Run("apply containers quota fails and skipped", func(t *testing.T) {
		t.Parallel()
		deps := cpuAdvisorDeps{
			getCurrentPathAllPodsDirAndMap: successCurrent,
			getPodAndRelativePath:          successPod,
			applyAllContainersQuota:        func(pod *v1.Pod, setToLimit bool) error { return mockErr },
			applyCPUWithRelativePath:       func(rel string, data *common.CPUData) error { return nil },
			getCPUWithRelativePath:         func(rel string) (*common.CPUStats, error) { return mockBG, nil },
		}

		assert.NoError(t, checkAndApplyAllPodsQuota(deps, mockCal, mockBG.CpuQuota))
		assert.NoError(t, checkAndApplyAllPodsQuota(deps, mockCal, mockBG2.CpuQuota))
		assert.NoError(t, checkAndApplyAllPodsQuota(deps, mockCal, mockBG3.CpuQuota))
	})

	t.Run("apply pod quota fails", func(t *testing.T) {
		t.Parallel()
		deps := cpuAdvisorDeps{
			getCurrentPathAllPodsDirAndMap: successCurrent,
			getPodAndRelativePath:          successPod,
			applyAllContainersQuota:        func(pod *v1.Pod, setToLimit bool) error { return nil },
			applyCPUWithRelativePath:       func(rel string, data *common.CPUData) error { return mockErr },
			getCPUWithRelativePath:         func(rel string) (*common.CPUStats, error) { return mockBG, nil },
		}

		assert.Error(t, checkAndApplyAllPodsQuota(deps, mockCal, mockBG.CpuQuota))
		assert.Error(t, checkAndApplyAllPodsQuota(deps, mockCal, mockBG2.CpuQuota))
		assert.Error(t, checkAndApplyAllPodsQuota(deps, mockCal, mockBG3.CpuQuota))
	})
}

func TestDynamicPolicy_applyAllContainersQuota(t *testing.T) {
	t.Parallel()

	containerPathMap := map[string]*v1.Container{
		"container1": {
			Name: "container1",
			Resources: v1.ResourceRequirements{
				Requests: v1.ResourceList{
					v1.ResourceCPU: resource2.MustParse("1"),
				},
			},
		},
		"container2": {
			Name: "container2",
			Resources: v1.ResourceRequirements{
				Requests: v1.ResourceList{
					v1.ResourceCPU: resource2.MustParse("2"),
				},
			},
		},
	}

	pod := &v1.Pod{
		Spec: v1.PodSpec{
			Containers: []v1.Container{
				{
					Name: "container1",
					Resources: v1.ResourceRequirements{
						Requests: v1.ResourceList{
							v1.ResourceCPU: resource2.MustParse("1"),
						},
					},
				},
				{
					Name: "container2",
					Resources: v1.ResourceRequirements{
						Requests: v1.ResourceList{
							v1.ResourceCPU: resource2.MustParse("2"),
						},
					},
				},
			},
		},
	}

	mockCPU := &common.CPUStats{
		CpuQuota:  1,
		CpuPeriod: 1000,
	}

	applyCount := 0
	deps := cpuAdvisorDeps{
		getAllContainersRelativePathMap: func(pod *v1.Pod) map[string]*v1.Container { return containerPathMap },
		applySubCgroupQuotaToUnLimit:    func(containerRelativePath string) error { return nil },
		getCPUWithRelativePath:          func(rel string) (*common.CPUStats, error) { return mockCPU, nil },
		applyCPUWithRelativePath:        func(rel string, data *common.CPUData) error { applyCount++; return nil },
	}

	err := applyAllContainersQuota(deps, pod, true)

	assert.NoError(t, err)
	assert.Equal(t, 2, applyCount)

	err = applyAllContainersQuota(deps, pod, false)
	assert.NoError(t, err)
}

func TestDynamicPolicy_checkAndApplySubCgroupPath(t *testing.T) {
	t.Parallel()

	t.Run("non directory entry is skipped", func(t *testing.T) {
		t.Parallel()
		d1 := mockDirEntry{isDir: false}
		err1 := checkAndApplySubCgroupPath(cpuAdvisorDeps{}, "path1", d1, nil)
		assert.NoError(t, err1)
	})

	t.Run("directory with negative quota is skipped", func(t *testing.T) {
		t.Parallel()
		d2 := mockDirEntry{isDir: true}
		subCPU2 := &common.CPUStats{CpuQuota: -1}
		deps := cpuAdvisorDeps{
			getCPUWithAbsolutePath: func(abs string) (*common.CPUStats, error) { return subCPU2, nil },
		}
		err2 := checkAndApplySubCgroupPath(deps, "path2", d2, nil)
		assert.NoError(t, err2)
	})

	t.Run("directory with positive quota is unlimited", func(t *testing.T) {
		t.Parallel()
		d3 := mockDirEntry{isDir: true}
		subCPU3 := &common.CPUStats{CpuQuota: 1000}
		deps := cpuAdvisorDeps{
			getCPUWithAbsolutePath:   func(abs string) (*common.CPUStats, error) { return subCPU3, nil },
			applyCPUWithAbsolutePath: func(abs string, data *common.CPUData) error { return nil },
		}
		err3 := checkAndApplySubCgroupPath(deps, "path3", d3, nil)
		assert.NoError(t, err3)
	})
}

// TestDynamicPolicy_generateBlockCPUSet verifies the block CPUSet generation logic.
// It uses a table-driven approach to test various scenarios including:
// - Two-phase allocation: Dedicated/Share blocks first, Reclaim blocks second.
// - Non-reclaimable CPU deduction: Ensuring reclaim blocks do not overlap with pinned CPUSets from resource packages marked as disable-reclaim.
// - Parallel execution: Ensuring no race conditions exist in the policy's read-only operations.
func TestDynamicPolicy_generateBlockCPUSet(t *testing.T) {
	t.Parallel()

	type testCase struct {
		name                   string
		disableReclaimSelector string
		// setupMachineState prepares the mock machine state, e.g., resource packages, existing pod allocations.
		setupMachineState func(state state.State, topo *machine.CPUTopology)
		// advisorResponse simulates the response from the CPU advisor containing blocks to be allocated.
		advisorResponse  *advisorapi.ListAndWatchResponse
		expectedError    bool
		expectedErrorStr string
		// validateResult contains custom assertions for the resulting BlockCPUSet.
		validateResult func(t *testing.T, blockCPUSet advisorapi.BlockCPUSet, topo *machine.CPUTopology)
	}

	testCases := []testCase{
		{
			// Scenario: A single reclaim block without a specific NUMA ID (FakedNUMAID).
			// It should be allocated from the global available pool minus any global non-reclaimable CPUs.
			name:                   "basic reclaim block with faked NUMA ID",
			disableReclaimSelector: "disable-reclaim=true",
			setupMachineState: func(st state.State, topo *machine.CPUTopology) {
				machineState := st.GetMachineState()
				// NUMA 0 has a non-reclaimable package using CPUs 0,1,2,3
				machineState[0].ResourcePackageStates = map[string]*state.ResourcePackageState{
					"pkg1": {
						Attributes:   map[string]string{"disable-reclaim": "true"},
						PinnedCPUSet: machine.NewCPUSet(0, 1, 2, 3),
					},
				}
				// NUMA 1 has a reclaimable package using CPUs 8,9
				machineState[1].ResourcePackageStates = map[string]*state.ResourcePackageState{
					"pkg2": {
						Attributes:   map[string]string{"disable-reclaim": "false"},
						PinnedCPUSet: machine.NewCPUSet(8, 9),
					},
				}
				st.SetMachineState(machineState, false)
			},
			advisorResponse: &advisorapi.ListAndWatchResponse{
				Entries: map[string]*advisorapi.CalculationEntries{
					"reclaim": {
						Entries: map[string]*advisorapi.CalculationInfo{
							"reclaim-entry": {
								OwnerPoolName: "reclaim",
								CalculationResultsByNumas: map[int64]*advisorapi.NumaCalculationResult{
									-1: { // FakedNUMAID
										Blocks: []*advisorapi.Block{
											{BlockId: "block-reclaim-1", Result: 4},
										},
									},
								},
							},
						},
					},
				},
			},
			expectedError: false,
			validateResult: func(t *testing.T, blockCPUSet advisorapi.BlockCPUSet, topo *machine.CPUTopology) {
				as := assert.New(t)
				res := blockCPUSet["block-reclaim-1"]
				as.Equal(4, res.Size())
				as.True(res.Intersection(machine.NewCPUSet(0, 1, 2, 3)).IsEmpty(), "reclaim block should not use non-reclaimable pinned CPUs")
			},
		},
		{
			// Scenario: Multiple NUMA-aware reclaim blocks.
			// Reclaim blocks tied to specific NUMA nodes must avoid the non-reclaimable CPUs on their respective nodes.
			name:                   "NUMA-aware reclaim block allocation",
			disableReclaimSelector: "disable-reclaim=true",
			setupMachineState: func(st state.State, topo *machine.CPUTopology) {
				machineState := st.GetMachineState()
				machineState[0].ResourcePackageStates = map[string]*state.ResourcePackageState{
					"pkg1": {
						Attributes:   map[string]string{"disable-reclaim": "true"},
						PinnedCPUSet: machine.NewCPUSet(0, 1, 2, 3), // NUMA 0
					},
				}
				machineState[1].ResourcePackageStates = map[string]*state.ResourcePackageState{
					"pkg2": {
						Attributes:   map[string]string{"disable-reclaim": "true"},
						PinnedCPUSet: machine.NewCPUSet(4, 5), // NUMA 1 (CPUs 4,5,6,7,12,13,14,15)
					},
				}
				st.SetMachineState(machineState, false)
			},
			advisorResponse: &advisorapi.ListAndWatchResponse{
				Entries: map[string]*advisorapi.CalculationEntries{
					"reclaim": {
						Entries: map[string]*advisorapi.CalculationInfo{
							"reclaim-entry": {
								OwnerPoolName: "reclaim",
								CalculationResultsByNumas: map[int64]*advisorapi.NumaCalculationResult{
									0: {
										Blocks: []*advisorapi.Block{{BlockId: "block-reclaim-numa0", Result: 2}},
									},
									1: {
										Blocks: []*advisorapi.Block{{BlockId: "block-reclaim-numa1", Result: 4}},
									},
								},
							},
						},
					},
				},
			},
			expectedError: false,
			validateResult: func(t *testing.T, blockCPUSet advisorapi.BlockCPUSet, topo *machine.CPUTopology) {
				as := assert.New(t)
				res0 := blockCPUSet["block-reclaim-numa0"]
				as.Equal(2, res0.Size())
				as.True(res0.Intersection(machine.NewCPUSet(0, 1, 2, 3)).IsEmpty())
				as.True(res0.IsSubsetOf(topo.CPUDetails.CPUsInNUMANodes(0)))

				res1 := blockCPUSet["block-reclaim-numa1"]
				as.Equal(4, res1.Size())
				as.True(res1.Intersection(machine.NewCPUSet(4, 5)).IsEmpty())
				as.True(res1.IsSubsetOf(topo.CPUDetails.CPUsInNUMANodes(1)))
			},
		},
		{
			// Scenario: Verifying two-phase allocation logic.
			// Dedicated and Share blocks should be allocated first. Then, Reclaim blocks should be allocated
			// from the remaining CPUs, while also avoiding the non-reclaimable CPUs.
			name:                   "mixed dedicated, share, and reclaim blocks",
			disableReclaimSelector: "disable-reclaim=true",
			setupMachineState: func(st state.State, topo *machine.CPUTopology) {
				// Set up a pre-allocated dedicated pod on NUMA 0
				podEntries := state.PodEntries{
					"pod-dedicated": state.ContainerEntries{
						"container-1": &state.AllocationInfo{
							AllocationResult:         machine.NewCPUSet(8, 9), // NUMA 0
							OriginalAllocationResult: machine.NewCPUSet(8, 9),
							TopologyAwareAssignments: map[int]machine.CPUSet{
								0: machine.NewCPUSet(8, 9),
							},
							AllocationMeta: commonstate.AllocationMeta{
								QoSLevel: apiconsts.PodAnnotationQoSLevelDedicatedCores,
							},
						},
					},
					"pod-shared": state.ContainerEntries{
						"container-1": &state.AllocationInfo{
							AllocationResult:         machine.NewCPUSet(2, 3, 10, 11), // NUMA 0
							OriginalAllocationResult: machine.NewCPUSet(2, 3, 10, 11),
							TopologyAwareAssignments: map[int]machine.CPUSet{
								0: machine.NewCPUSet(2, 3, 10, 11),
							},
							AllocationMeta: commonstate.AllocationMeta{
								QoSLevel: apiconsts.PodAnnotationQoSLevelSharedCores,
							},
						},
					},
				}
				st.SetPodEntries(podEntries, false)

				machineState, _ := state.GenerateMachineStateFromPodEntries(topo, podEntries, nil)
				// Add non-reclaimable package on NUMA 0 (CPUs 0, 1)
				machineState[0].ResourcePackageStates = map[string]*state.ResourcePackageState{
					"pkg1": {
						Attributes:   map[string]string{"disable-reclaim": "true"},
						PinnedCPUSet: machine.NewCPUSet(0, 1),
					},
				}
				machineState[1].ResourcePackageStates = map[string]*state.ResourcePackageState{
					"pkg2": {
						Attributes:   map[string]string{"disable-reclaim": "false"},
						PinnedCPUSet: machine.NewCPUSet(4, 5, 6, 7, 12, 13, 14, 15), // NUMA 1 (CPUs 4,5,6,7,12,13,14,15)
					},
				}
				st.SetMachineState(machineState, false)
			},
			advisorResponse: &advisorapi.ListAndWatchResponse{
				Entries: map[string]*advisorapi.CalculationEntries{
					"pod-dedicated": {
						Entries: map[string]*advisorapi.CalculationInfo{
							"container-1": {
								OwnerPoolName: "dedicated",
								CalculationResultsByNumas: map[int64]*advisorapi.NumaCalculationResult{
									0: {Blocks: []*advisorapi.Block{{BlockId: "block-dedicated-1", Result: 2}}},
								},
							},
						},
					},
					"share": {
						Entries: map[string]*advisorapi.CalculationInfo{
							"share-entry": {
								OwnerPoolName: "share",
								CalculationResultsByNumas: map[int64]*advisorapi.NumaCalculationResult{
									-1: {Blocks: []*advisorapi.Block{{BlockId: "block-share-1", Result: 4}}},
								},
							},
						},
					},
					"reclaim": {
						Entries: map[string]*advisorapi.CalculationInfo{
							"reclaim-entry": {
								OwnerPoolName: "reclaim",
								CalculationResultsByNumas: map[int64]*advisorapi.NumaCalculationResult{
									// NUMA 0 is excluded from global availableCPUs by the dedicated pod,
									// so global Reclaim (FakedNUMAID) must be allocated on NUMA 1 to succeed.
									-1: {
										Blocks: []*advisorapi.Block{
											{BlockId: "block-reclaim-1", Result: 4},
										},
									},
									// However, NUMA-aware Reclaim on NUMA 0 can still use the remaining
									// reclaimable CPUs on NUMA 0 (i.e. CPUs occupied by shared pods).
									0: {
										Blocks: []*advisorapi.Block{
											{BlockId: "block-reclaim-2", Result: 4},
										},
									},
								},
							},
						},
					},
				},
			},
			expectedError: false,
			validateResult: func(t *testing.T, blockCPUSet advisorapi.BlockCPUSet, topo *machine.CPUTopology) {
				as := assert.New(t)
				ded := blockCPUSet["block-dedicated-1"]
				as.True(ded.Equals(machine.NewCPUSet(8, 9)), "dedicated block should reuse existing allocation")

				share := blockCPUSet["block-share-1"]
				as.Equal(4, share.Size())
				as.True(share.IsSubsetOf(topo.CPUDetails.CPUsInNUMANodes(1)), "share block must be on NUMA 1 because NUMA 0 is excluded")

				rec := blockCPUSet["block-reclaim-1"]
				as.Equal(4, rec.Size())
				as.True(rec.IsSubsetOf(topo.CPUDetails.CPUsInNUMANodes(1)), "reclaim block must be on NUMA 1")
				as.True(rec.Intersection(share).IsEmpty(), "reclaim block must avoid share CPUs")

				rec2 := blockCPUSet["block-reclaim-2"]
				as.Equal(4, rec2.Size())
				as.True(rec2.IsSubsetOf(topo.CPUDetails.CPUsInNUMANodes(0)), "reclaim block must be on NUMA 0")
				as.True(rec2.Intersection(ded).IsEmpty(), "reclaim block must avoid dedicated CPUs")
				as.True(rec2.Intersection(machine.NewCPUSet(0, 1)).IsEmpty(), "reclaim block must avoid non-reclaimable pkg CPUs on NUMA 0")
			},
		},
		{
			// Scenario: Verifying priority of isolation pool over normal share pool.
			// Isolation pools (starts with "isolation") should be in Phase 1,
			// while normal share pools should be in Phase 2.
			name: "isolation pool priority over normal share pool",
			setupMachineState: func(st state.State, topo *machine.CPUTopology) {
				// No special machine state needed
			},
			advisorResponse: &advisorapi.ListAndWatchResponse{
				Entries: map[string]*advisorapi.CalculationEntries{
					"isolation": {
						Entries: map[string]*advisorapi.CalculationInfo{
							"isolation-entry": {
								OwnerPoolName: "isolation-1",
								CalculationResultsByNumas: map[int64]*advisorapi.NumaCalculationResult{
									0: {Blocks: []*advisorapi.Block{{BlockId: "block-isolation-1", Result: 8}}},
								},
							},
						},
					},
					"share": {
						Entries: map[string]*advisorapi.CalculationInfo{
							"share-entry": {
								OwnerPoolName: "share",
								CalculationResultsByNumas: map[int64]*advisorapi.NumaCalculationResult{
									-1: {Blocks: []*advisorapi.Block{{BlockId: "block-share-1", Result: 2}}},
								},
							},
						},
					},
				},
			},
			expectedError: false,
			validateResult: func(t *testing.T, blockCPUSet advisorapi.BlockCPUSet, topo *machine.CPUTopology) {
				as := assert.New(t)
				// NUMA 0 has 8 CPUs (0,1,2,3, 8,9,10,11)
				iso := blockCPUSet["block-isolation-1"]
				as.Equal(8, iso.Size())
				as.True(iso.IsSubsetOf(topo.CPUDetails.CPUsInNUMANodes(0)))

				// share block should be allocated from remaining CPUs on the node or globally.
				// Since NUMA 0 is exhausted by isolation, it must be allocated from NUMA 1.
				share := blockCPUSet["block-share-1"]
				as.Equal(2, share.Size())
				as.True(share.Intersection(iso).IsEmpty())
				as.True(share.IsSubsetOf(topo.CPUDetails.CPUsInNUMANodes(1)))
			},
		},
		{
			// Scenario: Verifying that share block (normal) now avoids only globalNonReclaimableCPUSet.
			// Previously it avoided allPinnedCPUSets.
			name:                   "share block can use reclaimable pinned CPUs",
			disableReclaimSelector: "disable-reclaim=true",
			setupMachineState: func(st state.State, topo *machine.CPUTopology) {
				machineState := st.GetMachineState()
				// pkg1 is reclaimable (disable-reclaim=false)
				machineState[0].ResourcePackageStates = map[string]*state.ResourcePackageState{
					"pkg1": {
						Attributes:   map[string]string{"disable-reclaim": "false"},
						PinnedCPUSet: machine.NewCPUSet(0, 1, 2, 3),
					},
				}
				st.SetMachineState(machineState, false)
			},
			advisorResponse: &advisorapi.ListAndWatchResponse{
				Entries: map[string]*advisorapi.CalculationEntries{
					"share": {
						Entries: map[string]*advisorapi.CalculationInfo{
							"share-entry": {
								OwnerPoolName: "share",
								CalculationResultsByNumas: map[int64]*advisorapi.NumaCalculationResult{
									-1: {Blocks: []*advisorapi.Block{{BlockId: "block-share-1", Result: 2}}},
								},
							},
						},
					},
				},
			},
			expectedError: false,
			validateResult: func(t *testing.T, blockCPUSet advisorapi.BlockCPUSet, topo *machine.CPUTopology) {
				as := assert.New(t)
				share := blockCPUSet["block-share-1"]
				as.Equal(2, share.Size())
				// Since pkg1 is reclaimable, and share is now in Phase 2 (treated like reclaim),
				// it is NOT excluded from the available pool. The CPU allocator picks CPUs
				// in numerical order, so it will allocate CPUs 0 and 1, which overlap with pkg1.
				as.True(share.Intersection(machine.NewCPUSet(0, 1, 2, 3)).Size() > 0, "share block should be able to use reclaimable pinned CPUs")
			},
		},
		{
			// Scenario: Verifying NUMA-binding share pool priority.
			name: "NUMA-binding share pool priority",
			setupMachineState: func(st state.State, topo *machine.CPUTopology) {
				// No special machine state needed
			},
			advisorResponse: &advisorapi.ListAndWatchResponse{
				Entries: map[string]*advisorapi.CalculationEntries{
					"share-NUMA": {
						Entries: map[string]*advisorapi.CalculationInfo{
							"share-entry": {
								OwnerPoolName: "share-NUMA0",
								CalculationResultsByNumas: map[int64]*advisorapi.NumaCalculationResult{
									0: {Blocks: []*advisorapi.Block{{BlockId: "block-share-numa-1", Result: 8}}},
								},
							},
						},
					},
					"share-normal": {
						Entries: map[string]*advisorapi.CalculationInfo{
							"share-entry": {
								OwnerPoolName: "share",
								CalculationResultsByNumas: map[int64]*advisorapi.NumaCalculationResult{
									-1: {Blocks: []*advisorapi.Block{{BlockId: "block-share-normal-1", Result: 2}}},
								},
							},
						},
					},
				},
			},
			expectedError: false,
			validateResult: func(t *testing.T, blockCPUSet advisorapi.BlockCPUSet, topo *machine.CPUTopology) {
				as := assert.New(t)
				// NUMA 0 has 8 CPUs.
				shareNuma := blockCPUSet["block-share-numa-1"]
				as.Equal(8, shareNuma.Size())
				as.True(shareNuma.IsSubsetOf(topo.CPUDetails.CPUsInNUMANodes(0)))

				// normal share should be pushed to Phase 2 and find CPUs elsewhere.
				shareNormal := blockCPUSet["block-share-normal-1"]
				as.Equal(2, shareNormal.Size())
				as.True(shareNormal.Intersection(shareNuma).IsEmpty())
				as.True(shareNormal.IsSubsetOf(topo.CPUDetails.CPUsInNUMANodes(1)))
			},
		},
		{
			// Scenario: Verifying that NUMA-binding pod excludes the whole NUMA node from global available pool.
			name: "NUMA-binding pod excludes NUMA node from global pool",
			setupMachineState: func(st state.State, topo *machine.CPUTopology) {
				// Dedicated pod must be present in state for allocateDedicatedBlocks to work
				podEntries := state.PodEntries{
					"dedicated": state.ContainerEntries{
						"dedicated-entry": &state.AllocationInfo{
							AllocationResult:         machine.NewCPUSet(0, 1, 2, 3),
							OriginalAllocationResult: machine.NewCPUSet(0, 1, 2, 3),
							TopologyAwareAssignments: map[int]machine.CPUSet{
								0: machine.NewCPUSet(0, 1, 2, 3),
							},
							AllocationMeta: commonstate.AllocationMeta{
								QoSLevel: apiconsts.PodAnnotationQoSLevelDedicatedCores,
							},
						},
					},
				}
				st.SetPodEntries(podEntries, false)
			},
			advisorResponse: &advisorapi.ListAndWatchResponse{
				Entries: map[string]*advisorapi.CalculationEntries{
					"dedicated": {
						Entries: map[string]*advisorapi.CalculationInfo{
							"dedicated-entry": {
								OwnerPoolName: "dedicated",
								CalculationResultsByNumas: map[int64]*advisorapi.NumaCalculationResult{
									0: {Blocks: []*advisorapi.Block{{BlockId: "block-dedicated-1", Result: 4}}},
								},
							},
						},
					},
					"share": {
						Entries: map[string]*advisorapi.CalculationInfo{
							"share-entry": {
								OwnerPoolName: "share",
								CalculationResultsByNumas: map[int64]*advisorapi.NumaCalculationResult{
									-1: {Blocks: []*advisorapi.Block{{BlockId: "block-share-1", Result: 6}}},
								},
							},
						},
					},
				},
			},
			expectedError: false,
			validateResult: func(t *testing.T, blockCPUSet advisorapi.BlockCPUSet, topo *machine.CPUTopology) {
				as := assert.New(t)
				// NUMA 0 has 8 CPUs. Dedicated takes 4.
				ded := blockCPUSet["block-dedicated-1"]
				as.Equal(4, ded.Size())
				as.True(ded.IsSubsetOf(topo.CPUDetails.CPUsInNUMANodes(0)))

				// The remaining 4 CPUs on NUMA 0 should be excluded from global available pool
				// because NUMA 0 now has a NUMA-binding pod.
				// share block (non-NUMA aware) needs 6 CPUs.
				// If it could use the remaining 4 on NUMA 0, it might take 4 from NUMA 0 and 2 from NUMA 1.
				// But since NUMA 0 is excluded, it MUST take all 6 from NUMA 1.
				share := blockCPUSet["block-share-1"]
				as.Equal(6, share.Size())
				as.True(share.IsSubsetOf(topo.CPUDetails.CPUsInNUMANodes(1)), "share block should be entirely on NUMA 1")
			},
		},
		{
			// Scenario: Verifying that dedicated pod returns error if its size is same but some CPUs are unavailable.
			name: "dedicated pod size same but CPUs unavailable returns error",
			setupMachineState: func(st state.State, topo *machine.CPUTopology) {
				// Dedicated pod on CPUs 0,1,2,3
				podEntries := state.PodEntries{
					"dedicated": state.ContainerEntries{
						"dedicated-entry": &state.AllocationInfo{
							AllocationResult:         machine.NewCPUSet(0, 1, 2, 3),
							OriginalAllocationResult: machine.NewCPUSet(0, 1, 2, 3),
							TopologyAwareAssignments: map[int]machine.CPUSet{
								0: machine.NewCPUSet(0, 1, 2, 3),
							},
							AllocationMeta: commonstate.AllocationMeta{
								QoSLevel: apiconsts.PodAnnotationQoSLevelDedicatedCores,
							},
						},
					},
					"reserve": state.ContainerEntries{
						"": &state.AllocationInfo{
							AllocationResult: machine.NewCPUSet(0),
						},
					},
				}
				st.SetPodEntries(podEntries, false)
			},
			advisorResponse: &advisorapi.ListAndWatchResponse{
				Entries: map[string]*advisorapi.CalculationEntries{
					"dedicated": {
						Entries: map[string]*advisorapi.CalculationInfo{
							"dedicated-entry": {
								OwnerPoolName: "dedicated",
								CalculationResultsByNumas: map[int64]*advisorapi.NumaCalculationResult{
									0: {Blocks: []*advisorapi.Block{{BlockId: "block-dedicated-1", Result: 4}}},
								},
							},
						},
					},
					"reserve": {
						Entries: map[string]*advisorapi.CalculationInfo{
							"": {
								OwnerPoolName: "reserve",
								CalculationResultsByNumas: map[int64]*advisorapi.NumaCalculationResult{
									-1: {Blocks: []*advisorapi.Block{{BlockId: "block-reserve", Result: 1}}},
								},
							},
						},
					},
				},
			},
			expectedError:    true,
			expectedErrorStr: "size not changed, but some CPUs are not available",
		},
		{
			// Scenario: Exhaustion of CPUs for reclaim.
			// After deducting dedicated, share, and non-reclaimable CPUs, if there are not enough
			// CPUs left for a reclaim block, an error should be returned.
			name:                   "not enough CPUs for reclaim block after deducting non-reclaimable",
			disableReclaimSelector: "disable-reclaim=true",
			setupMachineState: func(st state.State, topo *machine.CPUTopology) {
				machineState := st.GetMachineState()
				// NUMA 0 has 8 CPUs (0,1,2,3, 8,9,10,11). We pin 7 of them as non-reclaimable.
				machineState[0].ResourcePackageStates = map[string]*state.ResourcePackageState{
					"pkg1": {
						Attributes:   map[string]string{"disable-reclaim": "true"},
						PinnedCPUSet: machine.NewCPUSet(0, 1, 2, 3, 8, 9, 10), // only CPU 11 is available
					},
				}
				st.SetMachineState(machineState, false)
			},
			advisorResponse: &advisorapi.ListAndWatchResponse{
				Entries: map[string]*advisorapi.CalculationEntries{
					"reclaim": {
						Entries: map[string]*advisorapi.CalculationInfo{
							"reclaim-entry": {
								OwnerPoolName: "reclaim",
								CalculationResultsByNumas: map[int64]*advisorapi.NumaCalculationResult{
									0: {Blocks: []*advisorapi.Block{{BlockId: "block-reclaim-1", Result: 2}}}, // Requests 2, but only 1 available
								},
							},
						},
					},
				},
			},
			expectedError:    true,
			expectedErrorStr: "allocate cpuset for NUMA Aware reclaim block: block-reclaim-1 in NUMA: 0 failed",
			validateResult: func(t *testing.T, blockCPUSet advisorapi.BlockCPUSet, topo *machine.CPUTopology) {
				// No validation needed if error is expected
			},
		},
		{
			name:                   "test invalid disable reclaim selector",
			disableReclaimSelector: "disable-reclaim=true,,invalid",
			expectedError:          true,
		},
	}

	for _, tc := range testCases {
		tc := tc // capture range variable for parallel execution
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			as := assert.New(t)

			// Initialize a clean topology for each parallel test (16 CPUs, 2 Sockets, 2 NUMA nodes)
			// NUMA 0: 0, 1, 2, 3, 8, 9, 10, 11
			// NUMA 1: 4, 5, 6, 7, 12, 13, 14, 15
			topo, err := machine.GenerateDummyCPUTopology(16, 2, 2)
			as.NoError(err)

			conf := generateTestConfiguration(t, "", "")
			if tc.disableReclaimSelector != "" {
				conf.GetDynamicConfiguration().DisableReclaimPinnedCPUSetResourcePackageSelector = tc.disableReclaimSelector
			}

			// Strict isolation using a fresh temp directory
			st, _ := state.NewCheckpointState(&statedirectory.StateDirectoryConfiguration{StateFileDirectory: t.TempDir()}, "test", "test", topo, false, state.GenerateMachineStateFromPodEntries, metrics.DummyMetrics{})

			// Prepare initial machine state (e.g. static pools)
			machineState, _ := state.GenerateMachineStateFromPodEntries(topo, nil, nil)
			st.SetMachineState(machineState, false)

			if tc.setupMachineState != nil {
				tc.setupMachineState(st, topo)
			}

			policy := &DynamicPolicy{
				machineInfo: &machine.KatalystMachineInfo{
					CPUTopology: topo,
				},
				state: st,
				conf:  conf,
			}

			blockCPUSet, err := policy.generateBlockCPUSet(tc.advisorResponse)
			if tc.expectedError {
				as.Error(err)
				if tc.expectedErrorStr != "" {
					as.Contains(err.Error(), tc.expectedErrorStr)
				}
				return
			}
			as.NoError(err)
			if tc.validateResult != nil {
				tc.validateResult(t, blockCPUSet, topo)
			}
		})
	}
}

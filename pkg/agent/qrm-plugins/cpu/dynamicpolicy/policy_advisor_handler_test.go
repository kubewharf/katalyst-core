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

	"github.com/bytedance/mockey"
	"github.com/smartystreets/goconvey/convey"
	"github.com/stretchr/testify/assert"
	v1 "k8s.io/api/core/v1"
	resource2 "k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/labels"

	apiconsts "github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/advisorsvc"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	advisorapi "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/cpuadvisor"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/qrm/statedirectory"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/pod"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/cgroup/common"
	cgroupmgr "github.com/kubewharf/katalyst-core/pkg/util/cgroup/manager"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
	"github.com/kubewharf/katalyst-core/pkg/util/native"
)

var advisorTestMutex = &sync.Mutex{}

func TestDynamicPolicy_checkAndApplyIfCgroupV1(t *testing.T) {
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

	mockPodPathMap := map[string]*v1.Pod{
		"test-pod-1": mockPod,
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

	p := &DynamicPolicy{
		metaServer: &metaserver.MetaServer{
			MetaAgent: &agent.MetaAgent{
				PodFetcher: &pod.PodFetcherStub{},
			},
		},
	}

	advisorTestMutex.Lock()
	defer advisorTestMutex.Unlock()

	mockey.PatchConvey("test cgroup v1 resource", t, func() {
		mockey.Mock(common.CheckCgroup2UnifiedMode).IncludeCurrentGoRoutine().Return(false).Build()
		mockey.Mock(cgroupmgr.GetCPUWithRelativePath).IncludeCurrentGoRoutine().Return(mockBG, nil).Build()
		mockey.Mock((*DynamicPolicy).getCurrentPathAllPodsDirAndMap).IncludeCurrentGoRoutine().Return(mockPodPathMap, []string{"advisor-test-pod-1"}, nil).Build()
		mockey.Mock((*DynamicPolicy).getPodAndRelativePath).IncludeCurrentGoRoutine().Return(mockPod, "test_relative_path", nil).Build()
		mockey.Mock((*DynamicPolicy).checkAndApplyAllPodsQuota).IncludeCurrentGoRoutine().Return(nil).Build()

		err := p.checkAndApplyIfCgroupV1(mockCal, resources)
		convey.So(err, convey.ShouldBeNil)
	})

	mockey.PatchConvey("test cgroup v1 resource 2", t, func() {
		mockey.Mock(common.CheckCgroup2UnifiedMode).IncludeCurrentGoRoutine().Return(false).Build()
		mockey.Mock(cgroupmgr.GetCPUWithRelativePath).IncludeCurrentGoRoutine().Return(mockBG2, nil).Build()
		mockey.Mock((*DynamicPolicy).getCurrentPathAllPodsDirAndMap).IncludeCurrentGoRoutine().Return(mockPodPathMap, []string{"advisor-test-pod-1"}, nil).Build()
		mockey.Mock((*DynamicPolicy).getPodAndRelativePath).IncludeCurrentGoRoutine().Return(mockPod, "test_relative_path", nil).Build()
		mockey.Mock((*DynamicPolicy).checkAndApplyAllPodsQuota).IncludeCurrentGoRoutine().Return(nil).Build()

		err := p.checkAndApplyIfCgroupV1(mockCal, resources)
		convey.So(err, convey.ShouldBeNil)
	})
}

func TestDynamicPolicy_getAllDirs(t *testing.T) {
	t.Parallel()

	policy := &DynamicPolicy{}

	t.Run("basic path", func(t *testing.T) {
		t.Parallel()

		advisorTestMutex.Lock()
		defer advisorTestMutex.Unlock()
		_ = mockey.Mock(os.ReadDir).IncludeCurrentGoRoutine().To(func(dirname string) ([]os.DirEntry, error) {
			return []os.DirEntry{
				mockDirEntry{"foo", true},
				mockDirEntry{"bar", true},
			}, nil
		}).Build()

		dirs, err := policy.getAllDirs("/fake/path")
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

	advisorTestMutex.Lock()
	defer advisorTestMutex.Unlock()

	mockey.PatchConvey("test getCurrentPathAllPodsDirAndMap", t, func() {
		mockPodPathMap := map[string]*v1.Pod{
			"test-pod-1": {
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
		mockey.Mock((*DynamicPolicy).getAllPodsPathMap).IncludeCurrentGoRoutine().Return(mockPodPathMap, nil).Build()
		mockey.Mock((*DynamicPolicy).getAllDirs).IncludeCurrentGoRoutine().Return([]string{"advisor-test-pod-1"}, nil).Build()

		p := &DynamicPolicy{
			metaServer: &metaserver.MetaServer{
				MetaAgent: &agent.MetaAgent{
					PodFetcher: &pod.PodFetcherStub{},
				},
			},
		}

		resultMap, dirs, err := p.getCurrentPathAllPodsDirAndMap("test_group_path")
		convey.So(err, convey.ShouldBeNil)
		convey.So(resultMap, convey.ShouldNotBeNil)
		convey.So(dirs, convey.ShouldNotBeNil)
	})
}

func TestDynamicPolicy_getPodAndRelativePath(t *testing.T) {
	t.Parallel()

	advisorTestMutex.Lock()
	defer advisorTestMutex.Unlock()

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

	p := &DynamicPolicy{
		metaServer: &metaserver.MetaServer{
			MetaAgent: &agent.MetaAgent{
				PodFetcher: &pod.PodFetcherStub{},
			},
		},
	}

	mockey.PatchConvey("test getPodAndRelativePath", t, func() {
		_, _, err := p.getPodAndRelativePath(currentPath, dirs, podPathMap)
		convey.So(err, convey.ShouldBeNil)
	})
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

	p := &DynamicPolicy{
		metaServer: &metaserver.MetaServer{
			MetaAgent: &agent.MetaAgent{
				PodFetcher: &pod.PodFetcherStub{},
			},
		},
	}

	advisorTestMutex.Lock()
	defer advisorTestMutex.Unlock()
	mockey.PatchConvey("test getAllPodsPathMap", t, func() {
		mockey.Mock((*pod.PodFetcherStub).GetPodList).IncludeCurrentGoRoutine().Return(mockPods, nil).Build()
		mockey.Mock(common.GetPodAbsCgroupPath).IncludeCurrentGoRoutine().Return("test-pod-1-path", nil).Build()

		podPathMap, err := p.getAllPodsPathMap()

		convey.So(err, convey.ShouldBeNil)
		convey.So(len(podPathMap), convey.ShouldEqual, len(mockPods))
		convey.So(podPathMap["test-pod-1-path"], convey.ShouldEqual, mockPods[0])
	})
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

	p := &DynamicPolicy{
		metaServer: &metaserver.MetaServer{
			MetaAgent: &agent.MetaAgent{
				PodFetcher: &pod.PodFetcherStub{},
			},
		},
	}

	advisorTestMutex.Lock()
	defer advisorTestMutex.Unlock()
	mockey.PatchConvey("test getAllContainersRelativePathMap", t, func() {
		mockey.Mock(native.GetContainerID).IncludeCurrentGoRoutine().Return("test-container-ID", nil).Build()
		mockey.Mock(common.GetContainerRelativeCgroupPath).IncludeCurrentGoRoutine().Return("test-container-relative-path", nil).Build()

		testMap := p.getAllContainersRelativePathMap(mockPod)

		convey.So(len(testMap), convey.ShouldEqual, 1)
		convey.So(testMap["test-container-relative-path"].Name, convey.ShouldEqual, mockPod.Spec.Containers[0].Name)
	})
}

func TestDynamicPolicy_checkAllPodsQuota(t *testing.T) {
	t.Parallel()

	p := &DynamicPolicy{
		metaServer: &metaserver.MetaServer{
			MetaAgent: &agent.MetaAgent{
				PodFetcher: &pod.PodFetcherStub{},
			},
		},
	}

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

	advisorTestMutex.Lock()
	defer advisorTestMutex.Unlock()
	mockey.PatchConvey("test checkAndApplyAllPodsQuota", t, func() {
		mockey.Mock((*DynamicPolicy).getCurrentPathAllPodsDirAndMap).IncludeCurrentGoRoutine().Return(mockPodPathMap, mockPodDirs, nil).Build()
		mockey.Mock((*DynamicPolicy).getPodAndRelativePath).IncludeCurrentGoRoutine().Return(mockPod, "test_relative_path", nil).Build()
		mockey.Mock((*DynamicPolicy).applyAllContainersQuota).IncludeCurrentGoRoutine().Return(nil).Build()
		mockey.Mock(cgroupmgr.ApplyCPUWithRelativePath).IncludeCurrentGoRoutine().Return(nil).Build()
		mockey.Mock(cgroupmgr.GetCPUWithRelativePath).IncludeCurrentGoRoutine().Return(mockBG, nil).Build()

		err := p.checkAndApplyAllPodsQuota(mockCal, mockBG.CpuQuota)
		convey.So(err, convey.ShouldBeNil)

		err = p.checkAndApplyAllPodsQuota(mockCal, mockBG2.CpuQuota)
		convey.So(err, convey.ShouldBeNil)

		err = p.checkAndApplyAllPodsQuota(mockCal, mockBG3.CpuQuota)
		convey.So(err, convey.ShouldBeNil)
	})

	mockey.PatchConvey("test checkAndApplyAllPodsQuota", t, func() {
		mockey.Mock((*DynamicPolicy).getCurrentPathAllPodsDirAndMap).IncludeCurrentGoRoutine().Return(nil, nil, mockErr).Build()
		err := p.checkAndApplyAllPodsQuota(mockCal, mockBG.CpuQuota)
		convey.So(err, convey.ShouldNotBeNil)
	})

	mockey.PatchConvey("test checkAndApplyAllPodsQuota", t, func() {
		mockey.Mock((*DynamicPolicy).getCurrentPathAllPodsDirAndMap).IncludeCurrentGoRoutine().Return(mockPodPathMap, mockPodDirs, nil).Build()
		mockey.Mock((*DynamicPolicy).getPodAndRelativePath).IncludeCurrentGoRoutine().Return(mockPod, "test_relative_path", mockErr).Build()
		err := p.checkAndApplyAllPodsQuota(mockCal, mockBG.CpuQuota)
		convey.So(err, convey.ShouldBeNil)
	})

	mockey.PatchConvey("test checkAndApplyAllPodsQuota", t, func() {
		mockey.Mock((*DynamicPolicy).getCurrentPathAllPodsDirAndMap).IncludeCurrentGoRoutine().Return(mockPodPathMap, mockPodDirs, nil).Build()
		mockey.Mock((*DynamicPolicy).getPodAndRelativePath).IncludeCurrentGoRoutine().Return(mockPod, "test_relative_path", nil).Build()
		mockey.Mock(cgroupmgr.GetCPUWithRelativePath).IncludeCurrentGoRoutine().Return(mockBG, mockErr).Build()
		err := p.checkAndApplyAllPodsQuota(mockCal, mockBG.CpuQuota)
		convey.So(err, convey.ShouldNotBeNil)
	})

	mockey.PatchConvey("test checkAndApplyAllPodsQuota", t, func() {
		mockey.Mock((*DynamicPolicy).getCurrentPathAllPodsDirAndMap).IncludeCurrentGoRoutine().Return(mockPodPathMap, mockPodDirs, nil).Build()
		mockey.Mock((*DynamicPolicy).getPodAndRelativePath).IncludeCurrentGoRoutine().Return(mockPod, "test_relative_path", nil).Build()
		mockey.Mock((*DynamicPolicy).applyAllContainersQuota).IncludeCurrentGoRoutine().Return(mockErr).Build()
		mockey.Mock(cgroupmgr.ApplyCPUWithRelativePath).IncludeCurrentGoRoutine().Return(nil).Build()
		mockey.Mock(cgroupmgr.GetCPUWithRelativePath).IncludeCurrentGoRoutine().Return(mockBG, nil).Build()

		err := p.checkAndApplyAllPodsQuota(mockCal, mockBG.CpuQuota)
		convey.So(err, convey.ShouldBeNil)

		err = p.checkAndApplyAllPodsQuota(mockCal, mockBG2.CpuQuota)
		convey.So(err, convey.ShouldBeNil)

		err = p.checkAndApplyAllPodsQuota(mockCal, mockBG3.CpuQuota)
		convey.So(err, convey.ShouldBeNil)
	})

	mockey.PatchConvey("test checkAndApplyAllPodsQuota", t, func() {
		mockey.Mock((*DynamicPolicy).getCurrentPathAllPodsDirAndMap).IncludeCurrentGoRoutine().Return(mockPodPathMap, mockPodDirs, nil).Build()
		mockey.Mock((*DynamicPolicy).getPodAndRelativePath).IncludeCurrentGoRoutine().Return(mockPod, "test_relative_path", nil).Build()
		mockey.Mock((*DynamicPolicy).applyAllContainersQuota).IncludeCurrentGoRoutine().Return(nil).Build()
		mockey.Mock(cgroupmgr.ApplyCPUWithRelativePath).IncludeCurrentGoRoutine().Return(mockErr).Build()
		mockey.Mock(cgroupmgr.GetCPUWithRelativePath).IncludeCurrentGoRoutine().Return(mockBG, nil).Build()

		err := p.checkAndApplyAllPodsQuota(mockCal, mockBG.CpuQuota)
		convey.So(err, convey.ShouldNotBeNil)

		err = p.checkAndApplyAllPodsQuota(mockCal, mockBG2.CpuQuota)
		convey.So(err, convey.ShouldNotBeNil)

		err = p.checkAndApplyAllPodsQuota(mockCal, mockBG3.CpuQuota)
		convey.So(err, convey.ShouldNotBeNil)
	})
}

func TestDynamicPolicy_applyAllContainersQuota(t *testing.T) {
	t.Parallel()

	p := &DynamicPolicy{
		metaServer: &metaserver.MetaServer{
			MetaAgent: &agent.MetaAgent{
				PodFetcher: &pod.PodFetcherStub{},
			},
		},
	}

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

	advisorTestMutex.Lock()
	defer advisorTestMutex.Unlock()
	mockey.PatchConvey("test checkAllContainersQuota", t, func() {
		mockey.Mock((*DynamicPolicy).getAllContainersRelativePathMap).IncludeCurrentGoRoutine().Return(containerPathMap).Build()
		mockey.Mock((*DynamicPolicy).applyAllSubCgroupQuotaToUnLimit).IncludeCurrentGoRoutine().Return(nil).Build()
		mockey.Mock(cgroupmgr.GetCPUWithRelativePath).IncludeCurrentGoRoutine().Return(mockCPU, nil).Build()
		apply := mockey.Mock(cgroupmgr.ApplyCPUWithRelativePath).IncludeCurrentGoRoutine().Return(nil).Build()

		err := p.applyAllContainersQuota(pod, true)

		convey.So(err, convey.ShouldBeNil)
		convey.So(apply.Times(), convey.ShouldEqual, 2)

		err = p.applyAllContainersQuota(pod, false)
		convey.So(err, convey.ShouldBeNil)
	})
}

func TestDynamicPolicy_checkAndApplySubCgroupPath(t *testing.T) {
	t.Parallel()

	p := &DynamicPolicy{
		metaServer: &metaserver.MetaServer{
			MetaAgent: &agent.MetaAgent{
				PodFetcher: &pod.PodFetcherStub{},
			},
		},
	}

	advisorTestMutex.Lock()
	defer advisorTestMutex.Unlock()
	mockey.PatchConvey("test checkAndApplySubCgroupPath", t, func() {
		d1 := mockDirEntry{isDir: false}
		err1 := p.checkAndApplySubCgroupPath("path1", d1, nil)
		convey.So(err1, convey.ShouldBeNil)

		d2 := mockDirEntry{isDir: true}
		subCPU2 := &common.CPUStats{CpuQuota: -1}
		mockey.Mock(cgroupmgr.GetCPUWithAbsolutePath).IncludeCurrentGoRoutine().Return(subCPU2, nil).Build()
		err2 := p.checkAndApplySubCgroupPath("path2", d2, nil)
		convey.So(err2, convey.ShouldBeNil)
	})

	mockey.PatchConvey("test checkAndApplySubCgroupPath", t, func() {
		d3 := mockDirEntry{isDir: true}
		subCPU3 := &common.CPUStats{CpuQuota: 1000}
		mockey.Mock(cgroupmgr.GetCPUWithAbsolutePath).IncludeCurrentGoRoutine().Return(subCPU3, nil).Build()
		mockey.Mock(cgroupmgr.ApplyCPUWithAbsolutePath).IncludeCurrentGoRoutine().Return(nil).Build()
		err3 := p.checkAndApplySubCgroupPath("path3", d3, nil)
		convey.So(err3, convey.ShouldBeNil)
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
		advisorResponse *advisorapi.ListAndWatchResponse
		expectedError   bool
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
				st.SetMachineState(machineState, false)
			},
			advisorResponse: &advisorapi.ListAndWatchResponse{
				Entries: map[string]*advisorapi.CalculationEntries{
					"pod-dedicated": {
						Entries: map[string]*advisorapi.CalculationInfo{
							"container-1": {
								OwnerPoolName: "dedicated",
								CalculationResultsByNumas: map[int64]*advisorapi.NumaCalculationResult{
									0: {Blocks: []*advisorapi.Block{{BlockId: "container-1", Result: 2}}},
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
					"reclaim": {
						Entries: map[string]*advisorapi.CalculationInfo{
							"reclaim-entry": {
								OwnerPoolName: "reclaim",
								CalculationResultsByNumas: map[int64]*advisorapi.NumaCalculationResult{
									0: {Blocks: []*advisorapi.Block{{BlockId: "block-reclaim-1", Result: 2}}},
								},
							},
						},
					},
				},
			},
			expectedError: false,
			validateResult: func(t *testing.T, blockCPUSet advisorapi.BlockCPUSet, topo *machine.CPUTopology) {
				as := assert.New(t)
				ded := blockCPUSet["container-1"]
				as.True(ded.Equals(machine.NewCPUSet(8, 9)), "dedicated block should reuse existing allocation")

				share := blockCPUSet["block-share-1"]
				as.Equal(2, share.Size())

				rec := blockCPUSet["block-reclaim-1"]
				as.Equal(2, rec.Size())
				as.True(rec.Intersection(machine.NewCPUSet(0, 1)).IsEmpty(), "reclaim block must avoid non-reclaimable pkg CPUs")
				as.True(rec.Intersection(machine.NewCPUSet(8, 9)).IsEmpty(), "reclaim block must avoid dedicated CPUs")
				as.True(rec.Intersection(share).IsEmpty(), "reclaim block must avoid share CPUs")
			},
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
			expectedError: true,
			validateResult: func(t *testing.T, blockCPUSet advisorapi.BlockCPUSet, topo *machine.CPUTopology) {
				// No validation needed if error is expected
			},
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
				selector, _ := labels.Parse(tc.disableReclaimSelector)
				conf.GetDynamicConfiguration().DisableReclaimPinnedCPUSetResourcePackageSelector = selector
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
			} else {
				as.NoError(err)
				if tc.validateResult != nil {
					tc.validateResult(t, blockCPUSet, topo)
				}
			}
		})
	}
}

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
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/bytedance/mockey"
	"github.com/smartystreets/goconvey/convey"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	resource2 "k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	apiconsts "github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/advisorsvc"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	advisorapi "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/cpuadvisor"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	cpusetutil "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/util"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/util"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/qrm/statedirectory"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/pod"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	cgroupclient "github.com/kubewharf/katalyst-core/pkg/util/cgroup/client"
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

func TestDynamicPolicy_syncAdvisorOverlapFlags_PersistsDisableDedicatedCoresFlag(t *testing.T) {
	topo, err := machine.GenerateDummyCPUTopology(4, 1, 1)
	assert.NoError(t, err)

	stateDirectoryConfig := &statedirectory.StateDirectoryConfiguration{
		StateFileDirectory: t.TempDir(),
	}
	st, err := state.NewCheckpointState(
		stateDirectoryConfig, "test", "test", topo, false, state.GenerateMachineStateFromPodEntries, metrics.DummyMetrics{},
	)
	assert.NoError(t, err)

	policy := &DynamicPolicy{state: st}
	policy.syncAdvisorOverlapFlags(&advisorapi.ListAndWatchResponse{
		DisableDedicatedCoresOverlapReclaimedCores: true,
	})

	assert.True(t, st.GetDisableDedicatedCoresOverlapReclaimedCores())
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
			expectedErrorStr: "insufficient CPUs for NUMA-aware reclaim block: numa id: 0, requested: 2, available: 1",
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

func TestDynamicPolicy_generateReclaimBlockCPUSet_NUMAAwareInsufficientCPUsReturnsDiagnostic(t *testing.T) {
	topology, err := machine.GenerateDummyCPUTopology(16, 2, 2)
	require.NoError(t, err)

	policy, err := getTestDynamicPolicyWithInitialization(topology, t.TempDir())
	require.NoError(t, err)
	blockCPUSet := advisorapi.BlockCPUSet{}

	err = policy.generateReclaimBlockCPUSet(
		map[int][]*advisorapi.BlockInfo{
			0: {{
				Block: advisorapi.Block{
					BlockId: "reclaim-numa-0",
					Result:  2,
				},
			}},
		},
		machine.NewCPUSet(0),
		machine.NewCPUSet(0),
		machine.NewCPUSet(),
		blockCPUSet,
	)

	require.Error(t, err)
	require.ErrorContains(t, err, "numa id: 0")
	require.ErrorContains(t, err, "requested: 2")
	require.ErrorContains(t, err, "available: 1")
	require.Empty(t, blockCPUSet)
}

func TestDynamicPolicyPlanBlocks(t *testing.T) {
	topo, err := machine.GenerateDummyCPUTopology(8, 1, 1)
	require.NoError(t, err)

	policy, err := getTestDynamicPolicyWithInitialization(topo, t.TempDir())
	require.NoError(t, err)

	entries := policy.state.GetPodEntries()
	entries["pod-dedicated"] = state.ContainerEntries{
		"container": {
			AllocationResult:         machine.NewCPUSet(0, 1),
			OriginalAllocationResult: machine.NewCPUSet(0, 1),
			TopologyAwareAssignments: map[int]machine.CPUSet{0: machine.NewCPUSet(0, 1)},
			AllocationMeta: commonstate.AllocationMeta{
				PodUid:        "pod-dedicated",
				ContainerName: "container",
				ContainerType: pluginapi.ContainerType_MAIN.String(),
				QoSLevel:      apiconsts.PodAnnotationQoSLevelDedicatedCores,
			},
		},
	}
	policy.state.SetPodEntries(entries, false)

	resp := &advisorapi.ListAndWatchResponse{
		Entries: map[string]*advisorapi.CalculationEntries{
			"pod-dedicated": {
				Entries: map[string]*advisorapi.CalculationInfo{
					"container": {
						OwnerPoolName: commonstate.PoolNameDedicated,
						CalculationResultsByNumas: map[int64]*advisorapi.NumaCalculationResult{
							commonstate.FakedNUMAID: {Blocks: []*advisorapi.Block{{BlockId: "dedicated-block", Result: 2}}},
						},
					},
				},
			},
			commonstate.PoolNameShare: {
				Entries: map[string]*advisorapi.CalculationInfo{
					commonstate.FakedContainerName: {
						OwnerPoolName: commonstate.PoolNameShare,
						CalculationResultsByNumas: map[int64]*advisorapi.NumaCalculationResult{
							commonstate.FakedNUMAID: {Blocks: []*advisorapi.Block{{BlockId: "share-block", Result: 2}}},
						},
					},
				},
			},
			commonstate.PoolNameReclaim: {
				Entries: map[string]*advisorapi.CalculationInfo{
					commonstate.FakedContainerName: {
						OwnerPoolName: commonstate.PoolNameReclaim,
						CalculationResultsByNumas: map[int64]*advisorapi.NumaCalculationResult{
							commonstate.FakedNUMAID: {Blocks: []*advisorapi.Block{{BlockId: "reclaim-block", Result: 2}}},
						},
					},
				},
			},
		},
	}
	blocks := advisorapi.BlockCPUSet{
		"dedicated-block": machine.NewCPUSet(0, 1),
		"share-block":     machine.NewCPUSet(2, 3),
		"reclaim-block":   machine.NewCPUSet(4, 5),
	}

	before := cloneAdvisorState(policy.state)
	target, err := policy.planBlocks(before.PodEntries, before.MachineState, blocks, resp)
	require.NoError(t, err)
	require.Equal(t, before.PodEntries, policy.state.GetPodEntries())
	require.Equal(t, before.MachineState, policy.state.GetMachineState())

	require.NoError(t, policy.applyBlocks(blocks, resp))
	after := cloneAdvisorState(policy.state)
	require.Equal(t, target.PodEntries, after.PodEntries)
	require.Equal(t, target.MachineState, after.MachineState)
}

func TestDynamicPolicyApplyBlocksEmitsPoolMetricsBeforeFailingHook(t *testing.T) {
	topo, err := machine.GenerateDummyCPUTopology(8, 1, 1)
	require.NoError(t, err)

	policy, err := getTestDynamicPolicyWithInitialization(topo, t.TempDir())
	require.NoError(t, err)
	emitter := NewMockMetricsEmitter()
	policy.emitter = emitter

	entries := policy.state.GetPodEntries()
	entries["pod-dedicated"] = state.ContainerEntries{
		"container": {
			AllocationResult:         machine.NewCPUSet(0, 1),
			OriginalAllocationResult: machine.NewCPUSet(0, 1),
			TopologyAwareAssignments: map[int]machine.CPUSet{0: machine.NewCPUSet(0, 1)},
			AllocationMeta: commonstate.AllocationMeta{
				PodUid:        "pod-dedicated",
				ContainerName: "container",
				ContainerType: pluginapi.ContainerType_MAIN.String(),
				QoSLevel:      apiconsts.PodAnnotationQoSLevelDedicatedCores,
			},
		},
	}
	policy.state.SetPodEntries(entries, false)
	policy.RegisterAllocationHook(func(_, _ *state.AllocationInfo) error {
		return fmt.Errorf("injected hook failure")
	})

	resp := &advisorapi.ListAndWatchResponse{
		Entries: map[string]*advisorapi.CalculationEntries{
			"pod-dedicated": {
				Entries: map[string]*advisorapi.CalculationInfo{
					"container": {
						OwnerPoolName: commonstate.PoolNameDedicated,
						CalculationResultsByNumas: map[int64]*advisorapi.NumaCalculationResult{
							commonstate.FakedNUMAID: {Blocks: []*advisorapi.Block{{BlockId: "dedicated-block", Result: 2}}},
						},
					},
				},
			},
			commonstate.PoolNameShare: {
				Entries: map[string]*advisorapi.CalculationInfo{
					commonstate.FakedContainerName: {
						OwnerPoolName: commonstate.PoolNameShare,
						CalculationResultsByNumas: map[int64]*advisorapi.NumaCalculationResult{
							commonstate.FakedNUMAID: {Blocks: []*advisorapi.Block{{BlockId: "share-block", Result: 2}}},
						},
					},
				},
			},
			commonstate.PoolNameReclaim: {
				Entries: map[string]*advisorapi.CalculationInfo{
					commonstate.FakedContainerName: {
						OwnerPoolName: commonstate.PoolNameReclaim,
						CalculationResultsByNumas: map[int64]*advisorapi.NumaCalculationResult{
							commonstate.FakedNUMAID: {Blocks: []*advisorapi.Block{{BlockId: "reclaim-block", Result: 2}}},
						},
					},
				},
			},
		},
	}
	blocks := advisorapi.BlockCPUSet{
		"dedicated-block": machine.NewCPUSet(0, 1),
		"share-block":     machine.NewCPUSet(2, 3),
		"reclaim-block":   machine.NewCPUSet(4, 5),
	}

	err = policy.applyBlocks(blocks, resp)
	require.ErrorContains(t, err, "injected hook failure")
	require.ElementsMatch(t, []int64{2, 2}, emitter.storedInt64[util.MetricNamePoolSize])
}

func cloneAdvisorState(s state.State) *state.TargetState {
	return &state.TargetState{
		PodEntries:   s.GetPodEntries(),
		MachineState: s.GetMachineState(),
	}
}

func TestDynamicPolicyPlanBlocksUsesSnapshotMachineStateForSystemNUMAFallback(t *testing.T) {
	topo, err := machine.GenerateDummyCPUTopology(8, 1, 1)
	require.NoError(t, err)

	policy, err := getTestDynamicPolicyWithInitialization(topo, t.TempDir())
	require.NoError(t, err)
	policy.reservedCPUs = machine.NewCPUSet()

	currentMachineState, err := state.GenerateMachineStateFromPodEntries(topo, nil, nil)
	require.NoError(t, err)
	currentMachineState[0].DefaultCPUSet = machine.NewCPUSet(4, 5)
	policy.state.SetMachineState(currentMachineState, false)

	snapshotMachineState := currentMachineState.Clone()
	snapshotMachineState[0].DefaultCPUSet = machine.NewCPUSet(2, 3)
	curEntries := state.PodEntries{
		"system-pod": {
			"container": {
				AllocationMeta: commonstate.AllocationMeta{
					PodUid:        "system-pod",
					ContainerName: "container",
					QoSLevel:      apiconsts.PodAnnotationQoSLevelSystemCores,
					Annotations: map[string]string{
						apiconsts.PodAnnotationMemoryEnhancementNumaBinding: apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable,
					},
				},
			},
		},
	}

	target, err := policy.planBlocks(curEntries, snapshotMachineState, advisorapi.BlockCPUSet{}, &advisorapi.ListAndWatchResponse{
		Entries: map[string]*advisorapi.CalculationEntries{},
	})
	require.NoError(t, err)
	require.Equal(t, machine.NewCPUSet(2, 3), target.PodEntries["system-pod"]["container"].AllocationResult)
}

func TestDynamicPolicyPlanBlocksMissingPoolDoesNotEmitMetric(t *testing.T) {
	topo, err := machine.GenerateDummyCPUTopology(8, 1, 1)
	require.NoError(t, err)

	policy, err := getTestDynamicPolicyWithInitialization(topo, t.TempDir())
	require.NoError(t, err)
	emitter := NewMockMetricsEmitter()
	policy.emitter = emitter

	curEntries := state.PodEntries{
		"shared-pod": {
			"container": {
				RequestQuantity: 1,
				AllocationMeta: commonstate.AllocationMeta{
					PodUid:        "shared-pod",
					ContainerName: "container",
					OwnerPoolName: "missing-pool",
					QoSLevel:      apiconsts.PodAnnotationQoSLevelSharedCores,
				},
			},
		},
	}

	_, err = policy.planBlocks(curEntries, policy.state.GetMachineState(), advisorapi.BlockCPUSet{}, &advisorapi.ListAndWatchResponse{
		Entries: map[string]*advisorapi.CalculationEntries{},
	})
	require.Error(t, err)
	require.Empty(t, emitter.storedInt64)
}

func TestDynamicPolicyApplyBlocksMissingPoolDoesNotEmitMetric(t *testing.T) {
	topo, err := machine.GenerateDummyCPUTopology(8, 1, 1)
	require.NoError(t, err)

	policy, err := getTestDynamicPolicyWithInitialization(topo, t.TempDir())
	require.NoError(t, err)
	emitter := NewMockMetricsEmitter()
	policy.emitter = emitter
	policy.state.SetPodEntries(state.PodEntries{
		"shared-pod": {
			"container": {
				RequestQuantity: 1,
				AllocationMeta: commonstate.AllocationMeta{
					PodUid:        "shared-pod",
					PodNamespace:  "default",
					PodName:       "pod",
					ContainerName: "container",
					OwnerPoolName: "missing-pool",
					QoSLevel:      apiconsts.PodAnnotationQoSLevelSharedCores,
				},
			},
		},
	}, false)

	err = policy.applyBlocks(advisorapi.BlockCPUSet{}, &advisorapi.ListAndWatchResponse{
		Entries: map[string]*advisorapi.CalculationEntries{},
	})
	require.Error(t, err)
	require.Empty(t, emitter.storedInt64)
}

func TestDynamicPolicyGetAllocationPoolEntryMissingPoolEmitsMetric(t *testing.T) {
	emitter := NewMockMetricsEmitter()
	policy := &DynamicPolicy{emitter: emitter}
	allocationInfo := &state.AllocationInfo{
		AllocationMeta: commonstate.AllocationMeta{
			PodNamespace:  "default",
			PodName:       "pod",
			ContainerName: "container",
			QoSLevel:      apiconsts.PodAnnotationQoSLevelSharedCores,
		},
	}

	_, err := policy.getAllocationPoolEntry(allocationInfo, "missing-pool", state.PodEntries{})
	require.Error(t, err)
	require.Equal(t, []int64{1}, emitter.storedInt64[util.MetricNameOrphanContainer])
}

type recordingAdvisorCgroupClient struct {
	cgroupclient.FakeCgroupClient
	applied []string
	read    []string
	values  map[string]machine.CPUSet
}

func (c *recordingAdvisorCgroupClient) ApplyCPUSet(_ context.Context, relativePath string, data *common.CPUSetData) error {
	c.applied = append(c.applied, relativePath+":"+data.CPUs)
	cpuset, err := machine.Parse(data.CPUs)
	if err != nil {
		return err
	}
	if c.values == nil {
		c.values = make(map[string]machine.CPUSet)
	}
	c.values[relativePath] = cpuset
	return nil
}

func (c *recordingAdvisorCgroupClient) ReadCPUSet(_ context.Context, relativePath string) (machine.CPUSet, error) {
	c.read = append(c.read, relativePath)
	value, ok := c.values[relativePath]
	if !ok {
		return machine.NewCPUSet(), errors.New("unexpected cgroup read")
	}
	return value, nil
}

func setAdvisorCgroupTargetTestPods(policy *DynamicPolicy, entries state.PodEntries) {
	pods := make([]*v1.Pod, 0, len(entries))
	for podUID, containers := range entries {
		if containers.IsPoolEntry() {
			continue
		}
		podContainers := make([]v1.Container, 0, len(containers))
		containerStatuses := make([]v1.ContainerStatus, 0, len(containers))
		for containerName := range containers {
			podContainers = append(podContainers, v1.Container{Name: containerName})
			containerStatuses = append(containerStatuses, v1.ContainerStatus{
				Name:        containerName,
				ContainerID: "containerd://test-container-id",
			})
		}
		pods = append(pods, &v1.Pod{
			ObjectMeta: metav1.ObjectMeta{UID: types.UID(podUID)},
			Spec:       v1.PodSpec{Containers: podContainers},
			Status:     v1.PodStatus{ContainerStatuses: containerStatuses},
		})
	}
	policy.metaServer.MetaAgent.PodFetcher = &pod.PodFetcherStub{PodList: pods}
}

func prepareAdvisorBlocksFixture(t *testing.T) (*DynamicPolicy, advisorapi.BlockCPUSet, *advisorapi.ListAndWatchResponse) {
	t.Helper()

	topology, err := machine.GenerateDummyCPUTopology(8, 1, 1)
	require.NoError(t, err)
	stateDir := t.TempDir()
	policy, err := getTestDynamicPolicyWithInitialization(topology, stateDir)
	require.NoError(t, err)

	entries := policy.state.GetPodEntries()
	entries["pod-dedicated"] = state.ContainerEntries{
		"container": {
			AllocationResult:         machine.NewCPUSet(0, 1),
			OriginalAllocationResult: machine.NewCPUSet(0, 1),
			TopologyAwareAssignments: map[int]machine.CPUSet{0: machine.NewCPUSet(0, 1)},
			AllocationMeta: commonstate.AllocationMeta{
				PodUid:        "pod-dedicated",
				ContainerName: "container",
				ContainerType: pluginapi.ContainerType_MAIN.String(),
				QoSLevel:      apiconsts.PodAnnotationQoSLevelDedicatedCores,
			},
		},
	}
	policy.state.SetPodEntries(entries, true)
	machineState, err := state.GenerateMachineStateFromPodEntries(
		topology, entries, policy.state.GetMachineState(),
	)
	require.NoError(t, err)
	policy.state.SetMachineState(machineState, true)
	setAdvisorCgroupTargetTestPods(policy, entries)
	policy.cgroupClient = &recordingAdvisorCgroupClient{}

	return policy,
		advisorapi.BlockCPUSet{
			"dedicated-block": machine.NewCPUSet(6, 7),
			"share-block":     machine.NewCPUSet(2, 3),
			"reclaim-block":   machine.NewCPUSet(4, 5),
		},
		&advisorapi.ListAndWatchResponse{
			Entries: map[string]*advisorapi.CalculationEntries{
				"pod-dedicated": {Entries: map[string]*advisorapi.CalculationInfo{
					"container": {
						OwnerPoolName: commonstate.PoolNameDedicated,
						CalculationResultsByNumas: map[int64]*advisorapi.NumaCalculationResult{
							commonstate.FakedNUMAID: {Blocks: []*advisorapi.Block{{BlockId: "dedicated-block", Result: 2}}},
						},
					},
				}},
				commonstate.PoolNameShare: {Entries: map[string]*advisorapi.CalculationInfo{
					commonstate.FakedContainerName: {
						OwnerPoolName: commonstate.PoolNameShare,
						CalculationResultsByNumas: map[int64]*advisorapi.NumaCalculationResult{
							commonstate.FakedNUMAID: {Blocks: []*advisorapi.Block{{BlockId: "share-block", Result: 2}}},
						},
					},
				}},
				commonstate.PoolNameReclaim: {Entries: map[string]*advisorapi.CalculationInfo{
					commonstate.FakedContainerName: {
						OwnerPoolName: commonstate.PoolNameReclaim,
						CalculationResultsByNumas: map[int64]*advisorapi.NumaCalculationResult{
							commonstate.FakedNUMAID: {Blocks: []*advisorapi.Block{{BlockId: "reclaim-block", Result: 2}}},
						},
					},
				}},
			},
		}
}

func TestDynamicPolicyApplyBlocksDirect(t *testing.T) {
	policy, blocks, resp := prepareAdvisorBlocksFixture(t)
	policy.RegisterAllocationHook(func(_, target *state.AllocationInfo) error {
		if target.Annotations == nil {
			target.Annotations = make(map[string]string)
		}
		target.Annotations["hook.example/committed-state"] = "persisted"
		return nil
	})

	require.NoError(t, policy.applyBlocks(blocks, resp))

	committed := cloneAdvisorState(policy.state)
	require.Equal(t, "persisted", committed.PodEntries["pod-dedicated"]["container"].Annotations["hook.example/committed-state"])
	require.Equal(t, machine.NewCPUSet(6, 7), committed.PodEntries["pod-dedicated"]["container"].AllocationResult)
	require.Empty(t, policy.cgroupClient.(*recordingAdvisorCgroupClient).applied)
	require.Empty(t, policy.cgroupClient.(*recordingAdvisorCgroupClient).read)
}

func TestDynamicPolicyApplyBlocksHookFailureDoesNotUpdateStateOrRunCPUSetAdjustmentHandlers(t *testing.T) {
	policy, blocks, resp := prepareAdvisorBlocksFixture(t)
	before := cloneAdvisorState(policy.state)
	handlerCalls := 0
	require.NoError(t, policy.RegisterCPUSetAdjustmentHandler("test", func(context.Context, cpusetutil.CPUSetAdjustmentHandlerCtx) error {
		handlerCalls++
		return nil
	}))
	policy.RegisterAllocationHook(func(_, _ *state.AllocationInfo) error {
		return errors.New("hook failed")
	})

	err := policy.applyBlocks(blocks, resp)
	require.ErrorContains(t, err, "hook failed")

	require.Equal(t, before, cloneAdvisorState(policy.state))
	require.Empty(t, policy.cgroupClient.(*recordingAdvisorCgroupClient).applied)
	require.Zero(t, handlerCalls)
}

func TestDynamicPolicyApplyBlocksDoesNotRunCPUSetAdjustmentHandlers(t *testing.T) {
	policy, blocks, resp := prepareAdvisorBlocksFixture(t)
	handlerCalls := 0
	require.NoError(t, policy.RegisterCPUSetAdjustmentHandler("test", func(_ context.Context, handlerCtx cpusetutil.CPUSetAdjustmentHandlerCtx) error {
		handlerCalls++
		require.NotNil(t, handlerCtx)
		committed := cloneAdvisorState(policy.state)
		require.Equal(t, machine.NewCPUSet(6, 7), committed.PodEntries["pod-dedicated"]["container"].AllocationResult)
		return nil
	}))

	require.NoError(t, policy.applyBlocks(blocks, resp))
	require.Zero(t, handlerCalls)
	cgroup := policy.cgroupClient.(*recordingAdvisorCgroupClient)
	require.Empty(t, cgroup.applied)
	require.Empty(t, cgroup.read)
}

func TestDynamicPolicyAllocateByCPUAdvisorReturnsNilResponseErrorWithoutPendingCgroupTargets(t *testing.T) {
	policy, _, _ := prepareAdvisorBlocksFixture(t)

	err := policy.allocateByCPUAdvisor(nil, nil, nil)

	require.EqualError(t, err, "allocateByCPUAdvisor got nil qos aware lw response")
}

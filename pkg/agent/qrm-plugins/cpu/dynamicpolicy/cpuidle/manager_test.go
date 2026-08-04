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

package cpuidle

import (
	"fmt"
	"math"
	"strings"
	"sync"
	"testing"

	"github.com/bytedance/mockey"
	"github.com/stretchr/testify/assert"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	katalystapiconsts "github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/config"
	coreconsts "github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/pod"
	"github.com/kubewharf/katalyst-core/pkg/util/cgroup/common"
	cgroupmgr "github.com/kubewharf/katalyst-core/pkg/util/cgroup/manager"
)

var managerTestMutex sync.Mutex

func generateTestMetaServer(pods []*v1.Pod) *metaserver.MetaServer {
	return &metaserver.MetaServer{
		MetaAgent: &agent.MetaAgent{
			PodFetcher: &pod.PodFetcherStub{PodList: pods},
		},
	}
}

func TestManagerImpl_UpdateContainerCPUIdle(t *testing.T) {
	t.Parallel()

	managerTestMutex.Lock()
	defer managerTestMutex.Unlock()

	tests := []struct {
		name        string
		pod         *v1.Pod
		mocks       func(results map[string]int64)
		wantResults map[string]int64
		wantErr     bool
	}{
		{
			name: "apply cpu idle for configured sidecar only",
			pod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					UID:       "test-pod",
					Namespace: "default",
					Name:      "test-pod",
					Annotations: map[string]string{
						katalystapiconsts.PodAnnotationContainerCPUIdleRateKey: `{"sidecar": 50}`,
						coreconsts.MainContainerNameAnnotationKey:              "main",
					},
				},
				Spec: v1.PodSpec{
					Containers: []v1.Container{
						{
							Name:      "main",
							Resources: v1.ResourceRequirements{Limits: v1.ResourceList{v1.ResourceCPU: resource.MustParse("2")}},
						},
						{
							Name:      "sidecar",
							Resources: v1.ResourceRequirements{Limits: v1.ResourceList{v1.ResourceCPU: resource.MustParse("1")}},
						},
					},
				},
				Status: v1.PodStatus{ContainerStatuses: []v1.ContainerStatus{{Name: "main", Ready: true, ContainerID: "containerd://main-id"}, {Name: "sidecar", Ready: true, ContainerID: "containerd://sidecar-id"}}},
			},
			mocks: func(results map[string]int64) {
				mockey.Mock(common.CheckCgroup2UnifiedMode).Return(true).Build()
				mockey.Mock(common.IsCPUIdleSupported).Return(true).Build()
				mockey.Mock(common.GetContainerRelativeCgroupPath).To(func(_, containerID string) (string, error) {
					return "/sys/fs/cgroup/" + containerID, nil
				}).Build()
				mockey.Mock(cgroupmgr.GetCPUWithRelativePath).Return(&common.CPUStats{CpuQuota: 100000, CpuPeriod: 100000}, nil).Build()
				mockey.Mock(cgroupmgr.ApplyCPUWithRelativePath).To(func(relPath string, data *common.CPUData) error {
					results[relPath] = data.CpuQuota
					assert.NotNil(t, data.CpuIdlePtr)
					assert.True(t, *data.CpuIdlePtr)
					assert.Equal(t, uint64(defaultCPUCFSPeriodUs), data.CpuPeriod)
					return nil
				}).Build()
			},
			wantResults: map[string]int64{"/sys/fs/cgroup/sidecar-id": 150000},
		},
		{
			name: "invalid annotation is logged and skipped",
			pod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					UID:       "test-pod",
					Namespace: "default",
					Name:      "test-pod",
					Annotations: map[string]string{
						katalystapiconsts.PodAnnotationContainerCPUIdleRateKey: `{"sidecar": }`,
					},
				},
			},
			mocks: func(results map[string]int64) {
				mockey.Mock(common.CheckCgroup2UnifiedMode).Return(true).Build()
				mockey.Mock(common.IsCPUIdleSupported).Return(true).Build()
			},
			wantResults: map[string]int64{},
		},
		{
			name: "pod-level apply failure is logged and skipped",
			pod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					UID:       "test-pod",
					Namespace: "default",
					Name:      "test-pod",
					Annotations: map[string]string{
						katalystapiconsts.PodAnnotationContainerCPUIdleRateKey: `{"sidecar": 50}`,
					},
				},
				Spec: v1.PodSpec{
					Containers: []v1.Container{{Name: "sidecar"}},
				},
			},
			mocks: func(results map[string]int64) {
				mockey.Mock(common.CheckCgroup2UnifiedMode).Return(true).Build()
				mockey.Mock(common.IsCPUIdleSupported).Return(true).Build()
			},
			wantResults: map[string]int64{},
		},
		{
			name: "skip main container even when configured",
			pod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					UID:       "test-pod",
					Namespace: "default",
					Name:      "test-pod",
					Annotations: map[string]string{
						katalystapiconsts.PodAnnotationContainerCPUIdleRateKey: `{"main": 50}`,
						coreconsts.MainContainerNameAnnotationKey:              "main",
					},
				},
				Spec:   v1.PodSpec{Containers: []v1.Container{{Name: "main", Resources: v1.ResourceRequirements{Limits: v1.ResourceList{v1.ResourceCPU: resource.MustParse("2")}}}}},
				Status: v1.PodStatus{ContainerStatuses: []v1.ContainerStatus{{Name: "main", Ready: true, ContainerID: "containerd://main-id"}}},
			},
			mocks: func(results map[string]int64) {
				mockey.Mock(common.CheckCgroup2UnifiedMode).Return(true).Build()
				mockey.Mock(common.IsCPUIdleSupported).Return(true).Build()
				mockey.Mock(cgroupmgr.ApplyCPUWithRelativePath).To(func(relPath string, data *common.CPUData) error {
					results[relPath] = data.CpuQuota
					return nil
				}).Build()
			},
			wantResults: map[string]int64{},
		},
	}

	for _, tt := range tests {
		tt := tt
		mockey.PatchConvey(tt.name, t, func() {
			results := map[string]int64{}
			if tt.mocks != nil {
				tt.mocks(results)
			}

			metaServer := generateTestMetaServer([]*v1.Pod{tt.pod})
			manager := newManager(metaServer)
			conf := config.NewConfiguration()
			conf.MainContainerAnnotationKey = coreconsts.MainContainerNameAnnotationKey

			err := manager.UpdateContainerCPUIdle(conf)
			assert.Equal(t, tt.wantErr, err != nil)
			assert.Equal(t, tt.wantResults, results)
		})
	}
}

func TestManagerImpl_UpdateContainerCPUIdle_NilInputs(t *testing.T) {
	t.Parallel()

	t.Run("nil configuration", func(t *testing.T) {
		t.Parallel()

		manager := newManager(generateTestMetaServer(nil))
		err := manager.UpdateContainerCPUIdle(nil)
		assert.EqualError(t, err, "nil configuration")
	})

	t.Run("nil metaserver", func(t *testing.T) {
		t.Parallel()

		manager := newManager(nil)
		err := manager.UpdateContainerCPUIdle(config.NewConfiguration())
		assert.EqualError(t, err, "nil metaServer")
	})
}

func TestManagerImpl_ApplyCPUIdleSettingsForPodValidation(t *testing.T) {
	t.Parallel()

	t.Run("missing container cpu limit", func(t *testing.T) {
		t.Parallel()

		manager := newManager(nil)
		conf := config.NewConfiguration()
		pod := &v1.Pod{
			Spec: v1.PodSpec{
				Containers: []v1.Container{{Name: "sidecar"}},
			},
		}

		err := manager.applyCPUIdleSettingsForPod(conf, pod, map[string]int64{"sidecar": 50})
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "pod cpu limit is unset for containers: sidecar")
	})

	t.Run("invalid container names and rates", func(t *testing.T) {
		t.Parallel()

		manager := newManager(nil)
		conf := config.NewConfiguration()
		conf.MainContainerAnnotationKey = coreconsts.MainContainerNameAnnotationKey
		pod := &v1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: "default",
				Name:      "test-pod",
				Annotations: map[string]string{
					coreconsts.MainContainerNameAnnotationKey: "main",
				},
			},
			Spec: v1.PodSpec{
				Containers: []v1.Container{{
					Name:      "main",
					Resources: v1.ResourceRequirements{Limits: v1.ResourceList{v1.ResourceCPU: resource.MustParse("1")}},
				}},
			},
		}

		err := manager.applyCPUIdleSettingsForPod(conf, pod, map[string]int64{"": 50, "sidecar": 0, "main": 50})
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "empty target container name")
		assert.Contains(t, err.Error(), "invalid target cpu quota rate for container sidecar: 0")
	})

	t.Run("target cpu quota rate boundaries", func(t *testing.T) {
		t.Parallel()

		manager := newManager(nil)
		conf := config.NewConfiguration()
		conf.MainContainerAnnotationKey = coreconsts.MainContainerNameAnnotationKey
		pod := &v1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: "default",
				Name:      "test-pod",
				Annotations: map[string]string{
					coreconsts.MainContainerNameAnnotationKey: "main",
				},
			},
			Spec: v1.PodSpec{Containers: []v1.Container{
				{
					Name:      "main",
					Resources: v1.ResourceRequirements{Limits: v1.ResourceList{v1.ResourceCPU: resource.MustParse("1")}},
				},
				{
					Name:      "sidecar",
					Resources: v1.ResourceRequirements{Limits: v1.ResourceList{v1.ResourceCPU: resource.MustParse("1")}},
				},
			}},
		}

		testCases := []struct {
			name    string
			rate    int64
			wantErr bool
		}{
			{name: "zero rejected", rate: 0, wantErr: true},
			{name: "one hundred accepted", rate: 100},
			{name: "one hundred one rejected", rate: 101, wantErr: true},
			{name: "max int64 rejected", rate: math.MaxInt64, wantErr: true},
		}

		for _, tc := range testCases {
			tc := tc
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()

				err := manager.applyCPUIdleSettingsForPod(conf, pod, map[string]int64{"sidecar": tc.rate})
				if tc.wantErr {
					assert.EqualError(t, err, fmt.Sprintf("invalid target cpu quota rate for container sidecar: %d", tc.rate))
					return
				}

				assert.NoError(t, err)
			})
		}
	})
}

func TestManagerImpl_ApplyCPUIdleSettingsToContainer_SkipNotReady(t *testing.T) {
	t.Parallel()

	manager := newManager(nil)
	pod := &v1.Pod{
		Status: v1.PodStatus{ContainerStatuses: []v1.ContainerStatus{{
			Name:  "sidecar",
			Ready: false,
		}}},
	}

	err := manager.applyCPUIdleSettingsToContainer(pod, "sidecar", 50, 1000)
	assert.NoError(t, err)
}

func TestGetPodTotalCPULimitMilli(t *testing.T) {
	t.Parallel()

	t.Run("nil pod", func(t *testing.T) {
		t.Parallel()
		totalMilli, containersWithoutLimit := getPodTotalCPULimitMilli(nil)
		assert.Zero(t, totalMilli)
		assert.Nil(t, containersWithoutLimit)
	})

	t.Run("sum limits and track missing", func(t *testing.T) {
		t.Parallel()
		pod := &v1.Pod{
			Spec: v1.PodSpec{Containers: []v1.Container{
				{
					Name:      "main",
					Resources: v1.ResourceRequirements{Limits: v1.ResourceList{v1.ResourceCPU: resource.MustParse("1500m")}},
				},
				{Name: "sidecar"},
			}},
		}

		totalMilli, containersWithoutLimit := getPodTotalCPULimitMilli(pod)
		assert.Equal(t, int64(1500), totalMilli)
		assert.Equal(t, []string{"sidecar"}, containersWithoutLimit)
	})
}

func TestGetCurrentCPULimitMilli(t *testing.T) {
	t.Parallel()

	t.Run("invalid stats", func(t *testing.T) {
		t.Parallel()

		for _, cpuStats := range []*common.CPUStats{nil, {CpuQuota: math.MaxInt64, CpuPeriod: 100000}, {CpuQuota: 100000, CpuPeriod: 0}} {
			limitMilli, ok := getCurrentCPULimitMilli(cpuStats)
			assert.Zero(t, limitMilli)
			assert.False(t, ok)
		}
	})

	t.Run("valid stats", func(t *testing.T) {
		t.Parallel()

		limitMilli, ok := getCurrentCPULimitMilli(&common.CPUStats{CpuQuota: 200000, CpuPeriod: 100000})
		assert.True(t, ok)
		assert.Equal(t, int64(2000), limitMilli)
	})
}

func TestGetContainerStatusFromPodStatus(t *testing.T) {
	t.Parallel()

	pod := &v1.Pod{Status: v1.PodStatus{ContainerStatuses: []v1.ContainerStatus{{Name: "main"}}}}
	assert.Nil(t, getContainerStatusFromPodStatus(nil, "main"))
	assert.NotNil(t, getContainerStatusFromPodStatus(pod, "main"))
	assert.Nil(t, getContainerStatusFromPodStatus(pod, "sidecar"))
}

func TestGetContainerIDFromPodStatus(t *testing.T) {
	t.Parallel()

	t.Run("nil pod", func(t *testing.T) {
		t.Parallel()

		_, err := getContainerIDFromPodStatus(nil, "main")
		assert.EqualError(t, err, "nil pod")
	})

	t.Run("container not found", func(t *testing.T) {
		t.Parallel()

		_, err := getContainerIDFromPodStatus(&v1.Pod{}, "main")
		assert.EqualError(t, err, "container main container id not found")
	})

	t.Run("container not ready", func(t *testing.T) {
		t.Parallel()

		pod := &v1.Pod{Status: v1.PodStatus{ContainerStatuses: []v1.ContainerStatus{{
			Name:  "main",
			Ready: false,
			State: v1.ContainerState{Waiting: &v1.ContainerStateWaiting{}},
		}}}}

		_, err := getContainerIDFromPodStatus(pod, "main")
		assert.ErrorIs(t, err, errContainerNotReady)
		assert.True(t, strings.Contains(err.Error(), "container main"))
	})

	t.Run("container id not ready", func(t *testing.T) {
		t.Parallel()

		pod := &v1.Pod{Status: v1.PodStatus{ContainerStatuses: []v1.ContainerStatus{{
			Name:  "main",
			Ready: true,
			State: v1.ContainerState{Running: &v1.ContainerStateRunning{}},
		}}}}

		_, err := getContainerIDFromPodStatus(pod, "main")
		assert.ErrorIs(t, err, errContainerIDNotReady)
	})

	t.Run("trim prefix", func(t *testing.T) {
		t.Parallel()

		pod := &v1.Pod{Status: v1.PodStatus{ContainerStatuses: []v1.ContainerStatus{{
			Name:        "main",
			Ready:       true,
			ContainerID: "containerd://main-id",
		}}}}

		containerID, err := getContainerIDFromPodStatus(pod, "main")
		assert.NoError(t, err)
		assert.Equal(t, "main-id", containerID)
	})
}

func TestManagerImpl_GetContainerCurrentCPUStats_ValidateInputs(t *testing.T) {
	t.Parallel()

	manager := newManager(nil)
	_, err := manager.getContainerCurrentCPUStats(&v1.Pod{}, "main")
	assert.EqualError(t, err, "nil metaServer")

	manager = newManager(generateTestMetaServer(nil))
	_, err = manager.getContainerCurrentCPUStats(nil, "main")
	assert.EqualError(t, err, "nil pod")
}

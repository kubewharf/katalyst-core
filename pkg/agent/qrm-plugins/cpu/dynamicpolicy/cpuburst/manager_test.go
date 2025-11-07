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

package cpuburst

import (
	"fmt"
	"testing"

	"github.com/bytedance/mockey"
	"github.com/stretchr/testify/assert"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/config/generic"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/pod"
	"github.com/kubewharf/katalyst-core/pkg/util/cgroup/common"
	"github.com/kubewharf/katalyst-core/pkg/util/cgroup/manager"
)

func generateTestMetaServer(pods []*v1.Pod) *metaserver.MetaServer {
	podFetcher := &pod.PodFetcherStub{
		PodList: pods,
	}

	return &metaserver.MetaServer{
		MetaAgent: &agent.MetaAgent{
			PodFetcher: podFetcher,
		},
	}
}

func TestManagerImpl_UpdateCPUBurst(t *testing.T) {
	t.Parallel()

	qosConfig := generic.NewQoSConfiguration()
	type resultState map[string]int64

	tests := []struct {
		name        string
		pods        []*v1.Pod
		mocks       func(s resultState)
		wantErr     bool
		wantResults resultState
	}{
		{
			name: "dedicated cores pods with no cpu burst policy",
			pods: []*v1.Pod{
				{
					ObjectMeta: metav1.ObjectMeta{
						UID: "test-pod",
						Annotations: map[string]string{
							consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
						},
					},
					Spec: v1.PodSpec{
						Containers: []v1.Container{
							{
								Name: "test-container-1",
							},
							{
								Name: "test-container-2",
							},
						},
					},
					Status: v1.PodStatus{
						ContainerStatuses: []v1.ContainerStatus{
							{
								Name:        "test-container-1",
								ContainerID: "test-container-1-id",
							},
							{
								Name:        "test-container-2",
								ContainerID: "test-container-2-id",
							},
						},
					},
				},
			},
			mocks: func(s resultState) {
				mockey.Mock(common.IsContainerCgroupExist).Return(true, nil).Build()
				mockey.Mock(common.GetContainerAbsCgroupPath).To(func(_, podUID, containerID string) (string, error) {
					return "/sys/fs/cgroup/cpu/" + podUID + "/" + containerID, nil
				}).Build()
				mockey.Mock(manager.GetCPUWithAbsolutePath).Return(&common.CPUStats{CpuQuota: 200}, nil).Build()
				mockey.Mock(manager.ApplyCPUWithAbsolutePath).
					To(func(absPath string, cpuData *common.CPUData) error {
						s[absPath] = cpuData.CpuBurst
						return nil
					}).Build()
			},
			wantResults: resultState{
				"/sys/fs/cgroup/cpu/test-pod/test-container-1-id": 200,
				"/sys/fs/cgroup/cpu/test-pod/test-container-2-id": 200,
			},
		},
		{
			name: "shared cores pods with no cpu burst policy",
			pods: []*v1.Pod{
				{
					ObjectMeta: metav1.ObjectMeta{
						UID: "test-pod",
						Annotations: map[string]string{
							consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
						},
					},
					Spec: v1.PodSpec{
						Containers: []v1.Container{
							{
								Name: "test-container-1",
							},
							{
								Name: "test-container-2",
							},
						},
					},
					Status: v1.PodStatus{
						ContainerStatuses: []v1.ContainerStatus{
							{
								Name:        "test-container-1",
								ContainerID: "test-container-1-id",
							},
							{
								Name:        "test-container-2",
								ContainerID: "test-container-2-id",
							},
						},
					},
				},
			},
			mocks: func(s resultState) {
				mockey.Mock(common.IsContainerCgroupExist).Return(true, nil).Build()
				mockey.Mock(common.GetContainerAbsCgroupPath).To(func(_, podUID, containerID string) (string, error) {
					return "/sys/fs/cgroup/cpu/" + podUID + "/" + containerID, nil
				}).Build()
				mockey.Mock(manager.GetCPUWithAbsolutePath).Return(&common.CPUStats{CpuQuota: 200}, nil).Build()
				mockey.Mock(manager.ApplyCPUWithAbsolutePath).
					To(func(absPath string, cpuData *common.CPUData) error {
						s[absPath] = cpuData.CpuBurst
						return nil
					}).Build()
			},
			wantResults: resultState{},
		},
		{
			name: "mix of shared cores and dedicated cores pods with cpu burst enabled",
			pods: []*v1.Pod{
				{
					ObjectMeta: metav1.ObjectMeta{
						UID: "test-pod-1",
						Annotations: map[string]string{
							consts.PodAnnotationQoSLevelKey:       consts.PodAnnotationQoSLevelDedicatedCores,
							consts.PodAnnotationCPUEnhancementKey: `{"cpu_burst_policy":"static", "cpu_burst_percent":"50"}`,
						},
					},
					Spec: v1.PodSpec{
						Containers: []v1.Container{
							{
								Name: "test-container-1",
							},
							{
								Name: "test-container-2",
							},
						},
					},
					Status: v1.PodStatus{
						ContainerStatuses: []v1.ContainerStatus{
							{
								Name:        "test-container-1",
								ContainerID: "test-container-1-id",
							},
							{
								Name:        "test-container-2",
								ContainerID: "test-container-2-id",
							},
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						UID: "test-pod-2",
						Annotations: map[string]string{
							consts.PodAnnotationQoSLevelKey:       consts.PodAnnotationQoSLevelDedicatedCores,
							consts.PodAnnotationCPUEnhancementKey: `{"cpu_burst_policy":"static", "cpu_burst_percent":"25"}`,
						},
					},
					Spec: v1.PodSpec{
						Containers: []v1.Container{
							{
								Name: "test-container-1",
							},
							{
								Name: "test-container-2",
							},
						},
					},
					Status: v1.PodStatus{
						ContainerStatuses: []v1.ContainerStatus{
							{
								Name:        "test-container-1",
								ContainerID: "test-container-1-id",
							},
							{
								Name:        "test-container-2",
								ContainerID: "test-container-2-id",
							},
						},
					},
				},
			},
			mocks: func(s resultState) {
				mockey.Mock(common.IsContainerCgroupExist).Return(true, nil).Build()
				mockey.Mock(common.GetContainerAbsCgroupPath).To(func(_, podUID, containerID string) (string, error) {
					return "/sys/fs/cgroup/cpu/" + podUID + "/" + containerID, nil
				}).Build()
				mockey.Mock(manager.GetCPUWithAbsolutePath).Return(&common.CPUStats{CpuQuota: 200}, nil).Build()
				mockey.Mock(manager.ApplyCPUWithAbsolutePath).
					To(func(absPath string, cpuData *common.CPUData) error {
						s[absPath] = cpuData.CpuBurst
						return nil
					}).Build()
			},
			wantResults: resultState{
				"/sys/fs/cgroup/cpu/test-pod-1/test-container-1-id": 100,
				"/sys/fs/cgroup/cpu/test-pod-1/test-container-2-id": 100,
				"/sys/fs/cgroup/cpu/test-pod-2/test-container-1-id": 50,
				"/sys/fs/cgroup/cpu/test-pod-2/test-container-2-id": 50,
			},
		},
		{
			name: "get container ID fails returns error",
			pods: []*v1.Pod{
				{
					ObjectMeta: metav1.ObjectMeta{
						UID: "test-pod",
						Annotations: map[string]string{
							consts.PodAnnotationQoSLevelKey:       consts.PodAnnotationQoSLevelDedicatedCores,
							consts.PodAnnotationCPUEnhancementKey: `{"cpu_burst_policy":"static", "cpu_burst_percent":"50"}`,
						},
					},
					Spec: v1.PodSpec{
						Containers: []v1.Container{
							{
								Name: "test-container-1",
							},
							{
								Name: "test-container-2",
							},
						},
					},
				},
			},
			wantErr: true,
		},
		{
			name: "Checking container cgroup exists fails, returns error",
			pods: []*v1.Pod{
				{
					ObjectMeta: metav1.ObjectMeta{
						UID: "test-pod-1",
						Annotations: map[string]string{
							consts.PodAnnotationQoSLevelKey:       consts.PodAnnotationQoSLevelDedicatedCores,
							consts.PodAnnotationCPUEnhancementKey: `{"cpu_burst_policy":"static", "cpu_burst_percent":"50"}`,
						},
					},
					Spec: v1.PodSpec{
						Containers: []v1.Container{
							{
								Name: "test-container-1",
							},
							{
								Name: "test-container-2",
							},
						},
					},
					Status: v1.PodStatus{
						ContainerStatuses: []v1.ContainerStatus{
							{
								Name:        "test-container-1",
								ContainerID: "test-container-1-id",
							},
							{
								Name:        "test-container-2",
								ContainerID: "test-container-2-id",
							},
						},
					},
				},
			},
			mocks: func(s resultState) {
				mockey.Mock(common.IsContainerCgroupExist).Return(false, fmt.Errorf("test error")).Build()
			},
			wantErr: true,
		},
		{
			name: "Container cgroup path does not exist, just skip",
			pods: []*v1.Pod{
				{
					ObjectMeta: metav1.ObjectMeta{
						UID: "test-pod-1",
						Annotations: map[string]string{
							consts.PodAnnotationQoSLevelKey:       consts.PodAnnotationQoSLevelDedicatedCores,
							consts.PodAnnotationCPUEnhancementKey: `{"cpu_burst_policy":"static", "cpu_burst_percent":"50"}`,
						},
					},
					Spec: v1.PodSpec{
						Containers: []v1.Container{
							{
								Name: "test-container-1",
							},
							{
								Name: "test-container-2",
							},
						},
					},
					Status: v1.PodStatus{
						ContainerStatuses: []v1.ContainerStatus{
							{
								Name:        "test-container-1",
								ContainerID: "test-container-1-id",
							},
							{
								Name:        "test-container-2",
								ContainerID: "test-container-2-id",
							},
						},
					},
				},
			},
			mocks: func(s resultState) {
				mockey.Mock(common.IsContainerCgroupExist).Return(false, nil).Build()
				mockey.Mock(common.GetContainerAbsCgroupPath).To(func(_, podUID, containerID string) (string, error) {
					return "/sys/fs/cgroup/cpu/" + podUID + "/" + containerID, nil
				}).Build()
				mockey.Mock(manager.GetCPUWithAbsolutePath).Return(&common.CPUStats{CpuQuota: 200}, nil).Build()
				mockey.Mock(manager.ApplyCPUWithAbsolutePath).
					To(func(absPath string, cpuData *common.CPUData) error {
						s[absPath] = cpuData.CpuBurst
						return nil
					}).Build()
			},
			wantResults: make(resultState),
		},
	}

	for _, tt := range tests {
		mockey.PatchConvey(tt.name, t, func() {
			results := make(resultState)
			if tt.mocks != nil {
				tt.mocks(results)
			}

			cpuBurstManager := NewManager(generateTestMetaServer(tt.pods))

			err := cpuBurstManager.UpdateCPUBurst(qosConfig)
			if (err != nil) != tt.wantErr {
				t.Errorf("UpdateCPUBurst() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if !tt.wantErr {
				assert.Equal(t, tt.wantResults, results)
			}
		})
	}
}

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

package executor

import (
	"testing"

	"github.com/stretchr/testify/assert"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	"github.com/kubewharf/katalyst-core/pkg/util/cgroup/common"
	"github.com/kubewharf/katalyst-core/pkg/util/cgroup/manager"
)

func TestImpl_UpdateContainerResources(t *testing.T) {
	t.Parallel()

	impl := NewExecutor(&manager.FakeCgroupManager{})

	err := impl.UpdateContainerResources(nil, nil, nil)
	assert.Nil(t, err)

	err = impl.UpdateContainerResources(&v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: "testPod",
			UID:  "testUID",
		},
		Status: v1.PodStatus{
			ContainerStatuses: []v1.ContainerStatus{
				{
					Name:        "testContainer",
					ContainerID: "containerd://containerID",
				},
			},
		},
	},
		&v1.Container{
			Name: "testContainer",
		},
		nil)
	assert.NotNil(t, err)

	err = impl.UpdateContainerResources(
		&v1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name: "testPod",
				UID:  "testUID",
			},
			Status: v1.PodStatus{
				ContainerStatuses: []v1.ContainerStatus{
					{
						Name:        "testContainer",
						ContainerID: "containerd://containerID",
					},
				},
			},
		},
		&v1.Container{
			Name: "testContainer",
		}, map[string]*v1alpha1.ResourceAllocationInfo{
			"cpu": {
				OciPropertyName:  "CpusetCpus",
				AllocationResult: "0-3",
			},
			"memory": {
				OciPropertyName:  "CpusetMems",
				AllocationResult: "0,1",
			},
		})
	assert.NotNil(t, err)
}

func TestCommitCPUSet(t *testing.T) {
	t.Parallel()

	impl := &Impl{
		cgroupManager: &manager.FakeCgroupManager{},
	}
	err := impl.commitCPUSet("testPath", &common.CPUSetData{
		CPUs: "0-3",
		Mems: "0,1",
	})
	assert.Nil(t, err)
}

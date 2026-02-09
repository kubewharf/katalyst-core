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
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/pod"
)

func mockPodInMetaServer(dp *DynamicPolicy, alloc *state.AllocationInfo, cpuReq string) {
	mockPod := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			UID:  types.UID(alloc.PodUid),
			Name: alloc.PodName,
		},
		Spec: v1.PodSpec{
			Containers: []v1.Container{
				{
					Name: alloc.ContainerName,
					Resources: v1.ResourceRequirements{
						Requests: v1.ResourceList{
							v1.ResourceCPU: resource.MustParse(cpuReq),
						},
					},
				},
			},
		},
	}
	dp.metaServer.PodFetcher = &pod.PodFetcherStub{PodList: []*v1.Pod{mockPod}}
}

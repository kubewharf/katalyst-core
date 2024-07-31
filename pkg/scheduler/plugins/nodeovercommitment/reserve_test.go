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

package nodeovercommitment

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	v12 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/kubernetes/pkg/scheduler/framework"

	"github.com/kubewharf/katalyst-core/pkg/scheduler/plugins/nodeovercommitment/cache"
)

func TestReserve(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name      string
		pods      []*v1.Pod
		nodeName  string
		expectRes int
	}{
		{
			name:     "case 1",
			nodeName: "testNode",
			pods: []*v1.Pod{
				{
					ObjectMeta: v12.ObjectMeta{
						Name: "pod1",
						UID:  "poduid1",
					},
					Spec: v1.PodSpec{
						NodeName: "testNode",
						Containers: []v1.Container{
							{
								Name: "testContainer",
							},
						},
					},
				},
				{
					ObjectMeta: v12.ObjectMeta{
						Name: "pod2",
						UID:  "poduid2",
					},
					Spec: v1.PodSpec{
						NodeName: "testNode",
						Containers: []v1.Container{
							{
								Name: "testContainer",
								Resources: v1.ResourceRequirements{
									Requests: map[v1.ResourceName]resource.Quantity{
										v1.ResourceCPU:    resource.MustParse("4"),
										v1.ResourceMemory: resource.MustParse("8Gi"),
									},
								},
							},
							{
								Name: "testContainer2",
								Resources: v1.ResourceRequirements{
									Requests: map[v1.ResourceName]resource.Quantity{
										v1.ResourceCPU:    resource.MustParse("2"),
										v1.ResourceMemory: resource.MustParse("4Gi"),
									},
									Limits: map[v1.ResourceName]resource.Quantity{
										v1.ResourceCPU:    resource.MustParse("2"),
										v1.ResourceMemory: resource.MustParse("4Gi"),
									},
								},
							},
						},
					},
				},
				{
					ObjectMeta: v12.ObjectMeta{
						Name: "pod3",
						UID:  "poduid3",
					},
					Spec: v1.PodSpec{
						NodeName: "testNode",
						Containers: []v1.Container{
							{
								Name: "testContainer",
								Resources: v1.ResourceRequirements{
									Requests: map[v1.ResourceName]resource.Quantity{
										v1.ResourceCPU:    resource.MustParse("4"),
										v1.ResourceMemory: resource.MustParse("8Gi"),
									},
									Limits: map[v1.ResourceName]resource.Quantity{
										v1.ResourceCPU:    resource.MustParse("4"),
										v1.ResourceMemory: resource.MustParse("8Gi"),
									},
								},
							},
						},
					},
				},
				{
					ObjectMeta: v12.ObjectMeta{
						Name: "pod4",
						UID:  "poduid4",
					},
					Spec: v1.PodSpec{
						NodeName: "testNode",
						InitContainers: []v1.Container{
							{
								Name: "initContainer",
								Resources: v1.ResourceRequirements{
									Requests: map[v1.ResourceName]resource.Quantity{
										v1.ResourceCPU:    resource.MustParse("1"),
										v1.ResourceMemory: resource.MustParse("2Gi"),
									},
									Limits: map[v1.ResourceName]resource.Quantity{
										v1.ResourceCPU:    resource.MustParse("1"),
										v1.ResourceMemory: resource.MustParse("2Gi"),
									},
								},
							},
						},
						Containers: []v1.Container{
							{
								Name: "testContainer",
								Resources: v1.ResourceRequirements{
									Requests: map[v1.ResourceName]resource.Quantity{
										v1.ResourceCPU:    resource.MustParse("4"),
										v1.ResourceMemory: resource.MustParse("8Gi"),
									},
									Limits: map[v1.ResourceName]resource.Quantity{
										v1.ResourceCPU:    resource.MustParse("4"),
										v1.ResourceMemory: resource.MustParse("8Gi"),
									},
								},
							},
						},
					},
				},
			},
			expectRes: 8,
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			n := &NodeOvercommitment{}
			cs := framework.NewCycleState()

			for _, pod := range tc.pods {
				n.Reserve(context.TODO(), cs, pod, tc.nodeName)
			}

			nodeCache, err := cache.GetCache().GetNode(tc.nodeName)
			assert.NoError(t, err)
			assert.Equal(t, tc.expectRes, nodeCache.GetGuaranteedCPUs())

			for _, pod := range tc.pods {
				err = cache.GetCache().AddPod(pod)
				assert.NoError(t, err)
			}

			assert.Equal(t, tc.expectRes, nodeCache.GetGuaranteedCPUs())
			for _, pod := range tc.pods {
				err = cache.GetCache().RemovePod(pod)
				assert.NoError(t, err)
			}

			for _, pod := range tc.pods {
				n.Reserve(context.TODO(), cs, pod, tc.nodeName)
				n.Unreserve(context.TODO(), cs, pod, tc.nodeName)
			}
			assert.Equal(t, 0, nodeCache.GetGuaranteedCPUs())
		})
	}
}

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

package util

import (
	"testing"

	"github.com/stretchr/testify/assert"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/kubernetes/pkg/scheduler/framework"
)

func TestCalculateEffectiveResource(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name          string
		pod           *v1.Pod
		expectRes     framework.Resource
		expectNon0CPU int64
		expectNon0Mem int64
	}{
		{
			name: "pod1",
			pod: &v1.Pod{
				Spec: v1.PodSpec{
					Containers: []v1.Container{
						{
							Name: "c1",
							Resources: v1.ResourceRequirements{
								Requests: map[v1.ResourceName]resource.Quantity{
									v1.ResourceCPU:    resource.MustParse("2"),
									v1.ResourceMemory: resource.MustParse("4Gi"),
								},
							},
						},
					},
					InitContainers: []v1.Container{
						{
							Name: "c1",
							Resources: v1.ResourceRequirements{
								Requests: map[v1.ResourceName]resource.Quantity{
									v1.ResourceCPU:    resource.MustParse("4"),
									v1.ResourceMemory: resource.MustParse("8Gi"),
								},
							},
						},
					},
				},
			},
			expectRes: framework.Resource{
				MilliCPU: 4000,
				Memory:   8 * 1024 * 1024 * 1024,
			},
			expectNon0CPU: 4000,
			expectNon0Mem: 8 * 1024 * 1024 * 1024,
		},
		{
			name: "pod1",
			pod: &v1.Pod{
				Spec: v1.PodSpec{
					Containers: []v1.Container{
						{
							Name: "c1",
						},
					},
					InitContainers: []v1.Container{
						{
							Name: "c1",
						},
					},
				},
			},
			expectRes: framework.Resource{
				MilliCPU: 0,
				Memory:   0,
			},
			expectNon0CPU: 100,
			expectNon0Mem: 200 * 1024 * 1024,
		},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			res, non0CPU, non0Mem := CalculateEffectiveResource(tc.pod)

			assert.Equal(t, tc.expectRes.MilliCPU, res.MilliCPU)
			assert.Equal(t, tc.expectRes.Memory, res.Memory)
			assert.Equal(t, tc.expectNon0CPU, non0CPU)
			assert.Equal(t, tc.expectNon0Mem, non0Mem)
		})
	}
}

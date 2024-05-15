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

	"github.com/kubewharf/katalyst-api/pkg/apis/node/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/scheduler/plugins/nodeovercommitment/cache"
)

func TestPreFilter(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name                string
		pod                 *v1.Pod
		expectCPU           int64
		expectGuaranteedCPU int
	}{
		{
			name: "burstable pod",
			pod: &v1.Pod{
				Spec: v1.PodSpec{
					Containers: []v1.Container{
						{
							Resources: v1.ResourceRequirements{
								Limits: map[v1.ResourceName]resource.Quantity{
									v1.ResourceCPU:    resource.MustParse("4"),
									v1.ResourceMemory: resource.MustParse("8Gi"),
								},
								Requests: map[v1.ResourceName]resource.Quantity{
									v1.ResourceCPU:    resource.MustParse("2"),
									v1.ResourceMemory: resource.MustParse("4Gi"),
								},
							},
						},
					},
				},
			},
			expectCPU:           2000,
			expectGuaranteedCPU: 0,
		},
		{
			name: "guaranteed pod",
			pod: &v1.Pod{
				Spec: v1.PodSpec{
					Containers: []v1.Container{
						{
							Resources: v1.ResourceRequirements{
								Limits: map[v1.ResourceName]resource.Quantity{
									v1.ResourceCPU:    resource.MustParse("4"),
									v1.ResourceMemory: resource.MustParse("8Gi"),
								},
								Requests: map[v1.ResourceName]resource.Quantity{
									v1.ResourceCPU:    resource.MustParse("4"),
									v1.ResourceMemory: resource.MustParse("8Gi"),
								},
							},
						},
					},
				},
			},
			expectCPU:           0,
			expectGuaranteedCPU: 4,
		},
		{
			name: "guaranteed pod with init container",
			pod: &v1.Pod{
				Spec: v1.PodSpec{
					InitContainers: []v1.Container{
						{
							Resources: v1.ResourceRequirements{
								Limits: map[v1.ResourceName]resource.Quantity{
									v1.ResourceCPU:    resource.MustParse("4"),
									v1.ResourceMemory: resource.MustParse("8Gi"),
								},
								Requests: map[v1.ResourceName]resource.Quantity{
									v1.ResourceCPU:    resource.MustParse("4"),
									v1.ResourceMemory: resource.MustParse("8Gi"),
								},
							},
						},
					},
					Containers: []v1.Container{
						{
							Resources: v1.ResourceRequirements{
								Limits: map[v1.ResourceName]resource.Quantity{
									v1.ResourceCPU:    resource.MustParse("2"),
									v1.ResourceMemory: resource.MustParse("8Gi"),
								},
								Requests: map[v1.ResourceName]resource.Quantity{
									v1.ResourceCPU:    resource.MustParse("2"),
									v1.ResourceMemory: resource.MustParse("8Gi"),
								},
							},
						},
					},
				},
			},
			expectCPU:           0,
			expectGuaranteedCPU: 4,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			n := &NodeOvercommitment{}
			cs := framework.NewCycleState()
			res, stat := n.PreFilter(context.TODO(), cs, tc.pod)
			assert.Nil(t, res)
			assert.Nil(t, stat)

			fs, err := getPreFilterState(cs)
			assert.NoError(t, err)
			assert.Equal(t, tc.expectGuaranteedCPU, fs.GuaranteedCPUs)
			assert.Equal(t, tc.expectCPU, fs.MilliCPU)
		})
	}
}

func TestFilter(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name      string
		node      *v1.Node
		cnrs      []*v1alpha1.CustomNodeResource
		existPods []*v1.Pod
		pod       *v1.Pod
		requested *framework.Resource
		expectRes *framework.Status
	}{
		{
			name: "node cpumanager off",
			node: &v1.Node{
				ObjectMeta: v12.ObjectMeta{
					Name: "node1",
					Annotations: map[string]string{
						"katalyst.kubewharf.io/cpu_overcommit_ratio":     "2.0",
						"katalyst.kubewharf.io/memory_overcommit_ratio":  "1.0",
						"katalyst.kubewharf.io/original_allocatable_cpu": "16000",
					},
				},
				Status: v1.NodeStatus{
					Allocatable: map[v1.ResourceName]resource.Quantity{
						v1.ResourceCPU:    resource.MustParse("32"),
						v1.ResourceMemory: resource.MustParse("32Gi"),
					},
				},
			},
			cnrs: []*v1alpha1.CustomNodeResource{
				{
					ObjectMeta: v12.ObjectMeta{
						Name: "node1",
						Annotations: map[string]string{
							"katalyst.kubewharf.io/overcommit_cpu_manager":    "none",
							"katalyst.kubewharf.io/overcommit_memory_manager": "None",
							"katalyst.kubewharf.io/guaranteed_cpus":           "0",
						},
					},
				},
			},
			existPods: []*v1.Pod{},
			requested: &framework.Resource{
				MilliCPU: 24000,
			},
			pod: &v1.Pod{
				ObjectMeta: v12.ObjectMeta{
					Name: "pod1",
				},
				Spec: v1.PodSpec{
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
					},
				},
			},
			expectRes: nil,
		},
		{
			name: "node cpumanager on",
			node: &v1.Node{
				ObjectMeta: v12.ObjectMeta{
					Name: "node1",
					Annotations: map[string]string{
						"katalyst.kubewharf.io/cpu_overcommit_ratio":     "2.0",
						"katalyst.kubewharf.io/memory_overcommit_ratio":  "1.0",
						"katalyst.kubewharf.io/original_allocatable_cpu": "16000",
					},
				},
				Status: v1.NodeStatus{
					Allocatable: map[v1.ResourceName]resource.Quantity{
						v1.ResourceCPU:    resource.MustParse("32"),
						v1.ResourceMemory: resource.MustParse("32Gi"),
					},
				},
			},
			cnrs: []*v1alpha1.CustomNodeResource{
				{
					ObjectMeta: v12.ObjectMeta{
						Name: "node1",
						Annotations: map[string]string{
							"katalyst.kubewharf.io/overcommit_cpu_manager":    "static",
							"katalyst.kubewharf.io/overcommit_memory_manager": "None",
							"katalyst.kubewharf.io/guaranteed_cpus":           "4",
						},
					},
				},
			},
			requested: &framework.Resource{
				MilliCPU: 24000,
			},
			existPods: []*v1.Pod{
				{
					ObjectMeta: v12.ObjectMeta{
						Name: "pod01",
						UID:  "pod01",
					},
					Spec: v1.PodSpec{
						NodeName: "node1",
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
			pod: &v1.Pod{
				ObjectMeta: v12.ObjectMeta{
					Name: "pod1",
				},
				Spec: v1.PodSpec{
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
					},
				},
			},
			expectRes: nil,
		},
		{
			name: "node cpumanager on with recommend",
			node: &v1.Node{
				ObjectMeta: v12.ObjectMeta{
					Name: "node1",
					Annotations: map[string]string{
						"katalyst.kubewharf.io/cpu_overcommit_ratio":           "3.0",
						"katalyst.kubewharf.io/memory_overcommit_ratio":        "1.0",
						"katalyst.kubewharf.io/recommend_cpu_overcommit_ratio": "2.0",
						"katalyst.kubewharf.io/original_allocatable_cpu":       "16000",
					},
				},
				Status: v1.NodeStatus{
					Allocatable: map[v1.ResourceName]resource.Quantity{
						v1.ResourceCPU:    resource.MustParse("32"),
						v1.ResourceMemory: resource.MustParse("32Gi"),
					},
				},
			},
			cnrs: []*v1alpha1.CustomNodeResource{
				{
					ObjectMeta: v12.ObjectMeta{
						Name: "node1",
						Annotations: map[string]string{
							"katalyst.kubewharf.io/overcommit_cpu_manager":    "static",
							"katalyst.kubewharf.io/overcommit_memory_manager": "None",
							"katalyst.kubewharf.io/guaranteed_cpus":           "4",
						},
					},
				},
			},
			requested: &framework.Resource{
				MilliCPU: 24000,
			},
			existPods: []*v1.Pod{
				{
					ObjectMeta: v12.ObjectMeta{
						Name: "pod01",
						UID:  "pod01",
					},
					Spec: v1.PodSpec{
						NodeName: "node1",
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
			pod: &v1.Pod{
				ObjectMeta: v12.ObjectMeta{
					Name: "pod1",
				},
				Spec: v1.PodSpec{
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
					},
				},
			},
			expectRes: nil,
		},
		{
			name: "node cpumanager on 2",
			node: &v1.Node{
				ObjectMeta: v12.ObjectMeta{
					Name: "node1",
					Annotations: map[string]string{
						"katalyst.kubewharf.io/cpu_overcommit_ratio":     "2.0",
						"katalyst.kubewharf.io/memory_overcommit_ratio":  "1.0",
						"katalyst.kubewharf.io/original_allocatable_cpu": "16000m",
					},
				},
				Status: v1.NodeStatus{
					Allocatable: map[v1.ResourceName]resource.Quantity{
						v1.ResourceCPU:    resource.MustParse("32"),
						v1.ResourceMemory: resource.MustParse("32Gi"),
					},
				},
			},
			cnrs: []*v1alpha1.CustomNodeResource{
				{
					ObjectMeta: v12.ObjectMeta{
						Name: "node1",
						Annotations: map[string]string{
							"katalyst.kubewharf.io/overcommit_cpu_manager":    "static",
							"katalyst.kubewharf.io/overcommit_memory_manager": "None",
							"katalyst.kubewharf.io/guaranteed_cpus":           "8",
						},
					},
				},
			},
			requested: &framework.Resource{
				MilliCPU: 24000,
			},
			existPods: []*v1.Pod{
				{
					ObjectMeta: v12.ObjectMeta{
						Name: "pod01",
						UID:  "pod01",
					},
					Spec: v1.PodSpec{
						NodeName: "node1",
						Containers: []v1.Container{
							{
								Name: "testContainer",
								Resources: v1.ResourceRequirements{
									Requests: map[v1.ResourceName]resource.Quantity{
										v1.ResourceCPU:    resource.MustParse("8"),
										v1.ResourceMemory: resource.MustParse("8Gi"),
									},
									Limits: map[v1.ResourceName]resource.Quantity{
										v1.ResourceCPU:    resource.MustParse("8"),
										v1.ResourceMemory: resource.MustParse("8Gi"),
									},
								},
							},
						},
					},
				},
			},
			pod: &v1.Pod{
				ObjectMeta: v12.ObjectMeta{
					Name: "pod1",
				},
				Spec: v1.PodSpec{
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
					},
				},
			},
			expectRes: framework.NewStatus(framework.Unschedulable),
		},
		{
			name: "node cpumanager on 3",
			node: &v1.Node{
				ObjectMeta: v12.ObjectMeta{
					Name: "node1",
					Annotations: map[string]string{
						"katalyst.kubewharf.io/cpu_overcommit_ratio":     "2.0",
						"katalyst.kubewharf.io/memory_overcommit_ratio":  "1.0",
						"katalyst.kubewharf.io/original_allocatable_cpu": "16000m",
					},
				},
				Status: v1.NodeStatus{
					Allocatable: map[v1.ResourceName]resource.Quantity{
						v1.ResourceCPU:    resource.MustParse("32"),
						v1.ResourceMemory: resource.MustParse("32Gi"),
					},
				},
			},
			cnrs: []*v1alpha1.CustomNodeResource{
				{
					ObjectMeta: v12.ObjectMeta{
						Name: "node1",
						Annotations: map[string]string{
							"katalyst.kubewharf.io/overcommit_cpu_manager":    "static",
							"katalyst.kubewharf.io/overcommit_memory_manager": "None",
							"katalyst.kubewharf.io/guaranteed_cpus":           "4",
						},
					},
				},
			},
			requested: &framework.Resource{
				MilliCPU: 24000,
			},
			existPods: []*v1.Pod{
				{
					ObjectMeta: v12.ObjectMeta{
						Name: "pod01",
						UID:  "pod01",
					},
					Spec: v1.PodSpec{
						NodeName: "node1",
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
			pod: &v1.Pod{
				ObjectMeta: v12.ObjectMeta{
					Name: "pod1",
				},
				Spec: v1.PodSpec{
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
			expectRes: framework.NewStatus(framework.Unschedulable),
		},
		{
			name: "node memorymanager on, overcommit not allowed",
			node: &v1.Node{
				ObjectMeta: v12.ObjectMeta{
					Name: "node1",
					Annotations: map[string]string{
						"katalyst.kubewharf.io/cpu_overcommit_ratio":     "2.0",
						"katalyst.kubewharf.io/memory_overcommit_ratio":  "1.5",
						"katalyst.kubewharf.io/original_allocatable_cpu": "16000",
					},
				},
				Status: v1.NodeStatus{
					Allocatable: map[v1.ResourceName]resource.Quantity{
						v1.ResourceCPU:    resource.MustParse("32"),
						v1.ResourceMemory: resource.MustParse("32Gi"),
					},
				},
			},
			cnrs: []*v1alpha1.CustomNodeResource{
				{
					ObjectMeta: v12.ObjectMeta{
						Name: "node1",
						Annotations: map[string]string{
							"katalyst.kubewharf.io/overcommit_cpu_manager":    "none",
							"katalyst.kubewharf.io/overcommit_memory_manager": "Static",
							"katalyst.kubewharf.io/guaranteed_cpus":           "0",
						},
					},
				},
			},
			requested: &framework.Resource{
				MilliCPU: 0,
			},
			existPods: []*v1.Pod{},
			pod: &v1.Pod{
				ObjectMeta: v12.ObjectMeta{
					Name: "pod1",
				},
				Spec: v1.PodSpec{
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
					},
				},
			},
			expectRes: framework.NewStatus(framework.Unschedulable),
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			cnrs := tc.cnrs
			for _, cnr := range cnrs {
				cache.GetCache().AddOrUpdateCNR(cnr)
			}
			for _, pod := range tc.existPods {
				cache.GetCache().AddPod(pod)
			}
			defer func() {
				for _, cnr := range cnrs {
					cache.GetCache().RemoveCNR(cnr)
				}
			}()

			nodeInfo := framework.NewNodeInfo()
			nodeInfo.SetNode(tc.node)
			nodeInfo.Requested = tc.requested

			n := &NodeOvercommitment{}
			cs := framework.NewCycleState()
			res, stat := n.PreFilter(context.TODO(), cs, tc.pod)
			assert.Nil(t, res)
			assert.Nil(t, stat)

			status := n.Filter(context.TODO(), cs, tc.pod, nodeInfo)
			assert.Equal(t, tc.expectRes.Code(), status.Code())
		})
	}
}

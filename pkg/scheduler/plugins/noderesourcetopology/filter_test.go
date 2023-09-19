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

package noderesourcetopology

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/kubernetes/pkg/scheduler/apis/config"
	"k8s.io/kubernetes/pkg/scheduler/framework"

	"github.com/kubewharf/katalyst-api/pkg/apis/node/v1alpha1"
	"github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/config/generic"
	"github.com/kubewharf/katalyst-core/pkg/scheduler/cache"
	"github.com/kubewharf/katalyst-core/pkg/scheduler/util"
)

func makeTestFilterNodes(policy v1alpha1.TopologyPolicy) ([]*v1alpha1.CustomNodeResource, []string) {
	cnrs := []*v1alpha1.CustomNodeResource{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "node-2numa-8c16g"},
			Status: v1alpha1.CustomNodeResourceStatus{
				TopologyPolicy: policy,
				TopologyZone: []*v1alpha1.TopologyZone{
					{
						Name: "0",
						Type: v1alpha1.TopologyTypeSocket,
						Children: []*v1alpha1.TopologyZone{
							{
								Name: "0",
								Type: v1alpha1.TopologyTypeNuma,
								Resources: v1alpha1.Resources{
									Capacity: &v1.ResourceList{
										v1.ResourceCPU:    resource.MustParse("4"),
										v1.ResourceMemory: resource.MustParse("8Gi"),
										"Gpu":             resource.MustParse("4"),
									},
									Allocatable: &v1.ResourceList{
										v1.ResourceCPU:    resource.MustParse("4"),
										v1.ResourceMemory: resource.MustParse("8Gi"),
										"Gpu":             resource.MustParse("4"),
									},
								},
							},
							{
								Name: "1",
								Type: v1alpha1.TopologyTypeNuma,
								Resources: v1alpha1.Resources{
									Capacity: &v1.ResourceList{
										v1.ResourceCPU:    resource.MustParse("4"),
										v1.ResourceMemory: resource.MustParse("8Gi"),
										"Gpu":             resource.MustParse("4"),
									},
									Allocatable: &v1.ResourceList{
										v1.ResourceCPU:    resource.MustParse("4"),
										v1.ResourceMemory: resource.MustParse("8Gi"),
										"Gpu":             resource.MustParse("4"),
									},
								},
							},
						},
					},
				},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "node-2numa-4c8g"},
			Status: v1alpha1.CustomNodeResourceStatus{
				TopologyPolicy: policy,
				TopologyZone: []*v1alpha1.TopologyZone{
					{
						Name: "0",
						Type: v1alpha1.TopologyTypeSocket,
						Children: []*v1alpha1.TopologyZone{
							{
								Name: "0",
								Type: v1alpha1.TopologyTypeNuma,
								Resources: v1alpha1.Resources{
									Capacity: &v1.ResourceList{
										v1.ResourceCPU:    resource.MustParse("2"),
										v1.ResourceMemory: resource.MustParse("4Gi"),
										"Gpu":             resource.MustParse("2"),
									},
									Allocatable: &v1.ResourceList{
										v1.ResourceCPU:    resource.MustParse("2"),
										v1.ResourceMemory: resource.MustParse("4Gi"),
										"Gpu":             resource.MustParse("2"),
									},
								},
							},
							{
								Name: "1",
								Type: v1alpha1.TopologyTypeNuma,
								Resources: v1alpha1.Resources{
									Capacity: &v1.ResourceList{
										v1.ResourceCPU:    resource.MustParse("2"),
										v1.ResourceMemory: resource.MustParse("4Gi"),
										"Gpu":             resource.MustParse("2"),
									},
									Allocatable: &v1.ResourceList{
										v1.ResourceCPU:    resource.MustParse("2"),
										v1.ResourceMemory: resource.MustParse("4Gi"),
										"Gpu":             resource.MustParse("2"),
									},
								},
							},
						},
					},
				},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "node-2numa-8c16g-with-allocation"},
			Status: v1alpha1.CustomNodeResourceStatus{
				TopologyPolicy: policy,
				TopologyZone: []*v1alpha1.TopologyZone{
					{
						Name: "0",
						Type: v1alpha1.TopologyTypeSocket,
						Children: []*v1alpha1.TopologyZone{
							{
								Name: "0",
								Type: v1alpha1.TopologyTypeNuma,
								Resources: v1alpha1.Resources{
									Capacity: &v1.ResourceList{
										v1.ResourceCPU:    resource.MustParse("4"),
										v1.ResourceMemory: resource.MustParse("8Gi"),
										"Gpu":             resource.MustParse("4"),
									},
									Allocatable: &v1.ResourceList{
										v1.ResourceCPU:    resource.MustParse("4"),
										v1.ResourceMemory: resource.MustParse("8Gi"),
										"Gpu":             resource.MustParse("4"),
									},
								},
							},
							{
								Name: "1",
								Type: v1alpha1.TopologyTypeNuma,
								Resources: v1alpha1.Resources{
									Capacity: &v1.ResourceList{
										v1.ResourceCPU:    resource.MustParse("4"),
										v1.ResourceMemory: resource.MustParse("8Gi"),
										"Gpu":             resource.MustParse("4"),
									},
									Allocatable: &v1.ResourceList{
										v1.ResourceCPU:    resource.MustParse("4"),
										v1.ResourceMemory: resource.MustParse("8Gi"),
										"Gpu":             resource.MustParse("4"),
									},
								},
								Allocations: []*v1alpha1.Allocation{
									{
										Consumer: "testNamespace/testPod1/uid",
										Requests: &v1.ResourceList{
											v1.ResourceCPU:    resource.MustParse("1"),
											v1.ResourceMemory: resource.MustParse("2Gi"),
											"Gpu":             resource.MustParse("2"),
										},
									},
								},
							},
						},
					},
				},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "node-4numa-8c16g-cross-socket"},
			Status: v1alpha1.CustomNodeResourceStatus{
				TopologyPolicy: policy,
				TopologyZone: []*v1alpha1.TopologyZone{
					{
						Name: "0",
						Type: v1alpha1.TopologyTypeSocket,
						Children: []*v1alpha1.TopologyZone{
							{
								Name: "0",
								Type: v1alpha1.TopologyTypeNuma,
								Resources: v1alpha1.Resources{
									Capacity: &v1.ResourceList{
										v1.ResourceCPU:    resource.MustParse("2"),
										v1.ResourceMemory: resource.MustParse("4Gi"),
									},
									Allocatable: &v1.ResourceList{
										v1.ResourceCPU:    resource.MustParse("2"),
										v1.ResourceMemory: resource.MustParse("4Gi"),
									},
								},
							},
							{
								Name: "1",
								Type: v1alpha1.TopologyTypeNuma,
								Resources: v1alpha1.Resources{
									Capacity: &v1.ResourceList{
										v1.ResourceCPU:    resource.MustParse("2"),
										v1.ResourceMemory: resource.MustParse("4Gi"),
									},
									Allocatable: &v1.ResourceList{
										v1.ResourceCPU:    resource.MustParse("2"),
										v1.ResourceMemory: resource.MustParse("4Gi"),
									},
								},
								Allocations: []*v1alpha1.Allocation{
									{
										Consumer: "testNamespace/testPod2/uid",
										Requests: &v1.ResourceList{
											v1.ResourceCPU:    resource.MustParse("1"),
											v1.ResourceMemory: resource.MustParse("2Gi"),
										},
									},
								},
							},
						},
					},
					{
						Name: "1",
						Type: v1alpha1.TopologyTypeSocket,
						Children: []*v1alpha1.TopologyZone{
							{
								Name: "2",
								Type: v1alpha1.TopologyTypeNuma,
								Resources: v1alpha1.Resources{
									Capacity: &v1.ResourceList{
										v1.ResourceCPU:    resource.MustParse("2"),
										v1.ResourceMemory: resource.MustParse("4Gi"),
									},
									Allocatable: &v1.ResourceList{
										v1.ResourceCPU:    resource.MustParse("2"),
										v1.ResourceMemory: resource.MustParse("4Gi"),
									},
								},
								Allocations: []*v1alpha1.Allocation{
									{
										Consumer: "testNamespace/testPod3/uid",
										Requests: &v1.ResourceList{
											v1.ResourceCPU:    resource.MustParse("1"),
											v1.ResourceMemory: resource.MustParse("2Gi"),
										},
									},
								},
							},
							{
								Name: "3",
								Type: v1alpha1.TopologyTypeNuma,
								Resources: v1alpha1.Resources{
									Capacity: &v1.ResourceList{
										v1.ResourceCPU:    resource.MustParse("2"),
										v1.ResourceMemory: resource.MustParse("4Gi"),
									},
									Allocatable: &v1.ResourceList{
										v1.ResourceCPU:    resource.MustParse("2"),
										v1.ResourceMemory: resource.MustParse("4Gi"),
									},
								},
							},
						},
					},
				},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "node-4numa-8c16g-full-socket"},
			Status: v1alpha1.CustomNodeResourceStatus{
				TopologyPolicy: policy,
				TopologyZone: []*v1alpha1.TopologyZone{
					{
						Name: "0",
						Type: v1alpha1.TopologyTypeSocket,
						Children: []*v1alpha1.TopologyZone{
							{
								Name: "0",
								Type: v1alpha1.TopologyTypeNuma,
								Resources: v1alpha1.Resources{
									Capacity: &v1.ResourceList{
										v1.ResourceCPU:    resource.MustParse("2"),
										v1.ResourceMemory: resource.MustParse("4Gi"),
									},
									Allocatable: &v1.ResourceList{
										v1.ResourceCPU:    resource.MustParse("2"),
										v1.ResourceMemory: resource.MustParse("4Gi"),
									},
								},
							},
							{
								Name: "1",
								Type: v1alpha1.TopologyTypeNuma,
								Resources: v1alpha1.Resources{
									Capacity: &v1.ResourceList{
										v1.ResourceCPU:    resource.MustParse("2"),
										v1.ResourceMemory: resource.MustParse("4Gi"),
									},
									Allocatable: &v1.ResourceList{
										v1.ResourceCPU:    resource.MustParse("2"),
										v1.ResourceMemory: resource.MustParse("4Gi"),
									},
								},
							},
						},
					},
					{
						Name: "1",
						Type: v1alpha1.TopologyTypeSocket,
						Children: []*v1alpha1.TopologyZone{
							{
								Name: "2",
								Type: v1alpha1.TopologyTypeNuma,
								Resources: v1alpha1.Resources{
									Capacity: &v1.ResourceList{
										v1.ResourceCPU:    resource.MustParse("2"),
										v1.ResourceMemory: resource.MustParse("4Gi"),
									},
									Allocatable: &v1.ResourceList{
										v1.ResourceCPU:    resource.MustParse("2"),
										v1.ResourceMemory: resource.MustParse("4Gi"),
									},
								},
								Allocations: []*v1alpha1.Allocation{
									{
										Consumer: "testNamespace/testPod5/uid",
										Requests: &v1.ResourceList{
											v1.ResourceCPU:    resource.MustParse("1"),
											v1.ResourceMemory: resource.MustParse("2Gi"),
										},
									},
								},
							},
							{
								Name: "3",
								Type: v1alpha1.TopologyTypeNuma,
								Resources: v1alpha1.Resources{
									Capacity: &v1.ResourceList{
										v1.ResourceCPU:    resource.MustParse("2"),
										v1.ResourceMemory: resource.MustParse("4Gi"),
									},
									Allocatable: &v1.ResourceList{
										v1.ResourceCPU:    resource.MustParse("2"),
										v1.ResourceMemory: resource.MustParse("4Gi"),
									},
								},
								Allocations: []*v1alpha1.Allocation{
									{
										Consumer: "testNamespace/testPod4/uid",
										Requests: &v1.ResourceList{
											v1.ResourceCPU:    resource.MustParse("1"),
											v1.ResourceMemory: resource.MustParse("2Gi"),
										},
									},
								},
							},
						},
					},
				},
			},
		},
	}
	return cnrs, []string{"node-2numa-8c16g", "node-2numa-4c8g", "node-2numa-8c16g-with-allocation", "node-4numa-8c16g-cross-socket", "node-4numa-8c16g-full-socket"}
}

func TestFilterNative(t *testing.T) {
	type testCase struct {
		name            string
		policy          v1alpha1.TopologyPolicy
		alignedResource []string
		pod             *v1.Pod
		wantRes         map[string]*framework.Status
	}

	nativeTestCase := []testCase{
		{
			// 4C8G pod with a 4C8G container, can not be allocated on 2C4G NUMA node
			name:            "native pod + single numa",
			policy:          v1alpha1.TopologyPolicySingleNUMANodeContainerLevel,
			alignedResource: []string{"cpu", "memory"},
			pod: makePodByResourceList(&v1.ResourceList{
				v1.ResourceCPU:    resource.MustParse("4"),
				v1.ResourceMemory: resource.MustParse("8Gi"),
			}, map[string]string{}),
			wantRes: map[string]*framework.Status{
				"node-2numa-8c16g":                 nil,
				"node-2numa-4c8g":                  framework.NewStatus(2),
				"node-2numa-8c16g-with-allocation": nil,
				"node-4numa-8c16g-cross-socket":    framework.NewStatus(2),
				"node-4numa-8c16g-full-socket":     framework.NewStatus(2),
			},
		},
		{
			// 4C8G pod with two 2C4G container, both containers can be allocated in a NUMA node
			name:            "native pod multi container + single numa",
			policy:          v1alpha1.TopologyPolicySingleNUMANodeContainerLevel,
			alignedResource: []string{"cpu", "memory"},
			pod: makePodByResourceLists([]v1.ResourceList{
				{
					v1.ResourceCPU:    resource.MustParse("2"),
					v1.ResourceMemory: resource.MustParse("4Gi"),
				}, {
					v1.ResourceCPU:    resource.MustParse("2"),
					v1.ResourceMemory: resource.MustParse("4Gi"),
				},
			}, map[string]string{}),
			wantRes: map[string]*framework.Status{
				"node-2numa-8c16g":                 nil,
				"node-2numa-4c8g":                  nil,
				"node-2numa-8c16g-with-allocation": nil,
				"node-4numa-8c16g-cross-socket":    nil,
				"node-4numa-8c16g-full-socket":     nil,
			},
		},
		{
			name:            "native pod multi container + pod single numa",
			policy:          v1alpha1.TopologyPolicySingleNUMANodePodLevel,
			alignedResource: []string{"cpu", "memory"},
			pod: makePodByResourceLists([]v1.ResourceList{
				{
					v1.ResourceCPU:    resource.MustParse("2"),
					v1.ResourceMemory: resource.MustParse("4Gi"),
				}, {
					v1.ResourceCPU:    resource.MustParse("2"),
					v1.ResourceMemory: resource.MustParse("4Gi"),
				},
			}, map[string]string{}),
			wantRes: map[string]*framework.Status{
				"node-2numa-8c16g":                 nil,
				"node-2numa-4c8g":                  framework.NewStatus(2),
				"node-2numa-8c16g-with-allocation": nil,
				"node-4numa-8c16g-cross-socket":    framework.NewStatus(2),
				"node-4numa-8c16g-full-socket":     framework.NewStatus(2),
			},
		},
		{
			name:            "native pod + numeric",
			policy:          v1alpha1.TopologyPolicyNumericContainerLevel,
			alignedResource: []string{"cpu", "memory"},
			pod: makePodByResourceList(&v1.ResourceList{
				v1.ResourceCPU:    resource.MustParse("4"),
				v1.ResourceMemory: resource.MustParse("8Gi"),
			}, map[string]string{}),
			wantRes: map[string]*framework.Status{
				"node-2numa-8c16g":                 nil,
				"node-2numa-4c8g":                  nil,
				"node-2numa-8c16g-with-allocation": nil,
				"node-4numa-8c16g-cross-socket":    nil,
				"node-4numa-8c16g-full-socket":     nil,
			},
		},
		{
			name:            "native pod without full cpu",
			policy:          v1alpha1.TopologyPolicySingleNUMANodeContainerLevel,
			alignedResource: []string{"cpu", "memory"},
			pod: makePodByResourceList(&v1.ResourceList{
				v1.ResourceCPU:    resource.MustParse("350m"),
				v1.ResourceMemory: resource.MustParse("8Gi"),
			}, map[string]string{}),
			wantRes: map[string]*framework.Status{
				"node-2numa-8c16g":                 nil,
				"node-2numa-4c8g":                  nil,
				"node-2numa-8c16g-with-allocation": nil,
				"node-4numa-8c16g-cross-socket":    nil,
				"node-4numa-8c16g-full-socket":     nil,
			},
		},
		{
			name:            "native pod with non numa level resource",
			policy:          v1alpha1.TopologyPolicySingleNUMANodeContainerLevel,
			alignedResource: []string{"cpu", "memory"},
			pod: makePodByResourceList(&v1.ResourceList{
				v1.ResourceCPU:    resource.MustParse("4"),
				v1.ResourceMemory: resource.MustParse("8Gi"),
				"a/b":             resource.MustParse("200Gi"),
			}, map[string]string{}),
			wantRes: map[string]*framework.Status{
				"node-2numa-8c16g":                 nil,
				"node-2numa-4c8g":                  framework.NewStatus(2),
				"node-2numa-8c16g-with-allocation": nil,
				"node-4numa-8c16g-cross-socket":    framework.NewStatus(2),
				"node-4numa-8c16g-full-socket":     framework.NewStatus(2),
			},
		},
	}

	c := cache.GetCache()
	util.SetQoSConfig(generic.NewQoSConfiguration())
	for _, tc := range nativeTestCase {
		cnrs, nodes := makeTestFilterNodes(tc.policy)
		for _, cnr := range cnrs {
			c.AddOrUpdateCNR(cnr)
		}

		tm, err := MakeTestTm(MakeTestArgs(config.MostAllocated, tc.alignedResource, "native"))
		assert.NoError(t, err)

		ret := make(map[string]*framework.Status)
		for _, node := range nodes {
			n := &v1.Node{}
			n.SetName(node)
			nodeInfo := framework.NewNodeInfo()
			nodeInfo.SetNode(n)
			status := tm.(*TopologyMatch).Filter(context.TODO(), nil, tc.pod, nodeInfo)
			ret[node] = status
		}
		// check result
		for wantN, wantS := range tc.wantRes {
			if wantS == nil {
				assert.Nil(t, ret[wantN])
			} else {
				assert.Equal(t, wantS.Code(), ret[wantN].Code())
			}
		}
	}
}

func TestFilterDedicatedNumaBinding(t *testing.T) {
	type testCase struct {
		name            string
		policy          v1alpha1.TopologyPolicy
		alignedResource []string
		pod             *v1.Pod
		wantRes         map[string]*framework.Status
	}

	numaBindingCase := []testCase{
		{
			name:            "dedicated + numaBinding + single numa",
			policy:          v1alpha1.TopologyPolicySingleNUMANodeContainerLevel,
			alignedResource: []string{"cpu", "memory"},
			pod: makePodByResourceList(&v1.ResourceList{
				v1.ResourceCPU:    resource.MustParse("4"),
				v1.ResourceMemory: resource.MustParse("8Gi"),
			}, map[string]string{
				consts.PodAnnotationQoSLevelKey:          consts.PodAnnotationQoSLevelDedicatedCores,
				consts.PodAnnotationMemoryEnhancementKey: `{"numa_binding":"true"}`,
			}),
			wantRes: map[string]*framework.Status{
				"node-2numa-8c16g":                 nil,
				"node-2numa-4c8g":                  framework.NewStatus(2),
				"node-2numa-8c16g-with-allocation": nil,
				"node-4numa-8c16g-cross-socket":    framework.NewStatus(2),
				"node-4numa-8c16g-full-socket":     framework.NewStatus(2),
			},
		},
		{
			name:            "dedicated + numabinding + numeric",
			policy:          v1alpha1.TopologyPolicyNumericContainerLevel,
			alignedResource: []string{"cpu", "memory"},
			pod: makePodByResourceList(&v1.ResourceList{
				v1.ResourceCPU:    resource.MustParse("4"),
				v1.ResourceMemory: resource.MustParse("8Gi"),
				"Gpu":             resource.MustParse("6"),
			}, map[string]string{
				consts.PodAnnotationQoSLevelKey:          consts.PodAnnotationQoSLevelDedicatedCores,
				consts.PodAnnotationMemoryEnhancementKey: `{"numa_binding":"true"}`,
			}),
			wantRes: map[string]*framework.Status{
				"node-2numa-8c16g":                 nil,
				"node-2numa-4c8g":                  framework.NewStatus(2),
				"node-2numa-8c16g-with-allocation": nil,
				"node-4numa-8c16g-cross-socket":    framework.NewStatus(2),
				"node-4numa-8c16g-full-socket":     framework.NewStatus(2),
			},
		},
		{
			name:            "dedicated + numaBinding + numeric + Gpu aligned",
			policy:          v1alpha1.TopologyPolicyNumericContainerLevel,
			alignedResource: []string{"cpu", "memory", "Gpu"},
			pod: makePodByResourceList(&v1.ResourceList{
				v1.ResourceCPU:    resource.MustParse("4"),
				v1.ResourceMemory: resource.MustParse("8Gi"),
				"Gpu":             resource.MustParse("6"), // Gpu should be aligned, need two NUMA, but numabinding can only use one NUMA
			}, map[string]string{
				consts.PodAnnotationQoSLevelKey:          consts.PodAnnotationQoSLevelDedicatedCores,
				consts.PodAnnotationMemoryEnhancementKey: `{"numa_binding":"true"}`,
			}),
			wantRes: map[string]*framework.Status{
				"node-2numa-8c16g":                 framework.NewStatus(2),
				"node-2numa-4c8g":                  framework.NewStatus(2),
				"node-2numa-8c16g-with-allocation": framework.NewStatus(2),
				"node-4numa-8c16g-cross-socket":    framework.NewStatus(2),
				"node-4numa-8c16g-full-socket":     framework.NewStatus(2),
			},
		},
		{
			name:            "dedicated + multiContainer + numaBinding + numeric",
			policy:          v1alpha1.TopologyPolicyNumericContainerLevel,
			alignedResource: []string{"cpu", "memory"},
			pod: makePodByResourceLists([]v1.ResourceList{
				{
					v1.ResourceCPU:    resource.MustParse("2"),
					v1.ResourceMemory: resource.MustParse("4Gi"),
				}, {
					v1.ResourceCPU:    resource.MustParse("2"),
					v1.ResourceMemory: resource.MustParse("4Gi"),
				},
			}, map[string]string{
				consts.PodAnnotationQoSLevelKey:          consts.PodAnnotationQoSLevelDedicatedCores,
				consts.PodAnnotationMemoryEnhancementKey: `{"numa_binding":"true"}`,
			}),
			wantRes: map[string]*framework.Status{
				"node-2numa-8c16g":                 nil,
				"node-2numa-4c8g":                  nil,
				"node-2numa-8c16g-with-allocation": nil,
				"node-4numa-8c16g-cross-socket":    nil,
				"node-4numa-8c16g-full-socket":     nil,
			},
		},
		{
			name:            "dedicated + multiContainer + numaBinding + numeric + Gpu not aligned",
			policy:          v1alpha1.TopologyPolicyNumericContainerLevel,
			alignedResource: []string{"cpu", "memory"},
			pod: makePodByResourceLists([]v1.ResourceList{
				{
					v1.ResourceCPU:    resource.MustParse("2"),
					v1.ResourceMemory: resource.MustParse("4Gi"),
					"Gpu":             resource.MustParse("3"),
				}, {
					v1.ResourceCPU:    resource.MustParse("2"),
					v1.ResourceMemory: resource.MustParse("4Gi"),
					"Gpu":             resource.MustParse("3"),
				},
			}, map[string]string{
				consts.PodAnnotationQoSLevelKey:          consts.PodAnnotationQoSLevelDedicatedCores,
				consts.PodAnnotationMemoryEnhancementKey: `{"numa_binding":"true"}`,
			}),
			wantRes: map[string]*framework.Status{
				"node-2numa-8c16g":                 nil,
				"node-2numa-4c8g":                  nil,
				"node-2numa-8c16g-with-allocation": nil,
				"node-4numa-8c16g-cross-socket":    nil,
				"node-4numa-8c16g-full-socket":     nil,
			},
		},
		{
			name:            "dedicated + multiContainer + numaBinding + numeric + Gpu aligned",
			policy:          v1alpha1.TopologyPolicyNumericContainerLevel,
			alignedResource: []string{"cpu", "memory", "Gpu"},
			pod: makePodByResourceLists([]v1.ResourceList{
				{
					v1.ResourceCPU:    resource.MustParse("2"),
					v1.ResourceMemory: resource.MustParse("4Gi"),
					"Gpu":             resource.MustParse("3"),
				}, {
					v1.ResourceCPU:    resource.MustParse("2"),
					v1.ResourceMemory: resource.MustParse("4Gi"),
					"Gpu":             resource.MustParse("3"),
				},
			}, map[string]string{
				consts.PodAnnotationQoSLevelKey:          consts.PodAnnotationQoSLevelDedicatedCores,
				consts.PodAnnotationMemoryEnhancementKey: `{"numa_binding":"true"}`,
			}),
			wantRes: map[string]*framework.Status{
				"node-2numa-8c16g":                 nil,
				"node-2numa-4c8g":                  framework.NewStatus(2),
				"node-2numa-8c16g-with-allocation": nil,
				"node-4numa-8c16g-cross-socket":    nil,
				"node-4numa-8c16g-full-socket":     nil,
			},
		},
	}

	c := cache.GetCache()
	util.SetQoSConfig(generic.NewQoSConfiguration())
	for _, tc := range numaBindingCase {
		cnrs, nodes := makeTestFilterNodes(tc.policy)
		for _, cnr := range cnrs {
			c.AddOrUpdateCNR(cnr)
		}

		tm, err := MakeTestTm(MakeTestArgs(config.MostAllocated, tc.alignedResource, "dynamic"))
		assert.NoError(t, err)

		ret := make(map[string]*framework.Status)
		for _, node := range nodes {
			n := &v1.Node{}
			n.SetName(node)
			nodeInfo := framework.NewNodeInfo()
			nodeInfo.SetNode(n)
			status := tm.(*TopologyMatch).Filter(context.TODO(), nil, tc.pod, nodeInfo)
			ret[node] = status
		}
		// check result
		for wantN, wantS := range tc.wantRes {
			if wantS == nil {
				assert.Nil(t, ret[wantN])
			} else {
				assert.Equal(t, wantS.Code(), ret[wantN].Code())
			}
		}
	}
}

func TestFilterDedicatedExclusive(t *testing.T) {
	type testCase struct {
		name            string
		policy          v1alpha1.TopologyPolicy
		alignedResource []string
		pod             *v1.Pod
		wantRes         map[string]*framework.Status
	}

	numaExclusiveCase := []testCase{
		{
			name:            "dedicated + exclusive + single numa",
			policy:          v1alpha1.TopologyPolicySingleNUMANodeContainerLevel,
			alignedResource: []string{"cpu", "memory"},
			pod: makePodByResourceList(&v1.ResourceList{
				v1.ResourceCPU:    resource.MustParse("2"),
				v1.ResourceMemory: resource.MustParse("4Gi"),
			}, map[string]string{
				consts.PodAnnotationQoSLevelKey:          consts.PodAnnotationQoSLevelDedicatedCores,
				consts.PodAnnotationMemoryEnhancementKey: `{"numa_binding":"true","numa_exclusive":"true"}`,
			}),
			wantRes: map[string]*framework.Status{
				"node-2numa-8c16g":                 nil,
				"node-2numa-4c8g":                  nil,
				"node-2numa-8c16g-with-allocation": nil,
				"node-4numa-8c16g-cross-socket":    nil,
				"node-4numa-8c16g-full-socket":     nil,
			},
		},
		{
			name:            "dedicated + exclusive + single numa + multi container",
			policy:          v1alpha1.TopologyPolicySingleNUMANodeContainerLevel,
			alignedResource: []string{"cpu", "memory"},
			pod: makePodByResourceLists([]v1.ResourceList{
				{
					v1.ResourceCPU:    resource.MustParse("2"),
					v1.ResourceMemory: resource.MustParse("4Gi"),
				},
				{
					v1.ResourceCPU:    resource.MustParse("2"),
					v1.ResourceMemory: resource.MustParse("4Gi"),
				},
			}, map[string]string{
				consts.PodAnnotationQoSLevelKey:          consts.PodAnnotationQoSLevelDedicatedCores,
				consts.PodAnnotationMemoryEnhancementKey: `{"numa_binding":"true","numa_exclusive":"true"}`,
			}),
			wantRes: map[string]*framework.Status{
				"node-2numa-8c16g":                 nil,
				"node-2numa-4c8g":                  nil,
				"node-2numa-8c16g-with-allocation": nil,
				"node-4numa-8c16g-cross-socket":    nil,
				"node-4numa-8c16g-full-socket":     nil,
			},
		},
		{
			name:            "dedicated + exclusive + numeric",
			policy:          v1alpha1.TopologyPolicyNumericContainerLevel,
			alignedResource: []string{"cpu", "memory"},
			pod: makePodByResourceList(&v1.ResourceList{
				v1.ResourceCPU:    resource.MustParse("4"),
				v1.ResourceMemory: resource.MustParse("8Gi"),
			}, map[string]string{
				consts.PodAnnotationQoSLevelKey:          consts.PodAnnotationQoSLevelDedicatedCores,
				consts.PodAnnotationMemoryEnhancementKey: `{"numa_binding":"true","numa_exclusive":"true"}`,
			}),
			wantRes: map[string]*framework.Status{
				"node-2numa-8c16g":                 nil,                    // one NUMAs
				"node-2numa-4c8g":                  nil,                    // two NUMAs
				"node-2numa-8c16g-with-allocation": nil,                    // one NUMA
				"node-4numa-8c16g-cross-socket":    framework.NewStatus(2), // two NUMAs cross socket, not satisfy
				"node-4numa-8c16g-full-socket":     nil,                    // two NUMAs
			},
		},
		{
			name:            "dedicated + exclusive + numeric + multi container",
			policy:          v1alpha1.TopologyPolicyNumericContainerLevel,
			alignedResource: []string{"cpu", "memory"},
			pod: makePodByResourceLists([]v1.ResourceList{
				{
					v1.ResourceCPU:    resource.MustParse("2"),
					v1.ResourceMemory: resource.MustParse("4Gi"),
				},
				{
					v1.ResourceCPU:    resource.MustParse("2"),
					v1.ResourceMemory: resource.MustParse("4Gi"),
				},
			}, map[string]string{
				consts.PodAnnotationQoSLevelKey:          consts.PodAnnotationQoSLevelDedicatedCores,
				consts.PodAnnotationMemoryEnhancementKey: `{"numa_binding":"true","numa_exclusive":"true"}`,
			}),
			wantRes: map[string]*framework.Status{
				"node-2numa-8c16g":                 nil,
				"node-2numa-4c8g":                  nil,
				"node-2numa-8c16g-with-allocation": nil,
				"node-4numa-8c16g-cross-socket":    nil,
				"node-4numa-8c16g-full-socket":     nil,
			},
		},
	}

	c := cache.GetCache()
	util.SetQoSConfig(generic.NewQoSConfiguration())
	for _, tc := range numaExclusiveCase {
		cnrs, nodes := makeTestFilterNodes(tc.policy)
		for _, cnr := range cnrs {
			c.AddOrUpdateCNR(cnr)
		}

		tm, err := MakeTestTm(MakeTestArgs(config.MostAllocated, tc.alignedResource, "dynamic"))
		assert.NoError(t, err)

		ret := make(map[string]*framework.Status)
		for _, node := range nodes {
			n := &v1.Node{}
			n.SetName(node)
			nodeInfo := framework.NewNodeInfo()
			nodeInfo.SetNode(n)
			status := tm.(*TopologyMatch).Filter(context.TODO(), nil, tc.pod, nodeInfo)
			ret[node] = status
		}
		// check result
		for wantN, wantS := range tc.wantRes {
			if wantS == nil {
				assert.Nil(t, ret[wantN])
			} else {
				assert.Equal(t, wantS.Code(), ret[wantN].Code())
			}
		}
	}
}

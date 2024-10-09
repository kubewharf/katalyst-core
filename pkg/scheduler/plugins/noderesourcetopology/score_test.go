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
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/kubernetes/pkg/scheduler/apis/config"
	"k8s.io/kubernetes/pkg/scheduler/framework"
	"k8s.io/kubernetes/pkg/scheduler/framework/runtime"

	"github.com/kubewharf/katalyst-api/pkg/apis/node/v1alpha1"
	"github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/config/generic"
	"github.com/kubewharf/katalyst-core/pkg/scheduler/cache"
	"github.com/kubewharf/katalyst-core/pkg/scheduler/util"
)

func makeTestScoreNodes(policy v1alpha1.TopologyPolicy) ([]*v1alpha1.CustomNodeResource, []string, []*v1.Pod) {
	cnrs := []*v1alpha1.CustomNodeResource{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "score-node-2numa-8c16g"},
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
			ObjectMeta: metav1.ObjectMeta{Name: "score-node-2numa-4c8g"},
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
			ObjectMeta: metav1.ObjectMeta{Name: "score-node-2numa-8c16g-with-allocation"},
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
								Allocations: []*v1alpha1.Allocation{
									{
										Consumer: "testNamespace/testPod1/uid",
										Requests: &v1.ResourceList{
											v1.ResourceCPU:    resource.MustParse("1"),
											v1.ResourceMemory: resource.MustParse("2Gi"),
										},
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
										Consumer: "testNamespace/testPod2/uid",
										Requests: &v1.ResourceList{
											v1.ResourceCPU:    resource.MustParse("3"),
											v1.ResourceMemory: resource.MustParse("6Gi"),
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
			ObjectMeta: metav1.ObjectMeta{Name: "score-node-4numa-8c16g"},
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
	}

	pods := []*v1.Pod{
		{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: "testNamespace",
				Name:      "testPod1",
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
				},
			},
			Spec: v1.PodSpec{
				NodeName: "score-node-2numa-8c16g-with-allocation",
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: "testNamespace",
				Name:      "testPod2",
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
				},
			},
			Spec: v1.PodSpec{
				NodeName: "score-node-2numa-8c16g-with-allocation",
			},
		},
	}
	return cnrs, []string{"score-node-2numa-8c16g", "score-node-2numa-4c8g", "score-node-2numa-8c16g-with-allocation", "score-node-4numa-8c16g"}, pods
}

func TestScore(t *testing.T) {
	t.Parallel()
	type testCase struct {
		name            string
		policy          v1alpha1.TopologyPolicy
		strategy        config.ScoringStrategyType
		alignedResource []string
		pod             *v1.Pod
		wantStatus      *framework.Status
		wantRes         map[string]int64
	}

	nativeTestCases := []testCase{
		{
			name:            "native pod + single numa + MostAllocated strategy",
			policy:          v1alpha1.TopologyPolicySingleNUMANodeContainerLevel,
			strategy:        config.MostAllocated,
			alignedResource: []string{"cpu", "memory"},
			wantRes: map[string]int64{
				"node-2numa-8c16g":                 100, // just binpack a numanode full
				"node-2numa-4c8g":                  0,   // not satisfy
				"node-2numa-8c16g-with-allocation": 0,
				"node-4numa-8c16g":                 0, // not satisfy
			},
			wantStatus: nil,
			pod: makePodByResourceList(&v1.ResourceList{
				v1.ResourceCPU:    resource.MustParse("4"),
				v1.ResourceMemory: resource.MustParse("8Gi"),
			}, map[string]string{}),
		},
		{
			name:            "native pod + single numa + LeastAllocated strategy",
			policy:          v1alpha1.TopologyPolicySingleNUMANodeContainerLevel,
			strategy:        config.LeastAllocated,
			alignedResource: []string{"cpu", "memory"},
			wantRes: map[string]int64{
				"node-2numa-8c16g":                 50,
				"node-2numa-4c8g":                  0,
				"node-2numa-8c16g-with-allocation": 33,
				"node-4numa-8c16g":                 0,
			},
			wantStatus: nil,
			pod: makePodByResourceList(&v1.ResourceList{
				v1.ResourceCPU:    resource.MustParse("2"),
				v1.ResourceMemory: resource.MustParse("4Gi"),
			}, map[string]string{}),
		},
		{
			name:            "native pod + single numa + BalancedAllocation strategy",
			policy:          v1alpha1.TopologyPolicySingleNUMANodeContainerLevel,
			strategy:        consts.BalancedAllocation,
			alignedResource: []string{"cpu", "memory"},
			wantRes: map[string]int64{
				"node-2numa-8c16g":                 100,
				"node-2numa-4c8g":                  100,
				"node-2numa-8c16g-with-allocation": 100,
				"node-4numa-8c16g":                 100,
			},
			wantStatus: nil,
			pod: makePodByResourceList(&v1.ResourceList{
				v1.ResourceCPU:    resource.MustParse("2"),
				v1.ResourceMemory: resource.MustParse("4Gi"),
			}, map[string]string{}),
		},
		{
			name:            "native pod + no single numa nodes",
			policy:          v1alpha1.TopologyPolicyNumericContainerLevel,
			strategy:        config.MostAllocated,
			alignedResource: []string{"cpu", "memory"},
			wantRes: map[string]int64{
				// not best node
				"node-2numa-8c16g":                 0,
				"node-2numa-4c8g":                  0,
				"node-2numa-8c16g-with-allocation": 0,
				"node-4numa-8c16g":                 0,
			},
			wantStatus: nil,
			pod: makePodByResourceList(&v1.ResourceList{
				v1.ResourceCPU:    resource.MustParse("4"),
				v1.ResourceMemory: resource.MustParse("8Gi"),
			}, map[string]string{}),
		},
		{
			name:            "native pod + not Guaranteed",
			policy:          v1alpha1.TopologyPolicySingleNUMANodeContainerLevel,
			strategy:        config.MostAllocated,
			alignedResource: []string{"cpu", "memory"},
			wantRes: map[string]int64{
				// skip score
				"node-2numa-8c16g":                 100,
				"node-2numa-4c8g":                  100,
				"node-2numa-8c16g-with-allocation": 100,
				"node-4numa-8c16g":                 100,
			},
			wantStatus: nil,
			pod: makePodByResourceList(&v1.ResourceList{
				v1.ResourceCPU:    resource.MustParse("350m"),
				v1.ResourceMemory: resource.MustParse("8Gi"),
			}, map[string]string{}),
		},
	}

	dedicatedTestCases := []testCase{
		{
			name:            "dedicated_cores with numabinding + single numa + MostAllocated strategy",
			policy:          v1alpha1.TopologyPolicySingleNUMANodeContainerLevel,
			strategy:        config.MostAllocated,
			alignedResource: []string{"cpu", "memory"},
			wantRes: map[string]int64{
				"node-2numa-8c16g":                 50,
				"node-2numa-4c8g":                  100,
				"node-2numa-8c16g-with-allocation": 66,
				"node-4numa-8c16g":                 100,
			},
			pod: makePodByResourceList(&v1.ResourceList{
				v1.ResourceCPU:    resource.MustParse("2"),
				v1.ResourceMemory: resource.MustParse("4Gi"),
				"Gpu":             resource.MustParse("1"),
			}, map[string]string{
				consts.PodAnnotationQoSLevelKey:          consts.PodAnnotationQoSLevelDedicatedCores,
				consts.PodAnnotationMemoryEnhancementKey: `{"numa_binding":"true"}`,
			}),
		},
		{
			name:            "dedicated_cores with numabinding + single numa + LeastAllocated strategy",
			policy:          v1alpha1.TopologyPolicySingleNUMANodeContainerLevel,
			strategy:        config.LeastAllocated,
			alignedResource: []string{"cpu", "memory"},
			wantRes: map[string]int64{
				"node-2numa-8c16g":                 50,
				"node-2numa-4c8g":                  0,
				"node-2numa-8c16g-with-allocation": 33,
				"node-4numa-8c16g":                 0,
			},
			pod: makePodByResourceList(&v1.ResourceList{
				v1.ResourceCPU:    resource.MustParse("2"),
				v1.ResourceMemory: resource.MustParse("4Gi"),
				"Gpu":             resource.MustParse("1"),
			}, map[string]string{
				consts.PodAnnotationQoSLevelKey:          consts.PodAnnotationQoSLevelDedicatedCores,
				consts.PodAnnotationMemoryEnhancementKey: `{"numa_binding":"true"}`,
			}),
		},
		{
			name:            "dedicated_cores without numabinding",
			policy:          v1alpha1.TopologyPolicySingleNUMANodeContainerLevel,
			strategy:        config.LeastAllocated,
			alignedResource: []string{"cpu", "memory"},
			pod: makePodByResourceList(&v1.ResourceList{
				v1.ResourceCPU:    resource.MustParse("2"),
				v1.ResourceMemory: resource.MustParse("4Gi"),
				"Gpu":             resource.MustParse("1"),
			}, map[string]string{
				consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
			}),
			// ignore score
			wantRes: map[string]int64{
				"node-2numa-8c16g":                 100,
				"node-2numa-4c8g":                  100,
				"node-2numa-8c16g-with-allocation": 100,
				"node-4numa-8c16g":                 100,
			},
		},
		{
			name:            "dedicated_cores with numa_exclusive + single numa + MostAllocated",
			policy:          v1alpha1.TopologyPolicySingleNUMANodeContainerLevel,
			strategy:        config.MostAllocated,
			alignedResource: []string{"cpu", "memory"},
			wantRes: map[string]int64{
				"node-2numa-8c16g":                 50,
				"node-2numa-4c8g":                  100,
				"node-2numa-8c16g-with-allocation": 0,
				"node-4numa-8c16g":                 100,
			},
			pod: makePodByResourceList(&v1.ResourceList{
				v1.ResourceCPU:    resource.MustParse("2"),
				v1.ResourceMemory: resource.MustParse("4Gi"),
				"Gpu":             resource.MustParse("1"),
			}, map[string]string{
				consts.PodAnnotationQoSLevelKey:          consts.PodAnnotationQoSLevelDedicatedCores,
				consts.PodAnnotationMemoryEnhancementKey: `{"numa_binding":"true","numa_exclusive":"true"}`,
			}),
		},
		{
			name:            "dedicaetd_cores with numa_exclusive + single numa + LeastAllocated",
			policy:          v1alpha1.TopologyPolicySingleNUMANodeContainerLevel,
			strategy:        config.LeastAllocated,
			alignedResource: []string{"cpu", "memory"},
			wantRes: map[string]int64{
				"node-2numa-8c16g":                 50,
				"node-2numa-4c8g":                  0,
				"node-2numa-8c16g-with-allocation": 0,
				"node-4numa-8c16g":                 0,
			},
			pod: makePodByResourceList(&v1.ResourceList{
				v1.ResourceCPU:    resource.MustParse("2"),
				v1.ResourceMemory: resource.MustParse("4Gi"),
				"Gpu":             resource.MustParse("1"),
			}, map[string]string{
				consts.PodAnnotationQoSLevelKey:          consts.PodAnnotationQoSLevelDedicatedCores,
				consts.PodAnnotationMemoryEnhancementKey: `{"numa_binding":"true","numa_exclusive":"true"}`,
			}),
		},
		{
			name:            "dedicated_cores with numa_exclusive + numeric + MostAllocated",
			policy:          v1alpha1.TopologyPolicyNumericContainerLevel,
			strategy:        config.MostAllocated,
			alignedResource: []string{"cpu", "memory"},
			wantRes: map[string]int64{
				"node-2numa-8c16g":                 75, // one numaNode
				"node-2numa-4c8g":                  75, // two numaNodes
				"node-2numa-8c16g-with-allocation": 0,  // no numaNode idle
				"node-4numa-8c16g":                 75, // two numaNodes
			},
			pod: makePodByResourceList(&v1.ResourceList{
				v1.ResourceCPU:    resource.MustParse("3"),
				v1.ResourceMemory: resource.MustParse("6Gi"),
				"Gpu":             resource.MustParse("1"),
			}, map[string]string{
				consts.PodAnnotationQoSLevelKey:          consts.PodAnnotationQoSLevelDedicatedCores,
				consts.PodAnnotationMemoryEnhancementKey: `{"numa_binding":"true","numa_exclusive":"true"}`,
			}),
		},
	}

	c := cache.GetCache()
	for _, tc := range nativeTestCases {
		util.SetQoSConfig(generic.NewQoSConfiguration())
		cnrs, nodeNames, pods := makeTestScoreNodes(tc.policy)
		for _, cnr := range cnrs {
			c.AddOrUpdateCNR(cnr)
		}

		nodes := make([]*v1.Node, 0)
		for _, node := range nodeNames {
			n := &v1.Node{}
			n.SetName(node)
			nodes = append(nodes, n)
		}
		f, err := runtime.NewFramework(nil, nil,
			runtime.WithSnapshotSharedLister(newTestSharedLister(pods, nodes)))
		assert.NoError(t, err)
		tm, err := MakeTestTm(MakeTestArgs(tc.strategy, tc.alignedResource, "native"), f)
		assert.NoError(t, err)

		nodeToScore := make(map[string]int64)
		for _, node := range nodeNames {
			score, status := tm.(*TopologyMatch).Score(context.TODO(), nil, tc.pod, node)
			assert.True(t, reflect.DeepEqual(status, tc.wantStatus), tc.name)

			nodeToScore[node] = score
		}
		for wantN, wantS := range tc.wantRes {
			assert.Equal(t, wantS, nodeToScore["score-"+wantN])
		}

		// clean cache
		for _, cnr := range cnrs {
			c.RemoveCNR(cnr)
		}
	}

	for _, tc := range dedicatedTestCases {
		util.SetQoSConfig(generic.NewQoSConfiguration())
		cnrs, nodeNames, pods := makeTestScoreNodes(tc.policy)
		for _, cnr := range cnrs {
			c.AddOrUpdateCNR(cnr)
		}

		nodes := make([]*v1.Node, 0)
		for _, node := range nodeNames {
			n := &v1.Node{}
			n.SetName(node)
			nodes = append(nodes, n)
		}
		f, err := runtime.NewFramework(nil, nil,
			runtime.WithSnapshotSharedLister(newTestSharedLister(pods, nodes)))
		assert.NoError(t, err)
		tm, err := MakeTestTm(MakeTestArgs(tc.strategy, tc.alignedResource, "dynamic"), f)
		assert.NoError(t, err)
		nodeToScore := make(map[string]int64)
		for _, node := range nodeNames {
			score, status := tm.(*TopologyMatch).Score(context.TODO(), nil, tc.pod, node)
			assert.True(t, reflect.DeepEqual(status, tc.wantStatus), tc.name)

			nodeToScore[node] = score
		}
		for wantN, wantS := range tc.wantRes {
			assert.Equal(t, wantS, nodeToScore["score-"+wantN])
		}

		// clean cache
		for _, cnr := range cnrs {
			c.RemoveCNR(cnr)
		}
	}
}

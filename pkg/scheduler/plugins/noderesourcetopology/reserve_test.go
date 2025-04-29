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
	"k8s.io/kubernetes/pkg/scheduler/framework/runtime"

	"github.com/kubewharf/katalyst-api/pkg/apis/node/v1alpha1"
	"github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/config/generic"
	"github.com/kubewharf/katalyst-core/pkg/scheduler/cache"
	"github.com/kubewharf/katalyst-core/pkg/scheduler/util"
)

func makeTestReserveNode(name string) (*v1alpha1.CustomNodeResource, string) {
	return &v1alpha1.CustomNodeResource{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Status: v1alpha1.CustomNodeResourceStatus{
			TopologyPolicy: v1alpha1.TopologyPolicySingleNUMANodeContainerLevel,
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
	}, name
}

func TestReserve(t *testing.T) {
	t.Parallel()
	type testCase struct {
		name            string
		policy          v1alpha1.TopologyPolicy
		alignedResource []string
		reservePod      *v1.Pod
		schedulePod     *v1.Pod
	}

	testCases := []testCase{
		{
			name:            "dedicated + exclusive + single numa",
			policy:          v1alpha1.TopologyPolicySingleNUMANodeContainerLevel,
			alignedResource: []string{"cpu", "memory"},
			reservePod: makePodByResourceList(&v1.ResourceList{
				v1.ResourceCPU:    resource.MustParse("2"),
				v1.ResourceMemory: resource.MustParse("4Gi"),
			}, map[string]string{
				consts.PodAnnotationQoSLevelKey:          consts.PodAnnotationQoSLevelDedicatedCores,
				consts.PodAnnotationMemoryEnhancementKey: `{"numa_binding":"true","numa_exclusive":"true"}`,
			}),
			schedulePod: makePodByResourceList(&v1.ResourceList{
				v1.ResourceCPU:    resource.MustParse("2"),
				v1.ResourceMemory: resource.MustParse("4Gi"),
			}, map[string]string{
				consts.PodAnnotationQoSLevelKey:          consts.PodAnnotationQoSLevelDedicatedCores,
				consts.PodAnnotationMemoryEnhancementKey: `{"numa_binding":"true","numa_exclusive":"true"}`,
			}),
		},
		{
			name:   "reserve dedicated + schedule shared",
			policy: v1alpha1.TopologyPolicySingleNUMANodeContainerLevel,
			reservePod: makePodByResourceList(&v1.ResourceList{
				v1.ResourceCPU:    resource.MustParse("2"),
				v1.ResourceMemory: resource.MustParse("4Gi"),
			}, map[string]string{
				consts.PodAnnotationQoSLevelKey:          consts.PodAnnotationQoSLevelDedicatedCores,
				consts.PodAnnotationMemoryEnhancementKey: `{"numa_binding":"true","numa_exclusive":"true"}`,
			}),
			schedulePod: makePodByResourceList(&v1.ResourceList{
				v1.ResourceCPU:    resource.MustParse("1"),
				v1.ResourceMemory: resource.MustParse("2Gi"),
			}, map[string]string{
				consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
			}),
		},
		{
			name:   "too much shared requests",
			policy: v1alpha1.TopologyPolicySingleNUMANodeContainerLevel,
			reservePod: makePodByResourceList(&v1.ResourceList{
				v1.ResourceCPU:    resource.MustParse("6"),
				v1.ResourceMemory: resource.MustParse("8Gi"),
			}, map[string]string{
				consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
			}),
			schedulePod: makePodByResourceList(&v1.ResourceList{
				v1.ResourceCPU:    resource.MustParse("4"),
				v1.ResourceMemory: resource.MustParse("8Gi"),
			}, map[string]string{
				consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
			}),
		},
	}

	c := cache.GetCache()
	util.SetQoSConfig(generic.NewQoSConfiguration())
	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cnr, nodeName := makeTestReserveNode(tc.name)
			c.AddOrUpdateCNR(cnr)

			f, err := runtime.NewFramework(nil, nil,
				runtime.WithSnapshotSharedLister(newTestSharedLister(nil, nil)))
			assert.NoError(t, err)
			tm, err := MakeTestTm(MakeTestArgs(config.MostAllocated, tc.alignedResource, "dynamic"), f)
			assert.NoError(t, err)

			n := &v1.Node{}
			n.SetName(nodeName)
			nodeInfo := framework.NewNodeInfo()
			nodeInfo.SetNode(n)

			// pod can be allocated on node
			status := tm.(*TopologyMatch).Filter(context.TODO(), nil, tc.schedulePod, nodeInfo)
			assert.Nil(t, status)

			tm.(*TopologyMatch).Reserve(context.TODO(), nil, tc.reservePod, nodeName)

			// pod can not be allocated after reserve
			status = tm.(*TopologyMatch).Filter(context.TODO(), nil, tc.schedulePod, nodeInfo)
			assert.Equal(t, 2, int(status.Code()))

			tm.(*TopologyMatch).Unreserve(context.TODO(), nil, tc.reservePod, nodeName)

			// pod can be allocatd again
			status = tm.(*TopologyMatch).Filter(context.TODO(), nil, tc.schedulePod, nodeInfo)
			assert.Nil(t, status)
		})
	}
}

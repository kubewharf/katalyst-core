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

func makeTestReserveNode() (*v1alpha1.CustomNodeResource, string) {
	return &v1alpha1.CustomNodeResource{
		ObjectMeta: metav1.ObjectMeta{Name: "node-2numa-8c16g"},
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
	}, "node-2numa-8c16g"
}

func TestReserve(t *testing.T) {
	type testCase struct {
		name            string
		policy          v1alpha1.TopologyPolicy
		alignedResource []string
		pod             *v1.Pod
	}

	testCases := []testCase{
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
		},
	}

	c := cache.GetCache()
	util.SetQoSConfig(generic.NewQoSConfiguration())
	for _, tc := range testCases {
		cnr, nodeName := makeTestReserveNode()
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
		status := tm.(*TopologyMatch).Filter(context.TODO(), nil, tc.pod, nodeInfo)
		assert.Nil(t, status)

		tm.(*TopologyMatch).Reserve(context.TODO(), nil, tc.pod, nodeName)

		// pod can not be allocated after reserve
		status = tm.(*TopologyMatch).Filter(context.TODO(), nil, tc.pod, nodeInfo)
		assert.Equal(t, 2, int(status.Code()))

		tm.(*TopologyMatch).Unreserve(context.TODO(), nil, tc.pod, nodeName)

		// pod can be allocatd again
		status = tm.(*TopologyMatch).Filter(context.TODO(), nil, tc.pod, nodeInfo)
		assert.Nil(t, status)
	}
}

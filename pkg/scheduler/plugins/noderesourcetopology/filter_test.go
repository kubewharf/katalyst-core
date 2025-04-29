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

var _ framework.SharedLister = &testSharedLister{}

type testSharedLister struct {
	nodes       []*v1.Node
	nodeInfos   []*framework.NodeInfo
	nodeInfoMap map[string]*framework.NodeInfo
}

func (f *testSharedLister) NodeInfos() framework.NodeInfoLister {
	return f
}

func (f *testSharedLister) List() ([]*framework.NodeInfo, error) {
	return f.nodeInfos, nil
}

func (f *testSharedLister) HavePodsWithAffinityList() ([]*framework.NodeInfo, error) {
	return nil, nil
}

func (f *testSharedLister) HavePodsWithRequiredAntiAffinityList() ([]*framework.NodeInfo, error) {
	return nil, nil
}

func (f *testSharedLister) Get(nodeName string) (*framework.NodeInfo, error) {
	return f.nodeInfoMap[nodeName], nil
}

func newTestSharedLister(pods []*v1.Pod, nodes []*v1.Node) *testSharedLister {
	nodeInfoMap := make(map[string]*framework.NodeInfo)
	nodeInfos := make([]*framework.NodeInfo, 0)
	for _, pod := range pods {
		nodeName := pod.Spec.NodeName
		if _, ok := nodeInfoMap[nodeName]; !ok {
			nodeInfoMap[nodeName] = framework.NewNodeInfo()
		}
		nodeInfoMap[nodeName].AddPod(pod)
	}
	for _, node := range nodes {
		if _, ok := nodeInfoMap[node.Name]; !ok {
			nodeInfoMap[node.Name] = framework.NewNodeInfo()
		}
		nodeInfoMap[node.Name].SetNode(node)
	}

	for _, v := range nodeInfoMap {
		nodeInfos = append(nodeInfos, v)
	}

	return &testSharedLister{
		nodes:       nodes,
		nodeInfos:   nodeInfos,
		nodeInfoMap: nodeInfoMap,
	}
}

func makeTestFilterNodes(policy v1alpha1.TopologyPolicy, namePrefix string) ([]*v1alpha1.CustomNodeResource, []string, []*v1.Pod) {
	cnrs := []*v1alpha1.CustomNodeResource{
		{
			ObjectMeta: metav1.ObjectMeta{Name: namePrefix + "node-2numa-8c16g"},
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
			ObjectMeta: metav1.ObjectMeta{Name: namePrefix + "node-2numa-4c8g"},
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
			ObjectMeta: metav1.ObjectMeta{Name: namePrefix + "node-2numa-8c16g-with-allocation"},
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
			ObjectMeta: metav1.ObjectMeta{Name: namePrefix + "node-4numa-8c16g-cross-socket"},
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
			ObjectMeta: metav1.ObjectMeta{Name: namePrefix + "node-4numa-8c16g-full-socket"},
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
				NodeName: namePrefix + "node-2numa-8c16g-with-allocation",
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
				NodeName: namePrefix + "node-4numa-8c16g-cross-socket",
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: "testNamespace",
				Name:      "testPod3",
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
				},
			},
			Spec: v1.PodSpec{
				NodeName: namePrefix + "node-4numa-8c16g-cross-socket",
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: "testNamespace",
				Name:      "testPod4",
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
				},
			},
			Spec: v1.PodSpec{
				NodeName: namePrefix + "node-4numa-8c16g-full-socket",
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: "testNamespace",
				Name:      "testPod5",
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
				},
			},
			Spec: v1.PodSpec{
				NodeName: namePrefix + "node-4numa-8c16g-full-socket",
			},
		},
	}

	return cnrs, []string{
		namePrefix + "node-2numa-8c16g", namePrefix + "node-2numa-4c8g", namePrefix + "node-2numa-8c16g-with-allocation",
		namePrefix + "node-4numa-8c16g-cross-socket", namePrefix + "node-4numa-8c16g-full-socket",
	}, pods
}

func TestFilterNative(t *testing.T) {
	t.Parallel()
	type testCase struct {
		name            string
		prefix          string
		policy          v1alpha1.TopologyPolicy
		alignedResource []string
		pod             *v1.Pod
		wantRes         map[string]*framework.Status
	}

	nativeTestCase := []testCase{
		{
			// 4C8G pod with a 4C8G container, can not be allocated on 2C4G NUMA node
			name:            "native pod + single numa",
			prefix:          "FilterNative1",
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
			prefix:          "FilterNative2",
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
			prefix:          "FilterNative3",
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
			prefix:          "FilterNative4",
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
			prefix:          "FilterNative5",
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
			prefix:          "FilterNative6",
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
		cnrs, nodeNames, pods := makeTestFilterNodes(tc.policy, tc.prefix)
		for _, cnr := range cnrs {
			c.AddOrUpdateCNR(cnr)
		}

		ret := make(map[string]*framework.Status)
		nodeInfos := make([]*framework.NodeInfo, 0)
		nodes := make([]*v1.Node, 0)
		for _, node := range nodeNames {
			n := &v1.Node{}
			n.SetName(node)
			nodes = append(nodes, n)
			nodeInfo := framework.NewNodeInfo()
			nodeInfo.SetNode(n)
			for _, pod := range pods {
				nodeInfo.AddPod(pod)
			}
			nodeInfos = append(nodeInfos, nodeInfo)
		}
		f, err := runtime.NewFramework(nil, nil,
			runtime.WithSnapshotSharedLister(newTestSharedLister(pods, nodes)))
		assert.NoError(t, err)

		tm, err := MakeTestTm(MakeTestArgs(config.MostAllocated, tc.alignedResource, "native"), f)
		assert.NoError(t, err)

		for _, nodeInfo := range nodeInfos {
			status := tm.(*TopologyMatch).Filter(context.TODO(), nil, tc.pod, nodeInfo)
			ret[nodeInfo.Node().Name] = status
		}

		// check result
		for wantN, wantS := range tc.wantRes {
			if wantS == nil {
				assert.Nil(t, ret[wantN])
			} else {
				assert.Equal(t, wantS.Code(), ret[tc.prefix+wantN].Code())
			}
		}
	}
}

func TestFilterDedicatedNumaBinding(t *testing.T) {
	t.Parallel()
	type testCase struct {
		name            string
		prefix          string
		policy          v1alpha1.TopologyPolicy
		alignedResource []string
		pod             *v1.Pod
		wantRes         map[string]*framework.Status
	}

	numaBindingCase := []testCase{
		{
			name:            "dedicated + numaBinding + single numa",
			prefix:          "FilterDedicatedNumaBinding1",
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
			prefix:          "FilterDedicatedNumaBinding2",
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
			prefix:          "FilterDedicatedNumaBinding3",
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
			prefix:          "FilterDedicatedNumaBinding4",
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
			prefix:          "FilterDedicatedNumaBinding5",
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
			prefix:          "FilterDedicatedNumaBinding6",
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
		cnrs, nodeNames, pods := makeTestFilterNodes(tc.policy, tc.prefix)
		for _, cnr := range cnrs {
			c.AddOrUpdateCNR(cnr)
		}

		ret := make(map[string]*framework.Status)
		nodeInfos := make([]*framework.NodeInfo, 0)
		nodes := make([]*v1.Node, 0)
		for _, node := range nodeNames {
			n := &v1.Node{}
			n.SetName(node)
			nodes = append(nodes, n)
			nodeInfo := framework.NewNodeInfo()
			nodeInfo.SetNode(n)
			for _, pod := range pods {
				nodeInfo.AddPod(pod)
			}
			nodeInfos = append(nodeInfos, nodeInfo)
		}
		f, err := runtime.NewFramework(nil, nil,
			runtime.WithSnapshotSharedLister(newTestSharedLister(pods, nodes)))
		assert.NoError(t, err)

		tm, err := MakeTestTm(MakeTestArgs(config.MostAllocated, tc.alignedResource, "dynamic"), f)
		assert.NoError(t, err)

		for _, nodeInfo := range nodeInfos {
			status := tm.(*TopologyMatch).Filter(context.TODO(), nil, tc.pod, nodeInfo)
			ret[nodeInfo.Node().Name] = status
		}

		// check result
		for wantN, wantS := range tc.wantRes {
			if wantS == nil {
				assert.Nil(t, ret[wantN])
			} else {
				assert.Equal(t, wantS.Code(), ret[tc.prefix+wantN].Code())
			}
		}
	}
}

func TestFilterDedicatedExclusive(t *testing.T) {
	t.Parallel()
	type testCase struct {
		name            string
		prefix          string
		policy          v1alpha1.TopologyPolicy
		alignedResource []string
		pod             *v1.Pod
		wantRes         map[string]*framework.Status
	}

	numaExclusiveCase := []testCase{
		{
			name:            "dedicated + exclusive + single numa",
			prefix:          "FilterDedicatedExclusive1",
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
			prefix:          "FilterDedicatedExclusive1",
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
			prefix:          "FilterDedicatedExclusive1",
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
			prefix:          "FilterDedicatedExclusive1",
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
		cnrs, nodeNames, pods := makeTestFilterNodes(tc.policy, tc.prefix)
		for _, cnr := range cnrs {
			c.AddOrUpdateCNR(cnr)
		}

		ret := make(map[string]*framework.Status)
		nodeInfos := make([]*framework.NodeInfo, 0)
		nodes := make([]*v1.Node, 0)
		for _, node := range nodeNames {
			n := &v1.Node{}
			n.SetName(node)
			nodes = append(nodes, n)
			nodeInfo := framework.NewNodeInfo()
			nodeInfo.SetNode(n)
			for _, pod := range pods {
				nodeInfo.AddPod(pod)
			}
			nodeInfos = append(nodeInfos, nodeInfo)
		}
		f, err := runtime.NewFramework(nil, nil,
			runtime.WithSnapshotSharedLister(newTestSharedLister(pods, nodes)))
		assert.NoError(t, err)

		tm, err := MakeTestTm(MakeTestArgs(config.MostAllocated, tc.alignedResource, "dynamic"), f)
		assert.NoError(t, err)

		for _, nodeInfo := range nodeInfos {
			status := tm.(*TopologyMatch).Filter(context.TODO(), nil, tc.pod, nodeInfo)
			ret[nodeInfo.Node().Name] = status
		}
		// check result
		for wantN, wantS := range tc.wantRes {
			if wantS == nil {
				assert.Nil(t, ret[wantN])
			} else {
				assert.Equal(t, wantS.Code(), ret[tc.prefix+wantN].Code())
			}
		}
	}
}

func TestFilterSharedCores(t *testing.T) {
	t.Parallel()
	type testCase struct {
		name       string
		cnr        *v1alpha1.CustomNodeResource
		node       *v1.Node
		existsPods []*v1.Pod
		pod        *v1.Pod
		wantRes    *framework.Status
	}

	testCases := []testCase{
		{
			name: "empty node",
			cnr: &v1alpha1.CustomNodeResource{
				ObjectMeta: metav1.ObjectMeta{
					Name: "TestFilterSharedCores_1",
				},
				Status: v1alpha1.CustomNodeResourceStatus{
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
										},
										Allocatable: &v1.ResourceList{
											v1.ResourceCPU:    resource.MustParse("4"),
											v1.ResourceMemory: resource.MustParse("8Gi"),
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
										},
										Allocatable: &v1.ResourceList{
											v1.ResourceCPU:    resource.MustParse("4"),
											v1.ResourceMemory: resource.MustParse("8Gi"),
										},
									},
								},
							},
						},
					},
				},
			},
			existsPods: []*v1.Pod{},
			node: &v1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "TestFilterSharedCores_1",
				},
				Status: v1.NodeStatus{
					Capacity: map[v1.ResourceName]resource.Quantity{
						v1.ResourceCPU:    resource.MustParse("8"),
						v1.ResourceMemory: resource.MustParse("16Gi"),
					},
					Allocatable: map[v1.ResourceName]resource.Quantity{
						v1.ResourceCPU:    resource.MustParse("8"),
						v1.ResourceMemory: resource.MustParse("16Gi"),
					},
				},
			},
			pod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name: "testPod",
					UID:  "testUID",
				},
				Spec: v1.PodSpec{
					Containers: []v1.Container{
						{
							Name: "testContainer",
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
			wantRes: nil,
		},
		{
			name: "numa not satisfy",
			cnr: &v1alpha1.CustomNodeResource{
				ObjectMeta: metav1.ObjectMeta{
					Name: "TestFilterSharedCores_2",
				},
				Status: v1alpha1.CustomNodeResourceStatus{
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
										},
										Allocatable: &v1.ResourceList{
											v1.ResourceCPU:    resource.MustParse("4"),
											v1.ResourceMemory: resource.MustParse("8Gi"),
										},
									},
									Allocations: []*v1alpha1.Allocation{
										{
											Consumer: "/dedicated1/dedicated1",
											Requests: &v1.ResourceList{
												v1.ResourceCPU:    resource.MustParse("2"),
												v1.ResourceMemory: resource.MustParse("4Gi"),
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
										},
										Allocatable: &v1.ResourceList{
											v1.ResourceCPU:    resource.MustParse("4"),
											v1.ResourceMemory: resource.MustParse("8Gi"),
										},
									},
									Allocations: []*v1alpha1.Allocation{
										{
											Consumer: "/dedicated2/dedicated2",
											Requests: &v1.ResourceList{
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
			},
			existsPods: []*v1.Pod{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "dedicated1",
						UID:  "dedicated1",
						Annotations: map[string]string{
							consts.PodAnnotationQoSLevelKey:          consts.PodAnnotationQoSLevelDedicatedCores,
							consts.PodAnnotationMemoryEnhancementKey: `{"numa_binding":"true"}`,
						},
					},
					Spec: v1.PodSpec{
						Containers: []v1.Container{
							{
								Name: "testContainer",
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
					ObjectMeta: metav1.ObjectMeta{
						Name: "dedicated2",
						UID:  "dedicated2",
						Annotations: map[string]string{
							consts.PodAnnotationQoSLevelKey:          consts.PodAnnotationQoSLevelDedicatedCores,
							consts.PodAnnotationMemoryEnhancementKey: `{"numa_binding":"true"}`,
						},
					},
					Spec: v1.PodSpec{
						Containers: []v1.Container{
							{
								Name: "testContainer",
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
			},
			node: &v1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "TestFilterSharedCores_2",
				},
				Status: v1.NodeStatus{
					Capacity: map[v1.ResourceName]resource.Quantity{
						v1.ResourceCPU:    resource.MustParse("8"),
						v1.ResourceMemory: resource.MustParse("16Gi"),
					},
					Allocatable: map[v1.ResourceName]resource.Quantity{
						v1.ResourceCPU:    resource.MustParse("8"),
						v1.ResourceMemory: resource.MustParse("16Gi"),
					},
				},
			},
			pod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name: "testPod",
					UID:  "testUID",
				},
				Spec: v1.PodSpec{
					Containers: []v1.Container{
						{
							Name: "testContainer",
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
			wantRes: framework.NewStatus(framework.Unschedulable, ""),
		},
		{
			name: "single numa resource not satisfy",
			cnr: &v1alpha1.CustomNodeResource{
				ObjectMeta: metav1.ObjectMeta{
					Name: "TestFilterSharedCores_3",
				},
				Status: v1alpha1.CustomNodeResourceStatus{
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
										},
										Allocatable: &v1.ResourceList{
											v1.ResourceCPU:    resource.MustParse("4"),
											v1.ResourceMemory: resource.MustParse("8Gi"),
										},
									},
									Allocations: []*v1alpha1.Allocation{
										{
											Consumer: "/dedicated1/dedicated1",
											Requests: &v1.ResourceList{
												v1.ResourceCPU:    resource.MustParse("2"),
												v1.ResourceMemory: resource.MustParse("4Gi"),
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
										},
										Allocatable: &v1.ResourceList{
											v1.ResourceCPU:    resource.MustParse("4"),
											v1.ResourceMemory: resource.MustParse("8Gi"),
										},
									},
								},
							},
						},
					},
				},
			},
			existsPods: []*v1.Pod{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "dedicated1",
						UID:  "dedicated1",
						Annotations: map[string]string{
							consts.PodAnnotationQoSLevelKey:          consts.PodAnnotationQoSLevelDedicatedCores,
							consts.PodAnnotationMemoryEnhancementKey: `{"numa_binding":"true"}`,
						},
					},
					Spec: v1.PodSpec{
						Containers: []v1.Container{
							{
								Name: "testContainer",
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
					ObjectMeta: metav1.ObjectMeta{
						Name: "shared1",
						UID:  "shared1",
						Annotations: map[string]string{
							consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
						},
					},
					Spec: v1.PodSpec{
						NodeName: "TestFilterSharedCores_3",
						Containers: []v1.Container{
							{
								Name: "testContainer",
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
			},
			node: &v1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "TestFilterSharedCores_3",
				},
				Status: v1.NodeStatus{
					Capacity: map[v1.ResourceName]resource.Quantity{
						v1.ResourceCPU:    resource.MustParse("8"),
						v1.ResourceMemory: resource.MustParse("16Gi"),
					},
					Allocatable: map[v1.ResourceName]resource.Quantity{
						v1.ResourceCPU:    resource.MustParse("8"),
						v1.ResourceMemory: resource.MustParse("16Gi"),
					},
				},
			},
			pod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name: "testPod",
					UID:  "testUID",
				},
				Spec: v1.PodSpec{
					Containers: []v1.Container{
						{
							Name: "testContainer",
							Resources: v1.ResourceRequirements{
								Requests: map[v1.ResourceName]resource.Quantity{
									v1.ResourceCPU:    resource.MustParse("2"),
									v1.ResourceMemory: resource.MustParse("6Gi"),
								},
								Limits: map[v1.ResourceName]resource.Quantity{
									v1.ResourceCPU:    resource.MustParse("2"),
									v1.ResourceMemory: resource.MustParse("6Gi"),
								},
							},
						},
					},
				},
			},
			wantRes: framework.NewStatus(framework.Unschedulable, ""),
		},
		{
			name: "numa resource not satisfy for existed shared pods",
			cnr: &v1alpha1.CustomNodeResource{
				ObjectMeta: metav1.ObjectMeta{
					Name: "TestFilterSharedCores_4",
				},
				Status: v1alpha1.CustomNodeResourceStatus{
					TopologyPolicy: "NumericContainerLevel",
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
										},
										Allocatable: &v1.ResourceList{
											v1.ResourceCPU:    resource.MustParse("4"),
											v1.ResourceMemory: resource.MustParse("8Gi"),
										},
									},
									Allocations: []*v1alpha1.Allocation{
										{
											Consumer: "/dedicated1/dedicated1",
											Requests: &v1.ResourceList{
												v1.ResourceCPU:    resource.MustParse("2"),
												v1.ResourceMemory: resource.MustParse("4Gi"),
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
										},
										Allocatable: &v1.ResourceList{
											v1.ResourceCPU:    resource.MustParse("4"),
											v1.ResourceMemory: resource.MustParse("8Gi"),
										},
									},
								},
							},
						},
					},
				},
			},
			existsPods: []*v1.Pod{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "shared2",
						UID:  "shared2",
						Annotations: map[string]string{
							consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
						},
					},
					Spec: v1.PodSpec{
						NodeName: "TestFilterSharedCores_4",
						Containers: []v1.Container{
							{
								Name: "testContainer",
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
					ObjectMeta: metav1.ObjectMeta{
						Name: "shared1",
						UID:  "shared1",
						Annotations: map[string]string{
							consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
						},
					},
					Spec: v1.PodSpec{
						NodeName: "TestFilterSharedCores_4",
						Containers: []v1.Container{
							{
								Name: "testContainer",
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
					ObjectMeta: metav1.ObjectMeta{
						Name: "shared3",
						UID:  "shared3",
						Annotations: map[string]string{
							consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
						},
					},
					Spec: v1.PodSpec{
						NodeName: "TestFilterSharedCores_4",
						Containers: []v1.Container{
							{
								Name: "testContainer",
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
			},
			node: &v1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "TestFilterSharedCores_4",
				},
				Status: v1.NodeStatus{
					Capacity: map[v1.ResourceName]resource.Quantity{
						v1.ResourceCPU:    resource.MustParse("8"),
						v1.ResourceMemory: resource.MustParse("16Gi"),
					},
					Allocatable: map[v1.ResourceName]resource.Quantity{
						v1.ResourceCPU:    resource.MustParse("8"),
						v1.ResourceMemory: resource.MustParse("16Gi"),
					},
				},
			},
			pod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name: "testPod",
					UID:  "testUID",
					Annotations: map[string]string{
						consts.PodAnnotationQoSLevelKey:          consts.PodAnnotationQoSLevelDedicatedCores,
						consts.PodAnnotationMemoryEnhancementKey: `{"numa_binding":"true"}`,
					},
				},
				Spec: v1.PodSpec{
					Containers: []v1.Container{
						{
							Name: "testContainer",
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
			wantRes: framework.NewStatus(framework.Unschedulable, ""),
		},
		{
			name: "numa resource not satisfy for existed shared pods singlenumanode",
			cnr: &v1alpha1.CustomNodeResource{
				ObjectMeta: metav1.ObjectMeta{
					Name: "TestFilterSharedCores_5",
				},
				Status: v1alpha1.CustomNodeResourceStatus{
					TopologyPolicy: "SingleNUMANodeContainerLevel",
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
										},
										Allocatable: &v1.ResourceList{
											v1.ResourceCPU:    resource.MustParse("4"),
											v1.ResourceMemory: resource.MustParse("8Gi"),
										},
									},
									Allocations: []*v1alpha1.Allocation{
										{
											Consumer: "/dedicated1/dedicated1",
											Requests: &v1.ResourceList{
												v1.ResourceCPU:    resource.MustParse("2"),
												v1.ResourceMemory: resource.MustParse("4Gi"),
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
										},
										Allocatable: &v1.ResourceList{
											v1.ResourceCPU:    resource.MustParse("4"),
											v1.ResourceMemory: resource.MustParse("8Gi"),
										},
									},
								},
							},
						},
					},
				},
			},
			existsPods: []*v1.Pod{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "shared2",
						UID:  "shared2",
						Annotations: map[string]string{
							consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
						},
					},
					Spec: v1.PodSpec{
						NodeName: "TestFilterSharedCores_5",
						Containers: []v1.Container{
							{
								Name: "testContainer",
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
					ObjectMeta: metav1.ObjectMeta{
						Name: "shared1",
						UID:  "shared1",
						Annotations: map[string]string{
							consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
						},
					},
					Spec: v1.PodSpec{
						NodeName: "TestFilterSharedCores_5",
						Containers: []v1.Container{
							{
								Name: "testContainer",
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
					ObjectMeta: metav1.ObjectMeta{
						Name: "shared3",
						UID:  "shared3",
						Annotations: map[string]string{
							consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
						},
					},
					Spec: v1.PodSpec{
						NodeName: "TestFilterSharedCores_5",
						Containers: []v1.Container{
							{
								Name: "testContainer",
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
			},
			node: &v1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "TestFilterSharedCores_5",
				},
				Status: v1.NodeStatus{
					Capacity: map[v1.ResourceName]resource.Quantity{
						v1.ResourceCPU:    resource.MustParse("8"),
						v1.ResourceMemory: resource.MustParse("16Gi"),
					},
					Allocatable: map[v1.ResourceName]resource.Quantity{
						v1.ResourceCPU:    resource.MustParse("8"),
						v1.ResourceMemory: resource.MustParse("16Gi"),
					},
				},
			},
			pod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name: "testPod",
					UID:  "testUID",
					Annotations: map[string]string{
						consts.PodAnnotationQoSLevelKey:          consts.PodAnnotationQoSLevelDedicatedCores,
						consts.PodAnnotationMemoryEnhancementKey: `{"numa_binding":"true"}`,
					},
				},
				Spec: v1.PodSpec{
					Containers: []v1.Container{
						{
							Name: "testContainer",
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
			wantRes: framework.NewStatus(framework.Unschedulable, ""),
		},
		{
			name: "numa resource satisfy for existed shared pods",
			cnr: &v1alpha1.CustomNodeResource{
				ObjectMeta: metav1.ObjectMeta{
					Name: "TestFilterSharedCores_6",
				},
				Status: v1alpha1.CustomNodeResourceStatus{
					TopologyPolicy: "SingleNumaNodeContainerLevel",
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
										},
										Allocatable: &v1.ResourceList{
											v1.ResourceCPU:    resource.MustParse("4"),
											v1.ResourceMemory: resource.MustParse("8Gi"),
										},
									},
									Allocations: []*v1alpha1.Allocation{
										{
											Consumer: "/dedicated1/dedicated1",
											Requests: &v1.ResourceList{
												v1.ResourceCPU:    resource.MustParse("2"),
												v1.ResourceMemory: resource.MustParse("4Gi"),
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
										},
										Allocatable: &v1.ResourceList{
											v1.ResourceCPU:    resource.MustParse("4"),
											v1.ResourceMemory: resource.MustParse("8Gi"),
										},
									},
								},
							},
						},
					},
				},
			},
			existsPods: []*v1.Pod{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "shared2",
						UID:  "shared2",
						Annotations: map[string]string{
							consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
						},
					},
					Spec: v1.PodSpec{
						NodeName: "TestFilterSharedCores_6",
						Containers: []v1.Container{
							{
								Name: "testContainer",
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
					ObjectMeta: metav1.ObjectMeta{
						Name: "shared1",
						UID:  "shared1",
						Annotations: map[string]string{
							consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
						},
					},
					Spec: v1.PodSpec{
						NodeName: "TestFilterSharedCores_6",
						Containers: []v1.Container{
							{
								Name: "testContainer",
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
					ObjectMeta: metav1.ObjectMeta{
						Name: "dedicated1",
						UID:  "dedicated1",
						Annotations: map[string]string{
							consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
						},
					},
					Spec: v1.PodSpec{
						NodeName: "TestFilterSharedCores_6",
						Containers: []v1.Container{
							{
								Name: "testContainer",
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
			},
			node: &v1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "TestFilterSharedCores_6",
				},
				Status: v1.NodeStatus{
					Capacity: map[v1.ResourceName]resource.Quantity{
						v1.ResourceCPU:    resource.MustParse("8"),
						v1.ResourceMemory: resource.MustParse("16Gi"),
					},
					Allocatable: map[v1.ResourceName]resource.Quantity{
						v1.ResourceCPU:    resource.MustParse("8"),
						v1.ResourceMemory: resource.MustParse("16Gi"),
					},
				},
			},
			pod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name: "testPod",
					UID:  "testUID",
					Annotations: map[string]string{
						consts.PodAnnotationQoSLevelKey:          consts.PodAnnotationQoSLevelDedicatedCores,
						consts.PodAnnotationMemoryEnhancementKey: `{"numa_binding":"true"}`,
					},
				},
				Spec: v1.PodSpec{
					Containers: []v1.Container{
						{
							Name: "testContainer",
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
			wantRes: nil,
		},
	}

	c := cache.GetCache()
	util.SetQoSConfig(generic.NewQoSConfiguration())
	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c.AddOrUpdateCNR(tc.cnr)
			for _, pod := range tc.existsPods {
				c.AddPod(pod)
			}

			nodeInfo := framework.NewNodeInfo(tc.existsPods...)
			nodeInfo.SetNode(tc.node)

			f, err := runtime.NewFramework(nil, nil,
				runtime.WithSnapshotSharedLister(newTestSharedLister(tc.existsPods, []*v1.Node{tc.node})))
			assert.NoError(t, err)

			tm, err := MakeTestTm(MakeTestArgs(config.MostAllocated, []string{}, "dynamic"), f)
			assert.NoError(t, err)

			status := tm.(*TopologyMatch).Filter(context.TODO(), nil, tc.pod, nodeInfo)
			if status == nil {
				assert.Equal(t, tc.wantRes, status)
			} else {
				assert.Equal(t, tc.wantRes.Code(), status.Code())
			}
		})
	}
}

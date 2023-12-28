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

package numainterpodaffinity

import (
	"context"
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"
	"k8s.io/kubernetes/pkg/scheduler/framework"
	"k8s.io/kubernetes/pkg/scheduler/framework/runtime"

	"github.com/kubewharf/katalyst-api/pkg/apis/node/v1alpha1"
	"github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/config/generic"
	"github.com/kubewharf/katalyst-core/pkg/scheduler/cache"
	"github.com/kubewharf/katalyst-core/pkg/scheduler/util"
)

const (
	antiAffinityKey   = "antiAffinityKey"
	antiAffinityValue = "antiAffinityValue"
	affinityKey       = "affinityKey"
	affinityValue     = "affinityValue"

	defaultNamespace = "default"
)

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

func makeTestFilterNodes() ([]*v1alpha1.CustomNodeResource, []*v1.Node, []*v1.Pod) {
	cnrs := []*v1alpha1.CustomNodeResource{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "node-2numa-0pod"},
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
										"Gpu":             resource.MustParse("4"),
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
			ObjectMeta: metav1.ObjectMeta{Name: "node-2numa-1pod"},
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
										Consumer: "default/testPod1/uid1",
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
							},
						},
					},
				},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "node-4numa-2pod"},
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
										Consumer: "default/testPod2/uid2",
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
										Consumer: "default/testPod3/uid3",
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
	}

	pods := []*v1.Pod{
		{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: defaultNamespace,
				Name:      "testPod1",
				UID:       "uid1",
				Labels: map[string]string{
					antiAffinityKey: antiAffinityValue,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:                       consts.PodAnnotationQoSLevelDedicatedCores,
					consts.PodAnnotationMemoryEnhancementKey:              `{"numa_binding": "true", "numa_exclusive": "false"}`,
					consts.PodAnnotationMicroTopologyInterPodAntiAffinity: `{"required": [{"matchLabels": {"antiAffinityKey": "antiAffinityValue"}, "zone":"socket"}]}`,
				},
			},
			Spec: v1.PodSpec{
				NodeName: "node-2numa-1pod",
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: defaultNamespace,
				Name:      "testPod2",
				UID:       "uid2",
				Labels: map[string]string{
					affinityKey: affinityValue,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:                       consts.PodAnnotationQoSLevelDedicatedCores,
					consts.PodAnnotationMemoryEnhancementKey:              `{"numa_binding": "true", "numa_exclusive": "false"}`,
					consts.PodAnnotationMicroTopologyInterPodAntiAffinity: `{"required": [{"matchLabels": {"antiAffinityKey": "antiAffinityValue"}, "zone":"numa"}]}`,
				},
			},
			Spec: v1.PodSpec{
				NodeName: "node-4numa-2pod",
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: defaultNamespace,
				Name:      "testPod3",
				UID:       "uid3",
				Labels: map[string]string{
					antiAffinityKey: antiAffinityValue,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:          consts.PodAnnotationQoSLevelDedicatedCores,
					consts.PodAnnotationMemoryEnhancementKey: `{"numa_binding": "true", "numa_exclusive": "false"}`,
				},
			},
			Spec: v1.PodSpec{
				NodeName: "node-4numa-2pod",
			},
		},
	}

	nodes := []*v1.Node{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: "node-2numa-0pod",
				Labels: map[string]string{
					"region": "r1", "zone": "z1", "hostname": "node-2numa-0pod",
				},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: "node-2numa-1pod",
				Labels: map[string]string{
					"region": "r1", "zone": "z1", "hostname": "node-2numa-1pod",
				},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: "node-4numa-2pod",
				Labels: map[string]string{
					"region": "r1", "zone": "z1", "hostname": "node-4numa-2pod",
				},
			},
		},
	}

	return cnrs, nodes, pods
}

func TestFilter(t *testing.T) {
	testCases := []struct {
		name         string
		pod          *v1.Pod
		expectStatus []*framework.Status
	}{
		{
			name: "pod without labels and seletors",
			pod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "testPod",
					Namespace: defaultNamespace,
					Annotations: map[string]string{
						consts.PodAnnotationQoSLevelKey:          consts.PodAnnotationQoSLevelDedicatedCores,
						consts.PodAnnotationMemoryEnhancementKey: `{"numa_binding": "true", "numa_exclusive": "false"}`,
					},
				},
				Spec: v1.PodSpec{
					NodeName: "",
				},
			},
			expectStatus: []*framework.Status{nil, nil, nil},
		},
		{
			name: "pod without labels and with affinity seletors, exclusive=true",
			pod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "testPod",
					Namespace: defaultNamespace,
					Annotations: map[string]string{
						consts.PodAnnotationQoSLevelKey:                   consts.PodAnnotationQoSLevelDedicatedCores,
						consts.PodAnnotationMemoryEnhancementKey:          `{"numa_binding": "true", "numa_exclusive": "true"}`,
						consts.PodAnnotationMicroTopologyInterPodAffinity: `{"required": [{"matchLabels": {"affinityKey": "affinityValue"}, "zone":"numa"}]}`,
					},
				},
				Spec: v1.PodSpec{
					NodeName: "",
				},
			},
			expectStatus: []*framework.Status{
				framework.NewStatus(framework.Error, ErrGetPreFilterState),
				framework.NewStatus(framework.Error, ErrGetPreFilterState),
				framework.NewStatus(framework.Error, ErrGetPreFilterState),
			},
		},
		{
			name: "pod without labels and with anti-affinity seletors, exclusive=true",
			pod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "testPod",
					Namespace: defaultNamespace,
					Annotations: map[string]string{
						consts.PodAnnotationQoSLevelKey:                       consts.PodAnnotationQoSLevelDedicatedCores,
						consts.PodAnnotationMemoryEnhancementKey:              `{"numa_binding": "true", "numa_exclusive": "true"}`,
						consts.PodAnnotationMicroTopologyInterPodAntiAffinity: `{"required": [{"matchLabels": {"antiAffinityKey": "antiAffinityValue"}, "zone":"numa"}]}`,
					},
				},
				Spec: v1.PodSpec{
					NodeName: "",
				},
			},
			expectStatus: []*framework.Status{nil, nil, nil},
		},
		{
			name: "pod with anti-affinity labels and without seletors",
			pod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "testPod",
					Namespace: defaultNamespace,
					Labels: map[string]string{
						antiAffinityKey: antiAffinityValue,
					},
					Annotations: map[string]string{
						consts.PodAnnotationQoSLevelKey:          consts.PodAnnotationQoSLevelDedicatedCores,
						consts.PodAnnotationMemoryEnhancementKey: `{"numa_binding": "true", "numa_exclusive": "false"}`,
					},
				},
				Spec: v1.PodSpec{
					NodeName: "",
				},
			},
			expectStatus: []*framework.Status{
				nil,
				framework.NewStatus(framework.Unschedulable, ErrNUMAInterPodAffinityNotMatch),
				nil,
			},
		},
		{
			name: "pod without labels and with anti-affinity seletors",
			pod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "testPod",
					Namespace: defaultNamespace,
					Annotations: map[string]string{
						consts.PodAnnotationQoSLevelKey:                       consts.PodAnnotationQoSLevelDedicatedCores,
						consts.PodAnnotationMemoryEnhancementKey:              `{"numa_binding": "true", "numa_exclusive": "false"}`,
						consts.PodAnnotationMicroTopologyInterPodAntiAffinity: `{"required": [{"matchLabels": {"antiAffinityKey": "antiAffinityValue"}, "zone":"numa"}]}`,
					},
				},
				Spec: v1.PodSpec{
					NodeName: "",
				},
			},
			expectStatus: []*framework.Status{nil, nil, nil},
		},
		{
			name: "pod without labels and with anti-affinity seletors (zone=socket)",
			pod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "testPod",
					Namespace: defaultNamespace,
					Annotations: map[string]string{
						consts.PodAnnotationQoSLevelKey:                       consts.PodAnnotationQoSLevelDedicatedCores,
						consts.PodAnnotationMemoryEnhancementKey:              `{"numa_binding": "true", "numa_exclusive": "false"}`,
						consts.PodAnnotationMicroTopologyInterPodAntiAffinity: `{"required": [{"matchLabels": {"antiAffinityKey": "antiAffinityValue"}, "zone":"socket"}]}`,
					},
				},
				Spec: v1.PodSpec{
					NodeName: "",
				},
			},
			expectStatus: []*framework.Status{
				nil,
				framework.NewStatus(framework.Unschedulable, ErrNUMAInterPodAffinityNotMatch),
				nil,
			},
		},
		{
			name: "pod with anti-affinity labels and with anti-affinity seletors",
			pod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "testPod",
					Namespace: defaultNamespace,
					Labels: map[string]string{
						antiAffinityKey: antiAffinityValue,
					},
					Annotations: map[string]string{
						consts.PodAnnotationQoSLevelKey:                       consts.PodAnnotationQoSLevelDedicatedCores,
						consts.PodAnnotationMemoryEnhancementKey:              `{"numa_binding": "true", "numa_exclusive": "false"}`,
						consts.PodAnnotationMicroTopologyInterPodAntiAffinity: `{"required": [{"matchLabels": {"antiAffinityKey": "antiAffinityValue"}, "zone":"numa"}]}`,
					},
				},
				Spec: v1.PodSpec{
					NodeName: "",
				},
			},
			expectStatus: []*framework.Status{
				nil,
				framework.NewStatus(framework.Unschedulable, ErrNUMAInterPodAffinityNotMatch),
				nil,
			},
		},
		{
			name: "pod without labels and with affinity seletors",
			pod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "testPod",
					Namespace: defaultNamespace,
					Annotations: map[string]string{
						consts.PodAnnotationQoSLevelKey:                   consts.PodAnnotationQoSLevelDedicatedCores,
						consts.PodAnnotationMemoryEnhancementKey:          `{"numa_binding": "true", "numa_exclusive": "false"}`,
						consts.PodAnnotationMicroTopologyInterPodAffinity: `{"required": [{"matchLabels": {"affinityKey": "affinityValue"}, "zone":"numa"}]}`,
					},
				},
				Spec: v1.PodSpec{
					NodeName: "",
				},
			},
			expectStatus: []*framework.Status{
				framework.NewStatus(framework.Unschedulable, ErrNUMAInterPodAffinityNotMatch),
				framework.NewStatus(framework.Unschedulable, ErrNUMAInterPodAffinityNotMatch),
				nil,
			},
		},
		{
			name: "pod with anti-affinity labels and with affinity seletors",
			pod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "testPod",
					Namespace: defaultNamespace,
					Labels: map[string]string{
						antiAffinityKey: antiAffinityValue,
					},
					Annotations: map[string]string{
						consts.PodAnnotationQoSLevelKey:                   consts.PodAnnotationQoSLevelDedicatedCores,
						consts.PodAnnotationMemoryEnhancementKey:          `{"numa_binding": "true", "numa_exclusive": "false"}`,
						consts.PodAnnotationMicroTopologyInterPodAffinity: `{"required": [{"matchLabels": {"affinityKey": "affinityValue"}, "zone":"numa"}]}`,
					},
				},
				Spec: v1.PodSpec{
					NodeName: "",
				},
			},
			expectStatus: []*framework.Status{
				framework.NewStatus(framework.Unschedulable, ErrNUMAInterPodAffinityNotMatch),
				framework.NewStatus(framework.Unschedulable, ErrNUMAInterPodAffinityNotMatch),
				framework.NewStatus(framework.Unschedulable, ErrNUMAInterPodAffinityNotMatch),
			},
		},
		{
			name: "pod with anti-affinity labels and with affinity seletors (zone=socket)",
			pod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "testPod",
					Namespace: defaultNamespace,
					Labels: map[string]string{
						antiAffinityKey: antiAffinityValue,
					},
					Annotations: map[string]string{
						consts.PodAnnotationQoSLevelKey:                   consts.PodAnnotationQoSLevelDedicatedCores,
						consts.PodAnnotationMemoryEnhancementKey:          `{"numa_binding": "true", "numa_exclusive": "false"}`,
						consts.PodAnnotationMicroTopologyInterPodAffinity: `{"required": [{"matchLabels": {"affinityKey": "affinityValue"}, "zone":"socket"}]}`,
					},
				},
				Spec: v1.PodSpec{
					NodeName: "",
				},
			},
			expectStatus: []*framework.Status{
				framework.NewStatus(framework.Unschedulable, ErrNUMAInterPodAffinityNotMatch),
				framework.NewStatus(framework.Unschedulable, ErrNUMAInterPodAffinityNotMatch),
				nil,
			},
		},
	}

	c := cache.GetCache()
	util.SetQoSConfig(generic.NewQoSConfiguration())
	for _, test := range testCases {
		// make pods and nodes for testing
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		cnrs, nodes, pods := makeTestFilterNodes()
		for _, cnr := range cnrs {
			c.AddOrUpdateCNR(cnr)
		}

		// make NUMAInterPodAffinity plugin
		snapshot := newTestSharedLister(pods, nodes)
		f, err := runtime.NewFramework(nil, nil,
			runtime.WithSnapshotSharedLister(snapshot))
		assert.NoError(t, err)
		p, err := New(nil, f)
		assert.NoError(t, err)

		state := framework.NewCycleState()
		_, preFilterStatus := p.(framework.PreFilterPlugin).PreFilter(ctx, state, test.pod)
		for indexNode, node := range nodes {
			if !preFilterStatus.IsSuccess() {
				klog.Infof("Test name: %v, prefilter failed with status: %v", test.name, preFilterStatus)
			}
			nodeInfo, err := snapshot.Get(node.Name)
			assert.NoError(t, err)
			gotStatus := p.(framework.FilterPlugin).Filter(ctx, state, test.pod, nodeInfo)

			if !reflect.DeepEqual(gotStatus, test.expectStatus[indexNode]) {
				t.Errorf("Test name: %v, node: %s does not match: %v, want: %v", test.name, nodeInfo.Node().Name, gotStatus, test.expectStatus[indexNode])
			}
		}
	}
}

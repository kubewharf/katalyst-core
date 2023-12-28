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

func newTestSharedListerForReserve(pods []*v1.Pod, nodes []*v1.Node) *testSharedLister {
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

func makeTestFilterNodesForReserve() ([]*v1alpha1.CustomNodeResource, []*v1.Node, []*v1.Pod) {
	cnrs := []*v1alpha1.CustomNodeResource{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "node-2numa"},
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
				NodeName: "node-2numa",
			},
		},
	}

	nodes := []*v1.Node{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: "node-2numa",
				Labels: map[string]string{
					"region": "r1", "zone": "z1", "hostname": "node-2numa",
				},
			},
		},
	}

	return cnrs, nodes, pods
}

func TestReserve(t *testing.T) {
	testCases := []struct {
		name string
		pod  *v1.Pod
	}{
		{
			name: "pod without labels and seletors",
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
						consts.PodAnnotationMicroTopologyInterPodAntiAffinity: `{"required": [{"matchLabels": {"antiAffinityKey": "antiAffinityValue"}, "zone":"socket"}]}`,
					},
				},
				Spec: v1.PodSpec{
					NodeName: "",
				},
			},
		},
	}

	c := cache.GetCache()
	util.SetQoSConfig(generic.NewQoSConfiguration())
	for _, test := range testCases {
		// make pods and nodes for testing
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		cnrs, nodes, pods := makeTestFilterNodesForReserve()
		for _, cnr := range cnrs {
			c.AddOrUpdateCNR(cnr)
		}

		// make NUMAInterPodAffinity plugin
		snapshot := newTestSharedListerForReserve(pods, nodes)
		f, err := runtime.NewFramework(nil, nil,
			runtime.WithSnapshotSharedLister(snapshot))
		assert.NoError(t, err)
		p, err := New(nil, f)
		assert.NoError(t, err)

		nodeInfo, err := snapshot.Get(nodes[0].Name)
		assert.NoError(t, err)

		// pod can be allocated on node
		state := framework.NewCycleState()
		_, preFilterStatus := p.(framework.PreFilterPlugin).PreFilter(ctx, state, test.pod)
		if !preFilterStatus.IsSuccess() {
			klog.Infof("Test name: %v, prefilter failed with status: %v", test.name, preFilterStatus)
		}
		gotStatus := p.(framework.FilterPlugin).Filter(ctx, state, test.pod, nodeInfo)
		assert.Nil(t, gotStatus)
		p.(framework.ReservePlugin).Reserve(ctx, state, test.pod, nodes[0].Name)

		// pod can not be allocated after reserve
		state = framework.NewCycleState()
		_, preFilterStatus = p.(framework.PreFilterPlugin).PreFilter(ctx, state, test.pod)
		if !preFilterStatus.IsSuccess() {
			klog.Infof("Test name: %v, prefilter failed with status: %v", test.name, preFilterStatus)
		}
		gotStatus = p.(framework.FilterPlugin).Filter(ctx, state, test.pod, nodeInfo)
		assert.Equal(t, 2, int(gotStatus.Code()))
		p.(framework.ReservePlugin).Unreserve(ctx, state, test.pod, nodes[0].Name)

		// pod can be allocatd again
		state = framework.NewCycleState()
		_, preFilterStatus = p.(framework.PreFilterPlugin).PreFilter(ctx, state, test.pod)
		if !preFilterStatus.IsSuccess() {
			klog.Infof("Test name: %v, prefilter failed with status: %v", test.name, preFilterStatus)
		}
		gotStatus = p.(framework.FilterPlugin).Filter(ctx, state, test.pod, nodeInfo)
		assert.Nil(t, gotStatus)
	}
}

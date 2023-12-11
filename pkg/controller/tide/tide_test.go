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

package tide

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/tools/cache"

	v1alpha12 "github.com/kubewharf/katalyst-api/pkg/apis/tide/v1alpha1"
	katalystbase "github.com/kubewharf/katalyst-core/cmd/base"
)

func TestTide_RunOnce(t1 *testing.T) {
	nodePool := &v1alpha12.TideNodePool{
		ObjectMeta: metav1.ObjectMeta{
			Name: "np1",
		},
		Spec: v1alpha12.TideNodePoolSpec{
			NodeConfigs: v1alpha12.NodeConfigs{
				NodeSelector: map[string]string{"test": "test"},
			},
		},
	}
	type args struct {
		ctx          context.Context
		nodeList     []runtime.Object
		podList      []runtime.Object
		tideNodePool *v1alpha12.TideNodePool
	}
	tests := []struct {
		name                 string
		args                 args
		wantOnlineNodeCount  int
		wantOfflineNodeCount int
	}{
		{
			name: "release empty node to offline",
			args: args{
				ctx: context.Background(),
				nodeList: []runtime.Object{
					buildTideNode(NewNodePoolWrapper(nodePool.DeepCopy()), "n1", 1000, 1000, true),
					buildTideNode(NewNodePoolWrapper(nodePool.DeepCopy()), "n2", 1000, 1000, true),
				},
				podList:      nil,
				tideNodePool: nodePool.DeepCopy(),
			},
			wantOnlineNodeCount:  1,
			wantOfflineNodeCount: 1,
		},
		{
			name: "release empty node to online",
			args: args{
				ctx: context.Background(),
				nodeList: []runtime.Object{
					buildTideNode(NewNodePoolWrapper(nodePool.DeepCopy()), "n3", 1000, 1000, false),
					buildTideNode(NewNodePoolWrapper(nodePool.DeepCopy()), "n4", 1000, 1000, false),
				},
				podList:      []runtime.Object{buildOnlinePod(NewNodePoolWrapper(nodePool.DeepCopy()), "p1", 500, 500)},
				tideNodePool: nodePool.DeepCopy(),
			},
			wantOnlineNodeCount:  1,
			wantOfflineNodeCount: 1,
		},
	}
	for _, tt := range tests {
		t1.Run(tt.name, func(t1 *testing.T) {
			controlCtx, err := katalystbase.GenerateFakeGenericContext(append(tt.args.nodeList, tt.args.podList...))
			if err != nil {
				t1.Error(err)
			}
			t, err := NewTide(tt.args.ctx, controlCtx, nil, nil)
			if err != nil {
				t1.Error(err)
			}
			controlCtx.StartInformer(tt.args.ctx)
			syncd := cache.WaitForCacheSync(tt.args.ctx.Done(), t.nodeListerSynced, t.tideListerSynced, t.podListerSynced)
			assert.True(t1, syncd)
			wrapper := NewNodePoolWrapper(tt.args.tideNodePool)
			checker := func(pod *corev1.Pod) bool {
				return labels.SelectorFromSet(map[string]string{LabelPodTypeKey: LabelOnlinePodValue}).Matches(labels.Set(pod.GetLabels()))
			}
			t.RunOnce(tt.args.ctx, checker, wrapper)
			nodes, err := t.client.KubeClient.CoreV1().Nodes().List(tt.args.ctx, metav1.ListOptions{})
			if err != nil {
				t1.Error(err)
			}
			onlineNodes, offlineNodes := 0, 0
			for _, node := range nodes.Items {
				fmt.Println(node.Labels)
				if wrapper.GetOnlineTideNodeSelector().Matches(labels.Set(node.Labels)) {
					onlineNodes++
				}
				if wrapper.GetOfflineTideNodeSelector().Matches(labels.Set(node.Labels)) {
					offlineNodes++
				}
			}
			fmt.Println(onlineNodes, offlineNodes)
			assert.Equal(t1, onlineNodes, tt.wantOnlineNodeCount)
			assert.Equal(t1, offlineNodes, tt.wantOfflineNodeCount)
		})
	}
}

func buildNode(nodePool NodePoolWrapper, name string, millicpu int64, mem int64) *corev1.Node {
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name:     name,
			SelfLink: fmt.Sprintf("/api/v1/nodes/%s", name),
			Labels:   nodePool.GetNodeSelector(),
		},
		Spec: corev1.NodeSpec{
			ProviderID: name,
		},
		Status: corev1.NodeStatus{
			Capacity: corev1.ResourceList{
				corev1.ResourcePods: *resource.NewQuantity(100, resource.DecimalSI),
			},
		},
	}

	if millicpu >= 0 {
		node.Status.Capacity[corev1.ResourceCPU] = *resource.NewMilliQuantity(millicpu, resource.DecimalSI)
	}
	if mem >= 0 {
		node.Status.Capacity[corev1.ResourceMemory] = *resource.NewQuantity(mem, resource.DecimalSI)
	}

	node.Status.Allocatable = corev1.ResourceList{}
	for k, v := range node.Status.Capacity {
		node.Status.Allocatable[k] = v
	}

	return node
}

// buildTideNode creates a tide node with specified capacity.
func buildTideNode(nodePool NodePoolWrapper, name string, millicpu int64, mem int64, isOnline bool) *corev1.Node {
	node := buildNode(nodePool, name, millicpu, mem)
	node.Labels[nodePool.GetTideLabel().Key] = nodePool.GetTideLabel().Value
	node.Labels[LabelNodePoolKey] = nodePool.GetName()
	if isOnline {
		node.Labels[nodePool.GetOnlineLabel().Key] = nodePool.GetOnlineLabel().Value
		node.Spec.Taints = []corev1.Taint{
			{
				Key:    nodePool.GetEvictOfflinePodTaint().Key,
				Value:  nodePool.GetEvictOfflinePodTaint().Value,
				Effect: corev1.TaintEffect(nodePool.GetEvictOfflinePodTaint().Effect),
			},
		}
	} else {
		node.Labels[nodePool.GetOfflineLabel().Key] = nodePool.GetOfflineLabel().Value
		node.Spec.Taints = []corev1.Taint{
			{
				Key:    nodePool.GetEvictOnlinePodTaint().Key,
				Value:  nodePool.GetEvictOnlinePodTaint().Value,
				Effect: corev1.TaintEffect(nodePool.GetEvictOnlinePodTaint().Effect),
			},
		}
	}

	return node
}

func buildOnlinePod(nodePool NodePoolWrapper, name string, cpu int64, mem int64) *corev1.Pod {
	startTime := metav1.Unix(0, 0)
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			UID:         types.UID(name),
			Namespace:   "default",
			Name:        name,
			SelfLink:    fmt.Sprintf("/api/v1/namespaces/default/pods/%s", name),
			Annotations: map[string]string{},
			Labels:      map[string]string{LabelPodTypeKey: LabelOnlinePodValue},
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Resources: corev1.ResourceRequirements{
						Requests: corev1.ResourceList{},
					},
				},
			},
			Tolerations: []corev1.Toleration{
				{
					Key:               nodePool.GetEvictOfflinePodTaint().Key,
					Operator:          corev1.TolerationOpEqual,
					Value:             nodePool.GetEvictOfflinePodTaint().Value,
					Effect:            corev1.TaintEffect(nodePool.GetEvictOfflinePodTaint().Effect),
					TolerationSeconds: nil,
				},
			},
		},
		Status: corev1.PodStatus{
			StartTime: &startTime,
			Conditions: []corev1.PodCondition{
				{
					Type:               corev1.PodScheduled,
					Status:             corev1.ConditionFalse,
					LastProbeTime:      metav1.Time{},
					LastTransitionTime: metav1.Time{},
					Reason:             corev1.PodReasonUnschedulable,
					Message:            "",
				},
			},
		},
	}

	if cpu >= 0 {
		pod.Spec.Containers[0].Resources.Requests[corev1.ResourceCPU] = *resource.NewMilliQuantity(cpu, resource.DecimalSI)
	}
	if mem >= 0 {
		pod.Spec.Containers[0].Resources.Requests[corev1.ResourceMemory] = *resource.NewQuantity(mem, resource.DecimalSI)
	}

	return pod
}

func TestTide_Reconcile(t1 *testing.T) {
	nodeReverse := intstr.FromString("30%")
	nodePool := &v1alpha12.TideNodePool{
		ObjectMeta: metav1.ObjectMeta{
			Name: "np1",
		},
		Spec: v1alpha12.TideNodePoolSpec{
			NodeConfigs: v1alpha12.NodeConfigs{
				NodeSelector: map[string]string{"test": "test"},
				Reverse: v1alpha12.ReverseOptions{
					Online:  &nodeReverse,
					Offline: &nodeReverse,
				},
			},
		},
	}
	type args struct {
		ctx          context.Context
		nodeList     []runtime.Object
		podList      []runtime.Object
		tideNodePool *v1alpha12.TideNodePool
	}
	tests := []struct {
		name string
		args args

		wantReverseOnlineNodeCount  int
		wantReverseOfflineNodeCount int
		wantReverseTideNodeCount    int
	}{
		{
			name: "normal",
			args: args{
				ctx: context.Background(),
				nodeList: []runtime.Object{
					buildNode(NewNodePoolWrapper(nodePool), "n5", 1000, 1000),
					buildNode(NewNodePoolWrapper(nodePool), "n6", 1000, 1000),
					buildNode(NewNodePoolWrapper(nodePool), "n7", 1000, 1000),
					buildNode(NewNodePoolWrapper(nodePool), "n8", 1000, 1000),
				},
				podList:      nil,
				tideNodePool: nodePool,
			},
			wantReverseOnlineNodeCount:  2,
			wantReverseOfflineNodeCount: 1,
			wantReverseTideNodeCount:    1,
		},
	}
	for _, tt := range tests {
		t1.Run(tt.name, func(t1 *testing.T) {
			controlCtx, err := katalystbase.GenerateFakeGenericContext(append(tt.args.nodeList, tt.args.podList...), []runtime.Object{tt.args.tideNodePool})
			if err != nil {
				t1.Error(err)
			}
			t, err := NewTide(tt.args.ctx, controlCtx, nil, nil)
			if err != nil {
				t1.Error(err)
			}
			controlCtx.StartInformer(tt.args.ctx)
			syncd := cache.WaitForCacheSync(tt.args.ctx.Done(), t.nodeListerSynced, t.tideListerSynced, t.podListerSynced)
			assert.True(t1, syncd)
			t.Reconcile(tt.args.ctx, tt.args.tideNodePool)
			syncd = cache.WaitForCacheSync(tt.args.ctx.Done(), t.nodeListerSynced, t.tideListerSynced, t.podListerSynced)
			assert.True(t1, syncd)
			tideNodePool, err := t.client.InternalClient.TideV1alpha1().TideNodePools().Get(tt.args.ctx, tt.args.tideNodePool.Name, metav1.GetOptions{})
			if err != nil {
				t1.Error(err)
			}
			assert.Equal(t1, len(tideNodePool.Status.ReverseNodes.OnlineNodes), tt.wantReverseOnlineNodeCount)
			assert.Equal(t1, len(tideNodePool.Status.ReverseNodes.OfflineNodes), tt.wantReverseOfflineNodeCount)
			assert.Equal(t1, len(tideNodePool.Status.TideNodes.Nodes), tt.wantReverseTideNodeCount)
		})
	}
}

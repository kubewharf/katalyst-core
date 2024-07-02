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

package loadaware

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	v12 "k8s.io/apimachinery/pkg/apis/meta/v1"
	cache2 "k8s.io/client-go/tools/cache"
	"k8s.io/kubernetes/pkg/scheduler/framework"
	"k8s.io/kubernetes/pkg/scheduler/framework/runtime"

	v1alpha12 "github.com/kubewharf/katalyst-api/pkg/apis/node/v1alpha1"
	"github.com/kubewharf/katalyst-api/pkg/apis/workload/v1alpha1"
	katalyst_base "github.com/kubewharf/katalyst-core/cmd/base"
	"github.com/kubewharf/katalyst-core/pkg/config/generic"
	"github.com/kubewharf/katalyst-core/pkg/scheduler/util"
)

func TestTargetLoadPacking(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name        string
		targetRatio float64
		usageRatio  float64
		expectErr   bool
		expectRes   int64
	}{
		{
			name:        "less than target",
			targetRatio: 50,
			usageRatio:  10,
			expectErr:   false,
			expectRes:   60,
		},
		{
			name:        "greater than target",
			targetRatio: 50,
			usageRatio:  60,
			expectErr:   false,
			expectRes:   40,
		},
		{
			name:        "zero target",
			targetRatio: 0,
			usageRatio:  10,
			expectErr:   true,
			expectRes:   0,
		},
		{
			name:        "target greater than 100",
			targetRatio: 200,
			usageRatio:  10,
			expectErr:   true,
			expectRes:   0,
		},
		{
			name:        "usage less than 0",
			targetRatio: 50,
			usageRatio:  -1,
			expectErr:   false,
			expectRes:   50,
		},
		{
			name:        "usage greater than 100",
			targetRatio: 50,
			usageRatio:  101,
			expectErr:   false,
			expectRes:   0,
		},
		{
			name:        "low usage",
			targetRatio: 30,
			usageRatio:  0.1,
			expectErr:   false,
			expectRes:   30,
		},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			res, err := targetLoadPacking(tc.targetRatio, tc.usageRatio)
			if !tc.expectErr {
				assert.NoError(t, err)
			} else {
				assert.Error(t, err)
			}
			assert.Equal(t, tc.expectRes, res)
		})
	}
}

func TestScore(t *testing.T) {
	t.Parallel()

	util.SetQoSConfig(generic.NewQoSConfiguration())

	for _, tc := range []struct {
		name           string
		pod            *v1.Pod
		lowNode        *v1.Node
		highNode       *v1.Node
		lowNodePods    []*v1.Pod
		highNodePods   []*v1.Pod
		spd            []*v1alpha1.ServiceProfileDescriptor
		npd            []*v1alpha12.NodeProfileDescriptor
		enablePortrait bool
	}{
		{
			name: "enablePortrait",
			pod: &v1.Pod{
				ObjectMeta: v12.ObjectMeta{
					Name:      "pod1",
					UID:       "pod1UID",
					Namespace: "testNs",
					OwnerReferences: []v12.OwnerReference{
						{
							Kind: "Deployment",
							Name: "deployment1",
						},
					},
				},
				Spec: v1.PodSpec{
					Containers: []v1.Container{
						{
							Name: "testContainer",
							Resources: v1.ResourceRequirements{
								Limits: map[v1.ResourceName]resource.Quantity{
									v1.ResourceCPU:    resource.MustParse("8"),
									v1.ResourceMemory: resource.MustParse("16Gi"),
								},
								Requests: map[v1.ResourceName]resource.Quantity{
									v1.ResourceCPU:    resource.MustParse("8"),
									v1.ResourceMemory: resource.MustParse("16Gi"),
								},
							},
						},
					},
				},
			},
			lowNode: &v1.Node{
				ObjectMeta: v12.ObjectMeta{
					Name: "node1",
				},
				Spec: v1.NodeSpec{},
				Status: v1.NodeStatus{
					Capacity: map[v1.ResourceName]resource.Quantity{
						v1.ResourceCPU:    resource.MustParse("32"),
						v1.ResourceMemory: resource.MustParse("64Gi"),
					},
					Allocatable: map[v1.ResourceName]resource.Quantity{
						v1.ResourceCPU:    resource.MustParse("32"),
						v1.ResourceMemory: resource.MustParse("64Gi"),
					},
				},
			},
			highNode: &v1.Node{
				ObjectMeta: v12.ObjectMeta{
					Name: "node2",
				},
				Spec: v1.NodeSpec{},
				Status: v1.NodeStatus{
					Capacity: map[v1.ResourceName]resource.Quantity{
						v1.ResourceCPU:    resource.MustParse("32"),
						v1.ResourceMemory: resource.MustParse("64Gi"),
					},
					Allocatable: map[v1.ResourceName]resource.Quantity{
						v1.ResourceCPU:    resource.MustParse("32"),
						v1.ResourceMemory: resource.MustParse("64Gi"),
					},
				},
			},
			lowNodePods: []*v1.Pod{
				{
					ObjectMeta: v12.ObjectMeta{
						Name:      "pod2",
						UID:       "pod2UID",
						Namespace: "testNs",
						OwnerReferences: []v12.OwnerReference{
							{
								Kind: "Deployment",
								Name: "deployment3",
							},
						},
					},
				},
				{
					ObjectMeta: v12.ObjectMeta{
						Name:      "pod3",
						UID:       "pod3UID",
						Namespace: "testNs",
						OwnerReferences: []v12.OwnerReference{
							{
								Kind: "Deployment",
								Name: "deployment3",
							},
						},
					},
				},
			},
			highNodePods: []*v1.Pod{
				{
					ObjectMeta: v12.ObjectMeta{
						Name:      "pod5",
						UID:       "pod5UID",
						Namespace: "testNs",
						OwnerReferences: []v12.OwnerReference{
							{
								Kind: "Deployment",
								Name: "deployment2",
							},
						},
					},
				},
				{
					ObjectMeta: v12.ObjectMeta{
						Name:      "pod4",
						UID:       "pod4UID",
						Namespace: "testNs",
						OwnerReferences: []v12.OwnerReference{
							{
								Kind: "Deployment",
								Name: "deployment2",
							},
						},
					},
				},
			},
			spd: []*v1alpha1.ServiceProfileDescriptor{
				{
					ObjectMeta: v12.ObjectMeta{
						Name:      "deployment1",
						Namespace: "testNs",
					},
					Status: v1alpha1.ServiceProfileDescriptorStatus{
						AggMetrics: []v1alpha1.AggPodMetrics{
							{
								Scope: spdPortraitScope,
								Items: rangeItems(4, 8*1024*1024*1024),
							},
						},
					},
				},
				{
					ObjectMeta: v12.ObjectMeta{
						Name:      "deployment2",
						Namespace: "testNs",
					},
					Status: v1alpha1.ServiceProfileDescriptorStatus{
						AggMetrics: []v1alpha1.AggPodMetrics{
							{
								Scope: spdPortraitScope,
								Items: fixedItems(4, 8*1024*1024*1024),
							},
						},
					},
				},
				{
					ObjectMeta: v12.ObjectMeta{
						Name:      "deployment3",
						Namespace: "testNs",
					},
					Status: v1alpha1.ServiceProfileDescriptorStatus{
						AggMetrics: []v1alpha1.AggPodMetrics{
							{
								Scope: spdPortraitScope,
								Items: fixedItems(8, 16*1024*1024*1024),
							},
						},
					},
				},
			},
			npd:            []*v1alpha12.NodeProfileDescriptor{},
			enablePortrait: true,
		},
		{
			name: "unablePortrait",
			pod: &v1.Pod{
				ObjectMeta: v12.ObjectMeta{
					Name:      "pod1",
					UID:       "pod1UID",
					Namespace: "testNs",
					OwnerReferences: []v12.OwnerReference{
						{
							Kind: "Deployment",
							Name: "deployment1",
						},
					},
				},
				Spec: v1.PodSpec{
					Containers: []v1.Container{
						{
							Name: "testContainer",
							Resources: v1.ResourceRequirements{
								Limits: map[v1.ResourceName]resource.Quantity{
									v1.ResourceCPU:    resource.MustParse("8"),
									v1.ResourceMemory: resource.MustParse("16Gi"),
								},
								Requests: map[v1.ResourceName]resource.Quantity{
									v1.ResourceCPU:    resource.MustParse("8"),
									v1.ResourceMemory: resource.MustParse("16Gi"),
								},
							},
						},
					},
				},
			},
			lowNode: &v1.Node{
				ObjectMeta: v12.ObjectMeta{
					Name: "node1",
				},
				Spec: v1.NodeSpec{},
				Status: v1.NodeStatus{
					Capacity: map[v1.ResourceName]resource.Quantity{
						v1.ResourceCPU:    resource.MustParse("32"),
						v1.ResourceMemory: resource.MustParse("64Gi"),
					},
					Allocatable: map[v1.ResourceName]resource.Quantity{
						v1.ResourceCPU:    resource.MustParse("32"),
						v1.ResourceMemory: resource.MustParse("64Gi"),
					},
				},
			},
			highNode: &v1.Node{
				ObjectMeta: v12.ObjectMeta{
					Name: "node2",
				},
				Spec: v1.NodeSpec{},
				Status: v1.NodeStatus{
					Capacity: map[v1.ResourceName]resource.Quantity{
						v1.ResourceCPU:    resource.MustParse("32"),
						v1.ResourceMemory: resource.MustParse("64Gi"),
					},
					Allocatable: map[v1.ResourceName]resource.Quantity{
						v1.ResourceCPU:    resource.MustParse("32"),
						v1.ResourceMemory: resource.MustParse("64Gi"),
					},
				},
			},
			lowNodePods: []*v1.Pod{
				{
					ObjectMeta: v12.ObjectMeta{
						Name:      "pod2",
						UID:       "pod2UID",
						Namespace: "testNs",
						OwnerReferences: []v12.OwnerReference{
							{
								Kind: "Deployment",
								Name: "deployment3",
							},
						},
					},
				},
				{
					ObjectMeta: v12.ObjectMeta{
						Name:      "pod3",
						UID:       "pod3UID",
						Namespace: "testNs",
						OwnerReferences: []v12.OwnerReference{
							{
								Kind: "Deployment",
								Name: "deployment3",
							},
						},
					},
				},
			},
			highNodePods: []*v1.Pod{
				{
					ObjectMeta: v12.ObjectMeta{
						Name:      "pod5",
						UID:       "pod5UID",
						Namespace: "testNs",
						OwnerReferences: []v12.OwnerReference{
							{
								Kind: "Deployment",
								Name: "deployment2",
							},
						},
					},
				},
				{
					ObjectMeta: v12.ObjectMeta{
						Name:      "pod4",
						UID:       "pod4UID",
						Namespace: "testNs",
						OwnerReferences: []v12.OwnerReference{
							{
								Kind: "Deployment",
								Name: "deployment2",
							},
						},
					},
				},
			},
			spd: []*v1alpha1.ServiceProfileDescriptor{
				{
					ObjectMeta: v12.ObjectMeta{
						Name:      "deployment1",
						Namespace: "testNs",
					},
					Status: v1alpha1.ServiceProfileDescriptorStatus{
						AggMetrics: []v1alpha1.AggPodMetrics{
							{
								Scope: spdPortraitScope,
								Items: rangeItems(4, 8*1024*1024*1024),
							},
						},
					},
				},
				{
					ObjectMeta: v12.ObjectMeta{
						Name:      "deployment2",
						Namespace: "testNs",
					},
					Status: v1alpha1.ServiceProfileDescriptorStatus{
						AggMetrics: []v1alpha1.AggPodMetrics{
							{
								Scope: spdPortraitScope,
								Items: fixedItems(4, 8*1024*1024*1024),
							},
						},
					},
				},
				{
					ObjectMeta: v12.ObjectMeta{
						Name:      "deployment3",
						Namespace: "testNs",
					},
					Status: v1alpha1.ServiceProfileDescriptorStatus{
						AggMetrics: []v1alpha1.AggPodMetrics{
							{
								Scope: spdPortraitScope,
								Items: fixedItems(8, 16*1024*1024*1024),
							},
						},
					},
				},
			},
			npd: []*v1alpha12.NodeProfileDescriptor{
				{
					ObjectMeta: v12.ObjectMeta{
						Name: "node1",
					},
					Status: v1alpha12.NodeProfileDescriptorStatus{
						NodeMetrics: []v1alpha12.ScopedNodeMetrics{
							{
								Scope: loadAwareMetricScope,
								Metrics: []v1alpha12.MetricValue{
									{
										MetricName: "cpu",
										Value:      resource.MustParse("9088m"),
										Window:     &v12.Duration{Duration: 15 * time.Minute},
										Timestamp:  v12.Time{Time: time.Now()},
									},
									{
										MetricName: "memory",
										Value:      resource.MustParse("9035916Ki"),
										Window:     &v12.Duration{Duration: 15 * time.Minute},
										Timestamp:  v12.Time{Time: time.Now()},
									},
									{
										MetricName: "cpu",
										Value:      resource.MustParse("10090m"),
										Window:     &v12.Duration{Duration: time.Hour},
										Timestamp:  v12.Time{Time: time.Now()},
									},
									{
										MetricName: "memory",
										Value:      resource.MustParse("9035916Ki"),
										Window:     &v12.Duration{Duration: time.Hour},
										Timestamp:  v12.Time{Time: time.Now()},
									},
									{
										MetricName: "cpu",
										Value:      resource.MustParse("12088m"),
										Window:     &v12.Duration{Duration: 24 * time.Hour},
										Timestamp:  v12.Time{Time: time.Now()},
									},
									{
										MetricName: "memory",
										Value:      resource.MustParse("9035916Ki"),
										Window:     &v12.Duration{Duration: 24 * time.Hour},
										Timestamp:  v12.Time{Time: time.Now()},
									},
								},
							},
						},
					},
				},
				{
					ObjectMeta: v12.ObjectMeta{
						Name: "node2",
					},
					Status: v1alpha12.NodeProfileDescriptorStatus{
						NodeMetrics: []v1alpha12.ScopedNodeMetrics{
							{
								Scope: loadAwareMetricScope,
								Metrics: []v1alpha12.MetricValue{
									{
										MetricName: "cpu",
										Value:      resource.MustParse("1088m"),
										Window:     &v12.Duration{Duration: 15 * time.Minute},
										Timestamp:  v12.Time{Time: time.Now()},
									},
									{
										MetricName: "memory",
										Value:      resource.MustParse("5035916Ki"),
										Window:     &v12.Duration{Duration: 15 * time.Minute},
										Timestamp:  v12.Time{Time: time.Now()},
									},
									{
										MetricName: "cpu",
										Value:      resource.MustParse("1090m"),
										Window:     &v12.Duration{Duration: time.Hour},
										Timestamp:  v12.Time{Time: time.Now()},
									},
									{
										MetricName: "memory",
										Value:      resource.MustParse("5035916Ki"),
										Window:     &v12.Duration{Duration: time.Hour},
										Timestamp:  v12.Time{Time: time.Now()},
									},
									{
										MetricName: "cpu",
										Value:      resource.MustParse("1088m"),
										Window:     &v12.Duration{Duration: 24 * time.Hour},
										Timestamp:  v12.Time{Time: time.Now()},
									},
									{
										MetricName: "memory",
										Value:      resource.MustParse("5035916Ki"),
										Window:     &v12.Duration{Duration: 24 * time.Hour},
										Timestamp:  v12.Time{Time: time.Now()},
									},
								},
							},
						},
					},
				},
			},
			enablePortrait: false,
		},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			lowNodeInfo := framework.NewNodeInfo()
			lowNodeInfo.SetNode(tc.lowNode)
			for _, pod := range tc.lowNodePods {
				lowNodeInfo.AddPod(pod)
			}

			highNodeInfo := framework.NewNodeInfo()
			highNodeInfo.SetNode(tc.highNode)
			for _, pod := range tc.highNodePods {
				highNodeInfo.AddPod(pod)
			}

			fw, err := runtime.NewFramework(nil, nil,
				runtime.WithSnapshotSharedLister(newTestSharedLister(nil, []*v1.Node{tc.lowNode, tc.highNode})))
			assert.NoError(t, err)

			controlCtx, err := katalyst_base.GenerateFakeGenericContext()
			assert.NoError(t, err)

			p := &Plugin{
				handle:       fw,
				args:         makeTestArgs(),
				spdLister:    controlCtx.InternalInformerFactory.Workload().V1alpha1().ServiceProfileDescriptors().Lister(),
				npdLister:    controlCtx.InternalInformerFactory.Node().V1alpha1().NodeProfileDescriptors().Lister(),
				spdHasSynced: controlCtx.InternalInformerFactory.Workload().V1alpha1().ServiceProfileDescriptors().Informer().HasSynced,
				cache: &Cache{
					NodePodInfo: map[string]*NodeCache{},
				},
			}
			*p.args.EnablePortrait = tc.enablePortrait
			p.cache.SetSPDLister(p)

			for _, pr := range tc.spd {
				_, err = controlCtx.Client.InternalClient.WorkloadV1alpha1().ServiceProfileDescriptors(pr.Namespace).
					Create(context.TODO(), pr, v12.CreateOptions{})
				assert.NoError(t, err)
			}
			for _, n := range tc.npd {
				_, err = controlCtx.Client.InternalClient.NodeV1alpha1().NodeProfileDescriptors().
					Create(context.TODO(), n, v12.CreateOptions{})
				assert.NoError(t, err)
			}

			controlCtx.StartInformer(context.TODO())

			// wait for portrait synced
			if !cache2.WaitForCacheSync(context.TODO().Done(), p.spdHasSynced) {
				t.Error("wait for portrait informer synced fail")
				t.FailNow()
			}

			// add pod to cache
			for _, pod := range tc.lowNodePods {
				p.cache.addPod(tc.lowNode.Name, pod, time.Now())
			}
			for _, pod := range tc.highNodePods {
				p.cache.addPod(tc.highNode.Name, pod, time.Now())
			}

			lowScore, stat := p.Score(context.TODO(), nil, tc.pod, tc.lowNode.Name)
			assert.Nil(t, stat)
			assert.NotZero(t, lowScore)

			highScore, stat := p.Score(context.TODO(), nil, tc.pod, tc.highNode.Name)
			assert.Nil(t, stat)
			assert.NotZero(t, highScore)

			assert.Greater(t, highScore, lowScore)
		})
	}
}

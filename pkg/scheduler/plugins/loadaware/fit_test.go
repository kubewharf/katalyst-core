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
	"math/rand"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	v12 "k8s.io/apimachinery/pkg/apis/meta/v1"
	cache2 "k8s.io/client-go/tools/cache"
	"k8s.io/kubernetes/pkg/scheduler/framework"
	"k8s.io/kubernetes/pkg/scheduler/framework/runtime"
	"k8s.io/metrics/pkg/apis/metrics/v1beta1"
	"k8s.io/utils/pointer"

	v1alpha12 "github.com/kubewharf/katalyst-api/pkg/apis/node/v1alpha1"
	"github.com/kubewharf/katalyst-api/pkg/apis/scheduling/config"
	"github.com/kubewharf/katalyst-api/pkg/apis/workload/v1alpha1"
	"github.com/kubewharf/katalyst-api/pkg/consts"
	katalyst_base "github.com/kubewharf/katalyst-core/cmd/base"
	"github.com/kubewharf/katalyst-core/pkg/config/generic"
	"github.com/kubewharf/katalyst-core/pkg/scheduler/util"
)

func TestFilter(t *testing.T) {
	t.Parallel()

	util.SetQoSConfig(generic.NewQoSConfiguration())

	for _, tc := range []struct {
		name      string
		pod       *v1.Pod
		node      *v1.Node
		pods      []*v1.Pod
		npd       *v1alpha12.NodeProfileDescriptor
		portraits []*v1alpha1.ServiceProfileDescriptor
		expectRes *framework.Status
	}{
		{
			name: "filter success",
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
			node: &v1.Node{
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
			pods: []*v1.Pod{
				{
					ObjectMeta: v12.ObjectMeta{
						Name:      "pod2",
						UID:       "pod2UID",
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
			portraits: []*v1alpha1.ServiceProfileDescriptor{
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
			npd: &v1alpha12.NodeProfileDescriptor{
				ObjectMeta: v12.ObjectMeta{
					Name: "node1",
				},
				Spec: v1alpha12.NodeProfileDescriptorSpec{},
				Status: v1alpha12.NodeProfileDescriptorStatus{
					NodeMetrics: []v1alpha12.ScopedNodeMetrics{
						{
							Scope: loadAwareMetricScope,
							Metrics: []v1alpha12.MetricValue{
								{
									MetricName: "cpu",
									Value:      resource.MustParse("1088m"),
									Window:     &metav1.Duration{Duration: 15 * time.Minute},
									Timestamp:  metav1.Time{Time: time.Now()},
								},
								{
									MetricName: "memory",
									Value:      resource.MustParse("5035916Ki"),
									Window:     &metav1.Duration{Duration: 15 * time.Minute},
									Timestamp:  metav1.Time{Time: time.Now()},
								},
								{
									MetricName: "cpu",
									Value:      resource.MustParse("1090m"),
									Window:     &metav1.Duration{Duration: 5 * time.Minute},
									Timestamp:  metav1.Time{Time: time.Now()},
								},
								{
									MetricName: "memory",
									Value:      resource.MustParse("5035916Ki"),
									Window:     &metav1.Duration{Duration: 5 * time.Minute},
									Timestamp:  metav1.Time{Time: time.Now()},
								},
							},
						},
					},
				},
			},
			expectRes: nil,
		},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			nodeInfo := framework.NewNodeInfo()
			nodeInfo.SetNode(tc.node)
			for _, pod := range tc.pods {
				nodeInfo.AddPod(pod)
			}
			fw, err := runtime.NewFramework(nil, nil,
				runtime.WithSnapshotSharedLister(newTestSharedLister(tc.pods, []*v1.Node{tc.node})))
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
			p.cache.SetSPDLister(p)

			for _, pr := range tc.portraits {
				_, err = controlCtx.Client.InternalClient.WorkloadV1alpha1().ServiceProfileDescriptors(pr.Namespace).
					Create(context.TODO(), pr, v12.CreateOptions{})
				assert.NoError(t, err)
			}
			_, err = controlCtx.Client.InternalClient.NodeV1alpha1().NodeProfileDescriptors().
				Create(context.TODO(), tc.npd, v12.CreateOptions{})
			assert.NoError(t, err)
			controlCtx.StartInformer(context.TODO())

			// wait for portrait synced
			if !cache2.WaitForCacheSync(context.TODO().Done(), p.spdHasSynced) {
				t.Error("wait for portrait informer synced fail")
				t.FailNow()
			}

			// add pod to cache
			for _, pod := range tc.pods {
				p.cache.addPod(tc.node.Name, pod, time.Now())
			}

			status := p.Filter(context.TODO(), nil, tc.pod, nodeInfo)

			if tc.expectRes == nil {
				assert.Nil(t, status)
			} else {
				assert.Equal(t, tc.expectRes.Code(), status.Code())
			}
		})
	}
}

func TestFitByPortrait(t *testing.T) {
	t.Parallel()

	util.SetQoSConfig(generic.NewQoSConfiguration())

	for _, tc := range []struct {
		name      string
		pod       *v1.Pod
		node      *v1.Node
		pods      []*v1.Pod
		portraits []*v1alpha1.ServiceProfileDescriptor
		expectRes *framework.Status
	}{
		{
			name: "",
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
			node: &v1.Node{
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
			pods: []*v1.Pod{
				{
					ObjectMeta: v12.ObjectMeta{
						Name:      "pod2",
						UID:       "pod2UID",
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
			portraits: []*v1alpha1.ServiceProfileDescriptor{
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
			expectRes: nil,
		},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			nodeInfo := framework.NewNodeInfo()
			nodeInfo.SetNode(tc.node)
			for _, pod := range tc.pods {
				nodeInfo.AddPod(pod)
			}
			fw, err := runtime.NewFramework(nil, nil,
				runtime.WithSnapshotSharedLister(newTestSharedLister(tc.pods, []*v1.Node{tc.node})))
			assert.NoError(t, err)

			controlCtx, err := katalyst_base.GenerateFakeGenericContext()
			assert.NoError(t, err)

			p := &Plugin{
				handle:       fw,
				args:         makeTestArgs(),
				spdLister:    controlCtx.InternalInformerFactory.Workload().V1alpha1().ServiceProfileDescriptors().Lister(),
				spdHasSynced: controlCtx.InternalInformerFactory.Workload().V1alpha1().ServiceProfileDescriptors().Informer().HasSynced,
				cache: &Cache{
					NodePodInfo: map[string]*NodeCache{},
				},
			}
			p.cache.SetSPDLister(p)

			for _, pr := range tc.portraits {
				_, err = controlCtx.Client.InternalClient.WorkloadV1alpha1().ServiceProfileDescriptors(pr.Namespace).
					Create(context.TODO(), pr, v12.CreateOptions{})
				assert.NoError(t, err)
			}
			controlCtx.StartInformer(context.TODO())

			// wait for portrait synced
			if !cache2.WaitForCacheSync(context.TODO().Done(), p.spdHasSynced) {
				t.Error("wait for portrait informer synced fail")
				t.FailNow()
			}

			// add pod to cache
			for _, pod := range tc.pods {
				p.cache.addPod(tc.node.Name, pod, time.Now())
			}

			status := p.fitByPortrait(tc.pod, nodeInfo)

			if tc.expectRes == nil {
				assert.Nil(t, status)
			} else {
				assert.Equal(t, tc.expectRes.Code(), status.Code())
			}
		})
	}
}

func TestFitByNPD(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name      string
		node      *v1.Node
		npd       *v1alpha12.NodeProfileDescriptor
		expectRes *framework.Status
	}{
		{
			name: "less than threshold",
			node: &v1.Node{
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
			npd: &v1alpha12.NodeProfileDescriptor{
				ObjectMeta: v12.ObjectMeta{
					Name: "node1",
				},
				Spec: v1alpha12.NodeProfileDescriptorSpec{},
				Status: v1alpha12.NodeProfileDescriptorStatus{
					NodeMetrics: []v1alpha12.ScopedNodeMetrics{
						{
							Scope: loadAwareMetricScope,
							Metrics: []v1alpha12.MetricValue{
								{
									MetricName: "cpu",
									Value:      resource.MustParse("1088m"),
									Window:     &metav1.Duration{Duration: 15 * time.Minute},
									Timestamp:  metav1.Time{Time: time.Now()},
								},
								{
									MetricName: "memory",
									Value:      resource.MustParse("5035916Ki"),
									Window:     &metav1.Duration{Duration: 15 * time.Minute},
									Timestamp:  metav1.Time{Time: time.Now()},
								},
								{
									MetricName: "cpu",
									Value:      resource.MustParse("1090m"),
									Window:     &metav1.Duration{Duration: 5 * time.Minute},
									Timestamp:  metav1.Time{Time: time.Now()},
								},
								{
									MetricName: "memory",
									Value:      resource.MustParse("5035916Ki"),
									Window:     &metav1.Duration{Duration: 5 * time.Minute},
									Timestamp:  metav1.Time{Time: time.Now()},
								},
							},
						},
					},
				},
			},
			expectRes: nil,
		},
		{
			name: "more than threshold",
			node: &v1.Node{
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
			npd: &v1alpha12.NodeProfileDescriptor{
				ObjectMeta: v12.ObjectMeta{
					Name: "node1",
				},
				Spec: v1alpha12.NodeProfileDescriptorSpec{},
				Status: v1alpha12.NodeProfileDescriptorStatus{
					NodeMetrics: []v1alpha12.ScopedNodeMetrics{
						{
							Scope: loadAwareMetricScope,
							Metrics: []v1alpha12.MetricValue{
								{
									MetricName: "cpu",
									Value:      resource.MustParse("21088m"),
									Window:     &metav1.Duration{Duration: 15 * time.Minute},
									Timestamp:  metav1.Time{Time: time.Now()},
								},
								{
									MetricName: "memory",
									Value:      resource.MustParse("5035916Ki"),
									Window:     &metav1.Duration{Duration: 15 * time.Minute},
									Timestamp:  metav1.Time{Time: time.Now()},
								},
								{
									MetricName: "cpu",
									Value:      resource.MustParse("1090m"),
									Window:     &metav1.Duration{Duration: 5 * time.Minute},
									Timestamp:  metav1.Time{Time: time.Now()},
								},
								{
									MetricName: "memory",
									Value:      resource.MustParse("5035916Ki"),
									Window:     &metav1.Duration{Duration: 5 * time.Minute},
									Timestamp:  metav1.Time{Time: time.Now()},
								},
							},
						},
					},
				},
			},
			expectRes: framework.NewStatus(framework.Unschedulable, ""),
		},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			nodeInfo := framework.NewNodeInfo()
			nodeInfo.SetNode(tc.node)

			controlCtx, err := katalyst_base.GenerateFakeGenericContext()
			assert.NoError(t, err)

			p := &Plugin{
				args:      makeTestArgs(),
				npdLister: controlCtx.InternalInformerFactory.Node().V1alpha1().NodeProfileDescriptors().Lister(),
				cache: &Cache{
					NodePodInfo: map[string]*NodeCache{},
				},
			}

			_, err = controlCtx.Client.InternalClient.NodeV1alpha1().NodeProfileDescriptors().
				Create(context.TODO(), tc.npd, v12.CreateOptions{})
			assert.NoError(t, err)
			controlCtx.StartInformer(context.TODO())
			time.Sleep(time.Second)

			status := p.fitByNPD(nodeInfo)

			if tc.expectRes == nil {
				assert.Nil(t, status)
			} else {
				assert.Equal(t, tc.expectRes.Code(), status.Code())
			}
		})
	}
}

func fixedItems(cpu, memory int64) []v1beta1.PodMetrics {
	res := make([]v1beta1.PodMetrics, portraitItemsLength, portraitItemsLength)

	t := time.Now()
	for i := 0; i < portraitItemsLength; i++ {
		res[i].Timestamp = metav1.Time{Time: t.Add(time.Duration(i) * time.Hour)}
		res[i].Containers = []v1beta1.ContainerMetrics{
			{
				Name: spdPortraitLoadAwareMetricName,
				Usage: map[v1.ResourceName]resource.Quantity{
					cpuUsageMetric:    *resource.NewQuantity(cpu, resource.DecimalSI),
					memoryUsageMetric: *resource.NewQuantity(memory, resource.BinarySI),
				},
			},
		}
	}

	return res
}

func rangeItems(cpu, memory int64) []v1beta1.PodMetrics {
	res := make([]v1beta1.PodMetrics, portraitItemsLength, portraitItemsLength)

	t := time.Now()
	rand.Seed(t.UnixNano())
	for i := 0; i < portraitItemsLength; i++ {
		res[i].Timestamp = metav1.Time{Time: t.Add(time.Duration(i) * time.Hour)}
		res[i].Containers = []v1beta1.ContainerMetrics{
			{
				Name: spdPortraitLoadAwareMetricName,
				Usage: map[v1.ResourceName]resource.Quantity{
					cpuUsageMetric:    *resource.NewQuantity(rand.Int63n(cpu), resource.DecimalSI),
					memoryUsageMetric: *resource.NewQuantity(rand.Int63n(memory), resource.BinarySI),
				},
			},
		}
	}

	return res
}

func makeTestArgs() *config.LoadAwareArgs {
	args := &config.LoadAwareArgs{
		EnablePortrait: pointer.Bool(true),
		ResourceToTargetMap: map[v1.ResourceName]int64{
			v1.ResourceCPU:    40,
			v1.ResourceMemory: 50,
		},
		ResourceToThresholdMap: map[v1.ResourceName]int64{
			v1.ResourceCPU:    60,
			v1.ResourceMemory: 80,
		},
		ResourceToScalingFactorMap: map[v1.ResourceName]int64{
			v1.ResourceCPU:    100,
			v1.ResourceMemory: 100,
		},
		ResourceToWeightMap: map[v1.ResourceName]int64{
			v1.ResourceCPU:    1,
			v1.ResourceMemory: 1,
		},
		CalculateIndicatorWeight: map[config.IndicatorType]int64{
			consts.Usage15MinAvgKey: 5,
			consts.Usage1HourMaxKey: 3,
			consts.Usage1DayMaxKey:  2,
		},
		NodeMetricsExpiredSeconds:    new(int64),
		PodAnnotationLoadAwareEnable: new(string),
	}
	*args.NodeMetricsExpiredSeconds = 300
	*args.PodAnnotationLoadAwareEnable = ""

	return args
}

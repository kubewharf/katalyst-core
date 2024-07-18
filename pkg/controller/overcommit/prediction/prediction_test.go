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

package prediction

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	v13 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	v12 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/rand"
	"k8s.io/client-go/tools/cache"
	"k8s.io/metrics/pkg/apis/metrics/v1beta1"

	"github.com/kubewharf/katalyst-api/pkg/apis/workload/v1alpha1"
	"github.com/kubewharf/katalyst-api/pkg/consts"
	katalyst_base "github.com/kubewharf/katalyst-core/cmd/base"
	"github.com/kubewharf/katalyst-core/pkg/config/controller"
	"github.com/kubewharf/katalyst-core/pkg/controller/overcommit/prediction/common"
	"github.com/kubewharf/katalyst-core/pkg/controller/overcommit/prediction/predictor"
	"github.com/kubewharf/katalyst-core/pkg/controller/overcommit/prediction/provider/prom"
	"github.com/kubewharf/katalyst-core/pkg/util/datasource/prometheus"
)

func TestReconcileNodes(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name    string
		conf    *controller.OvercommitConfig
		node    *v1.Node
		pods    []*v1.Pod
		spds    []*v1alpha1.ServiceProfileDescriptor
		caches  map[string]map[string]*common.TimeSeries
		success bool
	}{
		{
			name:    "test1",
			success: true,
			conf: &controller.OvercommitConfig{
				Prediction: controller.PredictionConfig{
					CPUScaleFactor:         0.5,
					MemoryScaleFactor:      1,
					NodeCPUTargetLoad:      0.8,
					PodEstimatedCPULoad:    0.6,
					NodeMemoryTargetLoad:   0.9,
					PodEstimatedMemoryLoad: 0.8,
				},
			},
			node: &v1.Node{
				ObjectMeta: v12.ObjectMeta{
					Name: "node1",
					Annotations: map[string]string{
						consts.NodeAnnotationOriginalAllocatableCPUKey:    "31200m",
						consts.NodeAnnotationOriginalAllocatableMemoryKey: "29258114498560m",
					},
				},
			},
			spds: []*v1alpha1.ServiceProfileDescriptor{
				{
					ObjectMeta: v12.ObjectMeta{
						Name:      "testDeployment2",
						Namespace: "default",
					},
					Status: v1alpha1.ServiceProfileDescriptorStatus{
						AggMetrics: []v1alpha1.AggPodMetrics{
							testAggMetrics(4000, 8*1024*1024*1024, 24),
						},
					},
				},
				{
					ObjectMeta: v12.ObjectMeta{
						Name:      "testDeployment1",
						Namespace: "default",
					},
					Status: v1alpha1.ServiceProfileDescriptorStatus{
						AggMetrics: []v1alpha1.AggPodMetrics{
							testAggMetrics(2000, 4*1024*1024*1024, 24),
						},
					},
				},
			},
			pods: []*v1.Pod{
				{
					ObjectMeta: v12.ObjectMeta{
						Name:      "pod2",
						UID:       "pod2",
						Namespace: "default",
						OwnerReferences: []v12.OwnerReference{
							{
								APIVersion: "app/v1",
								Kind:       "Deployment",
								Name:       "testDeployment2",
							},
						},
					},
					Spec: v1.PodSpec{
						NodeName: "node1",
						Containers: []v1.Container{
							{
								Resources: v1.ResourceRequirements{
									Requests: map[v1.ResourceName]resource.Quantity{
										v1.ResourceCPU:    resource.MustParse("4"),
										v1.ResourceMemory: resource.MustParse("8Gi"),
									},
								},
							},
						},
					},
				},
				{
					ObjectMeta: v12.ObjectMeta{
						Name:      "pod1",
						UID:       "pod1",
						Namespace: "default",
						OwnerReferences: []v12.OwnerReference{
							{
								APIVersion: "app/v1",
								Kind:       "Deployment",
								Name:       "testDeployment1",
							},
						},
					},
					Spec: v1.PodSpec{
						NodeName: "node1",
						Containers: []v1.Container{
							{
								Resources: v1.ResourceRequirements{
									Requests: map[v1.ResourceName]resource.Quantity{
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
			name:    "node without pods",
			success: false,
			conf: &controller.OvercommitConfig{
				Prediction: controller.PredictionConfig{
					CPUScaleFactor:         0.5,
					MemoryScaleFactor:      1,
					NodeCPUTargetLoad:      0.8,
					PodEstimatedCPULoad:    0.6,
					NodeMemoryTargetLoad:   0.9,
					PodEstimatedMemoryLoad: 0.8,
				},
			},
			node: &v1.Node{
				ObjectMeta: v12.ObjectMeta{
					Name: "node1",
					Annotations: map[string]string{
						consts.NodeAnnotationOriginalAllocatableCPUKey:    "31200m",
						consts.NodeAnnotationOriginalAllocatableMemoryKey: "29258114498560m",
					},
				},
			},
			pods: []*v1.Pod{},
			spds: nil,
		},
		{
			name:    "pod time series error",
			success: false,
			conf: &controller.OvercommitConfig{
				Prediction: controller.PredictionConfig{
					CPUScaleFactor:         0.5,
					MemoryScaleFactor:      1,
					NodeCPUTargetLoad:      0.8,
					PodEstimatedCPULoad:    0.6,
					NodeMemoryTargetLoad:   0.9,
					PodEstimatedMemoryLoad: 0.8,
				},
			},
			node: &v1.Node{
				ObjectMeta: v12.ObjectMeta{
					Name: "node1",
					Annotations: map[string]string{
						consts.NodeAnnotationOriginalAllocatableCPUKey:    "31200m",
						consts.NodeAnnotationOriginalAllocatableMemoryKey: "29258114498560m",
					},
				},
			},
			spds: []*v1alpha1.ServiceProfileDescriptor{
				{
					ObjectMeta: v12.ObjectMeta{
						Name:      "testDeployment2",
						Namespace: "default",
					},
					Status: v1alpha1.ServiceProfileDescriptorStatus{
						AggMetrics: []v1alpha1.AggPodMetrics{
							testAggMetrics(4000, 8*1024*1024*1024, 20),
						},
					},
				},
				{
					ObjectMeta: v12.ObjectMeta{
						Name:      "testDeployment1",
						Namespace: "default",
					},
					Status: v1alpha1.ServiceProfileDescriptorStatus{
						AggMetrics: []v1alpha1.AggPodMetrics{
							testAggMetrics(2000, 4*1024*1024*1024, 18),
						},
					},
				},
			},
			pods: []*v1.Pod{
				{
					ObjectMeta: v12.ObjectMeta{
						Name:      "pod2",
						UID:       "pod2",
						Namespace: "default",
						OwnerReferences: []v12.OwnerReference{
							{
								APIVersion: "app/v1",
								Kind:       "Deployment",
								Name:       "testDeployment2",
							},
						},
					},
					Spec: v1.PodSpec{
						NodeName: "node1",
						Containers: []v1.Container{
							{
								Resources: v1.ResourceRequirements{
									Requests: map[v1.ResourceName]resource.Quantity{
										v1.ResourceCPU:    resource.MustParse("4"),
										v1.ResourceMemory: resource.MustParse("8Gi"),
									},
								},
							},
						},
					},
				}, {
					ObjectMeta: v12.ObjectMeta{
						Name:      "pod1",
						UID:       "pod1",
						Namespace: "default",
						OwnerReferences: []v12.OwnerReference{
							{
								APIVersion: "app/v1",
								Kind:       "Deployment",
								Name:       "testDeployment1",
							},
						},
					},
					Spec: v1.PodSpec{
						NodeName: "node1",
						Containers: []v1.Container{
							{
								Resources: v1.ResourceRequirements{
									Requests: map[v1.ResourceName]resource.Quantity{
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
			name:    "pod without owner",
			success: true,
			conf: &controller.OvercommitConfig{
				Prediction: controller.PredictionConfig{
					CPUScaleFactor:         0.5,
					MemoryScaleFactor:      1,
					NodeCPUTargetLoad:      0.8,
					PodEstimatedCPULoad:    0.6,
					NodeMemoryTargetLoad:   0.9,
					PodEstimatedMemoryLoad: 0.8,
				},
			},
			node: &v1.Node{
				ObjectMeta: v12.ObjectMeta{
					Name: "node1",
					Annotations: map[string]string{
						consts.NodeAnnotationOriginalAllocatableCPUKey:    "31200m",
						consts.NodeAnnotationOriginalAllocatableMemoryKey: "29258114498560m",
					},
				},
			},
			pods: []*v1.Pod{
				{
					ObjectMeta: v12.ObjectMeta{
						Name:            "pod2",
						UID:             "pod2",
						Namespace:       "default",
						OwnerReferences: []v12.OwnerReference{},
					},
					Spec: v1.PodSpec{
						NodeName: "node1",
						Containers: []v1.Container{
							{
								Resources: v1.ResourceRequirements{
									Requests: map[v1.ResourceName]resource.Quantity{
										v1.ResourceCPU:    resource.MustParse("4"),
										v1.ResourceMemory: resource.MustParse("8Gi"),
									},
								},
							},
						},
					},
				}, {
					ObjectMeta: v12.ObjectMeta{
						Name:      "pod1",
						UID:       "pod1",
						Namespace: "default",
						OwnerReferences: []v12.OwnerReference{
							{
								APIVersion: "app/v1",
								Kind:       "Deployment",
								Name:       "testDeployment1",
							},
						},
					},
					Spec: v1.PodSpec{
						NodeName: "node1",
						Containers: []v1.Container{
							{
								Resources: v1.ResourceRequirements{
									Requests: map[v1.ResourceName]resource.Quantity{
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
			name:    "spd without data",
			success: true,
			conf: &controller.OvercommitConfig{
				Prediction: controller.PredictionConfig{
					CPUScaleFactor:         0.5,
					MemoryScaleFactor:      1,
					NodeCPUTargetLoad:      0.8,
					PodEstimatedCPULoad:    0.6,
					NodeMemoryTargetLoad:   0.9,
					PodEstimatedMemoryLoad: 0.8,
				},
			},
			node: &v1.Node{
				ObjectMeta: v12.ObjectMeta{
					Name: "node1",
					Annotations: map[string]string{
						consts.NodeAnnotationOriginalAllocatableCPUKey:    "31200m",
						consts.NodeAnnotationOriginalAllocatableMemoryKey: "29258114498560m",
					},
				},
			},
			spds: []*v1alpha1.ServiceProfileDescriptor{
				{
					ObjectMeta: v12.ObjectMeta{
						Name:      "testDeployment2",
						Namespace: "default",
					},
					Status: v1alpha1.ServiceProfileDescriptorStatus{
						AggMetrics: []v1alpha1.AggPodMetrics{},
					},
				},
				{
					ObjectMeta: v12.ObjectMeta{
						Name:      "testDeployment1",
						Namespace: "default",
					},
					Status: v1alpha1.ServiceProfileDescriptorStatus{
						AggMetrics: []v1alpha1.AggPodMetrics{},
					},
				},
			},
			pods: []*v1.Pod{
				{
					ObjectMeta: v12.ObjectMeta{
						Name:      "pod2",
						UID:       "pod2",
						Namespace: "default",
						OwnerReferences: []v12.OwnerReference{
							{
								APIVersion: "app/v1",
								Kind:       "Deployment",
								Name:       "testDeployment2",
							},
						},
					},
					Spec: v1.PodSpec{
						NodeName: "node1",
						Containers: []v1.Container{
							{
								Resources: v1.ResourceRequirements{
									Requests: map[v1.ResourceName]resource.Quantity{
										v1.ResourceCPU:    resource.MustParse("4"),
										v1.ResourceMemory: resource.MustParse("8Gi"),
									},
								},
							},
						},
					},
				}, {
					ObjectMeta: v12.ObjectMeta{
						Name:      "pod1",
						UID:       "pod1",
						Namespace: "default",
						OwnerReferences: []v12.OwnerReference{
							{
								APIVersion: "app/v1",
								Kind:       "Deployment",
								Name:       "testDeployment1",
							},
						},
					},
					Spec: v1.PodSpec{
						NodeName: "node1",
						Containers: []v1.Container{
							{
								Resources: v1.ResourceRequirements{
									Requests: map[v1.ResourceName]resource.Quantity{
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
			name:    "predict by cache",
			success: true,
			conf: &controller.OvercommitConfig{
				Prediction: controller.PredictionConfig{
					CPUScaleFactor:         0.5,
					MemoryScaleFactor:      1,
					NodeCPUTargetLoad:      0.8,
					PodEstimatedCPULoad:    0.6,
					NodeMemoryTargetLoad:   0.9,
					PodEstimatedMemoryLoad: 0.8,
				},
			},
			node: &v1.Node{
				ObjectMeta: v12.ObjectMeta{
					Name: "node1",
					Annotations: map[string]string{
						consts.NodeAnnotationOriginalAllocatableCPUKey:    "31200m",
						consts.NodeAnnotationOriginalAllocatableMemoryKey: "29258114498560m",
					},
				},
			},
			caches: map[string]map[string]*common.TimeSeries{
				workloadUsageCacheName("default", "Deployment", "testDeployment2"): testWorkloadCaches(4000, 8*1024*1024*1024, 24),
				workloadUsageCacheName("default", "Deployment", "testDeployment1"): testWorkloadCaches(2000, 4*1024*1024*1024, 24),
			},
			pods: []*v1.Pod{
				{
					ObjectMeta: v12.ObjectMeta{
						Name:      "pod2",
						UID:       "pod2",
						Namespace: "default",
						OwnerReferences: []v12.OwnerReference{
							{
								APIVersion: "app/v1",
								Kind:       "Deployment",
								Name:       "testDeployment2",
							},
						},
					},
					Spec: v1.PodSpec{
						NodeName: "node1",
						Containers: []v1.Container{
							{
								Resources: v1.ResourceRequirements{
									Requests: map[v1.ResourceName]resource.Quantity{
										v1.ResourceCPU:    resource.MustParse("4"),
										v1.ResourceMemory: resource.MustParse("8Gi"),
									},
								},
							},
						},
					},
				},
				{
					ObjectMeta: v12.ObjectMeta{
						Name:      "pod1",
						UID:       "pod1",
						Namespace: "default",
						OwnerReferences: []v12.OwnerReference{
							{
								APIVersion: "app/v1",
								Kind:       "Deployment",
								Name:       "testDeployment1",
							},
						},
					},
					Spec: v1.PodSpec{
						NodeName: "node1",
						Containers: []v1.Container{
							{
								Resources: v1.ResourceRequirements{
									Requests: map[v1.ResourceName]resource.Quantity{
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
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.TODO()
			controlCtx, err := katalyst_base.GenerateFakeGenericContext()
			assert.NoError(t, err)

			p, err := newTestController(ctx, controlCtx, tc.conf)
			assert.NoError(t, err)

			if tc.caches != nil {
				p.predictor = &predictor.FakePredictor{}
				p.provider = &prom.FakeProvider{}
			}

			if tc.caches != nil {
				p.workloadUsageCache = tc.caches
			}

			_, err = controlCtx.Client.KubeClient.CoreV1().Nodes().Create(ctx, tc.node, v12.CreateOptions{})
			assert.NoError(t, err)
			for _, pod := range tc.pods {
				_, err = controlCtx.Client.KubeClient.CoreV1().Pods(pod.Namespace).Create(ctx, pod, v12.CreateOptions{})
				assert.NoError(t, err)
			}
			for _, spd := range tc.spds {
				_, err = controlCtx.Client.InternalClient.WorkloadV1alpha1().ServiceProfileDescriptors(spd.Namespace).
					Create(ctx, spd, v12.CreateOptions{})
				assert.NoError(t, err)
			}

			controlCtx.StartInformer(ctx)

			synced := cache.WaitForCacheSync(context.TODO().Done(), p.syncedFunc...)
			assert.True(t, synced)

			p.reconcileNodes()

			time.Sleep(time.Second)
			if tc.success {
				node, err := p.nodeLister.Get(tc.node.Name)
				assert.NoError(t, err)
				_, ok := node.Annotations[consts.NodeAnnotationPredictCPUOvercommitRatioKey]
				assert.True(t, ok)
				_, ok = node.Annotations[consts.NodeAnnotationPredictMemoryOvercommitRatioKey]
				assert.True(t, ok)
			}
		})
	}
}

func TestPodToWorkloadName(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name         string
		pod          *v1.Pod
		labelNameKey string
		labelTypeKey string
		expectedName string
		expectedType string
	}{
		{
			name: "by label",
			pod: &v1.Pod{
				ObjectMeta: v12.ObjectMeta{
					Labels: map[string]string{
						"app":     "testApp",
						"appType": "Deployment",
					},
				},
			},
			labelNameKey: "app",
			labelTypeKey: "appType",
			expectedName: "testApp",
			expectedType: "Deployment",
		},
		{
			name: "by label miss",
			pod: &v1.Pod{
				ObjectMeta: v12.ObjectMeta{
					Labels: map[string]string{
						"app": "testApp",
					},
				},
			},
			labelNameKey: "testapp",
			labelTypeKey: "appType",
			expectedName: "",
			expectedType: "",
		},
		{
			name: "by label nil",
			pod: &v1.Pod{
				ObjectMeta: v12.ObjectMeta{},
			},
			labelNameKey: "app",
			labelTypeKey: "appType",
			expectedName: "",
			expectedType: "",
		},
		{
			name: "by owner statefulSet",
			pod: &v1.Pod{
				ObjectMeta: v12.ObjectMeta{
					OwnerReferences: []v12.OwnerReference{
						{
							Kind: "StatefulSet",
							Name: "testApp",
						},
					},
				},
			},
			labelNameKey: "",
			labelTypeKey: "appType",
			expectedName: "testApp",
			expectedType: "StatefulSet",
		},
		{
			name: "by owner rs",
			pod: &v1.Pod{
				ObjectMeta: v12.ObjectMeta{
					OwnerReferences: []v12.OwnerReference{
						{
							Kind: "ReplicaSet",
							Name: "test-app-xxxx",
						},
					},
				},
			},
			labelNameKey: "",
			labelTypeKey: "appType",
			expectedName: "test-app",
			expectedType: "Deployment",
		},
		{
			name: "by owner rs fail",
			pod: &v1.Pod{
				ObjectMeta: v12.ObjectMeta{
					OwnerReferences: []v12.OwnerReference{
						{
							Kind: "ReplicaSet",
							Name: "test",
						},
					},
				},
			},
			labelNameKey: "",
			labelTypeKey: "appType",
			expectedName: "",
			expectedType: "",
		},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p := &Prediction{
				conf: &controller.OvercommitConfig{},
			}
			p.conf.Prediction.TargetReferenceNameKey = tc.labelNameKey
			p.conf.Prediction.TargetReferenceTypeKey = tc.labelTypeKey

			name, kind, _ := p.podToWorkloadNameAndType(tc.pod)
			assert.Equal(t, tc.expectedName, name)
			assert.Equal(t, tc.expectedType, kind)
		})
	}
}

func TestNodePodIndexFunc(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name      string
		pod       *v1.Pod
		err       bool
		expectRes []string
	}{
		{
			name: "nil pod",
			pod:  nil,
			err:  true,
		},
		{
			name:      "without node name",
			pod:       &v1.Pod{},
			err:       false,
			expectRes: []string{},
		},
		{
			name: "with node name",
			pod: &v1.Pod{
				Spec: v1.PodSpec{
					NodeName: "testNode",
				},
			},
			err:       false,
			expectRes: []string{"testNode"},
		},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			res, err := nodePodIndexFunc(tc.pod)
			if tc.err {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tc.expectRes, res)
			}
		})
	}
}

func TestTimeSeriesByRequest(t *testing.T) {
	t.Parallel()

	generateExpectTimeSeries := func(value float64) *common.TimeSeries {
		res := common.EmptyTimeSeries()
		for i := 0; i < 24; i++ {
			res.Samples = append(res.Samples, common.Sample{
				Value: value,
			})
		}
		return res
	}

	for _, tc := range []struct {
		name         string
		resourceList v1.ResourceList
		scaleFactor  float64
		expectCPU    *common.TimeSeries
		expectMemory *common.TimeSeries
	}{
		{
			name: "test1",
			resourceList: map[v1.ResourceName]resource.Quantity{
				v1.ResourceCPU:    resource.MustParse("2"),
				v1.ResourceMemory: resource.MustParse("4Gi"),
			},
			scaleFactor:  1,
			expectCPU:    generateExpectTimeSeries(2000),
			expectMemory: generateExpectTimeSeries(4 * 1024 * 1024 * 1024),
		},
		{
			name: "test2",
			resourceList: map[v1.ResourceName]resource.Quantity{
				v1.ResourceCPU:    resource.MustParse("4"),
				v1.ResourceMemory: resource.MustParse("8Gi"),
			},
			scaleFactor:  0.5,
			expectCPU:    generateExpectTimeSeries(2000),
			expectMemory: generateExpectTimeSeries(4 * 1024 * 1024 * 1024),
		},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p := &Prediction{
				conf: &controller.OvercommitConfig{
					Prediction: controller.PredictionConfig{
						CPUScaleFactor:    tc.scaleFactor,
						MemoryScaleFactor: tc.scaleFactor,
					},
				},
			}

			cpuTs, memTs := p.timeSeriesByRequest(tc.resourceList)
			assert.Equal(t, tc.expectCPU.Samples, cpuTs.Samples)
			assert.Equal(t, tc.expectMemory.Samples, memTs.Samples)
		})
	}
}

func TestNewPredictionController(t *testing.T) {
	t.Parallel()
	controlCtx, err := katalyst_base.GenerateFakeGenericContext([]runtime.Object{},
		[]runtime.Object{}, []runtime.Object{
			&v13.Deployment{
				TypeMeta: v12.TypeMeta{
					Kind:       "Deployment",
					APIVersion: "apps/v1",
				},
			},
		})
	assert.NoError(t, err)

	conf := &controller.OvercommitConfig{
		Prediction: controller.PredictionConfig{
			PromConfig: &prometheus.PromConfig{},
		},
	}
	p, err := NewPredictionController(context.TODO(), controlCtx, conf)
	assert.NoError(t, err)
	assert.NotNil(t, p)
}

func TestPodNameByWorkload(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name         string
		workloadName string
		workloadType string
		expectRes    string
	}{
		{
			name:         "daemonset",
			workloadName: "test",
			workloadType: "DaemonSet",
			expectRes:    "^test-[a-z0-9]{5}$",
		},
		{
			name:         "statefulset",
			workloadName: "test",
			workloadType: "StatefulSet",
			expectRes:    "^test-[0-9]*$",
		},
		{
			name:         "others",
			workloadName: "test",
			workloadType: "unknown",
			expectRes:    "^test-.*",
		},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			res := podNameByWorkload(tc.workloadName, tc.workloadType)
			assert.Equal(t, tc.expectRes, res)
		})
	}
}

func TestResourceToOvercommitRatio(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name            string
		resourceName    string
		request         float64
		estimateUsage   float64
		nodeAllocatable float64
		expectRes       float64
	}{
		{
			name:      "zero request",
			request:   0,
			expectRes: 0,
		},
		{
			name:          "zero usage",
			request:       1,
			estimateUsage: 0,
			expectRes:     0,
		},
		{
			name:            "zero allocatable",
			request:         1,
			estimateUsage:   1,
			nodeAllocatable: 0,
			expectRes:       0,
		},
		{
			name:            "unknown resource",
			resourceName:    "",
			request:         1,
			estimateUsage:   1,
			nodeAllocatable: 1,
			expectRes:       0,
		},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p := &Prediction{}
			res := p.resourceToOvercommitRatio("", tc.resourceName, tc.request, tc.estimateUsage, tc.nodeAllocatable)
			assert.Equal(t, tc.expectRes, res)
		})
	}
}

func testAggMetrics(cpu, memory int64, len int) v1alpha1.AggPodMetrics {
	now := time.Now()
	rand.Seed(now.Unix())
	startTime := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()).Add(24 * time.Hour)

	res := v1alpha1.AggPodMetrics{
		Scope: spdPortraitScope,
		Items: []v1beta1.PodMetrics{},
	}

	for i := 0; i < len; i++ {
		res.Items = append(res.Items, v1beta1.PodMetrics{
			Timestamp: v12.Time{Time: startTime},
			Containers: []v1beta1.ContainerMetrics{
				{
					Name: spdPortraitLoadAwareMetricName,
					Usage: map[v1.ResourceName]resource.Quantity{
						metricNameCpuUtilizationUsageSecondsMax: *resource.NewMilliQuantity(rand.Int63nRange(0, cpu), resource.DecimalSI),
						metricNameMemoryUtilizationMax:          *resource.NewQuantity(rand.Int63nRange(0, memory), resource.BinarySI),
					},
				},
			},
		})
		startTime = startTime.Add(time.Hour)
	}

	return res
}

func testWorkloadCaches(cpu, memory int64, len int) map[string]*common.TimeSeries {
	now := time.Now()
	rand.Seed(now.Unix())
	startTime := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()).Add(24 * time.Hour)

	res := map[string]*common.TimeSeries{
		"cpu": {
			Samples: make([]common.Sample, len),
		},
		"memory": {
			Samples: make([]common.Sample, len),
		},
	}
	for i := 0; i < len; i++ {
		res["cpu"].Samples[i] = common.Sample{
			Timestamp: startTime.Unix(),
			Value:     float64(rand.Int63nRange(0, cpu)),
		}
		res["memory"].Samples[i] = common.Sample{
			Timestamp: startTime.Unix(),
			Value:     float64(rand.Int63nRange(0, memory)),
		}
		startTime = startTime.Add(time.Hour)
	}
	return res
}

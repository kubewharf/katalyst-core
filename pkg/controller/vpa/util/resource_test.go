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

package util

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic/dynamicinformer"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"
	"k8s.io/utils/pointer"

	apis "github.com/kubewharf/katalyst-api/pkg/apis/autoscaling/v1alpha1"
	katalystutil "github.com/kubewharf/katalyst-core/pkg/util"
	"github.com/kubewharf/katalyst-core/pkg/util/native"
)

var (
	stsGVK = schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "Statefulset"}
	stsGVR = schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "statefulsets"}
)

func TestGetVPAResourceStatusWithCurrent(t *testing.T) {
	t.Parallel()

	// test with no indexers
	klog.Infof("================== test with no indexers ==================")
	testGetVPAResourceStatusWithCurrentAndIndexer(t, nil, []string{})

	// test with existed indexers
	indexerKeys := []string{"workload"}
	podIndexers := cache.Indexers{}
	for _, key := range indexerKeys {
		indexer := native.PodLabelIndexer(key)
		podIndexers[key] = indexer.IndexFunc
	}
	klog.Infof("================== test with existed indexers ==================")
	testGetVPAResourceStatusWithCurrentAndIndexer(t, podIndexers, indexerKeys)

	// test with none-existed indexers
	indexerKeys = []string{"none-exist"}
	podIndexers = cache.Indexers{}
	for _, key := range indexerKeys {
		indexer := native.PodLabelIndexer(key)
		podIndexers[key] = indexer.IndexFunc
	}
	klog.Infof("================== test with none-existed indexers ==================")
	testGetVPAResourceStatusWithCurrentAndIndexer(t, podIndexers, indexerKeys)
}

func testGetVPAResourceStatusWithCurrentAndIndexer(t *testing.T, podIndexers cache.Indexers, podIndexerKeys []string) {
	container1 := &v1.Container{
		Name: "c1",
		Resources: v1.ResourceRequirements{
			Limits: map[v1.ResourceName]resource.Quantity{
				v1.ResourceMemory: resource.MustParse("4Gi"),
				v1.ResourceCPU:    resource.MustParse("4"),
			},
			Requests: map[v1.ResourceName]resource.Quantity{
				v1.ResourceMemory: resource.MustParse("2Gi"),
				v1.ResourceCPU:    resource.MustParse("2"),
			},
		},
	}
	container2 := &v1.Container{
		Name: "c1",
		Resources: v1.ResourceRequirements{
			Limits: map[v1.ResourceName]resource.Quantity{
				v1.ResourceMemory: resource.MustParse("3Gi"),
				v1.ResourceCPU:    resource.MustParse("3"),
			},
			Requests: map[v1.ResourceName]resource.Quantity{
				v1.ResourceMemory: resource.MustParse("2Gi"),
				v1.ResourceCPU:    resource.MustParse("2"),
			},
		},
	}
	pod1 := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "default",
			Name:      "pod1",
			Labels: map[string]string{
				"workload": "sts",
			},
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: stsGVK.GroupVersion().String(),
					Kind:       stsGVK.Kind,
					Name:       "sts1",
				},
			},
			UID: types.UID("pod1-uid"),
		},
		Spec: v1.PodSpec{
			Containers: []v1.Container{*container1},
		},
	}
	pod2 := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "default",
			Name:      "pod2",
			Labels: map[string]string{
				"workload": "sts",
			},
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: stsGVK.GroupVersion().String(),
					Kind:       stsGVK.Kind,
					Name:       "sts1",
				},
			},
			UID: types.UID("pod2-uid"),
		},
		Spec: v1.PodSpec{
			Containers: []v1.Container{*container2},
		},
	}
	vpa1 := &apis.KatalystVerticalPodAutoscaler{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "default",
			Name:      "vpa1",
		},
		Spec: apis.KatalystVerticalPodAutoscalerSpec{
			TargetRef: apis.CrossVersionObjectReference{
				Kind:       stsGVK.Kind,
				Name:       "sts1",
				APIVersion: stsGVK.GroupVersion().String(),
			},
		},
		Status: apis.KatalystVerticalPodAutoscalerStatus{
			ContainerResources: []apis.ContainerResources{
				{
					ContainerName: pointer.String("c1"),
					Requests: &apis.ContainerResourceList{
						Target: map[v1.ResourceName]resource.Quantity{
							v1.ResourceMemory: resource.MustParse("1Gi"),
							v1.ResourceCPU:    resource.MustParse("1"),
						},
						UncappedTarget: map[v1.ResourceName]resource.Quantity{
							v1.ResourceMemory: resource.MustParse("1Gi"),
							v1.ResourceCPU:    resource.MustParse("1"),
						},
					},
				},
			},
		},
	}
	vpa2 := &apis.KatalystVerticalPodAutoscaler{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "default",
			Name:      "vpa1",
		},
		Spec: apis.KatalystVerticalPodAutoscalerSpec{
			TargetRef: apis.CrossVersionObjectReference{
				Kind:       stsGVK.Kind,
				Name:       "sts1",
				APIVersion: stsGVK.GroupVersion().String(),
			},
		},
		Status: apis.KatalystVerticalPodAutoscalerStatus{
			ContainerResources: []apis.ContainerResources{
				{
					ContainerName: pointer.String("c1"),
					Requests: &apis.ContainerResourceList{
						Target: map[v1.ResourceName]resource.Quantity{
							v1.ResourceMemory: resource.MustParse("3Gi"),
							v1.ResourceCPU:    resource.MustParse("3"),
						},
						UncappedTarget: map[v1.ResourceName]resource.Quantity{
							v1.ResourceMemory: resource.MustParse("3Gi"),
							v1.ResourceCPU:    resource.MustParse("3"),
						},
					},
				},
			},
		},
	}
	vpa3 := &apis.KatalystVerticalPodAutoscaler{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "default",
			Name:      "vpa1",
		},
		Spec: apis.KatalystVerticalPodAutoscalerSpec{
			TargetRef: apis.CrossVersionObjectReference{
				Kind:       stsGVK.Kind,
				Name:       "sts1",
				APIVersion: stsGVK.GroupVersion().String(),
			},
		},
		Status: apis.KatalystVerticalPodAutoscalerStatus{
			PodResources: []apis.PodResources{
				{
					PodName: pointer.String("pod1"),
					ContainerResources: []apis.ContainerResources{
						{
							ContainerName: pointer.String("c1"),
							Requests: &apis.ContainerResourceList{
								Target: map[v1.ResourceName]resource.Quantity{
									v1.ResourceMemory: resource.MustParse("3Gi"),
									v1.ResourceCPU:    resource.MustParse("3"),
								},
								UncappedTarget: map[v1.ResourceName]resource.Quantity{
									v1.ResourceMemory: resource.MustParse("3Gi"),
									v1.ResourceCPU:    resource.MustParse("3"),
								},
							},
							Limits: &apis.ContainerResourceList{
								Target: map[v1.ResourceName]resource.Quantity{
									v1.ResourceMemory: resource.MustParse("3Gi"),
									v1.ResourceCPU:    resource.MustParse("3"),
								},
								UncappedTarget: map[v1.ResourceName]resource.Quantity{
									v1.ResourceMemory: resource.MustParse("3Gi"),
									v1.ResourceCPU:    resource.MustParse("3"),
								},
							},
						},
					},
				},
			},
		},
	}

	vpa4 := &apis.KatalystVerticalPodAutoscaler{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "default",
			Name:      "vpa1",
		},
		Spec: apis.KatalystVerticalPodAutoscalerSpec{
			TargetRef: apis.CrossVersionObjectReference{
				Kind:       stsGVK.Kind,
				Name:       "sts1",
				APIVersion: stsGVK.GroupVersion().String(),
			},
		},
		Status: apis.KatalystVerticalPodAutoscalerStatus{
			ContainerResources: []apis.ContainerResources{
				{
					ContainerName: pointer.String("c1"),
					Limits: &apis.ContainerResourceList{
						Target:         v1.ResourceList{},
						UncappedTarget: v1.ResourceList{},
						LowerBound: v1.ResourceList{
							v1.ResourceMemory: resource.MustParse("400Mi"),
						},
						UpperBound: v1.ResourceList{
							v1.ResourceMemory: resource.MustParse("6000Mi"),
						},
					},
				},
			},
		},
	}

	sts1 := &appsv1.StatefulSet{
		TypeMeta: metav1.TypeMeta{
			Kind:       "StatefulSet",
			APIVersion: "apps/v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "default",
			Name:      "sts1",
		},
		Spec: appsv1.StatefulSetSpec{
			Replicas: pointer.Int32(1),
			Template: v1.PodTemplateSpec{
				Spec: v1.PodSpec{
					Containers: []v1.Container{*container1},
				},
			},
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"workload": "sts",
				},
			},
		},
		Status: appsv1.StatefulSetStatus{},
	}
	for _, tc := range []struct {
		name                  string
		vpa                   *apis.KatalystVerticalPodAutoscaler
		pods                  []*v1.Pod
		object                runtime.Object
		gvk                   schema.GroupVersionKind
		vpaPodResources       []apis.PodResources
		vpaContainerResources []apis.ContainerResources
	}{
		{
			name:            "shrink resource: vpa container",
			vpa:             vpa1,
			pods:            []*v1.Pod{pod1},
			object:          sts1,
			gvk:             stsGVK,
			vpaPodResources: []apis.PodResources{},
			vpaContainerResources: []apis.ContainerResources{
				{
					ContainerName: pointer.String("c1"),
					Requests: &apis.ContainerResourceList{
						Current: v1.ResourceList{
							v1.ResourceMemory: resource.MustParse("2Gi"),
							v1.ResourceCPU:    resource.MustParse("2"),
						},
						Target: v1.ResourceList{
							v1.ResourceMemory: resource.MustParse("1Gi"),
							v1.ResourceCPU:    resource.MustParse("1"),
						},
						UncappedTarget: v1.ResourceList{
							v1.ResourceMemory: resource.MustParse("1Gi"),
							v1.ResourceCPU:    resource.MustParse("1"),
						},
					},
				},
			},
		},
		{
			name:            "expand resource: vpa container",
			vpa:             vpa2,
			pods:            []*v1.Pod{pod1},
			object:          sts1,
			gvk:             stsGVK,
			vpaPodResources: []apis.PodResources{},
			vpaContainerResources: []apis.ContainerResources{
				{
					ContainerName: pointer.String("c1"),
					Requests: &apis.ContainerResourceList{
						Current: v1.ResourceList{
							v1.ResourceMemory: resource.MustParse("2Gi"),
							v1.ResourceCPU:    resource.MustParse("2"),
						},
						Target: v1.ResourceList{
							v1.ResourceMemory: resource.MustParse("3Gi"),
							v1.ResourceCPU:    resource.MustParse("3"),
						},
						UncappedTarget: v1.ResourceList{
							v1.ResourceMemory: resource.MustParse("3Gi"),
							v1.ResourceCPU:    resource.MustParse("3"),
						},
					},
				},
			},
		},
		{
			name:   "expand resource: two pod",
			vpa:    vpa3,
			pods:   []*v1.Pod{pod1, pod2},
			object: sts1,
			gvk:    stsGVK,
			vpaPodResources: []apis.PodResources{
				{
					PodName: pointer.String("pod1"),
					ContainerResources: []apis.ContainerResources{
						{
							ContainerName: pointer.String("c1"),
							Requests: &apis.ContainerResourceList{
								Current: v1.ResourceList{
									v1.ResourceMemory: resource.MustParse("2Gi"),
									v1.ResourceCPU:    resource.MustParse("2"),
								},
								Target: v1.ResourceList{
									v1.ResourceMemory: resource.MustParse("3Gi"),
									v1.ResourceCPU:    resource.MustParse("3"),
								},
								UncappedTarget: v1.ResourceList{
									v1.ResourceMemory: resource.MustParse("3Gi"),
									v1.ResourceCPU:    resource.MustParse("3"),
								},
							},
							Limits: &apis.ContainerResourceList{
								Current: v1.ResourceList{
									v1.ResourceMemory: resource.MustParse("4Gi"),
									v1.ResourceCPU:    resource.MustParse("4"),
								},
								Target: v1.ResourceList{
									v1.ResourceMemory: resource.MustParse("3Gi"),
									v1.ResourceCPU:    resource.MustParse("3"),
								},
								UncappedTarget: v1.ResourceList{
									v1.ResourceMemory: resource.MustParse("3Gi"),
									v1.ResourceCPU:    resource.MustParse("3"),
								},
							},
						},
					},
				},
			},
			vpaContainerResources: []apis.ContainerResources{},
		},
		{
			name:            "expand resource: without limit",
			vpa:             vpa4,
			pods:            []*v1.Pod{pod1},
			object:          sts1,
			gvk:             stsGVK,
			vpaPodResources: []apis.PodResources{},
			vpaContainerResources: []apis.ContainerResources{
				{
					ContainerName: pointer.String("c1"),
					Limits: &apis.ContainerResourceList{
						Current:        v1.ResourceList{},
						Target:         v1.ResourceList{},
						UncappedTarget: v1.ResourceList{},
						LowerBound: v1.ResourceList{
							v1.ResourceMemory: resource.MustParse("400Mi"),
						},
						UpperBound: v1.ResourceList{
							v1.ResourceMemory: resource.MustParse("6000Mi"),
						},
					},
				},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fakeDynamicClient := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme())
			dynamicFactory := dynamicinformer.NewDynamicSharedInformerFactory(fakeDynamicClient, 0)
			stsInformer := dynamicFactory.ForResource(stsGVR)
			workloadInformers := map[schema.GroupVersionKind]informers.GenericInformer{
				stsGVK: stsInformer,
			}

			KubeClient := fake.NewSimpleClientset()
			KubeInformerFactory := informers.NewSharedInformerFactory(KubeClient, 0)

			u, err := native.ToUnstructured(tc.object)
			assert.NoError(t, err)
			err = workloadInformers[tc.gvk].Informer().GetStore().Add(u)
			assert.NoError(t, err)

			podInformer := KubeInformerFactory.Core().V1().Pods()
			err = podInformer.Informer().AddIndexers(podIndexers)
			assert.NoError(t, err)

			for _, pod := range tc.pods {
				err = podInformer.Informer().GetStore().Add(pod)
				assert.NoError(t, err)
			}

			pods, err := katalystutil.GetPodListForVPA(tc.vpa, podInformer.Informer().GetIndexer(), podIndexerKeys, workloadInformers[tc.gvk].Lister(), podInformer.Lister())
			assert.NoError(t, err)

			vpaPodResources, vpaContainerResources, err := GetVPAResourceStatusWithCurrent(tc.vpa, pods)
			assert.NoError(t, err)
			assert.Equal(t, tc.vpaPodResources, vpaPodResources)
			assert.Equal(t, tc.vpaContainerResources, vpaContainerResources)
		})
	}
}

func TestGetVPAResourceStatusWithRecommendation(t *testing.T) {
	t.Parallel()

	type args struct {
		vpa                   *apis.KatalystVerticalPodAutoscaler
		recPodResources       []apis.RecommendedPodResources
		recContainerResources []apis.RecommendedContainerResources
	}
	tests := []struct {
		name    string
		args    args
		want    []apis.PodResources
		want1   []apis.ContainerResources
		wantErr assert.ErrorAssertionFunc
	}{
		{
			name: "without recommend limit but with memory bound",
			args: args{
				vpa: &apis.KatalystVerticalPodAutoscaler{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "vpa1",
					},
					Spec: apis.KatalystVerticalPodAutoscalerSpec{
						TargetRef: apis.CrossVersionObjectReference{
							Kind:       stsGVK.Kind,
							Name:       "sts1",
							APIVersion: stsGVK.GroupVersion().String(),
						},
						ResourcePolicy: apis.PodResourcePolicy{
							ContainerPolicies: []apis.ContainerResourcePolicy{
								{
									ContainerName: pointer.String("nginx"),
									ControlledResources: []v1.ResourceName{
										v1.ResourceCPU,
										v1.ResourceMemory,
									},
									ControlledValues: apis.ContainerControlledValuesRequestsAndLimits,
									MaxAllowed: v1.ResourceList{
										v1.ResourceMemory: resource.MustParse("6000Mi"),
									},
									MinAllowed: v1.ResourceList{
										v1.ResourceMemory: resource.MustParse("400Mi"),
									},
									ResourceResizePolicy: apis.ResourceResizePolicyNone,
								},
							},
						},
						UpdatePolicy: apis.PodUpdatePolicy{
							PodApplyStrategy:    apis.PodApplyStrategyStrategyGroup,
							PodMatchingStrategy: apis.PodMatchingStrategyAll,
							PodUpdatingStrategy: apis.PodUpdatingStrategyInplace,
						},
					},
					Status: apis.KatalystVerticalPodAutoscalerStatus{
						ContainerResources: []apis.ContainerResources{
							{
								ContainerName: pointer.String("nginx"),
								Limits: &apis.ContainerResourceList{
									Current: v1.ResourceList{
										v1.ResourceCPU:    resource.MustParse("4"),
										v1.ResourceMemory: resource.MustParse("6000Mi"),
									},
								},
							},
						},
					},
				},
				recContainerResources: []apis.RecommendedContainerResources{
					{
						ContainerName: pointer.String("nginx"),
						Limits: &apis.RecommendedRequestResources{
							Resources: v1.ResourceList{},
						},
					},
				},
			},
			want: []apis.PodResources{},
			want1: []apis.ContainerResources{
				{
					ContainerName: pointer.String("nginx"),
					Limits: &apis.ContainerResourceList{
						Current: v1.ResourceList{
							v1.ResourceCPU:    resource.MustParse("4"),
							v1.ResourceMemory: resource.MustParse("6000Mi"),
						},
						Target:         v1.ResourceList{},
						UncappedTarget: v1.ResourceList{},
						LowerBound: v1.ResourceList{
							v1.ResourceMemory: resource.MustParse("400Mi"),
						},
						UpperBound: v1.ResourceList{
							v1.ResourceMemory: resource.MustParse("6000Mi"),
						},
					},
				},
			},
			wantErr: func(t assert.TestingT, err error, i ...interface{}) bool {
				return true
			},
		},
		{
			name: "without either recommend limit or bound",
			args: args{
				vpa: &apis.KatalystVerticalPodAutoscaler{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "vpa1",
					},
					Spec: apis.KatalystVerticalPodAutoscalerSpec{
						TargetRef: apis.CrossVersionObjectReference{
							Kind:       stsGVK.Kind,
							Name:       "sts1",
							APIVersion: stsGVK.GroupVersion().String(),
						},
						ResourcePolicy: apis.PodResourcePolicy{
							ContainerPolicies: []apis.ContainerResourcePolicy{
								{
									ContainerName: pointer.String("nginx"),
									ControlledResources: []v1.ResourceName{
										v1.ResourceCPU,
										v1.ResourceMemory,
									},
									ControlledValues:     apis.ContainerControlledValuesRequestsAndLimits,
									ResourceResizePolicy: apis.ResourceResizePolicyNone,
								},
							},
						},
						UpdatePolicy: apis.PodUpdatePolicy{
							PodApplyStrategy:    apis.PodApplyStrategyStrategyGroup,
							PodMatchingStrategy: apis.PodMatchingStrategyAll,
							PodUpdatingStrategy: apis.PodUpdatingStrategyInplace,
						},
					},
				},
				recContainerResources: []apis.RecommendedContainerResources{
					{
						ContainerName: pointer.String("nginx"),
						Limits: &apis.RecommendedRequestResources{
							Resources: v1.ResourceList{},
						},
					},
				},
			},
			want: []apis.PodResources{},
			want1: []apis.ContainerResources{
				{
					ContainerName: pointer.String("nginx"),
					Limits: &apis.ContainerResourceList{
						Target:         v1.ResourceList{},
						UncappedTarget: v1.ResourceList{},
						LowerBound:     nil,
						UpperBound:     nil,
					},
				},
			},
			wantErr: func(t assert.TestingT, err error, i ...interface{}) bool {
				return true
			},
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, got1, err := GetVPAResourceStatusWithRecommendation(tt.args.vpa, tt.args.recPodResources, tt.args.recContainerResources)
			if !tt.wantErr(t, err, fmt.Sprintf("GetVPAResourceStatusWithRecommendation(%v, %v, %v)", tt.args.vpa, tt.args.recPodResources, tt.args.recContainerResources)) {
				return
			}
			assert.Equalf(t, tt.want, got, "GetVPAResourceStatusWithRecommendation(%v, %v, %v)", tt.args.vpa, tt.args.recPodResources, tt.args.recContainerResources)
			assert.Equalf(t, tt.want1, got1, "GetVPAResourceStatusWithRecommendation(%v, %v, %v)", tt.args.vpa, tt.args.recPodResources, tt.args.recContainerResources)
		})
	}
}

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
	"context"
	"testing"

	"github.com/alecthomas/units"
	"github.com/stretchr/testify/assert"
	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/dynamic/dynamicinformer"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/tools/cache"
	"k8s.io/utils/pointer"

	apis "github.com/kubewharf/katalyst-api/pkg/apis/autoscaling/v1alpha1"
	workload "github.com/kubewharf/katalyst-api/pkg/apis/workload/v1alpha1"
	externalfake "github.com/kubewharf/katalyst-api/pkg/client/clientset/versioned/fake"
	"github.com/kubewharf/katalyst-api/pkg/client/informers/externalversions"
	apiconsts "github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/util/native"
)

var (
	deployGVK = schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "Deployment"}
	deployGVR = schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}
	rsGVK     = schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "ReplicaSet"}
	rsGVR     = schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "replicasets"}
)

func TestFindSpdByVpa(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		vpa  *apis.KatalystVerticalPodAutoscaler
		wkl  []runtime.Object
		spd  []*workload.ServiceProfileDescriptor
		want struct {
			spd     *workload.ServiceProfileDescriptor
			wantErr bool
		}
	}{
		{
			name: "vpa match one spd",
			vpa: &apis.KatalystVerticalPodAutoscaler{
				ObjectMeta: metav1.ObjectMeta{Name: "vpa1", Namespace: "default"},
				Spec: apis.KatalystVerticalPodAutoscalerSpec{
					TargetRef: apis.CrossVersionObjectReference{
						Kind:       "Deployment",
						APIVersion: "apps/v1",
						Name:       "deployment1",
					},
				},
			},
			wkl: []runtime.Object{
				&appsv1.Deployment{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "deployment1",
						Namespace: "default",
						Annotations: map[string]string{
							apiconsts.WorkloadAnnotationSPDEnableKey: apiconsts.WorkloadAnnotationSPDEnabled,
						},
					},
				},
			},
			spd: []*workload.ServiceProfileDescriptor{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "spd1", Namespace: "default"},
					Spec: workload.ServiceProfileDescriptorSpec{
						TargetRef: apis.CrossVersionObjectReference{
							Kind:       "Deployment",
							APIVersion: "apps/v1",
							Name:       "deployment1",
						},
					},
				},
			},
			want: struct {
				spd     *workload.ServiceProfileDescriptor
				wantErr bool
			}{
				spd: &workload.ServiceProfileDescriptor{
					ObjectMeta: metav1.ObjectMeta{Name: "spd1", Namespace: "default"},
					Spec: workload.ServiceProfileDescriptorSpec{
						TargetRef: apis.CrossVersionObjectReference{
							Kind:       "Deployment",
							APIVersion: "apps/v1",
							Name:       "deployment1",
						},
					},
				},
				wantErr: false,
			},
		},
		{
			name: "vpa match no spd",
			vpa: &apis.KatalystVerticalPodAutoscaler{
				ObjectMeta: metav1.ObjectMeta{Name: "vpa1", Namespace: "default"},
				Spec: apis.KatalystVerticalPodAutoscalerSpec{
					TargetRef: apis.CrossVersionObjectReference{
						Kind:       "Deployment",
						APIVersion: "apps/v1",
						Name:       "deployment1",
					},
				},
			},
			wkl: []runtime.Object{
				&appsv1.Deployment{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "deployment1",
						Namespace: "default",
						Annotations: map[string]string{
							apiconsts.WorkloadAnnotationSPDEnableKey: apiconsts.WorkloadAnnotationSPDEnabled,
						},
					},
				},
			},
			spd: []*workload.ServiceProfileDescriptor{},
			want: struct {
				spd     *workload.ServiceProfileDescriptor
				wantErr bool
			}{
				spd:     nil,
				wantErr: true,
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spds := make([]runtime.Object, 0, len(tc.spd))
			for _, spd := range tc.spd {
				spds = append(spds, spd)
			}
			client := externalfake.NewSimpleClientset(spds...)
			factory := externalversions.NewSharedInformerFactory(client, 0)
			scheme := runtime.NewScheme()
			utilruntime.Must(v1.AddToScheme(scheme))
			utilruntime.Must(appsv1.AddToScheme(scheme))
			dynamicClient := dynamicfake.NewSimpleDynamicClient(scheme, tc.wkl...)
			dynamicInformerFactory := dynamicinformer.NewDynamicSharedInformerFactory(dynamicClient, 0)
			gvr, _ := meta.UnsafeGuessKindToResource(schema.FromAPIVersionAndKind(tc.vpa.Spec.TargetRef.APIVersion, tc.vpa.Spec.TargetRef.Kind))
			workloadLister := dynamicInformerFactory.ForResource(gvr).Lister()
			spdInformer := factory.Workload().V1alpha1().ServiceProfileDescriptors()

			err := spdInformer.Informer().AddIndexers(cache.Indexers{
				consts.TargetReferenceIndex: SPDTargetReferenceIndex,
			})
			assert.NoError(t, err)

			ctx := context.Background()
			factory.Start(ctx.Done())
			dynamicInformerFactory.Start(ctx.Done())
			cache.WaitForCacheSync(ctx.Done(), dynamicInformerFactory.ForResource(gvr).Informer().HasSynced, spdInformer.Informer().HasSynced)

			spd, err := GetSPDForVPA(tc.vpa, spdInformer.Informer().GetIndexer(), workloadLister, spdInformer.Lister())
			if tc.want.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
			assert.Equal(t, tc.want.spd, spd)
		})
	}
}

func TestGetVPAForPod(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	utilruntime.Must(v1.AddToScheme(scheme))
	utilruntime.Must(appsv1.AddToScheme(scheme))
	fakeDynamicClient := dynamicfake.NewSimpleDynamicClient(scheme)
	dynamicFactory := dynamicinformer.NewDynamicSharedInformerFactory(fakeDynamicClient, 0)
	dpInformer := dynamicFactory.ForResource(schema.GroupVersionResource{
		Group:    "apps",
		Version:  "v1",
		Resource: "deployments",
	})
	rsInformer := dynamicFactory.ForResource(schema.GroupVersionResource{
		Group:    "apps",
		Version:  "v1",
		Resource: "replicasets",
	})

	workloadInformers := map[schema.GroupVersionKind]cache.GenericLister{
		{Group: "apps", Version: "v1", Kind: "Deployment"}: dpInformer.Lister(),
		{Group: "apps", Version: "v1", Kind: "ReplicaSet"}: rsInformer.Lister(),
	}

	dp := &appsv1.Deployment{
		TypeMeta: metav1.TypeMeta{
			Kind:       "Deployment",
			APIVersion: "apps/v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "dp1",
			Namespace: "default",
			Annotations: map[string]string{
				apiconsts.WorkloadAnnotationVPAEnabledKey: apiconsts.WorkloadAnnotationVPAEnabled,
				apiconsts.WorkloadAnnotationSPDEnableKey:  apiconsts.WorkloadAnnotationSPDEnabled,
			},
		},
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"name": "dp1",
				},
			},
		},
	}

	u, err := native.ToUnstructured(dp)
	assert.NoError(t, err)
	_ = dpInformer.Informer().GetStore().Add(u)

	rs := &appsv1.ReplicaSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "rs1",
			Namespace: "default",
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: "apps/v1",
					Kind:       "Deployment",
					Name:       "dp1",
				},
			},
		},
	}

	u, err = native.ToUnstructured(rs)
	assert.NoError(t, err)
	_ = rsInformer.Informer().GetStore().Add(u)

	pod1 := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "pod1",
			Namespace: "default",
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: "apps/v1",
					Kind:       "ReplicaSet",
					Name:       "rs1",
				},
			},
			Labels: map[string]string{
				"name": "dp1",
			},
		},
	}

	internalClient := externalfake.NewSimpleClientset()
	internalFactory := externalversions.NewSharedInformerFactory(internalClient, 0)
	vpaInformer := internalFactory.Autoscaling().V1alpha1().KatalystVerticalPodAutoscalers()
	_ = vpaInformer.Informer().GetIndexer().AddIndexers(cache.Indexers{
		consts.TargetReferenceIndex: VPATargetReferenceIndex,
	})
	vpa := &apis.KatalystVerticalPodAutoscaler{
		ObjectMeta: metav1.ObjectMeta{Name: "vpa1", Namespace: "default"},
		Spec: apis.KatalystVerticalPodAutoscalerSpec{
			TargetRef: apis.CrossVersionObjectReference{
				Kind:       "Deployment",
				APIVersion: "apps/v1",
				Name:       "dp1",
			},
		},
	}
	_ = vpaInformer.Informer().GetStore().Add(vpa)

	v, err := GetVPAForPod(pod1, vpaInformer.Informer().GetIndexer(), workloadInformers, vpaInformer.Lister())
	assert.NoError(t, err)
	assert.Equal(t, vpa, v)

	pod2 := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "pod1",
			Namespace: "default",
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: "apps/v1",
					Kind:       "ReplicaSet",
					Name:       "rs2",
				},
			},
		},
	}
	v, err = GetVPAForPod(pod2, vpaInformer.Informer().GetIndexer(), workloadInformers, vpaInformer.Lister())
	assert.Nil(t, v)
	assert.Error(t, err)
}

func TestGetWorkloadByVPA(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name   string
		vpa    *apis.KatalystVerticalPodAutoscaler
		object runtime.Object
		gvk    schema.GroupVersionKind
	}{
		{
			name: "vpa to rs",
			vpa: &apis.KatalystVerticalPodAutoscaler{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "vpa1",
					Namespace: "default",
				},
				Spec: apis.KatalystVerticalPodAutoscalerSpec{
					TargetRef: apis.CrossVersionObjectReference{
						Kind:       rsGVK.Kind,
						Name:       "rs1",
						APIVersion: rsGVK.GroupVersion().String(),
					},
				},
			},
			object: &appsv1.StatefulSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "rs1",
					Namespace: "default",
				},
			},
			gvk: rsGVK,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fakeDynamicClient := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme())
			dynamicFactory := dynamicinformer.NewDynamicSharedInformerFactory(fakeDynamicClient, 0)
			deployInformer := dynamicFactory.ForResource(deployGVR)
			rsInformer := dynamicFactory.ForResource(rsGVR)
			workloadInformers := map[schema.GroupVersionKind]informers.GenericInformer{
				deployGVK: deployInformer,
				rsGVK:     rsInformer,
			}
			u, err := native.ToUnstructured(tc.object)
			assert.NoError(t, err)
			err = workloadInformers[tc.gvk].Informer().GetStore().Add(u)
			assert.NoError(t, err)

			object, err := GetWorkloadForVPA(tc.vpa, workloadInformers[schema.FromAPIVersionAndKind(tc.vpa.Spec.TargetRef.APIVersion,
				tc.vpa.Spec.TargetRef.Kind)].Lister())
			assert.NoError(t, err)
			assert.Equal(t, u, object)
		})
	}
}

func TestCheckVPARecommendationMatchVPA(t *testing.T) {
	t.Parallel()

	vpa1 := &apis.KatalystVerticalPodAutoscaler{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "vpa1",
			Namespace: "default",
			UID:       "vpauid1",
		},
	}
	vparec1 := &apis.VerticalPodAutoscalerRecommendation{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "vparec1",
			Namespace: "default",
			OwnerReferences: []metav1.OwnerReference{
				{
					Name: "vpa1",
					UID:  "vpauid1",
				},
			},
		},
	}
	vparec2 := &apis.VerticalPodAutoscalerRecommendation{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "vparec2",
			Namespace: "default",
			OwnerReferences: []metav1.OwnerReference{
				{
					Name: "vpa1",
					UID:  "vpauidbad",
				},
			},
		},
	}
	for _, tc := range []struct {
		name   string
		vparec *apis.VerticalPodAutoscalerRecommendation
		vpa    *apis.KatalystVerticalPodAutoscaler
		want   bool
	}{
		{
			name:   "matched",
			vpa:    vpa1,
			vparec: vparec1,
			want:   true,
		},
		{
			name:   "mismatch",
			vpa:    vpa1,
			vparec: vparec2,
			want:   false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			match := CheckVPARecommendationMatchVPA(tc.vparec, tc.vpa)
			assert.Equal(t, tc.want, match)
		})
	}
}

func TestIsVPAStatusLegal(t *testing.T) {
	t.Parallel()

	vpa1 := &apis.KatalystVerticalPodAutoscaler{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "vpa1",
			Namespace: "default",
			UID:       "vpauid1",
		},
		Status: apis.KatalystVerticalPodAutoscalerStatus{
			PodResources: nil,
			ContainerResources: []apis.ContainerResources{
				{
					ContainerName: pointer.String("c1"),
					Requests: &apis.ContainerResourceList{
						Target: map[v1.ResourceName]resource.Quantity{
							v1.ResourceCPU:    *resource.NewQuantity(2, resource.DecimalSI),
							v1.ResourceMemory: *resource.NewQuantity(2*int64(units.GiB), resource.BinarySI),
						},
					},
					Limits: &apis.ContainerResourceList{
						Target: map[v1.ResourceName]resource.Quantity{
							v1.ResourceCPU:    *resource.NewQuantity(4, resource.DecimalSI),
							v1.ResourceMemory: *resource.NewQuantity(4*int64(units.GiB), resource.BinarySI),
						},
					},
				},
			},
		},
	}
	pod1 := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "pod1",
			Namespace: "default",
		},
		Spec: v1.PodSpec{
			Containers: []v1.Container{
				{
					Name: "c1",
					Resources: v1.ResourceRequirements{
						Limits: map[v1.ResourceName]resource.Quantity{
							v1.ResourceCPU:    *resource.NewQuantity(2, resource.DecimalSI),
							v1.ResourceMemory: *resource.NewQuantity(2*int64(units.GiB), resource.BinarySI),
						},
						Requests: map[v1.ResourceName]resource.Quantity{
							v1.ResourceCPU:    *resource.NewQuantity(1, resource.DecimalSI),
							v1.ResourceMemory: *resource.NewQuantity(1*int64(units.GiB), resource.BinarySI),
						},
					},
				},
			},
		},
	}
	pod2 := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "pod2",
			Namespace: "default",
		},
		Spec: v1.PodSpec{
			Containers: []v1.Container{
				{
					Name: "c1",
					Resources: v1.ResourceRequirements{
						Limits: map[v1.ResourceName]resource.Quantity{
							v1.ResourceCPU:    *resource.NewQuantity(4, resource.DecimalSI),
							v1.ResourceMemory: *resource.NewQuantity(4*int64(units.GiB), resource.BinarySI),
						},
						Requests: map[v1.ResourceName]resource.Quantity{
							v1.ResourceCPU:    *resource.NewQuantity(1, resource.DecimalSI),
							v1.ResourceMemory: *resource.NewQuantity(1*int64(units.GiB), resource.BinarySI),
						},
					},
				},
			},
		},
		Status: v1.PodStatus{
			QOSClass: v1.PodQOSBurstable,
		},
	}
	pod3 := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "pod3",
			Namespace: "default",
		},
		Spec: v1.PodSpec{
			Containers: []v1.Container{
				{
					Name: "c1",
					Resources: v1.ResourceRequirements{
						Limits: map[v1.ResourceName]resource.Quantity{
							v1.ResourceCPU:    *resource.NewQuantity(4, resource.DecimalSI),
							v1.ResourceMemory: *resource.NewQuantity(4*int64(units.GiB), resource.BinarySI),
						},
						Requests: map[v1.ResourceName]resource.Quantity{
							v1.ResourceCPU:    *resource.NewQuantity(3, resource.DecimalSI),
							v1.ResourceMemory: *resource.NewQuantity(3*int64(units.GiB), resource.BinarySI),
						},
					},
				},
			},
		},
		Status: v1.PodStatus{
			QOSClass: v1.PodQOSBurstable,
		},
	}

	for _, tc := range []struct {
		name  string
		vpa   *apis.KatalystVerticalPodAutoscaler
		pods  []*v1.Pod
		legal bool
		msg   string
	}{
		{
			name:  "legal: empty vpa and empty pods",
			vpa:   vpa1,
			pods:  nil,
			legal: true,
		},
		{
			name: "legal: qos class empty but qos class not change",
			vpa:  vpa1,
			pods: []*v1.Pod{
				pod1,
			},
			legal: true,
			msg:   "",
		},
		{
			name: "legal: request and limit scale up",
			vpa:  vpa1,
			pods: []*v1.Pod{
				pod2,
			},
			legal: true,
			msg:   "",
		},
		{
			name: "legal: request and limit scale down",
			vpa:  vpa1,
			pods: []*v1.Pod{
				pod3,
			},
			legal: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			isLegal, msg, err := CheckVPAStatusLegal(tc.vpa, tc.pods)
			assert.Equal(t, tc.legal, isLegal)
			assert.NoError(t, err)
			assert.Equal(t, tc.msg, msg)
		})
	}
}

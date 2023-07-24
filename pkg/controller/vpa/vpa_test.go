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

package vpa

import (
	"context"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/cache"
	cliflag "k8s.io/component-base/cli/flag"
	"k8s.io/utils/pointer"

	apis "github.com/kubewharf/katalyst-api/pkg/apis/autoscaling/v1alpha1"
	apiconsts "github.com/kubewharf/katalyst-api/pkg/consts"
	katalystbase "github.com/kubewharf/katalyst-core/cmd/base"
	"github.com/kubewharf/katalyst-core/cmd/katalyst-controller/app/options"
	"github.com/kubewharf/katalyst-core/pkg/config/controller"
	"github.com/kubewharf/katalyst-core/pkg/config/generic"
	"github.com/kubewharf/katalyst-core/pkg/controller/vpa/util"
)

var makePod = func(name string, annotations, labels map[string]string, owners []metav1.OwnerReference) *v1.Pod {
	pod := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:            name,
			Namespace:       "default",
			Annotations:     annotations,
			Labels:          labels,
			OwnerReferences: owners,
		},
	}
	return pod
}

func TestVPAControllerSyncVPA(t *testing.T) {
	t.Parallel()

	pod1 := makePod("pod1",
		map[string]string{},
		map[string]string{"workload": "sts1"},
		[]metav1.OwnerReference{
			{
				Kind:       "StatefulSet",
				APIVersion: "apps/v1",
				Name:       "sts1",
			},
		},
	)
	pod1.Spec.Containers = []v1.Container{
		{
			Name: "c1",
			Resources: v1.ResourceRequirements{
				Limits: map[v1.ResourceName]resource.Quantity{
					v1.ResourceCPU:    resource.MustParse("4"),
					v1.ResourceMemory: resource.MustParse("4Gi"),
				},
				Requests: map[v1.ResourceName]resource.Quantity{
					v1.ResourceCPU:    resource.MustParse("2"),
					v1.ResourceMemory: resource.MustParse("2Gi"),
				},
			},
		},
	}
	pod1.Status.QOSClass = v1.PodQOSBurstable

	newPod1 := pod1.DeepCopy()
	newPod1.Annotations[apiconsts.PodAnnotationInplaceUpdateResourcesKey] = "{\"c1\":{\"requests\":{\"cpu\":\"1\",\"memory\":\"1Gi\"}}}"

	vpa1 := &apis.KatalystVerticalPodAutoscaler{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "vpa1",
			Namespace: "default",
			UID:       "vpauid1",
			Annotations: map[string]string{
				apiconsts.VPAAnnotationWorkloadRetentionPolicyKey: apiconsts.VPAAnnotationWorkloadRetentionPolicyDelete,
			},
		},
		Spec: apis.KatalystVerticalPodAutoscalerSpec{
			TargetRef: apis.CrossVersionObjectReference{
				Kind:       "StatefulSet",
				APIVersion: "apps/v1",
				Name:       "sts1",
			},
			UpdatePolicy: apis.PodUpdatePolicy{
				PodUpdatingStrategy: apis.PodUpdatingStrategyInplace,
				PodMatchingStrategy: apis.PodMatchingStrategyAll,
			},
		},
		Status: apis.KatalystVerticalPodAutoscalerStatus{
			ContainerResources: []apis.ContainerResources{
				{
					ContainerName: pointer.String("c1"),
					Requests: &apis.ContainerResourceList{
						Target: map[v1.ResourceName]resource.Quantity{
							v1.ResourceCPU:    resource.MustParse("1"),
							v1.ResourceMemory: resource.MustParse("1Gi"),
						},
						UncappedTarget: map[v1.ResourceName]resource.Quantity{
							v1.ResourceCPU:    resource.MustParse("1"),
							v1.ResourceMemory: resource.MustParse("1Gi"),
						},
					},
				},
			},
			Conditions: []apis.VerticalPodAutoscalerCondition{
				{
					Type:   apis.RecommendationUpdated,
					Status: v1.ConditionTrue,
					Reason: util.VPAConditionReasonUpdated,
				},
			},
		},
	}
	vpaNew1 := vpa1.DeepCopy()
	vpaNew1.OwnerReferences = nil

	vpa2 := vpaNew1.DeepCopy()
	vpa2.Annotations[apiconsts.VPAAnnotationWorkloadRetentionPolicyKey] = apiconsts.VPAAnnotationWorkloadRetentionPolicyRetain
	vpanew2 := vpa2.DeepCopy()
	vpanew2.OwnerReferences = []metav1.OwnerReference{
		{
			APIVersion: "apps/v1",
			Kind:       "StatefulSet",
			Name:       "sts1",
			UID:        "",
		},
	}

	vpa3 := &apis.KatalystVerticalPodAutoscaler{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "vpa1",
			Namespace: "default",
			UID:       "vpauid1",
			Annotations: map[string]string{
				apiconsts.VPAAnnotationWorkloadRetentionPolicyKey: apiconsts.VPAAnnotationWorkloadRetentionPolicyDelete,
			},
		},
		Spec: apis.KatalystVerticalPodAutoscalerSpec{
			TargetRef: apis.CrossVersionObjectReference{
				Kind:       "StatefulSet",
				APIVersion: "apps/v1",
				Name:       "sts1",
			},
			UpdatePolicy: apis.PodUpdatePolicy{
				PodUpdatingStrategy: apis.PodUpdatingStrategyInplace,
				PodMatchingStrategy: apis.PodMatchingStrategyAll,
			},
		},
		Status: apis.KatalystVerticalPodAutoscalerStatus{
			ContainerResources: []apis.ContainerResources{
				{
					ContainerName: pointer.String("c1"),
					Limits: &apis.ContainerResourceList{
						Target:         map[v1.ResourceName]resource.Quantity{},
						UncappedTarget: map[v1.ResourceName]resource.Quantity{},
					},
				},
			},
			Conditions: []apis.VerticalPodAutoscalerCondition{
				{
					Type:   apis.RecommendationUpdated,
					Status: v1.ConditionTrue,
					Reason: util.VPAConditionReasonUpdated,
				},
			},
		},
	}

	vpaNew3 := &apis.KatalystVerticalPodAutoscaler{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "vpa1",
			Namespace: "default",
			UID:       "vpauid1",
			Annotations: map[string]string{
				apiconsts.VPAAnnotationWorkloadRetentionPolicyKey: apiconsts.VPAAnnotationWorkloadRetentionPolicyDelete,
			},
		},
		Spec: apis.KatalystVerticalPodAutoscalerSpec{
			TargetRef: apis.CrossVersionObjectReference{
				Kind:       "StatefulSet",
				APIVersion: "apps/v1",
				Name:       "sts1",
			},
			UpdatePolicy: apis.PodUpdatePolicy{
				PodUpdatingStrategy: apis.PodUpdatingStrategyInplace,
				PodMatchingStrategy: apis.PodMatchingStrategyAll,
			},
		},
		Status: apis.KatalystVerticalPodAutoscalerStatus{
			ContainerResources: []apis.ContainerResources{
				{
					ContainerName: pointer.String("c1"),
					Limits: &apis.ContainerResourceList{
						Current: v1.ResourceList{
							v1.ResourceCPU:    resource.MustParse("4"),
							v1.ResourceMemory: resource.MustParse("4Gi"),
						},
						Target:         map[v1.ResourceName]resource.Quantity{},
						UncappedTarget: map[v1.ResourceName]resource.Quantity{},
					},
				},
			},
			Conditions: []apis.VerticalPodAutoscalerCondition{
				{
					Type:   apis.RecommendationUpdated,
					Status: v1.ConditionTrue,
					Reason: util.VPAConditionReasonUpdated,
				},
			},
		},
	}

	newPod3 := pod1.DeepCopy()

	for _, tc := range []struct {
		name    string
		object  runtime.Object
		pods    []*v1.Pod
		newPods []*v1.Pod
		vpa     *apis.KatalystVerticalPodAutoscaler
		vpaNew  *apis.KatalystVerticalPodAutoscaler
	}{
		{
			name:   "delete owner reference",
			vpa:    vpa1,
			vpaNew: vpaNew1,
			object: &appsv1.StatefulSet{
				TypeMeta: metav1.TypeMeta{
					Kind:       "StatefulSet",
					APIVersion: "apps/v1",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      "sts1",
					Namespace: "default",
					Annotations: map[string]string{
						apiconsts.WorkloadAnnotationVPAEnabledKey: apiconsts.WorkloadAnnotationVPAEnabled,
					},
				},
				Spec: appsv1.StatefulSetSpec{
					Selector: &metav1.LabelSelector{
						MatchLabels: map[string]string{
							"workload": "sts1",
						},
					},
				},
			},
			pods: []*v1.Pod{
				pod1,
			},
			newPods: []*v1.Pod{
				newPod1,
			},
		},
		{
			name:   "retain owner reference",
			vpa:    vpa2,
			vpaNew: vpanew2,
			object: &appsv1.StatefulSet{
				TypeMeta: metav1.TypeMeta{
					Kind:       "StatefulSet",
					APIVersion: "apps/v1",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      "sts1",
					Namespace: "default",
					Annotations: map[string]string{
						apiconsts.WorkloadAnnotationVPAEnabledKey: apiconsts.WorkloadAnnotationVPAEnabled,
					},
				},
				Spec: appsv1.StatefulSetSpec{
					Selector: &metav1.LabelSelector{
						MatchLabels: map[string]string{
							"workload": "sts1",
						},
					},
				},
			},
			pods: []*v1.Pod{
				pod1,
			},
			newPods: []*v1.Pod{
				newPod1,
			},
		},
		{
			name:   "sync pod current",
			vpa:    vpa3,
			vpaNew: vpaNew3,
			object: &appsv1.StatefulSet{
				TypeMeta: metav1.TypeMeta{
					Kind:       "StatefulSet",
					APIVersion: "apps/v1",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      "sts1",
					Namespace: "default",
					Annotations: map[string]string{
						apiconsts.WorkloadAnnotationVPAEnabledKey: apiconsts.WorkloadAnnotationVPAEnabled,
					},
				},
				Spec: appsv1.StatefulSetSpec{
					Selector: &metav1.LabelSelector{
						MatchLabels: map[string]string{
							"workload": "sts1",
						},
					},
				},
			},
			pods: []*v1.Pod{
				pod1,
			},
			newPods: []*v1.Pod{
				newPod3,
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fss := &cliflag.NamedFlagSets{}
			vpaOptions := options.NewVPAOptions()
			vpaOptions.AddFlags(fss)
			vpaConf := controller.NewVPAConfig()
			_ = vpaOptions.ApplyTo(vpaConf)

			workloadGVResources := []string{"statefulsets.v1.apps"}
			vpaConf.VPAWorkloadGVResources = workloadGVResources

			genericConf := &generic.GenericConfiguration{}
			controllerConf := &controller.GenericControllerConfiguration{
				DynamicGVResources: workloadGVResources,
			}

			controlCtx, err := katalystbase.GenerateFakeGenericContext(nil, nil, []runtime.Object{tc.object})
			assert.NoError(t, err)

			vpaController, err := NewVPAController(context.TODO(), controlCtx, genericConf, controllerConf, vpaConf)
			assert.NoError(t, err)

			_, err = controlCtx.Client.InternalClient.AutoscalingV1alpha1().
				KatalystVerticalPodAutoscalers(tc.vpa.Namespace).
				Create(context.TODO(), tc.vpa, metav1.CreateOptions{})
			assert.NoError(t, err)

			for _, pod := range tc.pods {
				_, err = controlCtx.Client.KubeClient.CoreV1().Pods(pod.Namespace).Create(context.TODO(), pod, metav1.CreateOptions{})
				assert.NoError(t, err)
			}

			key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(tc.vpa)
			assert.NoError(t, err)

			controlCtx.StartInformer(vpaController.ctx)
			synced := cache.WaitForCacheSync(vpaController.ctx.Done(), vpaController.syncedFunc...)
			assert.True(t, synced)

			err = vpaController.syncVPA(key)
			assert.NoError(t, err)

			for _, pod := range tc.newPods {
				p, err := controlCtx.Client.KubeClient.CoreV1().Pods(pod.Namespace).Get(context.TODO(), pod.Name, metav1.GetOptions{})
				assert.NoError(t, err)
				assert.Equal(t, pod, p)
			}

			vpa, err := controlCtx.Client.InternalClient.AutoscalingV1alpha1().KatalystVerticalPodAutoscalers(tc.vpaNew.Namespace).
				Get(context.TODO(), tc.vpaNew.Name, metav1.GetOptions{})
			assert.NoError(t, err)
			assert.Equal(t, tc.vpaNew.GetObjectMeta(), vpa.GetObjectMeta())
		})
	}
}

func TestVPAControllerSyncPod(t *testing.T) {
	t.Parallel()

	pod1 := makePod("pod1",
		map[string]string{},
		map[string]string{"workload": "sts1"},
		[]metav1.OwnerReference{
			{
				Kind:       "StatefulSet",
				APIVersion: "apps/v1",
				Name:       "sts1",
			},
		},
	)
	pod1.Spec.Containers = []v1.Container{
		{
			Name: "c1",
			Resources: v1.ResourceRequirements{
				Limits: map[v1.ResourceName]resource.Quantity{
					v1.ResourceCPU:    resource.MustParse("4"),
					v1.ResourceMemory: resource.MustParse("4Gi"),
				},
				Requests: map[v1.ResourceName]resource.Quantity{
					v1.ResourceCPU:    resource.MustParse("2"),
					v1.ResourceMemory: resource.MustParse("2Gi"),
				},
			},
		},
	}
	pod1.Status.QOSClass = v1.PodQOSBurstable

	newPod1 := pod1.DeepCopy()
	newPod1.Annotations[apiconsts.PodAnnotationInplaceUpdateResourcesKey] = "{\"c1\":{\"requests\":{\"cpu\":\"1\",\"memory\":\"1Gi\"}}}"
	for _, tc := range []struct {
		name   string
		object runtime.Object
		pod    *v1.Pod
		newPod *v1.Pod
		vpa    *apis.KatalystVerticalPodAutoscaler
	}{
		{
			name: "test1",
			vpa: &apis.KatalystVerticalPodAutoscaler{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "vpa1",
					Namespace: "default",
					UID:       "vpauid1",
				},
				Spec: apis.KatalystVerticalPodAutoscalerSpec{
					TargetRef: apis.CrossVersionObjectReference{
						Kind:       "StatefulSet",
						APIVersion: "apps/v1",
						Name:       "sts1",
					},
					UpdatePolicy: apis.PodUpdatePolicy{
						PodUpdatingStrategy: apis.PodUpdatingStrategyInplace,
						PodMatchingStrategy: apis.PodMatchingStrategyAll,
					},
				},
				Status: apis.KatalystVerticalPodAutoscalerStatus{
					ContainerResources: []apis.ContainerResources{
						{
							ContainerName: pointer.String("c1"),
							Requests: &apis.ContainerResourceList{
								Target: map[v1.ResourceName]resource.Quantity{
									v1.ResourceCPU:    resource.MustParse("1"),
									v1.ResourceMemory: resource.MustParse("1Gi"),
								},
								UncappedTarget: map[v1.ResourceName]resource.Quantity{
									v1.ResourceCPU:    resource.MustParse("1"),
									v1.ResourceMemory: resource.MustParse("1Gi"),
								},
							},
						},
					},
					Conditions: []apis.VerticalPodAutoscalerCondition{
						{
							Type:   apis.RecommendationUpdated,
							Status: v1.ConditionTrue,
							Reason: util.VPAConditionReasonUpdated,
						},
					},
				},
			},
			object: &appsv1.StatefulSet{
				TypeMeta: metav1.TypeMeta{
					Kind:       "StatefulSet",
					APIVersion: "apps/v1",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      "sts1",
					Namespace: "default",
					Annotations: map[string]string{
						apiconsts.WorkloadAnnotationVPAEnabledKey: apiconsts.WorkloadAnnotationVPAEnabled,
					},
				},
				Spec: appsv1.StatefulSetSpec{
					Selector: &metav1.LabelSelector{
						MatchLabels: map[string]string{
							"workload": "sts1",
						},
					},
				},
			},
			pod:    pod1,
			newPod: newPod1,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fss := &cliflag.NamedFlagSets{}
			vpaOptions := options.NewVPAOptions()
			vpaOptions.AddFlags(fss)
			vpaConf := controller.NewVPAConfig()
			_ = vpaOptions.ApplyTo(vpaConf)

			workloadGVResources := []string{"statefulsets.v1.apps"}
			vpaConf.VPAWorkloadGVResources = workloadGVResources

			genericConf := &generic.GenericConfiguration{}
			generalConf := &controller.GenericControllerConfiguration{
				DynamicGVResources: workloadGVResources,
			}

			controlCtx, err := katalystbase.GenerateFakeGenericContext(nil, nil, []runtime.Object{tc.object})
			assert.NoError(t, err)

			cxt, cancel := context.WithCancel(context.TODO())
			defer cancel()
			vpaController, err := NewVPAController(cxt, controlCtx, genericConf, generalConf, vpaConf)
			assert.NoError(t, err)

			controlCtx.StartInformer(cxt)
			go vpaController.Run()

			synced := cache.WaitForCacheSync(vpaController.ctx.Done(), vpaController.syncedFunc...)
			assert.True(t, synced)

			_, err = controlCtx.Client.InternalClient.AutoscalingV1alpha1().
				KatalystVerticalPodAutoscalers(tc.vpa.Namespace).
				Create(context.TODO(), tc.vpa, metav1.CreateOptions{})
			assert.NoError(t, err)

			_, err = controlCtx.Client.KubeClient.CoreV1().Pods(tc.pod.Namespace).Create(context.TODO(), tc.pod, metav1.CreateOptions{})
			assert.NoError(t, err)

			go vpaController.Run()

			key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(tc.vpa)
			assert.NoError(t, err)
			vpaController.vpaSyncQueue.Add(key)

			err = wait.PollImmediate(time.Millisecond*200, time.Second*5, func() (bool, error) {
				p, _ := controlCtx.Client.KubeClient.CoreV1().Pods(tc.newPod.Namespace).Get(context.TODO(), tc.newPod.Name, metav1.GetOptions{})
				eq := reflect.DeepEqual(tc.newPod, p)
				return eq, nil
			})
			assert.NoError(t, err)

			p, err := controlCtx.Client.KubeClient.CoreV1().Pods(tc.newPod.Namespace).Get(context.TODO(), tc.newPod.Name, metav1.GetOptions{})
			assert.NoError(t, err)
			assert.Equal(t, tc.newPod, p)
		})
	}
}

func TestVPAControllerSyncWorkload(t *testing.T) {
	t.Parallel()

	pod1 := makePod("pod1",
		map[string]string{},
		map[string]string{"workload": "sts1"},
		[]metav1.OwnerReference{
			{
				Kind:       "StatefulSet",
				APIVersion: "apps/v1",
				Name:       "sts1",
			},
		},
	)
	pod1.Spec.Containers = []v1.Container{
		{
			Name: "c1",
			Resources: v1.ResourceRequirements{
				Limits: map[v1.ResourceName]resource.Quantity{
					v1.ResourceCPU:    resource.MustParse("4"),
					v1.ResourceMemory: resource.MustParse("4Gi"),
				},
				Requests: map[v1.ResourceName]resource.Quantity{
					v1.ResourceCPU:    resource.MustParse("2"),
					v1.ResourceMemory: resource.MustParse("2Gi"),
				},
			},
		},
	}
	pod1.Status.QOSClass = v1.PodQOSBurstable

	newPod1 := pod1.DeepCopy()
	newPod1.Annotations[apiconsts.PodAnnotationInplaceUpdateResourcesKey] = "{\"c1\":{\"requests\":{\"cpu\":\"1\",\"memory\":\"1Gi\"}}}"
	for _, tc := range []struct {
		name   string
		object runtime.Object
		pod    *v1.Pod
		newPod *v1.Pod
		vpa    *apis.KatalystVerticalPodAutoscaler
	}{
		{
			name: "test1",
			vpa: &apis.KatalystVerticalPodAutoscaler{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "vpa1",
					Namespace: "default",
					UID:       "vpauid1",
				},
				Spec: apis.KatalystVerticalPodAutoscalerSpec{
					TargetRef: apis.CrossVersionObjectReference{
						Kind:       "StatefulSet",
						APIVersion: "apps/v1",
						Name:       "sts1",
					},
					UpdatePolicy: apis.PodUpdatePolicy{
						PodUpdatingStrategy: apis.PodUpdatingStrategyInplace,
						PodMatchingStrategy: apis.PodMatchingStrategyAll,
					},
				},
				Status: apis.KatalystVerticalPodAutoscalerStatus{
					ContainerResources: []apis.ContainerResources{
						{
							ContainerName: pointer.String("c1"),
							Requests: &apis.ContainerResourceList{
								Target: map[v1.ResourceName]resource.Quantity{
									v1.ResourceCPU:    resource.MustParse("1"),
									v1.ResourceMemory: resource.MustParse("1Gi"),
								},
								UncappedTarget: map[v1.ResourceName]resource.Quantity{
									v1.ResourceCPU:    resource.MustParse("1"),
									v1.ResourceMemory: resource.MustParse("1Gi"),
								},
							},
						},
					},
					Conditions: []apis.VerticalPodAutoscalerCondition{
						{
							Type:   apis.RecommendationUpdated,
							Status: v1.ConditionTrue,
							Reason: util.VPAConditionReasonUpdated,
						},
					},
				},
			},
			object: &appsv1.StatefulSet{
				TypeMeta: metav1.TypeMeta{
					Kind:       "StatefulSet",
					APIVersion: "apps/v1",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:        "sts1",
					Namespace:   "default",
					Annotations: map[string]string{apiconsts.WorkloadAnnotationVPAEnabledKey: apiconsts.WorkloadAnnotationVPAEnabled},
				},
				Spec: appsv1.StatefulSetSpec{
					Selector: &metav1.LabelSelector{
						MatchLabels: map[string]string{
							"workload": "sts1",
						},
					},
				},
			},
			pod:    pod1,
			newPod: newPod1,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fss := &cliflag.NamedFlagSets{}
			vpaOptions := options.NewVPAOptions()
			vpaOptions.AddFlags(fss)
			vpaConf := controller.NewVPAConfig()
			_ = vpaOptions.ApplyTo(vpaConf)

			workloadGVResources := []string{"statefulsets.v1.apps"}
			vpaConf.VPAWorkloadGVResources = workloadGVResources

			genericConf := &generic.GenericConfiguration{}
			controllerConf := &controller.GenericControllerConfiguration{
				DynamicGVResources: workloadGVResources,
			}

			controlCtx, err := katalystbase.GenerateFakeGenericContext(nil, nil, []runtime.Object{tc.object})
			assert.NoError(t, err)

			cxt, cancel := context.WithCancel(context.TODO())
			defer cancel()
			vpaController, err := NewVPAController(cxt, controlCtx, genericConf, controllerConf, vpaConf)
			assert.NoError(t, err)

			controlCtx.StartInformer(cxt)
			go vpaController.Run()

			synced := cache.WaitForCacheSync(vpaController.ctx.Done(), vpaController.syncedFunc...)
			assert.True(t, synced)

			_, err = controlCtx.Client.InternalClient.AutoscalingV1alpha1().
				KatalystVerticalPodAutoscalers(tc.vpa.Namespace).
				Create(context.TODO(), tc.vpa, metav1.CreateOptions{})
			assert.NoError(t, err)

			_, err = controlCtx.Client.KubeClient.CoreV1().Pods(tc.pod.Namespace).Create(context.TODO(), tc.pod, metav1.CreateOptions{})
			assert.NoError(t, err)

			err = wait.PollImmediate(time.Millisecond*20, time.Second*5, func() (bool, error) {
				p, _ := controlCtx.Client.KubeClient.CoreV1().Pods(tc.newPod.Namespace).Get(context.TODO(), tc.newPod.Name, metav1.GetOptions{})
				eq := reflect.DeepEqual(tc.newPod, p)
				return eq, nil
			})
			assert.NoError(t, err)

			p, err := controlCtx.Client.KubeClient.CoreV1().Pods(tc.newPod.Namespace).Get(context.TODO(), tc.newPod.Name, metav1.GetOptions{})
			assert.NoError(t, err)
			assert.Equal(t, tc.newPod, p)
		})
	}
}

func TestPodIndexerDuplicate(t *testing.T) {
	t.Parallel()

	vpaConf := controller.NewVPAConfig()
	genericConf := &generic.GenericConfiguration{}
	controllerConf := &controller.GenericControllerConfiguration{}
	controlCtx, err := katalystbase.GenerateFakeGenericContext(nil, nil, nil)
	assert.NoError(t, err)

	vpaConf.VPAPodLabelIndexerKeys = []string{"test-1"}

	_, err = NewVPAController(context.TODO(), controlCtx, genericConf, controllerConf, vpaConf)
	assert.NoError(t, err)

	_, err = NewVPAController(context.TODO(), controlCtx, genericConf, controllerConf, vpaConf)
	assert.NoError(t, err)

	_, err = NewResourceRecommendController(context.TODO(), controlCtx, genericConf, controllerConf, vpaConf)
	assert.NoError(t, err)

	indexers := controlCtx.KubeInformerFactory.Core().V1().Pods().Informer().GetIndexer().GetIndexers()
	assert.Equal(t, 2, len(indexers))
	_, exist := indexers["test-1"]
	assert.Equal(t, true, exist)

	vpaConf.VPAPodLabelIndexerKeys = []string{"test-2"}

	_, err = NewResourceRecommendController(context.TODO(), controlCtx, genericConf, controllerConf, vpaConf)
	assert.NoError(t, err)

	indexers = controlCtx.KubeInformerFactory.Core().V1().Pods().Informer().GetIndexer().GetIndexers()
	assert.Equal(t, 3, len(indexers))
}

func TestSyncPerformance(t *testing.T) {
	t.Parallel()

	var kubeObj, internalObj, dynamicObj []runtime.Object

	internalObj = append(internalObj, &apis.KatalystVerticalPodAutoscaler{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "vpa1",
			Namespace: "default",
			UID:       "uid-fake-vpa",
		},
		Spec: apis.KatalystVerticalPodAutoscalerSpec{
			TargetRef: apis.CrossVersionObjectReference{
				Kind:       "StatefulSet",
				APIVersion: "apps/v1",
				Name:       "sts1",
			},
			UpdatePolicy: apis.PodUpdatePolicy{
				PodUpdatingStrategy: apis.PodUpdatingStrategyInplace,
				PodMatchingStrategy: apis.PodMatchingStrategyAll,
			},
		},
	})

	dynamicObj = append(dynamicObj, &appsv1.StatefulSet{
		TypeMeta: metav1.TypeMeta{
			Kind:       "StatefulSet",
			APIVersion: "apps/v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "sts1",
			Namespace: "default",
			Annotations: map[string]string{
				apiconsts.WorkloadAnnotationVPAEnabledKey: apiconsts.WorkloadAnnotationVPAEnabled,
			},
		},
		Spec: appsv1.StatefulSetSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"workload":     "sts1",
					"test":         "pod-1",
					"test-mod-10":  "1",
					"test-mod-100": "1",
				},
			},
		},
	})

	amount := 1
	for i := 1; i <= amount; i++ {
		name := fmt.Sprintf("pod-%v", i)
		kubeObj = append(kubeObj, makePod(name,
			map[string]string{},
			map[string]string{
				"test":         name,
				"test-mod-10":  fmt.Sprintf("%v", i%10),
				"test-mod-100": fmt.Sprintf("%v", i%100),
				"workload":     "sts1",
			},
			[]metav1.OwnerReference{
				{
					Kind:       "StatefulSet",
					APIVersion: "apps/v1",
					Name:       "sts1",
				},
			},
		))
	}

	for _, tc := range []struct {
		name    string
		vpaConf *controller.VPAConfig
	}{
		{
			name: "sync without indexer",
			vpaConf: &controller.VPAConfig{
				VPAPodLabelIndexerKeys: []string{"test"},
			},
		},
		{
			name: "sync with the same indexer",
			vpaConf: &controller.VPAConfig{
				VPAPodLabelIndexerKeys: []string{"workload"},
			},
		},
		{
			name: "sync with individual indexer",
			vpaConf: &controller.VPAConfig{
				VPAPodLabelIndexerKeys: []string{"test"},
			},
		},
	} {
		t.Logf("test cases: %v", tc.name)

		workloadGVResources := []string{"statefulsets.v1.apps"}
		tc.vpaConf.VPAWorkloadGVResources = workloadGVResources

		genericConf := &generic.GenericConfiguration{}
		controllerConf := &controller.GenericControllerConfiguration{
			DynamicGVResources: workloadGVResources,
		}

		controlCtx, err := katalystbase.GenerateFakeGenericContext(kubeObj, internalObj, dynamicObj)
		assert.NoError(t, err)

		vc, err := NewVPAController(context.TODO(), controlCtx, genericConf, controllerConf, tc.vpaConf)
		assert.NoError(t, err)

		for _, obj := range kubeObj {
			err = controlCtx.KubeInformerFactory.Core().V1().Pods().Informer().GetStore().Add(obj)
			assert.NoError(t, err)
		}
		for _, obj := range internalObj {
			err = controlCtx.InternalInformerFactory.Autoscaling().V1alpha1().KatalystVerticalPodAutoscalers().Informer().GetStore().Add(obj)
			assert.NoError(t, err)
		}

		controlCtx.StartInformer(vc.ctx)
		synced := cache.WaitForCacheSync(vc.ctx.Done(), vc.syncedFunc...)
		assert.True(t, synced)

		err = vc.syncVPA("default" + "/" + "vpa1")
		assert.NoError(t, err)
	}
}

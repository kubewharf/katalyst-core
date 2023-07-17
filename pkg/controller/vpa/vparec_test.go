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
	"testing"

	"github.com/alecthomas/units"
	"github.com/stretchr/testify/assert"
	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
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
	"github.com/kubewharf/katalyst-core/pkg/util/native"
)

func TestVPARecControllerSyncVPA(t *testing.T) {
	t.Parallel()

	pod1 := makePod("pod1",
		map[string]string{apiconsts.WorkloadAnnotationVPAEnabledKey: apiconsts.WorkloadAnnotationVPAEnabled},
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
					v1.ResourceCPU:    *resource.NewQuantity(4, resource.DecimalSI),
					v1.ResourceMemory: *resource.NewQuantity(4*int64(units.GiB), resource.BinarySI),
				},
				Requests: map[v1.ResourceName]resource.Quantity{
					v1.ResourceCPU:    *resource.NewQuantity(2, resource.DecimalSI),
					v1.ResourceMemory: *resource.NewQuantity(2*int64(units.GiB), resource.BinarySI),
				},
			},
		},
	}
	pod1.Status.QOSClass = v1.PodQOSBurstable
	for _, tc := range []struct {
		name    string
		vpaOld  *apis.KatalystVerticalPodAutoscaler
		vparecs []*apis.VerticalPodAutoscalerRecommendation
		object  runtime.Object
		pods    []*v1.Pod
		vpaNew  *apis.KatalystVerticalPodAutoscaler
	}{
		{
			name: "set vpa without resource policy",
			vpaOld: &apis.KatalystVerticalPodAutoscaler{
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
				},
			},
			vpaNew: &apis.KatalystVerticalPodAutoscaler{
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
				},
			},
			vparecs: []*apis.VerticalPodAutoscalerRecommendation{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "vparec1",
						Namespace: "default",
						UID:       "vparecuid1",
						OwnerReferences: []metav1.OwnerReference{
							{
								Name: "vpa1",
								UID:  "vpauid1",
							},
						},
					},
					Spec: apis.VerticalPodAutoscalerRecommendationSpec{
						ContainerRecommendations: []apis.RecommendedContainerResources{
							{
								ContainerName: pointer.String("c1"),
								Requests: &apis.RecommendedRequestResources{
									Resources: map[v1.ResourceName]resource.Quantity{
										v1.ResourceCPU:    *resource.NewQuantity(1, resource.DecimalSI),
										v1.ResourceMemory: *resource.NewQuantity(1*int64(units.GiB), resource.BinarySI),
									},
								},
							},
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
		},
		{
			name: "set vpa with resource policy",
			vpaOld: &apis.KatalystVerticalPodAutoscaler{
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
					ResourcePolicy: apis.PodResourcePolicy{
						ContainerPolicies: []apis.ContainerResourcePolicy{
							{
								ContainerName: pointer.String("c1"),
								MinAllowed: v1.ResourceList{
									v1.ResourceCPU:    resource.MustParse("2"),
									v1.ResourceMemory: resource.MustParse("2Gi"),
								},
								ControlledResources: []v1.ResourceName{v1.ResourceCPU, v1.ResourceMemory},
								ControlledValues:    apis.ContainerControlledValuesRequestsAndLimits,
							},
						},
					},
				},
			},
			vpaNew: &apis.KatalystVerticalPodAutoscaler{
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
					ResourcePolicy: apis.PodResourcePolicy{
						ContainerPolicies: []apis.ContainerResourcePolicy{
							{
								ContainerName: pointer.String("c1"),
								MinAllowed: v1.ResourceList{
									v1.ResourceCPU:    resource.MustParse("2"),
									v1.ResourceMemory: resource.MustParse("2Gi"),
								},
								ControlledResources: []v1.ResourceName{v1.ResourceCPU, v1.ResourceMemory},
								ControlledValues:    apis.ContainerControlledValuesRequestsAndLimits,
							},
						},
					},
				},
				Status: apis.KatalystVerticalPodAutoscalerStatus{
					ContainerResources: []apis.ContainerResources{
						{
							ContainerName: pointer.String("c1"),
							Requests: &apis.ContainerResourceList{
								Target: map[v1.ResourceName]resource.Quantity{
									v1.ResourceCPU:    resource.MustParse("2"),
									v1.ResourceMemory: resource.MustParse("2Gi"),
								},
								UncappedTarget: map[v1.ResourceName]resource.Quantity{
									v1.ResourceCPU:    resource.MustParse("1"),
									v1.ResourceMemory: resource.MustParse("1Gi"),
								},
								LowerBound: map[v1.ResourceName]resource.Quantity{
									v1.ResourceCPU:    resource.MustParse("2"),
									v1.ResourceMemory: resource.MustParse("2Gi"),
								},
							},
						},
					},
				},
			},
			vparecs: []*apis.VerticalPodAutoscalerRecommendation{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "vparec1",
						Namespace: "default",
						UID:       "vparecuid1",
						OwnerReferences: []metav1.OwnerReference{
							{
								Name: "vpa1",
								UID:  "vpauid1",
							},
						},
					},
					Spec: apis.VerticalPodAutoscalerRecommendationSpec{
						ContainerRecommendations: []apis.RecommendedContainerResources{
							{
								ContainerName: pointer.String("c1"),
								Requests: &apis.RecommendedRequestResources{
									Resources: map[v1.ResourceName]resource.Quantity{
										v1.ResourceCPU:    *resource.NewQuantity(1, resource.DecimalSI),
										v1.ResourceMemory: *resource.NewQuantity(1*int64(units.GiB), resource.BinarySI),
									},
								},
							},
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
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			genericConfig := &generic.GenericConfiguration{}
			controllerConf := &controller.GenericControllerConfiguration{
				DynamicGVResources: []string{"statefulsets.v1.apps"},
			}
			fss := &cliflag.NamedFlagSets{}
			vpaOptions := options.NewVPAOptions()
			vpaOptions.AddFlags(fss)
			vpaConf := controller.NewVPAConfig()
			vpaOptions.ApplyTo(vpaConf)
			controlCtx, err := katalystbase.GenerateFakeGenericContext(nil, []runtime.Object{tc.vpaOld}, []runtime.Object{tc.object})
			assert.NoError(t, err)

			vparec, err := NewVPARecommendationController(context.TODO(), controlCtx, genericConfig, controllerConf, vpaConf)
			assert.NoError(t, err)

			workloadInformers := controlCtx.DynamicResourcesManager.GetDynamicInformers()
			u, err := native.ToUnstructured(tc.object)
			assert.NoError(t, err)
			err = workloadInformers["statefulsets.v1.apps"].Informer.Informer().GetStore().Add(u)
			assert.NoError(t, err)

			vpaInformer := controlCtx.InternalInformerFactory.Autoscaling().V1alpha1().KatalystVerticalPodAutoscalers()
			err = vpaInformer.Informer().GetStore().Add(tc.vpaOld)
			assert.NoError(t, err)

			vparecInformer := controlCtx.InternalInformerFactory.Autoscaling().V1alpha1().VerticalPodAutoscalerRecommendations()
			for _, rec := range tc.vparecs {
				err = vparecInformer.Informer().GetStore().Add(rec)
				assert.NoError(t, err)
			}
			for _, pod := range tc.pods {
				err = controlCtx.KubeInformerFactory.Core().V1().Pods().Informer().GetStore().Add(pod)
				assert.NoError(t, err)
			}

			for _, vpaRec := range tc.vparecs {
				key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(vpaRec)
				assert.NoError(t, err)

				err = vparec.syncVPARec(key)
				assert.NoError(t, err)
			}

			vpa, err := controlCtx.Client.InternalClient.AutoscalingV1alpha1().
				KatalystVerticalPodAutoscalers(tc.vpaNew.Namespace).Get(context.TODO(), tc.vpaNew.Name, metav1.GetOptions{})
			assert.NoError(t, err)
			assert.Equal(t, tc.vpaNew, vpa)
		})
	}
}

func TestVPARecControllerSyncVPARec(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name      string
		vparecOld *apis.VerticalPodAutoscalerRecommendation
		vpa       *apis.KatalystVerticalPodAutoscaler
		vparecNew *apis.VerticalPodAutoscalerRecommendation
	}{
		{
			name: "set vpa with resource policy",
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
					ResourcePolicy: apis.PodResourcePolicy{
						ContainerPolicies: []apis.ContainerResourcePolicy{
							{
								ContainerName: pointer.String("c1"),
								MinAllowed: v1.ResourceList{
									v1.ResourceCPU:    resource.MustParse("2"),
									v1.ResourceMemory: resource.MustParse("2Gi"),
								},
								ControlledResources: []v1.ResourceName{v1.ResourceCPU, v1.ResourceMemory},
								ControlledValues:    apis.ContainerControlledValuesRequestsAndLimits,
							},
						},
					},
				},
				Status: apis.KatalystVerticalPodAutoscalerStatus{
					ContainerResources: []apis.ContainerResources{
						{
							ContainerName: pointer.String("c1"),
							Requests: &apis.ContainerResourceList{
								Target: map[v1.ResourceName]resource.Quantity{
									v1.ResourceCPU:    resource.MustParse("2"),
									v1.ResourceMemory: resource.MustParse("2Gi"),
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
			vparecOld: &apis.VerticalPodAutoscalerRecommendation{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "vparec1",
					Namespace: "default",
					UID:       "vparecuid1",
					OwnerReferences: []metav1.OwnerReference{
						{
							Name: "vpa1",
							UID:  "vpauid1",
						},
					},
				},
				Spec: apis.VerticalPodAutoscalerRecommendationSpec{
					ContainerRecommendations: []apis.RecommendedContainerResources{
						{
							ContainerName: pointer.String("c1"),
							Requests: &apis.RecommendedRequestResources{
								Resources: map[v1.ResourceName]resource.Quantity{
									v1.ResourceCPU:    *resource.NewQuantity(1, resource.DecimalSI),
									v1.ResourceMemory: *resource.NewQuantity(1*int64(units.GiB), resource.BinarySI),
								},
							},
						},
					},
				},
			},
			vparecNew: &apis.VerticalPodAutoscalerRecommendation{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "vparec1",
					Namespace: "default",
					UID:       "vparecuid1",
					OwnerReferences: []metav1.OwnerReference{
						{
							Name: "vpa1",
							UID:  "vpauid1",
						},
					},
				},
				Spec: apis.VerticalPodAutoscalerRecommendationSpec{
					ContainerRecommendations: []apis.RecommendedContainerResources{
						{
							ContainerName: pointer.String("c1"),
							Requests: &apis.RecommendedRequestResources{
								Resources: map[v1.ResourceName]resource.Quantity{
									v1.ResourceCPU:    resource.MustParse("1"),
									v1.ResourceMemory: resource.MustParse("1Gi"),
								},
							},
						},
					},
				},
				Status: apis.VerticalPodAutoscalerRecommendationStatus{
					ContainerRecommendations: []apis.RecommendedContainerResources{
						{
							ContainerName: pointer.String("c1"),
							Requests: &apis.RecommendedRequestResources{
								Resources: v1.ResourceList{
									v1.ResourceCPU:    resource.MustParse("2"),
									v1.ResourceMemory: resource.MustParse("2Gi"),
								},
							},
						},
					},
					Conditions: []apis.VerticalPodAutoscalerRecommendationCondition{
						{
							Type:    apis.RecommendationUpdatedToVPA,
							Status:  v1.ConditionTrue,
							Reason:  util.VPARecConditionReasonUpdated,
							Message: "",
						},
					},
				},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			genericConfig := &generic.GenericConfiguration{}
			controllerConf := &controller.GenericControllerConfiguration{
				DynamicGVResources: []string{"statefulsets.v1.apps"},
			}

			fss := &cliflag.NamedFlagSets{}
			vpaOptions := options.NewVPAOptions()
			vpaOptions.AddFlags(fss)
			vpaConf := controller.NewVPAConfig()
			vpaOptions.ApplyTo(vpaConf)

			controlCtx, err := katalystbase.GenerateFakeGenericContext(nil, nil, nil)
			assert.NoError(t, err)

			vparecController, err := NewVPARecommendationController(context.TODO(), controlCtx, genericConfig, controllerConf, vpaConf)
			assert.NoError(t, err)

			vpaInformer := controlCtx.InternalInformerFactory.Autoscaling().V1alpha1().KatalystVerticalPodAutoscalers()
			err = vpaInformer.Informer().GetStore().Add(tc.vpa)
			assert.NoError(t, err)
			_, err = controlCtx.Client.InternalClient.AutoscalingV1alpha1().
				KatalystVerticalPodAutoscalers(tc.vpa.Namespace).
				Create(context.TODO(), tc.vpa, metav1.CreateOptions{})
			assert.NoError(t, err)

			vparecInformer := controlCtx.InternalInformerFactory.Autoscaling().V1alpha1().VerticalPodAutoscalerRecommendations()
			err = vparecInformer.Informer().GetStore().Add(tc.vparecOld)
			assert.NoError(t, err)
			_, err = controlCtx.Client.InternalClient.AutoscalingV1alpha1().
				VerticalPodAutoscalerRecommendations(tc.vparecOld.Namespace).
				Create(context.TODO(), tc.vparecOld, metav1.CreateOptions{})
			assert.NoError(t, err)

			controlCtx.StartInformer(vparecController.ctx)
			synced := cache.WaitForCacheSync(vparecController.ctx.Done(), vparecController.syncedFunc...)
			assert.True(t, synced)

			key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(tc.vpa)
			assert.NoError(t, err)

			err = vparecController.syncVPA(key)
			assert.NoError(t, err)

			vparec, err := controlCtx.Client.InternalClient.AutoscalingV1alpha1().
				VerticalPodAutoscalerRecommendations(tc.vparecNew.Namespace).
				Get(context.TODO(), tc.vparecNew.Name, metav1.GetOptions{})
			assert.NoError(t, err)
			vparec.Status.Conditions[0].LastTransitionTime = metav1.Time{}
			assert.Equal(t, tc.vparecNew, vparec)
		})
	}
}

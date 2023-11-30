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

package spd

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/cache"
	"k8s.io/utils/pointer"

	apis "github.com/kubewharf/katalyst-api/pkg/apis/autoscaling/v1alpha1"
	apiworkload "github.com/kubewharf/katalyst-api/pkg/apis/workload/v1alpha1"
	"github.com/kubewharf/katalyst-api/pkg/consts"
	katalystbase "github.com/kubewharf/katalyst-core/cmd/base"
	"github.com/kubewharf/katalyst-core/pkg/config/controller"
	"github.com/kubewharf/katalyst-core/pkg/config/generic"
	"github.com/kubewharf/katalyst-core/pkg/util"
)

func TestSPDController_updateBaselinePercentile(t *testing.T) {
	t.Parallel()

	type fields struct {
		podList  []runtime.Object
		workload *appsv1.StatefulSet
		spd      *apiworkload.ServiceProfileDescriptor
	}
	tests := []struct {
		name    string
		fields  fields
		wantSPD *apiworkload.ServiceProfileDescriptor
		wantErr assert.ErrorAssertionFunc
	}{
		{
			name: "one pod",
			fields: fields{
				podList: []runtime.Object{
					&v1.Pod{
						ObjectMeta: metav1.ObjectMeta{
							Name:      "pod1",
							Namespace: "default",
							OwnerReferences: []metav1.OwnerReference{
								{
									APIVersion: "apps/v1",
									Kind:       "StatefulSet",
									Name:       "sts1",
								},
							},
							Annotations: map[string]string{
								consts.PodAnnotationSPDNameKey: "spd1",
							},
							Labels: map[string]string{
								"workload": "sts1",
							},
							CreationTimestamp: metav1.NewTime(time.Date(2023, time.August, 1, 0, 0, 0, 0, time.UTC)),
						},
					},
				},
				workload: &appsv1.StatefulSet{
					TypeMeta: metav1.TypeMeta{
						Kind:       "StatefulSet",
						APIVersion: "apps/v1",
					},
					ObjectMeta: metav1.ObjectMeta{
						Name:        "sts1",
						Namespace:   "default",
						Annotations: map[string]string{},
					},
					Spec: appsv1.StatefulSetSpec{
						Selector: &metav1.LabelSelector{
							MatchLabels: map[string]string{
								"workload": "sts1",
							},
						},
					},
				},
				spd: &apiworkload.ServiceProfileDescriptor{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "spd1",
					},
					Spec: apiworkload.ServiceProfileDescriptorSpec{
						TargetRef: apis.CrossVersionObjectReference{
							Kind:       stsGVK.Kind,
							Name:       "sts1",
							APIVersion: stsGVK.GroupVersion().String(),
						},
						BaselinePercent: pointer.Int32(50),
					},
					Status: apiworkload.ServiceProfileDescriptorStatus{},
				},
			},
			wantSPD: &apiworkload.ServiceProfileDescriptor{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: "default",
					Name:      "spd1",
					Annotations: map[string]string{
						consts.SPDAnnotationBaselineSentinelKey: util.SPDBaselinePodMeta{
							TimeStamp: metav1.NewTime(time.Date(2023, time.August, 1, 0, 0, 0, 0, time.UTC)),
							PodName:   "pod1",
						}.String(),
					},
				},
				Spec: apiworkload.ServiceProfileDescriptorSpec{
					TargetRef: apis.CrossVersionObjectReference{
						Kind:       stsGVK.Kind,
						Name:       "sts1",
						APIVersion: stsGVK.GroupVersion().String(),
					},
					BaselinePercent: pointer.Int32(50),
				},
				Status: apiworkload.ServiceProfileDescriptorStatus{},
			},
			wantErr: assert.NoError,
		},
		{
			name: "none pod",
			fields: fields{
				podList: []runtime.Object{},
				workload: &appsv1.StatefulSet{
					TypeMeta: metav1.TypeMeta{
						Kind:       "StatefulSet",
						APIVersion: "apps/v1",
					},
					ObjectMeta: metav1.ObjectMeta{
						Name:        "sts1",
						Namespace:   "default",
						Annotations: map[string]string{},
					},
					Spec: appsv1.StatefulSetSpec{
						Selector: &metav1.LabelSelector{
							MatchLabels: map[string]string{
								"workload": "sts1",
							},
						},
					},
				},
				spd: &apiworkload.ServiceProfileDescriptor{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "spd1",
					},
					Spec: apiworkload.ServiceProfileDescriptorSpec{
						TargetRef: apis.CrossVersionObjectReference{
							Kind:       stsGVK.Kind,
							Name:       "sts1",
							APIVersion: stsGVK.GroupVersion().String(),
						},
						BaselinePercent: pointer.Int32(50),
					},
					Status: apiworkload.ServiceProfileDescriptorStatus{},
				},
			},
			wantSPD: &apiworkload.ServiceProfileDescriptor{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: "default",
					Name:      "spd1",
					Annotations: map[string]string{
						consts.SPDAnnotationBaselineSentinelKey: util.SPDBaselinePodMeta{}.String(),
					},
				},
				Spec: apiworkload.ServiceProfileDescriptorSpec{
					TargetRef: apis.CrossVersionObjectReference{
						Kind:       stsGVK.Kind,
						Name:       "sts1",
						APIVersion: stsGVK.GroupVersion().String(),
					},
					BaselinePercent: pointer.Int32(50),
				},
				Status: apiworkload.ServiceProfileDescriptorStatus{},
			},
			wantErr: assert.NoError,
		},
		{
			name: "three pod for 50% baseline percent",
			fields: fields{
				podList: []runtime.Object{
					&v1.Pod{
						ObjectMeta: metav1.ObjectMeta{
							Name:      "pod1",
							Namespace: "default",
							OwnerReferences: []metav1.OwnerReference{
								{
									APIVersion: "apps/v1",
									Kind:       "StatefulSet",
									Name:       "sts1",
								},
							},
							Annotations: map[string]string{
								consts.PodAnnotationSPDNameKey: "spd1",
							},
							Labels: map[string]string{
								"workload": "sts1",
							},
							CreationTimestamp: metav1.NewTime(time.Date(2023, time.August, 1, 0, 0, 0, 0, time.UTC)),
						},
					},
					&v1.Pod{
						ObjectMeta: metav1.ObjectMeta{
							Name:      "pod2",
							Namespace: "default",
							OwnerReferences: []metav1.OwnerReference{
								{
									APIVersion: "apps/v1",
									Kind:       "StatefulSet",
									Name:       "sts1",
								},
							},
							Annotations: map[string]string{
								consts.PodAnnotationSPDNameKey: "spd1",
							},
							Labels: map[string]string{
								"workload": "sts1",
							},
							CreationTimestamp: metav1.NewTime(time.Date(2023, time.August, 1, 0, 0, 1, 0, time.UTC)),
						},
					},
					&v1.Pod{
						ObjectMeta: metav1.ObjectMeta{
							Name:      "pod3",
							Namespace: "default",
							OwnerReferences: []metav1.OwnerReference{
								{
									APIVersion: "apps/v1",
									Kind:       "StatefulSet",
									Name:       "sts1",
								},
							},
							Annotations: map[string]string{
								consts.PodAnnotationSPDNameKey: "spd1",
							},
							Labels: map[string]string{
								"workload": "sts1",
							},
							CreationTimestamp: metav1.NewTime(time.Date(2023, time.August, 1, 0, 0, 2, 0, time.UTC)),
						},
					},
				},
				workload: &appsv1.StatefulSet{
					TypeMeta: metav1.TypeMeta{
						Kind:       "StatefulSet",
						APIVersion: "apps/v1",
					},
					ObjectMeta: metav1.ObjectMeta{
						Name:        "sts1",
						Namespace:   "default",
						Annotations: map[string]string{},
					},
					Spec: appsv1.StatefulSetSpec{
						Selector: &metav1.LabelSelector{
							MatchLabels: map[string]string{
								"workload": "sts1",
							},
						},
					},
				},
				spd: &apiworkload.ServiceProfileDescriptor{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "spd1",
					},
					Spec: apiworkload.ServiceProfileDescriptorSpec{
						TargetRef: apis.CrossVersionObjectReference{
							Kind:       stsGVK.Kind,
							Name:       "sts1",
							APIVersion: stsGVK.GroupVersion().String(),
						},
						BaselinePercent: pointer.Int32(50),
					},
					Status: apiworkload.ServiceProfileDescriptorStatus{},
				},
			},
			wantSPD: &apiworkload.ServiceProfileDescriptor{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: "default",
					Name:      "spd1",
					Annotations: map[string]string{
						consts.SPDAnnotationBaselineSentinelKey: "{\"timeStamp\":\"2023-08-01T00:00:01Z\",\"podName\":\"pod2\"}",
					},
				},
				Spec: apiworkload.ServiceProfileDescriptorSpec{
					TargetRef: apis.CrossVersionObjectReference{
						Kind:       stsGVK.Kind,
						Name:       "sts1",
						APIVersion: stsGVK.GroupVersion().String(),
					},
					BaselinePercent: pointer.Int32(50),
				},
				Status: apiworkload.ServiceProfileDescriptorStatus{},
			},
			wantErr: assert.NoError,
		},
		{
			name: "three pod for 100% baseline percent",
			fields: fields{
				podList: []runtime.Object{
					&v1.Pod{
						ObjectMeta: metav1.ObjectMeta{
							Name:      "pod1",
							Namespace: "default",
							OwnerReferences: []metav1.OwnerReference{
								{
									APIVersion: "apps/v1",
									Kind:       "StatefulSet",
									Name:       "sts1",
								},
							},
							Annotations: map[string]string{
								consts.PodAnnotationSPDNameKey: "spd1",
							},
							Labels: map[string]string{
								"workload": "sts1",
							},
							CreationTimestamp: metav1.NewTime(time.Date(2023, time.August, 1, 0, 0, 0, 0, time.UTC)),
						},
					},
					&v1.Pod{
						ObjectMeta: metav1.ObjectMeta{
							Name:      "pod2",
							Namespace: "default",
							OwnerReferences: []metav1.OwnerReference{
								{
									APIVersion: "apps/v1",
									Kind:       "StatefulSet",
									Name:       "sts1",
								},
							},
							Annotations: map[string]string{
								consts.PodAnnotationSPDNameKey: "spd1",
							},
							Labels: map[string]string{
								"workload": "sts1",
							},
							CreationTimestamp: metav1.NewTime(time.Date(2023, time.August, 1, 0, 0, 1, 0, time.UTC)),
						},
					},
					&v1.Pod{
						ObjectMeta: metav1.ObjectMeta{
							Name:      "pod3",
							Namespace: "default",
							OwnerReferences: []metav1.OwnerReference{
								{
									APIVersion: "apps/v1",
									Kind:       "StatefulSet",
									Name:       "sts1",
								},
							},
							Annotations: map[string]string{
								consts.PodAnnotationSPDNameKey: "spd1",
							},
							Labels: map[string]string{
								"workload": "sts1",
							},
							CreationTimestamp: metav1.NewTime(time.Date(2023, time.August, 1, 0, 0, 2, 0, time.UTC)),
						},
					},
				},
				workload: &appsv1.StatefulSet{
					TypeMeta: metav1.TypeMeta{
						Kind:       "StatefulSet",
						APIVersion: "apps/v1",
					},
					ObjectMeta: metav1.ObjectMeta{
						Name:        "sts1",
						Namespace:   "default",
						Annotations: map[string]string{},
					},
					Spec: appsv1.StatefulSetSpec{
						Selector: &metav1.LabelSelector{
							MatchLabels: map[string]string{
								"workload": "sts1",
							},
						},
					},
				},
				spd: &apiworkload.ServiceProfileDescriptor{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "spd1",
					},
					Spec: apiworkload.ServiceProfileDescriptorSpec{
						TargetRef: apis.CrossVersionObjectReference{
							Kind:       stsGVK.Kind,
							Name:       "sts1",
							APIVersion: stsGVK.GroupVersion().String(),
						},
						BaselinePercent: pointer.Int32(100),
					},
					Status: apiworkload.ServiceProfileDescriptorStatus{},
				},
			},
			wantSPD: &apiworkload.ServiceProfileDescriptor{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: "default",
					Name:      "spd1",
				},
				Spec: apiworkload.ServiceProfileDescriptorSpec{
					TargetRef: apis.CrossVersionObjectReference{
						Kind:       stsGVK.Kind,
						Name:       "sts1",
						APIVersion: stsGVK.GroupVersion().String(),
					},
					BaselinePercent: pointer.Int32(100),
				},
				Status: apiworkload.ServiceProfileDescriptorStatus{},
			},
			wantErr: assert.NoError,
		},
		{
			name: "three pod for 0% baseline percent",
			fields: fields{
				podList: []runtime.Object{
					&v1.Pod{
						ObjectMeta: metav1.ObjectMeta{
							Name:      "pod1",
							Namespace: "default",
							OwnerReferences: []metav1.OwnerReference{
								{
									APIVersion: "apps/v1",
									Kind:       "StatefulSet",
									Name:       "sts1",
								},
							},
							Annotations: map[string]string{
								consts.PodAnnotationSPDNameKey: "spd1",
							},
							Labels: map[string]string{
								"workload": "sts1",
							},
							CreationTimestamp: metav1.NewTime(time.Date(2023, time.August, 1, 0, 0, 0, 0, time.UTC)),
						},
					},
					&v1.Pod{
						ObjectMeta: metav1.ObjectMeta{
							Name:      "pod2",
							Namespace: "default",
							OwnerReferences: []metav1.OwnerReference{
								{
									APIVersion: "apps/v1",
									Kind:       "StatefulSet",
									Name:       "sts1",
								},
							},
							Annotations: map[string]string{
								consts.PodAnnotationSPDNameKey: "spd1",
							},
							Labels: map[string]string{
								"workload": "sts1",
							},
							CreationTimestamp: metav1.NewTime(time.Date(2023, time.August, 1, 0, 0, 1, 0, time.UTC)),
						},
					},
					&v1.Pod{
						ObjectMeta: metav1.ObjectMeta{
							Name:      "pod3",
							Namespace: "default",
							OwnerReferences: []metav1.OwnerReference{
								{
									APIVersion: "apps/v1",
									Kind:       "StatefulSet",
									Name:       "sts1",
								},
							},
							Annotations: map[string]string{
								consts.PodAnnotationSPDNameKey: "spd1",
							},
							Labels: map[string]string{
								"workload": "sts1",
							},
							CreationTimestamp: metav1.NewTime(time.Date(2023, time.August, 1, 0, 0, 2, 0, time.UTC)),
						},
					},
				},
				workload: &appsv1.StatefulSet{
					TypeMeta: metav1.TypeMeta{
						Kind:       "StatefulSet",
						APIVersion: "apps/v1",
					},
					ObjectMeta: metav1.ObjectMeta{
						Name:        "sts1",
						Namespace:   "default",
						Annotations: map[string]string{},
					},
					Spec: appsv1.StatefulSetSpec{
						Selector: &metav1.LabelSelector{
							MatchLabels: map[string]string{
								"workload": "sts1",
							},
						},
					},
				},
				spd: &apiworkload.ServiceProfileDescriptor{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "spd1",
					},
					Spec: apiworkload.ServiceProfileDescriptorSpec{
						TargetRef: apis.CrossVersionObjectReference{
							Kind:       stsGVK.Kind,
							Name:       "sts1",
							APIVersion: stsGVK.GroupVersion().String(),
						},
						BaselinePercent: pointer.Int32(0),
					},
					Status: apiworkload.ServiceProfileDescriptorStatus{},
				},
			},
			wantSPD: &apiworkload.ServiceProfileDescriptor{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: "default",
					Name:      "spd1",
				},
				Spec: apiworkload.ServiceProfileDescriptorSpec{
					TargetRef: apis.CrossVersionObjectReference{
						Kind:       stsGVK.Kind,
						Name:       "sts1",
						APIVersion: stsGVK.GroupVersion().String(),
					},
					BaselinePercent: pointer.Int32(0),
				},
				Status: apiworkload.ServiceProfileDescriptorStatus{},
			},
			wantErr: assert.NoError,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spdConfig := &controller.SPDConfig{
				SPDWorkloadGVResources: []string{"statefulsets.v1.apps"},
			}
			genericConfig := &generic.GenericConfiguration{}
			controllerConf := &controller.GenericControllerConfiguration{
				DynamicGVResources: []string{"statefulsets.v1.apps"},
			}

			ctx := context.TODO()
			controlCtx, err := katalystbase.GenerateFakeGenericContext(tt.fields.podList,
				[]runtime.Object{tt.fields.spd}, []runtime.Object{tt.fields.workload})
			assert.NoError(t, err)

			spdController, err := NewSPDController(ctx, controlCtx, genericConfig, controllerConf, spdConfig, nil, struct{}{})
			assert.NoError(t, err)

			controlCtx.StartInformer(ctx)
			go spdController.Run()
			synced := cache.WaitForCacheSync(ctx.Done(), spdController.syncedFunc...)
			assert.True(t, synced)
			time.Sleep(1 * time.Second)

			tt.wantErr(t, spdController.updateBaselineSentinel(tt.fields.spd), fmt.Sprintf("updateBaselineSentinel(%v)", tt.fields.spd))
			assert.Equal(t, tt.wantSPD, tt.fields.spd)
		})
	}
}

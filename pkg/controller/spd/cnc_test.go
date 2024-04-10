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
	configapi "github.com/kubewharf/katalyst-api/pkg/apis/config/v1alpha1"
	apiworkload "github.com/kubewharf/katalyst-api/pkg/apis/workload/v1alpha1"
	"github.com/kubewharf/katalyst-api/pkg/consts"
	katalystbase "github.com/kubewharf/katalyst-core/cmd/base"
	"github.com/kubewharf/katalyst-core/pkg/config/controller"
	"github.com/kubewharf/katalyst-core/pkg/config/generic"
)

func Test_cncCacheController_Run(t *testing.T) {
	t.Parallel()

	type fields struct {
		pod            *v1.Pod
		workload       *appsv1.StatefulSet
		spd            *apiworkload.ServiceProfileDescriptor
		cnc            *configapi.CustomNodeConfig
		enableCNCCache bool
	}
	tests := []struct {
		name    string
		fields  fields
		wantCNC *configapi.CustomNodeConfig
	}{
		{
			name: "update cnc spd config when cnc cache enable",
			fields: fields{
				pod: &v1.Pod{
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
					},
					Spec: v1.PodSpec{
						NodeName: "node1",
					},
				},
				workload: &appsv1.StatefulSet{
					TypeMeta: metav1.TypeMeta{
						Kind:       "StatefulSet",
						APIVersion: "apps/v1",
					},
					ObjectMeta: metav1.ObjectMeta{
						Name:      "sts1",
						Namespace: "default",
						Annotations: map[string]string{
							consts.WorkloadAnnotationSPDEnableKey: consts.WorkloadAnnotationSPDEnabled,
						},
					},
					Spec: appsv1.StatefulSetSpec{
						Selector: &metav1.LabelSelector{
							MatchLabels: map[string]string{
								"workload": "sts1",
							},
						},
						Template: v1.PodTemplateSpec{
							ObjectMeta: metav1.ObjectMeta{
								Annotations: map[string]string{
									"katalyst.kubewharf.io/qos_level": "dedicated_cores",
								},
							},
							Spec: v1.PodSpec{},
						},
					},
				},
				spd: &apiworkload.ServiceProfileDescriptor{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "sts1",
						OwnerReferences: []metav1.OwnerReference{
							{
								APIVersion: "apps/v1",
								Kind:       "StatefulSet",
								Name:       "sts1",
							},
						},
					},
					Spec: apiworkload.ServiceProfileDescriptorSpec{
						TargetRef: apis.CrossVersionObjectReference{
							Kind:       stsGVK.Kind,
							Name:       "sts1",
							APIVersion: stsGVK.GroupVersion().String(),
						},
						BaselinePercent: pointer.Int32(100),
					},
					Status: apiworkload.ServiceProfileDescriptorStatus{
						AggMetrics: []apiworkload.AggPodMetrics{},
					},
				},
				cnc: &configapi.CustomNodeConfig{
					ObjectMeta: metav1.ObjectMeta{
						Name: "node1",
					},
				},
				enableCNCCache: true,
			},
			wantCNC: &configapi.CustomNodeConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name: "node1",
				},
				Status: configapi.CustomNodeConfigStatus{
					ServiceProfileConfigList: []configapi.TargetConfig{
						{
							ConfigNamespace: "default",
							ConfigName:      "sts1",
							Hash:            "51131be1b092",
						},
					},
				},
			},
		},
		{
			name: "clear cnc spd config when cnc cache disable",
			fields: fields{
				pod: &v1.Pod{
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
					},
					Spec: v1.PodSpec{
						NodeName: "node1",
					},
				},
				workload: &appsv1.StatefulSet{
					TypeMeta: metav1.TypeMeta{
						Kind:       "StatefulSet",
						APIVersion: "apps/v1",
					},
					ObjectMeta: metav1.ObjectMeta{
						Name:      "sts1",
						Namespace: "default",
						Annotations: map[string]string{
							consts.WorkloadAnnotationSPDEnableKey: consts.WorkloadAnnotationSPDEnabled,
						},
					},
					Spec: appsv1.StatefulSetSpec{
						Selector: &metav1.LabelSelector{
							MatchLabels: map[string]string{
								"workload": "sts1",
							},
						},
						Template: v1.PodTemplateSpec{
							ObjectMeta: metav1.ObjectMeta{
								Annotations: map[string]string{
									"katalyst.kubewharf.io/qos_level": "dedicated_cores",
								},
							},
							Spec: v1.PodSpec{},
						},
					},
				},
				spd: &apiworkload.ServiceProfileDescriptor{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "sts1",
						OwnerReferences: []metav1.OwnerReference{
							{
								APIVersion: "apps/v1",
								Kind:       "StatefulSet",
								Name:       "sts1",
							},
						},
					},
					Spec: apiworkload.ServiceProfileDescriptorSpec{
						TargetRef: apis.CrossVersionObjectReference{
							Kind:       stsGVK.Kind,
							Name:       "sts1",
							APIVersion: stsGVK.GroupVersion().String(),
						},
						BaselinePercent: pointer.Int32(100),
					},
					Status: apiworkload.ServiceProfileDescriptorStatus{
						AggMetrics: []apiworkload.AggPodMetrics{},
					},
				},
				cnc: &configapi.CustomNodeConfig{
					ObjectMeta: metav1.ObjectMeta{
						Name: "node1",
					},
					Status: configapi.CustomNodeConfigStatus{
						ServiceProfileConfigList: []configapi.TargetConfig{
							{
								ConfigNamespace: "default",
								ConfigName:      "sts1",
								Hash:            "51131be1b092",
							},
						},
					},
				},
				enableCNCCache: false,
			},
			wantCNC: &configapi.CustomNodeConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name: "node1",
				},
				Status: configapi.CustomNodeConfigStatus{},
			},
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			spdConfig := &controller.SPDConfig{
				EnableCNCCache:         tt.fields.enableCNCCache,
				SPDWorkloadGVResources: []string{"statefulsets.v1.apps"},
			}
			genericConfig := &generic.GenericConfiguration{}
			controllerConf := &controller.GenericControllerConfiguration{
				DynamicGVResources: []string{"statefulsets.v1.apps"},
			}

			ctx := context.TODO()
			controlCtx, err := katalystbase.GenerateFakeGenericContext([]runtime.Object{tt.fields.pod},
				[]runtime.Object{tt.fields.spd, tt.fields.cnc}, []runtime.Object{tt.fields.workload})
			assert.NoError(t, err)

			spdController, err := NewSPDController(ctx, controlCtx, genericConfig, controllerConf,
				spdConfig, generic.NewQoSConfiguration(), struct{}{})
			assert.NoError(t, err)

			controlCtx.StartInformer(ctx)
			go spdController.Run()
			synced := cache.WaitForCacheSync(ctx.Done(), spdController.syncedFunc...)
			assert.True(t, synced)
			time.Sleep(1 * time.Second)

			newCNC, err := controlCtx.Client.InternalClient.ConfigV1alpha1().CustomNodeConfigs().
				Get(ctx, tt.fields.cnc.Name, metav1.GetOptions{})
			assert.NoError(t, err)
			assert.Equal(t, tt.wantCNC, newCNC)
		})
	}
}

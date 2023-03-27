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
	"time"

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
	apiworkload "github.com/kubewharf/katalyst-api/pkg/apis/workload/v1alpha1"
	apiconsts "github.com/kubewharf/katalyst-api/pkg/consts"
	katalystbase "github.com/kubewharf/katalyst-core/cmd/base"
	"github.com/kubewharf/katalyst-core/cmd/katalyst-controller/app/options"
	"github.com/kubewharf/katalyst-core/pkg/config/controller"
	"github.com/kubewharf/katalyst-core/pkg/config/generic"
	"github.com/kubewharf/katalyst-core/pkg/controller/vpa/util"
)

func TestResourceRecommendController_Run(t *testing.T) {
	type fields struct {
		workload *appsv1.StatefulSet
		spd      *apiworkload.ServiceProfileDescriptor
		vpa      *apis.KatalystVerticalPodAutoscaler
		vparec   *apis.VerticalPodAutoscalerRecommendation
	}
	tests := []struct {
		name   string
		fields fields
	}{
		{
			name: "",
			fields: fields{
				vpa: &apis.KatalystVerticalPodAutoscaler{
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
				},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.TODO()
			genericConf := &generic.GenericConfiguration{}
			controllerConf := &controller.GenericControllerConfiguration{
				DynamicGVResources: []string{"statefulsets.v1.apps"},
			}

			fss := &cliflag.NamedFlagSets{}
			vpaOptions := options.NewVPAOptions()
			vpaOptions.AddFlags(fss)
			vpaConf := controller.NewVPAConfig()
			vpaOptions.ApplyTo(vpaConf)

			controlCtx, err := katalystbase.GenerateFakeGenericContext(nil,
				[]runtime.Object{tt.fields.spd, tt.fields.vpa, tt.fields.vparec}, []runtime.Object{tt.fields.workload})
			assert.NoError(t, err)

			rrc, err := NewResourceRecommendController(ctx, controlCtx, genericConf, controllerConf, vpaConf)
			assert.NoError(t, err)

			controlCtx.StartInformer(ctx)
			go rrc.Run()
			synced := cache.WaitForCacheSync(ctx.Done(), rrc.syncedFunc...)
			assert.True(t, synced)
			time.Sleep(1 * time.Second)
		})
	}
}

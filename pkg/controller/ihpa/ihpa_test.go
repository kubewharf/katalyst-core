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

package ihpa

import (
	"context"
	"testing"

	"github.com/kubewharf/katalyst-api/pkg/apis/config/v1alpha1"

	"github.com/stretchr/testify/assert"
	appsv1 "k8s.io/api/apps/v1"
	v2 "k8s.io/api/autoscaling/v2"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/pointer"

	apiautoscaling "github.com/kubewharf/katalyst-api/pkg/apis/autoscaling/v1alpha2"
	apiconsts "github.com/kubewharf/katalyst-api/pkg/consts"
	katalystbase "github.com/kubewharf/katalyst-core/cmd/base"
	"github.com/kubewharf/katalyst-core/pkg/config/controller"
)

func TestResourcePortraitIndicatorPlugin(t *testing.T) {
	t.Parallel()

	type fields struct {
		ihpaConfig *controller.IHPAConfig
		workload   *appsv1.StatefulSet
		ihpa       *apiautoscaling.IntelligentHorizontalPodAutoscaler
	}
	tests := []struct {
		name    string
		fields  fields
		wantNil bool
		wantErr bool
	}{
		{
			name: "normal",
			fields: fields{
				workload: &appsv1.StatefulSet{
					TypeMeta: metav1.TypeMeta{
						Kind:       "StatefulSet",
						APIVersion: "apps/v1",
					},
					ObjectMeta: metav1.ObjectMeta{
						Name:      "sts1",
						Namespace: "default",
						Annotations: map[string]string{
							apiconsts.WorkloadAnnotationSPDEnableKey: apiconsts.WorkloadAnnotationSPDEnabled,
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
				ihpaConfig: &controller.IHPAConfig{},
				ihpa: &apiautoscaling.IntelligentHorizontalPodAutoscaler{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "x",
						Namespace: "x",
					},
					Spec: apiautoscaling.IntelligentHorizontalPodAutoscalerSpec{
						Autoscaler:      apiautoscaling.AutoscalerSpec{},
						ScaleStrategy:   "",
						AlgorithmConfig: v1alpha1.AlgorithmConfig{},
						TimeBounds:      nil,
					},
					Status: apiautoscaling.IntelligentHorizontalPodAutoscalerStatus{
						CurrentMetrics: []v2.MetricStatus{
							{
								Type: v2.ResourceMetricSourceType,
								Resource: &v2.ResourceMetricStatus{
									Name:    "cpu",
									Current: v2.MetricValueStatus{AverageUtilization: pointer.Int32(5)},
								},
							},
							{
								Type: v2.ExternalMetricSourceType,
								External: &v2.ExternalMetricStatus{
									Metric:  v2.MetricIdentifier{},
									Current: v2.MetricValueStatus{},
								},
							},
						},
						CurrentReplicas: 0,
						DesiredReplicas: 0,
					},
				},
			},
			wantNil: false,
			wantErr: false,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			controlCtx, err := katalystbase.GenerateFakeGenericContext([]runtime.Object{}, []runtime.Object{}, []runtime.Object{tt.fields.workload})
			assert.NoError(t, err)

			ctrl, err := NewIHPAController(context.Background(), controlCtx, nil, nil, tt.fields.ihpaConfig, nil, nil)
			if tt.wantNil {
				assert.Nil(t, ctrl)
			} else {
				assert.NotNil(t, ctrl)
				inf := controlCtx.InternalInformerFactory.Autoscaling().V1alpha2().IntelligentHorizontalPodAutoscalers()
				inf.Informer().GetStore().Add(tt.fields.ihpa)
				ctrl.ihpaLister = inf.Lister()
				ctrl.emitMetrics()
			}
			assert.Equal(t, tt.wantErr, err != nil)
		})
	}
}

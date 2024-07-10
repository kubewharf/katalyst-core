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

package resource_portrait

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"

	apiworkload "github.com/kubewharf/katalyst-api/pkg/apis/workload/v1alpha1"
	apiconsts "github.com/kubewharf/katalyst-api/pkg/consts"
	katalystbase "github.com/kubewharf/katalyst-core/cmd/base"
	"github.com/kubewharf/katalyst-core/pkg/config/controller"
	indicatorplugin "github.com/kubewharf/katalyst-core/pkg/controller/spd/indicator-plugin"
	"github.com/kubewharf/katalyst-core/pkg/util/datasource/prometheus"
	"github.com/kubewharf/katalyst-core/pkg/util/native"
)

func TestResourcePortraitIndicatorPlugin(t *testing.T) {
	t.Parallel()

	type fields struct {
		spdConfig *controller.SPDConfig
		workload  *appsv1.StatefulSet
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
				spdConfig: &controller.SPDConfig{
					ReSyncPeriod:           time.Second,
					SPDWorkloadGVResources: []string{"statefulsets.v1.apps"},
					IndicatorPlugins:       []string{"resource-portrait"},
					ResourcePortraitIndicatorPluginConfig: &controller.ResourcePortraitIndicatorPluginConfig{
						DataSource:                               "prom",
						DataSourcePromConfig:                     prometheus.PromConfig{Address: "prometheus:9090"},
						AlgorithmServingAddress:                  "serving:8080",
						AlgorithmConfigMapName:                   "resource-portrait-auto-created-config",
						AlgorithmConfigMapNamespace:              "kube-system",
						EnableAutomaticResyncGlobalConfiguration: true,
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

			spdWorkloadInformer := map[schema.GroupVersionResource]native.DynamicInformer{}
			workloadInformers := controlCtx.DynamicResourcesManager.GetDynamicInformers()
			assert.Equal(t, len(workloadInformers), 1)
			for _, workload := range tt.fields.spdConfig.SPDWorkloadGVResources {
				wf, ok := workloadInformers[workload]
				assert.True(t, ok)
				spdWorkloadInformer[wf.GVR] = wf
			}
			indicatorManager := indicatorplugin.NewIndicatorManager()

			plugin, err := ResourcePortraitIndicatorPluginInitFunc(context.Background(), tt.fields.spdConfig, nil, spdWorkloadInformer, controlCtx, indicatorManager)
			assert.Equal(t, tt.wantNil, plugin == nil)
			assert.Equal(t, tt.wantErr, err != nil)

			assert.Equal(t, plugin.Name(), ResourcePortraitPluginName)
			assert.Equal(t, plugin.GetSupportedExtendedIndicatorSpec(), []string{ResourcePortraitExtendedSpecName})

			var serviceBusinessIndicatorNames []apiworkload.ServiceBusinessIndicatorName
			assert.Equal(t, plugin.GetSupportedBusinessIndicatorSpec(), serviceBusinessIndicatorNames)
			var systemIndicatorSpec []apiworkload.ServiceSystemIndicatorName
			assert.Equal(t, plugin.GetSupportedSystemIndicatorSpec(), systemIndicatorSpec)
			var businessIndicatorStatus []apiworkload.ServiceBusinessIndicatorName
			assert.Equal(t, plugin.GetSupportedBusinessIndicatorStatus(), businessIndicatorStatus)
		})
	}
}

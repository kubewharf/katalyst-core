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
	"testing"

	"github.com/stretchr/testify/assert"
	v2 "k8s.io/api/autoscaling/v2"

	"github.com/kubewharf/katalyst-api/pkg/apis/autoscaling/v1alpha2"
	"github.com/kubewharf/katalyst-api/pkg/apis/config/v1alpha1"
	ihpacontroller "github.com/kubewharf/katalyst-core/pkg/controller/ihpa"
	resourceportrait "github.com/kubewharf/katalyst-core/pkg/controller/spd/indicator-plugin/plugins/resource-portrait"
)

func Test_initAlgorithmConfig(t *testing.T) {
	t.Parallel()
	t.Run("normal", func(t *testing.T) {
		t.Parallel()
		expected := v1alpha1.AlgorithmConfig{
			Method: resourceportrait.ResourcePortraitMethodPredict,
			Params: nil,
			TimeWindow: v1alpha1.TimeWindow{
				Input:           defaultAlgorithmConfigTimeWindowInput,
				HistorySteps:    defaultAlgorithmConfigTimeWindowHistorySteps,
				Aggregator:      v1alpha1.Avg,
				Output:          defaultAlgorithmConfigTimeWindowOutput,
				PredictionSteps: defaultAlgorithmConfigTimeWindowPredictionSteps,
			},
			ResyncPeriod: defaultAlgorithmConfigResyncPeriod,
		}
		conf := v1alpha1.AlgorithmConfig{}
		conf = initAlgorithmConfig(conf)
		assert.Equal(t, expected, conf)
	})
}

func Test_generateResourcePortraitConfig(t *testing.T) {
	t.Parallel()
	t.Run("normal", func(t *testing.T) {
		t.Parallel()
		expected := v1alpha1.ResourcePortraitConfig{
			Source: ihpacontroller.IHPAControllerName,
			AlgorithmConfig: v1alpha1.AlgorithmConfig{
				Method: resourceportrait.ResourcePortraitMethodPredict,
				TimeWindow: v1alpha1.TimeWindow{
					Input:           defaultAlgorithmConfigTimeWindowInput,
					HistorySteps:    defaultAlgorithmConfigTimeWindowHistorySteps,
					Aggregator:      v1alpha1.Avg,
					Output:          defaultAlgorithmConfigTimeWindowOutput,
					PredictionSteps: defaultAlgorithmConfigTimeWindowPredictionSteps,
				},
				ResyncPeriod: defaultAlgorithmConfigResyncPeriod,
			},
			Metrics:       []string{"cpu_utilization_user_seconds"},
			CustomMetrics: map[string]string{"test": "test"},
		}
		ihpa := v1alpha2.IntelligentHorizontalPodAutoscaler{
			Spec: v1alpha2.IntelligentHorizontalPodAutoscalerSpec{
				Autoscaler: v1alpha2.AutoscalerSpec{
					Metrics: []v1alpha2.MetricSpec{
						{
							Metric: &v2.MetricSpec{},
						},
						{
							CustomMetric: &v1alpha2.CustomMetricSpec{
								Identify: "test",
								Query:    "test",
							},
						},
						{
							CustomMetric: &v1alpha2.CustomMetricSpec{
								Identify: "cpu_utilization_user_seconds",
							},
						},
					},
				},
			},
		}
		conf := generateResourcePortraitConfig(&ihpa)
		assert.Equal(t, &expected, conf)
	})
}

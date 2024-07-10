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
	"github.com/kubewharf/katalyst-api/pkg/apis/autoscaling/v1alpha2"
	apiconfig "github.com/kubewharf/katalyst-api/pkg/apis/config/v1alpha1"
	ihpacontroller "github.com/kubewharf/katalyst-core/pkg/controller/ihpa"
	resourceportrait "github.com/kubewharf/katalyst-core/pkg/controller/spd/indicator-plugin/plugins/resource-portrait"
)

const (
	defaultAlgorithmConfigResyncPeriod              = 3600
	defaultAlgorithmConfigTimeWindowInput           = 60
	defaultAlgorithmConfigTimeWindowHistorySteps    = 7 * 24 * 60
	defaultAlgorithmConfigTimeWindowOutput          = 1
	defaultAlgorithmConfigTimeWindowPredictionSteps = 2 * 60
)

func generateResourcePortraitConfig(ihpa *v1alpha2.IntelligentHorizontalPodAutoscaler) *apiconfig.ResourcePortraitConfig {
	return &apiconfig.ResourcePortraitConfig{
		Source:          ihpacontroller.IHPAControllerName,
		AlgorithmConfig: initAlgorithmConfig(ihpa.Spec.AlgorithmConfig),
		Metrics:         getResourcePortraitMetricsFromIHPA(ihpa),
		CustomMetrics:   getResourcePortraitCustomMetricsFromIHPA(ihpa),
	}
}

func initAlgorithmConfig(algoConf apiconfig.AlgorithmConfig) apiconfig.AlgorithmConfig {
	if algoConf.Method == "" {
		algoConf.Method = resourceportrait.ResourcePortraitMethodPredict
	}
	if algoConf.ResyncPeriod == 0 {
		algoConf.ResyncPeriod = defaultAlgorithmConfigResyncPeriod
	}
	if algoConf.TimeWindow.Input == 0 {
		algoConf.TimeWindow.Input = defaultAlgorithmConfigTimeWindowInput
	}
	if algoConf.TimeWindow.HistorySteps == 0 {
		algoConf.TimeWindow.HistorySteps = defaultAlgorithmConfigTimeWindowHistorySteps
	}
	if algoConf.TimeWindow.Aggregator == "" {
		algoConf.TimeWindow.Aggregator = apiconfig.Avg
	}
	if algoConf.TimeWindow.Output == 0 {
		algoConf.TimeWindow.Output = defaultAlgorithmConfigTimeWindowOutput
	}
	if algoConf.TimeWindow.PredictionSteps == 0 {
		algoConf.TimeWindow.PredictionSteps = defaultAlgorithmConfigTimeWindowPredictionSteps
	}
	return algoConf
}

func getResourcePortraitMetricsFromIHPA(ihpa *v1alpha2.IntelligentHorizontalPodAutoscaler) []string {
	var metrics []string
	for _, metric := range ihpa.Spec.Autoscaler.Metrics {
		if metric.Metric != nil || metric.CustomMetric == nil {
			continue
		}
		if _, ok := resourceportrait.PresetWorkloadMetricQueryMapping[string(metric.CustomMetric.Identify)]; ok {
			metrics = append(metrics, string(metric.CustomMetric.Identify))
		}
	}
	return metrics
}

func getResourcePortraitCustomMetricsFromIHPA(ihpa *v1alpha2.IntelligentHorizontalPodAutoscaler) map[string]string {
	metrics := make(map[string]string)
	for _, metric := range ihpa.Spec.Autoscaler.Metrics {
		if metric.Metric != nil || metric.CustomMetric == nil {
			continue
		}
		if _, ok := resourceportrait.PresetWorkloadMetricQueryMapping[string(metric.CustomMetric.Identify)]; ok {
			continue
		}
		metrics[string(metric.CustomMetric.Identify)] = metric.CustomMetric.Query
	}
	return metrics
}

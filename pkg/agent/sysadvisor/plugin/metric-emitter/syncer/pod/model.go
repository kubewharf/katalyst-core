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

package pod

import (
	"context"
	"fmt"

	"k8s.io/klog/v2"

	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/inference/modelresultfetcher/borwein/latencyregression"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/inference/modelresultfetcher/borwein/trainingtpreg"
	borweinconsts "github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/inference/models/borwein/consts"
	borweininfsvc "github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/inference/models/borwein/inferencesvc"
	borweintypes "github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/inference/models/borwein/types"
	borweinutils "github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/inference/models/borwein/utils"
	"github.com/kubewharf/katalyst-core/pkg/custom-metric/store/data"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

var modelOutputToEmit []string = []string{
	borweinconsts.ModelNameBorwein,
}

// modelMetric emit pod_level model inference to kcmas.
func (p *MetricSyncerPod) modelMetric() {
	for _, modelName := range modelOutputToEmit {
		results, err := p.metaReader.GetInferenceResult(borweinutils.GetInferenceResultKey(modelName))
		if err != nil {
			klog.Errorf("failed to get inference results of model(%s)", modelName)
			continue
		}

		switch typedResults := results.(type) {
		case *borweintypes.BorweinInferenceResults:
			typedResults.RangeInferenceResults(func(podUID, containerName string, result *borweininfsvc.InferenceResult) {
				if result == nil || result.IsDefault {
					return
				}

				pod, err := p.metaServer.GetPod(context.Background(), podUID)
				if err != nil || !p.metricPod(pod) {
					return
				}

				tags := p.generateMetricTag(pod)
				// There are some cases indicate "valid_break_line":
				// 1. result.InferenceType == borweininfsvc.InferenceType_ClassificationOverload &&
				//		result.Output >= result.Percentile.
				// 2. ...
				// "Is_Default" cases have been filtered above.
				validBreakLine := (result.InferenceType == borweininfsvc.InferenceType_ClassificationOverload &&
					result.Output >= result.Percentile)

				_ = p.dataEmitter.StoreFloat64(podModelInferenceResultBorwein,
					float64(result.Output),
					metrics.MetricTypeNameRaw,
					append(tags,
						metrics.MetricTag{
							Key: fmt.Sprintf("%s", data.CustomMetricLabelKeyTimestamp),
							Val: fmt.Sprintf("%v", typedResults.Timestamp),
						},
						metrics.MetricTag{
							Key: fmt.Sprintf("%s%s", data.CustomMetricLabelSelectorPrefixKey, "inference_type"),
							Val: result.InferenceType.String(),
						},
						metrics.MetricTag{
							Key: fmt.Sprintf("%s%s", data.CustomMetricLabelSelectorPrefixKey, "valid_break_line"),
							Val: fmt.Sprintf("%v", validBreakLine),
						},
						metrics.MetricTag{
							Key: fmt.Sprintf("%s%s", data.CustomMetricLabelSelectorPrefixKey, "model_version"),
							Val: fmt.Sprintf("%v", result.ModelVersion),
						},
					)...)
			})

		default:
			klog.Warningf("invalid model result type: %T", typedResults)
		}
	}

	for modelName, customizedEmitterFunc := range p.modelToCustomizedEmitterFunc {
		general.Infof("calling customized emitter func for model: %s", modelName)
		customizedEmitterFunc()
	}

	general.InfofV(4, "get model metric for pod")
}

func (p *MetricSyncerPod) emitBorweinTrainingThroughput() {
	trainingThroughputData, resultTimestamp, err := trainingtpreg.GetTrainingTHRegPredictValue(p.metaReader)
	if err != nil {
		klog.Errorf("failed to get inference results of model(%s)", borweinconsts.ModelNameBorweinTrainingThroughput)
		return
	}

	for podUID, containerData := range trainingThroughputData {
		pod, err := p.metaServer.GetPod(context.Background(), podUID)
		if err != nil || !p.metricPod(pod) {
			return
		}

		tags := p.generateMetricTag(pod)

		for containerName, trainingThroughput := range containerData {
			_ = p.dataEmitter.StoreFloat64(podTrainingThroughputInferenceResultBorwein,
				trainingThroughput,
				metrics.MetricTypeNameRaw,
				append(tags,
					metrics.MetricTag{
						Key: fmt.Sprintf("%s", data.CustomMetricLabelKeyTimestamp),
						Val: fmt.Sprintf("%v", resultTimestamp),
					},
					metrics.MetricTag{
						Key: fmt.Sprintf("%scontainer", data.CustomMetricLabelSelectorPrefixKey),
						Val: containerName,
					},
				)...)
		}
	}
}

func (p *MetricSyncerPod) emitBorweinLatencyRegression() {
	latencyRegressionData, resultTimestamp, err := latencyregression.GetLatencyRegressionPredictResult(p.metaReader)
	if err != nil {
		klog.Errorf("failed to get inference results of model(%s) error: %v\n", borweinconsts.ModelNameBorweinLatencyRegression, err)
		return
	}

	klog.Infof("Start to emit pod latency regression result")

	predictSum := 0.0
	containerCnt := 0.0
	nodeName := ""

	for podUID, containerData := range latencyRegressionData {
		pod, err := p.metaServer.GetPod(context.Background(), podUID)
		if err != nil || !p.metricPod(pod) {
			return
		}

		if nodeName == "" {
			nodeName = pod.Spec.NodeName
		}

		tags := p.generateMetricTag(pod)

		for containerName, latencyRegression := range containerData {
			predictSum += latencyRegression.PredictValue
			containerCnt += 1

			klog.Infof("Emit latency regression result, pod %v, container %v, predict value %v",
				podUID, containerName, latencyRegression.PredictValue)
			_ = p.dataEmitter.StoreFloat64(podLatencyRegressionInferenceResultBorwein,
				latencyRegression.PredictValue,
				metrics.MetricTypeNameRaw,
				append(tags,
					metrics.MetricTag{
						Key: fmt.Sprintf("%s", data.CustomMetricLabelKeyTimestamp),
						Val: fmt.Sprintf("%v", resultTimestamp),
					},
					metrics.MetricTag{
						Key: fmt.Sprintf("%scontainer", data.CustomMetricLabelSelectorPrefixKey),
						Val: containerName,
					},
				)...)
		}
	}

	if containerCnt == 0 {
		klog.Errorf("Found no valid containers, emit node-level latency regression result error")
		return
	}

	predictAvg := predictSum / containerCnt

	klog.Infof("Emit node-level latency regression result, node %v, predict value %v", nodeName, predictAvg)
	_ = p.dataEmitter.StoreFloat64(nodeLatencyRegressionInferenceResultBorwein,
		predictAvg,
		metrics.MetricTypeNameRaw,
		metrics.MetricTag{
			Key: fmt.Sprintf("%s", data.CustomMetricLabelKeyTimestamp),
			Val: fmt.Sprintf("%v", resultTimestamp),
		},
		metrics.MetricTag{
			Key: fmt.Sprintf("%s", podMetricLabelSelectorNodeName),
			Val: nodeName,
		})
	_ = p.metricEmitter.StoreFloat64(metricBorweinInferenceResult,
		predictAvg,
		metrics.MetricTypeNameRaw,
		metrics.MetricTag{
			Key: fmt.Sprintf("%s", "model_name"),
			Val: fmt.Sprintf("%v", borweinconsts.ModelNameBorweinLatencyRegression),
		},
	)
}

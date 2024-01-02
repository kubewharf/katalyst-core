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

package helper

import (
	"strconv"

	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric/types"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

func GetContainerMetric(metricsFetcher types.MetricsFetcher, emitter metrics.MetricEmitter, podUID, containerName, metricName string, numaID int) (float64, error) {
	if numaID >= 0 {
		data, err := metricsFetcher.GetContainerNumaMetric(podUID, containerName, strconv.Itoa(numaID), metricName)
		if err != nil {
			general.Errorf(errMsgGetContainerNumaMetrics, metricName, podUID, containerName, numaID, err)
			return 0, err
		}
		containerMetricValue := data.Value
		_ = emitter.StoreFloat64(metricsNameContainerMetric, containerMetricValue, metrics.MetricTypeNameRaw,
			metrics.ConvertMapToTags(map[string]string{
				metricsTagKeyPodUID:        podUID,
				metricsTagKeyContainerName: containerName,
				metricsTagKeyNumaID:        strconv.Itoa(numaID),
				metricsTagKeyMetricName:    metricName,
			})...)
		return containerMetricValue, nil
	}
	data, err := metricsFetcher.GetContainerMetric(podUID, containerName, metricName)
	if err != nil {
		general.Errorf(errMsgGetContainerSystemMetrics, metricName, podUID, containerName, err)
		return 0, err
	}
	containerMetricValue := data.Value
	_ = emitter.StoreFloat64(metricsNameContainerMetric, containerMetricValue, metrics.MetricTypeNameRaw,
		metrics.ConvertMapToTags(map[string]string{
			metricsTagKeyPodUID:        podUID,
			metricsTagKeyContainerName: containerName,
			metricsTagKeyNumaID:        strconv.Itoa(numaID),
			metricsTagKeyMetricName:    metricName,
		})...)
	return containerMetricValue, nil
}

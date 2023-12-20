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
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric/types"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	metricutil "github.com/kubewharf/katalyst-core/pkg/util/metric"
)

func GetDeviceMetricWithTime(metricsFetcher types.MetricsFetcher, emitter metrics.MetricEmitter, metricName string, devName string) (metricutil.MetricData, error) {
	metricData, err := metricsFetcher.GetDeviceMetric(devName, metricName)
	if err != nil {
		general.Errorf(errMsgGetDeviceMetrics, metricName, devName, err)
		return metricutil.MetricData{}, err
	}
	_ = emitter.StoreFloat64(metricsNameDeviceMetric, metricData.Value, metrics.MetricTypeNameRaw,
		metrics.ConvertMapToTags(map[string]string{
			metricsTagKeyDeviceName: devName,
			metricsTagKeyMetricName: metricName,
		})...)
	return metricData, nil
}

func GetDeviceMetric(metricsFetcher types.MetricsFetcher, emitter metrics.MetricEmitter, metricName string, devName string) (float64, error) {
	metricWithTime, err := GetDeviceMetricWithTime(metricsFetcher, emitter, metricName, devName)
	if err != nil {
		return 0, err
	}
	return metricWithTime.Value, err
}

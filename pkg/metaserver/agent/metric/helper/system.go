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
	"fmt"
	"strconv"

	"github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	metricutil "github.com/kubewharf/katalyst-core/pkg/util/metric"
)

// GetWatermarkMetrics returns system-water mark related metrics (config)
// if numa node is specified, return config in this numa; otherwise return system-level config
func GetWatermarkMetrics(metricsFetcher metric.MetricsFetcher, emitter metrics.MetricEmitter, numaID int) (free, total, scaleFactor float64, err error) {
	if numaID >= 0 {
		data, err := metricsFetcher.GetNumaMetric(numaID, consts.MetricMemFreeNuma)
		if err != nil {
			return 0, 0, 0, fmt.Errorf(errMsgGetNumaMetrics, consts.MetricMemFreeNuma, numaID, err)
		}
		free = data.Value
		_ = emitter.StoreFloat64(metricsNameNumaMetric, free, metrics.MetricTypeNameRaw,
			metrics.ConvertMapToTags(map[string]string{
				metricsTagKeyNumaID:     strconv.Itoa(numaID),
				metricsTagKeyMetricName: consts.MetricMemFreeNuma,
			})...)

		data, err = metricsFetcher.GetNumaMetric(numaID, consts.MetricMemTotalNuma)
		if err != nil {
			return 0, 0, 0, fmt.Errorf(errMsgGetNumaMetrics, consts.MetricMemTotalNuma, numaID, err)
		}
		total = data.Value
		_ = emitter.StoreFloat64(metricsNameNumaMetric, total, metrics.MetricTypeNameRaw,
			metrics.ConvertMapToTags(map[string]string{
				metricsTagKeyNumaID:     strconv.Itoa(numaID),
				metricsTagKeyMetricName: consts.MetricMemTotalNuma,
			})...)
	} else {
		data, err := metricsFetcher.GetNodeMetric(consts.MetricMemFreeSystem)
		if err != nil {
			return 0, 0, 0, fmt.Errorf(errMsgGetSystemMetrics, consts.MetricMemFreeSystem, err)
		}
		free = data.Value
		_ = emitter.StoreFloat64(metricsNameSystemMetric, free, metrics.MetricTypeNameRaw,
			metrics.ConvertMapToTags(map[string]string{
				metricsTagKeyMetricName: consts.MetricMemFreeSystem,
			})...)

		data, err = metricsFetcher.GetNodeMetric(consts.MetricMemTotalSystem)
		if err != nil {
			return 0, 0, 0, fmt.Errorf(errMsgGetSystemMetrics, consts.MetricMemTotalSystem, err)
		}
		total = data.Value
		_ = emitter.StoreFloat64(metricsNameSystemMetric, total, metrics.MetricTypeNameRaw,
			metrics.ConvertMapToTags(map[string]string{
				metricsTagKeyMetricName: consts.MetricMemTotalSystem,
			})...)
	}

	data, err := metricsFetcher.GetNodeMetric(consts.MetricMemScaleFactorSystem)
	if err != nil {
		return 0, 0, 0, fmt.Errorf(errMsgGetSystemMetrics, consts.MetricMemScaleFactorSystem, err)
	}
	scaleFactor = data.Value
	_ = emitter.StoreFloat64(metricsNameSystemMetric, scaleFactor, metrics.MetricTypeNameRaw,
		metrics.ConvertMapToTags(map[string]string{
			metricsTagKeyMetricName: consts.MetricMemScaleFactorSystem,
		})...)

	return free, total, scaleFactor, nil
}

func GetSystemKswapdStealMetrics(metricsFetcher metric.MetricsFetcher, emitter metrics.MetricEmitter) (metricutil.MetricData, error) {
	kswapdSteal, err := metricsFetcher.GetNodeMetric(consts.MetricMemKswapdstealSystem)
	if err != nil {
		return metricutil.MetricData{}, fmt.Errorf(errMsgGetSystemMetrics, "mem.kswapdsteal.system", err)
	}
	_ = emitter.StoreFloat64(metricsNameSystemMetric, kswapdSteal.Value, metrics.MetricTypeNameRaw,
		metrics.ConvertMapToTags(map[string]string{
			metricsTagKeyMetricName: consts.MetricMemKswapdstealSystem,
		})...)

	return kswapdSteal, nil
}

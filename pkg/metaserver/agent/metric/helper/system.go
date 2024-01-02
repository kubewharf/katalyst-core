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
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric/types"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	metricutil "github.com/kubewharf/katalyst-core/pkg/util/metric"
)

// GetWatermarkMetrics returns system-water mark related metrics (config)
// if numa node is specified, return config in this numa; otherwise return system-level config
func GetWatermarkMetrics(metricsFetcher types.MetricsFetcher, emitter metrics.MetricEmitter, numaID int) (free, total, scaleFactor float64, err error) {
	if numaID >= 0 {
		free, err = GetNumaMetric(metricsFetcher, emitter, consts.MetricMemFreeNuma, numaID)
		if err != nil {
			return 0, 0, 0, fmt.Errorf(errMsgGetNumaMetrics, consts.MetricMemFreeNuma, numaID, err)
		}
		total, err = GetNumaMetric(metricsFetcher, emitter, consts.MetricMemTotalNuma, numaID)
		if err != nil {
			return 0, 0, 0, fmt.Errorf(errMsgGetNumaMetrics, consts.MetricMemFreeNuma, numaID, err)
		}
	} else {
		free, err = GetNodeMetric(metricsFetcher, emitter, consts.MetricMemFreeSystem)
		if err != nil {
			return 0, 0, 0, fmt.Errorf(errMsgGetSystemMetrics, consts.MetricMemFreeSystem, err)
		}
		total, err = GetNodeMetric(metricsFetcher, emitter, consts.MetricMemTotalSystem)
		if err != nil {
			return 0, 0, 0, fmt.Errorf(errMsgGetSystemMetrics, consts.MetricMemTotalSystem, err)
		}
	}

	scaleFactor, err = GetNodeMetric(metricsFetcher, emitter, consts.MetricMemScaleFactorSystem)
	if err != nil {
		return 0, 0, 0, fmt.Errorf(errMsgGetSystemMetrics, consts.MetricMemScaleFactorSystem, err)
	}

	return free, total, scaleFactor, nil
}

func GetNodeMetricWithTime(metricsFetcher types.MetricsFetcher, emitter metrics.MetricEmitter, metricName string) (metricutil.MetricData, error) {
	metricData, err := metricsFetcher.GetNodeMetric(metricName)
	if err != nil {
		return metricutil.MetricData{}, fmt.Errorf(errMsgGetSystemMetrics, metricName, err)
	}
	_ = emitter.StoreFloat64(metricsNameSystemMetric, metricData.Value, metrics.MetricTypeNameRaw,
		metrics.ConvertMapToTags(map[string]string{
			metricsTagKeyMetricName: metricName,
		})...)
	return metricData, nil
}

func GetNodeMetric(metricsFetcher types.MetricsFetcher, emitter metrics.MetricEmitter, metricName string) (float64, error) {
	metricWithTime, err := GetNodeMetricWithTime(metricsFetcher, emitter, metricName)
	if err != nil {
		return 0, err
	}
	return metricWithTime.Value, err
}

func GetNumaMetricWithTime(metricsFetcher types.MetricsFetcher, emitter metrics.MetricEmitter, metricName string, numaID int) (metricutil.MetricData, error) {
	metricData, err := metricsFetcher.GetNumaMetric(numaID, metricName)
	if err != nil {
		general.Errorf(errMsgGetNumaMetrics, metricName, numaID, err)
		return metricutil.MetricData{}, err
	}
	_ = emitter.StoreFloat64(metricsNameNumaMetric, metricData.Value, metrics.MetricTypeNameRaw,
		metrics.ConvertMapToTags(map[string]string{
			metricsTagKeyNumaID:     strconv.Itoa(numaID),
			metricsTagKeyMetricName: metricName,
		})...)
	return metricData, nil
}

func GetNumaMetric(metricsFetcher types.MetricsFetcher, emitter metrics.MetricEmitter, metricName string, numaID int) (float64, error) {
	metricWithTime, err := GetNumaMetricWithTime(metricsFetcher, emitter, metricName, numaID)
	if err != nil {
		return 0, err
	}
	return metricWithTime.Value, err
}

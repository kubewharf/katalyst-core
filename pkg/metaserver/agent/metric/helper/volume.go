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
)

func GetVolumeMetric(metricsFetcher types.MetricsFetcher, emitter metrics.MetricEmitter, podUID, volumeName, metricName string) (float64, error) {
	metricData, err := metricsFetcher.GetPodVolumeMetric(podUID, volumeName, metricName)
	if err != nil {
		general.Errorf(errMsgGetPodVolumeMetrics, podUID, volumeName, metricName, err)
		return 0, err
	}
	_ = emitter.StoreFloat64(metricsNamePodVolumeMetric, metricData.Value, metrics.MetricTypeNameRaw,
		metrics.ConvertMapToTags(map[string]string{
			metricsTagKeyPodUID:        podUID,
			metricsTagKeyPodVolumeName: volumeName,
			metricsTagKeyMetricName:    metricName,
		})...)
	return metricData.Value, nil
}

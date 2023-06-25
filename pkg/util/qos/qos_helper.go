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

package qos

import (
	"fmt"
	"strconv"

	v1 "k8s.io/api/core/v1"

	"github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/metric"
)

const (
	errMsgGetSystemMetrics          = "failed to get system metric, metric name: %s, err: %v"
	errMsgGetNumaMetrics            = "failed to get numa metric, metric name: %s, numa id: %d, err: %v"
	errMsgGetContainerNumaMetrics   = "failed to get container numa metric, metric name: %s, pod uid: %s, container name: %s, numa id: %d, err: %v"
	errMsgGetContainerSystemMetrics = "failed to get container system metric, metric name: %s, pod uid: %s, container name: %s, err: %v"
	errMsgCheckReclaimedPodFailed   = "failed to check reclaimed pod, pod: %s/%s, err: %v"
)

const (
	MetricsNameFetchMetricError = "fetch_metric_error_count"
	MetricsNameNumaMetric       = "numa_metric_raw"
	MetricsNameSystemMetric     = "system_metric_raw"
	MetricsNameContainerMetric  = "container_metric_raw"
	MetricsNamePodMetric        = "pod_metric_raw"

	MetricsTagKeyNumaID        = "numa_id"
	MetricsTagKeyAction        = "action"
	MetricsTagKeyMetricName    = "metric_name"
	MetricsTagKeyPodUID        = "pod_uid"
	MetricsTagKeyContainerName = "container_name"
)

type QosHelper struct {
	emitter    metrics.MetricEmitter
	metaServer *metaserver.MetaServer
}

func NewQosHelper(emitter metrics.MetricEmitter, metaServer *metaserver.MetaServer) *QosHelper {
	return &QosHelper{
		emitter:    emitter,
		metaServer: metaServer,
	}
}

// GetWatermarkMetrics returns system-water mark related metrics (config)
// if numa node is specified, return config in this numa; otherwise return system-level config
func (e *QosHelper) GetWatermarkMetrics(numaID int) (free, total, scaleFactor float64, err error) {
	if numaID >= 0 {
		data, err := e.metaServer.GetNumaMetric(numaID, consts.MetricMemFreeNuma)
		if err != nil {
			return 0, 0, 0, fmt.Errorf(errMsgGetNumaMetrics, consts.MetricMemFreeNuma, numaID, err)
		}
		free = data.Value
		_ = e.emitter.StoreFloat64(MetricsNameNumaMetric, free, metrics.MetricTypeNameRaw,
			metrics.ConvertMapToTags(map[string]string{
				MetricsTagKeyNumaID:     strconv.Itoa(numaID),
				MetricsTagKeyMetricName: consts.MetricMemFreeNuma,
			})...)

		data, err = e.metaServer.GetNumaMetric(numaID, consts.MetricMemTotalNuma)
		if err != nil {
			return 0, 0, 0, fmt.Errorf(errMsgGetNumaMetrics, consts.MetricMemTotalNuma, numaID, err)
		}
		total = data.Value
		_ = e.emitter.StoreFloat64(MetricsNameNumaMetric, total, metrics.MetricTypeNameRaw,
			metrics.ConvertMapToTags(map[string]string{
				MetricsTagKeyNumaID:     strconv.Itoa(numaID),
				MetricsTagKeyMetricName: consts.MetricMemTotalNuma,
			})...)
	} else {
		data, err := e.metaServer.GetNodeMetric(consts.MetricMemFreeSystem)
		if err != nil {
			return 0, 0, 0, fmt.Errorf(errMsgGetSystemMetrics, consts.MetricMemFreeSystem, err)
		}
		free = data.Value
		_ = e.emitter.StoreFloat64(MetricsNameSystemMetric, free, metrics.MetricTypeNameRaw,
			metrics.ConvertMapToTags(map[string]string{
				MetricsTagKeyMetricName: consts.MetricMemFreeSystem,
			})...)

		data, err = e.metaServer.GetNodeMetric(consts.MetricMemTotalSystem)
		if err != nil {
			return 0, 0, 0, fmt.Errorf(errMsgGetSystemMetrics, consts.MetricMemTotalSystem, err)
		}
		total = data.Value
		_ = e.emitter.StoreFloat64(MetricsNameSystemMetric, total, metrics.MetricTypeNameRaw,
			metrics.ConvertMapToTags(map[string]string{
				MetricsTagKeyMetricName: consts.MetricMemTotalSystem,
			})...)
	}

	data, err := e.metaServer.GetNodeMetric(consts.MetricMemScaleFactorSystem)
	if err != nil {
		return 0, 0, 0, fmt.Errorf(errMsgGetSystemMetrics, consts.MetricMemScaleFactorSystem, err)
	}
	scaleFactor = data.Value
	_ = e.emitter.StoreFloat64(MetricsNameSystemMetric, scaleFactor, metrics.MetricTypeNameRaw,
		metrics.ConvertMapToTags(map[string]string{
			MetricsTagKeyMetricName: consts.MetricMemScaleFactorSystem,
		})...)

	return free, total, scaleFactor, nil
}

func (e *QosHelper) GetSystemKswapdStealMetrics() (metric.MetricData, error) {
	kswapdSteal, err := e.metaServer.GetNodeMetric(consts.MetricMemKswapdstealSystem)
	if err != nil {
		return metric.MetricData{}, fmt.Errorf(errMsgGetSystemMetrics, "mem.kswapdsteal.system", err)
	}
	_ = e.emitter.StoreFloat64(MetricsNameSystemMetric, kswapdSteal.Value, metrics.MetricTypeNameRaw,
		metrics.ConvertMapToTags(map[string]string{
			MetricsTagKeyMetricName: consts.MetricMemKswapdstealSystem,
		})...)

	return kswapdSteal, nil
}

func (e *QosHelper) GetContainerMetric(podUID, containerName, metricName string, numaID int) (float64, error) {
	if numaID >= 0 {
		data, err := e.metaServer.GetContainerNumaMetric(podUID, containerName, strconv.Itoa(numaID), metricName)
		if err != nil {
			general.Errorf(errMsgGetContainerNumaMetrics, metricName, podUID, containerName, numaID, err)
			return 0, err
		}
		containerMetricValue := data.Value
		_ = e.emitter.StoreFloat64(MetricsNameContainerMetric, containerMetricValue, metrics.MetricTypeNameRaw,
			metrics.ConvertMapToTags(map[string]string{
				MetricsTagKeyPodUID:        podUID,
				MetricsTagKeyContainerName: containerName,
				MetricsTagKeyNumaID:        strconv.Itoa(numaID),
				MetricsTagKeyMetricName:    metricName,
			})...)
		return containerMetricValue, nil
	}
	data, err := e.metaServer.GetContainerMetric(podUID, containerName, metricName)
	if err != nil {
		general.Errorf(errMsgGetContainerSystemMetrics, metricName, podUID, containerName, err)
		return 0, err
	}
	containerMetricValue := data.Value
	_ = e.emitter.StoreFloat64(MetricsNameContainerMetric, containerMetricValue, metrics.MetricTypeNameRaw,
		metrics.ConvertMapToTags(map[string]string{
			MetricsTagKeyPodUID:        podUID,
			MetricsTagKeyContainerName: containerName,
			MetricsTagKeyNumaID:        strconv.Itoa(numaID),
			MetricsTagKeyMetricName:    metricName,
		})...)
	return containerMetricValue, nil
}

// GetPodMetric returns the value of a pod-level metric.
// And the value of a pod-level metric is calculated by summing the metric values for all containers in that pod.
func (e *QosHelper) GetPodMetric(pod *v1.Pod, metricName string, numaID int) (float64, bool) {
	if pod == nil {
		return 0, false
	}

	var podMetricValue float64
	for _, container := range pod.Spec.Containers {
		containerMetricValue, err := e.GetContainerMetric(string(pod.UID), container.Name, metricName, numaID)
		if err != nil {
			return 0, false
		}

		podMetricValue += containerMetricValue
	}

	_ = e.emitter.StoreFloat64(MetricsNamePodMetric, podMetricValue, metrics.MetricTypeNameRaw,
		metrics.ConvertMapToTags(map[string]string{
			MetricsTagKeyPodUID:     string(pod.UID),
			MetricsTagKeyNumaID:     strconv.Itoa(numaID),
			MetricsTagKeyMetricName: metricName,
		})...)

	return podMetricValue, true
}

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

const (
	metricsNameNumaMetric      = "numa_metric_raw"
	metricsNameSystemMetric    = "system_metric_raw"
	metricsNameContainerMetric = "container_metric_raw"
	metricsNamePodMetric       = "pod_metric_raw"
	metricsNamePodVolumeMetric = "pod_volume_metric_raw"

	metricsTagKeyNumaID        = "numa_id"
	metricsTagKeyMetricName    = "metric_name"
	metricsTagKeyPodUID        = "pod_uid"
	metricsTagKeyContainerName = "container_name"
	metricsTagKeyPodVolumeName = "pod_volume_name"
)

const (
	errMsgGetSystemMetrics          = "failed to get system metric, metric name: %s, err: %v"
	errMsgGetNumaMetrics            = "failed to get numa metric, metric name: %s, numa id: %d, err: %v"
	errMsgGetContainerNumaMetrics   = "failed to get container numa metric, metric name: %s, pod uid: %s, container name: %s, numa id: %d, err: %v"
	errMsgGetContainerSystemMetrics = "failed to get container system metric, metric name: %s, pod uid: %s, container name: %s, err: %v"
	errMsgGetPodVolumeMetrics       = "failed to get pod volume metric, pod uid: %s, volume name: %s, metric name: %s, err: %v"
)

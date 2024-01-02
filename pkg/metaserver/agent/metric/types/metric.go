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

package types

import (
	"context"

	v1 "k8s.io/api/core/v1"

	"github.com/kubewharf/katalyst-core/pkg/util/machine"
	"github.com/kubewharf/katalyst-core/pkg/util/metric"
)

type MetricsScope string

const (
	MetricsScopeNode      MetricsScope = "node"
	MetricsScopeNuma      MetricsScope = "numa"
	MetricsScopeCPU       MetricsScope = "cpu"
	MetricsScopeDevice    MetricsScope = "device"
	MetricsScopeContainer MetricsScope = "container"
)

// NotifiedRequest defines the structure as requests for notifier
type NotifiedRequest struct {
	MetricName string

	DeviceID string
	NumaID   int
	CoreID   int

	PodUID        string
	ContainerName string
	NumaNode      string
}

// NotifiedData defines the structure as response data for notifier
type NotifiedData struct {
	Scope    MetricsScope
	Req      NotifiedRequest
	Response chan NotifiedResponse
}

type NotifiedResponse struct {
	Req NotifiedRequest
	metric.MetricData
}

type MetricsReader interface {
	// GetNodeMetric get metric of node.
	GetNodeMetric(metricName string) (metric.MetricData, error)
	// GetNumaMetric get metric of numa.
	GetNumaMetric(numaID int, metricName string) (metric.MetricData, error)
	// GetDeviceMetric get metric of device.
	GetDeviceMetric(deviceName string, metricName string) (metric.MetricData, error)
	// GetCPUMetric get metric of cpu.
	GetCPUMetric(coreID int, metricName string) (metric.MetricData, error)
	// GetContainerMetric get metric of container.
	GetContainerMetric(podUID, containerName, metricName string) (metric.MetricData, error)
	// GetContainerNumaMetric get metric of container per numa.
	GetContainerNumaMetric(podUID, containerName, numaNode, metricName string) (metric.MetricData, error)
	// GetPodVolumeMetric get metric of pod volume.
	GetPodVolumeMetric(podUID, volumeName, metricName string) (metric.MetricData, error)

	// AggregatePodNumaMetric handles numa-level metric for all pods
	AggregatePodNumaMetric(podList []*v1.Pod, numaNode, metricName string, agg metric.Aggregator, filter metric.ContainerMetricFilter) metric.MetricData
	// AggregatePodMetric handles metric for all pods
	AggregatePodMetric(podList []*v1.Pod, metricName string, agg metric.Aggregator, filter metric.ContainerMetricFilter) metric.MetricData
	// AggregateCoreMetric handles metric for all cores
	AggregateCoreMetric(cpuset machine.CPUSet, metricName string, agg metric.Aggregator) metric.MetricData

	// GetCgroupMetric get metric of cgroup path: /kubepods/burstable, /kubepods/besteffort, etc.
	GetCgroupMetric(cgroupPath, metricName string) (metric.MetricData, error)
	// GetCgroupNumaMetric get NUMA metric of qos class: /kubepods/burstable, /kubepods/besteffort, etc.
	GetCgroupNumaMetric(cgroupPath, numaNode, metricName string) (metric.MetricData, error)

	HasSynced() bool
}

type MetricsProvisioner interface {
	Run(ctx context.Context)
	HasSynced() bool
}

type MetricsNotifierManager interface {
	// RegisterNotifier register a channel for raw metric, any time when metric
	// changes, send a data into this given channel along with current time, and
	// we will return a unique key to help with deRegister logic.
	//
	// this "current time" may not represent precisely time when this metric
	// is at, but it indeed is the most precise time katalyst system can provide.
	RegisterNotifier(scope MetricsScope, req NotifiedRequest, response chan NotifiedResponse) string
	DeRegisterNotifier(scope MetricsScope, key string)
	Notify()
}

type ExternalMetricManager interface {
	// RegisterExternalMetric register a function to set metric that can
	// only be obtained from external sources
	RegisterExternalMetric(f func(store *metric.MetricStore))
	Sample()
}

// MetricsFetcher is used to get Node and Pod metrics.
type MetricsFetcher interface {
	// Run starts the preparing logic to collect node metadata.
	Run(ctx context.Context)

	// RegisterNotifier register a channel for raw metric, any time when metric
	// changes, send a data into this given channel along with current time, and
	// we will return a unique key to help with deRegister logic.
	//
	// this "current time" may not represent precisely time when this metric
	// is at, but it indeed is the most precise time katalyst system can provide.
	RegisterNotifier(scope MetricsScope, req NotifiedRequest, response chan NotifiedResponse) string
	DeRegisterNotifier(scope MetricsScope, key string)

	// RegisterExternalMetric register a function to set metric that can
	// only be obtained from external sources
	RegisterExternalMetric(f func(store *metric.MetricStore))

	MetricsReader
}

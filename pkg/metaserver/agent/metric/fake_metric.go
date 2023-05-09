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

// Copyright 2022 The Katalyst Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package metric

import (
	"context"

	v1 "k8s.io/api/core/v1"

	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
	"github.com/kubewharf/katalyst-core/pkg/util/metric"
)

// NewFakeMetricsFetcher returns a fake MetricsFetcher.
func NewFakeMetricsFetcher(emitter metrics.MetricEmitter) MetricsFetcher {
	return &FakeMetricsFetcher{
		metricStore: metric.GetMetricStoreInstance(),
		emitter:     emitter,
	}
}

type FakeMetricsFetcher struct {
	metricStore *metric.MetricStore
	emitter     metrics.MetricEmitter
}

func (f *FakeMetricsFetcher) Run(ctx context.Context) {}

func (f *FakeMetricsFetcher) RegisterNotifier(scope MetricsScope, req NotifiedRequest, response chan NotifiedResponse) string {
	return ""
}

func (f *FakeMetricsFetcher) DeRegisterNotifier(scope MetricsScope, key string) {}

func (f *FakeMetricsFetcher) GetNodeMetric(metricName string) (float64, error) {
	return f.metricStore.GetNodeMetric(metricName)
}

func (f *FakeMetricsFetcher) GetNumaMetric(numaID int, metricName string) (float64, error) {
	return f.metricStore.GetNumaMetric(numaID, metricName)
}

func (f *FakeMetricsFetcher) GetDeviceMetric(deviceName string, metricName string) (float64, error) {
	return f.metricStore.GetDeviceMetric(deviceName, metricName)
}

func (f *FakeMetricsFetcher) GetCPUMetric(coreID int, metricName string) (float64, error) {
	return f.metricStore.GetCPUMetric(coreID, metricName)
}

func (f *FakeMetricsFetcher) GetContainerMetric(podUID, containerName, metricName string) (float64, error) {
	return f.metricStore.GetContainerMetric(podUID, containerName, metricName)
}

func (f *FakeMetricsFetcher) GetContainerNumaMetric(podUID, containerName, numaNode, metricName string) (float64, error) {
	return f.metricStore.GetContainerNumaMetric(podUID, containerName, numaNode, metricName)
}

func (f *FakeMetricsFetcher) SetNodeMetric(metricName string, value float64) {
	f.metricStore.SetNodeMetric(metricName, value)
}

func (f *FakeMetricsFetcher) SetNumaMetric(numaID int, metricName string, value float64) {
	f.metricStore.SetNumaMetric(numaID, metricName, value)
}

func (f *FakeMetricsFetcher) SetCPUMetric(cpu int, metricName string, value float64) {
	f.metricStore.SetCPUMetric(cpu, metricName, value)
}

func (f *FakeMetricsFetcher) SetDeviceMetric(deviceName string, metricName string, value float64) {
	f.metricStore.SetDeviceMetric(deviceName, metricName, value)
}

func (f *FakeMetricsFetcher) SetContainerMetric(podUID, containerName, metricName string, value float64) {
	f.metricStore.SetContainerMetric(podUID, containerName, metricName, value)
}

func (f *FakeMetricsFetcher) SetContainerNumaMetric(podUID, containerName, numaNode, metricName string, value float64) {
	f.metricStore.SetContainerNumaMetric(podUID, containerName, numaNode, metricName, value)
}

func (f *FakeMetricsFetcher) AggregatePodNumaMetric(podList []*v1.Pod, numaNode, metricName string, agg metric.Aggregator, filter metric.ContainerMetricFilter) float64 {
	return f.metricStore.AggregatePodNumaMetric(podList, numaNode, metricName, agg, filter)
}

func (f *FakeMetricsFetcher) AggregatePodMetric(podList []*v1.Pod, metricName string, agg metric.Aggregator, filter metric.ContainerMetricFilter) float64 {
	return f.metricStore.AggregatePodMetric(podList, metricName, agg, filter)
}

func (f *FakeMetricsFetcher) AggregateCoreMetric(cpuset machine.CPUSet, metricName string, agg metric.Aggregator) float64 {
	return f.metricStore.AggregateCoreMetric(cpuset, metricName, agg)
}

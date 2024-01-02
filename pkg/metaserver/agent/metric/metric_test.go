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

package metric

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/options"
	"github.com/kubewharf/katalyst-core/pkg/config"
	metrictypes "github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric/types"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/pod"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
	"github.com/kubewharf/katalyst-core/pkg/util/metric"
)

func generateTestConfiguration(t *testing.T) *config.Configuration {
	testConfiguration, err := options.NewOptions().Config()
	require.NoError(t, err)
	require.NotNil(t, testConfiguration)
	return testConfiguration
}

func Test_notifySystem(t *testing.T) {
	t.Parallel()

	now := time.Now()

	conf := generateTestConfiguration(t)
	f := NewMetricsFetcher(metrics.DummyMetrics{}, &pod.PodFetcherStub{}, conf)

	rChan := make(chan metrictypes.NotifiedResponse, 20)
	f.RegisterNotifier(metrictypes.MetricsScopeNode, metrictypes.NotifiedRequest{
		MetricName: "test-node-metric",
	}, rChan)
	f.RegisterNotifier(metrictypes.MetricsScopeNuma, metrictypes.NotifiedRequest{
		MetricName: "test-numa-metric",
		NumaID:     1,
	}, rChan)
	f.RegisterNotifier(metrictypes.MetricsScopeCPU, metrictypes.NotifiedRequest{
		MetricName: "test-cpu-metric",
		CoreID:     2,
	}, rChan)
	f.RegisterNotifier(metrictypes.MetricsScopeDevice, metrictypes.NotifiedRequest{
		MetricName: "test-device-metric",
		DeviceID:   "test-device",
	}, rChan)
	f.RegisterNotifier(metrictypes.MetricsScopeContainer, metrictypes.NotifiedRequest{
		MetricName:    "test-container-metric",
		PodUID:        "test-pod",
		ContainerName: "test-container",
	}, rChan)
	f.RegisterNotifier(metrictypes.MetricsScopeContainer, metrictypes.NotifiedRequest{
		MetricName:    "test-container-numa-metric",
		PodUID:        "test-pod",
		ContainerName: "test-container",
		NumaNode:      "3",
	}, rChan)

	m := f.(*MetricsFetcherImpl)
	m.metricStore.SetNodeMetric("test-node-metric", metric.MetricData{Value: 34, Time: &now})
	m.metricStore.SetNumaMetric(1, "test-numa-metric", metric.MetricData{Value: 56, Time: &now})
	m.metricStore.SetCPUMetric(2, "test-cpu-metric", metric.MetricData{Value: 78, Time: &now})
	m.metricStore.SetDeviceMetric("test-device", "test-device-metric", metric.MetricData{Value: 91, Time: &now})
	m.metricStore.SetContainerMetric("test-pod", "test-container", "test-container-metric", metric.MetricData{Value: 91, Time: &now})
	m.metricStore.SetContainerNumaMetric("test-pod", "test-container", "3", "test-container-numa-metric", metric.MetricData{Value: 75, Time: &now})

	go func() {
		for {
			select {
			case response := <-rChan:
				switch response.Req.MetricName {
				case "test-node-metric":
					assert.Equal(t, response.Value, 34)
				case "test-numa-metric":
					assert.Equal(t, response.Value, 56)
				case "test-cpu-metric":
					assert.Equal(t, response.Value, 78)
				case "test-device-metric":
					assert.Equal(t, response.Value, 91)
				case "test-container-metric":
					assert.Equal(t, response.Value, 91)
				case "test-container-numa-metric":
					assert.Equal(t, response.Value, 75)
				}
			}
		}
	}()

	time.Sleep(time.Millisecond * 3)
}

func TestStore_Aggregate(t *testing.T) {
	t.Parallel()

	now := time.Now()

	conf := generateTestConfiguration(t)
	f := NewMetricsFetcher(metrics.DummyMetrics{}, &pod.PodFetcherStub{}, conf).(*MetricsFetcherImpl)

	pod1 := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			UID: "pod1",
		},
		Spec: v1.PodSpec{
			Containers: []v1.Container{
				{
					Name: "container1",
				},
				{
					Name: "container2",
				},
			},
		},
	}
	pod2 := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			UID: "pod2",
		},
		Spec: v1.PodSpec{
			Containers: []v1.Container{
				{
					Name: "container3",
				},
			},
		},
	}
	pod3 := &v1.Pod{}

	f.metricStore.SetContainerMetric("pod1", "container1", "test-pod-metric", metric.MetricData{Value: 1, Time: &now})
	f.metricStore.SetContainerMetric("pod1", "container2", "test-pod-metric", metric.MetricData{Value: 1, Time: &now})
	f.metricStore.SetContainerMetric("pod2", "container3", "test-pod-metric", metric.MetricData{Value: 1, Time: &now})
	sum := f.AggregatePodMetric([]*v1.Pod{pod1, pod2, pod3}, "test-pod-metric", metric.AggregatorSum, metric.DefaultContainerMetricFilter)
	assert.Equal(t, float64(3), sum.Value)
	avg := f.AggregatePodMetric([]*v1.Pod{pod1, pod2, pod3}, "test-pod-metric", metric.AggregatorAvg, metric.DefaultContainerMetricFilter)
	assert.Equal(t, float64(1.5), avg.Value)

	f.metricStore.SetContainerNumaMetric("pod1", "container1", "0", "test-pod-numa-metric", metric.MetricData{Value: 1, Time: &now})
	f.metricStore.SetContainerNumaMetric("pod1", "container2", "0", "test-pod-numa-metric", metric.MetricData{Value: 1, Time: &now})
	f.metricStore.SetContainerNumaMetric("pod1", "container2", "1", "test-pod-numa-metric", metric.MetricData{Value: 1, Time: &now})
	f.metricStore.SetContainerNumaMetric("pod2", "container3", "0", "test-pod-numa-metric", metric.MetricData{Value: 1, Time: &now})
	f.metricStore.SetContainerNumaMetric("pod2", "container3", "1", "test-pod-numa-metric", metric.MetricData{Value: 1, Time: &now})
	f.metricStore.SetContainerNumaMetric("pod2", "container3", "1", "test-pod-numa-metric", metric.MetricData{Value: 1, Time: &now})
	sum = f.AggregatePodNumaMetric([]*v1.Pod{pod1, pod2, pod3}, "0", "test-pod-numa-metric", metric.AggregatorSum, metric.DefaultContainerMetricFilter)
	assert.Equal(t, float64(3), sum.Value)
	avg = f.AggregatePodNumaMetric([]*v1.Pod{pod1, pod2, pod3}, "0", "test-pod-numa-metric", metric.AggregatorAvg, metric.DefaultContainerMetricFilter)
	assert.Equal(t, float64(1.5), avg.Value)
	sum = f.AggregatePodNumaMetric([]*v1.Pod{pod1, pod2, pod3}, "1", "test-pod-numa-metric", metric.AggregatorSum, metric.DefaultContainerMetricFilter)
	assert.Equal(t, float64(2), sum.Value)
	avg = f.AggregatePodNumaMetric([]*v1.Pod{pod1, pod2, pod3}, "1", "test-pod-numa-metric", metric.AggregatorAvg, metric.DefaultContainerMetricFilter)
	assert.Equal(t, float64(1), avg.Value)

	f.metricStore.SetCPUMetric(1, "test-cpu-metric", metric.MetricData{Value: 1, Time: &now})
	f.metricStore.SetCPUMetric(1, "test-cpu-metric", metric.MetricData{Value: 2, Time: &now})
	f.metricStore.SetCPUMetric(2, "test-cpu-metric", metric.MetricData{Value: 1, Time: &now})
	f.metricStore.SetCPUMetric(0, "test-cpu-metric", metric.MetricData{Value: 1, Time: &now})
	sum = f.AggregateCoreMetric(machine.NewCPUSet(0, 1, 2, 3), "test-cpu-metric", metric.AggregatorSum)
	assert.Equal(t, float64(4), sum.Value)
	avg = f.AggregateCoreMetric(machine.NewCPUSet(0, 1, 2, 3), "test-cpu-metric", metric.AggregatorAvg)
	assert.Equal(t, float64(4/3.), avg.Value)
}

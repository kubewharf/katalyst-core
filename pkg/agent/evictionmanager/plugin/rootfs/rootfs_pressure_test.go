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

package rootfs

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	k8smetav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	evictionapi "k8s.io/kubernetes/pkg/kubelet/eviction/api"

	pluginapi "github.com/kubewharf/katalyst-api/pkg/protocol/evictionplugin/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/agent/evictionmanager/plugin"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric"
	metrictypes "github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric/types"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	utilmetric "github.com/kubewharf/katalyst-core/pkg/util/metric"
)

type testConf struct {
	minimumFreeThreshold                       *evictionapi.ThresholdValue
	minimumInodesFreeThreshold                 *evictionapi.ThresholdValue
	podMinimumUsedThreshold                    *evictionapi.ThresholdValue
	podMinimumInodesUsedThreshold              *evictionapi.ThresholdValue
	reclaimedQoSPodUsedPriorityThreshold       *evictionapi.ThresholdValue
	reclaimedQosPodInodesUsedPriorityThreshold *evictionapi.ThresholdValue
}

func makeConf(tc *testConf) *config.Configuration {
	conf := config.NewConfiguration()
	// conf.EvictionManagerSyncPeriod = evictionManagerSyncPeriod
	conf.GetDynamicConfiguration().EnableRootfsPressureEviction = true
	conf.GetDynamicConfiguration().MinimumFreeThreshold = tc.minimumFreeThreshold
	conf.GetDynamicConfiguration().MinimumInodesFreeThreshold = tc.minimumInodesFreeThreshold
	conf.GetDynamicConfiguration().PodMinimumUsedThreshold = tc.podMinimumUsedThreshold
	conf.GetDynamicConfiguration().PodMinimumInodesUsedThreshold = tc.podMinimumInodesUsedThreshold
	conf.GetDynamicConfiguration().ReclaimedQoSPodUsedPriorityThreshold = tc.reclaimedQoSPodUsedPriorityThreshold
	conf.GetDynamicConfiguration().ReclaimedQoSPodInodesUsedPriorityThreshold = tc.reclaimedQosPodInodesUsedPriorityThreshold

	return conf
}

func createRootfsPressureEvictionPlugin(tc *testConf, emitter metrics.MetricEmitter, metricsFetcher metrictypes.MetricsFetcher) plugin.EvictionPlugin {
	metaServer := &metaserver.MetaServer{
		MetaAgent: &agent.MetaAgent{
			MetricsFetcher: metricsFetcher,
		},
	}
	conf := makeConf(tc)
	return NewPodRootfsPressureEvictionPlugin(nil, nil, metaServer, emitter, conf)
}

func TestPodRootfsPressureEvictionPlugin_ThresholdMetNoMetricDataNoConf(t *testing.T) {
	emitter := metrics.DummyMetrics{}
	fakeFetcher := metric.NewFakeMetricsFetcher(emitter).(*metric.FakeMetricsFetcher)
	tc := &testConf{}
	rootfsPlugin := createRootfsPressureEvictionPlugin(tc, emitter, fakeFetcher)
	res, err := rootfsPlugin.ThresholdMet(context.TODO())
	assert.NoError(t, err)
	assert.Equal(t, pluginapi.ThresholdMetType_NOT_MET, res.MetType)
}

func TestPodRootfsPressureEvictionPlugin_ThresholdMetNoMetricData(t *testing.T) {
	emitter := metrics.DummyMetrics{}
	fakeFetcher := metric.NewFakeMetricsFetcher(emitter).(*metric.FakeMetricsFetcher)
	tc := &testConf{
		minimumFreeThreshold:       &evictionapi.ThresholdValue{Percentage: 0.9},
		minimumInodesFreeThreshold: &evictionapi.ThresholdValue{Percentage: 0.9},
	}
	rootfsPlugin := createRootfsPressureEvictionPlugin(tc, emitter, fakeFetcher)
	res, err := rootfsPlugin.ThresholdMet(context.TODO())
	assert.NoError(t, err)
	assert.Equal(t, pluginapi.ThresholdMetType_NOT_MET, res.MetType)
}

func TestPodRootfsPressureEvictionPlugin_ThresholdMetNotMet(t *testing.T) {
	emitter := metrics.DummyMetrics{}
	fakeFetcher := metric.NewFakeMetricsFetcher(emitter).(*metric.FakeMetricsFetcher)
	// create metric data without time.
	fakeFetcher.SetNodeMetric(consts.MetricsSystemRootfsAvailable, utilmetric.MetricData{Value: 2000})
	fakeFetcher.SetNodeMetric(consts.MetricsSystemRootfsUsed, utilmetric.MetricData{Value: 8000})
	fakeFetcher.SetNodeMetric(consts.MetricsSystemRootfsCapacity, utilmetric.MetricData{Value: 10000})

	fakeFetcher.SetNodeMetric(consts.MetricsSystemRootfsInodesFree, utilmetric.MetricData{Value: 2000})
	fakeFetcher.SetNodeMetric(consts.MetricsSystemRootfsInodesUsed, utilmetric.MetricData{Value: 8000})
	fakeFetcher.SetNodeMetric(consts.MetricsSystemRootfsInodes, utilmetric.MetricData{Value: 10000})

	tc := &testConf{
		minimumFreeThreshold:       &evictionapi.ThresholdValue{Percentage: 0.1},
		minimumInodesFreeThreshold: &evictionapi.ThresholdValue{Percentage: 0.1},
	}
	rootfsPlugin := createRootfsPressureEvictionPlugin(tc, emitter, fakeFetcher)

	res, err := rootfsPlugin.ThresholdMet(context.TODO())
	assert.NoError(t, err)
	assert.Equal(t, pluginapi.ThresholdMetType_NOT_MET, res.MetType)

	tc = &testConf{
		minimumFreeThreshold:       &evictionapi.ThresholdValue{Quantity: resource.NewQuantity(1000, resource.DecimalSI)},
		minimumInodesFreeThreshold: &evictionapi.ThresholdValue{Quantity: resource.NewQuantity(1000, resource.DecimalSI)},
	}
	rootfsPlugin = createRootfsPressureEvictionPlugin(tc, emitter, fakeFetcher)
	res, err = rootfsPlugin.ThresholdMet(context.TODO())
	assert.NoError(t, err)
	assert.Equal(t, pluginapi.ThresholdMetType_NOT_MET, res.MetType)
}

func TestPodRootfsPressureEvictionPlugin_ThresholdMetUsedMet(t *testing.T) {
	emitter := metrics.DummyMetrics{}
	fakeFetcher := metric.NewFakeMetricsFetcher(emitter).(*metric.FakeMetricsFetcher)
	// create metric data without time.
	fakeFetcher.SetNodeMetric(consts.MetricsSystemRootfsAvailable, utilmetric.MetricData{Value: 2000})
	fakeFetcher.SetNodeMetric(consts.MetricsSystemRootfsUsed, utilmetric.MetricData{Value: 8000})
	fakeFetcher.SetNodeMetric(consts.MetricsSystemRootfsCapacity, utilmetric.MetricData{Value: 10000})

	fakeFetcher.SetNodeMetric(consts.MetricsSystemRootfsInodesFree, utilmetric.MetricData{Value: 2000})
	fakeFetcher.SetNodeMetric(consts.MetricsSystemRootfsInodesUsed, utilmetric.MetricData{Value: 8000})
	fakeFetcher.SetNodeMetric(consts.MetricsSystemRootfsInodes, utilmetric.MetricData{Value: 10000})

	tc := &testConf{
		minimumFreeThreshold:       &evictionapi.ThresholdValue{Percentage: 0.3},
		minimumInodesFreeThreshold: &evictionapi.ThresholdValue{Percentage: 0.1},
	}
	rootfsPlugin := createRootfsPressureEvictionPlugin(tc, emitter, fakeFetcher)

	res, err := rootfsPlugin.ThresholdMet(context.TODO())
	assert.NoError(t, err)
	assert.Equal(t, pluginapi.ThresholdMetType_HARD_MET, res.MetType)

	tc = &testConf{
		minimumFreeThreshold:       &evictionapi.ThresholdValue{Quantity: resource.NewQuantity(3000, resource.DecimalSI)},
		minimumInodesFreeThreshold: &evictionapi.ThresholdValue{Quantity: resource.NewQuantity(1000, resource.DecimalSI)},
	}
	rootfsPlugin = createRootfsPressureEvictionPlugin(tc, emitter, fakeFetcher)
	res, err = rootfsPlugin.ThresholdMet(context.TODO())
	assert.NoError(t, err)
	assert.Equal(t, pluginapi.ThresholdMetType_HARD_MET, res.MetType)
}

func TestPodRootfsPressureEvictionPlugin_ThresholdMetInodesMet(t *testing.T) {
	emitter := metrics.DummyMetrics{}
	fakeFetcher := metric.NewFakeMetricsFetcher(emitter).(*metric.FakeMetricsFetcher)
	// create metric data without time.
	fakeFetcher.SetNodeMetric(consts.MetricsSystemRootfsAvailable, utilmetric.MetricData{Value: 2000})
	fakeFetcher.SetNodeMetric(consts.MetricsSystemRootfsUsed, utilmetric.MetricData{Value: 8000})
	fakeFetcher.SetNodeMetric(consts.MetricsSystemRootfsCapacity, utilmetric.MetricData{Value: 10000})

	fakeFetcher.SetNodeMetric(consts.MetricsSystemRootfsInodesFree, utilmetric.MetricData{Value: 2000})
	fakeFetcher.SetNodeMetric(consts.MetricsSystemRootfsInodesUsed, utilmetric.MetricData{Value: 8000})
	fakeFetcher.SetNodeMetric(consts.MetricsSystemRootfsInodes, utilmetric.MetricData{Value: 10000})

	tc := &testConf{
		minimumFreeThreshold:       &evictionapi.ThresholdValue{Percentage: 0.1},
		minimumInodesFreeThreshold: &evictionapi.ThresholdValue{Percentage: 0.3},
	}
	rootfsPlugin := createRootfsPressureEvictionPlugin(tc, emitter, fakeFetcher)

	res, err := rootfsPlugin.ThresholdMet(context.TODO())
	assert.NoError(t, err)
	assert.Equal(t, pluginapi.ThresholdMetType_HARD_MET, res.MetType)

	tc = &testConf{
		minimumFreeThreshold:       &evictionapi.ThresholdValue{Quantity: resource.NewQuantity(1000, resource.DecimalSI)},
		minimumInodesFreeThreshold: &evictionapi.ThresholdValue{Quantity: resource.NewQuantity(3000, resource.DecimalSI)},
	}
	rootfsPlugin = createRootfsPressureEvictionPlugin(tc, emitter, fakeFetcher)
	res, err = rootfsPlugin.ThresholdMet(context.TODO())
	assert.NoError(t, err)
	assert.Equal(t, pluginapi.ThresholdMetType_HARD_MET, res.MetType)
}

func TestPodRootfsPressureEvictionPlugin_ThresholdMet(t *testing.T) {
	emitter := metrics.DummyMetrics{}
	fakeFetcher := metric.NewFakeMetricsFetcher(emitter).(*metric.FakeMetricsFetcher)
	// create metric data without time.
	fakeFetcher.SetNodeMetric(consts.MetricsSystemRootfsAvailable, utilmetric.MetricData{Value: 2000})
	fakeFetcher.SetNodeMetric(consts.MetricsSystemRootfsUsed, utilmetric.MetricData{Value: 8000})
	fakeFetcher.SetNodeMetric(consts.MetricsSystemRootfsCapacity, utilmetric.MetricData{Value: 10000})

	fakeFetcher.SetNodeMetric(consts.MetricsSystemRootfsInodesFree, utilmetric.MetricData{Value: 2000})
	fakeFetcher.SetNodeMetric(consts.MetricsSystemRootfsInodesUsed, utilmetric.MetricData{Value: 8000})
	fakeFetcher.SetNodeMetric(consts.MetricsSystemRootfsInodes, utilmetric.MetricData{Value: 10000})

	tc := &testConf{
		minimumFreeThreshold:       &evictionapi.ThresholdValue{Percentage: 0.3},
		minimumInodesFreeThreshold: &evictionapi.ThresholdValue{Percentage: 0.3},
	}
	rootfsPlugin := createRootfsPressureEvictionPlugin(tc, emitter, fakeFetcher)

	res, err := rootfsPlugin.ThresholdMet(context.TODO())
	assert.NoError(t, err)
	assert.Equal(t, pluginapi.ThresholdMetType_HARD_MET, res.MetType)

	tc = &testConf{
		minimumFreeThreshold:       &evictionapi.ThresholdValue{Quantity: resource.NewQuantity(3000, resource.DecimalSI)},
		minimumInodesFreeThreshold: &evictionapi.ThresholdValue{Quantity: resource.NewQuantity(3000, resource.DecimalSI)},
	}
	rootfsPlugin = createRootfsPressureEvictionPlugin(tc, emitter, fakeFetcher)
	res, err = rootfsPlugin.ThresholdMet(context.TODO())
	assert.NoError(t, err)
	assert.Equal(t, pluginapi.ThresholdMetType_HARD_MET, res.MetType)
}

func TestPodRootfsPressureEvictionPlugin_ThresholdMetMetricDataExpire(t *testing.T) {
	metricTime := time.Now().Add(-65 * time.Second)

	emitter := metrics.DummyMetrics{}
	fakeFetcher := metric.NewFakeMetricsFetcher(emitter).(*metric.FakeMetricsFetcher)
	// create metric data without time.
	fakeFetcher.SetNodeMetric(consts.MetricsSystemRootfsAvailable, utilmetric.MetricData{Value: 2000, Time: &metricTime})
	fakeFetcher.SetNodeMetric(consts.MetricsSystemRootfsUsed, utilmetric.MetricData{Value: 8000, Time: &metricTime})
	fakeFetcher.SetNodeMetric(consts.MetricsSystemRootfsCapacity, utilmetric.MetricData{Value: 10000, Time: &metricTime})

	fakeFetcher.SetNodeMetric(consts.MetricsSystemRootfsInodesFree, utilmetric.MetricData{Value: 2000, Time: &metricTime})
	fakeFetcher.SetNodeMetric(consts.MetricsSystemRootfsInodesUsed, utilmetric.MetricData{Value: 8000, Time: &metricTime})
	fakeFetcher.SetNodeMetric(consts.MetricsSystemRootfsInodes, utilmetric.MetricData{Value: 10000, Time: &metricTime})

	tc := &testConf{
		minimumFreeThreshold:       &evictionapi.ThresholdValue{Percentage: 0.7},
		minimumInodesFreeThreshold: &evictionapi.ThresholdValue{Percentage: 0.7},
	}
	rootfsPlugin := createRootfsPressureEvictionPlugin(tc, emitter, fakeFetcher)

	res, err := rootfsPlugin.ThresholdMet(context.TODO())
	assert.NoError(t, err)
	assert.Equal(t, pluginapi.ThresholdMetType_NOT_MET, res.MetType)

	tc = &testConf{
		minimumFreeThreshold:       &evictionapi.ThresholdValue{Quantity: resource.NewQuantity(7000, resource.DecimalSI)},
		minimumInodesFreeThreshold: &evictionapi.ThresholdValue{Quantity: resource.NewQuantity(7000, resource.DecimalSI)},
	}
	rootfsPlugin = createRootfsPressureEvictionPlugin(tc, emitter, fakeFetcher)
	res, err = rootfsPlugin.ThresholdMet(context.TODO())
	assert.NoError(t, err)
	assert.Equal(t, pluginapi.ThresholdMetType_NOT_MET, res.MetType)
}

func makeGetTopNRequest() *pluginapi.GetTopEvictionPodsRequest {
	return &pluginapi.GetTopEvictionPodsRequest{
		TopN: 1,
		ActivePods: []*v1.Pod{
			{
				ObjectMeta: k8smetav1.ObjectMeta{UID: "podUID1"},
				Spec: v1.PodSpec{
					Containers: []v1.Container{
						{
							Name: "containerName",
						},
					},
				},
			},
			{
				ObjectMeta: k8smetav1.ObjectMeta{UID: "podUID2"},
				Spec: v1.PodSpec{
					Containers: []v1.Container{
						{
							Name: "containerName",
						},
					},
				},
			},
		},
	}
}

func TestPodRootfsPressureEvictionPlugin_GetTopEvictionPodsNotMet(t *testing.T) {
	emitter := metrics.DummyMetrics{}
	fakeFetcher := metric.NewFakeMetricsFetcher(emitter).(*metric.FakeMetricsFetcher)
	// create metric data without time.
	fakeFetcher.SetNodeMetric(consts.MetricsSystemRootfsAvailable, utilmetric.MetricData{Value: 2000})
	fakeFetcher.SetNodeMetric(consts.MetricsSystemRootfsUsed, utilmetric.MetricData{Value: 8000})
	fakeFetcher.SetNodeMetric(consts.MetricsSystemRootfsCapacity, utilmetric.MetricData{Value: 10000})

	fakeFetcher.SetNodeMetric(consts.MetricsSystemRootfsInodesFree, utilmetric.MetricData{Value: 2000})
	fakeFetcher.SetNodeMetric(consts.MetricsSystemRootfsInodesUsed, utilmetric.MetricData{Value: 8000})
	fakeFetcher.SetNodeMetric(consts.MetricsSystemRootfsInodes, utilmetric.MetricData{Value: 10000})

	tc := &testConf{
		minimumFreeThreshold:       &evictionapi.ThresholdValue{Percentage: 0.1},
		minimumInodesFreeThreshold: &evictionapi.ThresholdValue{Percentage: 0.1},
	}
	rootfsPlugin := createRootfsPressureEvictionPlugin(tc, emitter, fakeFetcher)

	res, err := rootfsPlugin.ThresholdMet(context.TODO())
	assert.NoError(t, err)
	assert.Equal(t, pluginapi.ThresholdMetType_NOT_MET, res.MetType)

	fakeFetcher.SetContainerMetric("podUID1", "containerName", consts.MetricsContainerRootfsUsed, utilmetric.MetricData{Value: 800})
	fakeFetcher.SetContainerMetric("podUID1", "containerName", consts.MetricsContainerRootfsInodesUsed, utilmetric.MetricData{Value: 800})
	fakeFetcher.SetContainerMetric("podUID2", "containerName", consts.MetricsContainerRootfsUsed, utilmetric.MetricData{Value: 799})
	fakeFetcher.SetContainerMetric("podUID2", "containerName", consts.MetricsContainerRootfsInodesUsed, utilmetric.MetricData{Value: 799})

	req := makeGetTopNRequest()
	resTop, err := rootfsPlugin.GetTopEvictionPods(context.TODO(), req)
	assert.NoError(t, err)
	assert.Equal(t, 0, len(resTop.TargetPods))
}

func TestPodRootfsPressureEvictionPlugin_GetTopEvictionPodsMet(t *testing.T) {
	emitter := metrics.DummyMetrics{}
	fakeFetcher := metric.NewFakeMetricsFetcher(emitter).(*metric.FakeMetricsFetcher)
	// create metric data without time.
	fakeFetcher.SetNodeMetric(consts.MetricsSystemRootfsAvailable, utilmetric.MetricData{Value: 2000})
	fakeFetcher.SetNodeMetric(consts.MetricsSystemRootfsUsed, utilmetric.MetricData{Value: 8000})
	fakeFetcher.SetNodeMetric(consts.MetricsSystemRootfsCapacity, utilmetric.MetricData{Value: 10000})

	fakeFetcher.SetNodeMetric(consts.MetricsSystemRootfsInodesFree, utilmetric.MetricData{Value: 2000})
	fakeFetcher.SetNodeMetric(consts.MetricsSystemRootfsInodesUsed, utilmetric.MetricData{Value: 8000})
	fakeFetcher.SetNodeMetric(consts.MetricsSystemRootfsInodes, utilmetric.MetricData{Value: 10000})

	tc := &testConf{
		minimumFreeThreshold:       &evictionapi.ThresholdValue{Percentage: 0.3},
		minimumInodesFreeThreshold: &evictionapi.ThresholdValue{Percentage: 0.3},
	}
	rootfsPlugin := createRootfsPressureEvictionPlugin(tc, emitter, fakeFetcher)

	res, err := rootfsPlugin.ThresholdMet(context.TODO())
	assert.NoError(t, err)
	assert.Equal(t, pluginapi.ThresholdMetType_HARD_MET, res.MetType)

	fakeFetcher.SetContainerMetric("podUID1", "containerName", consts.MetricsContainerRootfsUsed, utilmetric.MetricData{Value: 800})
	fakeFetcher.SetContainerMetric("podUID1", "containerName", consts.MetricsContainerRootfsInodesUsed, utilmetric.MetricData{Value: 800})
	fakeFetcher.SetContainerMetric("podUID2", "containerName", consts.MetricsContainerRootfsUsed, utilmetric.MetricData{Value: 799})
	fakeFetcher.SetContainerMetric("podUID2", "containerName", consts.MetricsContainerRootfsInodesUsed, utilmetric.MetricData{Value: 799})

	req := makeGetTopNRequest()
	resTop, err := rootfsPlugin.GetTopEvictionPods(context.TODO(), req)
	assert.NoError(t, err)
	assert.Equal(t, 1, len(resTop.TargetPods))
	assert.Equal(t, types.UID("podUID1"), resTop.TargetPods[0].UID)

	fakeFetcher.SetContainerMetric("podUID2", "containerName", consts.MetricsContainerRootfsUsed, utilmetric.MetricData{Value: 801})
	fakeFetcher.SetContainerMetric("podUID2", "containerName", consts.MetricsContainerRootfsInodesUsed, utilmetric.MetricData{Value: 801})
	resTop, err = rootfsPlugin.GetTopEvictionPods(context.TODO(), req)
	assert.NoError(t, err)
	assert.Equal(t, 1, len(resTop.TargetPods))
	assert.Equal(t, types.UID("podUID2"), resTop.TargetPods[0].UID)
}

func TestPodRootfsPressureEvictionPlugin_GetTopEvictionPodsUsedMet(t *testing.T) {
	emitter := metrics.DummyMetrics{}
	fakeFetcher := metric.NewFakeMetricsFetcher(emitter).(*metric.FakeMetricsFetcher)
	// create metric data without time.
	fakeFetcher.SetNodeMetric(consts.MetricsSystemRootfsAvailable, utilmetric.MetricData{Value: 2000})
	fakeFetcher.SetNodeMetric(consts.MetricsSystemRootfsUsed, utilmetric.MetricData{Value: 8000})
	fakeFetcher.SetNodeMetric(consts.MetricsSystemRootfsCapacity, utilmetric.MetricData{Value: 10000})

	fakeFetcher.SetNodeMetric(consts.MetricsSystemRootfsInodesFree, utilmetric.MetricData{Value: 2000})
	fakeFetcher.SetNodeMetric(consts.MetricsSystemRootfsInodesUsed, utilmetric.MetricData{Value: 8000})
	fakeFetcher.SetNodeMetric(consts.MetricsSystemRootfsInodes, utilmetric.MetricData{Value: 10000})

	tc := &testConf{
		minimumFreeThreshold:       &evictionapi.ThresholdValue{Percentage: 0.3},
		minimumInodesFreeThreshold: &evictionapi.ThresholdValue{Percentage: 0.1},
	}
	rootfsPlugin := createRootfsPressureEvictionPlugin(tc, emitter, fakeFetcher)

	res, err := rootfsPlugin.ThresholdMet(context.TODO())
	assert.NoError(t, err)
	assert.Equal(t, pluginapi.ThresholdMetType_HARD_MET, res.MetType)

	fakeFetcher.SetContainerMetric("podUID1", "containerName", consts.MetricsContainerRootfsUsed, utilmetric.MetricData{Value: 800})
	fakeFetcher.SetContainerMetric("podUID1", "containerName", consts.MetricsContainerRootfsInodesUsed, utilmetric.MetricData{Value: 799})
	fakeFetcher.SetContainerMetric("podUID2", "containerName", consts.MetricsContainerRootfsUsed, utilmetric.MetricData{Value: 799})
	fakeFetcher.SetContainerMetric("podUID2", "containerName", consts.MetricsContainerRootfsInodesUsed, utilmetric.MetricData{Value: 800})

	req := makeGetTopNRequest()
	resTop, err := rootfsPlugin.GetTopEvictionPods(context.TODO(), req)
	assert.NoError(t, err)
	assert.Equal(t, 1, len(resTop.TargetPods))
	assert.Equal(t, types.UID("podUID1"), resTop.TargetPods[0].UID)

	fakeFetcher.SetContainerMetric("podUID2", "containerName", consts.MetricsContainerRootfsUsed, utilmetric.MetricData{Value: 801})
	fakeFetcher.SetContainerMetric("podUID2", "containerName", consts.MetricsContainerRootfsInodesUsed, utilmetric.MetricData{Value: 699})
	resTop, err = rootfsPlugin.GetTopEvictionPods(context.TODO(), req)
	assert.NoError(t, err)
	assert.Equal(t, 1, len(resTop.TargetPods))
	assert.Equal(t, types.UID("podUID2"), resTop.TargetPods[0].UID)
}

func TestPodRootfsPressureEvictionPlugin_GetTopEvictionPodsInodesMet(t *testing.T) {
	emitter := metrics.DummyMetrics{}
	fakeFetcher := metric.NewFakeMetricsFetcher(emitter).(*metric.FakeMetricsFetcher)
	// create metric data without time.
	fakeFetcher.SetNodeMetric(consts.MetricsSystemRootfsAvailable, utilmetric.MetricData{Value: 2000})
	fakeFetcher.SetNodeMetric(consts.MetricsSystemRootfsUsed, utilmetric.MetricData{Value: 8000})
	fakeFetcher.SetNodeMetric(consts.MetricsSystemRootfsCapacity, utilmetric.MetricData{Value: 10000})

	fakeFetcher.SetNodeMetric(consts.MetricsSystemRootfsInodesFree, utilmetric.MetricData{Value: 2000})
	fakeFetcher.SetNodeMetric(consts.MetricsSystemRootfsInodesUsed, utilmetric.MetricData{Value: 8000})
	fakeFetcher.SetNodeMetric(consts.MetricsSystemRootfsInodes, utilmetric.MetricData{Value: 10000})

	tc := &testConf{
		minimumFreeThreshold:       &evictionapi.ThresholdValue{Percentage: 0.1},
		minimumInodesFreeThreshold: &evictionapi.ThresholdValue{Percentage: 0.3},
	}
	rootfsPlugin := createRootfsPressureEvictionPlugin(tc, emitter, fakeFetcher)

	res, err := rootfsPlugin.ThresholdMet(context.TODO())
	assert.NoError(t, err)
	assert.Equal(t, pluginapi.ThresholdMetType_HARD_MET, res.MetType)

	fakeFetcher.SetContainerMetric("podUID1", "containerName", consts.MetricsContainerRootfsUsed, utilmetric.MetricData{Value: 799})
	fakeFetcher.SetContainerMetric("podUID1", "containerName", consts.MetricsContainerRootfsInodesUsed, utilmetric.MetricData{Value: 800})
	fakeFetcher.SetContainerMetric("podUID2", "containerName", consts.MetricsContainerRootfsUsed, utilmetric.MetricData{Value: 800})
	fakeFetcher.SetContainerMetric("podUID2", "containerName", consts.MetricsContainerRootfsInodesUsed, utilmetric.MetricData{Value: 799})

	req := makeGetTopNRequest()
	resTop, err := rootfsPlugin.GetTopEvictionPods(context.TODO(), req)
	assert.NoError(t, err)
	assert.Equal(t, 1, len(resTop.TargetPods))
	assert.Equal(t, types.UID("podUID1"), resTop.TargetPods[0].UID)

	fakeFetcher.SetContainerMetric("podUID2", "containerName", consts.MetricsContainerRootfsUsed, utilmetric.MetricData{Value: 699})
	fakeFetcher.SetContainerMetric("podUID2", "containerName", consts.MetricsContainerRootfsInodesUsed, utilmetric.MetricData{Value: 801})
	resTop, err = rootfsPlugin.GetTopEvictionPods(context.TODO(), req)
	assert.NoError(t, err)
	assert.Equal(t, 1, len(resTop.TargetPods))
	assert.Equal(t, types.UID("podUID2"), resTop.TargetPods[0].UID)
}

func TestPodRootfsPressureEvictionPlugin_GetTopEvictionPodsUsedMetProtection(t *testing.T) {
	emitter := metrics.DummyMetrics{}
	fakeFetcher := metric.NewFakeMetricsFetcher(emitter).(*metric.FakeMetricsFetcher)
	// create metric data without time.
	fakeFetcher.SetNodeMetric(consts.MetricsSystemRootfsAvailable, utilmetric.MetricData{Value: 2000})
	fakeFetcher.SetNodeMetric(consts.MetricsSystemRootfsUsed, utilmetric.MetricData{Value: 8000})
	fakeFetcher.SetNodeMetric(consts.MetricsSystemRootfsCapacity, utilmetric.MetricData{Value: 10000})

	fakeFetcher.SetNodeMetric(consts.MetricsSystemRootfsInodesFree, utilmetric.MetricData{Value: 2000})
	fakeFetcher.SetNodeMetric(consts.MetricsSystemRootfsInodesUsed, utilmetric.MetricData{Value: 8000})
	fakeFetcher.SetNodeMetric(consts.MetricsSystemRootfsInodes, utilmetric.MetricData{Value: 10000})

	tc := &testConf{
		minimumFreeThreshold:       &evictionapi.ThresholdValue{Percentage: 0.3},
		minimumInodesFreeThreshold: &evictionapi.ThresholdValue{Percentage: 0.1},
		podMinimumUsedThreshold:    &evictionapi.ThresholdValue{Quantity: resource.NewQuantity(900, resource.DecimalSI)},
	}
	rootfsPlugin := createRootfsPressureEvictionPlugin(tc, emitter, fakeFetcher)

	res, err := rootfsPlugin.ThresholdMet(context.TODO())
	assert.NoError(t, err)
	assert.Equal(t, pluginapi.ThresholdMetType_HARD_MET, res.MetType)

	req := makeGetTopNRequest()

	fakeFetcher.SetContainerMetric("podUID1", "containerName", consts.MetricsContainerRootfsUsed, utilmetric.MetricData{Value: 800})
	fakeFetcher.SetContainerMetric("podUID1", "containerName", consts.MetricsContainerRootfsInodesUsed, utilmetric.MetricData{Value: 799})
	fakeFetcher.SetContainerMetric("podUID2", "containerName", consts.MetricsContainerRootfsUsed, utilmetric.MetricData{Value: 799})
	fakeFetcher.SetContainerMetric("podUID2", "containerName", consts.MetricsContainerRootfsInodesUsed, utilmetric.MetricData{Value: 800})
	resTop, err := rootfsPlugin.GetTopEvictionPods(context.TODO(), req)
	assert.NoError(t, err)
	assert.Equal(t, 0, len(resTop.TargetPods))

	fakeFetcher.SetContainerMetric("podUID1", "containerName", consts.MetricsContainerRootfsUsed, utilmetric.MetricData{Value: 1000})
	fakeFetcher.SetContainerMetric("podUID1", "containerName", consts.MetricsContainerRootfsInodesUsed, utilmetric.MetricData{Value: 1000})
	fakeFetcher.SetContainerMetric("podUID2", "containerName", consts.MetricsContainerRootfsUsed, utilmetric.MetricData{Value: 800})
	fakeFetcher.SetContainerMetric("podUID2", "containerName", consts.MetricsContainerRootfsInodesUsed, utilmetric.MetricData{Value: 800})
	resTop, err = rootfsPlugin.GetTopEvictionPods(context.TODO(), req)
	assert.NoError(t, err)
	assert.Equal(t, 1, len(resTop.TargetPods))
	assert.Equal(t, types.UID("podUID1"), resTop.TargetPods[0].UID)

	tc = &testConf{
		minimumFreeThreshold:       &evictionapi.ThresholdValue{Percentage: 0.3},
		minimumInodesFreeThreshold: &evictionapi.ThresholdValue{Percentage: 0.1},
		podMinimumUsedThreshold:    &evictionapi.ThresholdValue{Percentage: 0.1},
	}
	rootfsPlugin = createRootfsPressureEvictionPlugin(tc, emitter, fakeFetcher)
	res, err = rootfsPlugin.ThresholdMet(context.TODO())
	assert.NoError(t, err)
	assert.Equal(t, pluginapi.ThresholdMetType_HARD_MET, res.MetType)

	fakeFetcher.SetContainerMetric("podUID1", "containerName", consts.MetricsContainerRootfsUsed, utilmetric.MetricData{Value: 800})
	fakeFetcher.SetContainerMetric("podUID1", "containerName", consts.MetricsContainerRootfsInodesUsed, utilmetric.MetricData{Value: 799})
	fakeFetcher.SetContainerMetric("podUID2", "containerName", consts.MetricsContainerRootfsUsed, utilmetric.MetricData{Value: 799})
	fakeFetcher.SetContainerMetric("podUID2", "containerName", consts.MetricsContainerRootfsInodesUsed, utilmetric.MetricData{Value: 800})
	resTop, err = rootfsPlugin.GetTopEvictionPods(context.TODO(), req)
	assert.NoError(t, err)
	assert.Equal(t, 0, len(resTop.TargetPods))

	fakeFetcher.SetContainerMetric("podUID1", "containerName", consts.MetricsContainerRootfsUsed, utilmetric.MetricData{Value: 1100})
	fakeFetcher.SetContainerMetric("podUID1", "containerName", consts.MetricsContainerRootfsInodesUsed, utilmetric.MetricData{Value: 799})
	fakeFetcher.SetContainerMetric("podUID2", "containerName", consts.MetricsContainerRootfsUsed, utilmetric.MetricData{Value: 799})
	fakeFetcher.SetContainerMetric("podUID2", "containerName", consts.MetricsContainerRootfsInodesUsed, utilmetric.MetricData{Value: 1100})
	resTop, err = rootfsPlugin.GetTopEvictionPods(context.TODO(), req)
	assert.NoError(t, err)
	assert.Equal(t, 1, len(resTop.TargetPods))
	assert.Equal(t, types.UID("podUID1"), resTop.TargetPods[0].UID)

	fakeFetcher.SetContainerMetric("podUID1", "containerName", consts.MetricsContainerRootfsUsed, utilmetric.MetricData{Value: 1100})
	fakeFetcher.SetContainerMetric("podUID1", "containerName", consts.MetricsContainerRootfsInodesUsed, utilmetric.MetricData{Value: 799})
	fakeFetcher.SetContainerMetric("podUID2", "containerName", consts.MetricsContainerRootfsUsed, utilmetric.MetricData{Value: 1200})
	fakeFetcher.SetContainerMetric("podUID2", "containerName", consts.MetricsContainerRootfsInodesUsed, utilmetric.MetricData{Value: 1100})
	resTop, err = rootfsPlugin.GetTopEvictionPods(context.TODO(), req)
	assert.NoError(t, err)
	assert.Equal(t, 1, len(resTop.TargetPods))
	assert.Equal(t, types.UID("podUID2"), resTop.TargetPods[0].UID)

	tc = &testConf{
		minimumFreeThreshold:       &evictionapi.ThresholdValue{Percentage: 0.3},
		minimumInodesFreeThreshold: &evictionapi.ThresholdValue{Percentage: 0.1},
	}
	rootfsPlugin = createRootfsPressureEvictionPlugin(tc, emitter, fakeFetcher)
	res, err = rootfsPlugin.ThresholdMet(context.TODO())
	assert.NoError(t, err)
	assert.Equal(t, pluginapi.ThresholdMetType_HARD_MET, res.MetType)

	fakeFetcher.SetContainerMetric("podUID1", "containerName", consts.MetricsContainerRootfsUsed, utilmetric.MetricData{Value: 2})
	fakeFetcher.SetContainerMetric("podUID1", "containerName", consts.MetricsContainerRootfsInodesUsed, utilmetric.MetricData{Value: 799})
	fakeFetcher.SetContainerMetric("podUID2", "containerName", consts.MetricsContainerRootfsUsed, utilmetric.MetricData{Value: 1})
	fakeFetcher.SetContainerMetric("podUID2", "containerName", consts.MetricsContainerRootfsInodesUsed, utilmetric.MetricData{Value: 800})
	resTop, err = rootfsPlugin.GetTopEvictionPods(context.TODO(), req)
	assert.NoError(t, err)
	assert.Equal(t, 1, len(resTop.TargetPods))
	assert.Equal(t, types.UID("podUID1"), resTop.TargetPods[0].UID)

	fakeFetcher.SetContainerMetric("podUID1", "containerName", consts.MetricsContainerRootfsUsed, utilmetric.MetricData{Value: 1})
	fakeFetcher.SetContainerMetric("podUID1", "containerName", consts.MetricsContainerRootfsInodesUsed, utilmetric.MetricData{Value: 799})
	fakeFetcher.SetContainerMetric("podUID2", "containerName", consts.MetricsContainerRootfsUsed, utilmetric.MetricData{Value: 2})
	fakeFetcher.SetContainerMetric("podUID2", "containerName", consts.MetricsContainerRootfsInodesUsed, utilmetric.MetricData{Value: 800})
	resTop, err = rootfsPlugin.GetTopEvictionPods(context.TODO(), req)
	assert.NoError(t, err)
	assert.Equal(t, 1, len(resTop.TargetPods))
	assert.Equal(t, types.UID("podUID2"), resTop.TargetPods[0].UID)
}

func TestPodRootfsPressureEvictionPlugin_GetTopEvictionPodsInodesMetProtection(t *testing.T) {
	emitter := metrics.DummyMetrics{}
	fakeFetcher := metric.NewFakeMetricsFetcher(emitter).(*metric.FakeMetricsFetcher)
	// create metric data without time.
	fakeFetcher.SetNodeMetric(consts.MetricsSystemRootfsAvailable, utilmetric.MetricData{Value: 2000})
	fakeFetcher.SetNodeMetric(consts.MetricsSystemRootfsUsed, utilmetric.MetricData{Value: 8000})
	fakeFetcher.SetNodeMetric(consts.MetricsSystemRootfsCapacity, utilmetric.MetricData{Value: 10000})

	fakeFetcher.SetNodeMetric(consts.MetricsSystemRootfsInodesFree, utilmetric.MetricData{Value: 2000})
	fakeFetcher.SetNodeMetric(consts.MetricsSystemRootfsInodesUsed, utilmetric.MetricData{Value: 8000})
	fakeFetcher.SetNodeMetric(consts.MetricsSystemRootfsInodes, utilmetric.MetricData{Value: 10000})

	tc := &testConf{
		minimumFreeThreshold:          &evictionapi.ThresholdValue{Percentage: 0.1},
		minimumInodesFreeThreshold:    &evictionapi.ThresholdValue{Percentage: 0.3},
		podMinimumInodesUsedThreshold: &evictionapi.ThresholdValue{Quantity: resource.NewQuantity(900, resource.DecimalSI)},
	}
	rootfsPlugin := createRootfsPressureEvictionPlugin(tc, emitter, fakeFetcher)
	res, err := rootfsPlugin.ThresholdMet(context.TODO())
	assert.NoError(t, err)
	assert.Equal(t, pluginapi.ThresholdMetType_HARD_MET, res.MetType)

	fakeFetcher.SetContainerMetric("podUID1", "containerName", consts.MetricsContainerRootfsUsed, utilmetric.MetricData{Value: 799})
	fakeFetcher.SetContainerMetric("podUID1", "containerName", consts.MetricsContainerRootfsInodesUsed, utilmetric.MetricData{Value: 800})
	fakeFetcher.SetContainerMetric("podUID2", "containerName", consts.MetricsContainerRootfsUsed, utilmetric.MetricData{Value: 800})
	fakeFetcher.SetContainerMetric("podUID2", "containerName", consts.MetricsContainerRootfsInodesUsed, utilmetric.MetricData{Value: 799})
	req := makeGetTopNRequest()
	resTop, err := rootfsPlugin.GetTopEvictionPods(context.TODO(), req)
	assert.NoError(t, err)
	assert.Equal(t, 0, len(resTop.TargetPods))

	fakeFetcher.SetContainerMetric("podUID1", "containerName", consts.MetricsContainerRootfsUsed, utilmetric.MetricData{Value: 800})
	fakeFetcher.SetContainerMetric("podUID1", "containerName", consts.MetricsContainerRootfsInodesUsed, utilmetric.MetricData{Value: 1000})
	fakeFetcher.SetContainerMetric("podUID2", "containerName", consts.MetricsContainerRootfsUsed, utilmetric.MetricData{Value: 1000})
	fakeFetcher.SetContainerMetric("podUID2", "containerName", consts.MetricsContainerRootfsInodesUsed, utilmetric.MetricData{Value: 800})
	resTop, err = rootfsPlugin.GetTopEvictionPods(context.TODO(), req)
	assert.NoError(t, err)
	assert.Equal(t, 1, len(resTop.TargetPods))
	assert.Equal(t, types.UID("podUID1"), resTop.TargetPods[0].UID)

	tc = &testConf{
		minimumFreeThreshold:          &evictionapi.ThresholdValue{Percentage: 0.1},
		minimumInodesFreeThreshold:    &evictionapi.ThresholdValue{Percentage: 0.3},
		podMinimumInodesUsedThreshold: &evictionapi.ThresholdValue{Percentage: 0.1},
	}
	rootfsPlugin = createRootfsPressureEvictionPlugin(tc, emitter, fakeFetcher)
	res, err = rootfsPlugin.ThresholdMet(context.TODO())
	assert.NoError(t, err)
	assert.Equal(t, pluginapi.ThresholdMetType_HARD_MET, res.MetType)

	fakeFetcher.SetContainerMetric("podUID1", "containerName", consts.MetricsContainerRootfsUsed, utilmetric.MetricData{Value: 799})
	fakeFetcher.SetContainerMetric("podUID1", "containerName", consts.MetricsContainerRootfsInodesUsed, utilmetric.MetricData{Value: 800})
	fakeFetcher.SetContainerMetric("podUID2", "containerName", consts.MetricsContainerRootfsUsed, utilmetric.MetricData{Value: 800})
	fakeFetcher.SetContainerMetric("podUID2", "containerName", consts.MetricsContainerRootfsInodesUsed, utilmetric.MetricData{Value: 799})
	resTop, err = rootfsPlugin.GetTopEvictionPods(context.TODO(), req)
	assert.NoError(t, err)
	assert.Equal(t, 0, len(resTop.TargetPods))

	fakeFetcher.SetContainerMetric("podUID1", "containerName", consts.MetricsContainerRootfsUsed, utilmetric.MetricData{Value: 799})
	fakeFetcher.SetContainerMetric("podUID1", "containerName", consts.MetricsContainerRootfsInodesUsed, utilmetric.MetricData{Value: 1100})
	fakeFetcher.SetContainerMetric("podUID2", "containerName", consts.MetricsContainerRootfsUsed, utilmetric.MetricData{Value: 800})
	fakeFetcher.SetContainerMetric("podUID2", "containerName", consts.MetricsContainerRootfsInodesUsed, utilmetric.MetricData{Value: 799})
	resTop, err = rootfsPlugin.GetTopEvictionPods(context.TODO(), req)
	assert.NoError(t, err)
	assert.Equal(t, 1, len(resTop.TargetPods))
	assert.Equal(t, types.UID("podUID1"), resTop.TargetPods[0].UID)

	fakeFetcher.SetContainerMetric("podUID1", "containerName", consts.MetricsContainerRootfsUsed, utilmetric.MetricData{Value: 799})
	fakeFetcher.SetContainerMetric("podUID1", "containerName", consts.MetricsContainerRootfsInodesUsed, utilmetric.MetricData{Value: 1100})
	fakeFetcher.SetContainerMetric("podUID2", "containerName", consts.MetricsContainerRootfsUsed, utilmetric.MetricData{Value: 800})
	fakeFetcher.SetContainerMetric("podUID2", "containerName", consts.MetricsContainerRootfsInodesUsed, utilmetric.MetricData{Value: 1200})
	resTop, err = rootfsPlugin.GetTopEvictionPods(context.TODO(), req)
	assert.NoError(t, err)
	assert.Equal(t, 1, len(resTop.TargetPods))
	assert.Equal(t, types.UID("podUID2"), resTop.TargetPods[0].UID)

	tc = &testConf{
		minimumFreeThreshold:       &evictionapi.ThresholdValue{Percentage: 0.1},
		minimumInodesFreeThreshold: &evictionapi.ThresholdValue{Percentage: 0.3},
	}
	rootfsPlugin = createRootfsPressureEvictionPlugin(tc, emitter, fakeFetcher)
	res, err = rootfsPlugin.ThresholdMet(context.TODO())
	assert.NoError(t, err)
	assert.Equal(t, pluginapi.ThresholdMetType_HARD_MET, res.MetType)

	fakeFetcher.SetContainerMetric("podUID1", "containerName", consts.MetricsContainerRootfsUsed, utilmetric.MetricData{Value: 799})
	fakeFetcher.SetContainerMetric("podUID1", "containerName", consts.MetricsContainerRootfsInodesUsed, utilmetric.MetricData{Value: 2})
	fakeFetcher.SetContainerMetric("podUID2", "containerName", consts.MetricsContainerRootfsUsed, utilmetric.MetricData{Value: 800})
	fakeFetcher.SetContainerMetric("podUID2", "containerName", consts.MetricsContainerRootfsInodesUsed, utilmetric.MetricData{Value: 1})
	resTop, err = rootfsPlugin.GetTopEvictionPods(context.TODO(), req)
	assert.NoError(t, err)
	assert.Equal(t, 1, len(resTop.TargetPods))
	assert.Equal(t, types.UID("podUID1"), resTop.TargetPods[0].UID)

	fakeFetcher.SetContainerMetric("podUID1", "containerName", consts.MetricsContainerRootfsUsed, utilmetric.MetricData{Value: 799})
	fakeFetcher.SetContainerMetric("podUID1", "containerName", consts.MetricsContainerRootfsInodesUsed, utilmetric.MetricData{Value: 2})
	fakeFetcher.SetContainerMetric("podUID2", "containerName", consts.MetricsContainerRootfsUsed, utilmetric.MetricData{Value: 800})
	fakeFetcher.SetContainerMetric("podUID2", "containerName", consts.MetricsContainerRootfsInodesUsed, utilmetric.MetricData{Value: 3})
	resTop, err = rootfsPlugin.GetTopEvictionPods(context.TODO(), req)
	assert.NoError(t, err)
	assert.Equal(t, 1, len(resTop.TargetPods))
	assert.Equal(t, types.UID("podUID2"), resTop.TargetPods[0].UID)
}

func TestPodRootfsPressureEvictionPlugin_GetTopEvictionPodsMetReclaimedPriority(t *testing.T) {
	emitter := metrics.DummyMetrics{}
	fakeFetcher := metric.NewFakeMetricsFetcher(emitter).(*metric.FakeMetricsFetcher)
	// create metric data without time.
	fakeFetcher.SetNodeMetric(consts.MetricsSystemRootfsAvailable, utilmetric.MetricData{Value: 2000})
	fakeFetcher.SetNodeMetric(consts.MetricsSystemRootfsUsed, utilmetric.MetricData{Value: 8000})
	fakeFetcher.SetNodeMetric(consts.MetricsSystemRootfsCapacity, utilmetric.MetricData{Value: 10000})

	fakeFetcher.SetNodeMetric(consts.MetricsSystemRootfsInodesFree, utilmetric.MetricData{Value: 2000})
	fakeFetcher.SetNodeMetric(consts.MetricsSystemRootfsInodesUsed, utilmetric.MetricData{Value: 8000})
	fakeFetcher.SetNodeMetric(consts.MetricsSystemRootfsInodes, utilmetric.MetricData{Value: 10000})

	tc := &testConf{
		minimumFreeThreshold:                 &evictionapi.ThresholdValue{Percentage: 0.3},
		minimumInodesFreeThreshold:           &evictionapi.ThresholdValue{Percentage: 0.3},
		reclaimedQoSPodUsedPriorityThreshold: &evictionapi.ThresholdValue{Quantity: resource.NewQuantity(500, resource.DecimalSI)},
	}
	rootfsPlugin := createRootfsPressureEvictionPlugin(tc, emitter, fakeFetcher)

	res, err := rootfsPlugin.ThresholdMet(context.TODO())
	assert.NoError(t, err)
	assert.Equal(t, pluginapi.ThresholdMetType_HARD_MET, res.MetType)

	req := makeGetTopNRequest()
	req.ActivePods[0].Annotations = map[string]string{}
	req.ActivePods[1].Annotations = map[string]string{"katalyst.kubewharf.io/qos_level": "reclaimed_cores"}
	fakeFetcher.SetContainerMetric("podUID1", "containerName", consts.MetricsContainerRootfsUsed, utilmetric.MetricData{Value: 800})
	fakeFetcher.SetContainerMetric("podUID1", "containerName", consts.MetricsContainerRootfsInodesUsed, utilmetric.MetricData{Value: 800})
	fakeFetcher.SetContainerMetric("podUID2", "containerName", consts.MetricsContainerRootfsUsed, utilmetric.MetricData{Value: 799})
	fakeFetcher.SetContainerMetric("podUID2", "containerName", consts.MetricsContainerRootfsInodesUsed, utilmetric.MetricData{Value: 799})
	resTop, err := rootfsPlugin.GetTopEvictionPods(context.TODO(), req)
	assert.NoError(t, err)
	assert.Equal(t, 1, len(resTop.TargetPods))
	assert.Equal(t, types.UID("podUID2"), resTop.TargetPods[0].UID)

	req.ActivePods[0].Annotations = map[string]string{}
	req.ActivePods[1].Annotations = map[string]string{"katalyst.kubewharf.io/qos_level": "reclaimed_cores"}
	fakeFetcher.SetContainerMetric("podUID1", "containerName", consts.MetricsContainerRootfsUsed, utilmetric.MetricData{Value: 800})
	fakeFetcher.SetContainerMetric("podUID1", "containerName", consts.MetricsContainerRootfsInodesUsed, utilmetric.MetricData{Value: 800})
	fakeFetcher.SetContainerMetric("podUID2", "containerName", consts.MetricsContainerRootfsUsed, utilmetric.MetricData{Value: 400})
	fakeFetcher.SetContainerMetric("podUID2", "containerName", consts.MetricsContainerRootfsInodesUsed, utilmetric.MetricData{Value: 400})
	resTop, err = rootfsPlugin.GetTopEvictionPods(context.TODO(), req)
	assert.NoError(t, err)
	assert.Equal(t, 1, len(resTop.TargetPods))
	assert.Equal(t, types.UID("podUID1"), resTop.TargetPods[0].UID)

	req.ActivePods[0].Annotations = map[string]string{"katalyst.kubewharf.io/qos_level": "reclaimed_cores"}
	req.ActivePods[1].Annotations = map[string]string{}
	fakeFetcher.SetContainerMetric("podUID1", "containerName", consts.MetricsContainerRootfsUsed, utilmetric.MetricData{Value: 800})
	fakeFetcher.SetContainerMetric("podUID1", "containerName", consts.MetricsContainerRootfsInodesUsed, utilmetric.MetricData{Value: 800})
	fakeFetcher.SetContainerMetric("podUID2", "containerName", consts.MetricsContainerRootfsUsed, utilmetric.MetricData{Value: 801})
	fakeFetcher.SetContainerMetric("podUID2", "containerName", consts.MetricsContainerRootfsInodesUsed, utilmetric.MetricData{Value: 801})
	resTop, err = rootfsPlugin.GetTopEvictionPods(context.TODO(), req)
	assert.NoError(t, err)
	assert.Equal(t, 1, len(resTop.TargetPods))
	assert.Equal(t, types.UID("podUID1"), resTop.TargetPods[0].UID)

	req.ActivePods[0].Annotations = map[string]string{"katalyst.kubewharf.io/qos_level": "reclaimed_cores"}
	req.ActivePods[1].Annotations = map[string]string{}
	fakeFetcher.SetContainerMetric("podUID1", "containerName", consts.MetricsContainerRootfsUsed, utilmetric.MetricData{Value: 400})
	fakeFetcher.SetContainerMetric("podUID1", "containerName", consts.MetricsContainerRootfsInodesUsed, utilmetric.MetricData{Value: 400})
	fakeFetcher.SetContainerMetric("podUID2", "containerName", consts.MetricsContainerRootfsUsed, utilmetric.MetricData{Value: 801})
	fakeFetcher.SetContainerMetric("podUID2", "containerName", consts.MetricsContainerRootfsInodesUsed, utilmetric.MetricData{Value: 801})
	resTop, err = rootfsPlugin.GetTopEvictionPods(context.TODO(), req)
	assert.NoError(t, err)
	assert.Equal(t, 1, len(resTop.TargetPods))
	assert.Equal(t, types.UID("podUID2"), resTop.TargetPods[0].UID)

	tc = &testConf{
		minimumFreeThreshold:                 &evictionapi.ThresholdValue{Percentage: 0.3},
		minimumInodesFreeThreshold:           &evictionapi.ThresholdValue{Percentage: 0.3},
		reclaimedQoSPodUsedPriorityThreshold: &evictionapi.ThresholdValue{Percentage: 0.1},
	}
	rootfsPlugin = createRootfsPressureEvictionPlugin(tc, emitter, fakeFetcher)

	res, err = rootfsPlugin.ThresholdMet(context.TODO())
	assert.NoError(t, err)
	assert.Equal(t, pluginapi.ThresholdMetType_HARD_MET, res.MetType)

	req.ActivePods[0].Annotations = map[string]string{}
	req.ActivePods[1].Annotations = map[string]string{"katalyst.kubewharf.io/qos_level": "reclaimed_cores"}
	fakeFetcher.SetContainerMetric("podUID1", "containerName", consts.MetricsContainerRootfsUsed, utilmetric.MetricData{Value: 1200})
	fakeFetcher.SetContainerMetric("podUID1", "containerName", consts.MetricsContainerRootfsInodesUsed, utilmetric.MetricData{Value: 1200})
	fakeFetcher.SetContainerMetric("podUID2", "containerName", consts.MetricsContainerRootfsUsed, utilmetric.MetricData{Value: 1100})
	fakeFetcher.SetContainerMetric("podUID2", "containerName", consts.MetricsContainerRootfsInodesUsed, utilmetric.MetricData{Value: 1100})
	resTop, err = rootfsPlugin.GetTopEvictionPods(context.TODO(), req)
	assert.NoError(t, err)
	assert.Equal(t, 1, len(resTop.TargetPods))
	assert.Equal(t, types.UID("podUID2"), resTop.TargetPods[0].UID)

	req.ActivePods[0].Annotations = map[string]string{}
	req.ActivePods[1].Annotations = map[string]string{"katalyst.kubewharf.io/qos_level": "reclaimed_cores"}
	fakeFetcher.SetContainerMetric("podUID1", "containerName", consts.MetricsContainerRootfsUsed, utilmetric.MetricData{Value: 1200})
	fakeFetcher.SetContainerMetric("podUID1", "containerName", consts.MetricsContainerRootfsInodesUsed, utilmetric.MetricData{Value: 1200})
	fakeFetcher.SetContainerMetric("podUID2", "containerName", consts.MetricsContainerRootfsUsed, utilmetric.MetricData{Value: 900})
	fakeFetcher.SetContainerMetric("podUID2", "containerName", consts.MetricsContainerRootfsInodesUsed, utilmetric.MetricData{Value: 900})
	resTop, err = rootfsPlugin.GetTopEvictionPods(context.TODO(), req)
	assert.NoError(t, err)
	assert.Equal(t, 1, len(resTop.TargetPods))
	assert.Equal(t, types.UID("podUID1"), resTop.TargetPods[0].UID)

	req.ActivePods[0].Annotations = map[string]string{"katalyst.kubewharf.io/qos_level": "reclaimed_cores"}
	req.ActivePods[1].Annotations = map[string]string{}
	fakeFetcher.SetContainerMetric("podUID1", "containerName", consts.MetricsContainerRootfsUsed, utilmetric.MetricData{Value: 1200})
	fakeFetcher.SetContainerMetric("podUID1", "containerName", consts.MetricsContainerRootfsInodesUsed, utilmetric.MetricData{Value: 1200})
	fakeFetcher.SetContainerMetric("podUID2", "containerName", consts.MetricsContainerRootfsUsed, utilmetric.MetricData{Value: 1300})
	fakeFetcher.SetContainerMetric("podUID2", "containerName", consts.MetricsContainerRootfsInodesUsed, utilmetric.MetricData{Value: 1300})
	resTop, err = rootfsPlugin.GetTopEvictionPods(context.TODO(), req)
	assert.NoError(t, err)
	assert.Equal(t, 1, len(resTop.TargetPods))
	assert.Equal(t, types.UID("podUID1"), resTop.TargetPods[0].UID)

	req.ActivePods[0].Annotations = map[string]string{"katalyst.kubewharf.io/qos_level": "reclaimed_cores"}
	req.ActivePods[1].Annotations = map[string]string{}
	fakeFetcher.SetContainerMetric("podUID1", "containerName", consts.MetricsContainerRootfsUsed, utilmetric.MetricData{Value: 900})
	fakeFetcher.SetContainerMetric("podUID1", "containerName", consts.MetricsContainerRootfsInodesUsed, utilmetric.MetricData{Value: 900})
	fakeFetcher.SetContainerMetric("podUID2", "containerName", consts.MetricsContainerRootfsUsed, utilmetric.MetricData{Value: 1300})
	fakeFetcher.SetContainerMetric("podUID2", "containerName", consts.MetricsContainerRootfsInodesUsed, utilmetric.MetricData{Value: 1300})
	resTop, err = rootfsPlugin.GetTopEvictionPods(context.TODO(), req)
	assert.NoError(t, err)
	assert.Equal(t, 1, len(resTop.TargetPods))
	assert.Equal(t, types.UID("podUID2"), resTop.TargetPods[0].UID)

	tc = &testConf{
		minimumFreeThreshold:       &evictionapi.ThresholdValue{Percentage: 0.3},
		minimumInodesFreeThreshold: &evictionapi.ThresholdValue{Percentage: 0.3},
	}
	rootfsPlugin = createRootfsPressureEvictionPlugin(tc, emitter, fakeFetcher)

	res, err = rootfsPlugin.ThresholdMet(context.TODO())
	assert.NoError(t, err)
	assert.Equal(t, pluginapi.ThresholdMetType_HARD_MET, res.MetType)

	req.ActivePods[0].Annotations = map[string]string{}
	req.ActivePods[1].Annotations = map[string]string{"katalyst.kubewharf.io/qos_level": "reclaimed_cores"}
	fakeFetcher.SetContainerMetric("podUID1", "containerName", consts.MetricsContainerRootfsUsed, utilmetric.MetricData{Value: 1200})
	fakeFetcher.SetContainerMetric("podUID1", "containerName", consts.MetricsContainerRootfsInodesUsed, utilmetric.MetricData{Value: 1200})
	fakeFetcher.SetContainerMetric("podUID2", "containerName", consts.MetricsContainerRootfsUsed, utilmetric.MetricData{Value: 1100})
	fakeFetcher.SetContainerMetric("podUID2", "containerName", consts.MetricsContainerRootfsInodesUsed, utilmetric.MetricData{Value: 1100})
	resTop, err = rootfsPlugin.GetTopEvictionPods(context.TODO(), req)
	assert.NoError(t, err)
	assert.Equal(t, 1, len(resTop.TargetPods))
	assert.Equal(t, types.UID("podUID1"), resTop.TargetPods[0].UID)

	req.ActivePods[0].Annotations = map[string]string{"katalyst.kubewharf.io/qos_level": "reclaimed_cores"}
	req.ActivePods[1].Annotations = map[string]string{}
	fakeFetcher.SetContainerMetric("podUID1", "containerName", consts.MetricsContainerRootfsUsed, utilmetric.MetricData{Value: 1200})
	fakeFetcher.SetContainerMetric("podUID1", "containerName", consts.MetricsContainerRootfsInodesUsed, utilmetric.MetricData{Value: 1200})
	fakeFetcher.SetContainerMetric("podUID2", "containerName", consts.MetricsContainerRootfsUsed, utilmetric.MetricData{Value: 1300})
	fakeFetcher.SetContainerMetric("podUID2", "containerName", consts.MetricsContainerRootfsInodesUsed, utilmetric.MetricData{Value: 1300})
	resTop, err = rootfsPlugin.GetTopEvictionPods(context.TODO(), req)
	assert.NoError(t, err)
	assert.Equal(t, 1, len(resTop.TargetPods))
	assert.Equal(t, types.UID("podUID2"), resTop.TargetPods[0].UID)
}

func TestPodRootfsPressureEvictionPlugin_GetTopEvictionPodsInodesMetReclaimedPriority(t *testing.T) {
	emitter := metrics.DummyMetrics{}
	fakeFetcher := metric.NewFakeMetricsFetcher(emitter).(*metric.FakeMetricsFetcher)
	// create metric data without time.
	fakeFetcher.SetNodeMetric(consts.MetricsSystemRootfsAvailable, utilmetric.MetricData{Value: 2000})
	fakeFetcher.SetNodeMetric(consts.MetricsSystemRootfsUsed, utilmetric.MetricData{Value: 8000})
	fakeFetcher.SetNodeMetric(consts.MetricsSystemRootfsCapacity, utilmetric.MetricData{Value: 10000})

	fakeFetcher.SetNodeMetric(consts.MetricsSystemRootfsInodesFree, utilmetric.MetricData{Value: 2000})
	fakeFetcher.SetNodeMetric(consts.MetricsSystemRootfsInodesUsed, utilmetric.MetricData{Value: 8000})
	fakeFetcher.SetNodeMetric(consts.MetricsSystemRootfsInodes, utilmetric.MetricData{Value: 10000})

	tc := &testConf{
		minimumFreeThreshold:                       &evictionapi.ThresholdValue{Percentage: 0.1},
		minimumInodesFreeThreshold:                 &evictionapi.ThresholdValue{Percentage: 0.3},
		reclaimedQosPodInodesUsedPriorityThreshold: &evictionapi.ThresholdValue{Quantity: resource.NewQuantity(500, resource.DecimalSI)},
	}
	rootfsPlugin := createRootfsPressureEvictionPlugin(tc, emitter, fakeFetcher)

	res, err := rootfsPlugin.ThresholdMet(context.TODO())
	assert.NoError(t, err)
	assert.Equal(t, pluginapi.ThresholdMetType_HARD_MET, res.MetType)

	req := makeGetTopNRequest()
	req.ActivePods[0].Annotations = map[string]string{}
	req.ActivePods[1].Annotations = map[string]string{"katalyst.kubewharf.io/qos_level": "reclaimed_cores"}
	fakeFetcher.SetContainerMetric("podUID1", "containerName", consts.MetricsContainerRootfsUsed, utilmetric.MetricData{Value: 800})
	fakeFetcher.SetContainerMetric("podUID1", "containerName", consts.MetricsContainerRootfsInodesUsed, utilmetric.MetricData{Value: 800})
	fakeFetcher.SetContainerMetric("podUID2", "containerName", consts.MetricsContainerRootfsUsed, utilmetric.MetricData{Value: 900})
	fakeFetcher.SetContainerMetric("podUID2", "containerName", consts.MetricsContainerRootfsInodesUsed, utilmetric.MetricData{Value: 700})
	resTop, err := rootfsPlugin.GetTopEvictionPods(context.TODO(), req)
	assert.NoError(t, err)
	assert.Equal(t, 1, len(resTop.TargetPods))
	assert.Equal(t, types.UID("podUID2"), resTop.TargetPods[0].UID)

	req.ActivePods[0].Annotations = map[string]string{}
	req.ActivePods[1].Annotations = map[string]string{"katalyst.kubewharf.io/qos_level": "reclaimed_cores"}
	fakeFetcher.SetContainerMetric("podUID1", "containerName", consts.MetricsContainerRootfsUsed, utilmetric.MetricData{Value: 300})
	fakeFetcher.SetContainerMetric("podUID1", "containerName", consts.MetricsContainerRootfsInodesUsed, utilmetric.MetricData{Value: 800})
	fakeFetcher.SetContainerMetric("podUID2", "containerName", consts.MetricsContainerRootfsUsed, utilmetric.MetricData{Value: 500})
	fakeFetcher.SetContainerMetric("podUID2", "containerName", consts.MetricsContainerRootfsInodesUsed, utilmetric.MetricData{Value: 400})
	resTop, err = rootfsPlugin.GetTopEvictionPods(context.TODO(), req)
	assert.NoError(t, err)
	assert.Equal(t, 1, len(resTop.TargetPods))
	assert.Equal(t, types.UID("podUID1"), resTop.TargetPods[0].UID)

	req.ActivePods[0].Annotations = map[string]string{"katalyst.kubewharf.io/qos_level": "reclaimed_cores"}
	req.ActivePods[1].Annotations = map[string]string{}
	fakeFetcher.SetContainerMetric("podUID1", "containerName", consts.MetricsContainerRootfsUsed, utilmetric.MetricData{Value: 800})
	fakeFetcher.SetContainerMetric("podUID1", "containerName", consts.MetricsContainerRootfsInodesUsed, utilmetric.MetricData{Value: 800})
	fakeFetcher.SetContainerMetric("podUID2", "containerName", consts.MetricsContainerRootfsUsed, utilmetric.MetricData{Value: 900})
	fakeFetcher.SetContainerMetric("podUID2", "containerName", consts.MetricsContainerRootfsInodesUsed, utilmetric.MetricData{Value: 900})
	resTop, err = rootfsPlugin.GetTopEvictionPods(context.TODO(), req)
	assert.NoError(t, err)
	assert.Equal(t, 1, len(resTop.TargetPods))
	assert.Equal(t, types.UID("podUID1"), resTop.TargetPods[0].UID)

	req.ActivePods[0].Annotations = map[string]string{"katalyst.kubewharf.io/qos_level": "reclaimed_cores"}
	req.ActivePods[1].Annotations = map[string]string{}
	fakeFetcher.SetContainerMetric("podUID1", "containerName", consts.MetricsContainerRootfsUsed, utilmetric.MetricData{Value: 900})
	fakeFetcher.SetContainerMetric("podUID1", "containerName", consts.MetricsContainerRootfsInodesUsed, utilmetric.MetricData{Value: 400})
	fakeFetcher.SetContainerMetric("podUID2", "containerName", consts.MetricsContainerRootfsUsed, utilmetric.MetricData{Value: 400})
	fakeFetcher.SetContainerMetric("podUID2", "containerName", consts.MetricsContainerRootfsInodesUsed, utilmetric.MetricData{Value: 900})
	resTop, err = rootfsPlugin.GetTopEvictionPods(context.TODO(), req)
	assert.NoError(t, err)
	assert.Equal(t, 1, len(resTop.TargetPods))
	assert.Equal(t, types.UID("podUID2"), resTop.TargetPods[0].UID)

	tc = &testConf{
		minimumFreeThreshold:                       &evictionapi.ThresholdValue{Percentage: 0.1},
		minimumInodesFreeThreshold:                 &evictionapi.ThresholdValue{Percentage: 0.3},
		reclaimedQosPodInodesUsedPriorityThreshold: &evictionapi.ThresholdValue{Percentage: 0.1},
	}
	rootfsPlugin = createRootfsPressureEvictionPlugin(tc, emitter, fakeFetcher)

	res, err = rootfsPlugin.ThresholdMet(context.TODO())
	assert.NoError(t, err)
	assert.Equal(t, pluginapi.ThresholdMetType_HARD_MET, res.MetType)

	req.ActivePods[0].Annotations = map[string]string{}
	req.ActivePods[1].Annotations = map[string]string{"katalyst.kubewharf.io/qos_level": "reclaimed_cores"}
	fakeFetcher.SetContainerMetric("podUID1", "containerName", consts.MetricsContainerRootfsUsed, utilmetric.MetricData{Value: 800})
	fakeFetcher.SetContainerMetric("podUID1", "containerName", consts.MetricsContainerRootfsInodesUsed, utilmetric.MetricData{Value: 1200})
	fakeFetcher.SetContainerMetric("podUID2", "containerName", consts.MetricsContainerRootfsUsed, utilmetric.MetricData{Value: 900})
	fakeFetcher.SetContainerMetric("podUID2", "containerName", consts.MetricsContainerRootfsInodesUsed, utilmetric.MetricData{Value: 1100})
	resTop, err = rootfsPlugin.GetTopEvictionPods(context.TODO(), req)
	assert.NoError(t, err)
	assert.Equal(t, 1, len(resTop.TargetPods))
	assert.Equal(t, types.UID("podUID2"), resTop.TargetPods[0].UID)

	req.ActivePods[0].Annotations = map[string]string{}
	req.ActivePods[1].Annotations = map[string]string{"katalyst.kubewharf.io/qos_level": "reclaimed_cores"}
	fakeFetcher.SetContainerMetric("podUID1", "containerName", consts.MetricsContainerRootfsUsed, utilmetric.MetricData{Value: 800})
	fakeFetcher.SetContainerMetric("podUID1", "containerName", consts.MetricsContainerRootfsInodesUsed, utilmetric.MetricData{Value: 1200})
	fakeFetcher.SetContainerMetric("podUID2", "containerName", consts.MetricsContainerRootfsUsed, utilmetric.MetricData{Value: 900})
	fakeFetcher.SetContainerMetric("podUID2", "containerName", consts.MetricsContainerRootfsInodesUsed, utilmetric.MetricData{Value: 900})
	resTop, err = rootfsPlugin.GetTopEvictionPods(context.TODO(), req)
	assert.NoError(t, err)
	assert.Equal(t, 1, len(resTop.TargetPods))
	assert.Equal(t, types.UID("podUID1"), resTop.TargetPods[0].UID)

	req.ActivePods[0].Annotations = map[string]string{"katalyst.kubewharf.io/qos_level": "reclaimed_cores"}
	req.ActivePods[1].Annotations = map[string]string{}
	fakeFetcher.SetContainerMetric("podUID1", "containerName", consts.MetricsContainerRootfsUsed, utilmetric.MetricData{Value: 1500})
	fakeFetcher.SetContainerMetric("podUID1", "containerName", consts.MetricsContainerRootfsInodesUsed, utilmetric.MetricData{Value: 1200})
	fakeFetcher.SetContainerMetric("podUID2", "containerName", consts.MetricsContainerRootfsUsed, utilmetric.MetricData{Value: 1300})
	fakeFetcher.SetContainerMetric("podUID2", "containerName", consts.MetricsContainerRootfsInodesUsed, utilmetric.MetricData{Value: 1300})
	resTop, err = rootfsPlugin.GetTopEvictionPods(context.TODO(), req)
	assert.NoError(t, err)
	assert.Equal(t, 1, len(resTop.TargetPods))
	assert.Equal(t, types.UID("podUID1"), resTop.TargetPods[0].UID)

	req.ActivePods[0].Annotations = map[string]string{"katalyst.kubewharf.io/qos_level": "reclaimed_cores"}
	req.ActivePods[1].Annotations = map[string]string{}
	fakeFetcher.SetContainerMetric("podUID1", "containerName", consts.MetricsContainerRootfsUsed, utilmetric.MetricData{Value: 1500})
	fakeFetcher.SetContainerMetric("podUID1", "containerName", consts.MetricsContainerRootfsInodesUsed, utilmetric.MetricData{Value: 900})
	fakeFetcher.SetContainerMetric("podUID2", "containerName", consts.MetricsContainerRootfsUsed, utilmetric.MetricData{Value: 1300})
	fakeFetcher.SetContainerMetric("podUID2", "containerName", consts.MetricsContainerRootfsInodesUsed, utilmetric.MetricData{Value: 1300})
	resTop, err = rootfsPlugin.GetTopEvictionPods(context.TODO(), req)
	assert.NoError(t, err)
	assert.Equal(t, 1, len(resTop.TargetPods))
	assert.Equal(t, types.UID("podUID2"), resTop.TargetPods[0].UID)

	tc = &testConf{
		minimumFreeThreshold:       &evictionapi.ThresholdValue{Percentage: 0.1},
		minimumInodesFreeThreshold: &evictionapi.ThresholdValue{Percentage: 0.3},
	}
	rootfsPlugin = createRootfsPressureEvictionPlugin(tc, emitter, fakeFetcher)

	res, err = rootfsPlugin.ThresholdMet(context.TODO())
	assert.NoError(t, err)
	assert.Equal(t, pluginapi.ThresholdMetType_HARD_MET, res.MetType)

	req.ActivePods[0].Annotations = map[string]string{}
	req.ActivePods[1].Annotations = map[string]string{"katalyst.kubewharf.io/qos_level": "reclaimed_cores"}
	fakeFetcher.SetContainerMetric("podUID1", "containerName", consts.MetricsContainerRootfsUsed, utilmetric.MetricData{Value: 1000})
	fakeFetcher.SetContainerMetric("podUID1", "containerName", consts.MetricsContainerRootfsInodesUsed, utilmetric.MetricData{Value: 2})
	fakeFetcher.SetContainerMetric("podUID2", "containerName", consts.MetricsContainerRootfsUsed, utilmetric.MetricData{Value: 1100})
	fakeFetcher.SetContainerMetric("podUID2", "containerName", consts.MetricsContainerRootfsInodesUsed, utilmetric.MetricData{Value: 1})
	resTop, err = rootfsPlugin.GetTopEvictionPods(context.TODO(), req)
	assert.NoError(t, err)
	assert.Equal(t, 1, len(resTop.TargetPods))
	assert.Equal(t, types.UID("podUID1"), resTop.TargetPods[0].UID)

	req.ActivePods[0].Annotations = map[string]string{"katalyst.kubewharf.io/qos_level": "reclaimed_cores"}
	req.ActivePods[1].Annotations = map[string]string{}
	fakeFetcher.SetContainerMetric("podUID1", "containerName", consts.MetricsContainerRootfsUsed, utilmetric.MetricData{Value: 1200})
	fakeFetcher.SetContainerMetric("podUID1", "containerName", consts.MetricsContainerRootfsInodesUsed, utilmetric.MetricData{Value: 1})
	fakeFetcher.SetContainerMetric("podUID2", "containerName", consts.MetricsContainerRootfsUsed, utilmetric.MetricData{Value: 1000})
	fakeFetcher.SetContainerMetric("podUID2", "containerName", consts.MetricsContainerRootfsInodesUsed, utilmetric.MetricData{Value: 2})
	resTop, err = rootfsPlugin.GetTopEvictionPods(context.TODO(), req)
	assert.NoError(t, err)
	assert.Equal(t, 1, len(resTop.TargetPods))
	assert.Equal(t, types.UID("podUID2"), resTop.TargetPods[0].UID)
}

func TestPodRootfsPressureEvictionPlugin_GetTopEvictionPodsMetExpire(t *testing.T) {
	metricTime := time.Now().Add(-65 * time.Second)

	emitter := metrics.DummyMetrics{}
	fakeFetcher := metric.NewFakeMetricsFetcher(emitter).(*metric.FakeMetricsFetcher)
	// create metric data without time.
	fakeFetcher.SetNodeMetric(consts.MetricsSystemRootfsAvailable, utilmetric.MetricData{Value: 2000})
	fakeFetcher.SetNodeMetric(consts.MetricsSystemRootfsUsed, utilmetric.MetricData{Value: 8000})
	fakeFetcher.SetNodeMetric(consts.MetricsSystemRootfsCapacity, utilmetric.MetricData{Value: 10000})

	fakeFetcher.SetNodeMetric(consts.MetricsSystemRootfsInodesFree, utilmetric.MetricData{Value: 2000})
	fakeFetcher.SetNodeMetric(consts.MetricsSystemRootfsInodesUsed, utilmetric.MetricData{Value: 8000})
	fakeFetcher.SetNodeMetric(consts.MetricsSystemRootfsInodes, utilmetric.MetricData{Value: 10000})

	tc := &testConf{
		minimumFreeThreshold:       &evictionapi.ThresholdValue{Percentage: 0.3},
		minimumInodesFreeThreshold: &evictionapi.ThresholdValue{Percentage: 0.3},
	}
	rootfsPlugin := createRootfsPressureEvictionPlugin(tc, emitter, fakeFetcher)

	res, err := rootfsPlugin.ThresholdMet(context.TODO())
	assert.NoError(t, err)
	assert.Equal(t, pluginapi.ThresholdMetType_HARD_MET, res.MetType)

	fakeFetcher.SetContainerMetric("podUID1", "containerName", consts.MetricsContainerRootfsUsed, utilmetric.MetricData{Value: 800, Time: &metricTime})
	fakeFetcher.SetContainerMetric("podUID1", "containerName", consts.MetricsContainerRootfsInodesUsed, utilmetric.MetricData{Value: 800, Time: &metricTime})
	fakeFetcher.SetContainerMetric("podUID2", "containerName", consts.MetricsContainerRootfsUsed, utilmetric.MetricData{Value: 799})
	fakeFetcher.SetContainerMetric("podUID2", "containerName", consts.MetricsContainerRootfsInodesUsed, utilmetric.MetricData{Value: 799})

	req := makeGetTopNRequest()
	resTop, err := rootfsPlugin.GetTopEvictionPods(context.TODO(), req)
	assert.NoError(t, err)
	assert.Equal(t, 1, len(resTop.TargetPods))
	assert.Equal(t, types.UID("podUID2"), resTop.TargetPods[0].UID)

	fakeFetcher.SetContainerMetric("podUID1", "containerName", consts.MetricsContainerRootfsUsed, utilmetric.MetricData{Value: 800, Time: &metricTime})
	fakeFetcher.SetContainerMetric("podUID1", "containerName", consts.MetricsContainerRootfsInodesUsed, utilmetric.MetricData{Value: 800, Time: &metricTime})
	fakeFetcher.SetContainerMetric("podUID2", "containerName", consts.MetricsContainerRootfsUsed, utilmetric.MetricData{Value: 799, Time: &metricTime})
	fakeFetcher.SetContainerMetric("podUID2", "containerName", consts.MetricsContainerRootfsInodesUsed, utilmetric.MetricData{Value: 799, Time: &metricTime})
	resTop, err = rootfsPlugin.GetTopEvictionPods(context.TODO(), req)
	assert.NoError(t, err)
	assert.Equal(t, 0, len(resTop.TargetPods))
}

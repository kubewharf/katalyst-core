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

package cpu

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/kubernetes/pkg/features"

	"github.com/kubewharf/katalyst-api/pkg/protocol/evictionplugin/v1alpha1"
	cpuutil "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/util"
	"github.com/kubewharf/katalyst-core/pkg/config"
	evictionconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/adminqos/eviction"
	"github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/pod"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
	utilMetric "github.com/kubewharf/katalyst-core/pkg/util/metric"
	"github.com/kubewharf/katalyst-core/pkg/util/native"
)

type fakeKubeletConfigFetcher struct {
	config *native.KubeletConfiguration
}

func (f *fakeKubeletConfigFetcher) GetKubeletConfig(ctx context.Context) (*native.KubeletConfiguration, error) {
	return f.config, nil
}

func makeMetaServer() *metaserver.MetaServer {
	return &metaserver.MetaServer{
		MetaAgent: &agent.MetaAgent{
			MetricsFetcher: metric.NewFakeMetricsFetcher(metrics.DummyMetrics{}).(*metric.FakeMetricsFetcher),
			KatalystMachineInfo: &machine.KatalystMachineInfo{
				CPUTopology: &machine.CPUTopology{
					NumCPUs: 100,
				},
			},
			PodFetcher: &pod.PodFetcherStub{
				PodList: []*v1.Pod{
					{
						ObjectMeta: metav1.ObjectMeta{
							Name: "pod1",
							UID:  types.UID("pod1"),
						},
						Spec: v1.PodSpec{
							Containers: []v1.Container{
								{Name: "c1"},
							},
						},
					},
				},
			},
		},
	}
}

func TestCPUSystemPressureEvictionPlugin_ThresholdMet(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name            string
		configSetup     func(*evictionconfig.CPUSystemPressureEvictionPluginConfiguration)
		setupHistory    func(*SystemPressureEvictionPlugin)
		expectedMetType v1alpha1.ThresholdMetType
		expectedScope   string
	}{
		{
			name: "threshold not met",
			configSetup: func(c *evictionconfig.CPUSystemPressureEvictionPluginConfiguration) {
				c.EnableCPUSystemEviction = true
				c.SystemLoadUpperBoundRatio = 0.8
				c.SystemLoadLowerBoundRatio = 0.5
				c.ThresholdMetPercentage = 0.5
				c.MetricRingSize = 10
				c.SystemEvictionMetricMode = evictionconfig.NodeMetric
			},
			setupHistory: func(p *SystemPressureEvictionPlugin) {
				p.nodeMetricsHistory = map[string]*cpuutil.MetricRing{
					consts.MetricLoad1MinSystem: cpuutil.CreateMetricRing(10),
				}
				for i := 0; i < 10; i++ {
					p.nodeMetricsHistory[consts.MetricLoad1MinSystem].Push(&cpuutil.MetricSnapshot{
						Info: cpuutil.MetricInfo{
							Name:       consts.MetricLoad1MinSystem,
							Value:      10,
							UpperBound: 80,
							LowerBound: 50,
						},
						Time: int64(i + 1),
					})
				}
			},
			expectedMetType: v1alpha1.ThresholdMetType_NOT_MET,
		},
		{
			name: "soft threshold met",
			configSetup: func(c *evictionconfig.CPUSystemPressureEvictionPluginConfiguration) {
				c.EnableCPUSystemEviction = true
				c.SystemLoadUpperBoundRatio = 0.8
				c.SystemLoadLowerBoundRatio = 0.5
				c.ThresholdMetPercentage = 0.5
				c.MetricRingSize = 10
				c.SystemEvictionMetricMode = evictionconfig.NodeMetric
			},
			setupHistory: func(p *SystemPressureEvictionPlugin) {
				p.nodeMetricsHistory = map[string]*cpuutil.MetricRing{
					consts.MetricLoad1MinSystem: cpuutil.CreateMetricRing(10),
				}
				for i := 0; i < 6; i++ {
					p.nodeMetricsHistory[consts.MetricLoad1MinSystem].Push(&cpuutil.MetricSnapshot{
						Info: cpuutil.MetricInfo{
							Name:       consts.MetricLoad1MinSystem,
							Value:      60,
							UpperBound: 80,
							LowerBound: 50,
						},
						Time: int64(i + 1),
					})
				}
			},
			expectedMetType: v1alpha1.ThresholdMetType_SOFT_MET,
			expectedScope:   consts.MetricLoad1MinContainer,
		},
		{
			name: "hard threshold met with usage",
			configSetup: func(c *evictionconfig.CPUSystemPressureEvictionPluginConfiguration) {
				c.EnableCPUSystemEviction = true
				c.SystemUsageUpperBoundRatio = 0.9
				c.SystemUsageLowerBoundRatio = 0.6
				c.ThresholdMetPercentage = 0.5
				c.MetricRingSize = 10
				c.SystemEvictionMetricMode = evictionconfig.NodeMetric
			},
			setupHistory: func(p *SystemPressureEvictionPlugin) {
				p.nodeMetricsHistory = map[string]*cpuutil.MetricRing{
					consts.MetricCPUUsageSystem: cpuutil.CreateMetricRing(10),
				}
				for i := 0; i < 6; i++ {
					p.nodeMetricsHistory[consts.MetricCPUUsageSystem].Push(&cpuutil.MetricSnapshot{
						Info: cpuutil.MetricInfo{
							Name:       consts.MetricCPUUsageSystem,
							Value:      95,
							UpperBound: 90,
							LowerBound: 60,
						},
						Time: int64(i + 1),
					})
				}
			},
			expectedMetType: v1alpha1.ThresholdMetType_HARD_MET,
			expectedScope:   consts.MetricCPUUsageContainer,
		},
		{
			name: "hard threshold ONLY",
			configSetup: func(c *evictionconfig.CPUSystemPressureEvictionPluginConfiguration) {
				c.EnableCPUSystemEviction = true
				c.SystemUsageUpperBoundRatio = 0.9
				c.SystemUsageLowerBoundRatio = 0.0 // disabled
				c.ThresholdMetPercentage = 0.5
				c.MetricRingSize = 10
				c.SystemEvictionMetricMode = evictionconfig.NodeMetric
			},
			setupHistory: func(p *SystemPressureEvictionPlugin) {
				p.nodeMetricsHistory = map[string]*cpuutil.MetricRing{
					consts.MetricCPUUsageSystem: cpuutil.CreateMetricRing(10),
				}
				for i := 0; i < 6; i++ {
					p.nodeMetricsHistory[consts.MetricCPUUsageSystem].Push(&cpuutil.MetricSnapshot{
						Info: cpuutil.MetricInfo{
							Name:       consts.MetricCPUUsageSystem,
							Value:      95,
							UpperBound: 90,
							LowerBound: 0,
						},
						Time: int64(i + 1),
					})
				}
			},
			expectedMetType: v1alpha1.ThresholdMetType_HARD_MET,
			expectedScope:   consts.MetricCPUUsageContainer,
		},
		{
			name: "soft threshold ONLY",
			configSetup: func(c *evictionconfig.CPUSystemPressureEvictionPluginConfiguration) {
				c.EnableCPUSystemEviction = true
				c.SystemUsageUpperBoundRatio = 0.0 // disabled
				c.SystemUsageLowerBoundRatio = 0.6
				c.ThresholdMetPercentage = 0.5
				c.MetricRingSize = 10
				c.SystemEvictionMetricMode = evictionconfig.NodeMetric
			},
			setupHistory: func(p *SystemPressureEvictionPlugin) {
				p.nodeMetricsHistory = map[string]*cpuutil.MetricRing{
					consts.MetricCPUUsageSystem: cpuutil.CreateMetricRing(10),
				}
				for i := 0; i < 6; i++ {
					p.nodeMetricsHistory[consts.MetricCPUUsageSystem].Push(&cpuutil.MetricSnapshot{
						Info: cpuutil.MetricInfo{
							Name:       consts.MetricCPUUsageSystem,
							Value:      95,
							UpperBound: 0,
							LowerBound: 60,
						},
						Time: int64(i + 1),
					})
				}
			},
			expectedMetType: v1alpha1.ThresholdMetType_SOFT_MET,
			expectedScope:   consts.MetricCPUUsageContainer,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			conf := config.NewConfiguration()
			dynConf := evictionconfig.NewCPUSystemPressureEvictionPluginConfiguration()
			tt.configSetup(dynConf)
			conf.GetDynamicConfiguration().EvictionConfiguration.CPUSystemPressureEvictionPluginConfiguration = dynConf

			metaServer := makeMetaServer()
			emitter := metrics.DummyMetrics{}

			plugin := NewCPUSystemPressureEvictionPlugin(nil, nil, metaServer, emitter, conf).(*SystemPressureEvictionPlugin)
			if tt.setupHistory != nil {
				tt.setupHistory(plugin)
			}

			resp, err := plugin.ThresholdMet(context.Background(), &v1alpha1.GetThresholdMetRequest{})
			assert.NoError(t, err)
			assert.Equal(t, tt.expectedMetType, resp.MetType)
			if tt.expectedMetType != v1alpha1.ThresholdMetType_NOT_MET {
				assert.Equal(t, tt.expectedScope, resp.EvictionScope)
			}
		})
	}
}

func TestCPUSystemPressureEvictionPlugin_GetTopEvictionPods(t *testing.T) {
	t.Parallel()
	pod1 := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: "pod1",
			UID:  "pod1",
		},
	}
	pod2 := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: "pod2",
			UID:  "pod2",
		},
	}

	conf := config.NewConfiguration()
	dynConf := evictionconfig.NewCPUSystemPressureEvictionPluginConfiguration()
	dynConf.EnableCPUSystemEviction = true
	dynConf.EvictionCoolDownTime = 0 // bypass cool down
	dynConf.GracePeriod = 0
	conf.GetDynamicConfiguration().EvictionConfiguration.CPUSystemPressureEvictionPluginConfiguration = dynConf

	metaServer := makeMetaServer()
	emitter := metrics.DummyMetrics{}

	plugin := NewCPUSystemPressureEvictionPlugin(nil, nil, metaServer, emitter, conf).(*SystemPressureEvictionPlugin)

	// Simulate overMetricName being set
	plugin.overMetricName = consts.MetricLoad1MinContainer

	// Setup pod metrics history to make pod2 have higher load than pod1
	plugin.podMetricsHistory = map[string]entries{
		consts.MetricLoad1MinContainer: {
			"pod1": cpuutil.CreateMetricRing(10),
			"pod2": cpuutil.CreateMetricRing(10),
		},
	}

	plugin.podMetricsHistory[consts.MetricLoad1MinContainer]["pod1"].Push(&cpuutil.MetricSnapshot{
		Info: cpuutil.MetricInfo{Name: consts.MetricLoad1MinContainer, Value: 10},
		Time: 1,
	})
	plugin.podMetricsHistory[consts.MetricLoad1MinContainer]["pod2"].Push(&cpuutil.MetricSnapshot{
		Info: cpuutil.MetricInfo{Name: consts.MetricLoad1MinContainer, Value: 50},
		Time: 1,
	})

	req := &v1alpha1.GetTopEvictionPodsRequest{
		ActivePods: []*v1.Pod{pod1, pod2},
		TopN:       1,
	}

	resp, err := plugin.GetTopEvictionPods(context.Background(), req)
	assert.NoError(t, err)
	assert.Len(t, resp.TargetPods, 1)
	assert.Equal(t, "pod2", resp.TargetPods[0].Name)
}

func TestCPUSystemPressureEvictionPlugin_collectMetrics(t *testing.T) {
	t.Parallel()
	conf := config.NewConfiguration()
	dynConf := evictionconfig.NewCPUSystemPressureEvictionPluginConfiguration()
	dynConf.EnableCPUSystemEviction = true
	dynConf.SystemEvictionMetricMode = evictionconfig.NodeMetric
	dynConf.SystemLoadUpperBoundRatio = 0.8
	dynConf.MetricRingSize = 10
	conf.GetDynamicConfiguration().EvictionConfiguration.CPUSystemPressureEvictionPluginConfiguration = dynConf

	fakeMetricsFetcher := metric.NewFakeMetricsFetcher(metrics.DummyMetrics{}).(*metric.FakeMetricsFetcher)
	tNow := time.Now()
	fakeMetricsFetcher.SetNodeMetric(consts.MetricLoad1MinSystem, utilMetric.MetricData{Value: 100, Time: &tNow})

	metaServer := makeMetaServer()
	metaServer.MetricsFetcher = fakeMetricsFetcher

	emitter := metrics.DummyMetrics{}

	plugin := NewCPUSystemPressureEvictionPlugin(nil, nil, metaServer, emitter, conf).(*SystemPressureEvictionPlugin)

	plugin.collectMetrics(context.Background())

	assert.NotNil(t, plugin.nodeMetricsHistory[consts.MetricLoad1MinSystem])
	assert.NotNil(t, plugin.nodeMetricsHistory[consts.MetricLoad1MinSystem].Queue[0])
	assert.Equal(t, float64(100), plugin.nodeMetricsHistory[consts.MetricLoad1MinSystem].Queue[0].Info.Value)
}

func TestCPUSystemPressureEvictionPlugin_collectMetrics_NodeMetric_WithCPUManager(t *testing.T) {
	t.Parallel()
	conf := config.NewConfiguration()
	dynConf := evictionconfig.NewCPUSystemPressureEvictionPluginConfiguration()
	dynConf.EnableCPUSystemEviction = true
	dynConf.SystemEvictionMetricMode = evictionconfig.NodeMetric
	dynConf.SystemUsageUpperBoundRatio = 0.8
	dynConf.MetricRingSize = 10
	dynConf.CheckCPUManager = true
	conf.GetDynamicConfiguration().EvictionConfiguration.CPUSystemPressureEvictionPluginConfiguration = dynConf

	fakeMetricsFetcher := metric.NewFakeMetricsFetcher(metrics.DummyMetrics{}).(*metric.FakeMetricsFetcher)
	tNow := time.Now()
	// Set node metric to 100
	fakeMetricsFetcher.SetNodeMetric(consts.MetricCPUUsageSystem, utilMetric.MetricData{Value: 100, Time: &tNow})
	// Set fake container metrics
	// pod1 is guaranteed (has guaranteed cpu > 0)
	fakeMetricsFetcher.SetContainerMetric("pod1", "c1", consts.MetricCPUUsageContainer, utilMetric.MetricData{Value: 30, Time: &tNow})
	// pod2 is not guaranteed
	fakeMetricsFetcher.SetContainerMetric("pod2", "c1", consts.MetricCPUUsageContainer, utilMetric.MetricData{Value: 40, Time: &tNow})

	metaServer := makeMetaServer()
	metaServer.MetricsFetcher = fakeMetricsFetcher
	metaServer.KatalystMachineInfo = &machine.KatalystMachineInfo{
		CPUTopology: &machine.CPUTopology{
			NumCPUs: 100,
		},
	}

	// Mock CPUManager to be enabled
	metaServer.KubeletConfigFetcher = &fakeKubeletConfigFetcher{
		config: &native.KubeletConfiguration{
			FeatureGates: map[string]bool{
				string(features.CPUManager): true,
			},
			CPUManagerPolicy: "static",
		},
	}

	// Because the real test uses native.PodGuaranteedCPUs, it expects both QOS to be guaranteed and CPU req to be integer.
	// But Katalyst uses QoS helper which requires specific labels if we don't mock it perfectly.
	// The simpler way to test this logic is to mock native.PodGuaranteedCPUs logic or ensure our test pod meets it.
	// native.PodGuaranteedCPUs returns the integer sum of cpu requests IF the pod is Guaranteed QoS.
	// To make a pod Guaranteed in k8s, ALL containers must have matching CPU and Memory requests and limits.
	// We will construct such a pod:
	pod1 := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: "pod1",
			UID:  types.UID("pod1"),
		},
		Spec: v1.PodSpec{
			Containers: []v1.Container{
				{
					Name: "c1",
					Resources: v1.ResourceRequirements{
						Requests: v1.ResourceList{
							v1.ResourceCPU:    *resource.NewQuantity(2, resource.DecimalSI),
							v1.ResourceMemory: *resource.NewQuantity(1024, resource.BinarySI),
						},
						Limits: v1.ResourceList{
							v1.ResourceCPU:    *resource.NewQuantity(2, resource.DecimalSI),
							v1.ResourceMemory: *resource.NewQuantity(1024, resource.BinarySI),
						},
					},
				},
			},
		},
		Status: v1.PodStatus{
			QOSClass: v1.PodQOSGuaranteed,
		},
	}
	metaServer.PodFetcher = &pod.PodFetcherStub{
		PodList: []*v1.Pod{
			pod1,
			{
				ObjectMeta: metav1.ObjectMeta{
					Name: "pod2",
					UID:  types.UID("pod2"),
				},
				Spec: v1.PodSpec{
					Containers: []v1.Container{
						{Name: "c1"},
					},
				},
			},
		},
	}

	emitter := metrics.DummyMetrics{}

	plugin := NewCPUSystemPressureEvictionPlugin(nil, nil, metaServer, emitter, conf).(*SystemPressureEvictionPlugin)

	plugin.collectMetrics(context.Background())

	assert.NotNil(t, plugin.nodeMetricsHistory[consts.MetricCPUUsageSystem])
	assert.NotNil(t, plugin.nodeMetricsHistory[consts.MetricCPUUsageSystem].Queue[0])
	// NodeMetric mode with CPUManager should subtract guaranteed pod1(30) from Node(100) -> 70
	assert.Equal(t, float64(70), plugin.nodeMetricsHistory[consts.MetricCPUUsageSystem].Queue[0].Info.Value)

	// Check if capacity was properly deducted by 2 CPUs
	assert.Equal(t, float64(98*0.8), plugin.nodeMetricsHistory[consts.MetricCPUUsageSystem].Queue[0].Info.UpperBound)
}

func TestCPUSystemPressureEvictionPlugin_collectMetrics_PodAggregated(t *testing.T) {
	t.Parallel()
	conf := config.NewConfiguration()
	dynConf := evictionconfig.NewCPUSystemPressureEvictionPluginConfiguration()
	dynConf.EnableCPUSystemEviction = true
	dynConf.SystemEvictionMetricMode = evictionconfig.PodAggregatedMetric
	dynConf.SystemUsageUpperBoundRatio = 0.8
	dynConf.MetricRingSize = 10
	conf.GetDynamicConfiguration().EvictionConfiguration.CPUSystemPressureEvictionPluginConfiguration = dynConf

	fakeMetricsFetcher := metric.NewFakeMetricsFetcher(metrics.DummyMetrics{}).(*metric.FakeMetricsFetcher)
	tNow := time.Now()
	// Set fake container metrics to test aggregation
	fakeMetricsFetcher.SetContainerMetric("pod1", "c1", consts.MetricCPUUsageContainer, utilMetric.MetricData{Value: 30, Time: &tNow})
	fakeMetricsFetcher.SetContainerMetric("pod2", "c1", consts.MetricCPUUsageContainer, utilMetric.MetricData{Value: 40, Time: &tNow})

	metaServer := makeMetaServer()
	metaServer.MetricsFetcher = fakeMetricsFetcher
	metaServer.PodFetcher = &pod.PodFetcherStub{
		PodList: []*v1.Pod{
			{
				ObjectMeta: metav1.ObjectMeta{
					Name: "pod1",
					UID:  types.UID("pod1"),
				},
				Spec: v1.PodSpec{
					Containers: []v1.Container{
						{Name: "c1"},
					},
				},
			},
			{
				ObjectMeta: metav1.ObjectMeta{
					Name: "pod2",
					UID:  types.UID("pod2"),
				},
				Spec: v1.PodSpec{
					Containers: []v1.Container{
						{Name: "c1"},
					},
				},
			},
		},
	}

	emitter := metrics.DummyMetrics{}

	plugin := NewCPUSystemPressureEvictionPlugin(nil, nil, metaServer, emitter, conf).(*SystemPressureEvictionPlugin)

	plugin.collectMetrics(context.Background())

	assert.NotNil(t, plugin.nodeMetricsHistory[consts.MetricCPUUsageSystem])
	assert.NotNil(t, plugin.nodeMetricsHistory[consts.MetricCPUUsageSystem].Queue[0])
	// pod1(30) + pod2(40) = 70
	assert.Equal(t, float64(70), plugin.nodeMetricsHistory[consts.MetricCPUUsageSystem].Queue[0].Info.Value)
}

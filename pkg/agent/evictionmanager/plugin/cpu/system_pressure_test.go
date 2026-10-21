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

	apiconsts "github.com/kubewharf/katalyst-api/pkg/consts"
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

// makeRankPod is a helper to construct a pod for ranking tests.
func makeRankPod(name string, qosLevel string, priority *int32) *v1.Pod {
	return makeRankPodWithSaleMode(name, qosLevel, priority, "")
}

func makeRankPodWithSaleMode(name string, qosLevel string, priority *int32, saleMode string) *v1.Pod {
	p := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
			UID:  types.UID(name),
		},
		Spec: v1.PodSpec{
			Priority: priority,
		},
	}
	annotations := map[string]string{}
	if qosLevel != "" {
		annotations[apiconsts.PodAnnotationQoSLevelKey] = qosLevel
	}
	if saleMode != "" {
		annotations[apiconsts.PodAnnotationSaleModeKey] = saleMode
	}
	if len(annotations) > 0 {
		p.Annotations = annotations
	}
	return p
}

// pushPodMetric pushes a metric value to the given plugin's pod metrics history.
func pushPodMetric(p *SystemPressureEvictionPlugin, metricName, podUID string, value float64) {
	if p.podMetricsHistory == nil {
		p.podMetricsHistory = map[string]entries{}
	}
	if _, ok := p.podMetricsHistory[metricName]; !ok {
		p.podMetricsHistory[metricName] = map[string]*cpuutil.MetricRing{}
	}
	if _, ok := p.podMetricsHistory[metricName][podUID]; !ok {
		p.podMetricsHistory[metricName][podUID] = cpuutil.CreateMetricRing(10)
	}
	p.podMetricsHistory[metricName][podUID].Push(&cpuutil.MetricSnapshot{
		Info: cpuutil.MetricInfo{Name: metricName, Value: value},
		Time: 1,
	})
}

// TestCPUSystemPressureEvictionPlugin_GetTopEvictionPods_RankByQoSAndPriority verifies that
// the eviction ranking honors the configured EvictionRankingMetrics order
// (QoS first, Priority second by default), and that overMetricName is appended
// only as a tie-breaker when it is not already in the configured list.
func TestCPUSystemPressureEvictionPlugin_GetTopEvictionPods_RankByQoSAndPriority(t *testing.T) {
	t.Parallel()

	prioHigh := int32(100)
	prioLow := int32(10)

	tests := []struct {
		name           string
		rankingMetrics []string
		overMetricName string
		// (name, qosLevel, priority)
		pods       []*v1.Pod
		topN       uint64
		metricVals map[string]float64 // overMetricName value per pod uid
		// expected first-evicted pod name
		expectedFirst string
	}{
		{
			name:           "default ranking: reclaimed_cores evicted first",
			rankingMetrics: evictionconfig.DefaultEvictionRankingMetrics,
			overMetricName: consts.MetricLoad1MinContainer,
			pods: []*v1.Pod{
				// shared_cores with high priority should be evicted last
				makeRankPod("shared-high", apiconsts.PodAnnotationQoSLevelSharedCores, &prioHigh),
				// reclaimed_cores has the lowest QoS, regardless of priority/metric value
				makeRankPod("reclaimed-low", apiconsts.PodAnnotationQoSLevelReclaimedCores, &prioHigh),
			},
			topN: 1,
			// even though shared-high has higher metric value, reclaimed_cores wins by QoS
			metricVals:    map[string]float64{"shared-high": 100, "reclaimed-low": 1},
			expectedFirst: "reclaimed-low",
		},
		{
			name:           "default ranking: same QoS, lower priority evicted first",
			rankingMetrics: evictionconfig.DefaultEvictionRankingMetrics,
			overMetricName: consts.MetricLoad1MinContainer,
			pods: []*v1.Pod{
				makeRankPod("shared-high", apiconsts.PodAnnotationQoSLevelSharedCores, &prioHigh),
				makeRankPod("shared-low", apiconsts.PodAnnotationQoSLevelSharedCores, &prioLow),
			},
			topN:          1,
			metricVals:    map[string]float64{"shared-high": 100, "shared-low": 1},
			expectedFirst: "shared-low",
		},
		{
			name:           "default ranking: same QoS and priority, spot sale mode evicted first",
			rankingMetrics: evictionconfig.DefaultEvictionRankingMetrics,
			overMetricName: consts.MetricLoad1MinContainer,
			pods: []*v1.Pod{
				makeRankPodWithSaleMode("reserved", apiconsts.PodAnnotationQoSLevelSharedCores, &prioHigh, apiconsts.PodSaleModeReserved),
				makeRankPodWithSaleMode("spot", apiconsts.PodAnnotationQoSLevelSharedCores, &prioHigh, apiconsts.PodSaleModeSpot),
			},
			topN: 1,
			// even though reserved has higher metric value, spot wins by sale mode
			metricVals:    map[string]float64{"reserved": 100, "spot": 1},
			expectedFirst: "spot",
		},
		{
			name:           "tie on QoS+Priority+SaleMode: fall back to overMetricName appended at the end",
			rankingMetrics: evictionconfig.DefaultEvictionRankingMetrics,
			overMetricName: consts.MetricLoad1MinContainer,
			pods: []*v1.Pod{
				makeRankPod("a", apiconsts.PodAnnotationQoSLevelSharedCores, &prioHigh),
				makeRankPod("b", apiconsts.PodAnnotationQoSLevelSharedCores, &prioHigh),
			},
			topN:          1,
			metricVals:    map[string]float64{"a": 1, "b": 99},
			expectedFirst: "b",
		},
		{
			name: "overMetricName already in rankingMetrics: keep configured order, no duplication",
			rankingMetrics: []string{
				evictionconfig.FakeMetricQoSLevel,
				evictionconfig.FakeMetricPriority,
				consts.MetricLoad1MinContainer,
			},
			overMetricName: consts.MetricLoad1MinContainer,
			pods: []*v1.Pod{
				// pod-a has higher metric, but lower priority pod-b should still win by priority
				makeRankPod("pod-a", apiconsts.PodAnnotationQoSLevelSharedCores, &prioHigh),
				makeRankPod("pod-b", apiconsts.PodAnnotationQoSLevelSharedCores, &prioLow),
			},
			topN:          1,
			metricVals:    map[string]float64{"pod-a": 100, "pod-b": 1},
			expectedFirst: "pod-b",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			conf := config.NewConfiguration()
			dynConf := evictionconfig.NewCPUSystemPressureEvictionPluginConfiguration()
			dynConf.EnableCPUSystemEviction = true
			dynConf.EvictionCoolDownTime = 0
			dynConf.GracePeriod = 0
			dynConf.EvictionRankingMetrics = tt.rankingMetrics
			conf.GetDynamicConfiguration().EvictionConfiguration.CPUSystemPressureEvictionPluginConfiguration = dynConf

			metaServer := makeMetaServer()
			emitter := metrics.DummyMetrics{}

			plugin := NewCPUSystemPressureEvictionPlugin(nil, nil, metaServer, emitter, conf).(*SystemPressureEvictionPlugin)
			plugin.overMetricName = tt.overMetricName

			if tt.overMetricName != "" {
				for uid, val := range tt.metricVals {
					pushPodMetric(plugin, tt.overMetricName, uid, val)
				}
			}

			req := &v1alpha1.GetTopEvictionPodsRequest{
				ActivePods: tt.pods,
				TopN:       tt.topN,
			}
			resp, err := plugin.GetTopEvictionPods(context.Background(), req)
			assert.NoError(t, err)
			assert.Len(t, resp.TargetPods, int(tt.topN))
			assert.Equal(t, tt.expectedFirst, resp.TargetPods[0].Name)
		})
	}
}

// TestCPUSystemPressureEvictionPlugin_getEvictionCmpFunc_OverMetricNotDuplicated verifies
// that when overMetricName already exists in EvictionRankingMetrics, the resulting
// comparator chain has the same length as EvictionRankingMetrics (i.e. no duplication
// or reordering happens).
func TestCPUSystemPressureEvictionPlugin_getEvictionCmpFunc_OverMetricNotDuplicated(t *testing.T) {
	t.Parallel()

	conf := config.NewConfiguration()
	dynConf := evictionconfig.NewCPUSystemPressureEvictionPluginConfiguration()
	dynConf.EvictionRankingMetrics = []string{
		evictionconfig.FakeMetricQoSLevel,
		evictionconfig.FakeMetricPriority,
		consts.MetricLoad1MinContainer,
	}
	conf.GetDynamicConfiguration().EvictionConfiguration.CPUSystemPressureEvictionPluginConfiguration = dynConf

	plugin := NewCPUSystemPressureEvictionPlugin(nil, nil, makeMetaServer(), metrics.DummyMetrics{}, conf).(*SystemPressureEvictionPlugin)

	// overMetricName already in the list.
	plugin.overMetricName = consts.MetricLoad1MinContainer
	cmpFuncs := plugin.getEvictionCmpFunc(dynConf)
	assert.Len(t, cmpFuncs, len(dynConf.EvictionRankingMetrics),
		"overMetricName already in rankingMetrics should not introduce extra cmp func")

	// overMetricName not in the list -> appended as tie-breaker.
	plugin.overMetricName = consts.MetricCPUUsageContainer
	cmpFuncs = plugin.getEvictionCmpFunc(dynConf)
	assert.Len(t, cmpFuncs, len(dynConf.EvictionRankingMetrics)+1,
		"overMetricName not in rankingMetrics should be appended as a tie-breaker")

	// overMetricName empty -> no append.
	plugin.overMetricName = ""
	cmpFuncs = plugin.getEvictionCmpFunc(dynConf)
	assert.Len(t, cmpFuncs, len(dynConf.EvictionRankingMetrics),
		"empty overMetricName should not introduce extra cmp func")
}

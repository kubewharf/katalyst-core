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

package strategy

import (
	"context"
	"fmt"
	"io/ioutil"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	resourcepluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	apiconsts "github.com/kubewharf/katalyst-api/pkg/consts"
	pluginapi "github.com/kubewharf/katalyst-api/pkg/protocol/evictionplugin/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/cpueviction/rules"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	util "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/util"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/qrm/statedirectory"
	"github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric"
	metrictypes "github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric/types"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/pod"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
	utilmetric "github.com/kubewharf/katalyst-core/pkg/util/metric"
)

const (
	defaultPressureMetricRingSize                           = 1
	defaultPressureCPUPressureEvictionPodGracePeriodSeconds = -1
	defaultPressureLoadUpperBoundRatio                      = 1.8
	defaultPressureLoadLowerBoundRatio                      = 1.0
	defaultPressureLoadThresholdMetPercentage               = 0.8
	defaultPressureReservedForAllocate                      = "4"
	defaultPressureReservedForReclaim                       = "4"
	defaultPressureReservedForSystem                        = 0
)

var (
	cpuTopology, _ = machine.GenerateDummyCPUTopology(16, 2, 4)
	conf           = makeConf(defaultPressureMetricRingSize, int64(defaultPressureCPUPressureEvictionPodGracePeriodSeconds),
		defaultPressureLoadUpperBoundRatio, defaultPressureLoadLowerBoundRatio,
		defaultPressureLoadThresholdMetPercentage, defaultPressureReservedForReclaim,
		defaultPressureReservedForAllocate, defaultPressureReservedForSystem)
)

func makeMetaServerWithPodList(metricsFetcher metrictypes.MetricsFetcher, cpuTopology *machine.CPUTopology, podList []*v1.Pod) *metaserver.MetaServer {
	metaServer := &metaserver.MetaServer{
		MetaAgent: &agent.MetaAgent{},
	}

	metaServer.MetricsFetcher = metricsFetcher
	metaServer.KatalystMachineInfo = &machine.KatalystMachineInfo{
		CPUTopology: cpuTopology,
	}

	metaServer.PodFetcher = &pod.PodFetcherStub{
		PodList: podList,
	}

	return metaServer
}

func TestNumaCPUPressureEviction_update(t *testing.T) {
	t.Parallel()
	testingDir, err := ioutil.TempDir("", "dynamic_policy_numa_metrics")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(testingDir)

	cpuTopology, _ := machine.GenerateDummyCPUTopology(16, 2, 4)
	stateDirectoryConfig := &statedirectory.StateDirectoryConfiguration{
		StateFileDirectory: testingDir,
	}
	state1, _ := state.NewCheckpointState(stateDirectoryConfig, "test", "test", cpuTopology, false, state.GenerateMachineStateFromPodEntries, metrics.DummyMetrics{})

	podEntry := state.PodEntries{
		"pod1": state.ContainerEntries{
			"test": &state.AllocationInfo{
				AllocationMeta: commonstate.AllocationMeta{
					PodUid:         "pod1",
					PodNamespace:   "test",
					PodName:        "test",
					ContainerName:  "test",
					ContainerType:  resourcepluginapi.ContainerType_MAIN.String(),
					ContainerIndex: 0,
					OwnerPoolName:  commonstate.PoolNameShare,
					Labels: map[string]string{
						apiconsts.PodAnnotationQoSLevelKey: apiconsts.PodAnnotationQoSLevelSharedCores,
					},
					Annotations: map[string]string{
						apiconsts.PodAnnotationQoSLevelKey: apiconsts.PodAnnotationQoSLevelSharedCores,
					},
					QoSLevel: apiconsts.PodAnnotationQoSLevelSharedCores,
				},
				RampUp:                   false,
				AllocationResult:         machine.MustParse("1,3-6,9,11-14"),
				OriginalAllocationResult: machine.MustParse("1,3-6,9,11-14"),
				TopologyAwareAssignments: map[int]machine.CPUSet{
					0: machine.NewCPUSet(1, 9),
					1: machine.NewCPUSet(3, 11),
					2: machine.NewCPUSet(4, 5, 11, 12),
					3: machine.NewCPUSet(6, 14),
				},
				OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
					0: machine.NewCPUSet(1, 9),
					1: machine.NewCPUSet(3, 11),
					2: machine.NewCPUSet(4, 5, 11, 12),
					3: machine.NewCPUSet(6, 14),
				},
				RequestQuantity: 2,
			},
		},
	}

	state1.SetPodEntries(podEntry, true)

	type fields struct {
		conf               *config.Configuration
		numaPressureConfig *rules.NumaPressureConfig
		metricsHistory     *util.NumaMetricHistory
		setFakeMetric      func(store *metric.FakeMetricsFetcher)
		podList            []*v1.Pod
		enabled            bool
	}

	type want struct {
		metricsHistory    *util.NumaMetricHistory
		overloadNumaCount int
	}

	tests := []struct {
		name   string
		fields fields
		want   want
	}{
		{
			name: "test0",
			fields: fields{
				enabled: false,
				podList: []*v1.Pod{
					{
						ObjectMeta: metav1.ObjectMeta{
							Name:      "pod1",
							Namespace: "default",
							UID:       "pod1",
							Annotations: map[string]string{
								"katalyst.kubewharf.io/qos_level": "shared_cores",
							},
						},
						Spec: v1.PodSpec{
							Containers: []v1.Container{
								{
									Name: "container",
								},
							},
						},
					},
				},
				conf: conf,
				numaPressureConfig: &rules.NumaPressureConfig{
					MetricRingSize:         1,
					ThresholdMetPercentage: conf.DynamicAgentConfiguration.GetDynamicConfiguration().ThresholdMetPercentage,
					GracePeriod:            conf.DynamicAgentConfiguration.GetDynamicConfiguration().DeletionGracePeriod,
					ExpandFactor:           1.2,
				},
				setFakeMetric: func(store *metric.FakeMetricsFetcher) {
					store.SetContainerNumaMetric("pod1", "container", 0,
						consts.MetricCPUUsageContainer, utilmetric.MetricData{Value: 2})
				},
				metricsHistory: util.NewMetricHistory(conf.DynamicAgentConfiguration.GetDynamicConfiguration().LoadMetricRingSize),
			},
			want: want{
				metricsHistory: &util.NumaMetricHistory{},
			},
		},
		{
			name: "test1",
			fields: fields{
				enabled: true,
				podList: []*v1.Pod{
					{
						ObjectMeta: metav1.ObjectMeta{
							Name:      "pod1",
							Namespace: "default",
							UID:       "pod1",
							Annotations: map[string]string{
								"katalyst.kubewharf.io/qos_level": "shared_cores",
							},
						},
						Spec: v1.PodSpec{
							Containers: []v1.Container{
								{
									Name: "container",
								},
							},
						},
					},
				},
				conf: conf,
				numaPressureConfig: &rules.NumaPressureConfig{
					MetricRingSize:         1,
					ThresholdMetPercentage: conf.DynamicAgentConfiguration.GetDynamicConfiguration().NumaCPUPressureEvictionConfiguration.ThresholdMetPercentage,
					GracePeriod:            conf.DynamicAgentConfiguration.GetDynamicConfiguration().DeletionGracePeriod,
					ExpandFactor:           1.2,
				},
				setFakeMetric: func(store *metric.FakeMetricsFetcher) {
					store.SetContainerNumaMetric("pod1", "container", 0,
						consts.MetricCPUUsageContainer, utilmetric.MetricData{Value: 2})
				},
				metricsHistory: util.NewMetricHistory(conf.DynamicAgentConfiguration.GetDynamicConfiguration().LoadMetricRingSize),
			},
			want: want{
				metricsHistory: &util.NumaMetricHistory{
					Inner: map[int]map[string]map[string]*util.MetricRing{
						0: {
							util.FakePodUID: {
								consts.MetricCPUUsageContainer: {
									MaxLen: 1,
									Queue: []*util.MetricSnapshot{
										{Info: util.MetricInfo{Name: consts.MetricCPUUsageContainer, Value: 0.5}},
									},
									CurrentIndex: 0,
								},
							},
							"pod1": {
								consts.MetricCPUUsageContainer: {
									MaxLen: 1,
									Queue: []*util.MetricSnapshot{
										{Info: util.MetricInfo{Name: consts.MetricCPUUsageContainer, Value: 0.5}},
									},
									CurrentIndex: 0,
								},
							},
						},
						1: {
							util.FakePodUID: {
								consts.MetricCPUUsageContainer: {
									MaxLen:       1,
									Queue:        []*util.MetricSnapshot{nil},
									CurrentIndex: 0,
								},
							},
						},
						2: {
							util.FakePodUID: {
								consts.MetricCPUUsageContainer: {
									MaxLen:       1,
									Queue:        []*util.MetricSnapshot{nil},
									CurrentIndex: 0,
								},
							},
						},
						3: {
							util.FakePodUID: {
								consts.MetricCPUUsageContainer: {
									MaxLen:       1,
									Queue:        []*util.MetricSnapshot{nil},
									CurrentIndex: 0,
								},
							},
						},
					},
				},
			},
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			metricsFetcher := metric.NewFakeMetricsFetcher(metrics.DummyMetrics{})

			metaServer := makeMetaServerWithPodList(metricsFetcher, cpuTopology, tt.fields.podList)

			store := metricsFetcher.(*metric.FakeMetricsFetcher)
			tt.fields.setFakeMetric(store)

			p := &NumaCPUPressureEviction{
				metaServer:         metaServer,
				conf:               tt.fields.conf,
				numaPressureConfig: tt.fields.numaPressureConfig,
				metricsHistory:     tt.fields.metricsHistory,
				emitter:            metrics.DummyMetrics{},
				state:              state1,
				enabled:            tt.fields.enabled,
			}

			p.update(context.TODO())
			if CompareMetricHistory(p.metricsHistory, tt.want.metricsHistory) {
				t.Errorf("update() got = %v, wantCpuCodeName %v", p.metricsHistory, tt.want)
			}
		})
	}
}

func CompareMetricHistory(mh1, mh2 *util.NumaMetricHistory) bool {
	if mh1.RingSize != mh2.RingSize {
		return false
	}

	if len(mh1.Inner) != len(mh2.Inner) {
		return false
	}

	for numaID, podMap1 := range mh1.Inner {
		podMap2, exists := mh2.Inner[numaID]
		if !exists {
			return false
		}

		if len(podMap1) != len(podMap2) {
			return false
		}

		for podUID, metricMap1 := range podMap1 {
			metricMap2, exists := podMap2[podUID]
			if !exists {
				return false
			}

			if len(metricMap1) != len(metricMap2) {
				return false
			}

			for metricName, ring1 := range metricMap1 {
				ring2, exists := metricMap2[metricName]
				if !exists {
					return false
				}

				if ring1.MaxLen != ring2.MaxLen || ring1.CurrentIndex != ring2.CurrentIndex {
					return false
				}

				if len(ring1.Queue) != len(ring2.Queue) {
					return false
				}

				for i, snapshot1 := range ring1.Queue {
					snapshot2 := ring2.Queue[i]
					if snapshot1.Info.Name != snapshot2.Info.Name || snapshot1.Info.Value != snapshot2.Info.Value {
						return false
					}
				}
			}
		}
	}

	return true
}

func TestNumaCPUPressureEviction_GetEvictPods(t *testing.T) {
	t.Parallel()
	p := &NumaCPUPressureEviction{}
	req := &pluginapi.GetEvictPodsRequest{}
	want := &pluginapi.GetEvictPodsResponse{}

	got, err := p.GetEvictPods(context.Background(), req)
	assert.NoError(t, err)
	assert.Equal(t, want, got)
}

func TestNumaCPUPressureEviction_GetTopEvictionPods(t *testing.T) {
	t.Parallel()
	type fields struct {
		conf               *config.Configuration
		numaPressureConfig *rules.NumaPressureConfig
		syncPeriod         time.Duration
		thresholds         map[string]float64
		metricsHistory     *util.NumaMetricHistory
		overloadNumaCount  int
		enabled            bool
		podList            []*v1.Pod
	}
	type args struct {
		request *pluginapi.GetTopEvictionPodsRequest
	}
	type wantResp struct {
		want        *pluginapi.GetTopEvictionPodsResponse
		wantPodList []*v1.Pod
	}

	pod1 := makePod("pod1")
	pod1.OwnerReferences = []metav1.OwnerReference{
		{
			Kind: "DaemonSet",
		},
	}
	pod2 := makePod("pod2")
	pod3 := makePod("pod3")

	conf := &config.Configuration{}

	tests := []struct {
		name     string
		fields   fields
		args     args
		wantResp wantResp
		wantErr  assert.ErrorAssertionFunc
	}{
		{
			name: "nil request",
			fields: fields{
				enabled:            true,
				numaPressureConfig: &rules.NumaPressureConfig{},
			},
			args: args{
				request: nil,
			},
			wantResp: wantResp{
				want: nil,
			},
			wantErr: assert.Error,
		},
		{
			name: "empty active pods",
			fields: fields{
				enabled:            true,
				numaPressureConfig: &rules.NumaPressureConfig{},
			},
			args: args{
				request: &pluginapi.GetTopEvictionPodsRequest{
					ActivePods: []*v1.Pod{},
				},
			},
			wantResp: wantResp{
				want: &pluginapi.GetTopEvictionPodsResponse{},
			},
			wantErr: assert.NoError,
		},
		{
			name: "plugin not enabled",
			fields: fields{
				enabled:            false,
				numaPressureConfig: &rules.NumaPressureConfig{},
			},
			args: args{
				request: &pluginapi.GetTopEvictionPodsRequest{
					ActivePods: []*v1.Pod{pod1},
				},
			},
			wantResp: wantResp{
				want: &pluginapi.GetTopEvictionPodsResponse{},
			},
			wantErr: assert.NoError,
		},
		{
			name: "not overload",
			fields: fields{
				enabled:            true,
				overloadNumaCount:  0,
				numaPressureConfig: &rules.NumaPressureConfig{},
			},
			args: args{
				request: &pluginapi.GetTopEvictionPodsRequest{
					ActivePods: []*v1.Pod{pod1},
				},
			},
			wantResp: wantResp{
				want: &pluginapi.GetTopEvictionPodsResponse{},
			},
			wantErr: assert.NoError,
		},
		{
			name: "all pods filtered",
			fields: fields{
				podList: []*v1.Pod{pod1},
				conf:    conf,
				numaPressureConfig: &rules.NumaPressureConfig{
					MetricRingSize:         2,
					ThresholdMetPercentage: 0.5,
					GracePeriod:            -1,
					ExpandFactor:           1.2,
					SkippedPodKinds:        []string{"DaemonSet"},
					EnabledFilters:         []string{rules.OwnerRefFilterName},
				},
				metricsHistory: &util.NumaMetricHistory{
					Inner: map[int]map[string]map[string]*util.MetricRing{
						0: {
							util.FakePodUID: {
								consts.MetricCPUUsageContainer: {
									MaxLen: 2,
									Queue: []*util.MetricSnapshot{
										{Info: util.MetricInfo{Name: consts.MetricCPUUsageContainer, Value: 1.0}},
										nil,
									},
									CurrentIndex: 1,
								},
							},
							"pod1": {
								consts.MetricCPUUsageContainer: {
									MaxLen: 2,
									Queue: []*util.MetricSnapshot{
										{Info: util.MetricInfo{Name: consts.MetricCPUUsageContainer, Value: 0.9}},
										nil,
									},
									CurrentIndex: 1,
								},
							},
						},
					},
					RingSize: 2,
				},
				thresholds:        map[string]float64{consts.MetricCPUUsageContainer: 0.8},
				enabled:           true,
				overloadNumaCount: 1,
			},
			args: args{
				request: &pluginapi.GetTopEvictionPodsRequest{
					ActivePods: []*v1.Pod{pod1},
				},
			},
			wantResp: wantResp{
				want: &pluginapi.GetTopEvictionPodsResponse{},
			},
			wantErr: assert.NoError,
		},
		{
			name: "some pods filtered, evict one",
			fields: fields{
				podList: []*v1.Pod{pod1, pod2, pod3},
				conf:    conf,
				numaPressureConfig: &rules.NumaPressureConfig{
					MetricRingSize:         2,
					ThresholdMetPercentage: 0.5,
					GracePeriod:            -1,
					ExpandFactor:           1.2,
					SkippedPodKinds:        []string{"DaemonSet"},
					EnabledFilters:         []string{rules.OwnerRefFilterName},
					EnabledScorers:         []string{rules.ScorerNameUsageGap},
				},
				metricsHistory: &util.NumaMetricHistory{
					Inner: map[int]map[string]map[string]*util.MetricRing{
						0: {
							util.FakePodUID: {
								consts.MetricCPUUsageContainer: {
									MaxLen: 2,
									Queue: []*util.MetricSnapshot{
										{Info: util.MetricInfo{Name: consts.MetricCPUUsageContainer, Value: 1.0}},
										nil,
									},
									CurrentIndex: 1,
								},
							},
							"pod1": {
								consts.MetricCPUUsageContainer: {
									MaxLen: 2,
									Queue: []*util.MetricSnapshot{
										{Info: util.MetricInfo{Name: consts.MetricCPUUsageContainer, Value: 0.5}},
										nil,
									},
									CurrentIndex: 1,
								},
							},
							"pod2": {
								consts.MetricCPUUsageContainer: {
									MaxLen: 2,
									Queue: []*util.MetricSnapshot{
										{Info: util.MetricInfo{Name: consts.MetricCPUUsageContainer, Value: 0.3}},
										nil,
									},
									CurrentIndex: 1,
								},
							},
							"pod3": {
								consts.MetricCPUUsageContainer: {
									MaxLen: 2,
									Queue: []*util.MetricSnapshot{
										{Info: util.MetricInfo{Name: consts.MetricCPUUsageContainer, Value: 0.2}},
										nil,
									},
									CurrentIndex: 1,
								},
							},
						},
					},
					RingSize: 2,
				},
				thresholds:        map[string]float64{consts.MetricCPUUsageContainer: 0.8},
				enabled:           true,
				overloadNumaCount: 1,
			},
			args: args{
				request: &pluginapi.GetTopEvictionPodsRequest{
					ActivePods: []*v1.Pod{pod1, pod2, pod3},
					TopN:       1,
				},
			},
			wantResp: wantResp{
				want: &pluginapi.GetTopEvictionPodsResponse{TargetPods: []*v1.Pod{pod2}},
			},
			wantErr: assert.NoError,
		},
		{
			name: "evict pod with highest score",
			fields: fields{
				podList: []*v1.Pod{pod1, pod2, pod3},
				conf:    conf,
				numaPressureConfig: &rules.NumaPressureConfig{
					MetricRingSize:         2,
					ThresholdMetPercentage: 0.5,
					GracePeriod:            -1,
					ExpandFactor:           1.2,
					EnabledScorers:         []string{rules.ScorerNameUsageGap},
				},
				metricsHistory: &util.NumaMetricHistory{
					Inner: map[int]map[string]map[string]*util.MetricRing{
						0: {
							util.FakePodUID: {
								consts.MetricCPUUsageContainer: {
									MaxLen: 2,
									Queue: []*util.MetricSnapshot{
										{Info: util.MetricInfo{Name: consts.MetricCPUUsageContainer, Value: 1.0}},
										nil,
									},
									CurrentIndex: 1,
								},
							},
							"pod1": {
								consts.MetricCPUUsageContainer: {
									MaxLen: 2,
									Queue: []*util.MetricSnapshot{
										{Info: util.MetricInfo{Name: consts.MetricCPUUsageContainer, Value: 0.5}},
										nil,
									},
									CurrentIndex: 1,
								},
							},
							"pod2": {
								consts.MetricCPUUsageContainer: {
									MaxLen: 2,
									Queue: []*util.MetricSnapshot{
										{Info: util.MetricInfo{Name: consts.MetricCPUUsageContainer, Value: 0.3}},
										nil,
									},
									CurrentIndex: 1,
								},
							},
							"pod3": {
								consts.MetricCPUUsageContainer: {
									MaxLen: 2,
									Queue: []*util.MetricSnapshot{
										{Info: util.MetricInfo{Name: consts.MetricCPUUsageContainer, Value: 0.1}},
										nil,
									},
									CurrentIndex: 1,
								},
							},
						},
					},
					RingSize: 2,
				},
				thresholds:        map[string]float64{consts.MetricCPUUsageContainer: 0.8},
				enabled:           true,
				overloadNumaCount: 1,
			},
			args: args{
				request: &pluginapi.GetTopEvictionPodsRequest{
					ActivePods: []*v1.Pod{pod1, pod2, pod3},
					TopN:       1,
				},
			},
			wantResp: wantResp{
				want: &pluginapi.GetTopEvictionPodsResponse{TargetPods: []*v1.Pod{pod2}},
			},
			wantErr: assert.NoError,
		},
		{
			name: "evict pod with grace period",
			fields: fields{
				podList: []*v1.Pod{pod1, pod2, pod3},
				conf:    conf,
				numaPressureConfig: &rules.NumaPressureConfig{
					MetricRingSize:         2,
					ThresholdMetPercentage: 0.5,
					GracePeriod:            30,
					ExpandFactor:           1.2,
					EnabledScorers:         []string{rules.ScorerNameUsageGap},
					SkippedPodKinds:        []string{"DaemonSet"},
					EnabledFilters:         []string{rules.OwnerRefFilterName},
				},
				metricsHistory: &util.NumaMetricHistory{
					Inner: map[int]map[string]map[string]*util.MetricRing{
						0: {
							util.FakePodUID: {
								consts.MetricCPUUsageContainer: {
									MaxLen: 2,
									Queue: []*util.MetricSnapshot{
										{Info: util.MetricInfo{Name: consts.MetricCPUUsageContainer, Value: 1.0}},
										nil,
									},
									CurrentIndex: 1,
								},
							},
							"pod1": {
								consts.MetricCPUUsageContainer: {
									MaxLen: 2,
									Queue: []*util.MetricSnapshot{
										{Info: util.MetricInfo{Name: consts.MetricCPUUsageContainer, Value: 0.5}},
										nil,
									},
									CurrentIndex: 1,
								},
							},
							"pod2": {
								consts.MetricCPUUsageContainer: {
									MaxLen: 2,
									Queue: []*util.MetricSnapshot{
										{Info: util.MetricInfo{Name: consts.MetricCPUUsageContainer, Value: 0.3}},
										nil,
									},
									CurrentIndex: 1,
								},
							},
							"pod3": {
								consts.MetricCPUUsageContainer: {
									MaxLen: 2,
									Queue: []*util.MetricSnapshot{
										{Info: util.MetricInfo{Name: consts.MetricCPUUsageContainer, Value: 0.1}},
										nil,
									},
									CurrentIndex: 1,
								},
							},
						},
					},
					RingSize: 2,
				},
				thresholds:        map[string]float64{consts.MetricCPUUsageContainer: 0.8},
				enabled:           true,
				overloadNumaCount: 1,
			},
			args: args{
				request: &pluginapi.GetTopEvictionPodsRequest{
					ActivePods: []*v1.Pod{pod1, pod2, pod3},
					TopN:       1,
				},
			},
			wantResp: wantResp{
				want: &pluginapi.GetTopEvictionPodsResponse{
					TargetPods: []*v1.Pod{pod2},
					DeletionOptions: &pluginapi.DeletionOptions{
						GracePeriodSeconds: 30,
					},
				},
			},
			wantErr: assert.NoError,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cpuTopology, err := machine.GenerateDummyCPUTopology(16, 2, 4)
			assert.NoError(t, err)
			metricsFetcher := metric.NewFakeMetricsFetcher(metrics.DummyMetrics{})
			metaServer := makeMetaServerWithPodList(metricsFetcher, cpuTopology, tt.fields.podList)

			filterer, err := rules.NewFilter(metrics.DummyMetrics{}, tt.fields.numaPressureConfig.EnabledFilters)
			assert.NoError(t, err)
			scorer, err := rules.NewScorer(metrics.DummyMetrics{}, tt.fields.numaPressureConfig.EnabledScorers)
			assert.NoError(t, err)
			p := &NumaCPUPressureEviction{
				RWMutex:            sync.RWMutex{},
				state:              nil,
				emitter:            metrics.DummyMetrics{},
				metaServer:         metaServer,
				conf:               tt.fields.conf,
				numaPressureConfig: tt.fields.numaPressureConfig,
				syncPeriod:         tt.fields.syncPeriod,
				thresholds:         tt.fields.thresholds,
				metricsHistory:     tt.fields.metricsHistory,
				overloadNumaCount:  tt.fields.overloadNumaCount,
				enabled:            tt.fields.enabled,
				filterer:           filterer,
				scorer:             scorer,
			}

			assert.NoError(t, err)

			if p.overloadNumaCount > 0 {
				_, _, _, err = p.pickTopOverRatioNuma(targetMetric, p.thresholds)
				assert.NoError(t, err)
			}

			got, err := p.GetTopEvictionPods(context.TODO(), tt.args.request)
			if !tt.wantErr(t, err, fmt.Sprintf("GetTopEvictionPods(%v)", tt.args.request)) {
				return
			}
			assert.Equalf(t, tt.wantResp.want, got, "GetTopEvictionPods(%v)", tt.args.request)
		})
	}
}

func TestNumaCPUPressureEviction_ThresholdMet(t *testing.T) {
	t.Parallel()
	pod1 := makePod("pod1")
	pod2 := makePod("pod2")
	pod3 := makePod("pod3")
	dsPod := makePod("dspod")
	dsPod.OwnerReferences = []metav1.OwnerReference{
		{
			Kind: "DaemonSet",
		},
	}
	rsPod := makePod("rspod")
	rsPod.OwnerReferences = []metav1.OwnerReference{
		{
			Kind: "ReplicaSet",
		},
	}
	type fields struct {
		conf               *config.Configuration
		numaPressureConfig *rules.NumaPressureConfig
		syncPeriod         time.Duration
		thresholds         map[string]float64
		metricsHistory     *util.NumaMetricHistory
		overloadNumaCount  int
		enabled            bool
		podList            []*v1.Pod
	}
	type args struct {
		req *pluginapi.GetThresholdMetRequest
	}
	tests := []struct {
		name    string
		fields  fields
		args    args
		want    *pluginapi.ThresholdMetResponse
		wantErr assert.ErrorAssertionFunc
	}{
		{
			name: "no overload numa",
			fields: fields{
				podList: []*v1.Pod{
					pod1,
					pod2,
					pod3,
				},
				conf: conf,
				numaPressureConfig: &rules.NumaPressureConfig{
					MetricRingSize:         2,
					ThresholdMetPercentage: 0.5,
					GracePeriod:            -1,
					ExpandFactor:           1.2,
				},
				metricsHistory: &util.NumaMetricHistory{
					Inner: map[int]map[string]map[string]*util.MetricRing{
						0: {
							util.FakePodUID: {
								consts.MetricCPUUsageContainer: {
									MaxLen: 2,
									Queue: []*util.MetricSnapshot{
										{Info: util.MetricInfo{Name: consts.MetricCPUUsageContainer, Value: 1.0}},
										nil,
									},
									CurrentIndex: 1,
								},
							},
							"pod1": {
								consts.MetricCPUUsageContainer: {
									MaxLen: 2,
									Queue: []*util.MetricSnapshot{
										{Info: util.MetricInfo{Name: consts.MetricCPUUsageContainer, Value: 0.5}},
										nil,
									},
									CurrentIndex: 1,
								},
							},
							"pod2": {
								consts.MetricCPUUsageContainer: {
									MaxLen: 2,
									Queue: []*util.MetricSnapshot{
										{Info: util.MetricInfo{Name: consts.MetricCPUUsageContainer, Value: 0.25}},
										nil,
									},
									CurrentIndex: 1,
								},
							},
							"pod3": {
								consts.MetricCPUUsageContainer: {
									MaxLen: 2,
									Queue: []*util.MetricSnapshot{
										{Info: util.MetricInfo{Name: consts.MetricCPUUsageContainer, Value: 0.25}},
										nil,
									},
									CurrentIndex: 1,
								},
							},
						},
					},
					RingSize: 2,
				},
				thresholds: map[string]float64{
					consts.MetricCPUUsageContainer: 0.8,
				},
				enabled:           true,
				overloadNumaCount: 0,
			},
			args: args{
				req: &pluginapi.GetThresholdMetRequest{
					ActivePods: []*v1.Pod{
						pod1,
						pod2,
						pod3,
					},
				},
			},
			want: &pluginapi.ThresholdMetResponse{
				MetType: pluginapi.ThresholdMetType_NOT_MET,
			},
			wantErr: assert.NoError,
		},
		{
			name: "return not met when not enabled",
			fields: fields{
				podList: []*v1.Pod{
					pod1,
					pod2,
					pod3,
				},
				conf: conf,
				numaPressureConfig: &rules.NumaPressureConfig{
					MetricRingSize:         2,
					ThresholdMetPercentage: 0.5,
					GracePeriod:            -1,
					ExpandFactor:           1.2,
				},
				metricsHistory: &util.NumaMetricHistory{
					Inner: map[int]map[string]map[string]*util.MetricRing{
						0: {
							util.FakePodUID: {
								consts.MetricCPUUsageContainer: {
									MaxLen: 2,
									Queue: []*util.MetricSnapshot{
										{Info: util.MetricInfo{Name: consts.MetricCPUUsageContainer, Value: 1.0}},
										nil,
									},
									CurrentIndex: 1,
								},
							},
							"pod1": {
								consts.MetricCPUUsageContainer: {
									MaxLen: 2,
									Queue: []*util.MetricSnapshot{
										{Info: util.MetricInfo{Name: consts.MetricCPUUsageContainer, Value: 0.5}},
										nil,
									},
									CurrentIndex: 1,
								},
							},
							"pod2": {
								consts.MetricCPUUsageContainer: {
									MaxLen: 2,
									Queue: []*util.MetricSnapshot{
										{Info: util.MetricInfo{Name: consts.MetricCPUUsageContainer, Value: 0.25}},
										nil,
									},
									CurrentIndex: 1,
								},
							},
							"pod3": {
								consts.MetricCPUUsageContainer: {
									MaxLen: 2,
									Queue: []*util.MetricSnapshot{
										{Info: util.MetricInfo{Name: consts.MetricCPUUsageContainer, Value: 0.25}},
										nil,
									},
									CurrentIndex: 1,
								},
							},
						},
					},
					RingSize: 2,
				},
				thresholds: map[string]float64{
					consts.MetricCPUUsageContainer: 0.8,
				},
				enabled:           false,
				overloadNumaCount: 0,
			},
			args: args{
				req: &pluginapi.GetThresholdMetRequest{
					ActivePods: []*v1.Pod{
						pod1,
						pod2,
						pod3,
					},
				},
			},
			want: &pluginapi.ThresholdMetResponse{
				MetType: pluginapi.ThresholdMetType_NOT_MET,
			},
			wantErr: assert.NoError,
		},
		{
			name: "node overload",
			fields: fields{
				enabled:           true,
				overloadNumaCount: 4,
				podList: []*v1.Pod{
					makePod("pod1"),
				},
				numaPressureConfig: &rules.NumaPressureConfig{
					EnabledFilters: []string{},
				},
			},
			args: args{
				req: &pluginapi.GetThresholdMetRequest{
					ActivePods: []*v1.Pod{
						pod1,
					},
				},
			},
			want: &pluginapi.ThresholdMetResponse{
				ThresholdValue:    1,
				ObservedValue:     1,
				ThresholdOperator: pluginapi.ThresholdOperator_GREATER_THAN,
				MetType:           pluginapi.ThresholdMetType_HARD_MET,
				EvictionScope:     targetMetric,
				Condition: &pluginapi.Condition{
					ConditionType: pluginapi.ConditionType_NODE_CONDITION,
					Effects:       []string{string(v1.TaintEffectNoSchedule)},
					ConditionName: evictionConditionCPUUsagePressure,
					MetCondition:  true,
				},
				CandidatePods: []*v1.Pod{
					pod1,
				},
			},
			wantErr: assert.NoError,
		},
		{
			name: "some pods filtered",
			fields: fields{
				enabled:           true,
				overloadNumaCount: 1,
				podList:           []*v1.Pod{dsPod, rsPod},
				numaPressureConfig: &rules.NumaPressureConfig{
					EnabledFilters:  []string{rules.OwnerRefFilterName},
					SkippedPodKinds: []string{"DaemonSet"},
				},
			},
			args: args{
				req: &pluginapi.GetThresholdMetRequest{
					ActivePods: []*v1.Pod{dsPod, rsPod},
				},
			},
			want: &pluginapi.ThresholdMetResponse{
				ThresholdValue:    1,
				ObservedValue:     1,
				ThresholdOperator: pluginapi.ThresholdOperator_GREATER_THAN,
				MetType:           pluginapi.ThresholdMetType_HARD_MET,
				EvictionScope:     targetMetric,
				CandidatePods: []*v1.Pod{
					rsPod,
				},
			},
			wantErr: assert.NoError,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			metricsFetcher := metric.NewFakeMetricsFetcher(metrics.DummyMetrics{})

			metaServer := makeMetaServerWithPodList(metricsFetcher, cpuTopology, tt.fields.podList)

			filterer, err := rules.NewFilter(metrics.DummyMetrics{}, tt.fields.numaPressureConfig.EnabledFilters)
			assert.NoError(t, err)
			scorer, err := rules.NewScorer(metrics.DummyMetrics{}, tt.fields.numaPressureConfig.EnabledScorers)
			assert.NoError(t, err)
			p := &NumaCPUPressureEviction{
				metaServer:         metaServer,
				conf:               tt.fields.conf,
				numaPressureConfig: tt.fields.numaPressureConfig,
				syncPeriod:         tt.fields.syncPeriod,
				thresholds:         tt.fields.thresholds,
				metricsHistory:     tt.fields.metricsHistory,
				overloadNumaCount:  tt.fields.overloadNumaCount,
				enabled:            tt.fields.enabled,
				emitter:            metrics.DummyMetrics{},
				filterer:           filterer,
				scorer:             scorer,
			}
			assert.NoError(t, err)

			got, err := p.ThresholdMet(context.TODO(), tt.args.req)
			if !tt.wantErr(t, err, fmt.Sprintf("ThresholdMet")) {
				return
			}
			assert.Equalf(t, tt.want, got, "ThresholdMet")
		})
	}
}

func TestNumaCPUPressureEviction_calOverloadNumaCount(t *testing.T) {
	t.Parallel()
	pod1 := makePod("pod1")
	pod2 := makePod("pod2")
	pod3 := makePod("pod3")
	type fields struct {
		metaServer         *metaserver.MetaServer
		conf               *config.Configuration
		numaPressureConfig *rules.NumaPressureConfig
		syncPeriod         time.Duration
		thresholds         map[string]float64
		metricsHistory     *util.NumaMetricHistory
		overloadNumaCount  int
		enabled            bool
		podList            []*v1.Pod
	}
	tests := []struct {
		name                  string
		fields                fields
		wantOverloadNumaCount int
	}{
		{
			name: "test1",
			fields: fields{
				enabled: true,
				podList: []*v1.Pod{
					pod1,
					pod2,
					pod3,
				},
				conf: conf,
				numaPressureConfig: &rules.NumaPressureConfig{
					MetricRingSize:         2,
					ThresholdMetPercentage: 0.5,
					GracePeriod:            -1,
					ExpandFactor:           1.2,
				},
				metricsHistory: &util.NumaMetricHistory{
					Inner: map[int]map[string]map[string]*util.MetricRing{
						0: {
							util.FakePodUID: {
								consts.MetricCPUUsageContainer: {
									MaxLen: 2,
									Queue: []*util.MetricSnapshot{
										{Info: util.MetricInfo{Name: consts.MetricCPUUsageContainer, Value: 1.0}},
										{Info: util.MetricInfo{Name: consts.MetricCPUUsageContainer, Value: 1.0}},
									},
									CurrentIndex: 1,
								},
							},
							"pod1": {
								consts.MetricCPUUsageContainer: {
									MaxLen: 2,
									Queue: []*util.MetricSnapshot{
										{Info: util.MetricInfo{Name: consts.MetricCPUUsageContainer, Value: 0.5}},
										{Info: util.MetricInfo{Name: consts.MetricCPUUsageContainer, Value: 0.5}},
									},
									CurrentIndex: 1,
								},
							},
							"pod2": {
								consts.MetricCPUUsageContainer: {
									MaxLen: 2,
									Queue: []*util.MetricSnapshot{
										{Info: util.MetricInfo{Name: consts.MetricCPUUsageContainer, Value: 0.25}},
										{Info: util.MetricInfo{Name: consts.MetricCPUUsageContainer, Value: 0.25}},
									},
									CurrentIndex: 1,
								},
							},
							"pod3": {
								consts.MetricCPUUsageContainer: {
									MaxLen: 2,
									Queue: []*util.MetricSnapshot{
										{Info: util.MetricInfo{Name: consts.MetricCPUUsageContainer, Value: 0.25}},
										{Info: util.MetricInfo{Name: consts.MetricCPUUsageContainer, Value: 0.25}},
									},
									CurrentIndex: 1,
								},
							},
						},
					},
					RingSize: 2,
				},
				thresholds: map[string]float64{
					consts.MetricCPUUsageContainer: 0.8,
				},
				overloadNumaCount: 2,
			},
			wantOverloadNumaCount: 1,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			metricsFetcher := metric.NewFakeMetricsFetcher(metrics.DummyMetrics{})

			metaServer := makeMetaServerWithPodList(metricsFetcher, cpuTopology, tt.fields.podList)
			p := &NumaCPUPressureEviction{
				conf:               tt.fields.conf,
				numaPressureConfig: tt.fields.numaPressureConfig,
				syncPeriod:         tt.fields.syncPeriod,
				thresholds:         tt.fields.thresholds,
				metricsHistory:     tt.fields.metricsHistory,
				overloadNumaCount:  tt.fields.overloadNumaCount,
				enabled:            tt.fields.enabled,
				metaServer:         metaServer,
				emitter:            metrics.DummyMetrics{},
			}
			assert.Equalf(t, tt.wantOverloadNumaCount, p.calOverloadNumaCount(), "calOverloadNumaCount()")
		})
	}
}

func makePod(name string) *v1.Pod {
	pod1 := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "default",
			UID:       types.UID(name),
			Annotations: map[string]string{
				"katalyst.kubewharf.io/qos_level": "shared_cores",
			},
		},
		Spec: v1.PodSpec{
			Containers: []v1.Container{
				{
					Name: "container",
				},
			},
		},
	}
	return pod1
}

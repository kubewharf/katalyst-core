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
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	constsapi "github.com/kubewharf/katalyst-api/pkg/consts"
	pluginapi "github.com/kubewharf/katalyst-api/pkg/protocol/evictionplugin/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/cpueviction/rules"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/util"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/qrm/statedirectory"
	"github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
	utilmetric "github.com/kubewharf/katalyst-core/pkg/util/metric"
)

func TestNumaSysCPUPressureEviction_GetEvictPods(t *testing.T) {
	t.Parallel()
	p := &NumaSysCPUPressureEviction{}
	req := &pluginapi.GetEvictPodsRequest{}
	want := &pluginapi.GetEvictPodsResponse{}

	got, err := p.GetEvictPods(context.Background(), req)
	assert.NoError(t, err)
	assert.Equal(t, want, got)
}

func TestNumaSysCPUPressureEviction_GetTopEvictionPods(t *testing.T) {
	t.Parallel()

	type fields struct {
		conf                *config.Configuration
		syncPeriod          time.Duration
		metricsHistory      *util.NumaMetricHistory
		evictionConfig      *NumaSysCPUPressureEvictionConfig
		numaSysOverStats    []rules.NumaSysOverStat
		deletionGracePeriod int64
		overloadNumaCount   int
		enabled             bool
		podList             []*v1.Pod
		podFilter           []PodFilter
	}
	type args struct {
		request *pluginapi.GetTopEvictionPodsRequest
	}
	type wantResp struct {
		want        *pluginapi.GetTopEvictionPodsResponse
		wantPodList []*v1.Pod
	}

	pod1 := makePod("pod1")
	pod1.Annotations = map[string]string{
		constsapi.PodAnnotationNUMABindResultKey: "0",
	}
	pod2 := makePod("pod2")
	pod2.Annotations = map[string]string{
		constsapi.PodAnnotationNUMABindResultKey: "0",
	}
	pod3 := makePod("pod3")
	pod3.Annotations = map[string]string{
		constsapi.PodAnnotationNUMABindResultKey: "1",
	}
	pod4 := makePod("pod4")
	pod4.Annotations = map[string]string{
		constsapi.PodAnnotationNUMABindResultKey: "xxx",
	}

	conf := &config.Configuration{}
	cpuTopology, err := machine.GenerateDummyCPUTopology(16, 2, 4)
	assert.NoError(t, err)
	metricsFetcher := metric.NewFakeMetricsFetcher(metrics.DummyMetrics{})
	metricsFetcher.(*metric.FakeMetricsFetcher).SetContainerMetric(string(pod1.UID), "container", containerCPUUsageMetric, utilmetric.MetricData{
		Value: 60,
	})
	metricsFetcher.(*metric.FakeMetricsFetcher).SetContainerMetric(string(pod1.UID), "container", containerSysCPUUsageMetric, utilmetric.MetricData{
		Value: 40,
	})
	metricsFetcher.(*metric.FakeMetricsFetcher).SetContainerMetric(string(pod2.UID), "container", containerCPUUsageMetric, utilmetric.MetricData{
		Value: 40,
	})
	metricsFetcher.(*metric.FakeMetricsFetcher).SetContainerMetric(string(pod2.UID), "container", containerSysCPUUsageMetric, utilmetric.MetricData{
		Value: 20,
	})
	metricsFetcher.(*metric.FakeMetricsFetcher).SetContainerMetric(string(pod3.UID), "container", containerCPUUsageMetric, utilmetric.MetricData{
		Value: 40,
	})
	metricsFetcher.(*metric.FakeMetricsFetcher).SetContainerMetric(string(pod3.UID), "container", containerSysCPUUsageMetric, utilmetric.MetricData{
		Value: 20,
	})
	metricsFetcher.(*metric.FakeMetricsFetcher).SetContainerMetric(string(pod4.UID), "container", containerCPUUsageMetric, utilmetric.MetricData{
		Value: 65,
	})
	metricsFetcher.(*metric.FakeMetricsFetcher).SetContainerMetric(string(pod4.UID), "container", containerSysCPUUsageMetric, utilmetric.MetricData{
		Value: 45,
	})

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
				enabled:        true,
				evictionConfig: &NumaSysCPUPressureEvictionConfig{},
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
				enabled:        true,
				evictionConfig: &NumaSysCPUPressureEvictionConfig{},
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
				enabled:        false,
				evictionConfig: &NumaSysCPUPressureEvictionConfig{},
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
				enabled:           true,
				overloadNumaCount: 0,
				evictionConfig:    &NumaSysCPUPressureEvictionConfig{},
				numaSysOverStats:  []rules.NumaSysOverStat{},
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
			name: "all pods are filtered",
			fields: fields{
				podList:           []*v1.Pod{pod1},
				conf:              conf,
				evictionConfig:    &NumaSysCPUPressureEvictionConfig{},
				enabled:           true,
				overloadNumaCount: 1,
				numaSysOverStats: []rules.NumaSysOverStat{
					{
						NumaID:                    0,
						NumaCPUUsageAvg:           0.5,
						NumaSysCPUUsageAvg:        0.2,
						IsNumaSysCPUUsageSoftOver: true,
						IsNumaCPUUsageSoftOver:    true,
					},
				},
				podFilter: []PodFilter{func(pod *v1.Pod) (bool, error) { return false, nil }},
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
			name: "parse numa id failed",
			fields: fields{
				podList: []*v1.Pod{pod1, pod2, pod3, pod4},
				conf:    conf,
				evictionConfig: &NumaSysCPUPressureEvictionConfig{
					NumaSysOverTotalUsageEvictionThreshold: 0.3,
				},
				enabled:           true,
				overloadNumaCount: 1,
				numaSysOverStats: []rules.NumaSysOverStat{
					{
						NumaID:                    0,
						IsNumaSysCPUUsageHardOver: true,
						IsNumaCPUUsageHardOver:    true,
					},
				},
			},
			args: args{
				request: &pluginapi.GetTopEvictionPodsRequest{
					ActivePods: []*v1.Pod{pod1, pod2, pod3, pod4},
					TopN:       1,
				},
			},
			wantResp: wantResp{
				want: &pluginapi.GetTopEvictionPodsResponse{TargetPods: []*v1.Pod{pod1}},
			},
			wantErr: assert.NoError,
		},
		{
			name: "no numa sys over load with hard met",
			fields: fields{
				podList: []*v1.Pod{pod1, pod2, pod3},
				conf:    conf,
				evictionConfig: &NumaSysCPUPressureEvictionConfig{
					NumaSysOverTotalUsageEvictionThreshold: 0.3,
				},
				enabled:           true,
				overloadNumaCount: 1,
				numaSysOverStats: []rules.NumaSysOverStat{
					{
						NumaID:                    0,
						IsNumaSysCPUUsageSoftOver: true,
						IsNumaCPUUsageSoftOver:    true,
					},
				},
			},
			args: args{
				request: &pluginapi.GetTopEvictionPodsRequest{
					ActivePods: []*v1.Pod{pod1, pod2, pod3},
					TopN:       1,
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
				evictionConfig: &NumaSysCPUPressureEvictionConfig{
					NumaSysOverTotalUsageEvictionThreshold: 0.3,
				},
				enabled:           true,
				overloadNumaCount: 1,
				numaSysOverStats: []rules.NumaSysOverStat{
					{
						NumaID:                    0,
						IsNumaSysCPUUsageHardOver: true,
						IsNumaCPUUsageHardOver:    true,
					},
				},
			},
			args: args{
				request: &pluginapi.GetTopEvictionPodsRequest{
					ActivePods: []*v1.Pod{pod1, pod2, pod3},
					TopN:       1,
				},
			},
			wantResp: wantResp{
				want: &pluginapi.GetTopEvictionPodsResponse{TargetPods: []*v1.Pod{pod1}},
			},
			wantErr: assert.NoError,
		},
		{
			name: "evict pod with grace period",
			fields: fields{
				podList: []*v1.Pod{pod1, pod2, pod3},
				conf:    conf,
				evictionConfig: &NumaSysCPUPressureEvictionConfig{
					NumaSysOverTotalUsageEvictionThreshold: 0.3,
				},
				deletionGracePeriod: 30,
				enabled:             true,
				overloadNumaCount:   1,
				numaSysOverStats: []rules.NumaSysOverStat{
					{
						NumaID:                    0,
						IsNumaSysCPUUsageHardOver: true,
						IsNumaCPUUsageHardOver:    true,
					},
				},
			},
			args: args{
				request: &pluginapi.GetTopEvictionPodsRequest{
					ActivePods: []*v1.Pod{pod1, pod2, pod3},
					TopN:       1,
				},
			},
			wantResp: wantResp{
				want: &pluginapi.GetTopEvictionPodsResponse{
					TargetPods: []*v1.Pod{pod1},
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
			metaServer := makeMetaServerWithPodList(metricsFetcher, cpuTopology, tt.fields.podList)
			evictionConfigGetter := func() *NumaSysCPUPressureEvictionConfig {
				return getNumaSysPressureConfig(conf.GetDynamicConfiguration())
			}

			p := &NumaSysCPUPressureEviction{
				metaServer:          metaServer,
				conf:                tt.fields.conf,
				syncPeriod:          tt.fields.syncPeriod,
				metricsHistory:      tt.fields.metricsHistory,
				overloadNumaCount:   tt.fields.overloadNumaCount,
				numaSysOverStats:    tt.fields.numaSysOverStats,
				enabled:             tt.fields.enabled,
				evictionConfig:      tt.fields.evictionConfig,
				deletionGracePeriod: tt.fields.deletionGracePeriod,
				emitter:             metrics.DummyMetrics{},

				podFilter:            []PodFilter{defaultPodFilter},
				evictionConfigGetter: evictionConfigGetter,
			}
			if len(tt.fields.podFilter) != 0 {
				p.podFilter = tt.fields.podFilter
			}

			got, err := p.GetTopEvictionPods(context.TODO(), tt.args.request)
			if !tt.wantErr(t, err, fmt.Sprintf("GetTopEvictionPods(%v)", tt.args.request)) {
				return
			}
			assert.Equalf(t, tt.wantResp.want, got, "GetTopEvictionPods(%v)", tt.args.request)
		})
	}
}

func TestNumaSysCPUPressureEviction_ThresholdMet(t *testing.T) {
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
		conf              *config.Configuration
		syncPeriod        time.Duration
		metricsHistory    *util.NumaMetricHistory
		evictionConfig    *NumaSysCPUPressureEvictionConfig
		numaSysOverStats  []rules.NumaSysOverStat
		overloadNumaCount int
		enabled           bool
		podList           []*v1.Pod
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
			name: "return not met when not enabled",
			fields: fields{
				podList: []*v1.Pod{
					pod1,
					pod2,
					pod3,
				},
				conf:              conf,
				evictionConfig:    &NumaSysCPUPressureEvictionConfig{},
				metricsHistory:    &util.NumaMetricHistory{},
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
			name: "no active pod in the req",
			fields: fields{
				podList: []*v1.Pod{
					pod1,
					pod2,
					pod3,
				},
				conf:              conf,
				evictionConfig:    &NumaSysCPUPressureEvictionConfig{},
				metricsHistory:    &util.NumaMetricHistory{},
				enabled:           false,
				overloadNumaCount: 0,
			},
			args: args{
				req: &pluginapi.GetThresholdMetRequest{
					ActivePods: []*v1.Pod{},
				},
			},
			want: &pluginapi.ThresholdMetResponse{
				MetType: pluginapi.ThresholdMetType_NOT_MET,
			},
			wantErr: assert.NoError,
		},
		{
			name: "no sys overload numa",
			fields: fields{
				podList: []*v1.Pod{
					pod1,
					pod2,
					pod3,
				},
				conf:           conf,
				evictionConfig: &NumaSysCPUPressureEvictionConfig{},
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
				enabled:           true,
				overloadNumaCount: 0,
				numaSysOverStats:  make([]rules.NumaSysOverStat, 0),
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
			name: "numa overload with hard met",
			fields: fields{
				enabled: true,
				podList: []*v1.Pod{
					makePod("pod1"),
				},
				evictionConfig: &NumaSysCPUPressureEvictionConfig{
					NumaSysOverTotalUsageHardThreshold: 0.5,
				},
				overloadNumaCount: 1,
				numaSysOverStats: []rules.NumaSysOverStat{
					{
						NumaID:                    0,
						NumaCPUUsageAvg:           0.5,
						NumaSysCPUUsageAvg:        0.3,
						IsNumaCPUUsageHardOver:    true,
						IsNumaSysCPUUsageHardOver: true,
					},
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
				ThresholdValue:    0.5,
				ObservedValue:     0.6,
				ThresholdOperator: pluginapi.ThresholdOperator_GREATER_THAN,
				MetType:           pluginapi.ThresholdMetType_HARD_MET,
				EvictionScope:     containerSysCPUUsageMetric,
				Condition: &pluginapi.Condition{
					ConditionType: pluginapi.ConditionType_NODE_CONDITION,
					Effects:       []string{string(v1.TaintEffectNoSchedule)},
					ConditionName: evictionConditionSysCPUUsagePressure,
					MetCondition:  true,
				},
			},
			wantErr: assert.NoError,
		},
		{
			name: "numa overload with soft met",
			fields: fields{
				enabled:           true,
				overloadNumaCount: 1,
				podList: []*v1.Pod{
					makePod("pod1"),
				},
				evictionConfig: &NumaSysCPUPressureEvictionConfig{
					NumaSysOverTotalUsageSoftThreshold: 0.3,
				},
				numaSysOverStats: []rules.NumaSysOverStat{
					{
						NumaID:                    0,
						NumaCPUUsageAvg:           0.5,
						NumaSysCPUUsageAvg:        0.2,
						IsNumaSysCPUUsageSoftOver: true,
						IsNumaCPUUsageSoftOver:    true,
					},
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
				ThresholdValue:    0.3,
				ObservedValue:     0.4,
				ThresholdOperator: pluginapi.ThresholdOperator_GREATER_THAN,
				MetType:           pluginapi.ThresholdMetType_SOFT_MET,
				EvictionScope:     containerSysCPUUsageMetric,
				Condition: &pluginapi.Condition{
					ConditionType: pluginapi.ConditionType_NODE_CONDITION,
					Effects:       []string{string(v1.TaintEffectNoSchedule)},
					ConditionName: evictionConditionSysCPUUsagePressure,
					MetCondition:  true,
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
			evictionConfigGetter := func() *NumaSysCPUPressureEvictionConfig {
				return getNumaSysPressureConfig(conf.GetDynamicConfiguration())
			}

			p := &NumaSysCPUPressureEviction{
				metaServer:        metaServer,
				conf:              tt.fields.conf,
				syncPeriod:        tt.fields.syncPeriod,
				metricsHistory:    tt.fields.metricsHistory,
				overloadNumaCount: tt.fields.overloadNumaCount,
				numaSysOverStats:  tt.fields.numaSysOverStats,
				enabled:           tt.fields.enabled,
				evictionConfig:    tt.fields.evictionConfig,
				emitter:           metrics.DummyMetrics{},

				podFilter:            []PodFilter{defaultPodFilter},
				evictionConfigGetter: evictionConfigGetter,
			}

			got, err := p.ThresholdMet(context.TODO(), tt.args.req)
			if !tt.wantErr(t, err, fmt.Sprintf("ThresholdMet")) {
				return
			}
			assert.Equalf(t, tt.want, got, "ThresholdMet")
		})
	}
}

func TestNumaSysCPUPressureEviction_sync(t *testing.T) {
	t.Parallel()

	testingDir, err := ioutil.TempDir("", "dynamic_policy_numa_metrics")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(testingDir)

	stateDirectoryConfig := &statedirectory.StateDirectoryConfiguration{
		StateFileDirectory: testingDir,
	}
	state1, err := state.NewCheckpointState(stateDirectoryConfig, "test", "test", cpuTopology, false, state.GenerateMachineStateFromPodEntries, metrics.DummyMetrics{})
	assert.NoError(t, err)

	machineState := state1.GetMachineState()
	machineState[0].PodEntries = state.PodEntries{
		"pod1": state.ContainerEntries{
			"container": &state.AllocationInfo{},
		},
		"pod3": state.ContainerEntries{
			"container": &state.AllocationInfo{},
		},
	}
	machineState[1].PodEntries = state.PodEntries{
		"pod2": state.ContainerEntries{
			"container": &state.AllocationInfo{},
		},
	}
	state1.SetMachineState(machineState, false)

	type metricData struct {
		podUID        string
		containerName string
		metricName    string
		value         float64
	}

	type fields struct {
		conf           *config.Configuration
		syncPeriod     time.Duration
		evictionConfig *NumaSysCPUPressureEvictionConfig
		podList        []*v1.Pod
		metricData     []metricData
	}

	pod1 := makePod("pod1")
	pod2 := makePod("pod2")
	pod3 := makePod("pod3")

	tests := []struct {
		name                  string
		fields                fields
		wantOverloadNumaCount int
		wantNumaSysOverStats  []rules.NumaSysOverStat
	}{
		{
			name: "plugin is disabled",
			fields: fields{
				podList:        []*v1.Pod{pod1, pod2, pod3},
				evictionConfig: &NumaSysCPUPressureEvictionConfig{},
			},
			wantOverloadNumaCount: 0,
			wantNumaSysOverStats:  []rules.NumaSysOverStat{},
		},
		{
			name: "sync with numa sys soft over",
			fields: fields{
				podList: []*v1.Pod{pod1, pod2, pod3},
				evictionConfig: &NumaSysCPUPressureEvictionConfig{
					EnableEviction:                     true,
					MetricRingSize:                     1,
					ThresholdMetPercentage:             0.8,
					NumaCPUUsageSoftThreshold:          0.3,
					NumaCPUUsageHardThreshold:          0.5,
					NumaSysOverTotalUsageSoftThreshold: 0.2,
					NumaSysOverTotalUsageHardThreshold: 0.5,
				},
				metricData: []metricData{
					{
						podUID:        string(pod1.UID),
						containerName: pod1.Spec.Containers[0].Name,
						metricName:    containerCPUUsageMetric,
						value:         0.9,
					},
					{
						podUID:        string(pod1.UID),
						containerName: pod1.Spec.Containers[0].Name,
						metricName:    containerSysCPUUsageMetric,
						value:         0.3,
					},
					{
						podUID:        string(pod2.UID),
						containerName: pod2.Spec.Containers[0].Name,
						metricName:    containerCPUUsageMetric,
						value:         0.6,
					},
					{
						podUID:        string(pod2.UID),
						containerName: pod2.Spec.Containers[0].Name,
						metricName:    containerSysCPUUsageMetric,
						value:         0.12,
					},
					{
						podUID:        string(pod3.UID),
						containerName: pod3.Spec.Containers[0].Name,
						metricName:    containerCPUUsageMetric,
						value:         0.7,
					},
					{
						podUID:        string(pod3.UID),
						containerName: pod3.Spec.Containers[0].Name,
						metricName:    containerSysCPUUsageMetric,
						value:         0.1,
					},
				},
			},
			wantOverloadNumaCount: 1,
			wantNumaSysOverStats: []rules.NumaSysOverStat{
				{
					NumaID:                    0,
					NumaCPUUsageAvg:           0.4,
					NumaSysCPUUsageAvg:        0.1,
					IsNumaSysCPUUsageSoftOver: true,
					IsNumaCPUUsageSoftOver:    true,
				},
			},
		},
		{
			name: "sync with numa sys hard over",
			fields: fields{
				podList: []*v1.Pod{pod1, pod2, pod3},
				evictionConfig: &NumaSysCPUPressureEvictionConfig{
					EnableEviction:                     true,
					MetricRingSize:                     1,
					ThresholdMetPercentage:             0.8,
					NumaCPUUsageSoftThreshold:          0.3,
					NumaCPUUsageHardThreshold:          0.5,
					NumaSysOverTotalUsageSoftThreshold: 0.2,
					NumaSysOverTotalUsageHardThreshold: 0.5,
				},
				metricData: []metricData{
					{
						podUID:        string(pod1.UID),
						containerName: pod1.Spec.Containers[0].Name,
						metricName:    containerCPUUsageMetric,
						value:         2,
					},
					{
						podUID:        string(pod1.UID),
						containerName: pod1.Spec.Containers[0].Name,
						metricName:    containerSysCPUUsageMetric,
						value:         1.70,
					},
					{
						podUID:        string(pod2.UID),
						containerName: pod2.Spec.Containers[0].Name,
						metricName:    containerCPUUsageMetric,
						value:         0.8,
					},
					{
						podUID:        string(pod2.UID),
						containerName: pod2.Spec.Containers[0].Name,
						metricName:    containerSysCPUUsageMetric,
						value:         0.6,
					},
					{
						podUID:        string(pod3.UID),
						containerName: pod3.Spec.Containers[0].Name,
						metricName:    containerCPUUsageMetric,
						value:         1.2,
					},
					{
						podUID:        string(pod3.UID),
						containerName: pod3.Spec.Containers[0].Name,
						metricName:    containerSysCPUUsageMetric,
						value:         0.70,
					},
				},
			},
			wantOverloadNumaCount: 1,
			wantNumaSysOverStats: []rules.NumaSysOverStat{
				{
					NumaID:                    0,
					NumaCPUUsageAvg:           0.8,
					NumaSysCPUUsageAvg:        0.6,
					IsNumaSysCPUUsageSoftOver: true,
					IsNumaCPUUsageSoftOver:    true,
					IsNumaSysCPUUsageHardOver: true,
					IsNumaCPUUsageHardOver:    true,
				},
			},
		},
		{
			name: "sync without numa sys over",
			fields: fields{
				podList: []*v1.Pod{pod1, pod2, pod3},
				evictionConfig: &NumaSysCPUPressureEvictionConfig{
					EnableEviction:                     true,
					MetricRingSize:                     2,
					ThresholdMetPercentage:             0.8,
					NumaCPUUsageSoftThreshold:          0.3,
					NumaCPUUsageHardThreshold:          0.5,
					NumaSysOverTotalUsageSoftThreshold: 0.2,
					NumaSysOverTotalUsageHardThreshold: 0.5,
				},
				metricData: []metricData{
					{
						podUID:        string(pod1.UID),
						containerName: pod1.Spec.Containers[0].Name,
						metricName:    containerCPUUsageMetric,
						value:         0.1,
					},
					{
						podUID:        string(pod1.UID),
						containerName: pod1.Spec.Containers[0].Name,
						metricName:    containerSysCPUUsageMetric,
						value:         0.05,
					},
					{
						podUID:        string(pod2.UID),
						containerName: pod2.Spec.Containers[0].Name,
						metricName:    containerCPUUsageMetric,
						value:         0.8,
					},
					{
						podUID:        string(pod2.UID),
						containerName: pod2.Spec.Containers[0].Name,
						metricName:    containerSysCPUUsageMetric,
						value:         0.6,
					},
					{
						podUID:        string(pod3.UID),
						containerName: pod3.Spec.Containers[0].Name,
						metricName:    containerCPUUsageMetric,
						value:         0.1,
					},
					{
						podUID:        string(pod3.UID),
						containerName: pod3.Spec.Containers[0].Name,
						metricName:    containerSysCPUUsageMetric,
						value:         0.05,
					},
				},
			},
			wantOverloadNumaCount: 0,
			wantNumaSysOverStats:  []rules.NumaSysOverStat{},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cpuTopology, err := machine.GenerateDummyCPUTopology(16, 2, 4)
			assert.NoError(t, err)
			metricsFetcher := metric.NewFakeMetricsFetcher(metrics.DummyMetrics{})
			metricsFetcherImpl := metricsFetcher.(*metric.FakeMetricsFetcher)
			metaServer := makeMetaServerWithPodList(metricsFetcher, cpuTopology, tt.fields.podList)

			for _, data := range tt.fields.metricData {
				metricsFetcherImpl.SetContainerMetric(data.podUID, data.containerName, data.metricName, utilmetric.MetricData{
					Value: data.value,
				})
			}

			evictionConfigGetter := func() *NumaSysCPUPressureEvictionConfig {
				return tt.fields.evictionConfig
			}

			p := &NumaSysCPUPressureEviction{
				metaServer:     metaServer,
				state:          state1,
				conf:           tt.fields.conf,
				syncPeriod:     tt.fields.syncPeriod,
				evictionConfig: tt.fields.evictionConfig,
				metricsHistory: util.NewMetricHistory(tt.fields.evictionConfig.MetricRingSize),
				emitter:        metrics.DummyMetrics{},

				numaSysOverStats:     []rules.NumaSysOverStat{},
				podFilter:            []PodFilter{defaultPodFilter},
				evictionConfigGetter: evictionConfigGetter,
			}

			p.sync(context.TODO())
			assert.Equalf(t, tt.wantOverloadNumaCount, p.overloadNumaCount, "sync")
			assert.Equalf(t, tt.wantNumaSysOverStats, p.numaSysOverStats, "sync")
		})
	}
}

func TestNumaSysCPUPressureEviction_updateNumaSysOverStat(t *testing.T) {
	t.Parallel()

	type fields struct {
		conf           *config.Configuration
		syncPeriod     time.Duration
		metricsHistory *util.NumaMetricHistory
		evictionConfig *NumaSysCPUPressureEvictionConfig
		enabled        bool
		podList        []*v1.Pod
	}

	pod1 := makePod("pod1")
	pod1.Annotations = map[string]string{
		constsapi.PodAnnotationNUMABindResultKey: "0",
	}
	pod2 := makePod("pod2")
	pod2.Annotations = map[string]string{
		constsapi.PodAnnotationNUMABindResultKey: "0",
	}
	pod3 := makePod("pod3")
	pod3.Annotations = map[string]string{
		constsapi.PodAnnotationNUMABindResultKey: "1",
	}
	pod4 := makePod("pod4")
	pod4.Annotations = map[string]string{
		constsapi.PodAnnotationNUMABindResultKey: "1",
	}

	conf := &config.Configuration{}
	cpuTopology, err := machine.GenerateDummyCPUTopology(16, 2, 4)
	assert.NoError(t, err)
	metricsFetcher := metric.NewFakeMetricsFetcher(metrics.DummyMetrics{})
	metricsFetcher.(*metric.FakeMetricsFetcher).SetContainerMetric(string(pod1.UID), "container", containerCPUUsageMetric, utilmetric.MetricData{
		Value: 60,
	})
	metricsFetcher.(*metric.FakeMetricsFetcher).SetContainerMetric(string(pod1.UID), "container", containerSysCPUUsageMetric, utilmetric.MetricData{
		Value: 40,
	})
	metricsFetcher.(*metric.FakeMetricsFetcher).SetContainerMetric(string(pod2.UID), "container", containerCPUUsageMetric, utilmetric.MetricData{
		Value: 40,
	})
	metricsFetcher.(*metric.FakeMetricsFetcher).SetContainerMetric(string(pod2.UID), "container", containerSysCPUUsageMetric, utilmetric.MetricData{
		Value: 20,
	})
	metricsFetcher.(*metric.FakeMetricsFetcher).SetContainerMetric(string(pod3.UID), "container", containerCPUUsageMetric, utilmetric.MetricData{
		Value: 40,
	})
	metricsFetcher.(*metric.FakeMetricsFetcher).SetContainerMetric(string(pod3.UID), "container", containerSysCPUUsageMetric, utilmetric.MetricData{
		Value: 20,
	})
	metricsFetcher.(*metric.FakeMetricsFetcher).SetContainerMetric(string(pod4.UID), "container", containerCPUUsageMetric, utilmetric.MetricData{
		Value: 65,
	})
	metricsFetcher.(*metric.FakeMetricsFetcher).SetContainerMetric(string(pod4.UID), "container", containerSysCPUUsageMetric, utilmetric.MetricData{
		Value: 45,
	})

	tests := []struct {
		name                  string
		fields                fields
		wantOverloadNumaCount int
		wantNumaSysOverStats  []rules.NumaSysOverStat
	}{
		{
			name: "metrics history is empty",
			fields: fields{
				metricsHistory: &util.NumaMetricHistory{},
			},
			wantOverloadNumaCount: 0,
			wantNumaSysOverStats:  []rules.NumaSysOverStat{},
		},
		{
			name: "no numa sys soft over",
			fields: fields{
				metricsHistory: &util.NumaMetricHistory{
					Inner: map[int]map[string]map[string]*util.MetricRing{
						0: {
							util.FakePodUID: {
								consts.MetricCPUUsageContainer: {
									MaxLen: 2,
									Queue: []*util.MetricSnapshot{
										{Info: util.MetricInfo{Name: consts.MetricCPUUsageContainer, Value: 0.25, LowerBound: 0.3, UpperBound: 0.5}},
										{Info: util.MetricInfo{Name: consts.MetricCPUUsageContainer, Value: 0.25, LowerBound: 0.3, UpperBound: 0.5}},
									},
									CurrentIndex: 1,
								},
								consts.MetricCPUUsageSysContainer: {
									MaxLen: 2,
									Queue: []*util.MetricSnapshot{
										{Info: util.MetricInfo{Name: consts.MetricCPUUsageSysContainer, Value: 0.25, LowerBound: 0.3, UpperBound: 0.5}},
										{Info: util.MetricInfo{Name: consts.MetricCPUUsageSysContainer, Value: 0.25, LowerBound: 0.3, UpperBound: 0.5}},
									},
									CurrentIndex: 1,
								},
							},
						},
						1: {
							util.FakePodUID: {
								consts.MetricCPUUsageContainer: {
									MaxLen: 2,
									Queue: []*util.MetricSnapshot{
										{Info: util.MetricInfo{Name: consts.MetricCPUUsageContainer, Value: 0.25, LowerBound: 0.3, UpperBound: 0.5}},
										{Info: util.MetricInfo{Name: consts.MetricCPUUsageSysContainer, Value: 0.25, LowerBound: 0.3, UpperBound: 0.5}},
									},
									CurrentIndex: 1,
								},
								consts.MetricCPUUsageSysContainer: {
									MaxLen: 2,
									Queue: []*util.MetricSnapshot{
										{Info: util.MetricInfo{Name: consts.MetricCPUUsageSysContainer, Value: 0.25, LowerBound: 0.3, UpperBound: 0.5}},
										{Info: util.MetricInfo{Name: consts.MetricCPUUsageSysContainer, Value: 0.25, LowerBound: 0.3, UpperBound: 0.5}},
									},
									CurrentIndex: 1,
								},
							},
						},
					},
					RingSize: 2,
				},
				evictionConfig: &NumaSysCPUPressureEvictionConfig{
					ThresholdMetPercentage: 0.8,
				},
			},
			wantOverloadNumaCount: 0,
			wantNumaSysOverStats:  []rules.NumaSysOverStat{},
		},
		{
			name: "is numa sys soft over",
			fields: fields{
				metricsHistory: &util.NumaMetricHistory{
					Inner: map[int]map[string]map[string]*util.MetricRing{
						0: {
							util.FakePodUID: {
								consts.MetricCPUUsageContainer: {
									MaxLen: 2,
									Queue: []*util.MetricSnapshot{
										{Info: util.MetricInfo{Name: consts.MetricCPUUsageContainer, Value: 0.2, LowerBound: 0.3, UpperBound: 0.5}},
										{Info: util.MetricInfo{Name: consts.MetricCPUUsageContainer, Value: 0.2, LowerBound: 0.3, UpperBound: 0.5}},
									},
									CurrentIndex: 1,
								},
								consts.MetricCPUUsageSysContainer: {
									MaxLen: 2,
									Queue: []*util.MetricSnapshot{
										{Info: util.MetricInfo{Name: consts.MetricCPUUsageSysContainer, Value: 0.2, LowerBound: 0.3, UpperBound: 0.5}},
										{Info: util.MetricInfo{Name: consts.MetricCPUUsageSysContainer, Value: 0.2, LowerBound: 0.3, UpperBound: 0.5}},
									},
									CurrentIndex: 1,
								},
							},
						},
						1: {
							util.FakePodUID: {
								consts.MetricCPUUsageContainer: {
									MaxLen: 2,
									Queue: []*util.MetricSnapshot{
										{Info: util.MetricInfo{Name: consts.MetricCPUUsageContainer, Value: 0.4, LowerBound: 0.3, UpperBound: 0.5}},
										{Info: util.MetricInfo{Name: consts.MetricCPUUsageSysContainer, Value: 0.4, LowerBound: 0.3, UpperBound: 0.5}},
									},
									CurrentIndex: 1,
								},
								consts.MetricCPUUsageSysContainer: {
									MaxLen: 2,
									Queue: []*util.MetricSnapshot{
										{Info: util.MetricInfo{Name: consts.MetricCPUUsageSysContainer, Value: 0.1, LowerBound: 0.3, UpperBound: 0.5}},
										{Info: util.MetricInfo{Name: consts.MetricCPUUsageSysContainer, Value: 0.1, LowerBound: 0.3, UpperBound: 0.5}},
									},
									CurrentIndex: 1,
								},
							},
						},
					},
					RingSize: 2,
				},
				evictionConfig: &NumaSysCPUPressureEvictionConfig{
					ThresholdMetPercentage:             0.8,
					MetricRingSize:                     2,
					NumaSysOverTotalUsageSoftThreshold: 0.2,
					NumaSysOverTotalUsageHardThreshold: 0.5,
				},
			},
			wantOverloadNumaCount: 1,
			wantNumaSysOverStats: []rules.NumaSysOverStat{
				{
					NumaID:             1,
					NumaCPUUsageAvg:    0.4,
					NumaSysCPUUsageAvg: 0.1,

					IsNumaCPUUsageSoftOver:    true,
					IsNumaCPUUsageHardOver:    false,
					IsNumaSysCPUUsageSoftOver: true,
					IsNumaSysCPUUsageHardOver: false,
				},
			},
		},
		{
			name: "is numa sys hard over",
			fields: fields{
				metricsHistory: &util.NumaMetricHistory{
					Inner: map[int]map[string]map[string]*util.MetricRing{
						0: {
							util.FakePodUID: {
								consts.MetricCPUUsageContainer: {
									MaxLen: 2,
									Queue: []*util.MetricSnapshot{
										{Info: util.MetricInfo{Name: consts.MetricCPUUsageContainer, Value: 0.6, LowerBound: 0.3, UpperBound: 0.5}},
										{Info: util.MetricInfo{Name: consts.MetricCPUUsageContainer, Value: 0.6, LowerBound: 0.3, UpperBound: 0.5}},
									},
									CurrentIndex: 1,
								},
								consts.MetricCPUUsageSysContainer: {
									MaxLen: 2,
									Queue: []*util.MetricSnapshot{
										{Info: util.MetricInfo{Name: consts.MetricCPUUsageSysContainer, Value: 0.35, LowerBound: 0.3, UpperBound: 0.5}},
										{Info: util.MetricInfo{Name: consts.MetricCPUUsageSysContainer, Value: 0.35, LowerBound: 0.3, UpperBound: 0.5}},
									},
									CurrentIndex: 1,
								},
							},
						},
						1: {
							util.FakePodUID: {
								consts.MetricCPUUsageContainer: {
									MaxLen: 2,
									Queue: []*util.MetricSnapshot{
										{Info: util.MetricInfo{Name: consts.MetricCPUUsageContainer, Value: 0.25, LowerBound: 0.3, UpperBound: 0.5}},
										{Info: util.MetricInfo{Name: consts.MetricCPUUsageSysContainer, Value: 0.25, LowerBound: 0.3, UpperBound: 0.5}},
									},
									CurrentIndex: 1,
								},
								consts.MetricCPUUsageSysContainer: {
									MaxLen: 2,
									Queue: []*util.MetricSnapshot{
										{Info: util.MetricInfo{Name: consts.MetricCPUUsageSysContainer, Value: 0.05, LowerBound: 0.3, UpperBound: 0.5}},
										{Info: util.MetricInfo{Name: consts.MetricCPUUsageSysContainer, Value: 0.05, LowerBound: 0.3, UpperBound: 0.5}},
									},
									CurrentIndex: 1,
								},
							},
						},
					},
					RingSize: 2,
				},
				evictionConfig: &NumaSysCPUPressureEvictionConfig{
					ThresholdMetPercentage:             0.8,
					MetricRingSize:                     2,
					NumaSysOverTotalUsageSoftThreshold: 0.3,
					NumaSysOverTotalUsageHardThreshold: 0.5,
				},
			},
			wantOverloadNumaCount: 1,
			wantNumaSysOverStats: []rules.NumaSysOverStat{
				{
					NumaID:             0,
					NumaCPUUsageAvg:    0.6,
					NumaSysCPUUsageAvg: 0.35,

					IsNumaCPUUsageSoftOver:    true,
					IsNumaCPUUsageHardOver:    true,
					IsNumaSysCPUUsageSoftOver: true,
					IsNumaSysCPUUsageHardOver: true,
				},
			},
		},
		{
			name: "with two numa sys over",
			fields: fields{
				metricsHistory: &util.NumaMetricHistory{
					Inner: map[int]map[string]map[string]*util.MetricRing{
						0: {
							util.FakePodUID: {
								consts.MetricCPUUsageContainer: {
									MaxLen: 2,
									Queue: []*util.MetricSnapshot{
										{Info: util.MetricInfo{Name: consts.MetricCPUUsageContainer, Value: 0.55, LowerBound: 0.3, UpperBound: 0.5}},
										{Info: util.MetricInfo{Name: consts.MetricCPUUsageContainer, Value: 0.55, LowerBound: 0.3, UpperBound: 0.5}},
									},
									CurrentIndex: 1,
								},
								consts.MetricCPUUsageSysContainer: {
									MaxLen: 2,
									Queue: []*util.MetricSnapshot{
										{Info: util.MetricInfo{Name: consts.MetricCPUUsageSysContainer, Value: 0.3, LowerBound: 0.3, UpperBound: 0.5}},
										{Info: util.MetricInfo{Name: consts.MetricCPUUsageSysContainer, Value: 0.3, LowerBound: 0.3, UpperBound: 0.5}},
									},
									CurrentIndex: 1,
								},
							},
						},
						1: {
							util.FakePodUID: {
								consts.MetricCPUUsageContainer: {
									MaxLen: 2,
									Queue: []*util.MetricSnapshot{
										{Info: util.MetricInfo{Name: consts.MetricCPUUsageContainer, Value: 0.65, LowerBound: 0.3, UpperBound: 0.5}},
										{Info: util.MetricInfo{Name: consts.MetricCPUUsageSysContainer, Value: 0.65, LowerBound: 0.3, UpperBound: 0.5}},
									},
									CurrentIndex: 1,
								},
								consts.MetricCPUUsageSysContainer: {
									MaxLen: 2,
									Queue: []*util.MetricSnapshot{
										{Info: util.MetricInfo{Name: consts.MetricCPUUsageSysContainer, Value: 0.37, LowerBound: 0.3, UpperBound: 0.5}},
										{Info: util.MetricInfo{Name: consts.MetricCPUUsageSysContainer, Value: 0.37, LowerBound: 0.3, UpperBound: 0.5}},
									},
									CurrentIndex: 1,
								},
							},
						},
					},
					RingSize: 2,
				},
				evictionConfig: &NumaSysCPUPressureEvictionConfig{
					ThresholdMetPercentage:             0.8,
					MetricRingSize:                     2,
					NumaSysOverTotalUsageSoftThreshold: 0.3,
					NumaSysOverTotalUsageHardThreshold: 0.5,
				},
			},
			wantOverloadNumaCount: 2,
			wantNumaSysOverStats: []rules.NumaSysOverStat{
				{
					NumaID:             1,
					NumaCPUUsageAvg:    0.65,
					NumaSysCPUUsageAvg: 0.37,

					IsNumaCPUUsageSoftOver:    true,
					IsNumaCPUUsageHardOver:    true,
					IsNumaSysCPUUsageSoftOver: true,
					IsNumaSysCPUUsageHardOver: true,
				},
				{
					NumaID:             0,
					NumaCPUUsageAvg:    0.55,
					NumaSysCPUUsageAvg: 0.30,

					IsNumaCPUUsageSoftOver:    true,
					IsNumaCPUUsageHardOver:    true,
					IsNumaSysCPUUsageSoftOver: true,
					IsNumaSysCPUUsageHardOver: true,
				},
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			metaServer := makeMetaServerWithPodList(metricsFetcher, cpuTopology, tt.fields.podList)
			evictionConfigGetter := func() *NumaSysCPUPressureEvictionConfig {
				return getNumaSysPressureConfig(conf.GetDynamicConfiguration())
			}

			p := &NumaSysCPUPressureEviction{
				metaServer:     metaServer,
				conf:           tt.fields.conf,
				syncPeriod:     tt.fields.syncPeriod,
				metricsHistory: tt.fields.metricsHistory,
				enabled:        tt.fields.enabled,
				evictionConfig: tt.fields.evictionConfig,
				emitter:        metrics.DummyMetrics{},

				numaSysOverStats:     []rules.NumaSysOverStat{},
				podFilter:            []PodFilter{defaultPodFilter},
				evictionConfigGetter: evictionConfigGetter,
			}

			p.updateNumaSysOverStat()
			assert.Equalf(t, tt.wantOverloadNumaCount, p.overloadNumaCount, "updateNumaSysOverStat")
			assert.Equalf(t, tt.wantNumaSysOverStats, p.numaSysOverStats, "updateNumaSysOverStat")
		})
	}
}

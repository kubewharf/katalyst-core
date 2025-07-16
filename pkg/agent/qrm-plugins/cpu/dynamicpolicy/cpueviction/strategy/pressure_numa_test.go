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
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric"
	metrictypes "github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric/types"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/pod"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
	utilmetric "github.com/kubewharf/katalyst-core/pkg/util/metric"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	"github.com/kubewharf/katalyst-core/pkg/util/native"
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
	state1, _ := state.NewCheckpointState(testingDir, "test", "test", cpuTopology, false, state.GenerateMachineStateFromPodEntries, metrics.DummyMetrics{})

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
		numaPressureConfig *NumaPressureConfig
		metricsHistory     *NumaMetricHistory
		setFakeMetric      func(store *metric.FakeMetricsFetcher)
		podList            []*v1.Pod
		enabled            bool
	}

	type want struct {
		metricsHistory    *NumaMetricHistory
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
				numaPressureConfig: &NumaPressureConfig{
					MetricRingSize:         1,
					ThresholdMetPercentage: conf.DynamicAgentConfiguration.GetDynamicConfiguration().ThresholdMetPercentage,
					GracePeriod:            conf.DynamicAgentConfiguration.GetDynamicConfiguration().DeletionGracePeriod,
					ExpandFactor:           1.2,
				},
				setFakeMetric: func(store *metric.FakeMetricsFetcher) {
					store.SetContainerNumaMetric("pod1", "container", 0,
						consts.MetricCPUUsageContainer, utilmetric.MetricData{Value: 2})
				},
				metricsHistory: NewMetricHistory(conf.DynamicAgentConfiguration.GetDynamicConfiguration().LoadMetricRingSize),
			},
			want: want{
				metricsHistory: &NumaMetricHistory{},
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
				numaPressureConfig: &NumaPressureConfig{
					MetricRingSize:         1,
					ThresholdMetPercentage: conf.DynamicAgentConfiguration.GetDynamicConfiguration().NumaCPUPressureEvictionConfiguration.ThresholdMetPercentage,
					GracePeriod:            conf.DynamicAgentConfiguration.GetDynamicConfiguration().DeletionGracePeriod,
					ExpandFactor:           1.2,
				},
				setFakeMetric: func(store *metric.FakeMetricsFetcher) {
					store.SetContainerNumaMetric("pod1", "container", 0,
						consts.MetricCPUUsageContainer, utilmetric.MetricData{Value: 2})
				},
				metricsHistory: NewMetricHistory(conf.DynamicAgentConfiguration.GetDynamicConfiguration().LoadMetricRingSize),
			},
			want: want{
				metricsHistory: &NumaMetricHistory{
					Inner: map[int]map[string]map[string]*MetricRing{
						0: {
							FakePodUID: {
								consts.MetricCPUUsageContainer: {
									MaxLen: 1,
									Queue: []*MetricSnapshot{
										{Info: MetricInfo{Name: consts.MetricCPUUsageContainer, Value: 0.5}},
									},
									CurrentIndex: 0,
								},
							},
							"pod1": {
								consts.MetricCPUUsageContainer: {
									MaxLen: 1,
									Queue: []*MetricSnapshot{
										{Info: MetricInfo{Name: consts.MetricCPUUsageContainer, Value: 0.5}},
									},
									CurrentIndex: 0,
								},
							},
						},
						1: {
							FakePodUID: {
								consts.MetricCPUUsageContainer: {
									MaxLen:       1,
									Queue:        []*MetricSnapshot{nil},
									CurrentIndex: 0,
								},
							},
						},
						2: {
							FakePodUID: {
								consts.MetricCPUUsageContainer: {
									MaxLen:       1,
									Queue:        []*MetricSnapshot{nil},
									CurrentIndex: 0,
								},
							},
						},
						3: {
							FakePodUID: {
								consts.MetricCPUUsageContainer: {
									MaxLen:       1,
									Queue:        []*MetricSnapshot{nil},
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

func CompareMetricHistory(mh1, mh2 *NumaMetricHistory) bool {
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
	type fields struct {
		conf               *config.Configuration
		numaPressureConfig *NumaPressureConfig
		syncPeriod         time.Duration
		thresholds         map[string]float64
		metricsHistory     *NumaMetricHistory
		overloadNumaCount  int
		enabled            bool
		podList            []*v1.Pod
	}
	type args struct {
		request *pluginapi.GetEvictPodsRequest
	}
	type wantResp struct {
		want        *pluginapi.GetEvictPodsResponse
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
				podList: []*v1.Pod{
					pod1,
					pod2,
					pod3,
				},
				conf: conf,
				numaPressureConfig: &NumaPressureConfig{
					MetricRingSize:         2,
					ThresholdMetPercentage: 0.5,
					GracePeriod:            -1,
					ExpandFactor:           1.2,
				},
				metricsHistory: &NumaMetricHistory{
					Inner: map[int]map[string]map[string]*MetricRing{
						0: {
							FakePodUID: {
								consts.MetricCPUUsageContainer: {
									MaxLen: 2,
									Queue: []*MetricSnapshot{
										{Info: MetricInfo{Name: consts.MetricCPUUsageContainer, Value: 1.0}},
										nil,
									},
									CurrentIndex: 1,
								},
							},
							"pod1": {
								consts.MetricCPUUsageContainer: {
									MaxLen: 2,
									Queue: []*MetricSnapshot{
										{Info: MetricInfo{Name: consts.MetricCPUUsageContainer, Value: 0.5}},
										nil,
									},
									CurrentIndex: 1,
								},
							},
							"pod2": {
								consts.MetricCPUUsageContainer: {
									MaxLen: 2,
									Queue: []*MetricSnapshot{
										{Info: MetricInfo{Name: consts.MetricCPUUsageContainer, Value: 0.25}},
										nil,
									},
									CurrentIndex: 1,
								},
							},
							"pod3": {
								consts.MetricCPUUsageContainer: {
									MaxLen: 2,
									Queue: []*MetricSnapshot{
										{Info: MetricInfo{Name: consts.MetricCPUUsageContainer, Value: 0.25}},
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
				overloadNumaCount: 1,
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
			name: "pod filtered by kind",
			fields: fields{
				podList: []*v1.Pod{
					pod1,
					pod2,
					pod3,
				},
				conf: conf,
				numaPressureConfig: &NumaPressureConfig{
					MetricRingSize:         2,
					ThresholdMetPercentage: 0.5,
					GracePeriod:            -1,
					ExpandFactor:           1.2,
					SkippedPodKinds:        []string{"DaemonSet"},
				},
				metricsHistory: &NumaMetricHistory{
					Inner: map[int]map[string]map[string]*MetricRing{
						0: {
							FakePodUID: {
								consts.MetricCPUUsageContainer: {
									MaxLen: 2,
									Queue: []*MetricSnapshot{
										{Info: MetricInfo{Name: consts.MetricCPUUsageContainer, Value: 1.0}},
										nil,
									},
									CurrentIndex: 1,
								},
							},
							"pod1": {
								consts.MetricCPUUsageContainer: {
									MaxLen: 2,
									Queue: []*MetricSnapshot{
										{Info: MetricInfo{Name: consts.MetricCPUUsageContainer, Value: 0.5}},
										nil,
									},
									CurrentIndex: 1,
								},
							},
							"pod2": {
								consts.MetricCPUUsageContainer: {
									MaxLen: 2,
									Queue: []*MetricSnapshot{
										{Info: MetricInfo{Name: consts.MetricCPUUsageContainer, Value: 0.25}},
										nil,
									},
									CurrentIndex: 1,
								},
							},
							"pod3": {
								consts.MetricCPUUsageContainer: {
									MaxLen: 2,
									Queue: []*MetricSnapshot{
										{Info: MetricInfo{Name: consts.MetricCPUUsageContainer, Value: 0.25}},
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
				overloadNumaCount: 1,
			},
			args: args{
				request: &pluginapi.GetEvictPodsRequest{
					ActivePods: []*v1.Pod{pod1},
				},
			},
			wantResp: wantResp{
				want: &pluginapi.GetEvictPodsResponse{},
			},
			wantErr: assert.NoError,
		},
		{
			name: "empty active pods",
			fields: fields{
				podList: []*v1.Pod{
					pod1,
					pod2,
					pod3,
				},
				conf: conf,
				numaPressureConfig: &NumaPressureConfig{
					MetricRingSize:         2,
					ThresholdMetPercentage: 0.5,
					GracePeriod:            -1,
					ExpandFactor:           1.2,
				},
				metricsHistory: &NumaMetricHistory{
					Inner: map[int]map[string]map[string]*MetricRing{
						0: {
							FakePodUID: {
								consts.MetricCPUUsageContainer: {
									MaxLen: 2,
									Queue: []*MetricSnapshot{
										{Info: MetricInfo{Name: consts.MetricCPUUsageContainer, Value: 1.0}},
										nil,
									},
									CurrentIndex: 1,
								},
							},
							"pod1": {
								consts.MetricCPUUsageContainer: {
									MaxLen: 2,
									Queue: []*MetricSnapshot{
										{Info: MetricInfo{Name: consts.MetricCPUUsageContainer, Value: 0.5}},
										nil,
									},
									CurrentIndex: 1,
								},
							},
							"pod2": {
								consts.MetricCPUUsageContainer: {
									MaxLen: 2,
									Queue: []*MetricSnapshot{
										{Info: MetricInfo{Name: consts.MetricCPUUsageContainer, Value: 0.25}},
										nil,
									},
									CurrentIndex: 1,
								},
							},
							"pod3": {
								consts.MetricCPUUsageContainer: {
									MaxLen: 2,
									Queue: []*MetricSnapshot{
										{Info: MetricInfo{Name: consts.MetricCPUUsageContainer, Value: 0.25}},
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
				overloadNumaCount: 1,
			},
			args: args{
				request: &pluginapi.GetEvictPodsRequest{
					ActivePods: []*v1.Pod{},
				},
			},
			wantResp: wantResp{
				want: &pluginapi.GetEvictPodsResponse{},
			},
			wantErr: assert.NoError,
		},
		{
			name: "return empty when not enabled",
			fields: fields{
				podList: []*v1.Pod{
					pod1,
					pod2,
					pod3,
				},
				conf: conf,
				numaPressureConfig: &NumaPressureConfig{
					MetricRingSize:         2,
					ThresholdMetPercentage: 0.5,
					GracePeriod:            -1,
					ExpandFactor:           1.2,
				},
				metricsHistory: &NumaMetricHistory{
					Inner: map[int]map[string]map[string]*MetricRing{
						0: {
							FakePodUID: {
								consts.MetricCPUUsageContainer: {
									MaxLen: 2,
									Queue: []*MetricSnapshot{
										{Info: MetricInfo{Name: consts.MetricCPUUsageContainer, Value: 1.0}},
										nil,
									},
									CurrentIndex: 1,
								},
							},
							"pod1": {
								consts.MetricCPUUsageContainer: {
									MaxLen: 2,
									Queue: []*MetricSnapshot{
										{Info: MetricInfo{Name: consts.MetricCPUUsageContainer, Value: 0.5}},
										nil,
									},
									CurrentIndex: 1,
								},
							},
							"pod2": {
								consts.MetricCPUUsageContainer: {
									MaxLen: 2,
									Queue: []*MetricSnapshot{
										{Info: MetricInfo{Name: consts.MetricCPUUsageContainer, Value: 0.25}},
										nil,
									},
									CurrentIndex: 1,
								},
							},
							"pod3": {
								consts.MetricCPUUsageContainer: {
									MaxLen: 2,
									Queue: []*MetricSnapshot{
										{Info: MetricInfo{Name: consts.MetricCPUUsageContainer, Value: 0.25}},
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
				overloadNumaCount: 1,
			},
			args: args{
				request: &pluginapi.GetEvictPodsRequest{
					ActivePods: []*v1.Pod{
						pod1,
						pod2,
						pod3,
					},
				},
			},
			wantResp: wantResp{
				want: &pluginapi.GetEvictPodsResponse{},
			},
			wantErr: assert.NoError,
		},
		{
			name: "kill overload pod",
			fields: fields{
				podList: []*v1.Pod{
					pod1,
					pod2,
					pod3,
				},
				conf: conf,
				numaPressureConfig: &NumaPressureConfig{
					MetricRingSize:         2,
					ThresholdMetPercentage: 0.5,
					GracePeriod:            -1,
					ExpandFactor:           1.2,
					CandidateCount:         2,
				},
				metricsHistory: &NumaMetricHistory{
					Inner: map[int]map[string]map[string]*MetricRing{
						0: {
							FakePodUID: {
								consts.MetricCPUUsageContainer: {
									MaxLen: 2,
									Queue: []*MetricSnapshot{
										{Info: MetricInfo{Name: consts.MetricCPUUsageContainer, Value: 1.0}},
										nil,
									},
									CurrentIndex: 1,
								},
							},
							"pod1": {
								consts.MetricCPUUsageContainer: {
									MaxLen: 2,
									Queue: []*MetricSnapshot{
										{Info: MetricInfo{Name: consts.MetricCPUUsageContainer, Value: 0.5}},
										nil,
									},
									CurrentIndex: 1,
								},
							},
							"pod2": {
								consts.MetricCPUUsageContainer: {
									MaxLen: 2,
									Queue: []*MetricSnapshot{
										{Info: MetricInfo{Name: consts.MetricCPUUsageContainer, Value: 0.25}},
										nil,
									},
									CurrentIndex: 1,
								},
							},
							"pod3": {
								consts.MetricCPUUsageContainer: {
									MaxLen: 2,
									Queue: []*MetricSnapshot{
										{Info: MetricInfo{Name: consts.MetricCPUUsageContainer, Value: 0.25}},
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
					consts.MetricCPUUsageContainer: 0.48,
				},
				enabled:           true,
				overloadNumaCount: 1,
			},
			args: args{
				request: &pluginapi.GetEvictPodsRequest{
					// 0.5 - 0.48 / 1.2 = 0.1, near to pod2/3
					ActivePods: []*v1.Pod{
						pod1,
						pod2,
						pod3,
					},
				},
			},
			wantResp: wantResp{
				want: &pluginapi.GetEvictPodsResponse{
					EvictPods: []*pluginapi.EvictPod{
						{
							Pod: pod2,
							Reason: fmt.Sprintf("numa cpu usage %f overload, kill top pod with %f",
								0.5, 0.125),
							ForceEvict:         true,
							EvictionPluginName: EvictionNameNumaCpuPressure,
							DeletionOptions:    nil,
						},
					},
				},
				wantPodList: []*v1.Pod{pod2, pod3},
			},
			wantErr: assert.NoError,
		},
		{
			name: "do not kill overload pod if it's the only one",
			fields: fields{
				podList: []*v1.Pod{
					pod1,
					pod2,
					pod3,
				},
				conf: conf,
				numaPressureConfig: &NumaPressureConfig{
					MetricRingSize:         2,
					ThresholdMetPercentage: 0.5,
					GracePeriod:            -1,
					ExpandFactor:           1.2,
				},
				metricsHistory: &NumaMetricHistory{
					Inner: map[int]map[string]map[string]*MetricRing{
						0: {
							FakePodUID: {
								consts.MetricCPUUsageContainer: {
									MaxLen: 2,
									Queue: []*MetricSnapshot{
										{Info: MetricInfo{Name: consts.MetricCPUUsageContainer, Value: 1.0}},
										nil,
									},
									CurrentIndex: 1,
								},
							},
							"pod1": {
								consts.MetricCPUUsageContainer: {
									MaxLen: 2,
									Queue: []*MetricSnapshot{
										{Info: MetricInfo{Name: consts.MetricCPUUsageContainer, Value: 1.0}},
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
				overloadNumaCount: 1,
			},
			args: args{
				request: &pluginapi.GetEvictPodsRequest{
					ActivePods: []*v1.Pod{
						pod1,
					},
				},
			},
			wantResp: wantResp{
				want: &pluginapi.GetEvictPodsResponse{},
			},
			wantErr: assert.NoError,
		},
		{
			name: "numa not overload, do not kill any pod",
			fields: fields{
				podList: []*v1.Pod{
					pod1,
					pod2,
					pod3,
				},
				conf: conf,
				numaPressureConfig: &NumaPressureConfig{
					MetricRingSize:         2,
					ThresholdMetPercentage: 0.5,
					GracePeriod:            -1,
					ExpandFactor:           1.2,
				},
				metricsHistory: &NumaMetricHistory{
					Inner: map[int]map[string]map[string]*MetricRing{
						0: {
							FakePodUID: {
								consts.MetricCPUUsageContainer: {
									MaxLen: 2,
									Queue: []*MetricSnapshot{
										{Info: MetricInfo{Name: consts.MetricCPUUsageContainer, Value: 0.3}},
										nil,
									},
									CurrentIndex: 1,
								},
							},
							"pod1": {
								consts.MetricCPUUsageContainer: {
									MaxLen: 2,
									Queue: []*MetricSnapshot{
										{Info: MetricInfo{Name: consts.MetricCPUUsageContainer, Value: 0.1}},
										nil,
									},
									CurrentIndex: 0,
								},
							},
							"pod2": {
								consts.MetricCPUUsageContainer: {
									MaxLen: 2,
									Queue: []*MetricSnapshot{
										{Info: MetricInfo{Name: consts.MetricCPUUsageContainer, Value: 0.1}},
									},
									CurrentIndex: 0,
								},
							},
							"pod3": {
								consts.MetricCPUUsageContainer: {
									MaxLen: 2,
									Queue: []*MetricSnapshot{
										{Info: MetricInfo{Name: consts.MetricCPUUsageContainer, Value: 0.1}},
									},
									CurrentIndex: 0,
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
				request: &pluginapi.GetEvictPodsRequest{
					ActivePods: []*v1.Pod{
						pod1,
						pod2,
						pod3,
					},
				},
			},
			wantResp: wantResp{
				want: &pluginapi.GetEvictPodsResponse{},
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
			}
			got, err := p.GetEvictPods(context.TODO(), tt.args.request)
			if !tt.wantErr(t, err, fmt.Sprintf("GetEvictPods(%v, %v)", context.TODO(), tt.args.request)) {
				return
			}
			if tt.wantResp.wantPodList != nil {
				find := false
				for _, wantPod := range tt.wantResp.wantPodList {
					if wantPod.Name == got.EvictPods[0].Pod.Name {
						find = true
					}
				}
				if !find {
					t.Errorf("expected pod in %v, but %v", tt.wantResp.wantPodList, tt.wantResp.wantPodList)
				}
			} else {
				assert.Equalf(t, tt.wantResp.want, got, "GetEvictPods(%v, %v)", context.TODO(), tt.args.request)
			}
		})
	}
}

func TestNumaCPUPressureEviction_ThresholdMet(t *testing.T) {
	t.Parallel()
	pod1 := makePod("pod1")
	pod2 := makePod("pod2")
	pod3 := makePod("pod3")
	type fields struct {
		conf               *config.Configuration
		numaPressureConfig *NumaPressureConfig
		syncPeriod         time.Duration
		thresholds         map[string]float64
		metricsHistory     *NumaMetricHistory
		overloadNumaCount  int
		enabled            bool
		podList            []*v1.Pod
	}
	tests := []struct {
		name    string
		fields  fields
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
				numaPressureConfig: &NumaPressureConfig{
					MetricRingSize:         2,
					ThresholdMetPercentage: 0.5,
					GracePeriod:            -1,
					ExpandFactor:           1.2,
				},
				metricsHistory: &NumaMetricHistory{
					Inner: map[int]map[string]map[string]*MetricRing{
						0: {
							FakePodUID: {
								consts.MetricCPUUsageContainer: {
									MaxLen: 2,
									Queue: []*MetricSnapshot{
										{Info: MetricInfo{Name: consts.MetricCPUUsageContainer, Value: 1.0}},
										nil,
									},
									CurrentIndex: 1,
								},
							},
							"pod1": {
								consts.MetricCPUUsageContainer: {
									MaxLen: 2,
									Queue: []*MetricSnapshot{
										{Info: MetricInfo{Name: consts.MetricCPUUsageContainer, Value: 0.5}},
										nil,
									},
									CurrentIndex: 1,
								},
							},
							"pod2": {
								consts.MetricCPUUsageContainer: {
									MaxLen: 2,
									Queue: []*MetricSnapshot{
										{Info: MetricInfo{Name: consts.MetricCPUUsageContainer, Value: 0.25}},
										nil,
									},
									CurrentIndex: 1,
								},
							},
							"pod3": {
								consts.MetricCPUUsageContainer: {
									MaxLen: 2,
									Queue: []*MetricSnapshot{
										{Info: MetricInfo{Name: consts.MetricCPUUsageContainer, Value: 0.25}},
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
				numaPressureConfig: &NumaPressureConfig{
					MetricRingSize:         2,
					ThresholdMetPercentage: 0.5,
					GracePeriod:            -1,
					ExpandFactor:           1.2,
				},
				metricsHistory: &NumaMetricHistory{
					Inner: map[int]map[string]map[string]*MetricRing{
						0: {
							FakePodUID: {
								consts.MetricCPUUsageContainer: {
									MaxLen: 2,
									Queue: []*MetricSnapshot{
										{Info: MetricInfo{Name: consts.MetricCPUUsageContainer, Value: 1.0}},
										nil,
									},
									CurrentIndex: 1,
								},
							},
							"pod1": {
								consts.MetricCPUUsageContainer: {
									MaxLen: 2,
									Queue: []*MetricSnapshot{
										{Info: MetricInfo{Name: consts.MetricCPUUsageContainer, Value: 0.5}},
										nil,
									},
									CurrentIndex: 1,
								},
							},
							"pod2": {
								consts.MetricCPUUsageContainer: {
									MaxLen: 2,
									Queue: []*MetricSnapshot{
										{Info: MetricInfo{Name: consts.MetricCPUUsageContainer, Value: 0.25}},
										nil,
									},
									CurrentIndex: 1,
								},
							},
							"pod3": {
								consts.MetricCPUUsageContainer: {
									MaxLen: 2,
									Queue: []*MetricSnapshot{
										{Info: MetricInfo{Name: consts.MetricCPUUsageContainer, Value: 0.25}},
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
			want: &pluginapi.ThresholdMetResponse{
				MetType: pluginapi.ThresholdMetType_NOT_MET,
			},
			wantErr: assert.NoError,
		},
		{
			name: "all numas are overload",
			fields: fields{
				enabled:           true,
				overloadNumaCount: 4,
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

			// store := metricsFetcher.(*metric.FakeMetricsFetcher)
			// tt.fields.setFakeMetric(store)

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
			}
			got, err := p.ThresholdMet(context.TODO(), nil)
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
		numaPressureConfig *NumaPressureConfig
		syncPeriod         time.Duration
		thresholds         map[string]float64
		metricsHistory     *NumaMetricHistory
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
				numaPressureConfig: &NumaPressureConfig{
					MetricRingSize:         2,
					ThresholdMetPercentage: 0.5,
					GracePeriod:            -1,
					ExpandFactor:           1.2,
				},
				metricsHistory: &NumaMetricHistory{
					Inner: map[int]map[string]map[string]*MetricRing{
						0: {
							FakePodUID: {
								consts.MetricCPUUsageContainer: {
									MaxLen: 2,
									Queue: []*MetricSnapshot{
										{Info: MetricInfo{Name: consts.MetricCPUUsageContainer, Value: 1.0}},
										{Info: MetricInfo{Name: consts.MetricCPUUsageContainer, Value: 1.0}},
									},
									CurrentIndex: 1,
								},
							},
							"pod1": {
								consts.MetricCPUUsageContainer: {
									MaxLen: 2,
									Queue: []*MetricSnapshot{
										{Info: MetricInfo{Name: consts.MetricCPUUsageContainer, Value: 0.5}},
										{Info: MetricInfo{Name: consts.MetricCPUUsageContainer, Value: 0.5}},
									},
									CurrentIndex: 1,
								},
							},
							"pod2": {
								consts.MetricCPUUsageContainer: {
									MaxLen: 2,
									Queue: []*MetricSnapshot{
										{Info: MetricInfo{Name: consts.MetricCPUUsageContainer, Value: 0.25}},
										{Info: MetricInfo{Name: consts.MetricCPUUsageContainer, Value: 0.25}},
									},
									CurrentIndex: 1,
								},
							},
							"pod3": {
								consts.MetricCPUUsageContainer: {
									MaxLen: 2,
									Queue: []*MetricSnapshot{
										{Info: MetricInfo{Name: consts.MetricCPUUsageContainer, Value: 0.25}},
										{Info: MetricInfo{Name: consts.MetricCPUUsageContainer, Value: 0.25}},
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

func Test_findCandidatePods(t *testing.T) {
	t.Parallel()
	type args struct {
		pods           []PodWithUsage
		gap            float64
		candidateCount int
	}
	tests := []struct {
		name        string
		args        args
		wantNames   []string
		wantRatios  []float64
		wantErr     bool
		errContains string
	}{
		{
			name: "Normal case: Find 3 closest pods",
			args: args{
				pods: []PodWithUsage{
					{pod: makePod("pod1"), usageRatio: 0.25},
					{pod: makePod("pod2"), usageRatio: 0.40},
					{pod: makePod("pod3"), usageRatio: 0.30},
					{pod: makePod("pod4"), usageRatio: 0.60},
					{pod: makePod("pod5"), usageRatio: 0.35},
				},
				gap:            0.35,
				candidateCount: 3,
			},
			wantNames:   []string{"pod5", "pod3", "pod2"},
			wantRatios:  []float64{0.35, 0.30, 0.40},
			wantErr:     false,
			errContains: "",
		},
		{
			name: "Boundary case: Exactly meet candidate count",
			args: args{
				pods: []PodWithUsage{
					{pod: makePod("pod1"), usageRatio: 1.0},
					{pod: makePod("pod2"), usageRatio: 2.0},
				},
				gap:            1.5,
				candidateCount: 2,
			},
			wantNames:   []string{"pod1", "pod2"},
			wantRatios:  []float64{1.0, 2.0},
			wantErr:     false,
			errContains: "",
		},
		{
			name: "Error case: Insufficient pods",
			args: args{
				pods: []PodWithUsage{
					{pod: makePod("pod1"), usageRatio: 1.0},
				},
				gap:            0.5,
				candidateCount: 2,
			},
			wantNames:   nil,
			wantRatios:  nil,
			wantErr:     true,
			errContains: "pod slice must contain at least 2 elements",
		},
		{
			name: "Special case: Multiple pods with same distance to gap",
			args: args{
				pods: []PodWithUsage{
					{pod: makePod("pod1"), usageRatio: 0.2},
					{pod: makePod("pod2"), usageRatio: 0.8},
					{pod: makePod("pod3"), usageRatio: 0.4},
					{pod: makePod("pod4"), usageRatio: 0.6},
				},
				gap:            0.5,
				candidateCount: 2,
			},
			wantNames:   []string{"pod3", "pod4"},
			wantRatios:  []float64{0.4, 0.6},
			wantErr:     false,
			errContains: "",
		},
		{
			name: "Special case: Gap is 0.0",
			args: args{
				pods: []PodWithUsage{
					{pod: makePod("pod1"), usageRatio: 0.0},
					{pod: makePod("pod2"), usageRatio: 1.0},
					{pod: makePod("pod3"), usageRatio: -1.0},
				},
				gap:            0.0,
				candidateCount: 2,
			},
			wantNames:   []string{"pod1", "pod2"},
			wantRatios:  []float64{0.0, 1.0},
			wantErr:     false,
			errContains: "",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := findCandidatePods(tt.args.pods, tt.args.gap, tt.args.candidateCount)

			if tt.wantErr {
				assert.Error(t, err, "Expected an error but got none")
				if tt.errContains != "" {
					assert.Contains(t, err.Error(), tt.errContains, "Error message does not contain expected substring")
				}
				return
			}

			assert.NoError(t, err, "Unexpected error: %v", err)
			assert.NotNil(t, got, "Result should not be nil")
			assert.Equal(t, tt.args.candidateCount, len(got), "Unexpected number of candidate pods")

			for i := range got {
				assert.Equal(t, tt.wantNames[i], got[i].pod.Name, "Pod name mismatch at index %d", i)
				assert.InDelta(t, tt.wantRatios[i], got[i].usageRatio, 1e-9, "Usage ratio mismatch at index %d", i)
			}
		})
	}
}

func TestSelectPodRandomly_Concurrency(t *testing.T) {
	t.Parallel()
	pods := []PodWithUsage{
		{pod: makePod("pod1"), usageRatio: 0.1},
		{pod: makePod("pod2"), usageRatio: 0.2},
		{pod: makePod("pod3"), usageRatio: 0.3},
	}

	counter := map[string]int{
		"pod1": 0,
		"pod2": 0,
		"pod3": 0,
	}
	var mu sync.Mutex

	var wg sync.WaitGroup
	numWorkers := 1000

	wg.Add(numWorkers)
	for i := 0; i < numWorkers; i++ {
		go func() {
			defer wg.Done()
			result := selectPodRandomly(pods)
			if result != nil {
				mu.Lock()
				counter[result.pod.Name]++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	for name, count := range counter {
		assert.Greater(t, count, 0, "Pod %s was never selected", name)
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
					Env: []v1.EnvVar{
						{
							Name:  native.PrimaryPort,
							Value: "0.5",
						},
					},
				},
			},
		},
	}
	return pod1
}

func TestWithKindFilter(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		skippedKinds []string
		pod          *v1.Pod
		expected     bool
	}{
		{
			name:         "Skip ReplicaSet owner",
			skippedKinds: []string{"ReplicaSet"},
			pod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name: "pod1",
					OwnerReferences: []metav1.OwnerReference{
						{Kind: "ReplicaSet"},
					},
				},
			},
			expected: false,
		},
		{
			name:         "Keep Pod without skipped owner",
			skippedKinds: []string{"Deployment"},
			pod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name: "pod2",
					OwnerReferences: []metav1.OwnerReference{
						{Kind: "ReplicaSet"},
					},
				},
			},
			expected: true,
		},
		{
			name:         "Skip multiple kinds",
			skippedKinds: []string{"Deployment", "StatefulSet"},
			pod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name: "pod3",
					OwnerReferences: []metav1.OwnerReference{
						{Kind: "StatefulSet"},
					},
				},
			},
			expected: false,
		},
		{
			name:         "No owner references",
			skippedKinds: []string{"Deployment"},
			pod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{Name: "pod4"},
			},
			expected: true,
		},
		{
			name:         "Nil pod",
			skippedKinds: []string{"Deployment"},
			pod:          nil,
			expected:     true,
		},
		{
			name:         "Empty skipped kinds",
			skippedKinds: []string{},
			pod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name: "pod5",
					OwnerReferences: []metav1.OwnerReference{
						{Kind: "Deployment"},
					},
				},
			},
			expected: true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			filter := native.NewFilterWithOptions(
				WithKindFilter(tt.skippedKinds),
			)
			result := filter.Apply([]*v1.Pod{tt.pod})

			if tt.expected {
				assert.Len(t, result, 1, "Pod should be kept")
				assert.Equal(t, tt.pod, result[0], "Pod should match")
			} else {
				assert.Len(t, result, 0, "Pod should be filtered out")
			}
		})
	}
}

func TestWithConsumerFilter(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		pod      *v1.Pod
		expected bool
	}{
		{
			name: "Keep pod with PrimaryPort env (not consumer)",
			pod: &v1.Pod{
				Spec: v1.PodSpec{
					Containers: []v1.Container{
						{
							Env: []v1.EnvVar{
								{Name: native.PrimaryPort, Value: "8080"},
							},
						},
					},
				},
			},
			expected: true,
		},
		{
			name: "Skip pod without PrimaryPort env (consumer)",
			pod: &v1.Pod{
				Spec: v1.PodSpec{
					Containers: []v1.Container{
						{
							Env: []v1.EnvVar{
								{Name: "OTHER_VAR", Value: "value"},
							},
						},
					},
				},
			},
			expected: false,
		},
		{
			name: "Keep multi-container pod with PrimaryPort in any container",
			pod: &v1.Pod{
				Spec: v1.PodSpec{
					Containers: []v1.Container{
						{Env: []v1.EnvVar{{Name: "OTHER_VAR", Value: "value"}}},
						{Env: []v1.EnvVar{{Name: native.PrimaryPort, Value: "8080"}}},
					},
				},
			},
			expected: true,
		},
		{
			name: "Skip pod with empty containers (consumer)",
			pod: &v1.Pod{
				Spec: v1.PodSpec{
					Containers: []v1.Container{},
				},
			},
			expected: false,
		},
		{
			name:     "Keep nil pod",
			pod:      nil,
			expected: true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			filter := native.NewFilterWithOptions(
				WithConsumerFilter(),
			)
			inputPods := []*v1.Pod{tt.pod}
			if tt.pod == nil {
				inputPods = []*v1.Pod{} // Apply方法忽略nil，因此传入空列表
			}

			result := filter.Apply(inputPods)

			if tt.expected {
				if tt.pod != nil {
					assert.Len(t, result, 1, "Pod should be kept")
					assert.Equal(t, tt.pod, result[0], "Pod should match")
				} else {
					assert.Len(t, result, 0, "Nil pod results in empty list")
				}
			} else {
				assert.Len(t, result, 0, "Pod should be filtered out")
			}
		})
	}
}

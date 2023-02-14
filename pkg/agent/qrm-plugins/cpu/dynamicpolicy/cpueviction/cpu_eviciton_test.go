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

package cpueviction

import (
	"context"
	"fmt"
	"io/ioutil"
	"testing"
	"time"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/uuid"
	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"
	maputil "k8s.io/kubernetes/pkg/util/maps"

	apiconsts "github.com/kubewharf/katalyst-api/pkg/consts"
	evictionpluginapi "github.com/kubewharf/katalyst-api/pkg/protocol/evictionplugin/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	statepkg "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var (
	defaultMetricRingSize                           = 1
	defaultCPUPressureEvictionPodGracePeriodSeconds = -1
	defaultLoadUpperBoundRatio                      = 1.8
	defaultLoadThresholdMetPercentage               = 0.8
	defaultCPUMaxSuppressionToleranceRate           = 5.0
	defaultCPUMinSuppressionToleranceDuration       = 1 * time.Second
)

func makeCPUPressureEvictionPlugin(emitter metrics.MetricEmitter, metaServer *metaserver.MetaServer,
	conf *config.Configuration, state statepkg.State) (*cpuPressureEvictionPlugin, error) {

	plugin := &cpuPressureEvictionPlugin{
		state:                                    state,
		emitter:                                  emitter,
		metaServer:                               metaServer,
		metricsHistory:                           make(map[string]Entries),
		qosConf:                                  conf.QoSConfiguration,
		metricRingSize:                           conf.MetricRingSize,
		loadUpperBoundRatio:                      conf.LoadUpperBoundRatio,
		loadThresholdMetPercentage:               conf.LoadThresholdMetPercentage,
		cpuPressureEvictionPodGracePeriodSeconds: conf.CPUPressureEvictionPodGracePeriodSeconds,
		maxCPUSuppressionToleranceRate:           conf.MaxCPUSuppressionToleranceRate,
	}

	plugin.poolMetricCollectHandlers = map[string]PoolMetricCollectHandler{
		consts.MetricLoad1MinContainer: plugin.collectPoolLoad,
	}

	return plugin, nil
}

func makeMetaServer(metricsFetcher metric.MetricsFetcher, cpuTopology *machine.CPUTopology) *metaserver.MetaServer {
	metaServer := &metaserver.MetaServer{
		MetaAgent: &agent.MetaAgent{},
	}

	metaServer.MetricsFetcher = metricsFetcher
	metaServer.KatalystMachineInfo = &machine.KatalystMachineInfo{
		CPUTopology: cpuTopology,
	}

	return metaServer
}

func makeConf(metricRingSize int, cpuPressureEvictionPodGracePeriodSeconds int64, loadUpperBoundRatio,
	loadThresholdMetPercentage, cpuMaxSuppressionToleranceRate float64) *config.Configuration {
	conf := config.NewConfiguration()
	conf.MetricRingSize = metricRingSize
	conf.LoadUpperBoundRatio = loadUpperBoundRatio
	conf.LoadThresholdMetPercentage = loadThresholdMetPercentage
	conf.CPUPressureEvictionPodGracePeriodSeconds = cpuPressureEvictionPodGracePeriodSeconds
	conf.MaxCPUSuppressionToleranceRate = cpuMaxSuppressionToleranceRate
	conf.MinCPUSuppressionToleranceDuration = defaultCPUMinSuppressionToleranceDuration
	return conf
}

func makeState(topo *machine.CPUTopology) (statepkg.State, error) {
	tmpDir, err := ioutil.TempDir("", "checkpoint")
	if err != nil {
		return nil, fmt.Errorf("make tmp dir for checkpoint failed with error: %v", err)
	}
	return statepkg.NewCheckpointState(tmpDir, "test", "test", topo, false)
}

func TestNewCPUPressureEvictionPlugin(t *testing.T) {
	as := require.New(t)

	cpuTopology, err := machine.GenerateDummyCPUTopology(16, 2, 4)
	as.Nil(err)
	conf := makeConf(defaultMetricRingSize, int64(defaultCPUPressureEvictionPodGracePeriodSeconds),
		defaultLoadUpperBoundRatio, defaultLoadThresholdMetPercentage, defaultCPUMaxSuppressionToleranceRate)
	metaServer := makeMetaServer(metric.NewFakeMetricsFetcher(metrics.DummyMetrics{}), cpuTopology)
	stateImpl, err := makeState(cpuTopology)
	as.Nil(err)

	plugin, err := makeCPUPressureEvictionPlugin(metrics.DummyMetrics{}, metaServer, conf, stateImpl)
	as.Nil(err)
	as.NotNil(plugin)
	as.Equal(cpuPressureEvictionPluginName, plugin.Name())
	as.Equal(defaultMetricRingSize, plugin.metricRingSize)
	as.EqualValues(defaultCPUPressureEvictionPodGracePeriodSeconds, plugin.cpuPressureEvictionPodGracePeriodSeconds)
	as.Equal(defaultLoadUpperBoundRatio, plugin.loadUpperBoundRatio)
	as.Equal(defaultLoadThresholdMetPercentage, plugin.loadThresholdMetPercentage)

	return
}

func TestThresholdMet(t *testing.T) {
	as := require.New(t)

	cpuTopology, err := machine.GenerateDummyCPUTopology(16, 2, 4)
	as.Nil(err)
	conf := makeConf(defaultMetricRingSize, int64(defaultCPUPressureEvictionPodGracePeriodSeconds),
		defaultLoadUpperBoundRatio, defaultLoadThresholdMetPercentage, defaultCPUMaxSuppressionToleranceRate)
	metaServer := makeMetaServer(metric.NewFakeMetricsFetcher(metrics.DummyMetrics{}), cpuTopology)
	stateImpl, err := makeState(cpuTopology)
	as.Nil(err)

	plugin, err := makeCPUPressureEvictionPlugin(metrics.DummyMetrics{}, metaServer, conf, stateImpl)
	as.Nil(err)
	as.NotNil(plugin)

	pod1UID := string(uuid.NewUUID())
	testName := "test"

	tests := []struct {
		name              string
		podEntries        statepkg.PodEntries
		loads             map[string]map[string]float64
		wantMetType       evictionpluginapi.ThresholdMetType
		wantEvictionScope string
		wantCondition     *evictionpluginapi.Condition
	}{
		{
			name:              "not met load threshold",
			wantMetType:       evictionpluginapi.ThresholdMetType_NOT_MET,
			wantEvictionScope: "",
			wantCondition:     nil,
			podEntries: statepkg.PodEntries{
				pod1UID: statepkg.ContainerEntries{
					testName: &statepkg.AllocationInfo{
						PodUid:                   pod1UID,
						PodNamespace:             testName,
						PodName:                  testName,
						ContainerName:            testName,
						ContainerType:            pluginapi.ContainerType_MAIN.String(),
						ContainerIndex:           0,
						RampUp:                   false,
						OwnerPoolName:            statepkg.PoolNameShare,
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
						Labels: map[string]string{
							apiconsts.PodAnnotationQoSLevelKey: apiconsts.PodAnnotationQoSLevelSharedCores,
						},
						Annotations: map[string]string{
							apiconsts.PodAnnotationQoSLevelKey: apiconsts.PodAnnotationQoSLevelSharedCores,
						},
						QoSLevel:        apiconsts.PodAnnotationQoSLevelSharedCores,
						RequestQuantity: 2,
					},
				},
				state.PoolNameShare: state.ContainerEntries{
					"": &state.AllocationInfo{
						PodUid:                   state.PoolNameShare,
						OwnerPoolName:            state.PoolNameShare,
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
					},
				},
			},
			loads: map[string]map[string]float64{
				pod1UID: {
					testName: 1,
				},
			},
		},
		{
			name:              "met soft threshold",
			wantMetType:       evictionpluginapi.ThresholdMetType_SOFT_MET,
			wantEvictionScope: consts.MetricLoad1MinContainer,
			wantCondition: &evictionpluginapi.Condition{
				ConditionType: evictionpluginapi.ConditionType_NODE_CONDITION,
				Effects:       []string{string(v1.TaintEffectNoSchedule)},
				ConditionName: evictionConditionCPUPressure,
				MetCondition:  true,
			},
			podEntries: statepkg.PodEntries{
				pod1UID: statepkg.ContainerEntries{
					testName: &statepkg.AllocationInfo{
						PodUid:                   pod1UID,
						PodNamespace:             testName,
						PodName:                  testName,
						ContainerName:            testName,
						ContainerType:            pluginapi.ContainerType_MAIN.String(),
						ContainerIndex:           0,
						RampUp:                   false,
						OwnerPoolName:            statepkg.PoolNameShare,
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
						Labels: map[string]string{
							apiconsts.PodAnnotationQoSLevelKey: apiconsts.PodAnnotationQoSLevelSharedCores,
						},
						Annotations: map[string]string{
							apiconsts.PodAnnotationQoSLevelKey: apiconsts.PodAnnotationQoSLevelSharedCores,
						},
						QoSLevel:        apiconsts.PodAnnotationQoSLevelSharedCores,
						RequestQuantity: 2,
					},
				},
				state.PoolNameShare: state.ContainerEntries{
					"": &state.AllocationInfo{
						PodUid:                   state.PoolNameShare,
						OwnerPoolName:            state.PoolNameShare,
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
					},
				},
			},
			loads: map[string]map[string]float64{
				pod1UID: {
					testName: 11,
				},
			},
		},
		{
			name:              "met hard threshold",
			wantMetType:       evictionpluginapi.ThresholdMetType_HARD_MET,
			wantEvictionScope: consts.MetricLoad1MinContainer,
			wantCondition: &evictionpluginapi.Condition{
				ConditionType: evictionpluginapi.ConditionType_NODE_CONDITION,
				Effects:       []string{string(v1.TaintEffectNoSchedule)},
				ConditionName: evictionConditionCPUPressure,
				MetCondition:  true,
			},
			podEntries: statepkg.PodEntries{
				pod1UID: statepkg.ContainerEntries{
					testName: &statepkg.AllocationInfo{
						PodUid:                   pod1UID,
						PodNamespace:             testName,
						PodName:                  testName,
						ContainerName:            testName,
						ContainerType:            pluginapi.ContainerType_MAIN.String(),
						ContainerIndex:           0,
						RampUp:                   false,
						OwnerPoolName:            statepkg.PoolNameShare,
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
						Labels: map[string]string{
							apiconsts.PodAnnotationQoSLevelKey: apiconsts.PodAnnotationQoSLevelSharedCores,
						},
						Annotations: map[string]string{
							apiconsts.PodAnnotationQoSLevelKey: apiconsts.PodAnnotationQoSLevelSharedCores,
						},
						QoSLevel:        apiconsts.PodAnnotationQoSLevelSharedCores,
						RequestQuantity: 2,
					},
				},
				state.PoolNameShare: state.ContainerEntries{
					"": &state.AllocationInfo{
						PodUid:                   state.PoolNameShare,
						OwnerPoolName:            state.PoolNameShare,
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
					},
				},
			},
			loads: map[string]map[string]float64{
				pod1UID: {
					testName: 50,
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stateImpl, err := makeState(cpuTopology)
			as.Nil(err)

			fakeMetricsFetcher := metric.NewFakeMetricsFetcher(metrics.DummyMetrics{}).(*metric.FakeMetricsFetcher)
			assert.NotNil(t, fakeMetricsFetcher)

			for entryName, entries := range tt.podEntries {
				for subEntryName, entry := range entries {
					stateImpl.SetAllocationInfo(entryName, subEntryName, entry)

					if entries.IsPoolEntry() {
						continue
					}

					curLoad, found := tt.loads[entryName][subEntryName]
					as.True(found)
					fakeMetricsFetcher.SetContainerMetric(entryName, subEntryName, consts.MetricLoad1MinContainer, curLoad)
				}
			}

			plugin.state = stateImpl

			plugin.metaServer.MetricsFetcher = fakeMetricsFetcher

			plugin.collectMetrics(context.Background())

			metResp, err := plugin.ThresholdMet(context.Background(), &evictionpluginapi.Empty{})
			as.Nil(err)
			as.NotNil(t, metResp)

			as.Equal(tt.wantMetType, metResp.MetType)
			as.Equal(tt.wantEvictionScope, metResp.EvictionScope)
			if tt.wantCondition != nil && metResp.Condition != nil {
				as.Equal(*(tt.wantCondition), *(metResp.Condition))
			} else {
				as.Equal(tt.wantCondition, metResp.Condition)
			}
		})
	}
}

func TestGetTopEvictionPods(t *testing.T) {
	as := require.New(t)

	cpuTopology, err := machine.GenerateDummyCPUTopology(16, 2, 4)
	as.Nil(err)
	conf := makeConf(defaultMetricRingSize, int64(defaultCPUPressureEvictionPodGracePeriodSeconds),
		defaultLoadUpperBoundRatio, defaultLoadThresholdMetPercentage, defaultCPUMaxSuppressionToleranceRate)
	metaServer := makeMetaServer(metric.NewFakeMetricsFetcher(metrics.DummyMetrics{}), cpuTopology)
	stateImpl, err := makeState(cpuTopology)
	as.Nil(err)

	plugin, err := makeCPUPressureEvictionPlugin(metrics.DummyMetrics{}, metaServer, conf, stateImpl)
	as.Nil(err)
	as.NotNil(plugin)

	pod1UID := string(uuid.NewUUID())
	pod2UID := string(uuid.NewUUID())
	pod3UID := string(uuid.NewUUID())
	testName := "test"

	tests := []struct {
		name               string
		podEntries         statepkg.PodEntries
		loads              map[string]map[string]float64
		wantResp           *evictionpluginapi.GetTopEvictionPodsResponse
		wantEvictPodUIDSet sets.String
	}{
		{
			name:     "not met load threshold",
			wantResp: &evictionpluginapi.GetTopEvictionPodsResponse{},
			podEntries: statepkg.PodEntries{
				pod1UID: statepkg.ContainerEntries{
					testName: &statepkg.AllocationInfo{
						PodUid:                   pod1UID,
						PodNamespace:             testName,
						PodName:                  testName,
						ContainerName:            testName,
						ContainerType:            pluginapi.ContainerType_MAIN.String(),
						ContainerIndex:           0,
						RampUp:                   false,
						OwnerPoolName:            statepkg.PoolNameShare,
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
						Labels: map[string]string{
							apiconsts.PodAnnotationQoSLevelKey: apiconsts.PodAnnotationQoSLevelSharedCores,
						},
						Annotations: map[string]string{
							apiconsts.PodAnnotationQoSLevelKey: apiconsts.PodAnnotationQoSLevelSharedCores,
						},
						QoSLevel:        apiconsts.PodAnnotationQoSLevelSharedCores,
						RequestQuantity: 2,
					},
				},
				state.PoolNameShare: state.ContainerEntries{
					"": &state.AllocationInfo{
						PodUid:                   state.PoolNameShare,
						OwnerPoolName:            state.PoolNameShare,
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
					},
				},
			},
			loads: map[string]map[string]float64{
				pod1UID: {
					testName: 1,
				},
			},
		},
		{
			name:     "met soft threshold",
			wantResp: &evictionpluginapi.GetTopEvictionPodsResponse{},
			podEntries: statepkg.PodEntries{
				pod1UID: statepkg.ContainerEntries{
					testName: &statepkg.AllocationInfo{
						PodUid:                   pod1UID,
						PodNamespace:             testName,
						PodName:                  testName,
						ContainerName:            testName,
						ContainerType:            pluginapi.ContainerType_MAIN.String(),
						ContainerIndex:           0,
						RampUp:                   false,
						OwnerPoolName:            statepkg.PoolNameShare,
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
						Labels: map[string]string{
							apiconsts.PodAnnotationQoSLevelKey: apiconsts.PodAnnotationQoSLevelSharedCores,
						},
						Annotations: map[string]string{
							apiconsts.PodAnnotationQoSLevelKey: apiconsts.PodAnnotationQoSLevelSharedCores,
						},
						QoSLevel:        apiconsts.PodAnnotationQoSLevelSharedCores,
						RequestQuantity: 2,
					},
				},
				state.PoolNameShare: state.ContainerEntries{
					"": &state.AllocationInfo{
						PodUid:                   state.PoolNameShare,
						OwnerPoolName:            state.PoolNameShare,
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
					},
				},
			},
			loads: map[string]map[string]float64{
				pod1UID: {
					testName: 11,
				},
			},
		},
		{
			name:     "met hard threshold",
			wantResp: &evictionpluginapi.GetTopEvictionPodsResponse{},
			wantEvictPodUIDSet: sets.NewString(
				pod2UID,
			),
			podEntries: statepkg.PodEntries{
				pod1UID: statepkg.ContainerEntries{
					testName: &statepkg.AllocationInfo{
						PodUid:                   pod1UID,
						PodNamespace:             testName,
						PodName:                  testName,
						ContainerName:            testName,
						ContainerType:            pluginapi.ContainerType_MAIN.String(),
						ContainerIndex:           0,
						RampUp:                   false,
						OwnerPoolName:            statepkg.PoolNameShare,
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
						Labels: map[string]string{
							apiconsts.PodAnnotationQoSLevelKey: apiconsts.PodAnnotationQoSLevelSharedCores,
						},
						Annotations: map[string]string{
							apiconsts.PodAnnotationQoSLevelKey: apiconsts.PodAnnotationQoSLevelSharedCores,
						},
						QoSLevel:        apiconsts.PodAnnotationQoSLevelSharedCores,
						RequestQuantity: 2,
					},
				},
				pod2UID: statepkg.ContainerEntries{
					testName: &statepkg.AllocationInfo{
						PodUid:                   pod2UID,
						PodNamespace:             testName,
						PodName:                  testName,
						ContainerName:            testName,
						ContainerType:            pluginapi.ContainerType_MAIN.String(),
						ContainerIndex:           0,
						RampUp:                   false,
						OwnerPoolName:            statepkg.PoolNameShare,
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
						Labels: map[string]string{
							apiconsts.PodAnnotationQoSLevelKey: apiconsts.PodAnnotationQoSLevelSharedCores,
						},
						Annotations: map[string]string{
							apiconsts.PodAnnotationQoSLevelKey: apiconsts.PodAnnotationQoSLevelSharedCores,
						},
						QoSLevel:        apiconsts.PodAnnotationQoSLevelSharedCores,
						RequestQuantity: 2,
					},
				},
				pod3UID: state.ContainerEntries{
					testName: &state.AllocationInfo{
						PodUid:                   pod3UID,
						PodNamespace:             testName,
						PodName:                  testName,
						ContainerName:            testName,
						ContainerType:            pluginapi.ContainerType_MAIN.String(),
						ContainerIndex:           0,
						RampUp:                   false,
						OwnerPoolName:            state.PoolNameReclaim,
						AllocationResult:         machine.NewCPUSet(7, 8, 10, 15),
						OriginalAllocationResult: machine.NewCPUSet(7, 8, 10, 15),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(8),
							1: machine.NewCPUSet(10),
							3: machine.NewCPUSet(7, 15),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(8),
							1: machine.NewCPUSet(10),
							3: machine.NewCPUSet(7, 15),
						},
						Labels: map[string]string{
							apiconsts.PodAnnotationQoSLevelKey: apiconsts.PodAnnotationQoSLevelReclaimedCores,
						},
						Annotations: map[string]string{
							apiconsts.PodAnnotationQoSLevelKey: apiconsts.PodAnnotationQoSLevelReclaimedCores,
						},
						QoSLevel:        apiconsts.PodAnnotationQoSLevelReclaimedCores,
						RequestQuantity: 2,
					},
				},
				state.PoolNameShare: state.ContainerEntries{
					"": &state.AllocationInfo{
						PodUid:                   state.PoolNameShare,
						OwnerPoolName:            state.PoolNameShare,
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
					},
				},
				state.PoolNameReclaim: state.ContainerEntries{
					"": &state.AllocationInfo{
						PodUid:                   state.PoolNameReclaim,
						OwnerPoolName:            state.PoolNameReclaim,
						AllocationResult:         machine.MustParse("7-8,10,15"),
						OriginalAllocationResult: machine.MustParse("7-8,10,15"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(8),
							1: machine.NewCPUSet(10),
							3: machine.NewCPUSet(7, 15),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(8),
							1: machine.NewCPUSet(10),
							3: machine.NewCPUSet(7, 15),
						},
					},
				},
			},
			loads: map[string]map[string]float64{
				pod1UID: {
					testName: 20,
				},
				pod2UID: {
					testName: 50,
				},
				pod3UID: {
					testName: 200,
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stateImpl, err := makeState(cpuTopology)
			as.Nil(err)

			fakeMetricsFetcher := metric.NewFakeMetricsFetcher(metrics.DummyMetrics{}).(*metric.FakeMetricsFetcher)
			assert.NotNil(t, fakeMetricsFetcher)

			pods := make([]*v1.Pod, 0, len(tt.podEntries))
			candidatePods := make([]*v1.Pod, 0, len(tt.podEntries))

			for entryName, entries := range tt.podEntries {
				for subEntryName, entry := range entries {
					stateImpl.SetAllocationInfo(entryName, subEntryName, entry)

					if entries.IsPoolEntry() {
						continue
					}

					pod := &v1.Pod{
						ObjectMeta: metav1.ObjectMeta{
							UID:         types.UID(entry.PodUid),
							Name:        entry.PodName,
							Namespace:   entry.PodNamespace,
							Annotations: maputil.CopySS(entry.Annotations),
							Labels:      maputil.CopySS(entry.Labels),
						},
						Spec: v1.PodSpec{
							Containers: []v1.Container{
								{
									Name: entry.ContainerName,
								},
							},
						},
					}

					pods = append(pods, pod)

					if tt.wantEvictPodUIDSet.Has(entry.PodUid) {
						candidatePods = append(candidatePods, pod)
					}

					curLoad, found := tt.loads[entryName][subEntryName]
					as.True(found)
					fakeMetricsFetcher.SetContainerMetric(entryName, subEntryName, consts.MetricLoad1MinContainer, curLoad)
				}
			}

			plugin.state = stateImpl

			plugin.metaServer.MetricsFetcher = fakeMetricsFetcher

			plugin.collectMetrics(context.Background())

			metResp, err := plugin.ThresholdMet(context.Background(), &evictionpluginapi.Empty{})
			as.Nil(err)
			as.NotNil(t, metResp)

			resp, err := plugin.GetTopEvictionPods(context.TODO(), &evictionpluginapi.GetTopEvictionPodsRequest{
				ActivePods:    pods,
				TopN:          1,
				EvictionScope: consts.MetricLoad1MinContainer,
			})
			assert.NoError(t, err)
			assert.NotNil(t, resp)

			if metResp.MetType == evictionpluginapi.ThresholdMetType_HARD_MET {
				as.Equal(candidatePods, resp.TargetPods)
				tt.wantResp = &evictionpluginapi.GetTopEvictionPodsResponse{
					TargetPods: candidatePods,
				}
			}
			as.Equal(tt.wantResp, resp)
		})
	}
}

func TestGetEvictPods(t *testing.T) {
	as := require.New(t)

	cpuTopology, err := machine.GenerateDummyCPUTopology(16, 2, 4)
	as.Nil(err)
	conf := makeConf(defaultMetricRingSize, int64(defaultCPUPressureEvictionPodGracePeriodSeconds),
		defaultLoadUpperBoundRatio, defaultLoadThresholdMetPercentage, defaultCPUMaxSuppressionToleranceRate)
	metaServer := makeMetaServer(metric.NewFakeMetricsFetcher(metrics.DummyMetrics{}), cpuTopology)
	stateImpl, err := makeState(cpuTopology)
	as.Nil(err)

	plugin, err := makeCPUPressureEvictionPlugin(metrics.DummyMetrics{}, metaServer, conf, stateImpl)
	as.Nil(err)
	as.NotNil(plugin)

	pod1UID := string(uuid.NewUUID())
	pod1Name := "pod-1"
	pod2UID := string(uuid.NewUUID())
	pod2Name := "pod-2"

	tests := []struct {
		name               string
		podEntries         statepkg.PodEntries
		wantEvictPodUIDSet sets.String
	}{
		{
			name: "no over tolerance rate pod",
			podEntries: statepkg.PodEntries{
				pod1UID: statepkg.ContainerEntries{
					pod1Name: &statepkg.AllocationInfo{
						PodUid:                   pod1UID,
						PodNamespace:             pod1Name,
						PodName:                  pod1Name,
						ContainerName:            pod1Name,
						ContainerType:            pluginapi.ContainerType_MAIN.String(),
						ContainerIndex:           0,
						RampUp:                   false,
						OwnerPoolName:            statepkg.PoolNameReclaim,
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
						Labels: map[string]string{
							apiconsts.PodAnnotationQoSLevelKey: apiconsts.PodAnnotationQoSLevelReclaimedCores,
						},
						Annotations: map[string]string{
							apiconsts.PodAnnotationQoSLevelKey:       apiconsts.PodAnnotationQoSLevelReclaimedCores,
							apiconsts.PodAnnotationCPUEnhancementKey: `{"suppression_tolerance_rate": "1.2"}`,
						},
						QoSLevel:        apiconsts.PodAnnotationQoSLevelReclaimedCores,
						RequestQuantity: 2,
					},
				},
				state.PoolNameReclaim: state.ContainerEntries{
					"": &state.AllocationInfo{
						PodUid:                   state.PoolNameReclaim,
						OwnerPoolName:            state.PoolNameReclaim,
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
					},
				},
			},
			wantEvictPodUIDSet: sets.NewString(),
		},
		{
			name: "over tolerance rate",
			podEntries: statepkg.PodEntries{
				pod1UID: statepkg.ContainerEntries{
					pod1Name: &statepkg.AllocationInfo{
						PodUid:                   pod1UID,
						PodNamespace:             pod1Name,
						PodName:                  pod1Name,
						ContainerName:            pod1Name,
						ContainerType:            pluginapi.ContainerType_MAIN.String(),
						ContainerIndex:           0,
						RampUp:                   false,
						OwnerPoolName:            statepkg.PoolNameReclaim,
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
						Labels: map[string]string{
							apiconsts.PodAnnotationQoSLevelKey: apiconsts.PodAnnotationQoSLevelReclaimedCores,
						},
						Annotations: map[string]string{
							apiconsts.PodAnnotationQoSLevelKey:       apiconsts.PodAnnotationQoSLevelReclaimedCores,
							apiconsts.PodAnnotationCPUEnhancementKey: `{"suppression_tolerance_rate": "1.2"}`,
						},
						QoSLevel:        apiconsts.PodAnnotationQoSLevelReclaimedCores,
						RequestQuantity: 15,
					},
				},
				pod2UID: statepkg.ContainerEntries{
					pod1Name: &statepkg.AllocationInfo{
						PodUid:                   pod2UID,
						PodNamespace:             pod2Name,
						PodName:                  pod2Name,
						ContainerName:            pod2Name,
						ContainerType:            pluginapi.ContainerType_MAIN.String(),
						ContainerIndex:           0,
						RampUp:                   false,
						OwnerPoolName:            statepkg.PoolNameReclaim,
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
						Labels: map[string]string{
							apiconsts.PodAnnotationQoSLevelKey: apiconsts.PodAnnotationQoSLevelReclaimedCores,
						},
						Annotations: map[string]string{
							apiconsts.PodAnnotationQoSLevelKey:       apiconsts.PodAnnotationQoSLevelReclaimedCores,
							apiconsts.PodAnnotationCPUEnhancementKey: `{"suppression_tolerance_rate": "1.2"}`,
						},
						QoSLevel:        apiconsts.PodAnnotationQoSLevelReclaimedCores,
						RequestQuantity: 4,
					},
				},
				state.PoolNameReclaim: state.ContainerEntries{
					"": &state.AllocationInfo{
						PodUid:                   state.PoolNameReclaim,
						OwnerPoolName:            state.PoolNameReclaim,
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
					},
				},
			},
			wantEvictPodUIDSet: sets.NewString(pod1UID),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stateImpl, err := makeState(cpuTopology)
			as.Nil(err)

			pods := make([]*v1.Pod, 0, len(tt.podEntries))

			for entryName, entries := range tt.podEntries {
				for subEntryName, entry := range entries {
					stateImpl.SetAllocationInfo(entryName, subEntryName, entry)

					if entries.IsPoolEntry() {
						continue
					}

					pod := &v1.Pod{
						ObjectMeta: metav1.ObjectMeta{
							UID:         types.UID(entry.PodUid),
							Name:        entry.PodName,
							Namespace:   entry.PodNamespace,
							Annotations: maputil.CopySS(entry.Annotations),
							Labels:      maputil.CopySS(entry.Labels),
						},
						Spec: v1.PodSpec{
							Containers: []v1.Container{
								{
									Name: entry.ContainerName,
									Resources: v1.ResourceRequirements{
										Requests: v1.ResourceList{
											apiconsts.ReclaimedResourceMilliCPU: *resource.NewQuantity(int64(entry.RequestQuantity*1000), resource.DecimalSI),
										},
										Limits: v1.ResourceList{
											apiconsts.ReclaimedResourceMilliCPU: *resource.NewQuantity(int64(entry.RequestQuantity*1000), resource.DecimalSI),
										},
									},
								},
							},
						},
					}

					pods = append(pods, pod)
				}
			}

			plugin.state = stateImpl

			resp, err := plugin.GetEvictPods(context.TODO(), &evictionpluginapi.GetEvictPodsRequest{
				ActivePods: pods,
			})
			assert.NoError(t, err)
			assert.NotNil(t, resp)

			time.Sleep(1 * time.Second)

			resp, err = plugin.GetEvictPods(context.TODO(), &evictionpluginapi.GetEvictPodsRequest{
				ActivePods: pods,
			})
			assert.NoError(t, err)
			assert.NotNil(t, resp)

			evictPodUIDSet := sets.String{}
			for _, pod := range resp.EvictPods {
				evictPodUIDSet.Insert(string(pod.Pod.GetUID()))
			}
			assert.Equal(t, tt.wantEvictPodUIDSet, evictPodUIDSet)
		})
	}
}

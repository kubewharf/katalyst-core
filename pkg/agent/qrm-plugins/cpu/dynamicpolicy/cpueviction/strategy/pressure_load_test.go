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
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/uuid"
	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"
	maputil "k8s.io/kubernetes/pkg/util/maps"

	apiconsts "github.com/kubewharf/katalyst-api/pkg/consts"
	evictionpluginapi "github.com/kubewharf/katalyst-api/pkg/protocol/evictionplugin/v1alpha1"
	qrmstate "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

const (
	defaultMetricRingSize                           = 1
	defaultCPUPressureEvictionPodGracePeriodSeconds = -1
	defaultLoadUpperBoundRatio                      = 1.8
	defaultLoadThresholdMetPercentage               = 0.8
)

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
	loadThresholdMetPercentage float64) *config.Configuration {
	conf := config.NewConfiguration()
	conf.MetricRingSize = metricRingSize
	conf.LoadUpperBoundRatio = loadUpperBoundRatio
	conf.LoadThresholdMetPercentage = loadThresholdMetPercentage
	conf.CPUPressureEvictionPodGracePeriodSeconds = cpuPressureEvictionPodGracePeriodSeconds
	return conf
}

func makeState(topo *machine.CPUTopology) (qrmstate.State, error) {
	tmpDir, err := os.MkdirTemp("", "checkpoint")
	if err != nil {
		return nil, fmt.Errorf("make tmp dir for checkpoint failed with error: %v", err)
	}
	return qrmstate.NewCheckpointState(tmpDir, "test", "test", topo, false)
}

func TestNewCPUPressureLoadEviction(t *testing.T) {
	as := require.New(t)

	cpuTopology, err := machine.GenerateDummyCPUTopology(16, 2, 4)
	as.Nil(err)
	conf := makeConf(defaultMetricRingSize, int64(defaultCPUPressureEvictionPodGracePeriodSeconds),
		defaultLoadUpperBoundRatio, defaultLoadThresholdMetPercentage)
	metaServer := makeMetaServer(metric.NewFakeMetricsFetcher(metrics.DummyMetrics{}), cpuTopology)
	stateImpl, err := makeState(cpuTopology)
	as.Nil(err)

	plugin := NewCPUPressureEviction(metrics.DummyMetrics{}, metaServer, conf, stateImpl)
	as.Nil(err)
	as.NotNil(plugin)
	as.Equal(defaultMetricRingSize, plugin.(*CPUPressureLoadEviction).metricRingSize)
	as.EqualValues(defaultCPUPressureEvictionPodGracePeriodSeconds, plugin.(*CPUPressureLoadEviction).podGracePeriodSeconds)
	as.Equal(defaultLoadUpperBoundRatio, plugin.(*CPUPressureLoadEviction).loadUpperBoundRatio)
	as.Equal(defaultLoadThresholdMetPercentage, plugin.(*CPUPressureLoadEviction).loadThresholdMetPercentage)
}

func TestThresholdMet(t *testing.T) {
	as := require.New(t)

	cpuTopology, err := machine.GenerateDummyCPUTopology(16, 2, 4)
	as.Nil(err)
	conf := makeConf(defaultMetricRingSize, int64(defaultCPUPressureEvictionPodGracePeriodSeconds),
		defaultLoadUpperBoundRatio, defaultLoadThresholdMetPercentage)
	metaServer := makeMetaServer(metric.NewFakeMetricsFetcher(metrics.DummyMetrics{}), cpuTopology)
	stateImpl, err := makeState(cpuTopology)
	as.Nil(err)

	plugin := NewCPUPressureEviction(metrics.DummyMetrics{}, metaServer, conf, stateImpl)
	as.NotNil(plugin)

	pod1UID := string(uuid.NewUUID())
	testName := "test"

	tests := []struct {
		name              string
		podEntries        qrmstate.PodEntries
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
			podEntries: qrmstate.PodEntries{
				pod1UID: qrmstate.ContainerEntries{
					testName: &qrmstate.AllocationInfo{
						PodUid:                   pod1UID,
						PodNamespace:             testName,
						PodName:                  testName,
						ContainerName:            testName,
						ContainerType:            pluginapi.ContainerType_MAIN.String(),
						ContainerIndex:           0,
						RampUp:                   false,
						OwnerPoolName:            qrmstate.PoolNameShare,
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
				qrmstate.PoolNameShare: qrmstate.ContainerEntries{
					"": &qrmstate.AllocationInfo{
						PodUid:                   qrmstate.PoolNameShare,
						OwnerPoolName:            qrmstate.PoolNameShare,
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
			podEntries: qrmstate.PodEntries{
				pod1UID: qrmstate.ContainerEntries{
					testName: &qrmstate.AllocationInfo{
						PodUid:                   pod1UID,
						PodNamespace:             testName,
						PodName:                  testName,
						ContainerName:            testName,
						ContainerType:            pluginapi.ContainerType_MAIN.String(),
						ContainerIndex:           0,
						RampUp:                   false,
						OwnerPoolName:            qrmstate.PoolNameShare,
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
				qrmstate.PoolNameShare: qrmstate.ContainerEntries{
					"": &qrmstate.AllocationInfo{
						PodUid:                   qrmstate.PoolNameShare,
						OwnerPoolName:            qrmstate.PoolNameShare,
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
			podEntries: qrmstate.PodEntries{
				pod1UID: qrmstate.ContainerEntries{
					testName: &qrmstate.AllocationInfo{
						PodUid:                   pod1UID,
						PodNamespace:             testName,
						PodName:                  testName,
						ContainerName:            testName,
						ContainerType:            pluginapi.ContainerType_MAIN.String(),
						ContainerIndex:           0,
						RampUp:                   false,
						OwnerPoolName:            qrmstate.PoolNameShare,
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
				qrmstate.PoolNameShare: qrmstate.ContainerEntries{
					"": &qrmstate.AllocationInfo{
						PodUid:                   qrmstate.PoolNameShare,
						OwnerPoolName:            qrmstate.PoolNameShare,
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

			plugin.(*CPUPressureLoadEviction).state = stateImpl

			plugin.(*CPUPressureLoadEviction).metaServer.MetricsFetcher = fakeMetricsFetcher

			plugin.(*CPUPressureLoadEviction).collectMetrics(context.Background())

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
		defaultLoadUpperBoundRatio, defaultLoadThresholdMetPercentage)
	metaServer := makeMetaServer(metric.NewFakeMetricsFetcher(metrics.DummyMetrics{}), cpuTopology)
	stateImpl, err := makeState(cpuTopology)
	as.Nil(err)

	plugin := NewCPUPressureEviction(metrics.DummyMetrics{}, metaServer, conf, stateImpl)
	as.Nil(err)
	as.NotNil(plugin)

	pod1UID := string(uuid.NewUUID())
	pod2UID := string(uuid.NewUUID())
	pod3UID := string(uuid.NewUUID())
	testName := "test"

	tests := []struct {
		name               string
		podEntries         qrmstate.PodEntries
		loads              map[string]map[string]float64
		wantResp           *evictionpluginapi.GetTopEvictionPodsResponse
		wantEvictPodUIDSet sets.String
	}{
		{
			name:     "not met load threshold",
			wantResp: &evictionpluginapi.GetTopEvictionPodsResponse{},
			podEntries: qrmstate.PodEntries{
				pod1UID: qrmstate.ContainerEntries{
					testName: &qrmstate.AllocationInfo{
						PodUid:                   pod1UID,
						PodNamespace:             testName,
						PodName:                  testName,
						ContainerName:            testName,
						ContainerType:            pluginapi.ContainerType_MAIN.String(),
						ContainerIndex:           0,
						RampUp:                   false,
						OwnerPoolName:            qrmstate.PoolNameShare,
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
				qrmstate.PoolNameShare: qrmstate.ContainerEntries{
					"": &qrmstate.AllocationInfo{
						PodUid:                   qrmstate.PoolNameShare,
						OwnerPoolName:            qrmstate.PoolNameShare,
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
			podEntries: qrmstate.PodEntries{
				pod1UID: qrmstate.ContainerEntries{
					testName: &qrmstate.AllocationInfo{
						PodUid:                   pod1UID,
						PodNamespace:             testName,
						PodName:                  testName,
						ContainerName:            testName,
						ContainerType:            pluginapi.ContainerType_MAIN.String(),
						ContainerIndex:           0,
						RampUp:                   false,
						OwnerPoolName:            qrmstate.PoolNameShare,
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
				qrmstate.PoolNameShare: qrmstate.ContainerEntries{
					"": &qrmstate.AllocationInfo{
						PodUid:                   qrmstate.PoolNameShare,
						OwnerPoolName:            qrmstate.PoolNameShare,
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
			podEntries: qrmstate.PodEntries{
				pod1UID: qrmstate.ContainerEntries{
					testName: &qrmstate.AllocationInfo{
						PodUid:                   pod1UID,
						PodNamespace:             testName,
						PodName:                  testName,
						ContainerName:            testName,
						ContainerType:            pluginapi.ContainerType_MAIN.String(),
						ContainerIndex:           0,
						RampUp:                   false,
						OwnerPoolName:            qrmstate.PoolNameShare,
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
				pod2UID: qrmstate.ContainerEntries{
					testName: &qrmstate.AllocationInfo{
						PodUid:                   pod2UID,
						PodNamespace:             testName,
						PodName:                  testName,
						ContainerName:            testName,
						ContainerType:            pluginapi.ContainerType_MAIN.String(),
						ContainerIndex:           0,
						RampUp:                   false,
						OwnerPoolName:            qrmstate.PoolNameShare,
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
				pod3UID: qrmstate.ContainerEntries{
					testName: &qrmstate.AllocationInfo{
						PodUid:                   pod3UID,
						PodNamespace:             testName,
						PodName:                  testName,
						ContainerName:            testName,
						ContainerType:            pluginapi.ContainerType_MAIN.String(),
						ContainerIndex:           0,
						RampUp:                   false,
						OwnerPoolName:            qrmstate.PoolNameReclaim,
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
				qrmstate.PoolNameShare: qrmstate.ContainerEntries{
					"": &qrmstate.AllocationInfo{
						PodUid:                   qrmstate.PoolNameShare,
						OwnerPoolName:            qrmstate.PoolNameShare,
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
				qrmstate.PoolNameReclaim: qrmstate.ContainerEntries{
					"": &qrmstate.AllocationInfo{
						PodUid:                   qrmstate.PoolNameReclaim,
						OwnerPoolName:            qrmstate.PoolNameReclaim,
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

			plugin.(*CPUPressureLoadEviction).state = stateImpl

			plugin.(*CPUPressureLoadEviction).metaServer.MetricsFetcher = fakeMetricsFetcher

			plugin.(*CPUPressureLoadEviction).collectMetrics(context.Background())

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

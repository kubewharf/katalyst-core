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
	"math"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
	qrmstate "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric"
	metrictypes "github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric/types"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
	utilmetric "github.com/kubewharf/katalyst-core/pkg/util/metric"
)

const (
	defaultMetricRingSize                           = 1
	defaultCPUPressureEvictionPodGracePeriodSeconds = -1
	defaultLoadUpperBoundRatio                      = 1.8
	defaultLoadThresholdMetPercentage               = 0.8
	defaultReservedForAllocate                      = "4"
	defaultReservedForReclaim                       = "4"
	defaultReservedForSystem                        = 0
)

func makeMetaServer(metricsFetcher metrictypes.MetricsFetcher, cpuTopology *machine.CPUTopology) *metaserver.MetaServer {
	metaServer := &metaserver.MetaServer{
		MetaAgent: &agent.MetaAgent{},
	}

	metaServer.MetricsFetcher = metricsFetcher
	metaServer.KatalystMachineInfo = &machine.KatalystMachineInfo{
		CPUTopology: cpuTopology,
	}

	return metaServer
}

func makeConf(metricRingSize int, gracePeriod int64, loadUpperBoundRatio,
	loadThresholdMetPercentage float64, reservedForReclaim, reservedForAllocate string, reservedForSystem int) *config.Configuration {
	conf := config.NewConfiguration()
	conf.GetDynamicConfiguration().EnableLoadEviction = true
	conf.GetDynamicConfiguration().LoadMetricRingSize = metricRingSize
	conf.GetDynamicConfiguration().LoadUpperBoundRatio = loadUpperBoundRatio
	conf.GetDynamicConfiguration().LoadThresholdMetPercentage = loadThresholdMetPercentage
	conf.GetDynamicConfiguration().CPUPressureEvictionConfiguration.GracePeriod = gracePeriod
	conf.GetDynamicConfiguration().ReservedResourceForAllocate = v1.ResourceList{
		v1.ResourceCPU: resource.MustParse(reservedForAllocate),
	}
	conf.GetDynamicConfiguration().MinReclaimedResourceForAllocate = v1.ResourceList{
		v1.ResourceCPU: resource.MustParse(reservedForReclaim),
	}
	conf.ReservedCPUCores = reservedForSystem
	conf.LoadPressureEvictionSkipPools = []string{
		qrmstate.PoolNameReclaim,
		qrmstate.PoolNameDedicated,
		qrmstate.PoolNameFallback,
		qrmstate.PoolNameReserve,
	}
	return conf
}

func makeState(topo *machine.CPUTopology) (qrmstate.State, error) {
	tmpDir, err := os.MkdirTemp("", "checkpoint-makeState")
	if err != nil {
		return nil, fmt.Errorf("make tmp dir for checkpoint failed with error: %v", err)
	}
	return qrmstate.NewCheckpointState(tmpDir, "test", "test", topo, false)
}

func TestNewCPUPressureLoadEviction(t *testing.T) {
	t.Parallel()

	as := require.New(t)

	cpuTopology, err := machine.GenerateDummyCPUTopology(16, 2, 4)
	as.Nil(err)
	conf := makeConf(defaultMetricRingSize, int64(defaultCPUPressureEvictionPodGracePeriodSeconds),
		defaultLoadUpperBoundRatio, defaultLoadThresholdMetPercentage, defaultReservedForReclaim, defaultReservedForAllocate, defaultReservedForSystem)
	metaServer := makeMetaServer(metric.NewFakeMetricsFetcher(metrics.DummyMetrics{}), cpuTopology)
	stateImpl, err := makeState(cpuTopology)
	as.Nil(err)

	plugin, createPluginErr := NewCPUPressureLoadEviction(metrics.DummyMetrics{}, metaServer, conf, stateImpl)
	as.Nil(createPluginErr)
	as.Nil(err)
	as.NotNil(plugin)
}

func TestThresholdMet(t *testing.T) {
	t.Parallel()

	as := require.New(t)

	cpuTopology, err := machine.GenerateDummyCPUTopology(16, 2, 4)
	as.Nil(err)
	conf := makeConf(defaultMetricRingSize, int64(defaultCPUPressureEvictionPodGracePeriodSeconds),
		defaultLoadUpperBoundRatio, defaultLoadThresholdMetPercentage, defaultReservedForReclaim, defaultReservedForAllocate, defaultReservedForSystem)
	metaServer := makeMetaServer(metric.NewFakeMetricsFetcher(metrics.DummyMetrics{}), cpuTopology)
	stateImpl, err := makeState(cpuTopology)
	as.Nil(err)

	plugin, createPluginErr := NewCPUPressureLoadEviction(metrics.DummyMetrics{}, metaServer, conf, stateImpl)
	as.Nil(createPluginErr)
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
			now := time.Now()

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
					fakeMetricsFetcher.SetContainerMetric(entryName, subEntryName, consts.MetricLoad1MinContainer, utilmetric.MetricData{Value: curLoad, Time: &now})
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
	t.Parallel()

	as := require.New(t)

	cpuTopology, err := machine.GenerateDummyCPUTopology(16, 2, 4)
	as.Nil(err)
	conf := makeConf(defaultMetricRingSize, int64(defaultCPUPressureEvictionPodGracePeriodSeconds),
		defaultLoadUpperBoundRatio, defaultLoadThresholdMetPercentage, defaultReservedForReclaim, defaultReservedForAllocate, defaultReservedForSystem)
	metaServer := makeMetaServer(metric.NewFakeMetricsFetcher(metrics.DummyMetrics{}), cpuTopology)
	stateImpl, err := makeState(cpuTopology)
	as.Nil(err)

	plugin, createPluginErr := NewCPUPressureLoadEviction(metrics.DummyMetrics{}, metaServer, conf, stateImpl)
	as.Nil(createPluginErr)
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
			now := time.Now()

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
					fakeMetricsFetcher.SetContainerMetric(entryName, subEntryName, consts.MetricLoad1MinContainer, utilmetric.MetricData{Value: curLoad, Time: &now})
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

func TestCPUPressureLoadEviction_collectMetrics(t *testing.T) {
	t.Parallel()

	pod1UID := "pod1"
	pod2UID := "pod2"
	pod3UID := "pod3"
	pod4UID := "pod4"
	testName := "test"

	tests := []struct {
		name                    string
		reservedCPUForAllocate  string
		reservedCPUForReclaim   string
		reservedCPUForSystem    int
		enableReclaim           bool
		podEntries              qrmstate.PodEntries
		loads                   map[string]map[string]float64
		wantSharedPoolSnapshots MetricInfo
	}{
		{
			name:                   "use default bound, without dedicated core pod",
			reservedCPUForAllocate: "4",
			reservedCPUForReclaim:  "4",
			enableReclaim:          false,
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
					testName: 1,
				},
				pod2UID: {
					testName: 1.4,
				},
				pod3UID: {
					testName: 5,
				},
				pod4UID: {
					testName: 8,
				},
			},
			wantSharedPoolSnapshots: MetricInfo{
				Name:       consts.MetricLoad1MinContainer,
				Value:      2.4,
				UpperBound: 18,
				LowerBound: 10,
			},
		},
		{
			name:                   "use default bound, with dedicated core pod",
			reservedCPUForAllocate: "4",
			reservedCPUForReclaim:  "4",
			enableReclaim:          false,
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
						AllocationResult:         machine.MustParse("3-6,11-14"),
						OriginalAllocationResult: machine.MustParse("3-6,11-14"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(),
							1: machine.NewCPUSet(3, 11),
							2: machine.NewCPUSet(4, 5, 11, 12),
							3: machine.NewCPUSet(6, 14),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(),
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
						AllocationResult:         machine.MustParse("3-6,11-14"),
						OriginalAllocationResult: machine.MustParse("3-6,11-14"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(),
							1: machine.NewCPUSet(3, 11),
							2: machine.NewCPUSet(4, 5, 11, 12),
							3: machine.NewCPUSet(6, 14),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(),
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
						AllocationResult:         machine.NewCPUSet(7, 10, 15),
						OriginalAllocationResult: machine.NewCPUSet(7, 10, 15),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							1: machine.NewCPUSet(10),
							3: machine.NewCPUSet(7, 15),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
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
				pod4UID: qrmstate.ContainerEntries{
					testName: &qrmstate.AllocationInfo{
						PodUid:                   pod4UID,
						PodNamespace:             testName,
						PodName:                  testName,
						ContainerName:            testName,
						ContainerType:            pluginapi.ContainerType_MAIN.String(),
						ContainerIndex:           0,
						RampUp:                   false,
						OwnerPoolName:            qrmstate.PoolNameDedicated,
						AllocationResult:         machine.NewCPUSet(0-1, 8-9),
						OriginalAllocationResult: machine.NewCPUSet(0-1, 8-9),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(0, 1, 8, 9),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(0, 1, 8, 9),
						},
						Labels: map[string]string{
							apiconsts.PodAnnotationQoSLevelKey: apiconsts.PodAnnotationQoSLevelDedicatedCores,
						},
						Annotations: map[string]string{
							apiconsts.PodAnnotationQoSLevelKey: apiconsts.PodAnnotationQoSLevelDedicatedCores,
						},
						QoSLevel:        apiconsts.PodAnnotationQoSLevelDedicatedCores,
						RequestQuantity: 2,
					},
				},

				qrmstate.PoolNameShare: qrmstate.ContainerEntries{
					"": &qrmstate.AllocationInfo{
						PodUid:                   qrmstate.PoolNameShare,
						OwnerPoolName:            qrmstate.PoolNameShare,
						AllocationResult:         machine.MustParse("3-6,11-14"),
						OriginalAllocationResult: machine.MustParse("3-6,11-14"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(),
							1: machine.NewCPUSet(3, 11),
							2: machine.NewCPUSet(4, 5, 11, 12),
							3: machine.NewCPUSet(6, 14),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(),
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
						AllocationResult:         machine.MustParse("7,10,15"),
						OriginalAllocationResult: machine.MustParse("7,10,15"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							1: machine.NewCPUSet(10),
							3: machine.NewCPUSet(7, 15),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							1: machine.NewCPUSet(10),
							3: machine.NewCPUSet(7, 15),
						},
					},
				},
			},
			loads: map[string]map[string]float64{
				pod1UID: {
					testName: 1,
				},
				pod2UID: {
					testName: 2.4,
				},
				pod3UID: {
					testName: 5,
				},
				pod4UID: {
					testName: 8,
				},
			},
			wantSharedPoolSnapshots: MetricInfo{
				Name:       consts.MetricLoad1MinContainer,
				Value:      3.4,
				UpperBound: 8 * 1.8,
				LowerBound: 8,
			},
		},
		{
			name:                   "use dynamic bound, has pressure, with dedicated core pod",
			reservedCPUForAllocate: "0",
			reservedCPUForReclaim:  "4",
			reservedCPUForSystem:   4,
			enableReclaim:          true,
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
						AllocationResult:         machine.MustParse("3-6,11-14"),
						OriginalAllocationResult: machine.MustParse("3-6,11-14"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(),
							1: machine.NewCPUSet(3, 11),
							2: machine.NewCPUSet(4, 5, 11, 12),
							3: machine.NewCPUSet(6, 14),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(),
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
						AllocationResult:         machine.MustParse("3-6,11-14"),
						OriginalAllocationResult: machine.MustParse("3-6,11-14"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(),
							1: machine.NewCPUSet(3, 11),
							2: machine.NewCPUSet(4, 5, 11, 12),
							3: machine.NewCPUSet(6, 14),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(),
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
						AllocationResult:         machine.NewCPUSet(7, 10, 15),
						OriginalAllocationResult: machine.NewCPUSet(7, 10, 15),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							1: machine.NewCPUSet(10),
							3: machine.NewCPUSet(7, 15),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
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
				pod4UID: qrmstate.ContainerEntries{
					testName: &qrmstate.AllocationInfo{
						PodUid:                   pod4UID,
						PodNamespace:             testName,
						PodName:                  testName,
						ContainerName:            testName,
						ContainerType:            pluginapi.ContainerType_MAIN.String(),
						ContainerIndex:           0,
						RampUp:                   false,
						OwnerPoolName:            qrmstate.PoolNameDedicated,
						AllocationResult:         machine.NewCPUSet(0-1, 8-9),
						OriginalAllocationResult: machine.NewCPUSet(0-1, 8-9),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(0, 1, 8, 9),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(0, 1, 8, 9),
						},
						Labels: map[string]string{
							apiconsts.PodAnnotationQoSLevelKey: apiconsts.PodAnnotationQoSLevelDedicatedCores,
						},
						Annotations: map[string]string{
							apiconsts.PodAnnotationQoSLevelKey: apiconsts.PodAnnotationQoSLevelDedicatedCores,
						},
						QoSLevel:        apiconsts.PodAnnotationQoSLevelDedicatedCores,
						RequestQuantity: 2,
					},
				},

				qrmstate.PoolNameShare: qrmstate.ContainerEntries{
					"": &qrmstate.AllocationInfo{
						PodUid:                   qrmstate.PoolNameShare,
						OwnerPoolName:            qrmstate.PoolNameShare,
						AllocationResult:         machine.MustParse("3-6,11-14"),
						OriginalAllocationResult: machine.MustParse("3-6,11-14"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(),
							1: machine.NewCPUSet(3, 11),
							2: machine.NewCPUSet(4, 5, 11, 12),
							3: machine.NewCPUSet(6, 14),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(),
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
						AllocationResult:         machine.MustParse("7,10,15"),
						OriginalAllocationResult: machine.MustParse("7,10,15"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							1: machine.NewCPUSet(10),
							3: machine.NewCPUSet(7, 15),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							1: machine.NewCPUSet(10),
							3: machine.NewCPUSet(7, 15),
						},
					},
				},
			},
			loads: map[string]map[string]float64{
				pod1UID: {
					testName: 1,
				},
				pod2UID: {
					testName: 2.4,
				},
				pod3UID: {
					testName: 5,
				},
				pod4UID: {
					testName: 8,
				},
			},
			wantSharedPoolSnapshots: MetricInfo{
				Name:       consts.MetricLoad1MinContainer,
				Value:      3.4,
				LowerBound: 1,
				UpperBound: 8 * 1.8,
			},
		},
		{
			name:                   "use dynamic bound, no pressure, with dedicated core pod",
			reservedCPUForAllocate: "0",
			reservedCPUForReclaim:  "4",
			reservedCPUForSystem:   0,
			enableReclaim:          true,
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
						AllocationResult:         machine.MustParse("3-6,11-14"),
						OriginalAllocationResult: machine.MustParse("3-6,11-14"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(),
							1: machine.NewCPUSet(3, 11),
							2: machine.NewCPUSet(4, 5, 11, 12),
							3: machine.NewCPUSet(6, 14),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(),
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
						AllocationResult:         machine.MustParse("3-6,11-14"),
						OriginalAllocationResult: machine.MustParse("3-6,11-14"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(),
							1: machine.NewCPUSet(3, 11),
							2: machine.NewCPUSet(4, 5, 11, 12),
							3: machine.NewCPUSet(6, 14),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(),
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
						AllocationResult:         machine.NewCPUSet(7, 10, 15),
						OriginalAllocationResult: machine.NewCPUSet(7, 10, 15),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							1: machine.NewCPUSet(10),
							3: machine.NewCPUSet(7, 15),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
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
				pod4UID: qrmstate.ContainerEntries{
					testName: &qrmstate.AllocationInfo{
						PodUid:                   pod4UID,
						PodNamespace:             testName,
						PodName:                  testName,
						ContainerName:            testName,
						ContainerType:            pluginapi.ContainerType_MAIN.String(),
						ContainerIndex:           0,
						RampUp:                   false,
						OwnerPoolName:            qrmstate.PoolNameDedicated,
						AllocationResult:         machine.NewCPUSet(0-1, 8-9),
						OriginalAllocationResult: machine.NewCPUSet(0-1, 8-9),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(0, 1, 8, 9),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(0, 1, 8, 9),
						},
						Labels: map[string]string{
							apiconsts.PodAnnotationQoSLevelKey: apiconsts.PodAnnotationQoSLevelDedicatedCores,
						},
						Annotations: map[string]string{
							apiconsts.PodAnnotationQoSLevelKey: apiconsts.PodAnnotationQoSLevelDedicatedCores,
						},
						QoSLevel:        apiconsts.PodAnnotationQoSLevelDedicatedCores,
						RequestQuantity: 2,
					},
				},

				qrmstate.PoolNameShare: qrmstate.ContainerEntries{
					"": &qrmstate.AllocationInfo{
						PodUid:                   qrmstate.PoolNameShare,
						OwnerPoolName:            qrmstate.PoolNameShare,
						AllocationResult:         machine.MustParse("3-6,11-14"),
						OriginalAllocationResult: machine.MustParse("3-6,11-14"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(),
							1: machine.NewCPUSet(3, 11),
							2: machine.NewCPUSet(4, 5, 11, 12),
							3: machine.NewCPUSet(6, 14),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(),
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
						AllocationResult:         machine.MustParse("7,10,15"),
						OriginalAllocationResult: machine.MustParse("7,10,15"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							1: machine.NewCPUSet(10),
							3: machine.NewCPUSet(7, 15),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							1: machine.NewCPUSet(10),
							3: machine.NewCPUSet(7, 15),
						},
					},
				},
			},
			loads: map[string]map[string]float64{
				pod1UID: {
					testName: 1,
				},
				pod2UID: {
					testName: 2.4,
				},
				pod3UID: {
					testName: 5,
				},
				pod4UID: {
					testName: 8,
				},
			},
			wantSharedPoolSnapshots: MetricInfo{
				Name:       consts.MetricLoad1MinContainer,
				Value:      3.4,
				LowerBound: 4.4,
				UpperBound: 4.4 * 1.8,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			as := require.New(t)

			cpuTopology, err := machine.GenerateDummyCPUTopology(16, 2, 4)
			as.Nil(err)
			conf := makeConf(defaultMetricRingSize, int64(defaultCPUPressureEvictionPodGracePeriodSeconds),
				defaultLoadUpperBoundRatio, defaultLoadThresholdMetPercentage, tt.reservedCPUForReclaim, tt.reservedCPUForAllocate, tt.reservedCPUForSystem)
			conf.GetDynamicConfiguration().EnableReclaim = tt.enableReclaim
			stateImpl, err := makeState(cpuTopology)
			as.Nil(err)

			fakeMetricsFetcher := metric.NewFakeMetricsFetcher(metrics.DummyMetrics{}).(*metric.FakeMetricsFetcher)
			metaServer := makeMetaServer(fakeMetricsFetcher, cpuTopology)
			assert.NotNil(t, fakeMetricsFetcher)

			now := time.Now()
			for entryName, entries := range tt.podEntries {
				for subEntryName, entry := range entries {
					stateImpl.SetAllocationInfo(entryName, subEntryName, entry)

					if entries.IsPoolEntry() {
						continue
					}

					curLoad, found := tt.loads[entryName][subEntryName]
					as.True(found)
					fakeMetricsFetcher.SetContainerMetric(entryName, subEntryName, consts.MetricLoad1MinContainer, utilmetric.MetricData{Value: curLoad, Time: &now})
				}
			}

			plugin, createPluginErr := NewCPUPressureLoadEviction(metrics.DummyMetrics{}, metaServer, conf, stateImpl)
			as.Nil(createPluginErr)
			as.Nil(err)
			as.NotNil(plugin)
			p := plugin.(*CPUPressureLoadEviction)
			p.collectMetrics(context.TODO())
			metricRing := p.metricsHistory[consts.MetricLoad1MinContainer][qrmstate.PoolNameShare][""]

			snapshot := metricRing.Queue[metricRing.CurrentIndex]
			as.True(math.Abs(tt.wantSharedPoolSnapshots.Value-snapshot.Info.Value) < 0.01)
			as.True(math.Abs(tt.wantSharedPoolSnapshots.UpperBound-snapshot.Info.UpperBound) < 0.01)
			as.True(math.Abs(tt.wantSharedPoolSnapshots.LowerBound-snapshot.Info.LowerBound) < 0.01)
		})
	}
}

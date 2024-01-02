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

package memory

import (
	"context"
	"fmt"
	"io/ioutil"
	"os"
	"reflect"
	"strconv"
	"testing"
	"time"

	info "github.com/google/cadvisor/info/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"
	"k8s.io/utils/pointer"

	"github.com/kubewharf/katalyst-api/pkg/consts"
	katalyst_base "github.com/kubewharf/katalyst-core/cmd/base"
	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/options"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	qrmstate "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/memory/dynamicpolicy/memoryadvisor"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	memadvisorplugin "github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/qosaware/resource/memory/plugin"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/config"
	coreconsts "github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric"
	metrictypes "github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric/types"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/pod"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/spd"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	metricspool "github.com/kubewharf/katalyst-core/pkg/metrics/metrics-pool"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
	metricutil "github.com/kubewharf/katalyst-core/pkg/util/metric"
)

var (
	qosLevel2PoolName = map[string]string{
		consts.PodAnnotationQoSLevelSharedCores:    qrmstate.PoolNameShare,
		consts.PodAnnotationQoSLevelReclaimedCores: qrmstate.PoolNameReclaim,
		consts.PodAnnotationQoSLevelSystemCores:    qrmstate.PoolNameReserve,
		consts.PodAnnotationQoSLevelDedicatedCores: qrmstate.PoolNameDedicated,
	}
)

func makeContainerInfo(podUID, namespace, podName, containerName, qoSLevel string, annotations map[string]string,
	topologyAwareAssignments types.TopologyAwareAssignment, memoryRequest float64) *types.ContainerInfo {
	return &types.ContainerInfo{
		PodUID:                           podUID,
		PodNamespace:                     namespace,
		PodName:                          podName,
		ContainerName:                    containerName,
		ContainerIndex:                   0,
		Labels:                           nil,
		Annotations:                      annotations,
		QoSLevel:                         qoSLevel,
		CPURequest:                       0,
		MemoryRequest:                    memoryRequest,
		RampUp:                           false,
		ContainerType:                    v1alpha1.ContainerType_MAIN,
		OwnerPoolName:                    qosLevel2PoolName[qoSLevel],
		TopologyAwareAssignments:         topologyAwareAssignments,
		OriginalTopologyAwareAssignments: topologyAwareAssignments,
	}
}

func generateTestConfiguration(t *testing.T, checkpointDir, stateFileDir string) *config.Configuration {
	conf, err := options.NewOptions().Config()
	require.NoError(t, err)
	require.NotNil(t, conf)

	conf.GenericSysAdvisorConfiguration.StateFileDirectory = stateFileDir
	conf.MetaServerConfiguration.CheckpointManagerDir = checkpointDir
	conf.GetDynamicConfiguration().ReservedResourceForAllocate[v1.ResourceMemory] = resource.MustParse(fmt.Sprintf("%d", 4<<30))

	return conf
}

func newTestMemoryAdvisor(t *testing.T, pods []*v1.Pod, checkpointDir, stateFileDir string, fetcher metrictypes.MetricsFetcher, plugins []types.MemoryAdvisorPluginName) (*memoryResourceAdvisor, metacache.MetaCache) {
	conf := generateTestConfiguration(t, checkpointDir, stateFileDir)
	if len(plugins) == 0 {
		conf.MemoryAdvisorPlugins = []types.MemoryAdvisorPluginName{memadvisorplugin.CacheReaper}
	} else {
		conf.MemoryAdvisorPlugins = plugins
	}

	metaCache, err := metacache.NewMetaCacheImp(conf, metricspool.DummyMetricsEmitterPool{}, fetcher)
	require.NoError(t, err)
	require.NotNil(t, metaCache)

	genericCtx, err := katalyst_base.GenerateFakeGenericContext([]runtime.Object{})
	require.NoError(t, err)
	metaServer, err := metaserver.NewMetaServer(genericCtx.Client, metrics.DummyMetrics{}, conf)
	require.NoError(t, err)

	cpuTopology, err := machine.GenerateDummyCPUTopology(96, 2, 4)
	require.NoError(t, err)
	memoryTopology, err := machine.GenerateDummyMemoryTopology(4, 500<<30)
	require.NoError(t, err)

	metaServer.MetaAgent = &agent.MetaAgent{
		KatalystMachineInfo: &machine.KatalystMachineInfo{
			MachineInfo: &info.MachineInfo{
				MemoryCapacity: 1000 << 30,
			},
			CPUTopology:    cpuTopology,
			MemoryTopology: memoryTopology,
		},
		PodFetcher: &pod.PodFetcherStub{
			PodList: pods,
		},
		MetricsFetcher: fetcher,
	}

	err = metaServer.SetServiceProfilingManager(&spd.DummyServiceProfilingManager{})
	assert.NoError(t, err)

	mra := NewMemoryResourceAdvisor(conf, struct{}{}, metaCache, metaServer, metrics.DummyMetrics{})
	assert.NotNil(t, mra)

	return mra, metaCache
}

type nodeMetric struct {
	metricName  string
	metricValue metricutil.MetricData
}

type numaMetric struct {
	metricName  string
	metricValue metricutil.MetricData
	numaID      int
}

type containerMetric struct {
	metricName    string
	metricValue   metricutil.MetricData
	podUID        string
	containerName string
}

type containerNUMAMetric struct {
	metricName    string
	metricValue   metricutil.MetricData
	podUID        string
	containerName string
	numdID        int
}

type cgroupMetric struct {
	metricName  string
	metricValue metricutil.MetricData
	cgroupPath  string
}

var defaultNodeMetrics = []nodeMetric{
	{
		metricName:  coreconsts.MetricMemFreeSystem,
		metricValue: metricutil.MetricData{Value: 250 << 30},
	},
	{
		metricName:  coreconsts.MetricMemAvailableSystem,
		metricValue: metricutil.MetricData{Value: 300 << 30},
	},
	{
		metricName:  coreconsts.MetricMemFreeSystem,
		metricValue: metricutil.MetricData{Value: 100 << 30},
	},
	{
		metricName:  coreconsts.MetricMemPageCacheSystem,
		metricValue: metricutil.MetricData{Value: 100 << 30},
	},
	{
		metricName:  coreconsts.MetricMemBufferSystem,
		metricValue: metricutil.MetricData{Value: 100 << 30},
	},
	{
		metricName:  coreconsts.MetricMemTotalSystem,
		metricValue: metricutil.MetricData{Value: 500 << 30},
	},
	{
		metricName:  coreconsts.MetricMemScaleFactorSystem,
		metricValue: metricutil.MetricData{Value: 500},
	},
}

var defaultNumaMetrics = []numaMetric{
	{
		numaID:      0,
		metricName:  coreconsts.MetricMemFreeNuma,
		metricValue: metricutil.MetricData{Value: 60 << 30},
	},
	{
		numaID:      1,
		metricName:  coreconsts.MetricMemFreeNuma,
		metricValue: metricutil.MetricData{Value: 60 << 30},
	},
	{
		numaID:      2,
		metricName:  coreconsts.MetricMemFreeNuma,
		metricValue: metricutil.MetricData{Value: 60 << 30},
	},
	{
		numaID:      3,
		metricName:  coreconsts.MetricMemFreeNuma,
		metricValue: metricutil.MetricData{Value: 60 << 30},
	},
	{
		numaID:      0,
		metricName:  coreconsts.MetricMemTotalNuma,
		metricValue: metricutil.MetricData{Value: 120 << 30},
	},
	{
		numaID:      1,
		metricName:  coreconsts.MetricMemTotalNuma,
		metricValue: metricutil.MetricData{Value: 120 << 30},
	},
	{
		numaID:      2,
		metricName:  coreconsts.MetricMemTotalNuma,
		metricValue: metricutil.MetricData{Value: 120 << 30},
	},
	{
		numaID:      3,
		metricName:  coreconsts.MetricMemTotalNuma,
		metricValue: metricutil.MetricData{Value: 120 << 30},
	},
}

var dropCacheNodeMetrics = []nodeMetric{
	{
		metricName:  coreconsts.MetricMemFreeSystem,
		metricValue: metricutil.MetricData{Value: 30 << 30},
	},
	{
		metricName:  coreconsts.MetricMemTotalSystem,
		metricValue: metricutil.MetricData{Value: 500 << 30},
	},
	{
		metricName:  coreconsts.MetricMemScaleFactorSystem,
		metricValue: metricutil.MetricData{Value: 500},
	},
}

var dropCacheNUMAMetrics = []numaMetric{
	{
		numaID:      0,
		metricName:  coreconsts.MetricMemFreeNuma,
		metricValue: metricutil.MetricData{Value: 6 << 30},
	},
	{
		numaID:      1,
		metricName:  coreconsts.MetricMemFreeNuma,
		metricValue: metricutil.MetricData{Value: 60 << 30},
	},
	{
		numaID:      2,
		metricName:  coreconsts.MetricMemFreeNuma,
		metricValue: metricutil.MetricData{Value: 60 << 30},
	},
	{
		numaID:      3,
		metricName:  coreconsts.MetricMemFreeNuma,
		metricValue: metricutil.MetricData{Value: 60 << 30},
	},
	{
		numaID:      0,
		metricName:  coreconsts.MetricMemTotalNuma,
		metricValue: metricutil.MetricData{Value: 120 << 30},
	},
	{
		numaID:      1,
		metricName:  coreconsts.MetricMemTotalNuma,
		metricValue: metricutil.MetricData{Value: 120 << 30},
	},
	{
		numaID:      2,
		metricName:  coreconsts.MetricMemTotalNuma,
		metricValue: metricutil.MetricData{Value: 120 << 30},
	},
	{
		numaID:      3,
		metricName:  coreconsts.MetricMemTotalNuma,
		metricValue: metricutil.MetricData{Value: 120 << 30},
	},
}

var cgroupMetrics = []cgroupMetric{
	{
		metricName:  coreconsts.MetricMemRssCgroup,
		metricValue: metricutil.MetricData{Value: 100 << 30},
		cgroupPath:  "/kubepods/besteffort",
	},
	{
		metricName:  coreconsts.MetricMemUsageCgroup,
		metricValue: metricutil.MetricData{Value: 110 << 30},
		cgroupPath:  "/kubepods/besteffort",
	},
}

func TestUpdate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                 string
		pools                map[string]*types.PoolInfo
		containers           []*types.ContainerInfo
		pods                 []*v1.Pod
		wantHeadroom         resource.Quantity
		reclaimedEnable      bool
		needRecvAdvices      bool
		plugins              []types.MemoryAdvisorPluginName
		nodeMetrics          []nodeMetric
		numaMetrics          []numaMetric
		containerMetrics     []containerMetric
		containerNUMAMetrics []containerNUMAMetric
		cgroupMetrics        []cgroupMetric
		metricsFetcherSynced *bool
		wantAdviceResult     types.InternalMemoryCalculationResult
	}{
		{
			name:                 "metaCache not synced",
			pools:                map[string]*types.PoolInfo{},
			reclaimedEnable:      true,
			wantHeadroom:         resource.Quantity{},
			needRecvAdvices:      false,
			metricsFetcherSynced: pointer.Bool(false),
		},
		{
			name: "reserve pool only",
			pools: map[string]*types.PoolInfo{
				state.PoolNameReserve: {
					PoolName: state.PoolNameReserve,
					TopologyAwareAssignments: map[int]machine.CPUSet{
						0: machine.MustParse("0"),
						1: machine.MustParse("24"),
					},
					OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
						0: machine.MustParse("0"),
						1: machine.MustParse("24"),
					},
				},
			},
			reclaimedEnable: true,
			needRecvAdvices: true,
			wantHeadroom:    *resource.NewQuantity(996<<30, resource.DecimalSI),
			nodeMetrics:     defaultNodeMetrics,
			numaMetrics:     defaultNumaMetrics,
		},
		{
			name: "normal case",
			pools: map[string]*types.PoolInfo{
				state.PoolNameReserve: {
					PoolName: state.PoolNameReserve,
					TopologyAwareAssignments: map[int]machine.CPUSet{
						0: machine.MustParse("0"),
						1: machine.MustParse("24"),
					},
					OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
						0: machine.MustParse("0"),
						1: machine.MustParse("24"),
					},
				},
				state.PoolNameShare: {
					PoolName: state.PoolNameShare,
					TopologyAwareAssignments: map[int]machine.CPUSet{
						0: machine.MustParse("1"),
						1: machine.MustParse("25"),
					},
					OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
						0: machine.MustParse("1"),
						1: machine.MustParse("25"),
					},
				},
			},
			reclaimedEnable: true,
			needRecvAdvices: true,
			containers: []*types.ContainerInfo{
				makeContainerInfo("uid1", "default", "pod1", "c1", consts.PodAnnotationQoSLevelSharedCores, nil,
					map[int]machine.CPUSet{
						0: machine.MustParse("1"),
						1: machine.MustParse("25"),
					}, 0),
			},
			pods: []*v1.Pod{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "pod1",
						Namespace: "default",
						UID:       "uid1",
					},
				},
			},
			wantHeadroom: *resource.NewQuantity(988<<30, resource.DecimalSI),
			nodeMetrics:  defaultNodeMetrics,
			numaMetrics:  defaultNumaMetrics,
		},
		{
			name: "reclaimed disable case",
			pools: map[string]*types.PoolInfo{
				state.PoolNameReserve: {
					PoolName: state.PoolNameReserve,
					TopologyAwareAssignments: map[int]machine.CPUSet{
						0: machine.MustParse("0"),
						1: machine.MustParse("24"),
					},
					OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
						0: machine.MustParse("0"),
						1: machine.MustParse("24"),
					},
				},
				state.PoolNameShare: {
					PoolName: state.PoolNameShare,
					TopologyAwareAssignments: map[int]machine.CPUSet{
						0: machine.MustParse("1"),
						1: machine.MustParse("25"),
					},
					OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
						0: machine.MustParse("1"),
						1: machine.MustParse("25"),
					},
				},
			},
			reclaimedEnable: false,
			needRecvAdvices: true,
			containers: []*types.ContainerInfo{
				makeContainerInfo("uid1", "default", "pod1", "c1", consts.PodAnnotationQoSLevelSharedCores, nil,
					map[int]machine.CPUSet{
						0: machine.MustParse("1"),
						1: machine.MustParse("25"),
					}, 200<<30),
			},
			pods: []*v1.Pod{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "pod1",
						Namespace: "default",
						UID:       "uid1",
					},
				},
			},
			wantHeadroom: *resource.NewQuantity(796<<30, resource.DecimalSI),
			nodeMetrics:  defaultNodeMetrics,
			numaMetrics:  defaultNumaMetrics,
		},
		{
			name: "node pressure drop cache",
			pools: map[string]*types.PoolInfo{
				state.PoolNameReserve: {
					PoolName: state.PoolNameReserve,
					TopologyAwareAssignments: map[int]machine.CPUSet{
						0: machine.MustParse("0"),
						1: machine.MustParse("24"),
					},
					OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
						0: machine.MustParse("0"),
						1: machine.MustParse("24"),
					},
				},
			},
			reclaimedEnable: false,
			needRecvAdvices: true,
			containers: []*types.ContainerInfo{
				makeContainerInfo("uid1", "default", "pod1", "c1", consts.PodAnnotationQoSLevelReclaimedCores, nil,
					map[int]machine.CPUSet{
						0: machine.MustParse("1"),
						1: machine.MustParse("25"),
					}, 200<<30),
				makeContainerInfo("uid2", "default", "pod2", "c2", consts.PodAnnotationQoSLevelReclaimedCores, nil,
					map[int]machine.CPUSet{
						0: machine.MustParse("1"),
						1: machine.MustParse("25"),
					}, 200<<30),
				makeContainerInfo("uid3", "default", "pod3", "c3", consts.PodAnnotationQoSLevelReclaimedCores, nil,
					map[int]machine.CPUSet{
						0: machine.MustParse("1"),
						1: machine.MustParse("25"),
					}, 200<<30),
			},
			wantHeadroom: *resource.NewQuantity(996<<30, resource.DecimalSI),
			nodeMetrics:  dropCacheNodeMetrics,
			numaMetrics:  defaultNumaMetrics,
			containerMetrics: []containerMetric{
				{
					metricName:    coreconsts.MetricMemCacheContainer,
					metricValue:   metricutil.MetricData{Value: 60 << 30},
					podUID:        "uid1",
					containerName: "c1",
				},
				{
					metricName:    coreconsts.MetricMemCacheContainer,
					metricValue:   metricutil.MetricData{Value: 10 << 30},
					podUID:        "uid2",
					containerName: "c2",
				},
				{
					metricName:    coreconsts.MetricMemCacheContainer,
					metricValue:   metricutil.MetricData{Value: 20 << 30},
					podUID:        "uid3",
					containerName: "c3",
				},
			},
			wantAdviceResult: types.InternalMemoryCalculationResult{
				ContainerEntries: []types.ContainerMemoryAdvices{
					{
						PodUID:        "uid1",
						ContainerName: "c1",
						Values:        map[string]string{string(memoryadvisor.ControlKnobKeyDropCache): "true"},
					},
					{
						PodUID:        "uid3",
						ContainerName: "c3",
						Values:        map[string]string{string(memoryadvisor.ControlKnobKeyDropCache): "true"},
					},
				},
			},
		},
		{
			name: "numa0 pressure drop cache",
			pools: map[string]*types.PoolInfo{
				state.PoolNameReserve: {
					PoolName: state.PoolNameReserve,
					TopologyAwareAssignments: map[int]machine.CPUSet{
						0: machine.MustParse("0"),
						1: machine.MustParse("24"),
					},
					OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
						0: machine.MustParse("0"),
						1: machine.MustParse("24"),
					},
				},
			},
			reclaimedEnable: false,
			needRecvAdvices: true,
			containers: []*types.ContainerInfo{
				makeContainerInfo("uid1", "default", "pod1", "c1", consts.PodAnnotationQoSLevelReclaimedCores, nil,
					map[int]machine.CPUSet{
						0: machine.MustParse("1"),
						1: machine.MustParse("25"),
					}, 200<<30),
				makeContainerInfo("uid2", "default", "pod2", "c2", consts.PodAnnotationQoSLevelReclaimedCores, nil,
					map[int]machine.CPUSet{
						0: machine.MustParse("1"),
						1: machine.MustParse("25"),
					}, 200<<30),
				makeContainerInfo("uid3", "default", "pod3", "c3", consts.PodAnnotationQoSLevelReclaimedCores, nil,
					map[int]machine.CPUSet{
						0: machine.MustParse("1"),
						1: machine.MustParse("25"),
					}, 200<<30),
			},
			wantHeadroom: *resource.NewQuantity(996<<30, resource.DecimalSI),
			nodeMetrics:  defaultNodeMetrics,
			numaMetrics:  dropCacheNUMAMetrics,
			containerNUMAMetrics: []containerNUMAMetric{
				{
					metricName:    coreconsts.MetricsMemFilePerNumaContainer,
					metricValue:   metricutil.MetricData{Value: 10 << 20},
					podUID:        "uid1",
					containerName: "c1",
					numdID:        0,
				},
				{
					metricName:    coreconsts.MetricsMemFilePerNumaContainer,
					metricValue:   metricutil.MetricData{Value: 9 << 30},
					podUID:        "uid2",
					containerName: "c2",
					numdID:        0,
				},
				{
					metricName:    coreconsts.MetricsMemFilePerNumaContainer,
					metricValue:   metricutil.MetricData{Value: 2 << 30},
					podUID:        "uid3",
					containerName: "c3",
					numdID:        0,
				},
				{
					metricName:    coreconsts.MetricsMemFilePerNumaContainer,
					metricValue:   metricutil.MetricData{Value: 10 << 20},
					podUID:        "uid1",
					containerName: "c1",
					numdID:        1,
				},
				{
					metricName:    coreconsts.MetricsMemFilePerNumaContainer,
					metricValue:   metricutil.MetricData{Value: 9 << 30},
					podUID:        "uid2",
					containerName: "c2",
					numdID:        1,
				},
				{
					metricName:    coreconsts.MetricsMemFilePerNumaContainer,
					metricValue:   metricutil.MetricData{Value: 2 << 30},
					podUID:        "uid3",
					containerName: "c3",
					numdID:        1,
				},
			},
			wantAdviceResult: types.InternalMemoryCalculationResult{
				ContainerEntries: []types.ContainerMemoryAdvices{
					{
						PodUID:        "uid3",
						ContainerName: "c3",
						Values:        map[string]string{string(memoryadvisor.ControlKnobKeyDropCache): "true"},
					},
					{
						PodUID:        "uid2",
						ContainerName: "c2",
						Values:        map[string]string{string(memoryadvisor.ControlKnobKeyDropCache): "true"},
					},
				},
			},
		},
		{
			name: "set reclaimed group memory limit",
			pools: map[string]*types.PoolInfo{
				state.PoolNameReserve: {
					PoolName: state.PoolNameReserve,
					TopologyAwareAssignments: map[int]machine.CPUSet{
						0: machine.MustParse("0"),
						1: machine.MustParse("24"),
					},
					OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
						0: machine.MustParse("0"),
						1: machine.MustParse("24"),
					},
				},
			},
			reclaimedEnable: true,
			needRecvAdvices: true,
			wantHeadroom:    *resource.NewQuantity(996<<30, resource.DecimalSI),
			nodeMetrics:     defaultNodeMetrics,
			numaMetrics:     defaultNumaMetrics,
			cgroupMetrics:   cgroupMetrics,
			plugins:         []types.MemoryAdvisorPluginName{memadvisorplugin.MemoryGuard},
			wantAdviceResult: types.InternalMemoryCalculationResult{
				ExtraEntries: []types.ExtraMemoryAdvices{
					{
						CgroupPath: "/kubepods/besteffort",
						Values:     map[string]string{string(memoryadvisor.ControlKnobKeyMemoryLimitInBytes): strconv.Itoa(375 << 30)},
					},
				},
			},
		},
		{
			name: "bind memset",
			pools: map[string]*types.PoolInfo{
				state.PoolNameReserve: {
					PoolName: state.PoolNameReserve,
					TopologyAwareAssignments: map[int]machine.CPUSet{
						0: machine.MustParse("0"),
						1: machine.MustParse("24"),
					},
					OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
						0: machine.MustParse("0"),
						1: machine.MustParse("24"),
					},
				},
			},
			reclaimedEnable: false,
			needRecvAdvices: true,
			containers: []*types.ContainerInfo{
				makeContainerInfo("uid1", "default", "pod1", "c1", consts.PodAnnotationQoSLevelReclaimedCores, nil,
					map[int]machine.CPUSet{
						0: machine.MustParse("1"),
						1: machine.MustParse("25"),
					}, 200<<30),
				makeContainerInfo("uid2", "default", "pod2", "c2", consts.PodAnnotationQoSLevelReclaimedCores, nil,
					map[int]machine.CPUSet{
						0: machine.MustParse("1"),
						1: machine.MustParse("25"),
					}, 200<<30),
				makeContainerInfo("uid3", "default", "pod3", "c3", consts.PodAnnotationQoSLevelReclaimedCores, nil,
					map[int]machine.CPUSet{
						0: machine.MustParse("1"),
					}, 200<<30),
				makeContainerInfo("uid4", "default", "pod4", "c4", consts.PodAnnotationQoSLevelDedicatedCores, map[string]string{
					consts.PodAnnotationMemoryEnhancementNumaBinding:   consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
					consts.PodAnnotationMemoryEnhancementNumaExclusive: consts.PodAnnotationMemoryEnhancementNumaExclusiveEnable},
					map[int]machine.CPUSet{
						0: machine.MustParse("1"),
					}, 200<<30),
			},
			plugins:      []types.MemoryAdvisorPluginName{memadvisorplugin.MemsetBinder},
			nodeMetrics:  defaultNodeMetrics,
			numaMetrics:  defaultNumaMetrics,
			wantHeadroom: *resource.NewQuantity(871<<30, resource.DecimalSI),
			wantAdviceResult: types.InternalMemoryCalculationResult{
				ContainerEntries: []types.ContainerMemoryAdvices{
					{
						PodUID:        "uid1",
						ContainerName: "c1",
						Values:        map[string]string{string(memoryadvisor.ControlKnobKeyCPUSetMems): "1-3"},
					},
					{
						PodUID:        "uid2",
						ContainerName: "c2",
						Values:        map[string]string{string(memoryadvisor.ControlKnobKeyCPUSetMems): "1-3"},
					},
					{
						PodUID:        "uid3",
						ContainerName: "c3",
						Values:        map[string]string{string(memoryadvisor.ControlKnobKeyCPUSetMems): "1-3"},
					},
				},
			},
		},
		{
			name: "bind memset(numa1 pressure)",
			pools: map[string]*types.PoolInfo{
				state.PoolNameReserve: {
					PoolName: state.PoolNameReserve,
					TopologyAwareAssignments: map[int]machine.CPUSet{
						0: machine.MustParse("0"),
						1: machine.MustParse("24"),
					},
					OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
						0: machine.MustParse("0"),
						1: machine.MustParse("24"),
					},
				},
			},
			reclaimedEnable: false,
			needRecvAdvices: true,
			containers: []*types.ContainerInfo{
				makeContainerInfo("uid1", "default", "pod1", "c1", consts.PodAnnotationQoSLevelReclaimedCores, nil,
					map[int]machine.CPUSet{
						0: machine.MustParse("1"),
						1: machine.MustParse("25"),
					}, 200<<30),
				makeContainerInfo("uid2", "default", "pod2", "c2", consts.PodAnnotationQoSLevelReclaimedCores, nil,
					map[int]machine.CPUSet{
						0: machine.MustParse("1"),
						1: machine.MustParse("25"),
					}, 200<<30),
				makeContainerInfo("uid3", "default", "pod3", "c3", consts.PodAnnotationQoSLevelReclaimedCores, nil,
					map[int]machine.CPUSet{
						0: machine.MustParse("1"),
					}, 200<<30),
				makeContainerInfo("uid4", "default", "pod4", "c4", consts.PodAnnotationQoSLevelDedicatedCores, map[string]string{
					consts.PodAnnotationMemoryEnhancementNumaBinding:   consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
					consts.PodAnnotationMemoryEnhancementNumaExclusive: consts.PodAnnotationMemoryEnhancementNumaExclusiveEnable},
					map[int]machine.CPUSet{
						0: machine.MustParse("1"),
					}, 200<<30),
			},
			plugins:     []types.MemoryAdvisorPluginName{memadvisorplugin.MemsetBinder},
			nodeMetrics: defaultNodeMetrics,
			numaMetrics: []numaMetric{
				{
					numaID:      0,
					metricName:  coreconsts.MetricMemFreeNuma,
					metricValue: metricutil.MetricData{Value: 60 << 30},
				},
				{
					numaID:      1,
					metricName:  coreconsts.MetricMemFreeNuma,
					metricValue: metricutil.MetricData{Value: 1 << 30},
				},
				{
					numaID:      2,
					metricName:  coreconsts.MetricMemFreeNuma,
					metricValue: metricutil.MetricData{Value: 60 << 30},
				},
				{
					numaID:      3,
					metricName:  coreconsts.MetricMemFreeNuma,
					metricValue: metricutil.MetricData{Value: 60 << 30},
				},
				{
					numaID:      0,
					metricName:  coreconsts.MetricMemTotalNuma,
					metricValue: metricutil.MetricData{Value: 120 << 30},
				},
				{
					numaID:      1,
					metricName:  coreconsts.MetricMemTotalNuma,
					metricValue: metricutil.MetricData{Value: 120 << 30},
				},
				{
					numaID:      2,
					metricName:  coreconsts.MetricMemTotalNuma,
					metricValue: metricutil.MetricData{Value: 120 << 30},
				},
				{
					numaID:      3,
					metricName:  coreconsts.MetricMemTotalNuma,
					metricValue: metricutil.MetricData{Value: 120 << 30},
				},
			},
			wantHeadroom: *resource.NewQuantity(871<<30, resource.DecimalSI),
			wantAdviceResult: types.InternalMemoryCalculationResult{
				ContainerEntries: []types.ContainerMemoryAdvices{
					{
						PodUID:        "uid1",
						ContainerName: "c1",
						Values:        map[string]string{string(memoryadvisor.ControlKnobKeyCPUSetMems): "2-3"},
					},
					{
						PodUID:        "uid2",
						ContainerName: "c2",
						Values:        map[string]string{string(memoryadvisor.ControlKnobKeyCPUSetMems): "2-3"},
					},
					{
						PodUID:        "uid3",
						ContainerName: "c3",
						Values:        map[string]string{string(memoryadvisor.ControlKnobKeyCPUSetMems): "2-3"},
					},
				},
			},
		},
		{
			name: "bind memset(numa1-3 pressure)",
			pools: map[string]*types.PoolInfo{
				state.PoolNameReserve: {
					PoolName: state.PoolNameReserve,
					TopologyAwareAssignments: map[int]machine.CPUSet{
						0: machine.MustParse("0"),
						1: machine.MustParse("24"),
					},
					OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
						0: machine.MustParse("0"),
						1: machine.MustParse("24"),
					},
				},
			},
			reclaimedEnable: false,
			needRecvAdvices: true,
			containers: []*types.ContainerInfo{
				makeContainerInfo("uid1", "default", "pod1", "c1", consts.PodAnnotationQoSLevelReclaimedCores, nil,
					map[int]machine.CPUSet{
						0: machine.MustParse("1"),
						1: machine.MustParse("25"),
					}, 200<<30),
				makeContainerInfo("uid2", "default", "pod2", "c2", consts.PodAnnotationQoSLevelReclaimedCores, nil,
					map[int]machine.CPUSet{
						0: machine.MustParse("1"),
						1: machine.MustParse("25"),
					}, 200<<30),
				makeContainerInfo("uid3", "default", "pod3", "c3", consts.PodAnnotationQoSLevelReclaimedCores, nil,
					map[int]machine.CPUSet{
						0: machine.MustParse("1"),
					}, 200<<30),
				makeContainerInfo("uid4", "default", "pod4", "c4", consts.PodAnnotationQoSLevelDedicatedCores, map[string]string{
					consts.PodAnnotationMemoryEnhancementNumaBinding:   consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
					consts.PodAnnotationMemoryEnhancementNumaExclusive: consts.PodAnnotationMemoryEnhancementNumaExclusiveEnable},
					map[int]machine.CPUSet{
						0: machine.MustParse("1"),
					}, 200<<30),
			},
			plugins:     []types.MemoryAdvisorPluginName{memadvisorplugin.MemsetBinder},
			nodeMetrics: defaultNodeMetrics,
			numaMetrics: []numaMetric{
				{
					numaID:      0,
					metricName:  coreconsts.MetricMemFreeNuma,
					metricValue: metricutil.MetricData{Value: 60 << 30},
				},
				{
					numaID:      1,
					metricName:  coreconsts.MetricMemFreeNuma,
					metricValue: metricutil.MetricData{Value: 1 << 30},
				},
				{
					numaID:      2,
					metricName:  coreconsts.MetricMemFreeNuma,
					metricValue: metricutil.MetricData{Value: 1 << 30},
				},
				{
					numaID:      3,
					metricName:  coreconsts.MetricMemFreeNuma,
					metricValue: metricutil.MetricData{Value: 1 << 30},
				},
				{
					numaID:      0,
					metricName:  coreconsts.MetricMemTotalNuma,
					metricValue: metricutil.MetricData{Value: 120 << 30},
				},
				{
					numaID:      1,
					metricName:  coreconsts.MetricMemTotalNuma,
					metricValue: metricutil.MetricData{Value: 120 << 30},
				},
				{
					numaID:      2,
					metricName:  coreconsts.MetricMemTotalNuma,
					metricValue: metricutil.MetricData{Value: 120 << 30},
				},
				{
					numaID:      3,
					metricName:  coreconsts.MetricMemTotalNuma,
					metricValue: metricutil.MetricData{Value: 120 << 30},
				},
			},
			wantHeadroom: *resource.NewQuantity(871<<30, resource.DecimalSI),
			wantAdviceResult: types.InternalMemoryCalculationResult{
				ContainerEntries: []types.ContainerMemoryAdvices{
					{
						PodUID:        "uid1",
						ContainerName: "c1",
						Values:        map[string]string{string(memoryadvisor.ControlKnobKeyCPUSetMems): "1-3"},
					},
					{
						PodUID:        "uid2",
						ContainerName: "c2",
						Values:        map[string]string{string(memoryadvisor.ControlKnobKeyCPUSetMems): "1-3"},
					},
					{
						PodUID:        "uid3",
						ContainerName: "c3",
						Values:        map[string]string{string(memoryadvisor.ControlKnobKeyCPUSetMems): "1-3"},
					},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ckDir, err := ioutil.TempDir("", "checkpoint-TestUpdate")
			require.NoError(t, err)
			defer os.RemoveAll(ckDir)

			sfDir, err := ioutil.TempDir("", "statefile")
			require.NoError(t, err)
			defer os.RemoveAll(sfDir)

			fetcher := metric.NewFakeMetricsFetcher(metrics.DummyMetrics{})
			metricsFetcher := fetcher.(*metric.FakeMetricsFetcher)
			for _, nodeMetric := range tt.nodeMetrics {
				metricsFetcher.SetNodeMetric(nodeMetric.metricName, nodeMetric.metricValue)
			}
			for _, numaMetric := range tt.numaMetrics {
				metricsFetcher.SetNumaMetric(numaMetric.numaID, numaMetric.metricName, numaMetric.metricValue)
			}
			for _, containerMetric := range tt.containerMetrics {
				metricsFetcher.SetContainerMetric(containerMetric.podUID, containerMetric.containerName, containerMetric.metricName, containerMetric.metricValue)
			}
			for _, containerNUMAMetric := range tt.containerNUMAMetrics {
				metricsFetcher.SetContainerNumaMetric(containerNUMAMetric.podUID, containerNUMAMetric.containerName, strconv.Itoa(containerNUMAMetric.numdID), containerNUMAMetric.metricName, containerNUMAMetric.metricValue)
			}
			for _, qosClassMetric := range tt.cgroupMetrics {
				metricsFetcher.SetCgroupMetric(qosClassMetric.cgroupPath, qosClassMetric.metricName, qosClassMetric.metricValue)
			}
			if tt.metricsFetcherSynced != nil {
				metricsFetcher.SetSynced(*tt.metricsFetcherSynced)
			}

			advisor, metaCache := newTestMemoryAdvisor(t, tt.pods, ckDir, sfDir, fetcher, tt.plugins)
			advisor.conf.GetDynamicConfiguration().EnableReclaim = tt.reclaimedEnable
			_, advisorRecvChInterface := advisor.GetChannels()

			recvCh := advisorRecvChInterface.(chan types.InternalMemoryCalculationResult)

			for poolName, poolInfo := range tt.pools {
				err := metaCache.SetPoolInfo(poolName, poolInfo)
				assert.NoError(t, err)
			}
			for _, c := range tt.containers {
				err := metaCache.SetContainerInfo(c.PodUID, c.ContainerName, c)
				assert.NoError(t, err)
			}

			go func() {
				c, _ := advisor.GetChannels()
				ch := c.(chan types.TriggerInfo)
				ch <- types.TriggerInfo{TimeStamp: time.Now()}
			}()

			ctx, cancel := context.WithCancel(context.Background())
			advisor.Run(ctx)

			time.Sleep(10 * time.Millisecond) // Wait some time because no signal will be sent to channel
			if tt.needRecvAdvices {
				result := <-recvCh

				assert.ElementsMatch(t, tt.wantAdviceResult.ExtraEntries, result.ExtraEntries)
				assert.ElementsMatch(t, tt.wantAdviceResult.ContainerEntries, result.ContainerEntries)
			}
			headroom, err := advisor.GetHeadroom()

			if reflect.DeepEqual(tt.wantHeadroom, resource.Quantity{}) {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				if !reflect.DeepEqual(tt.wantHeadroom.MilliValue(), headroom.MilliValue()) {
					t.Errorf("headroom\nexpected: %+v\nactual: %+v", tt.wantHeadroom, headroom)
				}
			}

			cancel()
		})
	}
}

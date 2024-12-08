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
	"k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"
	"k8s.io/utils/pointer"

	"github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/options"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/memory/dynamicpolicy/memoryadvisor"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	memadvisorplugin "github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/qosaware/resource/memory/plugin"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/tmo"
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

var qosLevel2PoolName = map[string]string{
	consts.PodAnnotationQoSLevelSharedCores:    commonstate.PoolNameShare,
	consts.PodAnnotationQoSLevelReclaimedCores: commonstate.PoolNameReclaim,
	consts.PodAnnotationQoSLevelSystemCores:    commonstate.PoolNameReserve,
	consts.PodAnnotationQoSLevelDedicatedCores: commonstate.PoolNameDedicated,
}

func makeContainerInfo(podUID, namespace, podName, containerName, qoSLevel string, annotations map[string]string,
	topologyAwareAssignments types.TopologyAwareAssignment, memoryRequest float64,
) *types.ContainerInfo {
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
	conf.GraceBalanceReadLatencyThreshold = 100
	conf.ForceBalanceReadLatencyThreshold = 120
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

	cpuTopology, err := machine.GenerateDummyCPUTopology(96, 2, 4)
	require.NoError(t, err)
	memoryTopology, err := machine.GenerateDummyMemoryTopology(4, 500<<30)
	require.NoError(t, err)

	extraTopology, err := machine.GenerateDummyExtraTopology(4)
	require.NoError(t, err)

	metaServer := &metaserver.MetaServer{
		MetaAgent: &agent.MetaAgent{
			KatalystMachineInfo: &machine.KatalystMachineInfo{
				MachineInfo: &info.MachineInfo{
					MemoryCapacity: 1000 << 30,
				},
				CPUTopology:       cpuTopology,
				MemoryTopology:    memoryTopology,
				ExtraTopologyInfo: extraTopology,
			},
			PodFetcher: &pod.PodFetcherStub{
				PodList: pods,
			},
			MetricsFetcher: fetcher,
		},
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
	numaID        int
}

type cgroupMetric struct {
	metricName  string
	metricValue metricutil.MetricData
	cgroupPath  string
}

type cgroupNUMAMetric struct {
	metricName  string
	metricValue metricutil.MetricData
	numaID      int
	cgroupPath  string
}

var defaultPodList = []*v1.Pod{
	{
		ObjectMeta: metav1.ObjectMeta{
			UID:       "uid1",
			Name:      "pod1",
			Namespace: "default",
		},
		Spec: v1.PodSpec{
			Containers: []v1.Container{
				{
					Name: "c1",
				},
			},
		},
	},
	{
		ObjectMeta: metav1.ObjectMeta{
			UID:       "uid2",
			Name:      "pod2",
			Namespace: "default",
		},
		Spec: v1.PodSpec{
			Containers: []v1.Container{
				{
					Name: "c2",
				},
			},
		},
	},
	{
		ObjectMeta: metav1.ObjectMeta{
			UID:       "uid3",
			Name:      "pod3",
			Namespace: "default",
		},
		Spec: v1.PodSpec{
			Containers: []v1.Container{
				{
					Name: "c3",
				},
			},
		},
	},
	{
		ObjectMeta: metav1.ObjectMeta{
			UID:       "uid4",
			Name:      "pod4",
			Namespace: "default",
		},
		Spec: v1.PodSpec{
			Containers: []v1.Container{
				{
					Name: "c4",
				},
			},
		},
	},
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
	{
		numaID:      0,
		metricName:  coreconsts.MetricMemInactiveFileNuma,
		metricValue: metricutil.MetricData{Value: 10 << 30},
	},
	{
		numaID:      1,
		metricName:  coreconsts.MetricMemInactiveFileNuma,
		metricValue: metricutil.MetricData{Value: 10 << 30},
	},
	{
		numaID:      2,
		metricName:  coreconsts.MetricMemInactiveFileNuma,
		metricValue: metricutil.MetricData{Value: 10 << 30},
	},
	{
		numaID:      3,
		metricName:  coreconsts.MetricMemInactiveFileNuma,
		metricValue: metricutil.MetricData{Value: 10 << 30},
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
	{
		numaID:      0,
		metricName:  coreconsts.MetricMemInactiveFileNuma,
		metricValue: metricutil.MetricData{Value: 10 << 30},
	},
	{
		numaID:      1,
		metricName:  coreconsts.MetricMemInactiveFileNuma,
		metricValue: metricutil.MetricData{Value: 10 << 30},
	},
	{
		numaID:      2,
		metricName:  coreconsts.MetricMemInactiveFileNuma,
		metricValue: metricutil.MetricData{Value: 10 << 30},
	},
	{
		numaID:      3,
		metricName:  coreconsts.MetricMemInactiveFileNuma,
		metricValue: metricutil.MetricData{Value: 10 << 30},
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

var cgroupNUMAMetrics = []cgroupNUMAMetric{
	{
		metricName:  coreconsts.MetricsMemTotalPerNumaCgroup,
		numaID:      0,
		cgroupPath:  "/kubepods/besteffort",
		metricValue: metricutil.MetricData{Value: 6 << 30},
	},
	{
		metricName:  coreconsts.MetricsMemTotalPerNumaCgroup,
		numaID:      1,
		cgroupPath:  "/kubepods/besteffort",
		metricValue: metricutil.MetricData{Value: 6 << 30},
	},
	{
		metricName:  coreconsts.MetricsMemTotalPerNumaCgroup,
		numaID:      2,
		cgroupPath:  "/kubepods/besteffort",
		metricValue: metricutil.MetricData{Value: 6 << 30},
	},
	{
		metricName:  coreconsts.MetricsMemTotalPerNumaCgroup,
		numaID:      3,
		cgroupPath:  "/kubepods/besteffort",
		metricValue: metricutil.MetricData{Value: 6 << 30},
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
		cgroupNUMAMetrics    []cgroupNUMAMetric
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
				commonstate.PoolNameReserve: {
					PoolName: commonstate.PoolNameReserve,
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
				commonstate.PoolNameReserve: {
					PoolName: commonstate.PoolNameReserve,
					TopologyAwareAssignments: map[int]machine.CPUSet{
						0: machine.MustParse("0"),
						1: machine.MustParse("24"),
					},
					OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
						0: machine.MustParse("0"),
						1: machine.MustParse("24"),
					},
				},
				commonstate.PoolNameShare: {
					PoolName: commonstate.PoolNameShare,
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
				commonstate.PoolNameReserve: {
					PoolName: commonstate.PoolNameReserve,
					TopologyAwareAssignments: map[int]machine.CPUSet{
						0: machine.MustParse("0"),
						1: machine.MustParse("24"),
					},
					OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
						0: machine.MustParse("0"),
						1: machine.MustParse("24"),
					},
				},
				commonstate.PoolNameShare: {
					PoolName: commonstate.PoolNameShare,
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
				commonstate.PoolNameReserve: {
					PoolName: commonstate.PoolNameReserve,
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
			containerNUMAMetrics: []containerNUMAMetric{
				{
					metricName:    coreconsts.MetricsMemTotalPerNumaContainer,
					metricValue:   metricutil.MetricData{Value: 50 << 30},
					podUID:        "uid1",
					containerName: "c1",
					numaID:        0,
				},
				{
					metricName:    coreconsts.MetricsMemTotalPerNumaContainer,
					metricValue:   metricutil.MetricData{Value: 50 << 30},
					podUID:        "uid1",
					containerName: "c1",
					numaID:        1,
				},
				{
					metricName:    coreconsts.MetricsMemTotalPerNumaContainer,
					metricValue:   metricutil.MetricData{Value: 50 << 30},
					podUID:        "uid1",
					containerName: "c1",
					numaID:        2,
				},
				{
					metricName:    coreconsts.MetricsMemTotalPerNumaContainer,
					metricValue:   metricutil.MetricData{Value: 50 << 30},
					podUID:        "uid2",
					containerName: "c2",
					numaID:        0,
				},
				{
					metricName:    coreconsts.MetricsMemTotalPerNumaContainer,
					metricValue:   metricutil.MetricData{Value: 50 << 30},
					podUID:        "uid2",
					containerName: "c2",
					numaID:        1,
				},
				{
					metricName:    coreconsts.MetricsMemTotalPerNumaContainer,
					metricValue:   metricutil.MetricData{Value: 50 << 30},
					podUID:        "uid2",
					containerName: "c2",
					numaID:        2,
				},
				{
					metricName:    coreconsts.MetricsMemTotalPerNumaContainer,
					metricValue:   metricutil.MetricData{Value: 50 << 30},
					podUID:        "uid2",
					containerName: "c2",
					numaID:        3,
				},
				{
					metricName:    coreconsts.MetricsMemTotalPerNumaContainer,
					metricValue:   metricutil.MetricData{Value: 50 << 30},
					podUID:        "uid1",
					containerName: "c1",
					numaID:        3,
				},
				{
					metricName:    coreconsts.MetricsMemTotalPerNumaContainer,
					metricValue:   metricutil.MetricData{Value: 50 << 30},
					podUID:        "uid3",
					containerName: "c3",
					numaID:        0,
				},
				{
					metricName:    coreconsts.MetricsMemTotalPerNumaContainer,
					metricValue:   metricutil.MetricData{Value: 50 << 30},
					podUID:        "uid3",
					containerName: "c3",
					numaID:        1,
				},
				{
					metricName:    coreconsts.MetricsMemTotalPerNumaContainer,
					metricValue:   metricutil.MetricData{Value: 50 << 30},
					podUID:        "uid3",
					containerName: "c3",
					numaID:        2,
				},
				{
					metricName:    coreconsts.MetricsMemTotalPerNumaContainer,
					metricValue:   metricutil.MetricData{Value: 50 << 30},
					podUID:        "uid3",
					containerName: "c3",
					numaID:        3,
				},

				{
					metricName:    coreconsts.MetricsMemTotalPerNumaContainer,
					metricValue:   metricutil.MetricData{Value: 50 << 30},
					podUID:        "uid4",
					containerName: "c4",
					numaID:        0,
				},
				{
					metricName:    coreconsts.MetricsMemTotalPerNumaContainer,
					metricValue:   metricutil.MetricData{Value: 50 << 30},
					podUID:        "uid4",
					containerName: "c4",
					numaID:        1,
				},
				{
					metricName:    coreconsts.MetricsMemTotalPerNumaContainer,
					metricValue:   metricutil.MetricData{Value: 50 << 30},
					podUID:        "uid4",
					containerName: "c4",
					numaID:        2,
				},
				{
					metricName:    coreconsts.MetricsMemTotalPerNumaContainer,
					metricValue:   metricutil.MetricData{Value: 50 << 30},
					podUID:        "uid4",
					containerName: "c4",
					numaID:        3,
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
				commonstate.PoolNameReserve: {
					PoolName: commonstate.PoolNameReserve,
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
					numaID:        0,
				},
				{
					metricName:    coreconsts.MetricsMemFilePerNumaContainer,
					metricValue:   metricutil.MetricData{Value: 9 << 30},
					podUID:        "uid2",
					containerName: "c2",
					numaID:        0,
				},
				{
					metricName:    coreconsts.MetricsMemFilePerNumaContainer,
					metricValue:   metricutil.MetricData{Value: 2 << 30},
					podUID:        "uid3",
					containerName: "c3",
					numaID:        0,
				},
				{
					metricName:    coreconsts.MetricsMemFilePerNumaContainer,
					metricValue:   metricutil.MetricData{Value: 10 << 20},
					podUID:        "uid1",
					containerName: "c1",
					numaID:        1,
				},
				{
					metricName:    coreconsts.MetricsMemFilePerNumaContainer,
					metricValue:   metricutil.MetricData{Value: 9 << 30},
					podUID:        "uid2",
					containerName: "c2",
					numaID:        1,
				},
				{
					metricName:    coreconsts.MetricsMemFilePerNumaContainer,
					metricValue:   metricutil.MetricData{Value: 2 << 30},
					podUID:        "uid3",
					containerName: "c3",
					numaID:        1,
				},
				{
					metricName:    coreconsts.MetricsMemTotalPerNumaContainer,
					metricValue:   metricutil.MetricData{Value: 50 << 30},
					podUID:        "uid1",
					containerName: "c1",
					numaID:        0,
				},
				{
					metricName:    coreconsts.MetricsMemTotalPerNumaContainer,
					metricValue:   metricutil.MetricData{Value: 50 << 30},
					podUID:        "uid1",
					containerName: "c1",
					numaID:        1,
				},
				{
					metricName:    coreconsts.MetricsMemTotalPerNumaContainer,
					metricValue:   metricutil.MetricData{Value: 50 << 30},
					podUID:        "uid2",
					containerName: "c2",
					numaID:        0,
				},
				{
					metricName:    coreconsts.MetricsMemTotalPerNumaContainer,
					metricValue:   metricutil.MetricData{Value: 50 << 30},
					podUID:        "uid2",
					containerName: "c2",
					numaID:        1,
				},
				{
					metricName:    coreconsts.MetricsMemTotalPerNumaContainer,
					metricValue:   metricutil.MetricData{Value: 50 << 30},
					podUID:        "uid3",
					containerName: "c3",
					numaID:        0,
				},
				{
					metricName:    coreconsts.MetricsMemTotalPerNumaContainer,
					metricValue:   metricutil.MetricData{Value: 50 << 30},
					podUID:        "uid3",
					containerName: "c3",
					numaID:        1,
				},
				{
					metricName:    coreconsts.MetricsMemTotalPerNumaContainer,
					metricValue:   metricutil.MetricData{Value: 50 << 30},
					podUID:        "uid2",
					containerName: "c2",
					numaID:        0,
				},
				{
					metricName:    coreconsts.MetricsMemTotalPerNumaContainer,
					metricValue:   metricutil.MetricData{Value: 50 << 30},
					podUID:        "uid2",
					containerName: "c2",
					numaID:        1,
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
			name: "set reclaimed group memory limit(succeeded)",
			pools: map[string]*types.PoolInfo{
				commonstate.PoolNameReserve: {
					PoolName: commonstate.PoolNameReserve,
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
			reclaimedEnable:   true,
			needRecvAdvices:   true,
			wantHeadroom:      *resource.NewQuantity(996<<30, resource.DecimalSI),
			nodeMetrics:       defaultNodeMetrics,
			numaMetrics:       defaultNumaMetrics,
			cgroupMetrics:     cgroupMetrics,
			cgroupNUMAMetrics: cgroupNUMAMetrics,
			plugins:           []types.MemoryAdvisorPluginName{memadvisorplugin.MemoryGuard},
			wantAdviceResult: types.InternalMemoryCalculationResult{
				ExtraEntries: []types.ExtraMemoryAdvices{
					{
						CgroupPath: "/kubepods/besteffort",
						Values:     map[string]string{string(memoryadvisor.ControlKnobKeyMemoryLimitInBytes): strconv.Itoa(240 << 30)},
					},
				},
			},
		},
		{
			name: "set reclaimed group memory limit(failed)",
			pools: map[string]*types.PoolInfo{
				commonstate.PoolNameReserve: {
					PoolName: commonstate.PoolNameReserve,
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
			plugins:         []types.MemoryAdvisorPluginName{memadvisorplugin.MemoryGuard},
			wantAdviceResult: types.InternalMemoryCalculationResult{
				ExtraEntries: []types.ExtraMemoryAdvices{},
			},
		},
		{
			name: "memory offloading",
			pools: map[string]*types.PoolInfo{
				commonstate.PoolNameReserve: {
					PoolName: commonstate.PoolNameReserve,
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
			containers: []*types.ContainerInfo{
				makeContainerInfo("uid1", "default", "pod1", "c1", consts.PodAnnotationQoSLevelReclaimedCores, nil,
					map[int]machine.CPUSet{
						0: machine.MustParse("1"),
						1: machine.MustParse("25"),
					}, 200<<30),
				makeContainerInfo("uid4", "default", "pod4", "c4", consts.PodAnnotationQoSLevelReclaimedCores, nil,
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
						Annotations: map[string]string{
							consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
						},
						Labels: map[string]string{
							consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
						},
					},
					Spec: v1.PodSpec{
						Containers: []v1.Container{
							{
								Name: "c1",
							},
						},
					},
					Status: v1.PodStatus{
						ContainerStatuses: []v1.ContainerStatus{
							{
								Name:        "c1",
								ContainerID: "containerd://c1",
							},
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "pod2",
						Namespace: "default",
						UID:       "uid2",
						Annotations: map[string]string{
							consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
						},
						Labels: map[string]string{
							consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
						},
					},
					Spec: v1.PodSpec{
						Containers: []v1.Container{
							{
								Name: "c2",
							},
						},
					},
					Status: v1.PodStatus{
						ContainerStatuses: []v1.ContainerStatus{
							{
								Name:        "c2",
								ContainerID: "containerd://c2",
							},
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "pod3",
						Namespace: "default",
						UID:       "uid3",
						Annotations: map[string]string{
							consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
						},
						Labels: map[string]string{
							consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
						},
					},
					Spec: v1.PodSpec{
						Containers: []v1.Container{
							{
								Name: "c3",
							},
						},
					},
					Status: v1.PodStatus{
						ContainerStatuses: []v1.ContainerStatus{
							{
								Name:        "c3",
								ContainerID: "containerd://c3",
							},
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "pod4",
						Namespace: "default",
						UID:       "uid4",
						Annotations: map[string]string{
							consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
						},
						Labels: map[string]string{
							consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
						},
					},
					Spec: v1.PodSpec{
						Containers: []v1.Container{
							{
								Name: "c4",
							},
						},
					},
					Status: v1.PodStatus{
						ContainerStatuses: []v1.ContainerStatus{
							{
								Name:        "c4",
								ContainerID: "containerd://c4",
							},
						},
					},
				},
			},
			wantHeadroom: *resource.NewQuantity(996<<30, resource.DecimalSI),
			nodeMetrics:  defaultNodeMetrics,
			numaMetrics:  defaultNumaMetrics,
			containerMetrics: []containerMetric{
				{
					metricName:    coreconsts.MetricMemPsiAvg60Container,
					metricValue:   metricutil.MetricData{Value: 0.01},
					podUID:        "uid1",
					containerName: "c1",
				},
				{
					metricName:    coreconsts.MetricMemUsageContainer,
					metricValue:   metricutil.MetricData{Value: 10 << 30},
					podUID:        "uid1",
					containerName: "c1",
				},
				{
					metricName:    coreconsts.MetricMemCacheContainer,
					metricValue:   metricutil.MetricData{Value: 5 << 30},
					podUID:        "uid1",
					containerName: "c1",
				},
				{
					metricName:    coreconsts.MetricMemMappedContainer,
					metricValue:   metricutil.MetricData{Value: 2 << 30},
					podUID:        "uid1",
					containerName: "c1",
				},
				{
					metricName:    coreconsts.MetricMemInactiveAnonContainer,
					metricValue:   metricutil.MetricData{Value: 1 << 30},
					podUID:        "uid1",
					containerName: "c1",
				},
				{
					metricName:    coreconsts.MetricMemInactiveFileContainer,
					metricValue:   metricutil.MetricData{Value: 1 << 30},
					podUID:        "uid1",
					containerName: "c1",
				},
				{
					metricName:    coreconsts.MetricMemPgscanContainer,
					metricValue:   metricutil.MetricData{Value: 15000},
					podUID:        "uid1",
					containerName: "c1",
				},
				{
					metricName:    coreconsts.MetricMemPgstealContainer,
					metricValue:   metricutil.MetricData{Value: 10000},
					podUID:        "uid1",
					containerName: "c1",
				},
				{
					metricName:    coreconsts.MetricMemWorkingsetRefaultContainer,
					metricValue:   metricutil.MetricData{Value: 1000},
					podUID:        "uid1",
					containerName: "c1",
				},
				{
					metricName:    coreconsts.MetricMemWorkingsetActivateContainer,
					metricValue:   metricutil.MetricData{Value: 1000},
					podUID:        "uid1",
					containerName: "c1",
				},
				{
					metricName:    coreconsts.MetricMemPsiAvg60Container,
					metricValue:   metricutil.MetricData{Value: 0.01},
					podUID:        "uid2",
					containerName: "c2",
				},
				{
					metricName:    coreconsts.MetricMemUsageContainer,
					metricValue:   metricutil.MetricData{Value: 10 << 30},
					podUID:        "uid2",
					containerName: "c2",
				},
				{
					metricName:    coreconsts.MetricMemCacheContainer,
					metricValue:   metricutil.MetricData{Value: 3 << 30},
					podUID:        "uid2",
					containerName: "c2",
				},
				{
					metricName:    coreconsts.MetricMemMappedContainer,
					metricValue:   metricutil.MetricData{Value: 2 << 30},
					podUID:        "uid2",
					containerName: "c2",
				},
				{
					metricName:    coreconsts.MetricMemInactiveAnonContainer,
					metricValue:   metricutil.MetricData{Value: 2 << 30},
					podUID:        "uid2",
					containerName: "c2",
				},
				{
					metricName:    coreconsts.MetricMemInactiveFileContainer,
					metricValue:   metricutil.MetricData{Value: 1 << 30},
					podUID:        "uid2",
					containerName: "c2",
				},
				{
					metricName:    coreconsts.MetricMemPgscanContainer,
					metricValue:   metricutil.MetricData{Value: 15000},
					podUID:        "uid2",
					containerName: "c2",
				},
				{
					metricName:    coreconsts.MetricMemPgstealContainer,
					metricValue:   metricutil.MetricData{Value: 10000},
					podUID:        "uid2",
					containerName: "c2",
				},
				{
					metricName:    coreconsts.MetricMemWorkingsetRefaultContainer,
					metricValue:   metricutil.MetricData{Value: 1000},
					podUID:        "uid2",
					containerName: "c2",
				},
				{
					metricName:    coreconsts.MetricMemWorkingsetActivateContainer,
					metricValue:   metricutil.MetricData{Value: 1000},
					podUID:        "uid2",
					containerName: "c2",
				},
				{
					metricName:    coreconsts.MetricMemPsiAvg60Container,
					metricValue:   metricutil.MetricData{Value: 0.01},
					podUID:        "uid3",
					containerName: "c3",
				},
				{
					metricName:    coreconsts.MetricMemUsageContainer,
					metricValue:   metricutil.MetricData{Value: 10 << 30},
					podUID:        "uid3",
					containerName: "c3",
				},
				{
					metricName:    coreconsts.MetricMemCacheContainer,
					metricValue:   metricutil.MetricData{Value: 3 << 30},
					podUID:        "uid3",
					containerName: "c3",
				},
				{
					metricName:    coreconsts.MetricMemMappedContainer,
					metricValue:   metricutil.MetricData{Value: 2 << 30},
					podUID:        "uid3",
					containerName: "c3",
				},
				{
					metricName:    coreconsts.MetricMemInactiveAnonContainer,
					metricValue:   metricutil.MetricData{Value: 2 << 30},
					podUID:        "uid3",
					containerName: "c3",
				},
				{
					metricName:    coreconsts.MetricMemInactiveFileContainer,
					metricValue:   metricutil.MetricData{Value: 1 << 30},
					podUID:        "uid3",
					containerName: "c3",
				},
				{
					metricName:    coreconsts.MetricMemPgscanContainer,
					metricValue:   metricutil.MetricData{Value: 15000},
					podUID:        "uid3",
					containerName: "c3",
				},
				{
					metricName:    coreconsts.MetricMemPgstealContainer,
					metricValue:   metricutil.MetricData{Value: 10000},
					podUID:        "uid3",
					containerName: "c3",
				},
				{
					metricName:    coreconsts.MetricMemWorkingsetRefaultContainer,
					metricValue:   metricutil.MetricData{Value: 1000},
					podUID:        "uid3",
					containerName: "c3",
				},
				{
					metricName:    coreconsts.MetricMemWorkingsetActivateContainer,
					metricValue:   metricutil.MetricData{Value: 1000},
					podUID:        "uid3",
					containerName: "c3",
				},
				{
					metricName:    coreconsts.MetricMemPsiAvg60Container,
					metricValue:   metricutil.MetricData{Value: 0.01},
					podUID:        "uid4",
					containerName: "c4",
				},
				{
					metricName:    coreconsts.MetricMemUsageContainer,
					metricValue:   metricutil.MetricData{Value: 10 << 30},
					podUID:        "uid4",
					containerName: "c4",
				},
				{
					metricName:    coreconsts.MetricMemCacheContainer,
					metricValue:   metricutil.MetricData{Value: 2 << 30},
					podUID:        "uid4",
					containerName: "c4",
				},
				{
					metricName:    coreconsts.MetricMemMappedContainer,
					metricValue:   metricutil.MetricData{Value: 2 << 30},
					podUID:        "uid4",
					containerName: "c4",
				},
				{
					metricName:    coreconsts.MetricMemInactiveAnonContainer,
					metricValue:   metricutil.MetricData{Value: 1 << 30},
					podUID:        "uid4",
					containerName: "c4",
				},
				{
					metricName:    coreconsts.MetricMemInactiveFileContainer,
					metricValue:   metricutil.MetricData{Value: 1 << 30},
					podUID:        "uid4",
					containerName: "c4",
				},
				{
					metricName:    coreconsts.MetricMemPgscanContainer,
					metricValue:   metricutil.MetricData{Value: 15000},
					podUID:        "uid4",
					containerName: "c4",
				},
				{
					metricName:    coreconsts.MetricMemPgstealContainer,
					metricValue:   metricutil.MetricData{Value: 10000},
					podUID:        "uid4",
					containerName: "c4",
				},
				{
					metricName:    coreconsts.MetricMemWorkingsetRefaultContainer,
					metricValue:   metricutil.MetricData{Value: 1000},
					podUID:        "uid4",
					containerName: "c4",
				},
				{
					metricName:    coreconsts.MetricMemWorkingsetActivateContainer,
					metricValue:   metricutil.MetricData{Value: 1000},
					podUID:        "uid4",
					containerName: "c4",
				},
			},
			containerNUMAMetrics: []containerNUMAMetric{
				{
					metricName:    coreconsts.MetricsMemTotalPerNumaContainer,
					metricValue:   metricutil.MetricData{Value: 50 << 30},
					podUID:        "uid1",
					containerName: "c1",
					numaID:        0,
				},
				{
					metricName:    coreconsts.MetricsMemTotalPerNumaContainer,
					metricValue:   metricutil.MetricData{Value: 50 << 30},
					podUID:        "uid1",
					containerName: "c1",
					numaID:        1,
				},
				{
					metricName:    coreconsts.MetricsMemTotalPerNumaContainer,
					metricValue:   metricutil.MetricData{Value: 50 << 30},
					podUID:        "uid1",
					containerName: "c1",
					numaID:        2,
				},
				{
					metricName:    coreconsts.MetricsMemTotalPerNumaContainer,
					metricValue:   metricutil.MetricData{Value: 50 << 30},
					podUID:        "uid1",
					containerName: "c1",
					numaID:        3,
				},

				{
					metricName:    coreconsts.MetricsMemTotalPerNumaContainer,
					metricValue:   metricutil.MetricData{Value: 50 << 30},
					podUID:        "uid4",
					containerName: "c4",
					numaID:        0,
				},
				{
					metricName:    coreconsts.MetricsMemTotalPerNumaContainer,
					metricValue:   metricutil.MetricData{Value: 50 << 30},
					podUID:        "uid4",
					containerName: "c4",
					numaID:        1,
				},
				{
					metricName:    coreconsts.MetricsMemTotalPerNumaContainer,
					metricValue:   metricutil.MetricData{Value: 50 << 30},
					podUID:        "uid4",
					containerName: "c4",
					numaID:        2,
				},
				{
					metricName:    coreconsts.MetricsMemTotalPerNumaContainer,
					metricValue:   metricutil.MetricData{Value: 50 << 30},
					podUID:        "uid4",
					containerName: "c4",
					numaID:        3,
				},
			},

			cgroupMetrics: []cgroupMetric{
				{
					metricName:  coreconsts.MetricMemPsiAvg60Cgroup,
					metricValue: metricutil.MetricData{Value: 0.01},
					cgroupPath:  "/hdfs",
				},
				{
					metricName:  coreconsts.MetricMemPgstealCgroup,
					metricValue: metricutil.MetricData{Value: 0.01},
					cgroupPath:  "/hdfs",
				},
				{
					metricName:  coreconsts.MetricMemPgscanCgroup,
					metricValue: metricutil.MetricData{Value: 0.01},
					cgroupPath:  "/hdfs",
				},
				{
					metricName:  coreconsts.MetricMemWorkingsetRefaultCgroup,
					metricValue: metricutil.MetricData{Value: 0.01},
					cgroupPath:  "/hdfs",
				},
				{
					metricName:  coreconsts.MetricMemWorkingsetActivateCgroup,
					metricValue: metricutil.MetricData{Value: 1 << 30},
					cgroupPath:  "/hdfs",
				},
				{
					metricName:  coreconsts.MetricMemUsageCgroup,
					metricValue: metricutil.MetricData{Value: 4 << 30},
					cgroupPath:  "/hdfs",
				},
				{
					metricName:  coreconsts.MetricMemCacheCgroup,
					metricValue: metricutil.MetricData{Value: 3 << 30},
					cgroupPath:  "/hdfs",
				},
				{
					metricName:  coreconsts.MetricMemMappedCgroup,
					metricValue: metricutil.MetricData{Value: 1 << 30},
					cgroupPath:  "/hdfs",
				},
				{
					metricName:  coreconsts.MetricMemInactiveAnonCgroup,
					metricValue: metricutil.MetricData{Value: 1 << 30},
					cgroupPath:  "/hdfs",
				},
				{
					metricName:  coreconsts.MetricMemInactiveFileCgroup,
					metricValue: metricutil.MetricData{Value: 1 << 30},
					cgroupPath:  "/hdfs",
				},
			},
			plugins: []types.MemoryAdvisorPluginName{memadvisorplugin.TransparentMemoryOffloading},
			wantAdviceResult: types.InternalMemoryCalculationResult{
				ExtraEntries: []types.ExtraMemoryAdvices{
					{
						CgroupPath: "/hdfs",
						Values: map[string]string{
							string(memoryadvisor.ControlKnobKeySwapMax):          coreconsts.ControlKnobOFF,
							string(memoryadvisor.ControlKnowKeyMemoryOffloading): "38654705",
						},
					},
				},
				ContainerEntries: []types.ContainerMemoryAdvices{
					{
						PodUID:        "uid1",
						ContainerName: "c1",
						Values: map[string]string{
							string(memoryadvisor.ControlKnobKeySwapMax):          coreconsts.ControlKnobOFF,
							string(memoryadvisor.ControlKnowKeyMemoryOffloading): "96636764",
						},
					},
					{
						PodUID:        "uid3",
						ContainerName: "c3",
						Values: map[string]string{
							string(memoryadvisor.ControlKnobKeySwapMax):          coreconsts.ControlKnobON,
							string(memoryadvisor.ControlKnowKeyMemoryOffloading): "96636764",
						},
					},
					//{
					//	PodUID:        "uid4",
					//	ContainerName: "c4",
					//	Values: map[string]string{
					//		string(memoryadvisor.ControlKnobKeySwapMax):          coreconsts.ControlKnobOFF,
					//		string(memoryadvisor.ControlKnowKeyMemoryOffloading): "96636764",
					//	},
					//},
				},
			},
		},
		{
			name: "bind memset",
			pools: map[string]*types.PoolInfo{
				commonstate.PoolNameReserve: {
					PoolName: commonstate.PoolNameReserve,
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
			pods: []*v1.Pod{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "pod1",
						Namespace: "default",
						UID:       "uid1",
						Annotations: map[string]string{
							consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
						},
						Labels: map[string]string{
							consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
						},
					},
					Spec: v1.PodSpec{
						Containers: []v1.Container{
							{
								Name: "c1",
							},
						},
					},
					Status: v1.PodStatus{
						ContainerStatuses: []v1.ContainerStatus{
							{
								Name:        "c1",
								ContainerID: "containerd://c1",
							},
						},
						Phase: v1.PodRunning,
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "pod2",
						Namespace: "default",
						UID:       "uid2",
						Annotations: map[string]string{
							consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
						},
						Labels: map[string]string{
							consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
						},
					},
					Spec: v1.PodSpec{
						Containers: []v1.Container{
							{
								Name: "c2",
							},
						},
					},
					Status: v1.PodStatus{
						ContainerStatuses: []v1.ContainerStatus{
							{
								Name:        "c2",
								ContainerID: "containerd://c2",
							},
						},
						Phase: v1.PodRunning,
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "pod3",
						Namespace: "default",
						UID:       "uid3",
						Annotations: map[string]string{
							consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
						},
						Labels: map[string]string{
							consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
						},
					},
					Spec: v1.PodSpec{
						Containers: []v1.Container{
							{
								Name: "c3",
							},
						},
					},
					Status: v1.PodStatus{
						ContainerStatuses: []v1.ContainerStatus{
							{
								Name:        "c3",
								ContainerID: "containerd://c3",
							},
						},
						Phase: v1.PodRunning,
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "pod4",
						Namespace: "default",
						UID:       "uid4",
						Annotations: map[string]string{
							consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
						},
						Labels: map[string]string{
							consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
						},
					},
					Spec: v1.PodSpec{
						Containers: []v1.Container{
							{
								Name: "c4",
							},
						},
					},
					Status: v1.PodStatus{
						ContainerStatuses: []v1.ContainerStatus{
							{
								Name:        "c4",
								ContainerID: "containerd://c4",
							},
						},
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
					consts.PodAnnotationMemoryEnhancementNumaExclusive: consts.PodAnnotationMemoryEnhancementNumaExclusiveEnable,
				},
					map[int]machine.CPUSet{
						0: machine.MustParse("1"),
					}, 200<<30),
			},
			plugins:     []types.MemoryAdvisorPluginName{memadvisorplugin.MemsetBinder},
			nodeMetrics: defaultNodeMetrics,
			numaMetrics: defaultNumaMetrics,
			containerNUMAMetrics: []containerNUMAMetric{
				{
					metricName:    coreconsts.MetricsMemFilePerNumaContainer,
					metricValue:   metricutil.MetricData{Value: 10 << 20},
					podUID:        "uid1",
					containerName: "c1",
					numaID:        0,
				},
				{
					metricName:    coreconsts.MetricsMemFilePerNumaContainer,
					metricValue:   metricutil.MetricData{Value: 9 << 30},
					podUID:        "uid2",
					containerName: "c2",
					numaID:        0,
				},
				{
					metricName:    coreconsts.MetricsMemFilePerNumaContainer,
					metricValue:   metricutil.MetricData{Value: 2 << 30},
					podUID:        "uid3",
					containerName: "c3",
					numaID:        0,
				},
				{
					metricName:    coreconsts.MetricsMemFilePerNumaContainer,
					metricValue:   metricutil.MetricData{Value: 10 << 20},
					podUID:        "uid1",
					containerName: "c1",
					numaID:        1,
				},
				{
					metricName:    coreconsts.MetricsMemFilePerNumaContainer,
					metricValue:   metricutil.MetricData{Value: 9 << 30},
					podUID:        "uid2",
					containerName: "c2",
					numaID:        1,
				},
				{
					metricName:    coreconsts.MetricsMemFilePerNumaContainer,
					metricValue:   metricutil.MetricData{Value: 2 << 30},
					podUID:        "uid3",
					containerName: "c3",
					numaID:        1,
				},
				{
					metricName:    coreconsts.MetricsMemTotalPerNumaContainer,
					metricValue:   metricutil.MetricData{Value: 50 << 30},
					podUID:        "uid1",
					containerName: "c1",
					numaID:        0,
				},
				{
					metricName:    coreconsts.MetricsMemTotalPerNumaContainer,
					metricValue:   metricutil.MetricData{Value: 50 << 30},
					podUID:        "uid1",
					containerName: "c1",
					numaID:        1,
				},
				{
					metricName:    coreconsts.MetricsMemTotalPerNumaContainer,
					metricValue:   metricutil.MetricData{Value: 50 << 30},
					podUID:        "uid2",
					containerName: "c2",
					numaID:        0,
				},
				{
					metricName:    coreconsts.MetricsMemTotalPerNumaContainer,
					metricValue:   metricutil.MetricData{Value: 50 << 30},
					podUID:        "uid2",
					containerName: "c2",
					numaID:        1,
				},
				{
					metricName:    coreconsts.MetricsMemTotalPerNumaContainer,
					metricValue:   metricutil.MetricData{Value: 50 << 30},
					podUID:        "uid3",
					containerName: "c3",
					numaID:        0,
				},
				{
					metricName:    coreconsts.MetricsMemTotalPerNumaContainer,
					metricValue:   metricutil.MetricData{Value: 50 << 30},
					podUID:        "uid3",
					containerName: "c3",
					numaID:        1,
				},
				{
					metricName:    coreconsts.MetricsMemTotalPerNumaContainer,
					metricValue:   metricutil.MetricData{Value: 50 << 30},
					podUID:        "uid2",
					containerName: "c2",
					numaID:        0,
				},
				{
					metricName:    coreconsts.MetricsMemTotalPerNumaContainer,
					metricValue:   metricutil.MetricData{Value: 50 << 30},
					podUID:        "uid2",
					containerName: "c2",
					numaID:        1,
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
		{
			name: "bind memset(numa1 pressure)",
			pools: map[string]*types.PoolInfo{
				commonstate.PoolNameReserve: {
					PoolName: commonstate.PoolNameReserve,
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
			pods: []*v1.Pod{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "pod1",
						Namespace: "default",
						UID:       "uid1",
						Annotations: map[string]string{
							consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
						},
						Labels: map[string]string{
							consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
						},
					},
					Spec: v1.PodSpec{
						Containers: []v1.Container{
							{
								Name: "c1",
							},
						},
					},
					Status: v1.PodStatus{
						ContainerStatuses: []v1.ContainerStatus{
							{
								Name:        "c1",
								ContainerID: "containerd://c1",
							},
						},
						Phase: v1.PodRunning,
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "pod2",
						Namespace: "default",
						UID:       "uid2",
						Annotations: map[string]string{
							consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
						},
						Labels: map[string]string{
							consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
						},
					},
					Spec: v1.PodSpec{
						Containers: []v1.Container{
							{
								Name: "c2",
							},
						},
					},
					Status: v1.PodStatus{
						ContainerStatuses: []v1.ContainerStatus{
							{
								Name:        "c2",
								ContainerID: "containerd://c2",
							},
						},
						Phase: v1.PodRunning,
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "pod3",
						Namespace: "default",
						UID:       "uid3",
						Annotations: map[string]string{
							consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
						},
						Labels: map[string]string{
							consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
						},
					},
					Spec: v1.PodSpec{
						Containers: []v1.Container{
							{
								Name: "c3",
							},
						},
					},
					Status: v1.PodStatus{
						ContainerStatuses: []v1.ContainerStatus{
							{
								Name:        "c3",
								ContainerID: "containerd://c3",
							},
						},
						Phase: v1.PodRunning,
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "pod4",
						Namespace: "default",
						UID:       "uid4",
						Annotations: map[string]string{
							consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
						},
						Labels: map[string]string{
							consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
						},
					},
					Spec: v1.PodSpec{
						Containers: []v1.Container{
							{
								Name: "c4",
							},
						},
					},
					Status: v1.PodStatus{
						ContainerStatuses: []v1.ContainerStatus{
							{
								Name:        "c4",
								ContainerID: "containerd://c4",
							},
						},
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
					consts.PodAnnotationMemoryEnhancementNumaExclusive: consts.PodAnnotationMemoryEnhancementNumaExclusiveEnable,
				},
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
				{
					numaID:      0,
					metricName:  coreconsts.MetricMemInactiveFileNuma,
					metricValue: metricutil.MetricData{Value: 10 << 30},
				},
				{
					numaID:      1,
					metricName:  coreconsts.MetricMemInactiveFileNuma,
					metricValue: metricutil.MetricData{Value: 10 << 30},
				},
				{
					numaID:      2,
					metricName:  coreconsts.MetricMemInactiveFileNuma,
					metricValue: metricutil.MetricData{Value: 10 << 30},
				},
				{
					numaID:      3,
					metricName:  coreconsts.MetricMemInactiveFileNuma,
					metricValue: metricutil.MetricData{Value: 10 << 30},
				},
			},
			containerNUMAMetrics: []containerNUMAMetric{
				{
					metricName:    coreconsts.MetricsMemTotalPerNumaContainer,
					metricValue:   metricutil.MetricData{Value: 50 << 30},
					podUID:        "uid1",
					containerName: "c1",
					numaID:        0,
				},
				{
					metricName:    coreconsts.MetricsMemTotalPerNumaContainer,
					metricValue:   metricutil.MetricData{Value: 50 << 30},
					podUID:        "uid1",
					containerName: "c1",
					numaID:        1,
				},
				{
					metricName:    coreconsts.MetricsMemTotalPerNumaContainer,
					metricValue:   metricutil.MetricData{Value: 50 << 30},
					podUID:        "uid1",
					containerName: "c1",
					numaID:        2,
				},
				{
					metricName:    coreconsts.MetricsMemTotalPerNumaContainer,
					metricValue:   metricutil.MetricData{Value: 50 << 30},
					podUID:        "uid1",
					containerName: "c1",
					numaID:        3,
				},
				{
					metricName:    coreconsts.MetricsMemTotalPerNumaContainer,
					metricValue:   metricutil.MetricData{Value: 50 << 30},
					podUID:        "uid2",
					containerName: "c2",
					numaID:        0,
				},
				{
					metricName:    coreconsts.MetricsMemTotalPerNumaContainer,
					metricValue:   metricutil.MetricData{Value: 50 << 30},
					podUID:        "uid2",
					containerName: "c2",
					numaID:        1,
				},
				{
					metricName:    coreconsts.MetricsMemTotalPerNumaContainer,
					metricValue:   metricutil.MetricData{Value: 50 << 30},
					podUID:        "uid2",
					containerName: "c2",
					numaID:        2,
				},
				{
					metricName:    coreconsts.MetricsMemTotalPerNumaContainer,
					metricValue:   metricutil.MetricData{Value: 50 << 30},
					podUID:        "uid2",
					containerName: "c2",
					numaID:        3,
				},
				{
					metricName:    coreconsts.MetricsMemTotalPerNumaContainer,
					metricValue:   metricutil.MetricData{Value: 50 << 30},
					podUID:        "uid3",
					containerName: "c3",
					numaID:        0,
				},
				{
					metricName:    coreconsts.MetricsMemTotalPerNumaContainer,
					metricValue:   metricutil.MetricData{Value: 50 << 30},
					podUID:        "uid3",
					containerName: "c3",
					numaID:        1,
				},
				{
					metricName:    coreconsts.MetricsMemTotalPerNumaContainer,
					metricValue:   metricutil.MetricData{Value: 50 << 30},
					podUID:        "uid3",
					containerName: "c3",
					numaID:        2,
				},
				{
					metricName:    coreconsts.MetricsMemTotalPerNumaContainer,
					metricValue:   metricutil.MetricData{Value: 50 << 30},
					podUID:        "uid3",
					containerName: "c3",
					numaID:        3,
				},

				{
					metricName:    coreconsts.MetricsMemTotalPerNumaContainer,
					metricValue:   metricutil.MetricData{Value: 50 << 30},
					podUID:        "uid4",
					containerName: "c4",
					numaID:        0,
				},
				{
					metricName:    coreconsts.MetricsMemTotalPerNumaContainer,
					metricValue:   metricutil.MetricData{Value: 50 << 30},
					podUID:        "uid4",
					containerName: "c4",
					numaID:        1,
				},
				{
					metricName:    coreconsts.MetricsMemTotalPerNumaContainer,
					metricValue:   metricutil.MetricData{Value: 50 << 30},
					podUID:        "uid4",
					containerName: "c4",
					numaID:        2,
				},
				{
					metricName:    coreconsts.MetricsMemTotalPerNumaContainer,
					metricValue:   metricutil.MetricData{Value: 50 << 30},
					podUID:        "uid4",
					containerName: "c4",
					numaID:        3,
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
				commonstate.PoolNameReserve: {
					PoolName: commonstate.PoolNameReserve,
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
			pods: []*v1.Pod{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "pod1",
						Namespace: "default",
						UID:       "uid1",
						Annotations: map[string]string{
							consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
						},
						Labels: map[string]string{
							consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
						},
					},
					Spec: v1.PodSpec{
						Containers: []v1.Container{
							{
								Name: "c1",
							},
						},
					},
					Status: v1.PodStatus{
						ContainerStatuses: []v1.ContainerStatus{
							{
								Name:        "c1",
								ContainerID: "containerd://c1",
							},
						},
						Phase: v1.PodRunning,
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "pod2",
						Namespace: "default",
						UID:       "uid2",
						Annotations: map[string]string{
							consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
						},
						Labels: map[string]string{
							consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
						},
					},
					Spec: v1.PodSpec{
						Containers: []v1.Container{
							{
								Name: "c2",
							},
						},
					},
					Status: v1.PodStatus{
						ContainerStatuses: []v1.ContainerStatus{
							{
								Name:        "c2",
								ContainerID: "containerd://c2",
							},
						},
						Phase: v1.PodRunning,
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "pod3",
						Namespace: "default",
						UID:       "uid3",
						Annotations: map[string]string{
							consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
						},
						Labels: map[string]string{
							consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
						},
					},
					Spec: v1.PodSpec{
						Containers: []v1.Container{
							{
								Name: "c3",
							},
						},
					},
					Status: v1.PodStatus{
						ContainerStatuses: []v1.ContainerStatus{
							{
								Name:        "c3",
								ContainerID: "containerd://c3",
							},
						},
						Phase: v1.PodRunning,
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "pod4",
						Namespace: "default",
						UID:       "uid4",
						Annotations: map[string]string{
							consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
						},
						Labels: map[string]string{
							consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
						},
					},
					Spec: v1.PodSpec{
						Containers: []v1.Container{
							{
								Name: "c4",
							},
						},
					},
					Status: v1.PodStatus{
						ContainerStatuses: []v1.ContainerStatus{
							{
								Name:        "c4",
								ContainerID: "containerd://c4",
							},
						},
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
					consts.PodAnnotationMemoryEnhancementNumaExclusive: consts.PodAnnotationMemoryEnhancementNumaExclusiveEnable,
				},
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
				{
					numaID:      0,
					metricName:  coreconsts.MetricMemInactiveFileNuma,
					metricValue: metricutil.MetricData{Value: 10 << 30},
				},
				{
					numaID:      1,
					metricName:  coreconsts.MetricMemInactiveFileNuma,
					metricValue: metricutil.MetricData{Value: 10 << 30},
				},
				{
					numaID:      2,
					metricName:  coreconsts.MetricMemInactiveFileNuma,
					metricValue: metricutil.MetricData{Value: 10 << 30},
				},
				{
					numaID:      3,
					metricName:  coreconsts.MetricMemInactiveFileNuma,
					metricValue: metricutil.MetricData{Value: 10 << 30},
				},
			},
			containerNUMAMetrics: []containerNUMAMetric{
				{
					metricName:    coreconsts.MetricsMemTotalPerNumaContainer,
					metricValue:   metricutil.MetricData{Value: 50 << 30},
					podUID:        "uid1",
					containerName: "c1",
					numaID:        0,
				},
				{
					metricName:    coreconsts.MetricsMemTotalPerNumaContainer,
					metricValue:   metricutil.MetricData{Value: 50 << 30},
					podUID:        "uid1",
					containerName: "c1",
					numaID:        1,
				},
				{
					metricName:    coreconsts.MetricsMemTotalPerNumaContainer,
					metricValue:   metricutil.MetricData{Value: 50 << 30},
					podUID:        "uid1",
					containerName: "c1",
					numaID:        2,
				},
				{
					metricName:    coreconsts.MetricsMemTotalPerNumaContainer,
					metricValue:   metricutil.MetricData{Value: 50 << 30},
					podUID:        "uid2",
					containerName: "c2",
					numaID:        0,
				},
				{
					metricName:    coreconsts.MetricsMemTotalPerNumaContainer,
					metricValue:   metricutil.MetricData{Value: 50 << 30},
					podUID:        "uid2",
					containerName: "c2",
					numaID:        1,
				},
				{
					metricName:    coreconsts.MetricsMemTotalPerNumaContainer,
					metricValue:   metricutil.MetricData{Value: 50 << 30},
					podUID:        "uid2",
					containerName: "c2",
					numaID:        2,
				},
				{
					metricName:    coreconsts.MetricsMemTotalPerNumaContainer,
					metricValue:   metricutil.MetricData{Value: 50 << 30},
					podUID:        "uid2",
					containerName: "c2",
					numaID:        3,
				},
				{
					metricName:    coreconsts.MetricsMemTotalPerNumaContainer,
					metricValue:   metricutil.MetricData{Value: 50 << 30},
					podUID:        "uid1",
					containerName: "c1",
					numaID:        3,
				},
				{
					metricName:    coreconsts.MetricsMemTotalPerNumaContainer,
					metricValue:   metricutil.MetricData{Value: 50 << 30},
					podUID:        "uid3",
					containerName: "c3",
					numaID:        0,
				},
				{
					metricName:    coreconsts.MetricsMemTotalPerNumaContainer,
					metricValue:   metricutil.MetricData{Value: 50 << 30},
					podUID:        "uid3",
					containerName: "c3",
					numaID:        1,
				},
				{
					metricName:    coreconsts.MetricsMemTotalPerNumaContainer,
					metricValue:   metricutil.MetricData{Value: 50 << 30},
					podUID:        "uid3",
					containerName: "c3",
					numaID:        2,
				},
				{
					metricName:    coreconsts.MetricsMemTotalPerNumaContainer,
					metricValue:   metricutil.MetricData{Value: 50 << 30},
					podUID:        "uid3",
					containerName: "c3",
					numaID:        3,
				},

				{
					metricName:    coreconsts.MetricsMemTotalPerNumaContainer,
					metricValue:   metricutil.MetricData{Value: 50 << 30},
					podUID:        "uid4",
					containerName: "c4",
					numaID:        0,
				},
				{
					metricName:    coreconsts.MetricsMemTotalPerNumaContainer,
					metricValue:   metricutil.MetricData{Value: 50 << 30},
					podUID:        "uid4",
					containerName: "c4",
					numaID:        1,
				},
				{
					metricName:    coreconsts.MetricsMemTotalPerNumaContainer,
					metricValue:   metricutil.MetricData{Value: 50 << 30},
					podUID:        "uid4",
					containerName: "c4",
					numaID:        2,
				},
				{
					metricName:    coreconsts.MetricsMemTotalPerNumaContainer,
					metricValue:   metricutil.MetricData{Value: 50 << 30},
					podUID:        "uid4",
					containerName: "c4",
					numaID:        3,
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
		{
			name: "numa memory balance(grace balance)",
			pools: map[string]*types.PoolInfo{
				commonstate.PoolNameReserve: {
					PoolName: commonstate.PoolNameReserve,
					TopologyAwareAssignments: map[int]machine.CPUSet{
						0: machine.MustParse("0"),
						1: machine.MustParse("0"),
						2: machine.MustParse("0"),
						3: machine.MustParse("0"),
					},
					OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
						0: machine.MustParse("0"),
						1: machine.MustParse("0"),
						2: machine.MustParse("0"),
						3: machine.MustParse("0"),
					},
				},
				commonstate.PoolNameShare: {
					PoolName: commonstate.PoolNameShare,
					TopologyAwareAssignments: map[int]machine.CPUSet{
						0: machine.MustParse("1"),
						1: machine.MustParse("24"),
					},
					OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
						0: machine.MustParse("1"),
						1: machine.MustParse("24"),
					},
				},
				commonstate.PoolNameReclaim: {
					PoolName: commonstate.PoolNameReclaim,
					TopologyAwareAssignments: map[int]machine.CPUSet{
						0: machine.MustParse("2"),
						1: machine.MustParse("25"),
						2: machine.MustParse("48"),
						3: machine.MustParse("72"),
					},
					OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
						0: machine.MustParse("2"),
						1: machine.MustParse("25"),
						2: machine.MustParse("48"),
						3: machine.MustParse("72"),
					},
				},
			},
			reclaimedEnable: true,
			needRecvAdvices: true,
			containers: []*types.ContainerInfo{
				makeContainerInfo("uid1", "default", "pod1", "c1", consts.PodAnnotationQoSLevelSharedCores, nil,
					map[int]machine.CPUSet{
						0: machine.MustParse("1"),
						1: machine.MustParse("24"),
					}, 200<<30),
				makeContainerInfo("uid2", "default", "pod2", "c2", consts.PodAnnotationQoSLevelSharedCores, nil,
					map[int]machine.CPUSet{
						0: machine.MustParse("1"),
						1: machine.MustParse("24"),
					}, 200<<30),
				makeContainerInfo("uid3", "default", "pod3", "c3", consts.PodAnnotationQoSLevelReclaimedCores, nil,
					map[int]machine.CPUSet{
						0: machine.MustParse("2"),
						1: machine.MustParse("25"),
						2: machine.MustParse("48"),
						3: machine.MustParse("72"),
					}, 200<<30),
				makeContainerInfo("uid4", "default", "pod4", "c4", consts.PodAnnotationQoSLevelReclaimedCores, nil,
					map[int]machine.CPUSet{
						0: machine.MustParse("2"),
						1: machine.MustParse("25"),
						2: machine.MustParse("48"),
						3: machine.MustParse("72"),
					}, 200<<30),
			},
			pods:        defaultPodList,
			plugins:     []types.MemoryAdvisorPluginName{memadvisorplugin.NumaMemoryBalancer},
			nodeMetrics: defaultNodeMetrics,
			numaMetrics: []numaMetric{
				{
					numaID:      0,
					metricName:  coreconsts.MetricMemLatencyReadNuma,
					metricValue: metricutil.MetricData{Value: 110},
				},
				{
					numaID:      1,
					metricName:  coreconsts.MetricMemLatencyReadNuma,
					metricValue: metricutil.MetricData{Value: 50},
				},
				{
					numaID:      2,
					metricName:  coreconsts.MetricMemLatencyReadNuma,
					metricValue: metricutil.MetricData{Value: 20},
				},
				{
					numaID:      3,
					metricName:  coreconsts.MetricMemLatencyReadNuma,
					metricValue: metricutil.MetricData{Value: 10},
				},
				{
					numaID:      0,
					metricName:  coreconsts.MetricMemBandwidthNuma,
					metricValue: metricutil.MetricData{Value: 1000},
				},
				{
					numaID:      1,
					metricName:  coreconsts.MetricMemBandwidthNuma,
					metricValue: metricutil.MetricData{Value: 300},
				},
				{
					numaID:      2,
					metricName:  coreconsts.MetricMemBandwidthNuma,
					metricValue: metricutil.MetricData{Value: 200},
				},
				{
					numaID:      3,
					metricName:  coreconsts.MetricMemBandwidthNuma,
					metricValue: metricutil.MetricData{Value: 100},
				},
				{
					numaID:      0,
					metricName:  coreconsts.MetricMemFreeNuma,
					metricValue: metricutil.MetricData{Value: 10 << 30},
				},
				{
					numaID:      1,
					metricName:  coreconsts.MetricMemFreeNuma,
					metricValue: metricutil.MetricData{Value: 10 << 30},
				},
				{
					numaID:      2,
					metricName:  coreconsts.MetricMemFreeNuma,
					metricValue: metricutil.MetricData{Value: 10 << 30},
				},
				{
					numaID:      3,
					metricName:  coreconsts.MetricMemFreeNuma,
					metricValue: metricutil.MetricData{Value: 10 << 30},
				},
				{
					numaID:      0,
					metricName:  coreconsts.MetricMemTotalNuma,
					metricValue: metricutil.MetricData{Value: 200 << 30},
				},
				{
					numaID:      1,
					metricName:  coreconsts.MetricMemTotalNuma,
					metricValue: metricutil.MetricData{Value: 200 << 30},
				},
				{
					numaID:      2,
					metricName:  coreconsts.MetricMemTotalNuma,
					metricValue: metricutil.MetricData{Value: 200 << 30},
				},
				{
					numaID:      3,
					metricName:  coreconsts.MetricMemTotalNuma,
					metricValue: metricutil.MetricData{Value: 200 << 30},
				},
				{
					numaID:      0,
					metricName:  coreconsts.MetricMemInactiveFileNuma,
					metricValue: metricutil.MetricData{Value: 5 << 30},
				},
				{
					numaID:      1,
					metricName:  coreconsts.MetricMemInactiveFileNuma,
					metricValue: metricutil.MetricData{Value: 5 << 30},
				},
				{
					numaID:      2,
					metricName:  coreconsts.MetricMemInactiveFileNuma,
					metricValue: metricutil.MetricData{Value: 5 << 30},
				},
				{
					numaID:      3,
					metricName:  coreconsts.MetricMemInactiveFileNuma,
					metricValue: metricutil.MetricData{Value: 5 << 30},
				},
			},
			containerNUMAMetrics: []containerNUMAMetric{
				{
					metricName:    coreconsts.MetricsMemAnonPerNumaContainer,
					metricValue:   metricutil.MetricData{Value: 2 << 30},
					podUID:        "uid1",
					containerName: "c1",
					numaID:        0,
				},
				{
					metricName:    coreconsts.MetricsMemAnonPerNumaContainer,
					metricValue:   metricutil.MetricData{Value: 1 << 30},
					podUID:        "uid2",
					containerName: "c2",
					numaID:        0,
				},
				{
					metricName:    coreconsts.MetricsMemAnonPerNumaContainer,
					metricValue:   metricutil.MetricData{Value: 2 << 30},
					podUID:        "uid3",
					containerName: "c3",
					numaID:        0,
				},
				{
					metricName:    coreconsts.MetricsMemAnonPerNumaContainer,
					metricValue:   metricutil.MetricData{Value: 1 << 30},
					podUID:        "uid4",
					containerName: "c4",
					numaID:        0,
				},
				{
					metricName:    coreconsts.MetricsMemTotalPerNumaContainer,
					metricValue:   metricutil.MetricData{Value: 50 << 30},
					podUID:        "uid1",
					containerName: "c1",
					numaID:        0,
				},
				{
					metricName:    coreconsts.MetricsMemTotalPerNumaContainer,
					metricValue:   metricutil.MetricData{Value: 50 << 30},
					podUID:        "uid2",
					containerName: "c2",
					numaID:        0,
				},
				{
					metricName:    coreconsts.MetricsMemTotalPerNumaContainer,
					metricValue:   metricutil.MetricData{Value: 50 << 30},
					podUID:        "uid3",
					containerName: "c3",
					numaID:        0,
				},
				{
					metricName:    coreconsts.MetricsMemTotalPerNumaContainer,
					metricValue:   metricutil.MetricData{Value: 50 << 30},
					podUID:        "uid3",
					containerName: "c3",
					numaID:        1,
				},
				{
					metricName:    coreconsts.MetricsMemTotalPerNumaContainer,
					metricValue:   metricutil.MetricData{Value: 50 << 30},
					podUID:        "uid3",
					containerName: "c3",
					numaID:        2,
				},
				{
					metricName:    coreconsts.MetricsMemTotalPerNumaContainer,
					metricValue:   metricutil.MetricData{Value: 50 << 30},
					podUID:        "uid3",
					containerName: "c3",
					numaID:        3,
				},
				{
					metricName:    coreconsts.MetricsMemTotalPerNumaContainer,
					metricValue:   metricutil.MetricData{Value: 50 << 30},
					podUID:        "uid4",
					containerName: "c4",
					numaID:        0,
				},
				{
					metricName:    coreconsts.MetricsMemTotalPerNumaContainer,
					metricValue:   metricutil.MetricData{Value: 50 << 30},
					podUID:        "uid4",
					containerName: "c4",
					numaID:        1,
				},
				{
					metricName:    coreconsts.MetricsMemTotalPerNumaContainer,
					metricValue:   metricutil.MetricData{Value: 50 << 30},
					podUID:        "uid4",
					containerName: "c4",
					numaID:        2,
				},
				{
					metricName:    coreconsts.MetricsMemTotalPerNumaContainer,
					metricValue:   metricutil.MetricData{Value: 50 << 30},
					podUID:        "uid4",
					containerName: "c4",
					numaID:        3,
				},
			},
			wantHeadroom: *resource.NewQuantity(980<<30, resource.DecimalSI),
			wantAdviceResult: types.InternalMemoryCalculationResult{
				ExtraEntries: []types.ExtraMemoryAdvices{
					{
						Values: map[string]string{
							string(memoryadvisor.ControlKnobKeyBalanceNumaMemory): "{\"destNumaList\":[1],\"sourceNuma\":0,\"migrateContainers\":[{\"podUID\":\"uid4\",\"containerName\":\"c4\",\"destNumaList\":[0,1,2,3]},{\"podUID\":\"uid3\",\"containerName\":\"c3\",\"destNumaList\":[0,1,2,3]}],\"totalRSS\":3221225472,\"threshold\":0.7}",
						},
					},
				},
			},
		},
		{
			name: "numa memory balance(force balance,evict)",
			pools: map[string]*types.PoolInfo{
				commonstate.PoolNameReserve: {
					PoolName: commonstate.PoolNameReserve,
					TopologyAwareAssignments: map[int]machine.CPUSet{
						0: machine.MustParse("0"),
						1: machine.MustParse("0"),
						2: machine.MustParse("0"),
						3: machine.MustParse("0"),
					},
					OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
						0: machine.MustParse("0"),
						1: machine.MustParse("0"),
						2: machine.MustParse("0"),
						3: machine.MustParse("0"),
					},
				},
				commonstate.PoolNameShare: {
					PoolName: commonstate.PoolNameShare,
					TopologyAwareAssignments: map[int]machine.CPUSet{
						0: machine.MustParse("1"),
						1: machine.MustParse("24"),
					},
					OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
						0: machine.MustParse("1"),
						1: machine.MustParse("24"),
					},
				},
				commonstate.PoolNameReclaim: {
					PoolName: commonstate.PoolNameReclaim,
					TopologyAwareAssignments: map[int]machine.CPUSet{
						0: machine.MustParse("2"),
						1: machine.MustParse("25"),
						2: machine.MustParse("48"),
						3: machine.MustParse("72"),
					},
					OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
						0: machine.MustParse("2"),
						1: machine.MustParse("25"),
						2: machine.MustParse("48"),
						3: machine.MustParse("72"),
					},
				},
			},
			reclaimedEnable: true,
			needRecvAdvices: true,
			containers: []*types.ContainerInfo{
				makeContainerInfo("uid1", "default", "pod1", "c1", consts.PodAnnotationQoSLevelSharedCores, nil,
					map[int]machine.CPUSet{
						0: machine.MustParse("1"),
						1: machine.MustParse("24"),
					}, 200<<30),
				makeContainerInfo("uid2", "default", "pod2", "c2", consts.PodAnnotationQoSLevelSharedCores, nil,
					map[int]machine.CPUSet{
						0: machine.MustParse("1"),
						1: machine.MustParse("24"),
					}, 200<<30),
				makeContainerInfo("uid3", "default", "pod3", "c3", consts.PodAnnotationQoSLevelReclaimedCores, nil,
					map[int]machine.CPUSet{
						0: machine.MustParse("2"),
						1: machine.MustParse("25"),
						2: machine.MustParse("48"),
						3: machine.MustParse("72"),
					}, 200<<30),
				makeContainerInfo("uid4", "default", "pod4", "c4", consts.PodAnnotationQoSLevelReclaimedCores, nil,
					map[int]machine.CPUSet{
						0: machine.MustParse("2"),
						1: machine.MustParse("25"),
						2: machine.MustParse("48"),
						3: machine.MustParse("72"),
					}, 200<<30),
			},
			pods:        defaultPodList,
			plugins:     []types.MemoryAdvisorPluginName{memadvisorplugin.NumaMemoryBalancer},
			nodeMetrics: defaultNodeMetrics,
			numaMetrics: []numaMetric{
				{
					numaID:      0,
					metricName:  coreconsts.MetricMemLatencyReadNuma,
					metricValue: metricutil.MetricData{Value: 121},
				},
				{
					numaID:      1,
					metricName:  coreconsts.MetricMemLatencyReadNuma,
					metricValue: metricutil.MetricData{Value: 105},
				},
				{
					numaID:      2,
					metricName:  coreconsts.MetricMemLatencyReadNuma,
					metricValue: metricutil.MetricData{Value: 20},
				},
				{
					numaID:      3,
					metricName:  coreconsts.MetricMemLatencyReadNuma,
					metricValue: metricutil.MetricData{Value: 10},
				},
				{
					numaID:      0,
					metricName:  coreconsts.MetricMemBandwidthNuma,
					metricValue: metricutil.MetricData{Value: 1000},
				},
				{
					numaID:      1,
					metricName:  coreconsts.MetricMemBandwidthNuma,
					metricValue: metricutil.MetricData{Value: 300},
				},
				{
					numaID:      2,
					metricName:  coreconsts.MetricMemBandwidthNuma,
					metricValue: metricutil.MetricData{Value: 200},
				},
				{
					numaID:      3,
					metricName:  coreconsts.MetricMemBandwidthNuma,
					metricValue: metricutil.MetricData{Value: 100},
				},
				{
					numaID:      0,
					metricName:  coreconsts.MetricMemFreeNuma,
					metricValue: metricutil.MetricData{Value: 10},
				},
				{
					numaID:      1,
					metricName:  coreconsts.MetricMemFreeNuma,
					metricValue: metricutil.MetricData{Value: 10},
				},
				{
					numaID:      2,
					metricName:  coreconsts.MetricMemFreeNuma,
					metricValue: metricutil.MetricData{Value: 10},
				},
				{
					numaID:      3,
					metricName:  coreconsts.MetricMemFreeNuma,
					metricValue: metricutil.MetricData{Value: 10},
				},
				{
					numaID:      0,
					metricName:  coreconsts.MetricMemTotalNuma,
					metricValue: metricutil.MetricData{Value: 200},
				},
				{
					numaID:      1,
					metricName:  coreconsts.MetricMemTotalNuma,
					metricValue: metricutil.MetricData{Value: 200},
				},
				{
					numaID:      2,
					metricName:  coreconsts.MetricMemTotalNuma,
					metricValue: metricutil.MetricData{Value: 200},
				},
				{
					numaID:      3,
					metricName:  coreconsts.MetricMemTotalNuma,
					metricValue: metricutil.MetricData{Value: 200},
				},
				{
					numaID:      0,
					metricName:  coreconsts.MetricMemInactiveFileNuma,
					metricValue: metricutil.MetricData{Value: 5},
				},
				{
					numaID:      1,
					metricName:  coreconsts.MetricMemInactiveFileNuma,
					metricValue: metricutil.MetricData{Value: 5},
				},
				{
					numaID:      2,
					metricName:  coreconsts.MetricMemInactiveFileNuma,
					metricValue: metricutil.MetricData{Value: 5},
				},
				{
					numaID:      3,
					metricName:  coreconsts.MetricMemInactiveFileNuma,
					metricValue: metricutil.MetricData{Value: 5},
				},
			},
			containerNUMAMetrics: []containerNUMAMetric{
				{
					metricName:    coreconsts.MetricsMemAnonPerNumaContainer,
					metricValue:   metricutil.MetricData{Value: 2 << 10},
					podUID:        "uid1",
					containerName: "c1",
					numaID:        0,
				},
				{
					metricName:    coreconsts.MetricsMemAnonPerNumaContainer,
					metricValue:   metricutil.MetricData{Value: 1 << 10},
					podUID:        "uid2",
					containerName: "c2",
					numaID:        0,
				},
				{
					metricName:    coreconsts.MetricsMemAnonPerNumaContainer,
					metricValue:   metricutil.MetricData{Value: 2 << 10},
					podUID:        "uid3",
					containerName: "c3",
					numaID:        0,
				},
				{
					metricName:    coreconsts.MetricsMemAnonPerNumaContainer,
					metricValue:   metricutil.MetricData{Value: 512 << 10},
					podUID:        "uid4",
					containerName: "c4",
					numaID:        0,
				},
				{
					metricName:    coreconsts.MetricsMemTotalPerNumaContainer,
					metricValue:   metricutil.MetricData{Value: 50 << 30},
					podUID:        "uid1",
					containerName: "c1",
					numaID:        0,
				},
				{
					metricName:    coreconsts.MetricsMemTotalPerNumaContainer,
					metricValue:   metricutil.MetricData{Value: 50 << 30},
					podUID:        "uid2",
					containerName: "c2",
					numaID:        0,
				},
				{
					metricName:    coreconsts.MetricsMemTotalPerNumaContainer,
					metricValue:   metricutil.MetricData{Value: 50 << 30},
					podUID:        "uid3",
					containerName: "c3",
					numaID:        0,
				},
				{
					metricName:    coreconsts.MetricsMemTotalPerNumaContainer,
					metricValue:   metricutil.MetricData{Value: 50 << 30},
					podUID:        "uid3",
					containerName: "c3",
					numaID:        1,
				},
				{
					metricName:    coreconsts.MetricsMemTotalPerNumaContainer,
					metricValue:   metricutil.MetricData{Value: 50 << 30},
					podUID:        "uid3",
					containerName: "c3",
					numaID:        2,
				},
				{
					metricName:    coreconsts.MetricsMemTotalPerNumaContainer,
					metricValue:   metricutil.MetricData{Value: 50 << 30},
					podUID:        "uid3",
					containerName: "c3",
					numaID:        3,
				},
				{
					metricName:    coreconsts.MetricsMemTotalPerNumaContainer,
					metricValue:   metricutil.MetricData{Value: 50 << 30},
					podUID:        "uid4",
					containerName: "c4",
					numaID:        0,
				},
				{
					metricName:    coreconsts.MetricsMemTotalPerNumaContainer,
					metricValue:   metricutil.MetricData{Value: 50 << 30},
					podUID:        "uid4",
					containerName: "c4",
					numaID:        1,
				},
				{
					metricName:    coreconsts.MetricsMemTotalPerNumaContainer,
					metricValue:   metricutil.MetricData{Value: 50 << 30},
					podUID:        "uid4",
					containerName: "c4",
					numaID:        2,
				},
				{
					metricName:    coreconsts.MetricsMemTotalPerNumaContainer,
					metricValue:   metricutil.MetricData{Value: 50 << 30},
					podUID:        "uid4",
					containerName: "c4",
					numaID:        3,
				},
			},
			wantHeadroom: *resource.NewQuantity(980<<30, resource.DecimalSI),
			wantAdviceResult: types.InternalMemoryCalculationResult{
				ContainerEntries: []types.ContainerMemoryAdvices{},
			},
		},
		{
			name: "numa memory balance(force balance,no reclaimed pod)",
			pools: map[string]*types.PoolInfo{
				commonstate.PoolNameReserve: {
					PoolName: commonstate.PoolNameReserve,
					TopologyAwareAssignments: map[int]machine.CPUSet{
						0: machine.MustParse("0"),
						1: machine.MustParse("0"),
						2: machine.MustParse("0"),
						3: machine.MustParse("0"),
					},
					OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
						0: machine.MustParse("0"),
						1: machine.MustParse("0"),
						2: machine.MustParse("0"),
						3: machine.MustParse("0"),
					},
				},
				commonstate.PoolNameShare: {
					PoolName: commonstate.PoolNameShare,
					TopologyAwareAssignments: map[int]machine.CPUSet{
						0: machine.MustParse("1"),
						1: machine.MustParse("24"),
						2: machine.MustParse("48"),
					},
					OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
						0: machine.MustParse("1"),
						1: machine.MustParse("24"),
						2: machine.MustParse("48"),
					},
				},
			},
			reclaimedEnable: true,
			needRecvAdvices: true,
			containers: []*types.ContainerInfo{
				makeContainerInfo("uid1", "default", "pod1", "c1", consts.PodAnnotationQoSLevelSharedCores, nil,
					map[int]machine.CPUSet{
						0: machine.MustParse("1"),
						1: machine.MustParse("24"),
					}, 200<<30),
				makeContainerInfo("uid2", "default", "pod2", "c2", consts.PodAnnotationQoSLevelSharedCores, nil,
					map[int]machine.CPUSet{
						0: machine.MustParse("1"),
						1: machine.MustParse("24"),
					}, 200<<30),
			},
			pods:        defaultPodList,
			plugins:     []types.MemoryAdvisorPluginName{memadvisorplugin.NumaMemoryBalancer},
			nodeMetrics: defaultNodeMetrics,
			numaMetrics: []numaMetric{
				{
					numaID:      0,
					metricName:  coreconsts.MetricMemLatencyReadNuma,
					metricValue: metricutil.MetricData{Value: 121},
				},
				{
					numaID:      1,
					metricName:  coreconsts.MetricMemLatencyReadNuma,
					metricValue: metricutil.MetricData{Value: 110},
				},
				{
					numaID:      2,
					metricName:  coreconsts.MetricMemLatencyReadNuma,
					metricValue: metricutil.MetricData{Value: 20},
				},
				{
					numaID:      3,
					metricName:  coreconsts.MetricMemLatencyReadNuma,
					metricValue: metricutil.MetricData{Value: 10},
				},
				{
					numaID:      0,
					metricName:  coreconsts.MetricMemBandwidthNuma,
					metricValue: metricutil.MetricData{Value: 1000},
				},
				{
					numaID:      1,
					metricName:  coreconsts.MetricMemBandwidthNuma,
					metricValue: metricutil.MetricData{Value: 300},
				},
				{
					numaID:      2,
					metricName:  coreconsts.MetricMemBandwidthNuma,
					metricValue: metricutil.MetricData{Value: 200},
				},
				{
					numaID:      3,
					metricName:  coreconsts.MetricMemBandwidthNuma,
					metricValue: metricutil.MetricData{Value: 100},
				},
				{
					numaID:      0,
					metricName:  coreconsts.MetricMemFreeNuma,
					metricValue: metricutil.MetricData{Value: 10},
				},
				{
					numaID:      1,
					metricName:  coreconsts.MetricMemFreeNuma,
					metricValue: metricutil.MetricData{Value: 10},
				},
				{
					numaID:      2,
					metricName:  coreconsts.MetricMemFreeNuma,
					metricValue: metricutil.MetricData{Value: 10},
				},
				{
					numaID:      3,
					metricName:  coreconsts.MetricMemFreeNuma,
					metricValue: metricutil.MetricData{Value: 10},
				},
				{
					numaID:      0,
					metricName:  coreconsts.MetricMemTotalNuma,
					metricValue: metricutil.MetricData{Value: 200},
				},
				{
					numaID:      1,
					metricName:  coreconsts.MetricMemTotalNuma,
					metricValue: metricutil.MetricData{Value: 200},
				},
				{
					numaID:      2,
					metricName:  coreconsts.MetricMemTotalNuma,
					metricValue: metricutil.MetricData{Value: 200},
				},
				{
					numaID:      3,
					metricName:  coreconsts.MetricMemTotalNuma,
					metricValue: metricutil.MetricData{Value: 200},
				},
				{
					numaID:      0,
					metricName:  coreconsts.MetricMemInactiveFileNuma,
					metricValue: metricutil.MetricData{Value: 5},
				},
				{
					numaID:      1,
					metricName:  coreconsts.MetricMemInactiveFileNuma,
					metricValue: metricutil.MetricData{Value: 5},
				},
				{
					numaID:      2,
					metricName:  coreconsts.MetricMemInactiveFileNuma,
					metricValue: metricutil.MetricData{Value: 5},
				},
				{
					numaID:      3,
					metricName:  coreconsts.MetricMemInactiveFileNuma,
					metricValue: metricutil.MetricData{Value: 5},
				},
			},
			containerNUMAMetrics: []containerNUMAMetric{
				{
					metricName:    coreconsts.MetricsMemAnonPerNumaContainer,
					metricValue:   metricutil.MetricData{Value: 2 << 30},
					podUID:        "uid1",
					containerName: "c1",
					numaID:        0,
				},
				{
					metricName:    coreconsts.MetricsMemAnonPerNumaContainer,
					metricValue:   metricutil.MetricData{Value: 1 << 30},
					podUID:        "uid2",
					containerName: "c2",
					numaID:        0,
				},
				{
					metricName:    coreconsts.MetricsMemAnonPerNumaContainer,
					metricValue:   metricutil.MetricData{Value: 2 << 30},
					podUID:        "uid3",
					containerName: "c3",
					numaID:        0,
				},
				{
					metricName:    coreconsts.MetricsMemAnonPerNumaContainer,
					metricValue:   metricutil.MetricData{Value: 512 << 20},
					podUID:        "uid4",
					containerName: "c4",
					numaID:        0,
				},
				{
					metricName:    coreconsts.MetricsMemTotalPerNumaContainer,
					metricValue:   metricutil.MetricData{Value: 50 << 30},
					podUID:        "uid1",
					containerName: "c1",
					numaID:        0,
				},
				{
					metricName:    coreconsts.MetricsMemTotalPerNumaContainer,
					metricValue:   metricutil.MetricData{Value: 50 << 30},
					podUID:        "uid2",
					containerName: "c2",
					numaID:        0,
				},
				{
					metricName:    coreconsts.MetricsMemTotalPerNumaContainer,
					metricValue:   metricutil.MetricData{Value: 50 << 30},
					podUID:        "uid3",
					containerName: "c3",
					numaID:        0,
				},
				{
					metricName:    coreconsts.MetricsMemTotalPerNumaContainer,
					metricValue:   metricutil.MetricData{Value: 50 << 30},
					podUID:        "uid4",
					containerName: "c4",
					numaID:        0,
				},
			},
			wantHeadroom: *resource.NewQuantity(980<<30, resource.DecimalSI),
			wantAdviceResult: types.InternalMemoryCalculationResult{
				ExtraEntries: []types.ExtraMemoryAdvices{
					{
						Values: map[string]string{
							string(memoryadvisor.ControlKnobKeyBalanceNumaMemory): "{\"destNumaList\":[3,2],\"sourceNuma\":0,\"migrateContainers\":[{\"podUID\":\"uid2\",\"containerName\":\"c2\",\"destNumaList\":[0,1,2]},{\"podUID\":\"uid1\",\"containerName\":\"c1\",\"destNumaList\":[0,1,2]}],\"totalRSS\":3221225472,\"threshold\":0.7}",
						},
					},
				},
			},
		},
		{
			name: "numa memory balance(grace balance,latency gap)",
			pools: map[string]*types.PoolInfo{
				commonstate.PoolNameReserve: {
					PoolName: commonstate.PoolNameReserve,
					TopologyAwareAssignments: map[int]machine.CPUSet{
						0: machine.MustParse("0"),
						1: machine.MustParse("0"),
						2: machine.MustParse("0"),
						3: machine.MustParse("0"),
					},
					OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
						0: machine.MustParse("0"),
						1: machine.MustParse("0"),
						2: machine.MustParse("0"),
						3: machine.MustParse("0"),
					},
				},
				commonstate.PoolNameShare: {
					PoolName: commonstate.PoolNameShare,
					TopologyAwareAssignments: map[int]machine.CPUSet{
						0: machine.MustParse("1"),
						1: machine.MustParse("24"),
					},
					OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
						0: machine.MustParse("1"),
						1: machine.MustParse("24"),
					},
				},
				commonstate.PoolNameReclaim: {
					PoolName: commonstate.PoolNameReclaim,
					TopologyAwareAssignments: map[int]machine.CPUSet{
						0: machine.MustParse("2"),
						1: machine.MustParse("25"),
						2: machine.MustParse("48"),
						3: machine.MustParse("72"),
					},
					OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
						0: machine.MustParse("2"),
						1: machine.MustParse("25"),
						2: machine.MustParse("48"),
						3: machine.MustParse("72"),
					},
				},
			},
			reclaimedEnable: true,
			needRecvAdvices: true,
			containers: []*types.ContainerInfo{
				makeContainerInfo("uid1", "default", "pod1", "c1", consts.PodAnnotationQoSLevelSharedCores, nil,
					map[int]machine.CPUSet{
						0: machine.MustParse("1"),
						1: machine.MustParse("24"),
					}, 200<<30),
				makeContainerInfo("uid2", "default", "pod2", "c2", consts.PodAnnotationQoSLevelSharedCores, nil,
					map[int]machine.CPUSet{
						0: machine.MustParse("1"),
						1: machine.MustParse("24"),
					}, 200<<30),
				makeContainerInfo("uid3", "default", "pod3", "c3", consts.PodAnnotationQoSLevelReclaimedCores, nil,
					map[int]machine.CPUSet{
						0: machine.MustParse("2"),
						1: machine.MustParse("25"),
						2: machine.MustParse("48"),
						3: machine.MustParse("72"),
					}, 200<<30),
				makeContainerInfo("uid4", "default", "pod4", "c4", consts.PodAnnotationQoSLevelReclaimedCores, nil,
					map[int]machine.CPUSet{
						0: machine.MustParse("2"),
						1: machine.MustParse("25"),
						2: machine.MustParse("48"),
						3: machine.MustParse("72"),
					}, 200<<30),
			},
			pods:        defaultPodList,
			plugins:     []types.MemoryAdvisorPluginName{memadvisorplugin.NumaMemoryBalancer},
			nodeMetrics: defaultNodeMetrics,
			numaMetrics: []numaMetric{
				{
					numaID:      0,
					metricName:  coreconsts.MetricMemLatencyReadNuma,
					metricValue: metricutil.MetricData{Value: 90},
				},
				{
					numaID:      1,
					metricName:  coreconsts.MetricMemLatencyReadNuma,
					metricValue: metricutil.MetricData{Value: 40},
				},
				{
					numaID:      2,
					metricName:  coreconsts.MetricMemLatencyReadNuma,
					metricValue: metricutil.MetricData{Value: 20},
				},
				{
					numaID:      3,
					metricName:  coreconsts.MetricMemLatencyReadNuma,
					metricValue: metricutil.MetricData{Value: 10},
				},
				{
					numaID:      0,
					metricName:  coreconsts.MetricMemBandwidthNuma,
					metricValue: metricutil.MetricData{Value: 1000},
				},
				{
					numaID:      1,
					metricName:  coreconsts.MetricMemBandwidthNuma,
					metricValue: metricutil.MetricData{Value: 300},
				},
				{
					numaID:      2,
					metricName:  coreconsts.MetricMemBandwidthNuma,
					metricValue: metricutil.MetricData{Value: 200},
				},
				{
					numaID:      3,
					metricName:  coreconsts.MetricMemBandwidthNuma,
					metricValue: metricutil.MetricData{Value: 100},
				},
				{
					numaID:      0,
					metricName:  coreconsts.MetricMemFreeNuma,
					metricValue: metricutil.MetricData{Value: 10},
				},
				{
					numaID:      1,
					metricName:  coreconsts.MetricMemFreeNuma,
					metricValue: metricutil.MetricData{Value: 10},
				},
				{
					numaID:      2,
					metricName:  coreconsts.MetricMemFreeNuma,
					metricValue: metricutil.MetricData{Value: 10},
				},
				{
					numaID:      3,
					metricName:  coreconsts.MetricMemFreeNuma,
					metricValue: metricutil.MetricData{Value: 10},
				},
				{
					numaID:      0,
					metricName:  coreconsts.MetricMemTotalNuma,
					metricValue: metricutil.MetricData{Value: 200},
				},
				{
					numaID:      1,
					metricName:  coreconsts.MetricMemTotalNuma,
					metricValue: metricutil.MetricData{Value: 200},
				},
				{
					numaID:      2,
					metricName:  coreconsts.MetricMemTotalNuma,
					metricValue: metricutil.MetricData{Value: 200},
				},
				{
					numaID:      3,
					metricName:  coreconsts.MetricMemTotalNuma,
					metricValue: metricutil.MetricData{Value: 200},
				},
				{
					numaID:      0,
					metricName:  coreconsts.MetricMemInactiveFileNuma,
					metricValue: metricutil.MetricData{Value: 5},
				},
				{
					numaID:      1,
					metricName:  coreconsts.MetricMemInactiveFileNuma,
					metricValue: metricutil.MetricData{Value: 5},
				},
				{
					numaID:      2,
					metricName:  coreconsts.MetricMemInactiveFileNuma,
					metricValue: metricutil.MetricData{Value: 5},
				},
				{
					numaID:      3,
					metricName:  coreconsts.MetricMemInactiveFileNuma,
					metricValue: metricutil.MetricData{Value: 5},
				},
			},
			containerNUMAMetrics: []containerNUMAMetric{
				{
					metricName:    coreconsts.MetricsMemAnonPerNumaContainer,
					metricValue:   metricutil.MetricData{Value: 2 << 30},
					podUID:        "uid1",
					containerName: "c1",
					numaID:        0,
				},
				{
					metricName:    coreconsts.MetricsMemAnonPerNumaContainer,
					metricValue:   metricutil.MetricData{Value: 1 << 30},
					podUID:        "uid2",
					containerName: "c2",
					numaID:        0,
				},
				{
					metricName:    coreconsts.MetricsMemAnonPerNumaContainer,
					metricValue:   metricutil.MetricData{Value: 2 << 30},
					podUID:        "uid3",
					containerName: "c3",
					numaID:        0,
				},
				{
					metricName:    coreconsts.MetricsMemAnonPerNumaContainer,
					metricValue:   metricutil.MetricData{Value: 1 << 30},
					podUID:        "uid4",
					containerName: "c4",
					numaID:        0,
				},
				{
					metricName:    coreconsts.MetricsMemTotalPerNumaContainer,
					metricValue:   metricutil.MetricData{Value: 50 << 30},
					podUID:        "uid1",
					containerName: "c1",
					numaID:        0,
				},
				{
					metricName:    coreconsts.MetricsMemTotalPerNumaContainer,
					metricValue:   metricutil.MetricData{Value: 50 << 30},
					podUID:        "uid2",
					containerName: "c2",
					numaID:        0,
				},
				{
					metricName:    coreconsts.MetricsMemTotalPerNumaContainer,
					metricValue:   metricutil.MetricData{Value: 50 << 30},
					podUID:        "uid3",
					containerName: "c3",
					numaID:        0,
				},
				{
					metricName:    coreconsts.MetricsMemTotalPerNumaContainer,
					metricValue:   metricutil.MetricData{Value: 50 << 30},
					podUID:        "uid3",
					containerName: "c3",
					numaID:        1,
				},
				{
					metricName:    coreconsts.MetricsMemTotalPerNumaContainer,
					metricValue:   metricutil.MetricData{Value: 50 << 30},
					podUID:        "uid3",
					containerName: "c3",
					numaID:        2,
				},
				{
					metricName:    coreconsts.MetricsMemTotalPerNumaContainer,
					metricValue:   metricutil.MetricData{Value: 50 << 30},
					podUID:        "uid3",
					containerName: "c3",
					numaID:        3,
				},
				{
					metricName:    coreconsts.MetricsMemTotalPerNumaContainer,
					metricValue:   metricutil.MetricData{Value: 50 << 30},
					podUID:        "uid4",
					containerName: "c4",
					numaID:        0,
				},
				{
					metricName:    coreconsts.MetricsMemTotalPerNumaContainer,
					metricValue:   metricutil.MetricData{Value: 50 << 30},
					podUID:        "uid4",
					containerName: "c4",
					numaID:        1,
				},
				{
					metricName:    coreconsts.MetricsMemTotalPerNumaContainer,
					metricValue:   metricutil.MetricData{Value: 50 << 30},
					podUID:        "uid4",
					containerName: "c4",
					numaID:        2,
				},
				{
					metricName:    coreconsts.MetricsMemTotalPerNumaContainer,
					metricValue:   metricutil.MetricData{Value: 50 << 30},
					podUID:        "uid4",
					containerName: "c4",
					numaID:        3,
				},
			},
			wantHeadroom: *resource.NewQuantity(980<<30, resource.DecimalSI),
			wantAdviceResult: types.InternalMemoryCalculationResult{
				ExtraEntries: []types.ExtraMemoryAdvices{
					{
						Values: map[string]string{
							string(memoryadvisor.ControlKnobKeyBalanceNumaMemory): "{\"destNumaList\":[1],\"sourceNuma\":0,\"migrateContainers\":[{\"podUID\":\"uid4\",\"containerName\":\"c4\",\"destNumaList\":[0,1,2,3]},{\"podUID\":\"uid3\",\"containerName\":\"c3\",\"destNumaList\":[0,1,2,3]}],\"totalRSS\":3221225472,\"threshold\":0.7}",
						},
					},
				},
			},
		},
		{
			name: "numa memory balance(force balance,bandwidth level medium)",
			pools: map[string]*types.PoolInfo{
				commonstate.PoolNameReserve: {
					PoolName: commonstate.PoolNameReserve,
					TopologyAwareAssignments: map[int]machine.CPUSet{
						0: machine.MustParse("0"),
						1: machine.MustParse("0"),
						2: machine.MustParse("0"),
						3: machine.MustParse("0"),
					},
					OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
						0: machine.MustParse("0"),
						1: machine.MustParse("0"),
						2: machine.MustParse("0"),
						3: machine.MustParse("0"),
					},
				},
				commonstate.PoolNameShare: {
					PoolName: commonstate.PoolNameShare,
					TopologyAwareAssignments: map[int]machine.CPUSet{
						0: machine.MustParse("1"),
						1: machine.MustParse("24"),
						2: machine.MustParse("48"),
					},
					OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
						0: machine.MustParse("1"),
						1: machine.MustParse("24"),
						2: machine.MustParse("48"),
					},
				},
			},
			reclaimedEnable: true,
			needRecvAdvices: true,
			containers: []*types.ContainerInfo{
				makeContainerInfo("uid1", "default", "pod1", "c1", consts.PodAnnotationQoSLevelSharedCores, nil,
					map[int]machine.CPUSet{
						0: machine.MustParse("1"),
						1: machine.MustParse("24"),
						2: machine.MustParse("48"),
					}, 200<<30),
				makeContainerInfo("uid2", "default", "pod2", "c2", consts.PodAnnotationQoSLevelSharedCores, nil,
					map[int]machine.CPUSet{
						0: machine.MustParse("1"),
						1: machine.MustParse("24"),
						2: machine.MustParse("48"),
					}, 200<<30),
			},
			pods:        defaultPodList,
			plugins:     []types.MemoryAdvisorPluginName{memadvisorplugin.NumaMemoryBalancer},
			nodeMetrics: defaultNodeMetrics,
			numaMetrics: []numaMetric{
				{
					numaID:      0,
					metricName:  coreconsts.MetricMemLatencyReadNuma,
					metricValue: metricutil.MetricData{Value: 130},
				},
				{
					numaID:      1,
					metricName:  coreconsts.MetricMemLatencyReadNuma,
					metricValue: metricutil.MetricData{Value: 40},
				},
				{
					numaID:      2,
					metricName:  coreconsts.MetricMemLatencyReadNuma,
					metricValue: metricutil.MetricData{Value: 20},
				},
				{
					numaID:      3,
					metricName:  coreconsts.MetricMemLatencyReadNuma,
					metricValue: metricutil.MetricData{Value: 10},
				},
				{
					numaID:      0,
					metricName:  coreconsts.MetricMemBandwidthNuma,
					metricValue: metricutil.MetricData{Value: 600},
				},
				{
					numaID:      1,
					metricName:  coreconsts.MetricMemBandwidthNuma,
					metricValue: metricutil.MetricData{Value: 100},
				},
				{
					numaID:      2,
					metricName:  coreconsts.MetricMemBandwidthNuma,
					metricValue: metricutil.MetricData{Value: 1000},
				},
				{
					numaID:      3,
					metricName:  coreconsts.MetricMemBandwidthNuma,
					metricValue: metricutil.MetricData{Value: 300},
				},
				{
					numaID:      0,
					metricName:  coreconsts.MetricMemFreeNuma,
					metricValue: metricutil.MetricData{Value: 10},
				},
				{
					numaID:      1,
					metricName:  coreconsts.MetricMemFreeNuma,
					metricValue: metricutil.MetricData{Value: 10},
				},
				{
					numaID:      2,
					metricName:  coreconsts.MetricMemFreeNuma,
					metricValue: metricutil.MetricData{Value: 10},
				},
				{
					numaID:      3,
					metricName:  coreconsts.MetricMemFreeNuma,
					metricValue: metricutil.MetricData{Value: 10},
				},
				{
					numaID:      0,
					metricName:  coreconsts.MetricMemTotalNuma,
					metricValue: metricutil.MetricData{Value: 200},
				},
				{
					numaID:      1,
					metricName:  coreconsts.MetricMemTotalNuma,
					metricValue: metricutil.MetricData{Value: 200},
				},
				{
					numaID:      2,
					metricName:  coreconsts.MetricMemTotalNuma,
					metricValue: metricutil.MetricData{Value: 200},
				},
				{
					numaID:      3,
					metricName:  coreconsts.MetricMemTotalNuma,
					metricValue: metricutil.MetricData{Value: 200},
				},
				{
					numaID:      0,
					metricName:  coreconsts.MetricMemInactiveFileNuma,
					metricValue: metricutil.MetricData{Value: 5},
				},
				{
					numaID:      1,
					metricName:  coreconsts.MetricMemInactiveFileNuma,
					metricValue: metricutil.MetricData{Value: 5},
				},
				{
					numaID:      2,
					metricName:  coreconsts.MetricMemInactiveFileNuma,
					metricValue: metricutil.MetricData{Value: 5},
				},
				{
					numaID:      3,
					metricName:  coreconsts.MetricMemInactiveFileNuma,
					metricValue: metricutil.MetricData{Value: 5},
				},
			},
			containerNUMAMetrics: []containerNUMAMetric{
				{
					metricName:    coreconsts.MetricsMemAnonPerNumaContainer,
					metricValue:   metricutil.MetricData{Value: 2 << 30},
					podUID:        "uid1",
					containerName: "c1",
					numaID:        0,
				},
				{
					metricName:    coreconsts.MetricsMemAnonPerNumaContainer,
					metricValue:   metricutil.MetricData{Value: 1 << 30},
					podUID:        "uid2",
					containerName: "c2",
					numaID:        0,
				},
				{
					metricName:    coreconsts.MetricsMemAnonPerNumaContainer,
					metricValue:   metricutil.MetricData{Value: 2 << 30},
					podUID:        "uid1",
					containerName: "c1",
					numaID:        2,
				},
				{
					metricName:    coreconsts.MetricsMemAnonPerNumaContainer,
					metricValue:   metricutil.MetricData{Value: 1 << 30},
					podUID:        "uid4",
					containerName: "c4",
					numaID:        2,
				},
				{
					metricName:    coreconsts.MetricsMemTotalPerNumaContainer,
					metricValue:   metricutil.MetricData{Value: 50 << 30},
					podUID:        "uid1",
					containerName: "c1",
					numaID:        0,
				},
				{
					metricName:    coreconsts.MetricsMemTotalPerNumaContainer,
					metricValue:   metricutil.MetricData{Value: 50 << 30},
					podUID:        "uid2",
					containerName: "c2",
					numaID:        0,
				},
				{
					metricName:    coreconsts.MetricsMemTotalPerNumaContainer,
					metricValue:   metricutil.MetricData{Value: 50 << 30},
					podUID:        "uid3",
					containerName: "c3",
					numaID:        0,
				},
				{
					metricName:    coreconsts.MetricsMemTotalPerNumaContainer,
					metricValue:   metricutil.MetricData{Value: 50 << 30},
					podUID:        "uid4",
					containerName: "c4",
					numaID:        0,
				},
			},
			wantHeadroom: *resource.NewQuantity(980<<30, resource.DecimalSI),
			wantAdviceResult: types.InternalMemoryCalculationResult{
				ExtraEntries: []types.ExtraMemoryAdvices{
					{
						Values: map[string]string{
							string(memoryadvisor.ControlKnobKeyBalanceNumaMemory): "{\"destNumaList\":[1],\"sourceNuma\":2,\"migrateContainers\":[{\"podUID\":\"uid1\",\"containerName\":\"c1\",\"destNumaList\":[0,1,2]}],\"totalRSS\":2147483648,\"threshold\":0.7}",
						},
					},
				},
			},
		},
		{
			name: "numa memory balance(force balance,bandwidth level low)",
			pools: map[string]*types.PoolInfo{
				commonstate.PoolNameReserve: {
					PoolName: commonstate.PoolNameReserve,
					TopologyAwareAssignments: map[int]machine.CPUSet{
						0: machine.MustParse("0"),
						1: machine.MustParse("0"),
						2: machine.MustParse("0"),
						3: machine.MustParse("0"),
					},
					OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
						0: machine.MustParse("0"),
						1: machine.MustParse("0"),
						2: machine.MustParse("0"),
						3: machine.MustParse("0"),
					},
				},
				commonstate.PoolNameShare: {
					PoolName: commonstate.PoolNameShare,
					TopologyAwareAssignments: map[int]machine.CPUSet{
						0: machine.MustParse("1"),
						1: machine.MustParse("24"),
						2: machine.MustParse("48"),
					},
					OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
						0: machine.MustParse("1"),
						1: machine.MustParse("24"),
						2: machine.MustParse("48"),
					},
				},
			},
			reclaimedEnable: true,
			needRecvAdvices: true,
			containers: []*types.ContainerInfo{
				makeContainerInfo("uid1", "default", "pod1", "c1", consts.PodAnnotationQoSLevelSharedCores, nil,
					map[int]machine.CPUSet{
						0: machine.MustParse("1"),
						1: machine.MustParse("24"),
					}, 200<<30),
				makeContainerInfo("uid2", "default", "pod2", "c2", consts.PodAnnotationQoSLevelSharedCores, nil,
					map[int]machine.CPUSet{
						0: machine.MustParse("1"),
						1: machine.MustParse("24"),
					}, 200<<30),
			},
			pods:        defaultPodList,
			plugins:     []types.MemoryAdvisorPluginName{memadvisorplugin.NumaMemoryBalancer},
			nodeMetrics: defaultNodeMetrics,
			numaMetrics: []numaMetric{
				{
					numaID:      0,
					metricName:  coreconsts.MetricMemLatencyReadNuma,
					metricValue: metricutil.MetricData{Value: 130},
				},
				{
					numaID:      1,
					metricName:  coreconsts.MetricMemLatencyReadNuma,
					metricValue: metricutil.MetricData{Value: 40},
				},
				{
					numaID:      2,
					metricName:  coreconsts.MetricMemLatencyReadNuma,
					metricValue: metricutil.MetricData{Value: 20},
				},
				{
					numaID:      3,
					metricName:  coreconsts.MetricMemLatencyReadNuma,
					metricValue: metricutil.MetricData{Value: 10},
				},
				{
					numaID:      0,
					metricName:  coreconsts.MetricMemBandwidthNuma,
					metricValue: metricutil.MetricData{Value: 300},
				},
				{
					numaID:      1,
					metricName:  coreconsts.MetricMemBandwidthNuma,
					metricValue: metricutil.MetricData{Value: 300},
				},
				{
					numaID:      2,
					metricName:  coreconsts.MetricMemBandwidthNuma,
					metricValue: metricutil.MetricData{Value: 1000},
				},
				{
					numaID:      3,
					metricName:  coreconsts.MetricMemBandwidthNuma,
					metricValue: metricutil.MetricData{Value: 100},
				},
				{
					numaID:      0,
					metricName:  coreconsts.MetricMemFreeNuma,
					metricValue: metricutil.MetricData{Value: 10},
				},
				{
					numaID:      1,
					metricName:  coreconsts.MetricMemFreeNuma,
					metricValue: metricutil.MetricData{Value: 10},
				},
				{
					numaID:      2,
					metricName:  coreconsts.MetricMemFreeNuma,
					metricValue: metricutil.MetricData{Value: 10},
				},
				{
					numaID:      3,
					metricName:  coreconsts.MetricMemFreeNuma,
					metricValue: metricutil.MetricData{Value: 10},
				},
				{
					numaID:      0,
					metricName:  coreconsts.MetricMemTotalNuma,
					metricValue: metricutil.MetricData{Value: 200},
				},
				{
					numaID:      1,
					metricName:  coreconsts.MetricMemTotalNuma,
					metricValue: metricutil.MetricData{Value: 200},
				},
				{
					numaID:      2,
					metricName:  coreconsts.MetricMemTotalNuma,
					metricValue: metricutil.MetricData{Value: 200},
				},
				{
					numaID:      3,
					metricName:  coreconsts.MetricMemTotalNuma,
					metricValue: metricutil.MetricData{Value: 200},
				},
				{
					numaID:      0,
					metricName:  coreconsts.MetricMemInactiveFileNuma,
					metricValue: metricutil.MetricData{Value: 5},
				},
				{
					numaID:      1,
					metricName:  coreconsts.MetricMemInactiveFileNuma,
					metricValue: metricutil.MetricData{Value: 5},
				},
				{
					numaID:      2,
					metricName:  coreconsts.MetricMemInactiveFileNuma,
					metricValue: metricutil.MetricData{Value: 5},
				},
				{
					numaID:      3,
					metricName:  coreconsts.MetricMemInactiveFileNuma,
					metricValue: metricutil.MetricData{Value: 5},
				},
			},
			containerNUMAMetrics: []containerNUMAMetric{
				{
					metricName:    coreconsts.MetricsMemAnonPerNumaContainer,
					metricValue:   metricutil.MetricData{Value: 2 << 30},
					podUID:        "uid1",
					containerName: "c1",
					numaID:        0,
				},
				{
					metricName:    coreconsts.MetricsMemAnonPerNumaContainer,
					metricValue:   metricutil.MetricData{Value: 1 << 30},
					podUID:        "uid2",
					containerName: "c2",
					numaID:        0,
				},
				{
					metricName:    coreconsts.MetricsMemAnonPerNumaContainer,
					metricValue:   metricutil.MetricData{Value: 2 << 30},
					podUID:        "uid1",
					containerName: "c1",
					numaID:        2,
				},
				{
					metricName:    coreconsts.MetricsMemAnonPerNumaContainer,
					metricValue:   metricutil.MetricData{Value: 1 << 30},
					podUID:        "uid4",
					containerName: "c4",
					numaID:        2,
				},
				{
					metricName:    coreconsts.MetricsMemTotalPerNumaContainer,
					metricValue:   metricutil.MetricData{Value: 50 << 30},
					podUID:        "uid1",
					containerName: "c1",
					numaID:        0,
				},
				{
					metricName:    coreconsts.MetricsMemTotalPerNumaContainer,
					metricValue:   metricutil.MetricData{Value: 50 << 30},
					podUID:        "uid2",
					containerName: "c2",
					numaID:        0,
				},
				{
					metricName:    coreconsts.MetricsMemTotalPerNumaContainer,
					metricValue:   metricutil.MetricData{Value: 50 << 30},
					podUID:        "uid3",
					containerName: "c3",
					numaID:        0,
				},
				{
					metricName:    coreconsts.MetricsMemTotalPerNumaContainer,
					metricValue:   metricutil.MetricData{Value: 50 << 30},
					podUID:        "uid4",
					containerName: "c4",
					numaID:        0,
				},
			},
			wantHeadroom: *resource.NewQuantity(980<<30, resource.DecimalSI),
			wantAdviceResult: types.InternalMemoryCalculationResult{
				ExtraEntries: []types.ExtraMemoryAdvices{
					{
						Values: map[string]string{
							string(memoryadvisor.ControlKnobKeyBalanceNumaMemory): "{\"destNumaList\":[0],\"sourceNuma\":2,\"migrateContainers\":[{\"podUID\":\"uid1\",\"containerName\":\"c1\",\"destNumaList\":[0,1,2]}],\"totalRSS\":2147483648,\"threshold\":0.7}",
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
				metricsFetcher.SetContainerNumaMetric(containerNUMAMetric.podUID, containerNUMAMetric.containerName, containerNUMAMetric.numaID, containerNUMAMetric.metricName, containerNUMAMetric.metricValue)
			}
			for _, qosClassMetric := range tt.cgroupMetrics {
				metricsFetcher.SetCgroupMetric(qosClassMetric.cgroupPath, qosClassMetric.metricName, qosClassMetric.metricValue)
			}
			for _, cgroupNUMAMetric := range tt.cgroupNUMAMetrics {
				metricsFetcher.SetCgroupNumaMetric(cgroupNUMAMetric.cgroupPath, cgroupNUMAMetric.numaID, cgroupNUMAMetric.metricName, cgroupNUMAMetric.metricValue)
			}
			if tt.metricsFetcherSynced != nil {
				metricsFetcher.SetSynced(*tt.metricsFetcherSynced)
			}

			advisor, metaCache := newTestMemoryAdvisor(t, tt.pods, ckDir, sfDir, fetcher, tt.plugins)
			advisor.conf.GetDynamicConfiguration().EnableReclaim = tt.reclaimedEnable
			transparentMemoryOffloadingConfiguration := tmo.NewTransparentMemoryOffloadingConfiguration()
			transparentMemoryOffloadingConfiguration.QoSLevelConfigs[consts.QoSLevelReclaimedCores] = tmo.NewTMOConfigDetail(transparentMemoryOffloadingConfiguration.DefaultConfigurations)
			transparentMemoryOffloadingConfiguration.QoSLevelConfigs[consts.QoSLevelReclaimedCores].EnableTMO = true
			transparentMemoryOffloadingConfiguration.QoSLevelConfigs[consts.QoSLevelReclaimedCores].EnableSwap = false
			transparentMemoryOffloadingConfiguration.QoSLevelConfigs[consts.QoSLevelSharedCores] = tmo.NewTMOConfigDetail(transparentMemoryOffloadingConfiguration.DefaultConfigurations)
			transparentMemoryOffloadingConfiguration.QoSLevelConfigs[consts.QoSLevelSharedCores].EnableTMO = true
			transparentMemoryOffloadingConfiguration.QoSLevelConfigs[consts.QoSLevelSharedCores].EnableSwap = true

			// cgroup level
			transparentMemoryOffloadingConfiguration.CgroupConfigs["/sys/fs/cgroup/hdfs"] = tmo.NewTMOConfigDetail(transparentMemoryOffloadingConfiguration.DefaultConfigurations)
			transparentMemoryOffloadingConfiguration.CgroupConfigs["/sys/fs/cgroup/hdfs"].EnableTMO = true
			transparentMemoryOffloadingConfiguration.CgroupConfigs["/sys/fs/cgroup/hdfs"].EnableSwap = false

			advisor.conf.GetDynamicConfiguration().TransparentMemoryOffloadingConfiguration = transparentMemoryOffloadingConfiguration
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

			time.Sleep(100 * time.Millisecond) // Wait some time because no signal will be sent to channel
			if tt.needRecvAdvices {
				result := <-recvCh

				assert.ElementsMatch(t, tt.wantAdviceResult.ExtraEntries, result.ExtraEntries)
				assert.ElementsMatch(t, tt.wantAdviceResult.ContainerEntries, result.ContainerEntries)
			}
			headroom, _, err := advisor.GetHeadroom()

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

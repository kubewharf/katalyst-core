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
	"io/ioutil"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	k8stypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	configapi "github.com/kubewharf/katalyst-api/pkg/apis/config/v1alpha1"
	configv1alpha1 "github.com/kubewharf/katalyst-api/pkg/apis/config/v1alpha1"
	workloadapis "github.com/kubewharf/katalyst-api/pkg/apis/workload/v1alpha1"
	"github.com/kubewharf/katalyst-api/pkg/consts"
	katalyst_base "github.com/kubewharf/katalyst-core/cmd/base"
	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/options"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/qosaware/resource/cpu/region"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/config"
	metric_consts "github.com/kubewharf/katalyst-core/pkg/consts"
	pkgconsts "github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/pod"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/spd"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	metricspool "github.com/kubewharf/katalyst-core/pkg/metrics/metrics-pool"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
	utilmetric "github.com/kubewharf/katalyst-core/pkg/util/metric"
)

func generateTestConfiguration(t *testing.T, checkpointDir, stateFileDir string) *config.Configuration {
	conf, err := options.NewOptions().Config()
	require.NoError(t, err)
	require.NotNil(t, conf)

	conf.GenericSysAdvisorConfiguration.StateFileDirectory = stateFileDir
	conf.MetaServerConfiguration.CheckpointManagerDir = checkpointDir
	conf.CPUShareConfiguration.RestrictRefPolicy = nil

	return conf
}

func newTestCPUResourceAdvisor(t *testing.T, pods []*v1.Pod, conf *config.Configuration, mf *metric.FakeMetricsFetcher, profiles map[k8stypes.UID]spd.DummyPodServiceProfile) (*cpuResourceAdvisor, metacache.MetaCache) {
	metaCache, err := metacache.NewMetaCacheImp(conf, metricspool.DummyMetricsEmitterPool{}, mf)
	require.NoError(t, err)

	// numa node0 cpu(s): 0-23,48-71
	// numa node1 cpu(s): 24-47,72-95
	cpuTopology, err := machine.GenerateDummyCPUTopology(96, 2, 2)
	assert.NoError(t, err)

	genericCtx, err := katalyst_base.GenerateFakeGenericContext([]runtime.Object{})
	require.NoError(t, err)

	metaServer, err := metaserver.NewMetaServer(genericCtx.Client, metrics.DummyMetrics{}, conf)
	require.NoError(t, err)

	metaServer.MetaAgent = &agent.MetaAgent{
		KatalystMachineInfo: &machine.KatalystMachineInfo{
			CPUTopology: cpuTopology,
		},
		PodFetcher: &pod.PodFetcherStub{
			PodList: pods,
		},
		MetricsFetcher: mf,
	}

	err = metaServer.SetServiceProfilingManager(spd.NewDummyServiceProfilingManager(profiles))
	require.NoError(t, err)

	cra := NewCPUResourceAdvisor(conf, struct{}{}, metaCache, metaServer, metrics.DummyMetrics{})
	require.NotNil(t, cra)

	return cra, metaCache
}

func makeContainerInfo(podUID, namespace, podName, containerName, qoSLevel, ownerPoolName string, annotations map[string]string,
	topologyAwareAssignments types.TopologyAwareAssignment, cpu ...float64,
) *types.ContainerInfo {
	req, limit := 0., 0.
	if len(cpu) == 1 {
		req, limit = cpu[0], cpu[0]
	} else if len(cpu) == 2 {
		req, limit = cpu[0], cpu[1]
	}

	return &types.ContainerInfo{
		PodUID:                           podUID,
		PodNamespace:                     namespace,
		PodName:                          podName,
		ContainerName:                    containerName,
		ContainerIndex:                   0,
		Labels:                           nil,
		Annotations:                      annotations,
		QoSLevel:                         qoSLevel,
		CPURequest:                       req,
		CPULimit:                         limit,
		MemoryRequest:                    0,
		MemoryLimit:                      0,
		RampUp:                           false,
		OriginOwnerPoolName:              ownerPoolName,
		OwnerPoolName:                    ownerPoolName,
		TopologyAwareAssignments:         topologyAwareAssignments,
		OriginalTopologyAwareAssignments: topologyAwareAssignments,
		ContainerType:                    v1alpha1.ContainerType_MAIN,
	}
}

func TestAdvisorUpdate(t *testing.T) {
	t.Parallel()

	type containerMetricItem struct {
		pod       string
		container string
		value     float64
	}

	type numaMetricItem struct {
		numaID int
		name   string
		value  float64
	}

	tests := []struct {
		name                          string
		preUpdate                     bool
		pools                         map[string]*types.PoolInfo
		containers                    []*types.ContainerInfo
		pods                          []*v1.Pod
		nodeEnableReclaim             bool
		headroomAssembler             types.CPUHeadroomAssemblerName
		headroomPolicies              map[configapi.QoSRegionType][]types.CPUHeadroomPolicyName
		podProfiles                   map[k8stypes.UID]spd.DummyPodServiceProfile
		wantInternalCalculationResult types.InternalCPUCalculationResult
		wantHeadroom                  resource.Quantity
		wantHeadroomErr               bool
		containerMetrics              []containerMetricItem
		numaMetricItems               []numaMetricItem
	}{
		{
			name:                          "missing_reserve_pool",
			pools:                         map[string]*types.PoolInfo{},
			wantInternalCalculationResult: types.InternalCPUCalculationResult{},
			wantHeadroom:                  resource.Quantity{},
		},
		{
			name: "provision:reserve_pool_only",
			pools: map[string]*types.PoolInfo{
				commonstate.PoolNameReserve: {
					PoolName: commonstate.PoolNameReserve,
					TopologyAwareAssignments: map[int]machine.CPUSet{
						0: machine.MustParse("0"),
						1: machine.MustParse("24"),
					},
				},
			},
			nodeEnableReclaim: true,
			wantInternalCalculationResult: types.InternalCPUCalculationResult{
				PoolEntries: map[string]map[int]int{
					commonstate.PoolNameReserve: {-1: 2},
					commonstate.PoolNameReclaim: {-1: 94},
				},
			},
			headroomPolicies: map[configapi.QoSRegionType][]types.CPUHeadroomPolicyName{
				configapi.QoSRegionTypeDedicatedNumaExclusive: {types.CPUHeadroomPolicyNone},
			},
			wantHeadroom:    resource.Quantity{},
			wantHeadroomErr: false,
		},
		{
			name: "provision:single_small_share_pool",
			pools: map[string]*types.PoolInfo{
				commonstate.PoolNameReserve: {
					PoolName: commonstate.PoolNameReserve,
					TopologyAwareAssignments: map[int]machine.CPUSet{
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
				},
			},
			containers: []*types.ContainerInfo{
				makeContainerInfo("uid1", "default", "pod1", "c1", consts.PodAnnotationQoSLevelSharedCores, commonstate.PoolNameShare, nil,
					map[int]machine.CPUSet{
						0: machine.MustParse("1"),
						1: machine.MustParse("25"),
					}, 4),
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
			nodeEnableReclaim: true,
			wantInternalCalculationResult: types.InternalCPUCalculationResult{
				PoolEntries: map[string]map[int]int{
					commonstate.PoolNameReserve: {-1: 2},
					commonstate.PoolNameShare:   {-1: 8},
					commonstate.PoolNameReclaim: {-1: 86},
				},
			},
			wantHeadroom: resource.Quantity{},
		},
		{
			name: "provision:single_large_share_pool",
			pools: map[string]*types.PoolInfo{
				commonstate.PoolNameReserve: {
					PoolName: commonstate.PoolNameReserve,
					TopologyAwareAssignments: map[int]machine.CPUSet{
						0: machine.MustParse("0"),
						1: machine.MustParse("24"),
					},
				},
				commonstate.PoolNameShare: {
					PoolName: commonstate.PoolNameShare,
					TopologyAwareAssignments: map[int]machine.CPUSet{
						0: machine.MustParse("1-23,48-71"),
						1: machine.MustParse("25-47,72-95"),
					},
				},
			},
			containers: []*types.ContainerInfo{
				makeContainerInfo("uid1", "default", "pod1", "c1", consts.PodAnnotationQoSLevelSharedCores, commonstate.PoolNameShare, nil,
					map[int]machine.CPUSet{
						0: machine.MustParse("1-22,48-70"),
						1: machine.MustParse("25-46,72-94"),
					}, 100),
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
			nodeEnableReclaim: true,
			wantInternalCalculationResult: types.InternalCPUCalculationResult{
				PoolEntries: map[string]map[int]int{
					commonstate.PoolNameReserve: {-1: 2},
					commonstate.PoolNameShare:   {-1: 90},
					commonstate.PoolNameReclaim: {-1: 4},
				},
			},
			wantHeadroom: resource.Quantity{},
		},
		{
			name: "provision:multi_small_share_pools",
			pools: map[string]*types.PoolInfo{
				commonstate.PoolNameReserve: {
					PoolName: commonstate.PoolNameReserve,
					TopologyAwareAssignments: map[int]machine.CPUSet{
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
				},
				"batch": {
					PoolName: "batch",
					TopologyAwareAssignments: map[int]machine.CPUSet{
						0: machine.MustParse("2"),
						1: machine.MustParse("26"),
					},
				},
			},
			containers: []*types.ContainerInfo{
				makeContainerInfo("uid1", "default", "pod1", "c1", consts.PodAnnotationQoSLevelSharedCores, commonstate.PoolNameShare, nil,
					map[int]machine.CPUSet{
						0: machine.MustParse("1"),
						1: machine.MustParse("25"),
					}, 4),
				makeContainerInfo("uid2", "default", "pod2", "c2", consts.PodAnnotationQoSLevelSharedCores, "batch", nil,
					map[int]machine.CPUSet{
						0: machine.MustParse("2"),
						1: machine.MustParse("26"),
					}, 6),
			},
			pods: []*v1.Pod{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "pod1",
						Namespace: "default",
						UID:       "uid1",
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "pod2",
						Namespace: "default",
						UID:       "uid2",
					},
				},
			},
			nodeEnableReclaim: true,
			wantInternalCalculationResult: types.InternalCPUCalculationResult{
				PoolEntries: map[string]map[int]int{
					commonstate.PoolNameReserve: {-1: 2},
					commonstate.PoolNameShare:   {-1: 6},
					"batch":                     {-1: 8},
					commonstate.PoolNameReclaim: {-1: 80},
				},
			},
			wantHeadroom: resource.Quantity{},
		},
		{
			name: "provision:multi_large_share_pools",
			pools: map[string]*types.PoolInfo{
				commonstate.PoolNameReserve: {
					PoolName: commonstate.PoolNameReserve,
					TopologyAwareAssignments: map[int]machine.CPUSet{
						0: machine.MustParse("0"),
						1: machine.MustParse("24"),
					},
				},
				commonstate.PoolNameShare: {
					PoolName: commonstate.PoolNameShare,
					TopologyAwareAssignments: map[int]machine.CPUSet{
						0: machine.MustParse("1-5,48-52"),
						1: machine.MustParse("25-29,72-76"),
					},
				},
				"batch": {
					PoolName: "batch",
					TopologyAwareAssignments: map[int]machine.CPUSet{
						0: machine.MustParse("1-12,48-60"),
						1: machine.MustParse("25-36,72-84"),
					},
				},
			},
			containers: []*types.ContainerInfo{
				makeContainerInfo("uid1", "default", "pod1", "c1", consts.PodAnnotationQoSLevelSharedCores, commonstate.PoolNameShare, nil,
					map[int]machine.CPUSet{
						0: machine.MustParse("1-5,48-52"),
						1: machine.MustParse("25-29,72-76"),
					}, 100),
				makeContainerInfo("uid2", "default", "pod2", "c2", consts.PodAnnotationQoSLevelSharedCores, "batch", nil,
					map[int]machine.CPUSet{
						0: machine.MustParse("1-12,48-60"),
						1: machine.MustParse("25-36,72-84"),
					}, 200),
			},
			pods: []*v1.Pod{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "pod1",
						Namespace: "default",
						UID:       "uid1",
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "pod2",
						Namespace: "default",
						UID:       "uid2",
					},
				},
			},
			nodeEnableReclaim: true,
			wantInternalCalculationResult: types.InternalCPUCalculationResult{
				PoolEntries: map[string]map[int]int{
					commonstate.PoolNameReserve: {-1: 2},
					commonstate.PoolNameShare:   {-1: 30},
					"batch":                     {-1: 60},
					commonstate.PoolNameReclaim: {-1: 4},
				},
			},
			wantHeadroom: resource.Quantity{},
		},
		{
			name: "provision:single_dedicated_numa_exclusive",
			pools: map[string]*types.PoolInfo{
				commonstate.PoolNameReserve: {
					PoolName: commonstate.PoolNameReserve,
					TopologyAwareAssignments: map[int]machine.CPUSet{
						0: machine.MustParse("0"),
						1: machine.MustParse("24"),
					},
				},
				commonstate.PoolNameReclaim: {
					PoolName: commonstate.PoolNameReclaim,
					TopologyAwareAssignments: map[int]machine.CPUSet{
						0: machine.MustParse("70-71"),
						1: machine.MustParse("25-46"),
					},
				},
			},
			containers: []*types.ContainerInfo{
				makeContainerInfo("uid1", "default", "pod1", "c1", consts.PodAnnotationQoSLevelDedicatedCores, commonstate.PoolNameDedicated,
					map[string]string{consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable},
					map[int]machine.CPUSet{
						0: machine.MustParse("1-23,48-71"),
					}, 36),
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
			nodeEnableReclaim: true,
			headroomAssembler: types.CPUHeadroomAssemblerDedicated,
			wantInternalCalculationResult: types.InternalCPUCalculationResult{
				PoolEntries: map[string]map[int]int{
					commonstate.PoolNameReserve: {
						-1: 2,
					},
					commonstate.PoolNameReclaim: {
						0:  4,
						-1: 47,
					},
				},
			},
			// dedicated_cores headroom(9) + empty numa headroom(45)
			wantHeadroom: *resource.NewQuantity(54, resource.DecimalSI),
		},
		{
			name: "provision:single_dedicated_numa_exclusive with invalid headroom policy",
			pools: map[string]*types.PoolInfo{
				commonstate.PoolNameReserve: {
					PoolName: commonstate.PoolNameReserve,
					TopologyAwareAssignments: map[int]machine.CPUSet{
						0: machine.MustParse("0"),
						1: machine.MustParse("24"),
					},
				},
				commonstate.PoolNameReclaim: {
					PoolName: commonstate.PoolNameReclaim,
					TopologyAwareAssignments: map[int]machine.CPUSet{
						0: machine.MustParse("70-71"),
						1: machine.MustParse("25-46"),
					},
				},
			},
			containers: []*types.ContainerInfo{
				makeContainerInfo("uid1", "default", "pod1", "c1", consts.PodAnnotationQoSLevelDedicatedCores, commonstate.PoolNameDedicated,
					map[string]string{consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable},
					map[int]machine.CPUSet{
						0: machine.MustParse("1-23,48-71"),
					}, 36),
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
			nodeEnableReclaim: true,
			headroomAssembler: types.CPUHeadroomAssemblerDedicated,
			headroomPolicies: map[configapi.QoSRegionType][]types.CPUHeadroomPolicyName{
				configapi.QoSRegionTypeDedicatedNumaExclusive: {types.CPUHeadroomPolicyNonReclaim},
			},
			wantInternalCalculationResult: types.InternalCPUCalculationResult{
				PoolEntries: map[string]map[int]int{
					commonstate.PoolNameReserve: {
						-1: 2,
					},
					commonstate.PoolNameReclaim: {
						0:  4,
						-1: 47,
					},
				},
			},
			wantHeadroomErr: true,
			// dedicated_cores headroom(9) + empty numa headroom(45)
			wantHeadroom: *resource.NewQuantity(54, resource.DecimalSI),
		},
		{
			name: "single_dedicated_numa_exclusive pod un-reclaimed",
			pools: map[string]*types.PoolInfo{
				commonstate.PoolNameReserve: {
					PoolName: commonstate.PoolNameReserve,
					TopologyAwareAssignments: map[int]machine.CPUSet{
						0: machine.MustParse("0"),
						1: machine.MustParse("24"),
					},
				},
				commonstate.PoolNameReclaim: {
					PoolName: commonstate.PoolNameReclaim,
					TopologyAwareAssignments: map[int]machine.CPUSet{
						0: machine.MustParse("70-71"),
						1: machine.MustParse("25-47,72-95"),
					},
				},
			},
			containers: []*types.ContainerInfo{
				makeContainerInfo("uid1", "default", "pod1", "c1", consts.PodAnnotationQoSLevelDedicatedCores, commonstate.PoolNameDedicated,
					map[string]string{consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable},
					map[int]machine.CPUSet{
						0: machine.MustParse("1-23,48-71"),
					}, 36),
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
			nodeEnableReclaim: true,
			podProfiles:       map[k8stypes.UID]spd.DummyPodServiceProfile{"uid1": {PerformanceLevel: spd.PerformanceLevelPoor, Score: 0}},
			headroomAssembler: types.CPUHeadroomAssemblerDedicated,
			headroomPolicies: map[configapi.QoSRegionType][]types.CPUHeadroomPolicyName{
				configapi.QoSRegionTypeShare:                  {types.CPUHeadroomPolicyCanonical},
				configapi.QoSRegionTypeDedicatedNumaExclusive: {types.CPUHeadroomPolicyNUMAExclusive},
			},
			wantInternalCalculationResult: types.InternalCPUCalculationResult{
				PoolEntries: map[string]map[int]int{
					commonstate.PoolNameReserve: {
						-1: 2,
					},
					commonstate.PoolNameReclaim: {
						0:  2,
						-1: 47,
					},
				},
			},
			wantHeadroom: *resource.NewQuantity(45, resource.DecimalSI),
		},
		{
			name: "single_dedicated_numa_exclusive pod with performance score",
			pools: map[string]*types.PoolInfo{
				commonstate.PoolNameReserve: {
					PoolName: commonstate.PoolNameReserve,
					TopologyAwareAssignments: map[int]machine.CPUSet{
						0: machine.MustParse("0"),
						1: machine.MustParse("24"),
					},
				},
				commonstate.PoolNameReclaim: {
					PoolName: commonstate.PoolNameReclaim,
					TopologyAwareAssignments: map[int]machine.CPUSet{
						0: machine.MustParse("70-71"),
						1: machine.MustParse("25-47,72-95"),
					},
				},
			},
			containers: []*types.ContainerInfo{
				makeContainerInfo("uid1", "default", "pod1", "c1", consts.PodAnnotationQoSLevelDedicatedCores, commonstate.PoolNameDedicated,
					map[string]string{consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable},
					map[int]machine.CPUSet{
						0: machine.MustParse("1-23,48-71"),
					}, 36),
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
			nodeEnableReclaim: true,
			podProfiles:       map[k8stypes.UID]spd.DummyPodServiceProfile{"uid1": {PerformanceLevel: spd.PerformanceLevelPerfect, Score: 50}},
			headroomAssembler: types.CPUHeadroomAssemblerDedicated,
			headroomPolicies: map[configapi.QoSRegionType][]types.CPUHeadroomPolicyName{
				configapi.QoSRegionTypeShare:                  {types.CPUHeadroomPolicyCanonical},
				configapi.QoSRegionTypeDedicatedNumaExclusive: {types.CPUHeadroomPolicyNUMAExclusive},
			},
			wantInternalCalculationResult: types.InternalCPUCalculationResult{
				PoolEntries: map[string]map[int]int{
					commonstate.PoolNameReserve: {
						-1: 2,
					},
					commonstate.PoolNameReclaim: {
						0:  4,
						-1: 47,
					},
				},
			},
			wantHeadroom: *resource.NewQuantity(49, resource.DecimalSI),
		},
		{
			name: "dedicated_numa_exclusive_&_share",
			pools: map[string]*types.PoolInfo{
				commonstate.PoolNameReserve: {
					PoolName: commonstate.PoolNameReserve,
					TopologyAwareAssignments: map[int]machine.CPUSet{
						0: machine.MustParse("0"),
						1: machine.MustParse("24"),
					},
				},
				commonstate.PoolNameShare: {
					PoolName: commonstate.PoolNameShare,
					TopologyAwareAssignments: map[int]machine.CPUSet{
						1: machine.MustParse("25-30"),
					},
				},
				commonstate.PoolNameReclaim: {
					PoolName: commonstate.PoolNameReclaim,
					TopologyAwareAssignments: map[int]machine.CPUSet{
						0: machine.MustParse("70-71"),
						1: machine.MustParse("31-47,72-95"),
					},
				},
			},
			containers: []*types.ContainerInfo{
				makeContainerInfo("uid1", "default", "pod1", "c1", consts.PodAnnotationQoSLevelDedicatedCores, commonstate.PoolNameDedicated,
					map[string]string{consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable},
					map[int]machine.CPUSet{
						0: machine.MustParse("1-23,48-71"),
					}, 36),
				makeContainerInfo("uid2", "default", "pod2", "c2", consts.PodAnnotationQoSLevelSharedCores, commonstate.PoolNameShare, nil,
					map[int]machine.CPUSet{
						1: machine.MustParse("25-28"),
					}, 4),
			},
			pods: []*v1.Pod{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "pod1",
						Namespace: "default",
						UID:       "uid1",
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "pod2",
						Namespace: "default",
						UID:       "uid2",
					},
				},
			},
			nodeEnableReclaim: true,
			wantInternalCalculationResult: types.InternalCPUCalculationResult{
				PoolEntries: map[string]map[int]int{
					commonstate.PoolNameReserve: {
						-1: 2,
					},
					commonstate.PoolNameShare: {
						-1: 6,
					},
					commonstate.PoolNameReclaim: {
						0:  4,
						-1: 41,
					},
				},
			},
			wantHeadroom: *resource.NewQuantity(50, resource.DecimalSI), // 41 + 9
		},
		{
			name: "dedicated_numa_exclusive_&_share_disable_reclaim",
			pools: map[string]*types.PoolInfo{
				commonstate.PoolNameReserve: {
					PoolName: commonstate.PoolNameReserve,
					TopologyAwareAssignments: map[int]machine.CPUSet{
						0: machine.MustParse("0"),
						1: machine.MustParse("24"),
					},
				},
				commonstate.PoolNameShare: {
					PoolName: commonstate.PoolNameShare,
					TopologyAwareAssignments: map[int]machine.CPUSet{
						1: machine.MustParse("25-30"),
					},
				},
				commonstate.PoolNameReclaim: {
					PoolName: commonstate.PoolNameReclaim,
					TopologyAwareAssignments: map[int]machine.CPUSet{
						0: machine.MustParse("70-71"),
						1: machine.MustParse("31-47,72-95"),
					},
				},
			},
			containers: []*types.ContainerInfo{
				makeContainerInfo("uid1", "default", "pod1", "c1", consts.PodAnnotationQoSLevelDedicatedCores, commonstate.PoolNameDedicated,
					map[string]string{consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable},
					map[int]machine.CPUSet{
						0: machine.MustParse("1-23,48-71"),
					}, 36),
				makeContainerInfo("uid2", "default", "pod2", "c2", consts.PodAnnotationQoSLevelSharedCores, commonstate.PoolNameShare, nil,
					map[int]machine.CPUSet{
						1: machine.MustParse("25-28"),
					}, 4),
			},
			pods: []*v1.Pod{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "pod1",
						Namespace: "default",
						UID:       "uid1",
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "pod2",
						Namespace: "default",
						UID:       "uid2",
					},
				},
			},
			nodeEnableReclaim: false,
			wantInternalCalculationResult: types.InternalCPUCalculationResult{
				PoolEntries: map[string]map[int]int{
					commonstate.PoolNameReserve: {
						-1: 2,
					},
					commonstate.PoolNameShare: {
						-1: 45,
					},
					commonstate.PoolNameReclaim: {
						0:  2,
						-1: 2,
					},
				},
			},
			wantHeadroom: *resource.NewQuantity(0, resource.DecimalSI),
		},
		{
			name:      "provision:single_large_share_pool&isolation_within_limits",
			preUpdate: true,
			pools: map[string]*types.PoolInfo{
				commonstate.PoolNameReserve: {
					PoolName: commonstate.PoolNameReserve,
					TopologyAwareAssignments: map[int]machine.CPUSet{
						0: machine.MustParse("0"),
						1: machine.MustParse("24"),
					},
				},
				commonstate.PoolNameShare: {
					PoolName: commonstate.PoolNameShare,
					TopologyAwareAssignments: map[int]machine.CPUSet{
						0: machine.MustParse("1-21,48-69"),
						1: machine.MustParse("25-45,72-93"),
					},
				},
				commonstate.PoolNameReclaim: {
					PoolName: commonstate.PoolNameReclaim,
					TopologyAwareAssignments: map[int]machine.CPUSet{
						0: machine.MustParse("22-23,70-71"),
						1: machine.MustParse("46-47,94-95"),
					},
				},
			},
			containers: []*types.ContainerInfo{
				makeContainerInfo("uid1", "default", "pod1", "c1", consts.PodAnnotationQoSLevelSharedCores, commonstate.PoolNameShare, nil,
					map[int]machine.CPUSet{
						0: machine.MustParse("1-22,48-70"),
						1: machine.MustParse("25-46,72-94"),
					}, 2),
				makeContainerInfo("uid2", "default", "pod2", "c2", consts.PodAnnotationQoSLevelSharedCores, commonstate.PoolNameShare, nil,
					map[int]machine.CPUSet{
						0: machine.MustParse("1-22,48-70"),
						1: machine.MustParse("25-46,72-94"),
					}, 20),
				makeContainerInfo("uid3", "default", "pod3", "c3", consts.PodAnnotationQoSLevelSharedCores, commonstate.PoolNameShare, nil,
					map[int]machine.CPUSet{
						0: machine.MustParse("1-22,48-70"),
						1: machine.MustParse("25-46,72-94"),
					}, 30),
			},
			pods: []*v1.Pod{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "pod1",
						Namespace: "default",
						UID:       "uid1",
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "pod2",
						Namespace: "default",
						UID:       "uid2",
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "pod3",
						Namespace: "default",
						UID:       "uid3",
					},
				},
			},
			nodeEnableReclaim: true,
			wantInternalCalculationResult: types.InternalCPUCalculationResult{
				PoolEntries: map[string]map[int]int{
					commonstate.PoolNameReserve: {-1: 2},
					commonstate.PoolNameShare:   {-1: 84},
					commonstate.PoolNameReclaim: {-1: 8},
					"isolation-pod1":            {-1: 2},
				},
			},
			wantHeadroom: resource.Quantity{},
			containerMetrics: []containerMetricItem{
				{
					pod:       "uid1",
					container: "c1",
					value:     50,
				},
				{
					pod:       "uid2",
					container: "c2",
					value:     8,
				},
				{
					pod:       "uid3",
					container: "c3",
					value:     4,
				},
			},
			numaMetricItems: []numaMetricItem{
				{
					numaID: 0,
					name:   pkgconsts.MetricMemLatencyReadNuma,
					value:  80,
				},
				{
					numaID: 1,
					name:   pkgconsts.MetricMemLatencyReadNuma,
					value:  80,
				},
				{
					numaID: 0,
					name:   pkgconsts.MetricMemLatencyWriteNuma,
					value:  200,
				},
				{
					numaID: 1,
					name:   pkgconsts.MetricMemLatencyWriteNuma,
					value:  200,
				},
				{
					numaID: 0,
					name:   pkgconsts.MetricMemAMDL3MissLatencyNuma,
					value:  400,
				},
				{
					numaID: 1,
					name:   pkgconsts.MetricMemAMDL3MissLatencyNuma,
					value:  400,
				},
			},
		},
		{
			name:      "provision:single_large_share_pool&isolation_exceed_limit",
			preUpdate: true,
			pools: map[string]*types.PoolInfo{
				commonstate.PoolNameReserve: {
					PoolName: commonstate.PoolNameReserve,
					TopologyAwareAssignments: map[int]machine.CPUSet{
						0: machine.MustParse("0"),
						1: machine.MustParse("24"),
					},
				},
				commonstate.PoolNameShare: {
					PoolName: commonstate.PoolNameShare,
					TopologyAwareAssignments: map[int]machine.CPUSet{
						0: machine.MustParse("1-23,48-71"),
						1: machine.MustParse("25-47,72-95"),
					},
				},
			},
			containers: []*types.ContainerInfo{
				makeContainerInfo("uid1", "default", "pod1", "c1", consts.PodAnnotationQoSLevelSharedCores, commonstate.PoolNameShare, nil,
					map[int]machine.CPUSet{
						0: machine.MustParse("1-22,48-70"),
						1: machine.MustParse("25-46,72-94"),
					}, 8),
				makeContainerInfo("uid2", "default", "pod2", "c2", consts.PodAnnotationQoSLevelSharedCores, commonstate.PoolNameShare, nil,
					map[int]machine.CPUSet{
						0: machine.MustParse("1-22,48-70"),
						1: machine.MustParse("25-46,72-94"),
					}, 20),
				makeContainerInfo("uid3", "default", "pod3", "c3", consts.PodAnnotationQoSLevelSharedCores, commonstate.PoolNameShare, nil,
					map[int]machine.CPUSet{
						0: machine.MustParse("1-22,48-70"),
						1: machine.MustParse("25-46,72-94"),
					}, 30),
				makeContainerInfo("uid4", "default", "pod4", "c4", consts.PodAnnotationQoSLevelSharedCores, commonstate.PoolNameShare, nil,
					map[int]machine.CPUSet{
						0: machine.MustParse("1-22,48-70"),
						1: machine.MustParse("25-46,72-94"),
					}, 2, 8),
			},
			pods: []*v1.Pod{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "pod1",
						Namespace: "default",
						UID:       "uid1",
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "pod2",
						Namespace: "default",
						UID:       "uid2",
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "pod3",
						Namespace: "default",
						UID:       "uid3",
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "pod4",
						Namespace: "default",
						UID:       "uid4",
					},
				},
			},
			nodeEnableReclaim: true,
			wantInternalCalculationResult: types.InternalCPUCalculationResult{
				PoolEntries: map[string]map[int]int{
					commonstate.PoolNameReserve: {-1: 2},
					commonstate.PoolNameShare:   {-1: 90},
					commonstate.PoolNameReclaim: {-1: 4},
				},
			},
			wantHeadroom: resource.Quantity{},
			containerMetrics: []containerMetricItem{
				{
					pod:       "uid1",
					container: "c1",
					value:     50,
				},
				{
					pod:       "uid2",
					container: "c2",
					value:     8,
				},
				{
					pod:       "uid3",
					container: "c3",
					value:     4,
				},
				{
					pod:       "uid4",
					container: "c4",
					value:     70,
				},
			},
		},
		{
			name:      "provision:ignore_share_cores_without_request",
			preUpdate: true,
			pools: map[string]*types.PoolInfo{
				commonstate.PoolNameReserve: {
					PoolName: commonstate.PoolNameReserve,
					TopologyAwareAssignments: map[int]machine.CPUSet{
						0: machine.MustParse("0"),
						1: machine.MustParse("24"),
					},
				},
				commonstate.PoolNameShare: {
					PoolName: commonstate.PoolNameShare,
					TopologyAwareAssignments: map[int]machine.CPUSet{
						0: machine.MustParse("1-23,48-71"),
						1: machine.MustParse("25-47,72-95"),
					},
				},
			},
			containers: []*types.ContainerInfo{
				makeContainerInfo("uid1", "default", "pod1", "c1", consts.PodAnnotationQoSLevelSharedCores, commonstate.PoolNameShare, nil,
					map[int]machine.CPUSet{
						0: machine.MustParse("1-22,48-70"),
						1: machine.MustParse("25-46,72-94"),
					}, 8),
				makeContainerInfo("uid2", "default", "pod2", "c2", consts.PodAnnotationQoSLevelSharedCores, commonstate.PoolNameShare, nil,
					map[int]machine.CPUSet{
						0: machine.MustParse("1-22,48-70"),
						1: machine.MustParse("25-46,72-94"),
					}, 20),
				makeContainerInfo("uid3", "default", "pod3", "c3", consts.PodAnnotationQoSLevelSharedCores, commonstate.PoolNameShare, nil,
					map[int]machine.CPUSet{
						0: machine.MustParse("1-22,48-70"),
						1: machine.MustParse("25-46,72-94"),
					}, 30),
				makeContainerInfo("uid4", "default", "pod4", "c4", consts.PodAnnotationQoSLevelSharedCores, commonstate.PoolNameShare, nil,
					map[int]machine.CPUSet{
						0: machine.MustParse("1-22,48-70"),
						1: machine.MustParse("25-46,72-94"),
					}, 2, 8),
				makeContainerInfo("uid5", "default", "pod5", "c5", consts.PodAnnotationQoSLevelSharedCores, "", nil,
					map[int]machine.CPUSet{
						0: machine.MustParse("1-22,48-70"),
						1: machine.MustParse("25-46,72-94"),
					}),
			},
			pods: []*v1.Pod{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "pod1",
						Namespace: "default",
						UID:       "uid1",
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "pod2",
						Namespace: "default",
						UID:       "uid2",
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "pod3",
						Namespace: "default",
						UID:       "uid3",
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "pod4",
						Namespace: "default",
						UID:       "uid4",
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "pod4",
						Namespace: "default",
						UID:       "uid4",
					},
				},
			},
			nodeEnableReclaim: true,
			wantInternalCalculationResult: types.InternalCPUCalculationResult{
				PoolEntries: map[string]map[int]int{
					commonstate.PoolNameReserve: {-1: 2},
					commonstate.PoolNameShare:   {-1: 90},
					commonstate.PoolNameReclaim: {-1: 4},
				},
			},
			wantHeadroom: resource.Quantity{},
			containerMetrics: []containerMetricItem{
				{
					pod:       "uid1",
					container: "c1",
					value:     50,
				},
				{
					pod:       "uid2",
					container: "c2",
					value:     8,
				},
				{
					pod:       "uid3",
					container: "c3",
					value:     4,
				},
				{
					pod:       "uid4",
					container: "c4",
					value:     70,
				},
				{
					pod:       "uid5",
					container: "c5",
					value:     7,
				},
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			now := time.Now()

			ckDir, err := ioutil.TempDir("", "checkpoint-TestAdvisorUpdate")
			require.NoError(t, err)
			defer func() { _ = os.RemoveAll(ckDir) }()

			sfDir, err := ioutil.TempDir("", "statefile")
			require.NoError(t, err)
			defer func() { _ = os.RemoveAll(sfDir) }()

			mf := metric.NewFakeMetricsFetcher(metrics.DummyMetrics{}).(*metric.FakeMetricsFetcher)

			conf := generateTestConfiguration(t, ckDir, sfDir)
			if tt.headroomAssembler != "" {
				conf.CPUAdvisorConfiguration.HeadroomAssembler = tt.headroomAssembler
			}
			if len(tt.headroomPolicies) != 0 {
				conf.CPUAdvisorConfiguration.HeadroomPolicies = tt.headroomPolicies
			}
			conf.IsolatedMaxResourceRatio = 0.3
			conf.IsolationLockInThreshold = 1
			conf.IsolationLockOutPeriodSecs = 30
			conf.GetDynamicConfiguration().RegionIndicatorTargetConfiguration = map[configapi.QoSRegionType][]configv1alpha1.IndicatorTargetConfiguration{
				configapi.QoSRegionTypeShare: {
					{
						Name:   workloadapis.ServiceSystemIndicatorNameCPUSchedWait,
						Target: 460,
					},
					{
						Name:   workloadapis.ServiceSystemIndicatorNameCPUUsageRatio,
						Target: 0.8,
					},
					{
						Name:   workloadapis.ServiceSystemIndicatorNameMemoryAccessReadLatency,
						Target: 80,
					},
					{
						Name:   workloadapis.ServiceSystemIndicatorNameMemoryAccessWriteLatency,
						Target: 200,
					},
					{
						Name:   workloadapis.ServiceSystemIndicatorNameMemoryL3MissLatency,
						Target: 400,
					},
				},
				configapi.QoSRegionTypeDedicatedNumaExclusive: {
					{
						Name:   workloadapis.ServiceSystemIndicatorNameCPI,
						Target: 1.4,
					},
				},
			}

			advisor, metaCache := newTestCPUResourceAdvisor(t, tt.pods, conf, mf, tt.podProfiles)
			advisor.startTime = time.Now().Add(-types.StartUpPeriod)
			advisor.conf.GetDynamicConfiguration().EnableReclaim = tt.nodeEnableReclaim

			if len(tt.containerMetrics) > 0 {
				advisor.conf.IsolationDisabled = false
				for _, m := range tt.containerMetrics {
					mf.SetContainerMetric(m.pod, m.container, metric_consts.MetricCPUNrRunnableContainer, utilmetric.MetricData{Value: m.value, Time: &now})
				}
			}

			for _, metric := range tt.numaMetricItems {
				mf.SetNumaMetric(metric.numaID, metric.name, utilmetric.MetricData{Value: metric.value, Time: &now})
			}

			recvChInterface, sendChInterface := advisor.GetChannels()
			recvCh := recvChInterface.(chan types.TriggerInfo)
			sendCh := sendChInterface.(chan types.InternalCPUCalculationResult)

			for poolName, poolInfo := range tt.pools {
				_ = metaCache.SetPoolInfo(poolName, poolInfo)
			}
			for _, c := range tt.containers {
				_ = metaCache.SetContainerInfo(c.PodUID, c.ContainerName, c)
			}

			var wg sync.WaitGroup
			wg.Add(1)
			ctx, cancel := context.WithCancel(context.Background())
			go func() {
				defer wg.Done()
				advisor.Run(ctx)
			}()

			// if preUpdate is enabled, trigger an empty update firstly
			if tt.preUpdate {
				recvCh <- types.TriggerInfo{TimeStamp: time.Now()}
				timeoutTick := time.NewTimer(time.Second * 5)
				select {
				case <-timeoutTick.C:
					t.Errorf("timeout get response")
				case _ = <-sendCh:
				}
			}

			// trigger advisor update
			recvCh <- types.TriggerInfo{TimeStamp: time.Now()}

			// check provision
			if !reflect.DeepEqual(tt.wantInternalCalculationResult, types.InternalCPUCalculationResult{}) {
				timeoutTick := time.NewTimer(time.Second * 5)
				select {
				case <-timeoutTick.C:
					t.Errorf("timeout get response")
				case advisorResp := <-sendCh:
					resp := make(map[string]map[int]int)
					for pool := range advisorResp.PoolEntries {
						if strings.HasPrefix(pool, "isolation") && len(pool) > 15 {
							resp[pool[:14]] = advisorResp.PoolEntries[pool]
						} else {
							resp[pool] = advisorResp.PoolEntries[pool]
						}
					}
					if !reflect.DeepEqual(tt.wantInternalCalculationResult.PoolEntries, resp) {
						t.Errorf("cpu provision\nexpected: %+v,\nactual: %+v", tt.wantInternalCalculationResult, advisorResp)
					}
				}
			}

			// check headroom
			if !reflect.DeepEqual(tt.wantHeadroom, resource.Quantity{}) {
				headroom, _, err := advisor.GetHeadroom()
				if tt.wantHeadroomErr {
					assert.Error(t, err)
				} else {
					assert.NoError(t, err)
					if !reflect.DeepEqual(tt.wantHeadroom.MilliValue(), headroom.MilliValue()) {
						t.Errorf("headroom\nexpected: %+v\nactual: %+v", tt.wantHeadroom, headroom)
					}
				}
			}

			cancel()
			wg.Wait()
		})
	}
}

func TestGetIsolatedContainerRegions(t *testing.T) {
	t.Parallel()

	c1_1 := &types.ContainerInfo{PodUID: "p1", ContainerName: "c1_1"}
	c1_2 := &types.ContainerInfo{PodUID: "p1", ContainerName: "c1_2"}
	c2 := &types.ContainerInfo{PodUID: "p2", ContainerName: "c2"}
	c3_1 := &types.ContainerInfo{PodUID: "p3", ContainerName: "c3_1"}
	c3_2 := &types.ContainerInfo{PodUID: "p3", ContainerName: "c3_2"}

	conf, _ := options.NewOptions().Config()

	r1 := &region.QoSRegionShare{
		QoSRegionBase: region.NewQoSRegionBase("r1", "", configapi.QoSRegionTypeIsolation,
			conf, struct{}{}, false, nil, nil, nil),
	}
	_ = r1.AddContainer(c1_1)
	_ = r1.AddContainer(c1_2)

	r2 := &region.QoSRegionShare{
		QoSRegionBase: region.NewQoSRegionBase("r2", "", configapi.QoSRegionTypeShare,
			conf, struct{}{}, false, nil, nil, nil),
	}
	_ = r2.AddContainer(c2)

	r3 := &region.QoSRegionShare{
		QoSRegionBase: region.NewQoSRegionBase("r3", "", configapi.QoSRegionTypeDedicatedNumaExclusive,
			conf, struct{}{}, false, nil, nil, nil),
	}
	_ = r3.AddContainer(c3_1)
	_ = r3.AddContainer(c3_2)

	advisor := &cpuResourceAdvisor{
		regionMap: map[string]region.QoSRegion{
			"r1": r1,
			"r2": r2,
			"r3": r3,
		},
	}

	f := func(c *types.ContainerInfo) []string {
		rs, err := advisor.getContainerRegions(c, configapi.QoSRegionTypeIsolation)
		assert.NoError(t, err)
		var res []string
		for _, r := range rs {
			res = append(res, r.Name())
		}
		return res
	}
	assert.ElementsMatch(t, []string{"r1"}, f(c1_1))
	assert.ElementsMatch(t, []string{"r1"}, f(c1_2))
	assert.ElementsMatch(t, []string{}, f(c2))
	assert.ElementsMatch(t, []string{}, f(c3_1))
	assert.ElementsMatch(t, []string{}, f(c3_2))
}

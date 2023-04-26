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
	"fmt"
	"io/ioutil"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/kubewharf/katalyst-api/pkg/consts"
	katalyst_base "github.com/kubewharf/katalyst-core/cmd/base"
	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/options"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	qrmstate "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

func generateTestConfiguration(t *testing.T, checkpointDir, stateFileDir string) *config.Configuration {
	conf, err := options.NewOptions().Config()
	require.NoError(t, err)
	require.NotNil(t, conf)

	conf.GenericSysAdvisorConfiguration.StateFileDirectory = stateFileDir
	conf.MetaServerConfiguration.CheckpointManagerDir = checkpointDir

	return conf
}

func newTestCPUResourceAdvisor(t *testing.T, checkpointDir, stateFileDir string) (*cpuResourceAdvisor, metacache.MetaCache) {
	conf := generateTestConfiguration(t, checkpointDir, stateFileDir)

	metaCache, err := metacache.NewMetaCacheImp(conf, metric.NewFakeMetricsFetcher(metrics.DummyMetrics{}))
	require.NoError(t, err)
	require.NotNil(t, metaCache)

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
	}

	cra := NewCPUResourceAdvisor(conf, struct{}{}, metaCache, metaServer, nil)
	assert.NotNil(t, cra)

	return cra, metaCache
}

func makeContainerInfo(podUID, namespace, podName, containerName, qoSLevel, ownerPoolName string, annotations map[string]string,
	topologyAwareAssignments types.TopologyAwareAssignment, cpuRequest float64) *types.ContainerInfo {
	return &types.ContainerInfo{
		PodUID:                           podUID,
		PodNamespace:                     namespace,
		PodName:                          podName,
		ContainerName:                    containerName,
		ContainerIndex:                   0,
		Labels:                           nil,
		Annotations:                      annotations,
		QoSLevel:                         qoSLevel,
		CPURequest:                       cpuRequest,
		MemoryRequest:                    0,
		RampUp:                           false,
		OwnerPoolName:                    ownerPoolName,
		TopologyAwareAssignments:         topologyAwareAssignments,
		OriginalTopologyAwareAssignments: topologyAwareAssignments,
	}
}

func TestUpdate(t *testing.T) {
	tests := []struct {
		name                          string
		pools                         map[string]*types.PoolInfo
		containers                    []*types.ContainerInfo
		reclaimEnabled                bool
		wantInternalCalculationResult InternalCalculationResult
		wantHeadroom                  resource.Quantity
	}{
		{
			name:                          "missing reserve pool",
			pools:                         map[string]*types.PoolInfo{},
			wantInternalCalculationResult: InternalCalculationResult{},
			wantHeadroom:                  resource.Quantity{},
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
			reclaimEnabled: true,
			wantInternalCalculationResult: InternalCalculationResult{
				map[string]map[int]resource.Quantity{
					state.PoolNameReserve: {-1: *resource.NewQuantity(2, resource.DecimalSI)},
					state.PoolNameReclaim: {-1: *resource.NewQuantity(94, resource.DecimalSI)},
				},
			},
			wantHeadroom: resource.MustParse(fmt.Sprintf("%d", 94)),
		},
		{
			name: "small share pool",
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
			reclaimEnabled: true,
			containers: []*types.ContainerInfo{
				makeContainerInfo("uid1", "default", "pod1", "c1", consts.PodAnnotationQoSLevelSharedCores, qrmstate.PoolNameShare, nil,
					map[int]machine.CPUSet{
						0: machine.MustParse("1"),
						1: machine.MustParse("25"),
					}, 4),
			},
			wantInternalCalculationResult: InternalCalculationResult{
				map[string]map[int]resource.Quantity{
					state.PoolNameReserve: {-1: *resource.NewQuantity(2, resource.DecimalSI)},
					state.PoolNameShare:   {-1: *resource.NewQuantity(8, resource.DecimalSI)},
					state.PoolNameReclaim: {-1: *resource.NewQuantity(86, resource.DecimalSI)},
				},
			},
			wantHeadroom: resource.MustParse(fmt.Sprintf("%d", 86)),
		},
		{
			name: "large share pool",
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
						0: machine.MustParse("1-23,48-71"),
						1: machine.MustParse("25-47,72-95"),
					},
					OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
						0: machine.MustParse("1-23,48-71"),
						1: machine.MustParse("25-47,72-95"),
					},
				},
			},
			reclaimEnabled: true,
			containers: []*types.ContainerInfo{
				makeContainerInfo("uid1", "default", "pod1", "c1", consts.PodAnnotationQoSLevelSharedCores, qrmstate.PoolNameShare, nil,
					map[int]machine.CPUSet{
						0: machine.MustParse("1-23,48-71"),
						1: machine.MustParse("25-47,72-95"),
					}, 96),
			},
			wantInternalCalculationResult: InternalCalculationResult{
				map[string]map[int]resource.Quantity{
					state.PoolNameReserve: {-1: *resource.NewQuantity(2, resource.DecimalSI)},
					state.PoolNameShare:   {-1: *resource.NewQuantity(90, resource.DecimalSI)},
					state.PoolNameReclaim: {-1: *resource.NewQuantity(4, resource.DecimalSI)},
				},
			},
			wantHeadroom: resource.MustParse(fmt.Sprintf("%d", 4)),
		},
		{
			name: "dedicated numa exclusive and share",
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
						1: machine.MustParse("25-26,72-73"),
					},
					OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
						1: machine.MustParse("25-26,72-73"),
					},
				},
			},
			reclaimEnabled: true,
			containers: []*types.ContainerInfo{
				makeContainerInfo("uid1", "default", "pod1", "c1", consts.PodAnnotationQoSLevelDedicatedCores, qrmstate.PoolNameDedicated,
					map[string]string{consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable},
					map[int]machine.CPUSet{
						0: machine.MustParse("1-23,48-71"),
					}, 48),
				makeContainerInfo("uid2", "default", "pod2", "c2", consts.PodAnnotationQoSLevelSharedCores, qrmstate.PoolNameShare, nil,
					map[int]machine.CPUSet{
						1: machine.MustParse("25-26,72-73"),
					}, 4),
			},
			wantInternalCalculationResult: InternalCalculationResult{
				map[string]map[int]resource.Quantity{
					state.PoolNameReserve: {
						-1: *resource.NewQuantity(2, resource.DecimalSI),
					},
					state.PoolNameShare: {-1: *resource.NewQuantity(6, resource.DecimalSI)},
					state.PoolNameReclaim: {
						0:  *resource.NewQuantity(2, resource.DecimalSI),
						-1: *resource.NewQuantity(41, resource.DecimalSI),
					},
				},
			},
			wantHeadroom: resource.MustParse(fmt.Sprintf("%d", 43)),
		},
		{
			name: "dedicated numa exclusive and share, reclaim disabled",
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
						1: machine.MustParse("25-26,72-73"),
					},
					OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
						1: machine.MustParse("25-26,72-73"),
					},
				},
			},
			reclaimEnabled: false,
			containers: []*types.ContainerInfo{
				makeContainerInfo("uid1", "default", "pod1", "c1", consts.PodAnnotationQoSLevelDedicatedCores, qrmstate.PoolNameDedicated,
					map[string]string{consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable},
					map[int]machine.CPUSet{
						0: machine.MustParse("1-23,48-71"),
					}, 47),
				makeContainerInfo("uid2", "default", "pod2", "c2", consts.PodAnnotationQoSLevelSharedCores, qrmstate.PoolNameShare, nil,
					map[int]machine.CPUSet{
						1: machine.MustParse("25-26,72-73"),
					}, 6),
			},
			wantInternalCalculationResult: InternalCalculationResult{
				map[string]map[int]resource.Quantity{
					state.PoolNameReserve: {
						-1: *resource.NewQuantity(2, resource.DecimalSI),
					},
					state.PoolNameShare: {-1: *resource.NewQuantity(45, resource.DecimalSI)},
					state.PoolNameReclaim: {
						0:  *resource.NewQuantity(2, resource.DecimalSI),
						-1: *resource.NewQuantity(2, resource.DecimalSI),
					},
				},
			},
			wantHeadroom: resource.MustParse(fmt.Sprintf("%d", 4)),
		},
		{
			name: "dedicated numa exclusive only",
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
				state.PoolNameReclaim: {
					PoolName: state.PoolNameReclaim,
					TopologyAwareAssignments: map[int]machine.CPUSet{
						0: machine.MustParse("3-6"),
						1: machine.MustParse("24-48"),
					},
					OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
						0: machine.MustParse("0"),
						1: machine.MustParse("24"),
					},
				},
			},
			reclaimEnabled: true,
			containers: []*types.ContainerInfo{
				makeContainerInfo("uid1", "default", "pod1", "c1", consts.PodAnnotationQoSLevelDedicatedCores, qrmstate.PoolNameDedicated,
					map[string]string{consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable},
					map[int]machine.CPUSet{
						0: machine.MustParse("1-23,48-71"),
					}, 48),
			},
			wantInternalCalculationResult: InternalCalculationResult{
				map[string]map[int]resource.Quantity{
					state.PoolNameReserve: {
						-1: *resource.NewQuantity(2, resource.DecimalSI),
					},
					state.PoolNameReclaim: {
						0:  *resource.NewQuantity(2, resource.DecimalSI),
						-1: *resource.NewQuantity(47, resource.DecimalSI),
					},
				},
			},
			wantHeadroom: resource.MustParse(fmt.Sprintf("%d", 49)),
		},
		{
			name: "multi large share pool",
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
						0: machine.MustParse("1-23,48-71"),
						1: machine.MustParse("25-47,72-95"),
					},
					OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
						0: machine.MustParse("1-23,48-71"),
						1: machine.MustParse("25-47,72-95"),
					},
				},
				"batch": {
					PoolName: "batch",
					TopologyAwareAssignments: map[int]machine.CPUSet{
						0: machine.MustParse("1-23,48-71"),
						1: machine.MustParse("25-47,72-95"),
					},
					OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
						0: machine.MustParse("1-23,48-71"),
						1: machine.MustParse("25-47,72-95"),
					},
				},
			},
			reclaimEnabled: true,
			containers: []*types.ContainerInfo{
				makeContainerInfo("uid1", "default", "pod1", "c1", consts.PodAnnotationQoSLevelSharedCores, qrmstate.PoolNameShare, nil,
					map[int]machine.CPUSet{
						0: machine.MustParse("1-23,48-71"),
						1: machine.MustParse("25-47,72-95"),
					}, 96),
				makeContainerInfo("uid2", "default", "pod2", "c2", consts.PodAnnotationQoSLevelSharedCores, "batch", nil,
					map[int]machine.CPUSet{
						0: machine.MustParse("1-23,48-71"),
						1: machine.MustParse("25-47,72-95"),
					}, 96),
			},
			wantInternalCalculationResult: InternalCalculationResult{
				map[string]map[int]resource.Quantity{
					state.PoolNameReserve: {-1: *resource.NewQuantity(2, resource.DecimalSI)},
					state.PoolNameShare:   {-1: *resource.NewQuantity(45, resource.DecimalSI)},
					"batch":               {-1: *resource.NewQuantity(45, resource.DecimalSI)},
					state.PoolNameReclaim: {-1: *resource.NewQuantity(4, resource.DecimalSI)},
				},
			},
			wantHeadroom: resource.MustParse(fmt.Sprintf("%d", 4)),
		},
		{
			name: "multi small share pool",
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
				"batch": {
					PoolName: "batch",
					TopologyAwareAssignments: map[int]machine.CPUSet{
						0: machine.MustParse("2"),
						1: machine.MustParse("26"),
					},
					OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
						0: machine.MustParse("2"),
						1: machine.MustParse("26"),
					},
				},
			},
			reclaimEnabled: true,
			containers: []*types.ContainerInfo{
				makeContainerInfo("uid1", "default", "pod1", "c1", consts.PodAnnotationQoSLevelSharedCores, qrmstate.PoolNameShare, nil,
					map[int]machine.CPUSet{
						0: machine.MustParse("1"),
						1: machine.MustParse("25"),
					}, 4),
				makeContainerInfo("uid2", "default", "pod2", "c2", consts.PodAnnotationQoSLevelSharedCores, "batch", nil,
					map[int]machine.CPUSet{
						0: machine.MustParse("2"),
						1: machine.MustParse("26"),
					}, 4),
			},
			wantInternalCalculationResult: InternalCalculationResult{
				map[string]map[int]resource.Quantity{
					state.PoolNameReserve: {-1: *resource.NewQuantity(2, resource.DecimalSI)},
					state.PoolNameShare:   {-1: *resource.NewQuantity(8, resource.DecimalSI)},
					"batch":               {-1: *resource.NewQuantity(8, resource.DecimalSI)},
					state.PoolNameReclaim: {-1: *resource.NewQuantity(78, resource.DecimalSI)},
				},
			},
			wantHeadroom: resource.MustParse(fmt.Sprintf("%d", 78)),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ckDir, err := ioutil.TempDir("", "checkpoint")
			require.NoError(t, err)
			defer os.RemoveAll(ckDir)

			sfDir, err := ioutil.TempDir("", "statefile")
			require.NoError(t, err)
			defer os.RemoveAll(sfDir)

			advisor, metaCache := newTestCPUResourceAdvisor(t, ckDir, sfDir)
			advisor.startTime = time.Now().Add(-types.StartUpPeriod * 2)
			advisor.conf.ReclaimedResourceConfiguration.SetEnableReclaim(tt.reclaimEnabled)

			recvChInterface, sendChInterface := advisor.GetChannels()
			recvCh := recvChInterface.(chan struct{})
			sendCh := sendChInterface.(chan InternalCalculationResult)

			for poolName, poolInfo := range tt.pools {
				err := metaCache.SetPoolInfo(poolName, poolInfo)
				assert.NoError(t, err)
			}
			for _, c := range tt.containers {
				err := metaCache.SetContainerInfo(c.PodUID, c.ContainerName, c)
				assert.NoError(t, err)
			}

			ctx, cancel := context.WithCancel(context.Background())
			go advisor.Run(ctx)

			// Trigger advisor update
			recvCh <- struct{}{}

			if reflect.DeepEqual(tt.wantInternalCalculationResult, InternalCalculationResult{}) {
				time.Sleep(10 * time.Millisecond) // Wait some time because no signal will be sent to channel
				_, err := advisor.GetHeadroom()
				assert.Error(t, err)
			} else {
				// Check provision
				advisorResp := <-sendCh
				if !reflect.DeepEqual(tt.wantInternalCalculationResult, advisorResp) {
					t.Errorf("cpu provision\nexpected: %+v,\nactual: %+v", tt.wantInternalCalculationResult, advisorResp)
				}

				// Check headroom
				headroom, err := advisor.GetHeadroom()
				assert.NoError(t, err)
				if !reflect.DeepEqual(tt.wantHeadroom.MilliValue(), headroom.MilliValue()) {
					t.Errorf("headroom\nexpected: %+v\nactual: %+v", tt.wantHeadroom, headroom)
				}
			}

			cancel()
		})
	}
}

func TestGenShareRegionPools(t *testing.T) {
	tests := []struct {
		name                         string
		originShareRegionRequirement map[string]int
		sharePoolSize                int
		wantShareRegionRequirement   map[string]int
	}{
		{
			name:                         "sharePoolSize < total requirement",
			originShareRegionRequirement: map[string]int{"r1": 2, "r2": 3},
			sharePoolSize:                4,
			wantShareRegionRequirement:   map[string]int{"r1": 2, "r2": 2},
		},
		{
			name:                         "sharePoolSize < total requirement",
			originShareRegionRequirement: map[string]int{"r1": 1, "r2": 5},
			sharePoolSize:                4,
			wantShareRegionRequirement:   map[string]int{"r1": 1, "r2": 3},
		},
		{
			name:                         "sharePoolSize < total requirement",
			originShareRegionRequirement: map[string]int{"r1": 20, "r2": 30},
			sharePoolSize:                10,
			wantShareRegionRequirement:   map[string]int{"r1": 4, "r2": 6},
		},
		{
			name:                         "sharePoolSize == total requirement",
			originShareRegionRequirement: map[string]int{"r1": 2, "r2": 3},
			sharePoolSize:                5,
			wantShareRegionRequirement:   map[string]int{"r1": 2, "r2": 3},
		},
		{
			name:                         "sharePoolSize > total requirement",
			originShareRegionRequirement: map[string]int{"r1": 2, "r2": 3},
			sharePoolSize:                10,
			wantShareRegionRequirement:   map[string]int{"r1": 2, "r2": 3},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pools := genShareRegionPools(tt.originShareRegionRequirement, tt.sharePoolSize)
			assert.Equal(t, tt.wantShareRegionRequirement, pools)
		})
	}
}

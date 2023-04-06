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
	"fmt"
	"io/ioutil"
	"reflect"
	"testing"
	"time"

	info "github.com/google/cadvisor/info/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/options"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	qrmstate "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/qosaware/resource/memory/headroompolicy"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

var (
	qosLevel2PoolName = map[string]string{
		consts.PodAnnotationQoSLevelSharedCores:    qrmstate.PoolNameShare,
		consts.PodAnnotationQoSLevelReclaimedCores: qrmstate.PoolNameReclaim,
		consts.PodAnnotationQoSLevelSystemCores:    qrmstate.PoolNameReserve,
		consts.PodAnnotationQoSLevelDedicatedCores: qrmstate.PoolNameDedicated,
	}
)

func makeContainerInfo(podUID, namespace, podName, containerName, qoSLevel string, annotations map[string]string, topologyAwareAssignments types.TopologyAwareAssignment) *types.ContainerInfo {
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
		MemoryRequest:                    0,
		RampUp:                           false,
		OwnerPoolName:                    qosLevel2PoolName[qoSLevel],
		TopologyAwareAssignments:         topologyAwareAssignments,
		OriginalTopologyAwareAssignments: topologyAwareAssignments,
	}
}

func generateTestConfiguration(t *testing.T) *config.Configuration {
	conf, err := options.NewOptions().Config()
	require.NoError(t, err)
	require.NotNil(t, conf)

	tmpStateDir, err := ioutil.TempDir("", "sys-advisor-test")
	require.NoError(t, err)

	conf.GenericSysAdvisorConfiguration.StateFileDirectory = tmpStateDir
	conf.ReclaimedResourceConfiguration.ReservedResourceForAllocate[v1.ResourceMemory] = resource.MustParse(fmt.Sprintf("%d", 4<<30))

	return conf
}

func newTestMemoryAdvisor(t *testing.T) (*memoryResourceAdvisor, metacache.MetaCache) {
	conf := generateTestConfiguration(t)

	metaCache, err := metacache.NewMetaCacheImp(generateTestConfiguration(t), metric.NewFakeMetricsFetcher(metrics.DummyMetrics{}))
	require.NoError(t, err)
	require.NotNil(t, metaCache)

	metaServer := &metaserver.MetaServer{
		MetaAgent: &agent.MetaAgent{
			KatalystMachineInfo: &machine.KatalystMachineInfo{
				MachineInfo: &info.MachineInfo{
					MemoryCapacity: 1000 << 30,
				},
			},
		},
	}

	mra := NewMemoryResourceAdvisor(conf, struct{}{}, metaCache, metaServer, nil)

	reservedDefault := conf.ReclaimedResourceConfiguration.ReservedResourceForAllocate[v1.ResourceMemory]
	mra.reservedForAllocate = reservedDefault.Value()

	mra.headroomPolicy = headroompolicy.NewPolicyCanonical(conf, struct{}{}, metaCache, nil, nil)

	assert.Equal(t, mra.Name(), memoryResourceAdvisorName)
	assert.Nil(t, mra.GetChannel())

	return mra, metaCache
}

func TestUpdate(t *testing.T) {
	tests := []struct {
		name               string
		pools              map[string]*types.PoolInfo
		containers         []*types.ContainerInfo
		wantGetHeadroomErr bool
		wantHeadroom       resource.Quantity
	}{
		{
			name:               "missing reserve pool",
			pools:              map[string]*types.PoolInfo{},
			wantGetHeadroomErr: true,
			wantHeadroom:       resource.Quantity{},
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
			wantGetHeadroomErr: false,
			wantHeadroom:       *resource.NewQuantity(996<<30, resource.DecimalSI),
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
			containers: []*types.ContainerInfo{
				makeContainerInfo("uid1", "default", "pod1", "c1", consts.PodAnnotationQoSLevelSharedCores, nil,
					map[int]machine.CPUSet{
						0: machine.MustParse("1"),
						1: machine.MustParse("25"),
					}),
			},
			wantGetHeadroomErr: false,
			wantHeadroom:       *resource.NewQuantity(988<<30, resource.DecimalSI),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			advisor, metaCache := newTestMemoryAdvisor(t)
			advisor.startTime = time.Now().Add(-startUpPeriod * 2)

			for poolName, poolInfo := range tt.pools {
				err := metaCache.SetPoolInfo(poolName, poolInfo)
				assert.NoError(t, err)
			}
			for _, c := range tt.containers {
				err := metaCache.SetContainerInfo(c.PodUID, c.ContainerName, c)
				assert.NoError(t, err)
			}

			advisor.headroomPolicy.SetEssentials(float64(advisor.metaServer.MemoryCapacity), float64(advisor.reservedForAllocate))
			advisor.Update()

			headroom, err := advisor.GetHeadroom()
			assert.Equal(t, tt.wantGetHeadroomErr, err != nil)
			if !reflect.DeepEqual(tt.wantHeadroom.MilliValue(), headroom.MilliValue()) {
				t.Errorf("headroom expected: %+v, actual: %+v", tt.wantHeadroom, headroom)
			}
		})
	}
}

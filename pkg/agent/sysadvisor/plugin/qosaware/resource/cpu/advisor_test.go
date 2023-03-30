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
	"fmt"
	"io/ioutil"
	"reflect"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/kubewharf/katalyst-api/pkg/consts"
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

func generateTestConfiguration(t *testing.T) *config.Configuration {
	conf, err := options.NewOptions().Config()
	require.NoError(t, err)
	require.NotNil(t, conf)

	tmpStateDir, err := ioutil.TempDir("", "sys-advisor-test")
	require.NoError(t, err)

	conf.GenericSysAdvisorConfiguration.StateFileDirectory = tmpStateDir
	conf.ReclaimedResourceConfiguration.ReservedResourceForAllocate[v1.ResourceCPU] = resource.MustParse("4")

	return conf
}

func newTestCPUResourceAdvisor(t *testing.T) *cpuResourceAdvisor {
	conf := generateTestConfiguration(t)

	metaCache, err := metacache.NewMetaCache(conf, metric.NewFakeMetricsFetcher(metrics.DummyMetrics{}))
	require.NoError(t, err)
	require.NotNil(t, metaCache)

	// numa node0 cpu(s): 0-23,48-71
	// numa node1 cpu(s): 24-47,72-95
	cpuTopology, err := machine.GenerateDummyCPUTopology(96, 2, 2)
	assert.NoError(t, err)

	metaServer := &metaserver.MetaServer{
		MetaAgent: &agent.MetaAgent{
			KatalystMachineInfo: &machine.KatalystMachineInfo{
				CPUTopology: cpuTopology,
			},
		},
	}

	cra := NewCPUResourceAdvisor(conf, metaCache, metaServer, nil)
	assert.Equal(t, cra.Name(), cpuResourceAdvisorName)

	return cra
}

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

func TestUpdate(t *testing.T) {
	tests := []struct {
		name               string
		pools              map[string]*types.PoolInfo
		containers         []*types.ContainerInfo
		wantCPUProvision   CPUProvision
		wantGetHeadroomErr bool
		wantHeadroom       resource.Quantity
	}{
		{
			name:               "missing reserve pool",
			pools:              map[string]*types.PoolInfo{},
			wantCPUProvision:   CPUProvision{},
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
			wantCPUProvision: CPUProvision{
				map[string]map[int]resource.Quantity{
					state.PoolNameReserve: {-1: *resource.NewQuantity(2, resource.DecimalSI)},
					state.PoolNameReclaim: {-1: *resource.NewQuantity(94, resource.DecimalSI)},
				},
			},
			wantGetHeadroomErr: false,
			wantHeadroom:       resource.MustParse(fmt.Sprintf("%d", 94)),
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
			containers: []*types.ContainerInfo{
				makeContainerInfo("uid1", "default", "pod1", "c1", consts.PodAnnotationQoSLevelSharedCores, nil,
					map[int]machine.CPUSet{
						0: machine.MustParse("1"),
						1: machine.MustParse("25"),
					}),
			},
			wantCPUProvision: CPUProvision{
				map[string]map[int]resource.Quantity{
					state.PoolNameReserve: {-1: *resource.NewQuantity(2, resource.DecimalSI)},
					state.PoolNameShare:   {-1: *resource.NewQuantity(8, resource.DecimalSI)},
					state.PoolNameReclaim: {-1: *resource.NewQuantity(86, resource.DecimalSI)},
				},
			},
			wantGetHeadroomErr: false,
			wantHeadroom:       resource.MustParse(fmt.Sprintf("%d", 86)),
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
			containers: []*types.ContainerInfo{
				makeContainerInfo("uid1", "default", "pod1", "c1", consts.PodAnnotationQoSLevelSharedCores, nil,
					map[int]machine.CPUSet{
						0: machine.MustParse("1-23,48-71"),
						1: machine.MustParse("25-47,72-95"),
					}),
			},
			wantCPUProvision: CPUProvision{
				map[string]map[int]resource.Quantity{
					state.PoolNameReserve: {-1: *resource.NewQuantity(2, resource.DecimalSI)},
					state.PoolNameShare:   {-1: *resource.NewQuantity(90, resource.DecimalSI)},
					state.PoolNameReclaim: {-1: *resource.NewQuantity(4, resource.DecimalSI)},
				},
			},
			wantGetHeadroomErr: false,
			wantHeadroom:       resource.MustParse(fmt.Sprintf("%d", 4)),
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
			containers: []*types.ContainerInfo{
				makeContainerInfo("uid1", "default", "pod1", "c1", consts.PodAnnotationQoSLevelDedicatedCores,
					map[string]string{consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable},
					map[int]machine.CPUSet{
						0: machine.MustParse("1-23,48-71"),
					}),
				makeContainerInfo("uid2", "default", "pod2", "c2", consts.PodAnnotationQoSLevelSharedCores, nil,
					map[int]machine.CPUSet{
						1: machine.MustParse("25-26,72-73"),
					}),
			},
			wantCPUProvision:   CPUProvision{},
			wantGetHeadroomErr: false,
			wantHeadroom:       resource.MustParse(fmt.Sprintf("%d", 41)),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			advisor := newTestCPUResourceAdvisor(t)
			advisor.startTime = time.Now().Add(-startUpPeriod * 2)
			finishCh := make(chan struct{})

			for poolName, poolInfo := range tt.pools {
				err := advisor.metaCache.SetPoolInfo(poolName, poolInfo)
				assert.NoError(t, err)
			}
			for _, c := range tt.containers {
				err := advisor.metaCache.SetContainerInfo(c.PodUID, c.ContainerName, c)
				assert.NoError(t, err)
			}

			advisorCh := advisor.GetChannel().(chan CPUProvision)

			// Check internal calculation result by another go routine
			go func(ch chan CPUProvision) {
				if reflect.DeepEqual(tt.wantCPUProvision, CPUProvision{}) {
					finishCh <- struct{}{}
					return
				}
				cpuProvision := <-advisorCh
				if !reflect.DeepEqual(tt.wantCPUProvision, cpuProvision) {
					t.Errorf("cpu provision expected: %+v, actual: %+v", tt.wantCPUProvision, cpuProvision)
				}
				finishCh <- struct{}{}
			}(advisorCh)

			advisor.Update()

			// Check headroom
			headroom, err := advisor.GetHeadroom()
			assert.Equal(t, tt.wantGetHeadroomErr, err != nil)
			if err == nil && !reflect.DeepEqual(tt.wantHeadroom.MilliValue(), headroom.MilliValue()) {
				t.Errorf("headroom expected: %+v, actual: %+v", tt.wantHeadroom, headroom)
			}

			<-finishCh
		})
	}
}

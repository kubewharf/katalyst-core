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
	"testing"
	"time"

	info "github.com/google/cadvisor/info/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/pod"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/spd"
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

func newTestMemoryAdvisor(t *testing.T, pods []*v1.Pod, checkpointDir, stateFileDir string) (*memoryResourceAdvisor, metacache.MetaCache) {
	conf := generateTestConfiguration(t, checkpointDir, stateFileDir)

	metaCache, err := metacache.NewMetaCacheImp(conf, metric.NewFakeMetricsFetcher(metrics.DummyMetrics{}))
	require.NoError(t, err)
	require.NotNil(t, metaCache)

	genericCtx, err := katalyst_base.GenerateFakeGenericContext([]runtime.Object{})
	require.NoError(t, err)
	metaServer, err := metaserver.NewMetaServer(genericCtx.Client, metrics.DummyMetrics{}, conf)
	require.NoError(t, err)

	metaServer.MetaAgent = &agent.MetaAgent{
		KatalystMachineInfo: &machine.KatalystMachineInfo{
			MachineInfo: &info.MachineInfo{
				MemoryCapacity: 1000 << 30,
			},
		},
		PodFetcher: &pod.PodFetcherStub{
			PodList: pods,
		},
	}

	err = metaServer.SetServiceProfilingManager(&spd.DummyServiceProfilingManager{})
	assert.NoError(t, err)

	mra := NewMemoryResourceAdvisor(conf, struct{}{}, metaCache, metaServer, nil)
	assert.NotNil(t, mra)

	return mra, metaCache
}

func TestUpdate(t *testing.T) {
	tests := []struct {
		name            string
		pools           map[string]*types.PoolInfo
		containers      []*types.ContainerInfo
		pods            []*v1.Pod
		wantHeadroom    resource.Quantity
		reclaimedEnable bool
	}{
		{
			name:            "missing reserve pool",
			pools:           map[string]*types.PoolInfo{},
			reclaimedEnable: true,
			wantHeadroom:    resource.Quantity{},
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
			wantHeadroom:    *resource.NewQuantity(996<<30, resource.DecimalSI),
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

			advisor, metaCache := newTestMemoryAdvisor(t, tt.pods, ckDir, sfDir)
			advisor.startTime = time.Now().Add(-startUpPeriod * 2)
			advisor.conf.GetDynamicConfiguration().EnableReclaim = tt.reclaimedEnable
			_, _ = advisor.GetChannels()

			for poolName, poolInfo := range tt.pools {
				err := metaCache.SetPoolInfo(poolName, poolInfo)
				assert.NoError(t, err)
			}
			for _, c := range tt.containers {
				err := metaCache.SetContainerInfo(c.PodUID, c.ContainerName, c)
				assert.NoError(t, err)
			}

			ctx, cancel := context.WithCancel(context.Background())
			advisor.Run(ctx)

			time.Sleep(10 * time.Millisecond) // Wait some time because no signal will be sent to channel
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

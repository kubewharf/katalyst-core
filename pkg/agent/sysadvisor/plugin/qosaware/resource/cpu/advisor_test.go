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

	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/options"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/config"
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
	conf.ReclaimedResourceConfiguration.ReservedResourceForAllocate[v1.ResourceCPU] = resource.MustParse("2")

	return conf
}

func newTestCPUResourceAdvisor(t *testing.T) *cpuResourceAdvisor {
	conf := generateTestConfiguration(t)

	metaCache, err := metacache.NewMetaCache(conf, metric.NewFakeMetricsFetcher(metrics.DummyMetrics{}))
	require.NoError(t, err)
	require.NotNil(t, metaCache)

	cra := &cpuResourceAdvisor{
		name:           cpuResourceAdvisorName,
		cpuLimitSystem: 96,
		startTime:      time.Now().Add(-startUpPeriod),
		isReady:        false,
		isInitialized:  false,
		advisorCh:      make(chan CPUProvision),
		metaCache:      metaCache,
		emitter:        nil,
	}

	// New cpu calculator instance
	calculator, err := newCPUCalculator(conf, metaCache, maxRampUpStep, maxRampDownStep, minRampDownPeriod)
	require.NoError(t, err)
	require.NotNil(t, calculator)

	cra.calculator = calculator

	assert.Equal(t, cra.Name(), cpuResourceAdvisorName)

	return cra
}

func TestUpdate(t *testing.T) {
	tests := []struct {
		name               string
		pools              map[string]*types.PoolInfo
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
						1: machine.MustParse("48"),
					},
					OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
						0: machine.MustParse("0"),
						1: machine.MustParse("48"),
					},
				},
			},
			wantCPUProvision: CPUProvision{
				map[string]int{
					state.PoolNameReserve: 2,
					state.PoolNameShare:   4,
					state.PoolNameReclaim: 90,
				},
			},
			wantGetHeadroomErr: false,
			wantHeadroom:       resource.MustParse(fmt.Sprintf("%d", 90)),
		},
		{
			name: "want min share pool",
			pools: map[string]*types.PoolInfo{
				state.PoolNameReserve: {
					PoolName: state.PoolNameReserve,
					TopologyAwareAssignments: map[int]machine.CPUSet{
						0: machine.MustParse("0"),
						1: machine.MustParse("48"),
					},
					OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
						0: machine.MustParse("0"),
						1: machine.MustParse("48"),
					},
				},
				state.PoolNameShare: {
					PoolName: state.PoolNameShare,
					TopologyAwareAssignments: map[int]machine.CPUSet{
						0: machine.MustParse("1"),
						1: machine.MustParse("49"),
					},
					OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
						0: machine.MustParse("1"),
						1: machine.MustParse("49"),
					},
				},
			},
			wantCPUProvision: CPUProvision{
				map[string]int{
					state.PoolNameReserve: 2,
					state.PoolNameShare:   4,
					state.PoolNameReclaim: 90,
				},
			},
			wantGetHeadroomErr: false,
			wantHeadroom:       resource.MustParse(fmt.Sprintf("%d", 90)),
		},
		{
			name: "want max share pool",
			pools: map[string]*types.PoolInfo{
				state.PoolNameReserve: {
					PoolName: state.PoolNameReserve,
					TopologyAwareAssignments: map[int]machine.CPUSet{
						0: machine.MustParse("0"),
						1: machine.MustParse("48"),
					},
					OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
						0: machine.MustParse("0"),
						1: machine.MustParse("48"),
					},
				},
				state.PoolNameShare: {
					PoolName: state.PoolNameShare,
					TopologyAwareAssignments: map[int]machine.CPUSet{
						0: machine.MustParse("1-47"),
						1: machine.MustParse("49-95"),
					},
					OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
						0: machine.MustParse("1-47"),
						1: machine.MustParse("49-95"),
					},
				},
			},
			wantCPUProvision: CPUProvision{
				map[string]int{
					state.PoolNameReserve: 2,
					state.PoolNameShare:   90,
					state.PoolNameReclaim: 4,
				},
			},
			wantGetHeadroomErr: false,
			wantHeadroom:       resource.MustParse(fmt.Sprintf("%d", 4)),
		},
		{
			name: "normal case",
			pools: map[string]*types.PoolInfo{
				state.PoolNameReserve: {
					PoolName: state.PoolNameReserve,
					TopologyAwareAssignments: map[int]machine.CPUSet{
						0: machine.MustParse("0"),
						1: machine.MustParse("48"),
					},
					OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
						0: machine.MustParse("0"),
						1: machine.MustParse("48"),
					},
				},
				state.PoolNameShare: {
					PoolName: state.PoolNameShare,
					TopologyAwareAssignments: map[int]machine.CPUSet{
						0: machine.MustParse("1-10"),
						1: machine.MustParse("49-57"),
					},
					OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
						0: machine.MustParse("1-10"),
						1: machine.MustParse("49-57"),
					},
				},
			},
			wantCPUProvision: CPUProvision{
				map[string]int{
					state.PoolNameReserve: 2,
					state.PoolNameShare:   20,
					state.PoolNameReclaim: 74,
				},
			},
			wantGetHeadroomErr: false,
			wantHeadroom:       resource.MustParse(fmt.Sprintf("%d", 74)),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			advisor := newTestCPUResourceAdvisor(t)
			finishCh := make(chan struct{})

			for poolName, poolInfo := range tt.pools {
				advisor.metaCache.SetPoolInfo(poolName, poolInfo)
			}

			advisorCh := advisor.GetChannel().(chan CPUProvision)

			go func(ch chan CPUProvision) {
				cpuProvision := <-advisorCh
				if !reflect.DeepEqual(tt.wantCPUProvision, cpuProvision) {
					t.Errorf("cpu provision expected: %+v, actual: %+v", tt.wantCPUProvision, cpuProvision)
				}
				finishCh <- struct{}{}
			}(advisorCh)

			advisor.Update()

			headroom, err := advisor.GetHeadroom()
			if err != nil {
				advisorCh <- CPUProvision{}
			}
			assert.Equal(t, tt.wantGetHeadroomErr, err != nil)
			if !reflect.DeepEqual(tt.wantHeadroom, headroom) {
				t.Errorf("headroom expected: %+v, actual: %+v", tt.wantHeadroom, headroom)
			}

			<-finishCh
		})
	}
}

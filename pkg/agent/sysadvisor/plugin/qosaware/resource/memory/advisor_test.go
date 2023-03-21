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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/options"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/qosaware/resource/memory/headroompolicy"
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
	conf.ReclaimedResourceConfiguration.ReservedResourceForAllocate[v1.ResourceMemory] = resource.MustParse(fmt.Sprintf("%d", 2<<30))

	return conf
}

func newTestMemoryAdvisor(t *testing.T) *memoryResourceAdvisor {
	conf := generateTestConfiguration(t)

	metaCache, err := metacache.NewMetaCache(generateTestConfiguration(t), metric.NewFakeMetricsFetcher(metrics.DummyMetrics{}))
	require.NoError(t, err)
	require.NotNil(t, metaCache)

	mra := &memoryResourceAdvisor{
		name:              memoryResourceAdvisorName,
		memoryLimitSystem: 1000 << 30,
		startTime:         time.Now().Add(-startUpPeriod),
		isReady:           false,
		metaCache:         metaCache,
		emitter:           nil,
	}
	reservedDefault := conf.ReclaimedResourceConfiguration.ReservedResourceForAllocate[v1.ResourceMemory]
	mra.reservedForAllocateDefault = reservedDefault.Value()

	policyName := conf.MemoryAdvisorConfiguration.MemoryAdvisorPolicy
	memPolicy, err := headroompolicy.NewPolicy(types.MemoryAdvisorPolicyName(policyName), metaCache, nil)
	require.NoError(t, err)
	require.NotNil(t, memPolicy)

	mra.policy = memPolicy

	assert.Equal(t, mra.Name(), memoryResourceAdvisorName)
	assert.Nil(t, mra.GetChannel())

	return mra
}

func TestUpdate(t *testing.T) {
	tests := []struct {
		name               string
		pools              map[string]*types.PoolInfo
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
			},
			wantGetHeadroomErr: false,
			wantHeadroom:       resource.MustParse("998Gi"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			advisor := newTestMemoryAdvisor(t)

			for poolName, poolInfo := range tt.pools {
				advisor.metaCache.SetPoolInfo(poolName, poolInfo)
			}

			advisor.Update()

			headroom, err := advisor.GetHeadroom()
			assert.Equal(t, tt.wantGetHeadroomErr, err != nil)
			if !reflect.DeepEqual(tt.wantHeadroom.MilliValue(), headroom.MilliValue()) {
				t.Errorf("headroom expected: %+v, actual: %+v", tt.wantHeadroom, headroom)
			}
		})
	}
}

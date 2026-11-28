/*
Copyright 2026 The Katalyst Authors.

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
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

func TestPublicationDomainContract(t *testing.T) {
	t.Parallel()

	conf := generateTestConfiguration(t, t.TempDir(), t.TempDir())
	conf.GenericSysAdvisorConfiguration.EnableShareCoresNumaBinding = true
	d := conf.GetDynamicConfiguration()
	d.EnableReclaim = true
	d.EnableRampUpReclaimHardPartition = true
	d.DisableDedicatedCoresOverlapReclaimedCores = true
	d.AllowSharedCoresOverlapReclaimedCores = false
	d.InitialRampUpReclaimCPUSetRatio = 0.05
	d.ReclaimedCPUMaxRatio = 0.3
	mf := metric.NewFakeMetricsFetcher(metrics.DummyMetrics{}).(*metric.FakeMetricsFetcher)
	cra, cache := newTestCPUResourceAdvisor(t, nil, conf, mf, nil)
	cra.isolator = &staticIsolator{}
	require.NoError(t, cache.SetPoolInfo(commonstate.PoolNameReserve, &types.PoolInfo{PoolName: commonstate.PoolNameReserve, TopologyAwareAssignments: types.TopologyAwareAssignment{}}))
	for n := 0; n < 2; n++ {
		uid := string(rune('a' + n))
		c := makeContainerInfo(uid, "default", uid, "main", consts.PodAnnotationQoSLevelSharedCores, "", map[string]string{consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable}, map[int]machine.CPUSet{n: machine.NewCPUSet()}, 1)
		c.RampUp = true
		require.NoError(t, cache.SetContainerInfo(uid, "main", c))
	}
	result, err := cra.update()
	require.NoError(t, err)
	require.Greater(t, result.CalculationResult.PoolEntries[commonstate.PoolNameReclaim][0].Size, 0)
	require.Greater(t, result.CalculationResult.PoolEntries[commonstate.PoolNameReclaim][1].Size, 0)
	require.Equal(t, 0, result.CalculationResult.PoolEntries[commonstate.PoolNameReclaim][-1].Size, "fake must not claim NUMAs already published as real")
}

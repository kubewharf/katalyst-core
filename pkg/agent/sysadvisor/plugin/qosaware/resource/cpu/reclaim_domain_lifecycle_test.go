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
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	configapi "github.com/kubewharf/katalyst-api/pkg/apis/config/v1alpha1"
	"github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/qosaware/resource/cpu/assembler/provisionassembler"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/qosaware/resource/cpu/cputypes"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

type publicationTraceAssembler struct {
	provisionassembler.ProvisionAssembler
	ctx  provisionassembler.ProvisionContext
	fail bool
}

func (a *publicationTraceAssembler) AssembleProvision(ctx provisionassembler.ProvisionContext) (types.InternalCPUCalculationResult, error) {
	a.ctx = ctx
	r, err := a.ProvisionAssembler.AssembleProvision(ctx)
	if err != nil {
		return r, err
	}
	if a.fail {
		return r, fmt.Errorf("after real assembly")
	}
	return r, nil
}

func domainFixture(t *testing.T) (*cpuResourceAdvisor, metacache.MetaCache, *publicationTraceAssembler) {
	conf := generateTestConfiguration(t, t.TempDir(), t.TempDir())
	conf.EnableShareCoresNumaBinding = true
	conf.IsolationDisabled = true
	conf.ProvisionPolicies[configapi.QoSRegionTypeShare] = []types.CPUProvisionPolicyName{types.CPUProvisionPolicyCanonical}
	conf.RestrictRefPolicy = nil
	conf.PolicyRama.EnableBorwein = false
	conf.HeadroomPolicies[configapi.QoSRegionTypeShare] = nil
	d := conf.GetDynamicConfiguration()
	d.EnableReclaim = true
	d.EnableRampUpReclaimHardPartition = true
	d.DisableDedicatedCoresOverlapReclaimedCores = true
	d.AllowSharedCoresOverlapReclaimedCores = false
	d.InitialRampUpReclaimCPUSetRatio = 0.05
	d.ReclaimedCPUMaxRatio = 0.3
	d.RegionIndicatorTargetConfiguration[configapi.QoSRegionTypeShare] = nil
	d.ReservedResourceForAllocate[v1.ResourceCPU] = resource.MustParse("0")
	mf := metric.NewFakeMetricsFetcher(metrics.DummyMetrics{}).(*metric.FakeMetricsFetcher)
	cra, cache := newTestCPUResourceAdvisor(t, []*v1.Pod{{ObjectMeta: metav1.ObjectMeta{UID: "bound", Name: "bound"}}}, conf, mf, nil)
	cra.isolator = &staticIsolator{}
	require.NoError(t, cache.SetPoolInfo(commonstate.PoolNameReserve, &types.PoolInfo{PoolName: commonstate.PoolNameReserve, TopologyAwareAssignments: types.TopologyAwareAssignment{}}))
	a := &publicationTraceAssembler{ProvisionAssembler: cra.provisionAssembler}
	cra.provisionAssembler = a
	return cra, cache, a
}

func assertDisjointPublication(t *testing.T, result *types.InternalCPUCalculationResult, ctx provisionassembler.ProvisionContext) {
	t.Helper()
	total := 0
	for numa, value := range result.PoolEntries[commonstate.PoolNameReclaim] {
		total += value.Size
		if numa >= 0 && value.Size > 0 {
			require.False(t, ctx.NonBindingNumas.Contains(numa), "positive real must not have fake backing")
		}
	}
	require.Greater(t, total, 0)
	for scope, target := range result.ReclaimConstraintTargets {
		if scope == string(cputypes.NewNonExclusiveReclaimConstraintScope(-1)) {
			require.Equal(t, ctx.NonBindingNumas.ToSliceInt(), target.MemberNUMAs)
		}
	}
}

func TestPublicationDomainLifecycle(t *testing.T) {
	t.Parallel()

	cra, cache, a := domainFixture(t)
	ci := makeContainerInfo("bound", "default", "bound", "main", consts.PodAnnotationQoSLevelSharedCores, "", map[string]string{consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable}, types.TopologyAwareAssignment{1: machine.NewCPUSet()}, 2)
	ci.RampUp = true
	global := makeContainerInfo("global", "default", "global", "main", consts.PodAnnotationQoSLevelSharedCores, "", nil, types.TopologyAwareAssignment{}, 1)
	global.RampUp = true
	require.NoError(t, cache.SetContainerInfo("bound", "main", ci))
	require.NoError(t, cache.SetContainerInfo("global", "main", global))
	for _, phase := range []string{"ramp", "steady", "deleted", "recreated"} {
		switch phase {
		case "steady":
			ci.RampUp = false
			ci.OwnerPoolName = "binding-NUMA1"
			ci.OriginOwnerPoolName = "binding-NUMA1"
			ci.TopologyAwareAssignments = types.TopologyAwareAssignment{1: machine.MustParse("25,73")}
			require.NoError(t, cache.SetContainerInfo("bound", "main", ci))
			require.NoError(t, cache.SetPoolInfo("binding-NUMA1", &types.PoolInfo{PoolName: "binding-NUMA1", TopologyAwareAssignments: ci.TopologyAwareAssignments}))
		case "deleted":
			require.NoError(t, cache.DeleteContainer("bound", "main"))
			require.NoError(t, cache.DeletePool("binding-NUMA1"))
		case "recreated":
			ci.RampUp = true
			ci.OwnerPoolName = ""
			ci.OriginOwnerPoolName = ""
			ci.TopologyAwareAssignments = types.TopologyAwareAssignment{1: machine.NewCPUSet()}
			require.NoError(t, cache.SetContainerInfo("bound", "main", ci))
		}
		advice, err := cra.update()
		require.NoError(t, err)
		assertDisjointPublication(t, advice.CalculationResult, a.ctx)
		if phase == "deleted" {
			require.Equal(t, []int{0, 1}, a.ctx.NonBindingNumas.ToSliceInt())
		} else {
			require.Equal(t, []int{0}, a.ctx.NonBindingNumas.ToSliceInt())
		}
		t.Logf("DOMAIN_PHASE %s backing=%v reclaim=%v", phase, a.ctx.NonBindingNumas, advice.CalculationResult.PoolEntries[commonstate.PoolNameReclaim])
	}
	before := cra.reclaimConstraintGuard.snapshot()
	require.NotEmpty(t, before, "failure oracle must observe actual retained state")
	a.fail = true
	failed, err := cra.update()
	require.ErrorContains(t, err, "after real assembly")
	require.Nil(t, failed)
	require.ElementsMatch(t, before, cra.reclaimConstraintGuard.snapshot())
	a.fail = false
	_, err = cra.UpdateAndGetAdvice(context.Background())
	require.NoError(t, err)
}

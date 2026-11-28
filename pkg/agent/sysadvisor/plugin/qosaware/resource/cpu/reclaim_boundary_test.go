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
	"k8s.io/apimachinery/pkg/util/sets"

	apiconsts "github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	cpuconsts "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/qosaware/resource/cpu/cputypes"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	metaagent "github.com/kubewharf/katalyst-core/pkg/metaserver/agent"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

func TestReclaimBoundaryAdvisorRejectsPartialHint(t *testing.T) {
	t.Parallel()

	cra := &cpuResourceAdvisor{
		metaServer: &metaserver.MetaServer{
			MetaAgent: &metaagent.MetaAgent{
				KatalystMachineInfo: &machine.KatalystMachineInfo{
					CPUTopology: &machine.CPUTopology{CPUDetails: machine.CPUDetails{0: {NUMANodeID: 0}, 1: {NUMANodeID: 1}}},
				},
			},
		},
	}
	for _, hint := range []string{"0,1-2-3", "0,2-1", "0,", "0-1", "0,1"} {
		hint := hint
		t.Run(hint, func(t *testing.T) {
			t.Parallel()
			ci := &types.ContainerInfo{
				PodUID: "pod", ContainerName: "main", QoSLevel: apiconsts.PodAnnotationQoSLevelDedicatedCores,
				Annotations: map[string]string{
					apiconsts.PodAnnotationMemoryEnhancementNumaBinding: apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable,
					cpuconsts.CPUStateAnnotationKeyNUMAHint:             hint,
				},
				TopologyAwareAssignments: types.TopologyAwareAssignment{0: machine.NewCPUSet(0), 1: machine.NewCPUSet(1)},
			}
			domains, err := cra.rampUpReclaimDomainsForContainer(ci)
			if hint == "0-1" || hint == "0,1" {
				require.NoError(t, err)
				require.Equal(t, sets.NewInt(0, 1), domains)
			} else {
				require.Error(t, err)
			}
		})
	}
}

func TestReclaimBoundaryOverlapPublicationAcknowledgesPhysicalSize(t *testing.T) {
	t.Parallel()

	scope := cputypes.NewNonExclusiveReclaimConstraintScope(0)
	members := map[cputypes.ReclaimConstraintScope][]int{scope: {0}}
	cache := metacache.NewDummyMetaCacheImp()
	require.NoError(t, cache.SetPoolInfo(commonstate.PoolNameReclaim, &types.PoolInfo{
		TopologyAwareAssignments: types.TopologyAwareAssignment{0: machine.MustParse("0-7")},
	}))
	cra := &cpuResourceAdvisor{metaCache: cache}
	result := &types.InternalCPUCalculationResult{
		PoolEntries:     map[string]map[int]types.CPUResource{commonstate.PoolNameReclaim: {0: {Size: 2}}},
		PoolOverlapInfo: map[string]map[int]map[string]int{commonstate.PoolNameReclaim: {0: {"share": 2}}},
		PoolOverlapPodContainerInfo: map[string]map[int]map[string]map[string]int{
			commonstate.PoolNameReclaim: {0: {"dedicated": {"main": 4, "sidecar": 4}}},
		},
	}
	published := cra.publishedReclaimByScope(result, members)
	observed, readable := cra.observedReclaimByScope(members)
	require.Equal(t, observed[scope], published[scope], "container aliases must count one physical overlap allocation")
	ceiling := 8
	guard := reclaimConstraintGuard{
		scopeState: map[cputypes.ReclaimConstraintScope]reclaimConstraintScopeState{scope: {
			Ceiling: &ceiling, Desired: 10, Floor: 1, SteadyCap: 8,
			LastPublished: published[scope], HasPublished: true, HasDescriptor: true,
		}},
	}
	decision := guard.decideCeiling(scope, true, observed, readable, 2, 2)
	require.True(t, decision.acked)
	require.Equal(t, 10, *decision.ceiling)
}

func TestReclaimBoundaryDiagnosticsDescribeCommittedCycle(t *testing.T) {
	t.Parallel()

	cra, cache, _ := domainFixture(t)
	ci := makeContainerInfo("bound", "default", "bound", "main", apiconsts.PodAnnotationQoSLevelSharedCores, "",
		map[string]string{apiconsts.PodAnnotationMemoryEnhancementNumaBinding: apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable},
		types.TopologyAwareAssignment{1: machine.NewCPUSet()}, 2)
	ci.RampUp = true
	require.NoError(t, cache.SetContainerInfo("bound", "main", ci))
	emitter := newRecordingMetricEmitter()
	cra.emitter = emitter
	_, err := cra.update()
	require.NoError(t, err)
	scope := string(cputypes.NewNonExclusiveReclaimConstraintScope(1))
	found := false
	for _, sample := range emitter.records {
		if sample.name == metricReclaimScopeLastPublished && sample.tags["scope"] == scope {
			found = true
			require.Equal(t, int64(cra.reclaimConstraintGuard.scopeState[cputypes.ReclaimConstraintScope(scope)].LastPublished), sample.value)
		}
	}
	require.True(t, found, "the first committed cycle must emit its scope diagnostic")
}

func TestReclaimBoundaryDisabledPartitionDrainsRetainedScope(t *testing.T) {
	t.Parallel()

	cra, cache, assembler := domainFixture(t)
	ci := makeContainerInfo("bound", "default", "bound", "main", apiconsts.PodAnnotationQoSLevelSharedCores, "",
		map[string]string{apiconsts.PodAnnotationMemoryEnhancementNumaBinding: apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable},
		types.TopologyAwareAssignment{1: machine.NewCPUSet()}, 2)
	ci.RampUp = true
	require.NoError(t, cache.SetContainerInfo("bound", "main", ci))
	_, err := cra.update()
	require.NoError(t, err)
	scope := cputypes.NewNonExclusiveReclaimConstraintScope(1)
	require.NotEmpty(t, cra.reclaimConstraintGuard.scopeState)
	retained := cra.reclaimConstraintGuard.scopeState[scope]
	ceiling := 2
	retained.Ceiling = &ceiling
	retained.Desired = 12
	retained.SteadyCap = 8
	retained.LastPublished = 2
	retained.HasPublished = true
	retained.HasDescriptor = true
	cra.reclaimConstraintGuard.scopeState[scope] = retained
	cra.reclaimConstraintGuard.targets[scope] = types.ReclaimConstraintTarget{Desired: 12, Floor: 1, SteadyCap: 8, MemberNUMAs: []int{1}}
	cra.conf.GetDynamicConfiguration().EnableRampUpReclaimHardPartition = false
	_, err = cra.update()
	require.NoError(t, err)
	require.Empty(t, assembler.ctx.RampUpReclaimCPUSetCap)
	retained, ok := cra.reclaimConstraintGuard.scopeState[scope]
	require.True(t, ok, "unfinished scope must be retained while draining")
	require.Equal(t, retained.SteadyCap, retained.Desired,
		"disabled hard partition must drain retained scope even while its container remains in ramp-up")
}

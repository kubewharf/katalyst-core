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

package dynamicpolicy

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/util/sets"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/advisorsvc"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	advisorapi "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/cpuadvisor"
	"github.com/kubewharf/katalyst-core/pkg/agent/utilcomponent/featuregatenegotiation/finders/feature_cpu"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

// This synthetic response preserves the consumer contract: two unequal real
// reclaim blocks and an empty fake scope. It is not a live-node quantity replay.
func TestRampBalanceResponseConsumer(t *testing.T) {
	t.Parallel()

	const responseJSON = `{"entries":{"reclaim":{"entries":{"":{"owner_pool_name":"reclaim","calculation_results_by_numas":{"-1":{},"0":{"blocks":[{"result":14,"block_id":"real-0"}]},"1":{"blocks":[{"result":2,"block_id":"real-1"}]}}}}},"reserve":{"entries":{"":{"owner_pool_name":"reserve","calculation_results_by_numas":{"-1":{"blocks":[{"block_id":"reserved"}]}}}}}},"disable_dedicated_cores_overlap_reclaimed_cores":true}`
	var response advisorapi.GetAdviceResponse
	require.NoError(t, json.Unmarshal([]byte(responseJSON), &response))
	topo, err := machine.GenerateDummyCPUTopology(96, 1, 2)
	require.NoError(t, err)
	p, err := getTestDynamicPolicyWithoutInitialization(topo, t.TempDir())
	require.NoError(t, err)
	p.config.reservedCPUs = machine.NewCPUSet()
	p.state.SetPodEntries(rampTestEntries(t, "binding"), false)
	lw := convertGetAdviceResponse(&response)
	fake := lw.Entries[commonstate.PoolNameReclaim].Entries[commonstate.FakedContainerName].CalculationResultsByNumas[commonstate.FakedNUMAID]
	require.NotNil(t, fake)
	for _, block := range fake.Blocks {
		require.Zero(t, block.Result, "no positive fake block may share the two real publication domains")
	}
	features := map[string]*advisorsvc.FeatureGate{
		feature_cpu.NegotiationFeatureGateDedicatedReclaimDisjointPartition: {Name: feature_cpu.NegotiationFeatureGateDedicatedReclaimDisjointPartition},
	}
	plan, err := p.generateBlockCPUSet(lw, features, true)
	require.NoError(t, err)
	require.Equal(t, 14, plan["real-0"].Size())
	require.Equal(t, 2, plan["real-1"].Size())
	require.True(t, plan["real-0"].IsSubsetOf(topo.CPUDetails.CPUsInNUMANodes(0)))
	require.True(t, plan["real-1"].IsSubsetOf(topo.CPUDetails.CPUsInNUMANodes(1)))
	entries := p.state.GetPodEntries()
	before := entries.Clone()
	domains, err := entries.ActiveRampUpDomains(topo)
	require.NoError(t, err)
	conf := p.config.conf.GetDynamicConfiguration()
	exempt, err := p.balanceExemptDomainsByNUMA(entries, domains)
	require.NoError(t, err)
	require.ElementsMatch(t, []int{0, 1}, exempt.UnsortedList())
	for i := 0; i < 64; i++ {
		retried, err := p.balanceExemptDomainsByNUMA(entries, domains)
		require.NoError(t, err)
		require.True(t, exempt.Equal(retried))
		require.NoError(t, p.validateHardPartitionBlockPlanWithDynamicConfig(lw, plan, domains, retried, conf))
	}
	require.Equal(t, before, p.state.GetPodEntries(), "planning and validation must not advance the committed ramp lifecycle")
	// No exception is granted when only the legacy fake domain remains active.
	require.ErrorContains(t,
		p.validateHardPartitionBlockPlanWithDynamicConfig(lw, plan, sets.NewInt(commonstate.FakedNUMAID), nil, conf),
		"imbalanced across physical NUMAs")
}

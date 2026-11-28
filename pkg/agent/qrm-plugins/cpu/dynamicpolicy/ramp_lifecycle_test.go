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
	"testing"

	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/util/sets"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

func TestRampBalanceExemptionLifecycle(t *testing.T) {
	t.Parallel()

	topo, err := machine.GenerateDummyCPUTopology(96, 2, 2)
	require.NoError(t, err)
	p, err := getTestDynamicPolicyWithoutInitialization(topo, t.TempDir())
	require.NoError(t, err)
	entries := rampTestEntries(t, "lifecycle")
	active := sets.NewInt(0, 1, commonstate.FakedNUMAID)
	exempt, err := p.balanceExemptDomainsByNUMA(entries, active)
	require.NoError(t, err)
	require.ElementsMatch(t, []int{0, 1}, exempt.UnsortedList())
	// The existing committed lifecycle, not request count, removes the exception.
	entries["lifecycle-n0"]["main"].RampUp = false
	exempt, err = p.balanceExemptDomainsByNUMA(entries, active)
	require.NoError(t, err)
	require.ElementsMatch(t, []int{1}, exempt.UnsortedList())
	entries["lifecycle-n1"]["main"].RampUp = false
	exempt, err = p.balanceExemptDomainsByNUMA(entries, active)
	require.NoError(t, err)
	require.Empty(t, exempt)
	exempt, err = p.balanceExemptDomainsByNUMA(state.PodEntries{}, active)
	require.NoError(t, err)
	require.Empty(t, exempt)
	exempt, err = p.balanceExemptDomainsByNUMA(rampTestEntries(t, "fresh"), sets.NewInt(commonstate.FakedNUMAID))
	require.NoError(t, err)
	require.Empty(t, exempt)
}

func TestRampBalanceExemptionMissingTopology(t *testing.T) {
	t.Parallel()

	topo, err := machine.GenerateDummyCPUTopology(96, 2, 2)
	require.NoError(t, err)
	p, err := getTestDynamicPolicyWithoutInitialization(topo, t.TempDir())
	require.NoError(t, err)
	p.machine.machineInfo.CPUTopology = nil
	exempt, err := p.balanceExemptDomainsByNUMA(rampTestEntries(t, "missing"), sets.NewInt(0, 1))
	require.Error(t, err)
	require.Empty(t, exempt)
	p.machine.machineInfo = nil
	require.NotPanics(t, func() {
		exempt, err = p.balanceExemptDomainsByNUMA(rampTestEntries(t, "missing"), sets.NewInt(0, 1))
		require.Error(t, err)
		require.Empty(t, exempt)
	})
}

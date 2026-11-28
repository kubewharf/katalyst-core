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
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

// A retry must not change admission for unchanged input, regardless of the
// number of repeated requests within the same committed ramp lifecycle.
func TestRampBalanceExemptionRepeatability(t *testing.T) {
	t.Parallel()

	topo, err := machine.GenerateDummyCPUTopology(96, 2, 2)
	require.NoError(t, err)
	p, err := getTestDynamicPolicyWithoutInitialization(topo, t.TempDir())
	require.NoError(t, err)
	entries := rampTestEntries(t, "same-input")
	active := sets.NewInt(0, 1, commonstate.FakedNUMAID)
	first, err := p.balanceExemptDomainsByNUMA(entries, active)
	require.NoError(t, err)
	require.ElementsMatch(t, []int{0, 1}, first.UnsortedList())
	for attempt := 0; attempt < 64; attempt++ {
		next, err := p.balanceExemptDomainsByNUMA(entries, active)
		require.NoError(t, err)
		require.True(t, first.Equal(next), "identical retry input must not revoke exemption at attempt %d", attempt)
	}
}

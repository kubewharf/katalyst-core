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

package dynamicpolicy

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

func TestTargetDrivenReclaimConfigCombinations(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name           string
		enableReclaim  bool
		hardPartition  bool
		disableOverlap bool
		expectEnabled  bool
	}{
		{"all three true", true, true, true, true},
		{"reclaim off", false, true, true, false},
		{"hard partition off", true, false, true, false},
		{"overlap off", true, true, false, false},
		{"only reclaim", true, false, false, false},
		{"only hard partition", false, true, false, false},
		{"only overlap", false, false, true, false},
		{"all false", false, false, false, false},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			topology, err := machine.GenerateDummyCPUTopology(8, 1, 2)
			require.NoError(t, err)
			p, err := getTestDynamicPolicyWithoutInitialization(topology, t.TempDir())
			require.NoError(t, err)
			p.config.dynamicConfig.GetDynamicConfiguration().EnableReclaim = tc.enableReclaim
			p.config.dynamicConfig.GetDynamicConfiguration().EnableRampUpReclaimHardPartition = tc.hardPartition
			p.state.SetDisableDedicatedCoresOverlapReclaimedCores(tc.disableOverlap, false)
			require.Equal(t, tc.expectEnabled, p.targetDrivenHardReclaimEnabled())
		})
	}
}

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

package provisionassembler

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	metaagent "github.com/kubewharf/katalyst-core/pkg/metaserver/agent"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

// newFloorTestAssembler builds a minimal ProvisionAssemblerCommon wired only
// with the inputs applyDomainScopedLiveReclaimFloor reads: the active ramp-up
// domains, the QRM-committed live reclaim size per NUMA, and a fake NUMA whose
// CPUDetails report a fixed capacity for numaID. The per-cycle inputs live on the
// fixture overrides, which ctx() folds into the ProvisionContext on every call.
func newFloorTestAssembler(rampUpDomains []int, liveByNUMA map[int]int, numaID, numaCap int) *provisionTestFixture {
	cpuDetails := machine.CPUDetails{}
	for cpu := 0; cpu < numaCap; cpu++ {
		cpuDetails[cpu] = machine.CPUTopoInfo{NUMANodeID: numaID}
	}
	metaServer := &metaserver.MetaServer{
		MetaAgent: &metaagent.MetaAgent{
			KatalystMachineInfo: &machine.KatalystMachineInfo{
				CPUTopology: &machine.CPUTopology{CPUDetails: cpuDetails},
			},
		},
	}
	return &provisionTestFixture{
		ProvisionAssemblerCommon: &ProvisionAssemblerCommon{metaServer: metaServer},
		dynamicConf:              dynamic.NewConfiguration(),
		RampUpDomains:            rampUpDomains,
		LiveReclaimByNUMA:        liveByNUMA,
	}
}

// TestApplyDomainScopedLiveReclaimFloor locks in the post-f90202803 semantics:
// a real-NUMA ramp-up domain protects mid-ramp reclaim, a global (FakedNUMAID)
// ramp-up does NOT broadcast its floor to real NUMAs, and the floor is skipped
// when dedicated+live would exceed NUMA capacity.
func TestApplyDomainScopedLiveReclaimFloor(t *testing.T) {
	t.Parallel()

	t.Run("real NUMA ramp-up raises reclaim to the live size", func(t *testing.T) {
		t.Parallel()
		// NUMA 0 hosts an active ramp-up domain and has capacity for
		// totalAllocated(0)+liveSize(10) <= numaCap(16): reclaim is lifted.
		pa := newFloorTestAssembler([]int{0}, map[int]int{0: 10}, 0, 16)
		got := pa.applyDomainScopedLiveReclaimFloor(pa.ctx(), 0, 4, map[string]int{})
		require.Equal(t, 10, got)
	})

	t.Run("global-only ramp-up does not floor a real NUMA", func(t *testing.T) {
		t.Parallel()
		// Only the global/FakedNUMAID domain is ramping. Live reclaim on real
		// NUMA 0 must NOT be protected: global backfill may land elsewhere, so
		// mid-ramp reclaim on NUMA 0 is not guaranteed to stay there.
		pa := newFloorTestAssembler([]int{commonstate.FakedNUMAID}, map[int]int{0: 10}, 0, 16)
		got := pa.applyDomainScopedLiveReclaimFloor(pa.ctx(), 0, 4, map[string]int{})
		require.Equal(t, 4, got)
	})

	t.Run("capacity overflow skips the floor", func(t *testing.T) {
		t.Parallel()
		// Ramp-up on NUMA 0 and a live size of 10, but dedicated already uses
		// 10 of the 12 NUMA cores: totalAllocated(10)+liveSize(10) > numaCap(12),
		// so the floor must not shrink a hard CPURequest.
		pa := newFloorTestAssembler([]int{0}, map[int]int{0: 10}, 0, 12)
		got := pa.applyDomainScopedLiveReclaimFloor(pa.ctx(), 0, 4, map[string]int{"dedicated": 10})
		require.Equal(t, 4, got)
	})

	t.Run("nil live reclaim map leaves size untouched", func(t *testing.T) {
		t.Parallel()
		pa := newFloorTestAssembler([]int{0}, nil, 0, 16)
		got := pa.applyDomainScopedLiveReclaimFloor(pa.ctx(), 0, 4, map[string]int{})
		require.Equal(t, 4, got)
	})
}

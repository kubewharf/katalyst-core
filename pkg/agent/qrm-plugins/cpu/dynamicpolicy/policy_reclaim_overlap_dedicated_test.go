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

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

// TestReviseReclaimPoolOverlapModePreservesCommittedReclaim pins the dc06
// incident contract: when dedicated/reclaim overlap is ALLOWED
// (disableDedicatedOverlap=false) and ordinary dedicated covers the whole
// machine, the committed reclaim identity legitimately lives inside dedicated.
// If the advisor omits its reclaim block, the fallback eligible set must keep
// dedicated CPUs (it must not unconditionally subtract pooledUnionDedicatedCPUSet),
// so the committed, non-empty reclaim pool is frozen verbatim instead of being
// intersected down to empty and tearing the advisor apply down with a
// "no candidate" error.
func TestReviseReclaimPoolOverlapModePreservesCommittedReclaim(t *testing.T) {
	t.Parallel()
	seed, seedAssign := seed180Default()
	p, cleanup := newReclaimFallback180Policy(t, machine.NewCPUSet(), seed, seedAssign)
	defer cleanup()
	prior := seedCommittedPrior180(t, p) // committed 88-89,178-179 (4 CPUs)

	// Overlap mode: dedicated overlap allowed, share overlap not.
	mode := reclaimFallbackMode{enableReclaim: false, allowSharedOverlap: false, disableDedicatedOverlap: false}
	allCPUs := p.machine.machineInfo.CPUDetails.CPUs()

	entries := emptyReclaimEntries()
	// ordinary dedicated union == whole machine.
	require.NoError(t, p.reviseReclaimPool(entries, machine.NewCPUSet(), allCPUs.Clone(), mode),
		"overlap-mode committed reclaim must be frozen/preserved, not re-derived into empty")

	got := entries[commonstate.PoolNameReclaim][commonstate.FakedContainerName].AllocationResult
	require.True(t, got.Equals(prior),
		"committed reclaim identity %s must be preserved verbatim, got %s", prior, got)
	require.False(t, got.IsEmpty(), "successful terminal reclaim state must stay non-empty")
}

// TestReviseReclaimPoolDisjointModeStillExcludesDedicated is the boundary pin:
// when disableDedicatedOverlap=true, the eligible set MUST exclude ordinary
// dedicated even in the advisor-omitted-reclaim fallback. With dedicated covering
// the whole machine and no free CPUs, the fallback must fail closed
// (errReclaimFallbackEmptyRequired) — it must NOT silently freeze a stale committed
// reclaim that now overlaps dedicated ownership. This proves the dedicated
// subtraction is gated on the flag, not removed wholesale.
func TestReviseReclaimPoolDisjointModeStillExcludesDedicated(t *testing.T) {
	t.Parallel()
	seed, seedAssign := seed180Default()
	p, cleanup := newReclaimFallback180Policy(t, machine.NewCPUSet(), seed, seedAssign)
	defer cleanup()
	prior := seedCommittedPrior180(t, p)

	// Disjoint mode: dedicated overlap disabled.
	mode := reclaimFallbackMode{enableReclaim: true, allowSharedOverlap: false, disableDedicatedOverlap: true}
	allCPUs := p.machine.machineInfo.CPUDetails.CPUs()

	entries := emptyReclaimEntries()
	err := p.reviseReclaimPool(entries, machine.NewCPUSet(), allCPUs.Clone(), mode)
	require.ErrorIs(t, err, errReclaimFallbackEmptyRequired,
		"disjoint mode with dedicated owning everything must fail closed, not freeze committed=%s", prior)
}

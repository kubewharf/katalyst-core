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

	configapi "github.com/kubewharf/katalyst-api/pkg/apis/config/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/qosaware/resource/cpu/region"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

// TestNewSnapshotDeepCopiesAdvisorBookkeeping locks in the core isolation contract
// of P3: the Snapshot built at cycle start must be value-semantic. Mutating the
// advisor's live maps (or CPUSet) after NewSnapshot returns must not change the
// snapshot, and mutating the snapshot's maps must not feed back into the advisor
// sources. Region objects themselves are intentionally shared read-only; only the
// map structure is copied.
func TestNewSnapshotDeepCopiesAdvisorBookkeeping(t *testing.T) {
	t.Parallel()

	regionA := NewFakeRegion("a", configapi.QoSRegionTypeShare, "a")
	regionMap := map[string]region.QoSRegion{"a": regionA}
	reserved := map[int]int{0: 2}
	caps := map[int]int{0: 8}
	numaAvailable := map[int]int{0: 16}
	nonBinding := machine.NewCPUSet(0)

	// The ramp-up domain target carries an optional *int ceiling and a []int member
	// NUMA slice; both are nested mutable state the snapshot must own.
	ceiling := 5
	domainScope := NewNonExclusiveReclaimConstraintScope(0)
	domainTargets := map[ReclaimConstraintScope]types.ReclaimConstraintTarget{
		domainScope: {Desired: 8, Floor: 2, SteadyCap: 6, Ceiling: &ceiling, MemberNUMAs: []int{0, 1}},
	}

	snap := NewSnapshot(regionMap, reserved, caps, numaAvailable, nonBinding, false, true, []int{0, 1}, domainTargets)

	// The advisor mutates its own bookkeeping after the snapshot was taken.
	regionMap["b"] = NewFakeRegion("b", configapi.QoSRegionTypeShare, "b")
	reserved[0] = 99
	reserved[1] = 5
	caps[0] = 100
	caps[2] = 7
	numaAvailable[0] = 100
	nonBinding.Add(3)
	mutatedTarget := domainTargets[domainScope]
	mutatedTarget.Desired = 999
	domainTargets[domainScope] = mutatedTarget      // struct value field (map values are not addressable)
	ceiling = 999                                   // nested *int (ceiling is the pointee variable)
	domainTargets[domainScope].MemberNUMAs[0] = 999 // nested []int

	// The snapshot must reflect only the values present at snapshot time.
	require.Len(t, snap.RegionMap, 1, "region-map key additions after snapshot must not leak in")
	_, hasB := snap.RegionMap["b"]
	require.False(t, hasB)
	require.Equal(t, map[int]int{0: 2}, snap.ReservedForReclaim)
	require.Equal(t, map[int]int{0: 8}, snap.RampUpReclaimCPUSetCap)
	require.Equal(t, map[int]int{0: 16}, snap.NUMAAvailable)
	require.Equal(t, machine.NewCPUSet(0), snap.NonBindingNumas,
		"CPUSet additions after snapshot must not leak in (must be cloned, not aliased)")

	// ReclaimDomainTargets: map entry, nested *int and nested []int must all be isolated.
	dt := snap.ReclaimDomainTargets[domainScope]
	require.Equal(t, 8, dt.Desired, "a later mutation of the source target's Desired must not leak in")
	require.NotNil(t, dt.Ceiling)
	require.Equal(t, 5, *dt.Ceiling, "the snapshot must own its own cloned ceiling, not alias the advisor's *int")
	require.Equal(t, []int{0, 1}, dt.MemberNUMAs, "the snapshot must own its own cloned member-NUMA slice")

	// Mutating the snapshot must not feed back into the advisor sources.
	snap.ReservedForReclaim[0] = 500
	snap.NUMAAvailable[1] = 42
	snap.NonBindingNumas.Add(7)
	*dt.Ceiling = 0
	dt.MemberNUMAs[0] = 0
	require.Equal(t, map[int]int{0: 99, 1: 5}, reserved,
		"mutating the snapshot must not write back to the advisor map")
	require.Equal(t, map[int]int{0: 100}, numaAvailable,
		"snapshot mutation must not write back; source keeps only the advisor's own change")
	require.True(t, nonBinding.Equals(machine.NewCPUSet(0, 3)),
		"the source CPUSet must remain independently owned by the advisor")
	require.Equal(t, 999, *domainTargets[domainScope].Ceiling,
		"mutating the snapshot's cloned ceiling must not write back to the advisor")
	require.Equal(t, 999, domainTargets[domainScope].MemberNUMAs[0],
		"mutating the snapshot's cloned member-NUMA slice must not write back to the advisor")
}

// TestNewSnapshotNilSourcesStayNil documents that a nil advisor map yields a nil
// snapshot entry (treated as "absent this cycle") rather than an empty-but-non-nil
// map, so the nil-vs-empty wiring semantics of the old pointer fields are
// preserved.
func TestNewSnapshotNilSourcesStayNil(t *testing.T) {
	t.Parallel()
	snap := NewSnapshot(nil, nil, nil, nil, machine.CPUSet{}, false, true, nil, nil)
	require.Nil(t, snap.RegionMap)
	require.Nil(t, snap.ReservedForReclaim)
	require.Nil(t, snap.RampUpReclaimCPUSetCap)
	require.Nil(t, snap.NUMAAvailable)
	require.Nil(t, snap.ReclaimDomainTargets)
}

// TestAssembleProvisionDoesNotLeakPriorCycleRampUpDomains proves the assembler is
// stateless across cycles: a hard-partition ramp-up on NUMA 0 in round 1 must not
// persist into round 2 once the advisor moves the ramp-up entirely to NUMA 1. If
// the assembler retained the previous cycle's context on the struct, round 2 would
// still publish NUMA 0's target.
func TestAssembleProvisionDoesNotLeakPriorCycleRampUpDomains(t *testing.T) {
	t.Parallel()

	pa := newDefaultShareAssembler(t, map[int]int{0: 16, 1: 16}, machine.NewCPUSet(0, 1), nil,
		map[int]int{0: 2, 1: 2}, false, true, nil)
	pa.conf.GetDynamicConfiguration().EnableReclaim = true
	pa.conf.GetDynamicConfiguration().EnableRampUpReclaimHardPartition = true

	// Round 1: ramp-up hard target on NUMA 0 only.
	pa.rampUpReclaimCPUSetCap = map[int]int{0: 10}
	pa.RampUpDomains = []int{0}
	res1, err := pa.AssembleProvision(pa.ctx())
	require.NoError(t, err)
	require.Contains(t, res1.PoolEntries[commonstate.PoolNameReclaim], 0,
		"round 1 must publish the NUMA 0 hard target")

	// Round 2: ramp-up moves entirely to NUMA 1; NUMA 0 target is removed.
	pa.rampUpReclaimCPUSetCap = map[int]int{1: 10}
	pa.RampUpDomains = []int{1}
	res2, err := pa.AssembleProvision(pa.ctx())
	require.NoError(t, err)
	require.Contains(t, res2.PoolEntries[commonstate.PoolNameReclaim], 1)
	require.NotContains(t, res2.PoolEntries[commonstate.PoolNameReclaim], 0,
		"the prior cycle's NUMA 0 ramp-up target must not leak into round 2")
}

// TestAssembleProvisionRepeatedCallsAreIndependentWithFreshContext reuses one
// assembler across many cycles with disjoint inputs and asserts the result always
// reflects exactly the context handed in, never a blend with a previous cycle.
func TestAssembleProvisionRepeatedCallsAreIndependentWithFreshContext(t *testing.T) {
	t.Parallel()

	pa := newDefaultShareAssembler(t, map[int]int{0: 16}, machine.NewCPUSet(0), nil,
		map[int]int{0: 2}, false, true, nil)
	pa.conf.GetDynamicConfiguration().EnableReclaim = true

	// Baseline: no ramp-up, reclaim should publish the steady (non-hard) entry.
	base, err := pa.AssembleProvision(pa.ctx())
	require.NoError(t, err)
	_, baseHard := base.PoolEntries[commonstate.PoolNameReclaim][0]
	require.False(t, baseHard, "baseline must not publish a hard-partition reclaim target")

	// Drive an explicit ramp-up cycle, then immediately drop it again. The assembler
	// must not remember the ramp-up once the caller stops passing it.
	pa.RampUpDomains = []int{0}
	pa.rampUpReclaimCPUSetCap = map[int]int{0: 10}
	ramped, err := pa.AssembleProvision(pa.ctx())
	require.NoError(t, err)
	require.Contains(t, ramped.PoolEntries[commonstate.PoolNameReclaim], 0,
		"active ramp-up must publish the NUMA 0 hard-partition reclaim target")

	// Fresh context with no ramp-up overrides at all.
	pa.RampUpDomains = nil
	pa.rampUpReclaimCPUSetCap = map[int]int{}
	after, err := pa.AssembleProvision(pa.ctx())
	require.NoError(t, err)
	require.NotContains(t, after.PoolEntries[commonstate.PoolNameReclaim], 0,
		"dropping ramp-up domains must take effect on the very next call; no prior-cycle state may persist")
}

// TestAdvisorMutationBetweenCallsIsObservedWithoutCrossContamination proves the
// wiring-level guarantee: the same assembler is called twice, and the advisor's
// source map is mutated between cycles. Because assembleProvision rebuilds the
// Snapshot from the live sources every cycle, the second call observes the new
// value (proving the assembler never cached a stale map reference) while the first
// call's already-built result is unaffected.
func TestAdvisorMutationBetweenCallsIsObservedWithoutCrossContamination(t *testing.T) {
	t.Parallel()

	pa := newDefaultShareAssembler(t, map[int]int{0: 16}, machine.NewCPUSet(0), nil,
		map[int]int{0: 2}, false, true, nil)
	pa.conf.GetDynamicConfiguration().EnableReclaim = true

	// The reservation floor lives in the advisor-live source map. Snapshot it once.
	before := pa.ctx()
	require.Equal(t, 2, before.Snapshot.ReservedForReclaim[0])

	// The advisor mutates its own bookkeeping between cycles.
	pa.reservedForReclaim[0] = 8

	// The already-built snapshot is untouched by the later mutation.
	require.Equal(t, 2, before.Snapshot.ReservedForReclaim[0],
		"an already-built snapshot must be immutable w.r.t. later advisor mutations")

	// The next cycle's freshly built snapshot observes the new value.
	after := pa.ctx()
	require.Equal(t, 8, after.Snapshot.ReservedForReclaim[0],
		"a freshly built snapshot must observe the advisor's current bookkeeping")
}

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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

// mustTopology180 reuses the shared contiguous 180-CPU, 2-NUMA, adjacent-SMT
// fixture (NUMA0=0-89, NUMA1=90-179, pairs (0,1)...(178,179)) defined in the
// bootstrap recovery test. It is the same topology the apply/integration tests
// build on.
func mustTopology180(t *testing.T) *machine.CPUTopology {
	t.Helper()
	return contiguous180CPUTopology(t)
}

func TestSelectBoundedReclaimFallbackNotWholeMachine(t *testing.T) {
	t.Parallel()
	topo := mustTopology180(t)
	eligible := machine.MustParse("0-39")
	def := machine.MustParse("0-3")

	got, err := selectBoundedReclaimFallback(topo, eligible, machine.NewCPUSet(), def, 4, reclaimFallbackMode{
		enableReclaim: true, allowSharedOverlap: false, disableDedicatedOverlap: true,
	})
	assert.NoError(t, err)
	assert.True(t, got.Size() <= 4, "bounded to want, not eligible.Size(): got %d", got.Size())
	assert.False(t, got.Equals(machine.MustParse("0-39")), "must not take whole eligible")
}

func TestSelectBoundedReclaimFallbackStrictShortfallHardFail(t *testing.T) {
	t.Parallel()
	topo := mustTopology180(t)
	eligible := machine.MustParse("0-1")
	def := machine.MustParse("0-3")

	_, err := selectBoundedReclaimFallback(topo, eligible, machine.NewCPUSet(), def, 4, reclaimFallbackMode{
		enableReclaim: true, allowSharedOverlap: false, disableDedicatedOverlap: true,
	})
	assert.ErrorIs(t, err, errReclaimFallbackCapacityInsufficient)
}

func TestSelectBoundedReclaimFallbackPrefersCommittedPrior(t *testing.T) {
	t.Parallel()
	topo := mustTopology180(t)
	eligible := machine.MustParse("0-39")
	prior := machine.MustParse("8-11")

	got, err := selectBoundedReclaimFallback(topo, eligible, prior, machine.MustParse("0-3"), 4, reclaimFallbackMode{
		enableReclaim: true, allowSharedOverlap: false, disableDedicatedOverlap: true,
	})
	assert.NoError(t, err)
	assert.Equal(t, "8-11", got.String(), "prefer committed prior over re-seeding default")
}

func TestSelectBoundedReclaimFallbackWantZeroReturnsEmpty(t *testing.T) {
	t.Parallel()
	topo := mustTopology180(t)
	// want<=0 means no explicit requirement: return empty. The caller owns the
	// empty-vs-seed policy; the helper never materializes an unfiltered prior/default.
	got, err := selectBoundedReclaimFallback(topo, machine.MustParse("0-39"), machine.NewCPUSet(), machine.MustParse("0-3"), 0, reclaimFallbackMode{})
	assert.NoError(t, err)
	assert.True(t, got.IsEmpty(), "want=0 must return empty, got %s", got)

	// Even with a non-empty committed prior, want=0 returns empty.
	got, err = selectBoundedReclaimFallback(topo, machine.MustParse("0-39"), machine.MustParse("4-7"), machine.MustParse("0-3"), 0, reclaimFallbackMode{})
	assert.NoError(t, err)
	assert.True(t, got.IsEmpty(), "want=0 must not unfiltered-restore prior, got %s", got)
}

func TestSelectBoundedReclaimFallbackEmptyEligibleReturnsRequiredError(t *testing.T) {
	t.Parallel()
	topo := mustTopology180(t)
	// want>0 but eligible empty (all reserved/forbidden) -> errEmptyRequired, no write.
	_, err := selectBoundedReclaimFallback(topo, machine.NewCPUSet(), machine.NewCPUSet(), machine.MustParse("0-3"), 4, reclaimFallbackMode{
		enableReclaim: true, allowSharedOverlap: false, disableDedicatedOverlap: false,
	})
	assert.ErrorIs(t, err, errReclaimFallbackEmptyRequired)
}

func TestSelectBoundedReclaimFallbackOversizedPriorClippedToWant(t *testing.T) {
	t.Parallel()
	topo := mustTopology180(t)
	// committed prior larger than want is clipped to want via core-aligned selection.
	prior := machine.MustParse("0-7") // 8 CPUs, want=4
	got, err := selectBoundedReclaimFallback(topo, machine.MustParse("0-39"), prior, machine.MustParse("0-3"), 4, reclaimFallbackMode{
		enableReclaim: true, allowSharedOverlap: false, disableDedicatedOverlap: true,
	})
	assert.NoError(t, err)
	assert.Equal(t, 4, got.Size(), "oversized prior must be clipped to want=4, got %d", got.Size())
}

func TestSelectBoundedReclaimFallbackOverlapBoundedNotWholeEligible(t *testing.T) {
	t.Parallel()
	topo := mustTopology180(t)
	// Legacy overlap mode selects up to want CPUs, never the whole eligible set.
	eligible := machine.MustParse("0-39") // 40 CPUs
	got, err := selectBoundedReclaimFallback(topo, eligible, machine.NewCPUSet(), machine.MustParse("0-3"), 4, reclaimFallbackMode{
		enableReclaim: true, allowSharedOverlap: true, disableDedicatedOverlap: false,
	})
	assert.NoError(t, err)
	assert.Equal(t, 4, got.Size(), "overlap must be bounded to want=4, got %d", got.Size())
	assert.True(t, got.IsSubsetOf(eligible))
}

func TestSelectBoundedReclaimFallbackForbiddenSeedNotCloned(t *testing.T) {
	t.Parallel()
	topo := mustTopology180(t)
	// Seed (defaultPreference) overlaps forbidden/system CPUs that are excluded from
	// eligible. The helper must not clone the unfiltered seed; eligible empty + want>0
	// must return errEmptyRequired.
	seed := machine.MustParse("0-3") // would be the unfiltered rescue
	eligible := machine.NewCPUSet()  // all CPUs are reserved/forbidden/system
	_, err := selectBoundedReclaimFallback(topo, eligible, machine.NewCPUSet(), seed, 4, reclaimFallbackMode{
		enableReclaim: true, allowSharedOverlap: true, disableDedicatedOverlap: false,
	})
	assert.ErrorIs(t, err, errReclaimFallbackEmptyRequired,
		"must not unfiltered-clone seed when eligible is empty")

	// Even non-strict overlap mode must not rescue the seed from forbidden CPUs.
	_, err = selectBoundedReclaimFallback(topo, eligible, machine.NewCPUSet(), seed, 4, reclaimFallbackMode{
		enableReclaim: false, allowSharedOverlap: true, disableDedicatedOverlap: false,
	})
	assert.ErrorIs(t, err, errReclaimFallbackEmptyRequired,
		"legacy overlap must not clone forbidden seed")
}

func TestSelectBoundedReclaimFallbackPreservesPartialPriorThenFills(t *testing.T) {
	t.Parallel()
	topo := mustTopology180(t)
	// Prior has one complete core (CPUs 8,9 = SMT pair), want=4. Eligible has more
	// complete cores. Result must preserve the prior core and fill to want=4.
	prior := machine.MustParse("8-9")
	eligible := machine.MustParse("0-15")
	got, err := selectBoundedReclaimFallback(topo, eligible, prior, machine.MustParse("0-1"), 4, reclaimFallbackMode{
		enableReclaim: true, allowSharedOverlap: false, disableDedicatedOverlap: true,
	})
	assert.NoError(t, err)
	assert.True(t, prior.IsSubsetOf(got), "result must preserve prior core 8-9, got %s", got)
	assert.Equal(t, 4, got.Size(), "result must be filled to want=4, got %d", got.Size())
	assert.True(t, got.IsSubsetOf(eligible))
}

func TestSelectBoundedReclaimFallbackMixedHalfPriorCompleteEligible(t *testing.T) {
	t.Parallel()
	topo := mustTopology180(t)
	// Prior has a half-core (only CPU 8, not 9). Strict mode requires complete cores.
	// Eligible has complete cores (0-1, 2-3). Result must not keep the half prior;
	// it selects complete cores from eligible up to want=4.
	prior := machine.MustParse("8")
	eligible := machine.MustParse("0-7")
	got, err := selectBoundedReclaimFallback(topo, eligible, prior, machine.MustParse("0-1"), 4, reclaimFallbackMode{
		enableReclaim: true, allowSharedOverlap: false, disableDedicatedOverlap: true,
	})
	assert.NoError(t, err)
	assert.Equal(t, 4, got.Size(), "strict must fill complete cores to want=4, got %d", got.Size())
	assert.True(t, got.IsSubsetOf(eligible))
	// Half-core prior CPU 8 must not appear (strict requires complete cores).
	assert.False(t, got.Intersection(prior).Size() > 0, "half-core prior must not leak into strict result, got %s", got)
}

// TestDynamicConfigPointerSwapDoesNotMutateCapturedSnapshot pins the snapshot contract
// the reclaim-fallback attempt relies on: GetDynamicConfiguration returns the live
// *Configuration pointer under the read lock, and SetDynamicConfiguration publishes a
// brand-new object and swaps c.conf — it does NOT mutate the object an attempt already
// captured. So a config switch mid-attempt cannot flip the immutable reclaimFallbackMode
// derived once at attempt start; the attempt keeps observing its own snapshot.
func TestDynamicConfigPointerSwapDoesNotMutateCapturedSnapshot(t *testing.T) {
	t.Parallel()
	dac := dynamic.NewDynamicAgentConfiguration()
	captured := dac.GetDynamicConfiguration()
	require.NotNil(t, captured)
	captured.EnableReclaim = true

	// A config switch swaps in a fresh Configuration object.
	dac.SetDynamicConfiguration(dynamic.NewConfiguration())

	current := dac.GetDynamicConfiguration()
	require.NotSame(t, captured, current, "SetDynamicConfiguration must publish a new object, not reuse/mutate the captured one")
	assert.True(t, captured.EnableReclaim, "the captured attempt snapshot must be untouched by the pointer swap")
	assert.False(t, current.EnableReclaim, "the published object starts from defaults")

	// The mode derived from the captured pointer is immutable for the attempt.
	mode := reclaimFallbackMode{enableReclaim: captured.EnableReclaim, allowSharedOverlap: false, disableDedicatedOverlap: true}
	assert.True(t, mode.strict())
}

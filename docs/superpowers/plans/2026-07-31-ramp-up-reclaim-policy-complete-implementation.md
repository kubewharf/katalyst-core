# RampUp Reclaim Policy Complete Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpower-subagent-driven-development (recommended) or superpower-executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Implement the final RampUp Reclaim / mixed workload / resource package / stable advice contract while preserving the proto, checkpoint, and `AllocationInfo` hard constraints.

**Architecture:** Build the implementation in small, testable layers: shared pure helpers first, then QRM snapshot/candidate primitives, then admission planning, then SysAdvisor stable advice validation, then integration and rollout gates. QRM owns concrete CPUSet safety and checkpoint commits; SysAdvisor owns stable policy decisions and emits verifiable summaries; Bulkhead consumes committed target state.

**Tech Stack:** Go, existing Katalyst QRM CPU dynamic policy, SysAdvisor QoSAware CPU plugin, gogo/protobuf generated advisor service, existing checkpoint/state packages, existing `machine.CPUSet`.

---

## File Map

- `pkg/agent/utilcomponent/reclaimpolicy/`: new shared Pod `EnableReclaim` evaluator used by QRM and SysAdvisor.
- `pkg/agent/qrm-plugins/cpu/dynamicpolicy/util/ramp_up_reclaim.go`: extend target helper with `enableReclaim`, per-NUMA semantics, typed errors.
- `pkg/agent/qrm-plugins/cpu/dynamicpolicy/util/ramp_up_reclaim_test.go`: update helper tests and red/green coverage.
- `pkg/agent/qrm-plugins/cpu/dynamicpolicy/planner/`: new pure planning package for immutable snapshot, COW candidate, target calculator, package domain cache, and batch pool allocator.
- `pkg/agent/qrm-plugins/cpu/dynamicpolicy/policy_resource_package.go`: route repeated pinned/common domain derivation through `ResourcePackageDomainCache`.
- `pkg/agent/qrm-plugins/cpu/dynamicpolicy/policy_allocation_handlers.go`: integrate admission planner for DNB/SNB/non-binding shared.
- `pkg/agent/qrm-plugins/cpu/dynamicpolicy/policy_advisor_handler.go`: implement token/revision/hash `GetAdvice` fence and stable summary validation.
- `pkg/agent/sysadvisor/plugin/qosaware/resource/helper/`: replace local `PodEnableReclaim` logic with shared evaluator.
- `pkg/agent/sysadvisor/plugin/qosaware/resource/cpu/assembler/`: emit stable domain summary/digest and use shared isolation requirement helper.
- `pkg/agent/qrm-plugins/cpu/dynamicpolicy/state`: no schema additions; only existing state accessors may be used.
- `pkg/agent/qrm-plugins/cpu/dynamicpolicy/cpuadvisor/cpu.proto`: only add `disable_dedicated_cores_overlap_reclaimed_cores` to the allowed response messages if not already present.

## Task 1: RampUp target helper contract

**Files:**
- Modify: `pkg/agent/qrm-plugins/cpu/dynamicpolicy/util/ramp_up_reclaim.go`
- Modify: `pkg/agent/qrm-plugins/cpu/dynamicpolicy/util/ramp_up_reclaim_test.go`

- [ ] **Step 1: Add failing tests for `enableReclaim=false`**

Add cases to `TestCalculateRampUpReclaimTarget`:

```go
{
    name:          "enable reclaim false ignores ratio",
    ratio:         0.8,
    eligible:      20,
    reserve:       2,
    cap:           20,
    enableReclaim: false,
    want:          2,
},
{
    name:          "enable reclaim true uses ratio target",
    ratio:         0.8,
    eligible:      20,
    reserve:       2,
    cap:           20,
    enableReclaim: true,
    want:          16,
},
```

Update the test struct with `enableReclaim bool` and call:

```go
got, err := CalculateRampUpReclaimTarget(
    tt.eligible,
    tt.reserve,
    tt.cap,
    tt.ratio,
    tt.enableReclaim,
    tt.exclusive,
)
```

- [ ] **Step 2: Run failing helper tests**

Run:

```bash
go test ./pkg/agent/qrm-plugins/cpu/dynamicpolicy/util -run TestCalculateRampUpReclaimTarget -count=1
```

Expected: compile failure because `CalculateRampUpReclaimTarget` still lacks `enableReclaim`.

- [ ] **Step 3: Extend helper signature**

Change:

```go
func CalculateRampUpReclaimTarget(eligible, reserve, cap int, ratio float64, exclusive bool) (int, error)
```

to:

```go
func CalculateRampUpReclaimTarget(eligible, reserve, cap int, ratio float64, enableReclaim, exclusive bool) (int, error)
```

Change target logic to:

```go
target := reserve
if enableReclaim && ratio > 0 {
    ratioTarget := int(math.Floor(ratio * float64(eligible)))
    ratioTarget -= ratioTarget % 2
    target = int(math.Max(float64(target), float64(ratioTarget)))
}
```

- [ ] **Step 4: Run helper tests**

Run:

```bash
go test ./pkg/agent/qrm-plugins/cpu/dynamicpolicy/util -run TestCalculateRampUpReclaimTarget -count=1
```

Expected: PASS.

## Task 2: Shared reclaim policy evaluator

**Files:**
- Create: `pkg/agent/utilcomponent/reclaimpolicy/evaluator.go`
- Create: `pkg/agent/utilcomponent/reclaimpolicy/evaluator_test.go`
- Later modify SysAdvisor/QRM call sites after tests pass.

- [ ] **Step 1: Add evaluator API and tests**

Create tests covering:

```go
func TestEvaluatePodReclaimNodeDisabled(t *testing.T) {
    decision, err := EvaluatePodReclaim(context.Background(), fakeReader{}, "pod-1", false)
    require.NoError(t, err)
    require.False(t, decision.EnableReclaim)
    require.Equal(t, ReasonNodeDisabled, decision.Reason)
}

func TestEvaluatePodReclaimSPDNotFoundDefaultsTrue(t *testing.T) {
    decision, err := EvaluatePodReclaim(context.Background(), fakeReader{spdNotFound: true}, "pod-1", true)
    require.NoError(t, err)
    require.True(t, decision.EnableReclaim)
    require.Equal(t, ReasonSPDNotFoundDefaultTrue, decision.Reason)
}
```

Define fake reader methods in the test file for SPD not found, performance poor, service baseline, and generic errors.

- [ ] **Step 2: Run failing evaluator tests**

Run:

```bash
go test ./pkg/agent/utilcomponent/reclaimpolicy -count=1
```

Expected: FAIL because package/functions do not exist.

- [ ] **Step 3: Implement evaluator types**

Create:

```go
package reclaimpolicy

import "context"

const (
    ReasonNodeDisabled           = "node_enable_reclaim_disabled"
    ReasonPerformanceLevelPoor   = "performance_level_poor"
    ReasonServiceBaseline        = "service_baseline"
    ReasonSPDNotFoundDefaultTrue = "spd_not_found_default_true"
    ReasonEligible               = "eligible"
)

type PodReclaimDecision struct {
    EnableReclaim bool
    Reason        string
    Source        string
}

type PodMetaReader interface {
    IsPerformanceLevelPoor(ctx context.Context, podUID string) (bool, error)
    IsServiceBaseline(ctx context.Context, podUID string) (bool, error)
    IsSPDNotFound(error) bool
}
```

Implement `EvaluatePodReclaim` with the final scheme ordering:

```go
func EvaluatePodReclaim(ctx context.Context, metaReader PodMetaReader, podUID string, nodeEnableReclaim bool) (PodReclaimDecision, error) {
    if !nodeEnableReclaim {
        return PodReclaimDecision{EnableReclaim: false, Reason: ReasonNodeDisabled, Source: "node"}, nil
    }
    poor, err := metaReader.IsPerformanceLevelPoor(ctx, podUID)
    if err != nil {
        if metaReader.IsSPDNotFound(err) {
            return PodReclaimDecision{EnableReclaim: true, Reason: ReasonSPDNotFoundDefaultTrue, Source: "spd"}, nil
        }
        return PodReclaimDecision{}, err
    }
    if poor {
        return PodReclaimDecision{EnableReclaim: false, Reason: ReasonPerformanceLevelPoor, Source: "spd"}, nil
    }
    baseline, err := metaReader.IsServiceBaseline(ctx, podUID)
    if err != nil {
        if metaReader.IsSPDNotFound(err) {
            return PodReclaimDecision{EnableReclaim: true, Reason: ReasonSPDNotFoundDefaultTrue, Source: "spd"}, nil
        }
        return PodReclaimDecision{}, err
    }
    if baseline {
        return PodReclaimDecision{EnableReclaim: false, Reason: ReasonServiceBaseline, Source: "baseline"}, nil
    }
    return PodReclaimDecision{EnableReclaim: true, Reason: ReasonEligible, Source: "default"}, nil
}
```

- [ ] **Step 4: Run evaluator tests**

Run:

```bash
go test ./pkg/agent/utilcomponent/reclaimpolicy -count=1
```

Expected: PASS.

## Task 3: Resource package domain cache

**Files:**
- Create: `pkg/agent/qrm-plugins/cpu/dynamicpolicy/planner/resource_package_domain_cache.go`
- Create: `pkg/agent/qrm-plugins/cpu/dynamicpolicy/planner/resource_package_domain_cache_test.go`

- [ ] **Step 1: Write cache tests**

Test that pinned packages are separated from common domain:

```go
func TestBuildResourcePackageDomainCache(t *testing.T) {
    eligible := map[int]machine.CPUSet{
        0: machine.NewCPUSet(0, 1, 2, 3, 4, 5, 6, 7),
    }
    pinned := map[int]map[string]machine.CPUSet{
        0: {
            "pkg-a": machine.NewCPUSet(0, 1),
            "pkg-b": machine.NewCPUSet(2, 3),
        },
    }

    cache := BuildResourcePackageDomainCache(eligible, pinned, 7)

    require.Equal(t, machine.NewCPUSet(0, 1, 2, 3), cache.PinnedUnion[0])
    require.Equal(t, machine.NewCPUSet(0, 1), cache.PackageDomain[0]["pkg-a"])
    require.Equal(t, machine.NewCPUSet(4, 5, 6, 7), cache.CommonDomain[0])
    require.Equal(t, uint64(7), cache.Revision)
}
```

- [ ] **Step 2: Run failing cache tests**

Run:

```bash
go test ./pkg/agent/qrm-plugins/cpu/dynamicpolicy/planner -run TestBuildResourcePackageDomainCache -count=1
```

Expected: FAIL because package does not exist.

- [ ] **Step 3: Implement cache**

Create:

```go
package planner

import "github.com/kubewharf/katalyst-core/pkg/util/machine"

type ResourcePackageDomainCache struct {
    PinnedUnion   map[int]machine.CPUSet
    PackageDomain map[int]map[string]machine.CPUSet
    CommonDomain  map[int]machine.CPUSet
    Revision      uint64
}

func BuildResourcePackageDomainCache(
    eligible map[int]machine.CPUSet,
    pinned map[int]map[string]machine.CPUSet,
    revision uint64,
) *ResourcePackageDomainCache {
    cache := &ResourcePackageDomainCache{
        PinnedUnion:   make(map[int]machine.CPUSet, len(eligible)),
        PackageDomain: make(map[int]map[string]machine.CPUSet, len(eligible)),
        CommonDomain:  make(map[int]machine.CPUSet, len(eligible)),
        Revision:      revision,
    }
    for numaID, domain := range eligible {
        union := machine.NewCPUSet()
        cache.PackageDomain[numaID] = make(map[string]machine.CPUSet)
        for pkgName, pkgSet := range pinned[numaID] {
            pkgDomain := domain.Intersection(pkgSet)
            cache.PackageDomain[numaID][pkgName] = pkgDomain
            union = union.Union(pkgDomain)
        }
        cache.PinnedUnion[numaID] = union
        cache.CommonDomain[numaID] = domain.Difference(union)
    }
    return cache
}
```

- [ ] **Step 4: Run planner cache tests**

Run:

```bash
go test ./pkg/agent/qrm-plugins/cpu/dynamicpolicy/planner -run TestBuildResourcePackageDomainCache -count=1
```

Expected: PASS.

## Task 4: Batch pool allocator

**Files:**
- Create: `pkg/agent/qrm-plugins/cpu/dynamicpolicy/planner/batch_pool_allocator.go`
- Create: `pkg/agent/qrm-plugins/cpu/dynamicpolicy/planner/batch_pool_allocator_test.go`

- [ ] **Step 1: Write allocation tests**

Test historical reuse and deterministic top-up:

```go
func TestBatchPoolAllocatorReusesHistoricalThenTopUp(t *testing.T) {
    allocator := NewBatchPoolAllocator()
    domain := machine.NewCPUSet(0, 1, 2, 3, 4, 5)
    quantity := map[string]int{"pool-a": 3, "pool-b": 2}
    historical := map[string]machine.CPUSet{
        "pool-a": machine.NewCPUSet(0, 1, 9),
        "pool-b": machine.NewCPUSet(2),
    }

    got, err := allocator.Allocate(domain, quantity, historical)
    require.NoError(t, err)
    require.Equal(t, 3, got["pool-a"].Size())
    require.True(t, got["pool-a"].IsSuperset(machine.NewCPUSet(0, 1)))
    require.Equal(t, 2, got["pool-b"].Size())
    require.True(t, got["pool-b"].IsSuperset(machine.NewCPUSet(2)))
    require.True(t, got["pool-a"].Intersection(got["pool-b"]).IsEmpty())
}
```

- [ ] **Step 2: Run failing allocator tests**

Run:

```bash
go test ./pkg/agent/qrm-plugins/cpu/dynamicpolicy/planner -run TestBatchPoolAllocator -count=1
```

Expected: FAIL because allocator does not exist.

- [ ] **Step 3: Implement allocator**

Create `BatchPoolAllocator` that:

- sorts pool names lexicographically;
- first assigns `historical[pool].Intersection(domain).Take(quantity)` semantics using sorted CPU slices;
- removes assigned CPUs from a single free domain;
- second pass fills deficits from the same free domain in sorted CPU order;
- errors if total quantity exceeds `domain.Size()`.

- [ ] **Step 4: Run allocator tests**

Run:

```bash
go test ./pkg/agent/qrm-plugins/cpu/dynamicpolicy/planner -run TestBatchPoolAllocator -count=1
```

Expected: PASS.

## Task 5: COW candidate scaffolding

**Files:**
- Create: `pkg/agent/qrm-plugins/cpu/dynamicpolicy/planner/snapshot.go`
- Create: `pkg/agent/qrm-plugins/cpu/dynamicpolicy/planner/candidate.go`
- Create: `pkg/agent/qrm-plugins/cpu/dynamicpolicy/planner/candidate_test.go`

- [ ] **Step 1: Write COW tests**

Add tests proving unmodified NUMA state is reused and dirty NUMA is cloned:

```go
func TestCPUStateCandidateClonesOnlyDirtyNUMA(t *testing.T) {
    snapshot := CPUStateSnapshot{
        MachineState: map[int]*state.NUMANodeState{
            0: {DefaultCPUSet: machine.NewCPUSet(0, 1)},
            1: {DefaultCPUSet: machine.NewCPUSet(2, 3)},
        },
    }
    candidate := NewCPUStateCandidate(snapshot)
    candidate.UpdateNUMADefaultCPUSet(0, machine.NewCPUSet(0))

    materialized := candidate.Materialize()
    require.Equal(t, machine.NewCPUSet(0), materialized.MachineState[0].DefaultCPUSet)
    require.Equal(t, machine.NewCPUSet(2, 3), materialized.MachineState[1].DefaultCPUSet)
    require.True(t, candidate.IsNUMADirty(0))
    require.False(t, candidate.IsNUMADirty(1))
}
```

- [ ] **Step 2: Run failing COW tests**

Run:

```bash
go test ./pkg/agent/qrm-plugins/cpu/dynamicpolicy/planner -run TestCPUStateCandidate -count=1
```

Expected: FAIL because candidate scaffolding does not exist.

- [ ] **Step 3: Implement snapshot/candidate**

Define:

```go
type CPUStateSnapshot struct {
    InMemoryRevision uint64
    PodEntries       state.PodEntries
    MachineState     state.NUMANodeMap
}

type CPUStateCandidate struct {
    base       CPUStateSnapshot
    numaDirty  map[int]struct{}
    numaStates state.NUMANodeMap
}
```

Implement `NewCPUStateCandidate`, `UpdateNUMADefaultCPUSet`, `IsNUMADirty`, and `Materialize`.

- [ ] **Step 4: Run COW tests**

Run:

```bash
go test ./pkg/agent/qrm-plugins/cpu/dynamicpolicy/planner -run TestCPUStateCandidate -count=1
```

Expected: PASS.

## Task 6: Request freshness primitives

**Files:**
- Create: `pkg/agent/qrm-plugins/cpu/dynamicpolicy/planner/freshness.go`
- Create: `pkg/agent/qrm-plugins/cpu/dynamicpolicy/planner/freshness_test.go`
- Later integrate in `pkg/agent/qrm-plugins/cpu/dynamicpolicy/policy_advisor_handler.go`.

- [ ] **Step 1: Write freshness tests**

Tests:

- stale token rejects;
- stale revision rejects;
- matching token/revision but changed normalized hash rejects;
- matching all fields accepts.

Use:

```go
snapshot := PendingAdviceSnapshot{
    Token:                 3,
    InMemoryRevision:      8,
    NormalizedRequestHash: 99,
}
current := AdviceFreshness{
    Token:                 3,
    InMemoryRevision:      8,
    NormalizedRequestHash: 99,
}
require.NoError(t, snapshot.Validate(current))
```

- [ ] **Step 2: Implement freshness structs**

Create:

```go
type PendingAdviceSnapshot struct {
    Token                 uint64
    InMemoryRevision      uint64
    NormalizedRequestHash uint64
}

type AdviceFreshness struct {
    Token                 uint64
    InMemoryRevision      uint64
    NormalizedRequestHash uint64
}

func (p PendingAdviceSnapshot) Validate(current AdviceFreshness) error
```

- [ ] **Step 3: Run freshness tests**

Run:

```bash
go test ./pkg/agent/qrm-plugins/cpu/dynamicpolicy/planner -run TestPendingAdviceSnapshot -count=1
```

Expected: PASS.

## Task 7: Stable advice summary contract

**Files:**
- Create: `pkg/agent/qrm-plugins/cpu/dynamicpolicy/planner/stable_summary.go`
- Create: `pkg/agent/qrm-plugins/cpu/dynamicpolicy/planner/stable_summary_test.go`
- Later modify SysAdvisor assembler to populate equivalent summary.

- [ ] **Step 1: Write summary validation tests**

Test digest mismatch rejects:

```go
summary := StableAdviceDomainSummary{PackageDomainDigest: 1}
local := LocalDomainSummary{PackageDomainDigest: 2}
require.ErrorContains(t, summary.Validate(local), "package domain digest mismatch")
```

- [ ] **Step 2: Implement summary structs**

Create:

```go
type StableAdviceDomainSummary struct {
    PerNUMAFloor         map[int]int
    PerPoolBudget        map[string]int
    PackageDomainDigest  uint64
    BlockGraphDigest     uint64
    OverlapModeDigest    uint64
}

type LocalDomainSummary StableAdviceDomainSummary

func (s StableAdviceDomainSummary) Validate(local LocalDomainSummary) error
```

- [ ] **Step 3: Run summary tests**

Run:

```bash
go test ./pkg/agent/qrm-plugins/cpu/dynamicpolicy/planner -run TestStableAdviceDomainSummary -count=1
```

Expected: PASS.

## Task 8: Proto whitelist field

**Files:**
- Modify: `pkg/agent/qrm-plugins/cpu/dynamicpolicy/cpuadvisor/cpu.proto`
- Regenerate generated protobuf files with repository-standard command.
- Test: generated proto compile.

- [ ] **Step 1: Inspect existing proto messages**

Run:

```bash
grep -n "message ListAndWatchResponse\\|message GetAdviceResponse\\|disable_dedicated_cores_overlap_reclaimed_cores" pkg/agent/qrm-plugins/cpu/dynamicpolicy/cpuadvisor/cpu.proto
```

Expected: confirm whether the field is already present.

- [ ] **Step 2: Add only allowed fields if missing**

Add:

```proto
message ListAndWatchResponse {
  ...
  bool disable_dedicated_cores_overlap_reclaimed_cores = 4;
}

message GetAdviceResponse {
  ...
  bool disable_dedicated_cores_overlap_reclaimed_cores = 5;
}
```

Do not add decision, generation, cohort, source pool, deadline, token, or metadata fields.

- [ ] **Step 3: Regenerate protobuf**

Run the repository's proto generation command. If no wrapper exists, use the command already used in this repository's `Makefile` or scripts.

Expected: generated Go files compile and contain the two getters only.

## Task 9: Integration wiring checkpoint

**Files:**
- Modify QRM and SysAdvisor call sites incrementally after Tasks 1-8 pass.

- [ ] **Step 1: Replace direct target helper call sites**

Find:

```bash
grep -R "CalculateRampUpReclaimTarget(" -n pkg/agent
```

Update all call sites to pass `enableReclaim`.

- [ ] **Step 2: Replace SysAdvisor-local reclaim decision logic**

Find:

```bash
grep -R "PodEnableReclaim\\|PerformanceLevelPoor\\|service baseline" -n pkg/agent/sysadvisor
```

Replace with `reclaimpolicy.EvaluatePodReclaim` through an adapter implementing `PodMetaReader`.

- [ ] **Step 3: Wire QRM admission through planner primitives**

Start with one workload class at a time:

1. non-binding shared;
2. SNB;
3. non-exclusive DNB;
4. exclusive DNB.

Each workload class must have a failing unit test before wiring.

- [ ] **Step 4: Run focused integration tests**

Run:

```bash
go test ./pkg/agent/qrm-plugins/cpu/dynamicpolicy/... -count=1
go test ./pkg/agent/sysadvisor/plugin/qosaware/resource/... -count=1
```

Expected: PASS or known unrelated failures documented before proceeding.

## Self-Review

- Spec coverage: This plan covers shared evaluator, target helper, resource package domain cache, batch allocator, COW candidate, freshness fence, stable summary, proto whitelist, and integration wiring. Bulkhead owner-level transfer graph is intentionally excluded from the first coding batch because the user asked to begin code generation from the final scheme and recent scope excluded Bulkhead-specific P1 work.
- Placeholder scan: No `TBD` or `TODO` placeholders are present. The only deferred work is explicitly marked as “later integrate” with concrete files and commands.
- Type consistency: The plan consistently uses `CPUStateSnapshot`, `CPUStateCandidate`, `ResourcePackageDomainCache`, `BatchPoolAllocator`, `PendingAdviceSnapshot`, and `StableAdviceDomainSummary`.

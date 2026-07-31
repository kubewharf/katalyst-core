# Ramp-Up Reclaim Hard Partition Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpower-subagent-driven-development (recommended) or superpower-executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Implement `EnableRampUpReclaimHardPartition` and `InitialRampUpReclaimCPUSetRatio` so shared, SNB, non-exclusive DNB, and exclusive DNB all preserve a concrete hard reclaim CPUSet during ramp-up, with exclusive DNB split from reclaim on every bound NUMA.

**Architecture:** QRM owns concrete CPU IDs and persists the hard ramp-up reclaim reservation by reusing the reclaim pool entry's `AllocationResult`; SysAdvisor only sizes and publishes independent blocks that respect the QRM-owned bootstrap target. QRM first computes and validates a candidate state, commits that state as the target, then asks bulkhead to apply the committed state through the safe writer and read-back loop.

**Tech Stack:** Go, Kubernetes CRD API, Katalyst QRM DynamicPolicy, SysAdvisor provision assembler, CPUSet bulkhead topology handlers, Testify.

---

## Implementation guardrails

- Complex state transitions must include short comments that explain ownership, not mechanics. Good comments name the invariant, for example: `// Hard reclaim CPUs are represented once by PoolNameReclaim/FakedContainerName; main containers keep workload allocations only.`
- Do not add silent clamps for ratio/cap failures. Invalid bootstrap targets must return explicit errors before state mutation.
- Do not mix candidate and live state. Every helper used during candidate apply must receive the candidate entries/state explicitly.
- Do not enable the global hard partition switch until all four workload modes are implemented and tested: shared, SNB, non-exclusive DNB, exclusive DNB.
- Do not leave the local absolute `katalyst-api` replace in a CI-ready commit.

## File map

- API schema: `../katalyst-api-ramp-up-reclaim-bulkhead-integration/pkg/apis/config/v1alpha1/adminqos.go`
- API deepcopy: `../katalyst-api-ramp-up-reclaim-bulkhead-integration/pkg/apis/config/v1alpha1/zz_generated.deepcopy.go`
- Core dynamic QRM config: `pkg/config/agent/dynamic/adminqos/qrm/cpu_plugin.go`
- QRM CLI/options: `cmd/katalyst-agent/app/options/dynamic/adminqos/qrm/cpu_plugin.go`
- CPU state: `pkg/agent/qrm-plugins/cpu/dynamicpolicy/state/state.go`
- In-memory state: `pkg/agent/qrm-plugins/cpu/dynamicpolicy/state/state_mem.go`
- Candidate target state: `pkg/agent/qrm-plugins/cpu/dynamicpolicy/state/target_state.go`
- QRM allocation: `pkg/agent/qrm-plugins/cpu/dynamicpolicy/policy_allocation_handlers.go`
- QRM advisor apply/materialization: `pkg/agent/qrm-plugins/cpu/dynamicpolicy/policy_advisor_handler.go`
- QRM resource report lifecycle: `pkg/agent/qrm-plugins/cpu/dynamicpolicy/policy.go`
- CPUSet adjustment runner: `pkg/agent/qrm-plugins/cpu/dynamicpolicy/cpuset_adjustment_handler.go`
- CPUSet adjustment context: `pkg/agent/qrm-plugins/cpu/dynamicpolicy/util/cpuset_adjustment.go`
- Bulkhead view/manager: `pkg/agent/qrm-plugins/cpu/dynamicpolicy/bulkhead`
- SysAdvisor CPU request/response bridge: `pkg/agent/sysadvisor/plugin/qosaware/server/cpu_server.go`
- SysAdvisor CPU advisor: `pkg/agent/sysadvisor/plugin/qosaware/resource/cpu/advisor.go`
- SysAdvisor assembler: `pkg/agent/sysadvisor/plugin/qosaware/resource/cpu/assembler/provisionassembler/assembler_common.go`

---

### Task 1: API and core config

**Files:**
- Modify: `../katalyst-api-ramp-up-reclaim-bulkhead-integration/pkg/apis/config/v1alpha1/adminqos.go`
- Modify: `../katalyst-api-ramp-up-reclaim-bulkhead-integration/pkg/apis/config/v1alpha1/zz_generated.deepcopy.go`
- Modify: `pkg/config/agent/dynamic/adminqos/qrm/cpu_plugin.go`
- Modify: `pkg/config/agent/dynamic/adminqos/qrm/cpu_plugin_test.go`

- [ ] **Step 1: Write API/core config tests**

Add tests that distinguish enable switch and ratio values:

```go
func TestCPUPluginConfigurationApplyRampUpReclaimHardPartitionConfig(t *testing.T) {
    enabled := true
    disabled := false
    zero := 0.0
    defaultRatio := 0.25
    half := 0.5
    tests := []struct {
        name string
        enable *bool
        ratio *float64
        wantEnabled bool
        wantRatio float64
    }{
        {name: "nil switch keeps legacy disabled"},
        {name: "explicit disabled keeps legacy disabled", enable: &disabled},
        {name: "enabled nil ratio keeps flag default", enable: &enabled, wantEnabled: true, wantRatio: defaultRatio},
        {name: "enabled explicit zero uses reserve floor", enable: &enabled, ratio: &zero, wantEnabled: true},
        {name: "enabled positive ratio uses ratio mode", enable: &enabled, ratio: &half, wantEnabled: true, wantRatio: 0.5},
    }
    // Build AdminQoSConfiguration with defaultRatio and assert switch semantics are independent from ratio value.
}
```

- [ ] **Step 2: Run focused config tests**

Run: `go test ./pkg/config/agent/dynamic/adminqos/qrm -run TestCPUPluginConfigurationApplyRampUpReclaimHardPartitionConfig -count=1`

Expected: FAIL because the enable switch and ratio field do not exist.

- [ ] **Step 3: Add API field with clear semantic comment**

Add these fields to API `CPUPluginConfig`:

```go
// EnableRampUpReclaimHardPartition enables hard reclaim partitioning while a
// workload is in ramp-up. When disabled or unset, legacy ramp-up behavior is kept.
EnableRampUpReclaimHardPartition *bool `json:"enableRampUpReclaimHardPartition,omitempty"`

// InitialRampUpReclaimCPUSetRatio controls the optional dynamic ratio target used
// after hard partitioning is enabled. nil keeps the startup flag/default ratio;
// 0 uses reserve floors only; (0,1] uses the larger of reserve floor and ratio target.
InitialRampUpReclaimCPUSetRatio *float64 `json:"initialRampUpReclaimCPUSetRatio,omitempty"`
```

Update deepcopy so both pointers are copied independently.

- [ ] **Step 4: Add core config field and validation**

Add the same pointer fields to core QRM CPU plugin configuration and reject ratio values outside `[0,1]` during config apply:

```go
if cfg.InitialRampUpReclaimCPUSetRatio != nil {
    ratio := *cfg.InitialRampUpReclaimCPUSetRatio
    if ratio < 0 || ratio > 1 {
        return fmt.Errorf("initialRampUpReclaimCPUSetRatio must be in [0,1], got %f", ratio)
    }
}
```

- [ ] **Step 5: Run config suites**

Run:

```bash
go test ./pkg/config/agent/dynamic/adminqos/qrm -count=1
go test ./pkg/apis/config/v1alpha1 -count=1
```

Expected: PASS.

---

### Task 2: Reclaim pool state reuse

**Files:**
- Modify: `pkg/agent/qrm-plugins/cpu/dynamicpolicy/state/state.go`
- Modify: `pkg/agent/qrm-plugins/cpu/dynamicpolicy/state/state_mem.go`
- Modify: `pkg/agent/qrm-plugins/cpu/dynamicpolicy/state/target_state.go`
- Modify: `pkg/agent/qrm-plugins/cpu/dynamicpolicy/state/state_test.go`

- [ ] **Step 1: Write reclaim pool reuse tests**

Add tests that verify:

```go
func TestReclaimPoolAllocationResultClonesIndependently(t *testing.T) {
    reclaimPool := &AllocationInfo{
        AllocationMeta: commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
        AllocationResult: machine.NewCPUSet(1, 2),
    }
    cloned := reclaimPool.Clone()
    cloned.AllocationResult = cloned.AllocationResult.Difference(machine.NewCPUSet(1))
    assert.True(t, reclaimPool.AllocationResult.Equals(machine.NewCPUSet(1, 2)))
    assert.True(t, cloned.AllocationResult.Equals(machine.NewCPUSet(2)))
}
```

- [ ] **Step 2: Run focused state tests**

Run: `go test ./pkg/agent/qrm-plugins/cpu/dynamicpolicy/state -run 'TestReclaimPoolAllocationResult|TestTargetState' -count=1`

Expected: `TestTargetState` fails until candidate getters are implemented; reclaim pool clone behavior should pass with existing `AllocationResult`.

- [ ] **Step 3: Keep hard reclaim in reclaim pool entry**

Do not add `RampUpReclaimPlanned` or `RampUpReclaimCPUSet`. The hard reclaim target is the committed reclaim pool entry:

```go
entries[commonstate.PoolNameReclaim][commonstate.FakedContainerName].AllocationResult
```

Add code comments where the planner updates the reclaim pool entry:

```go
// Ramp-up hard reclaim reuses the reclaim pool entry. Main containers keep
// workload allocations in AllocationResult; reclaim ownership is represented
// once by PoolNameReclaim/FakedContainerName to avoid duplicate accounting.
```

- [ ] **Step 4: Extend TargetState as a complete candidate snapshot**

Add overlap flags and implement the `ReadonlyState` getters needed by CPUSet adjustment and source-pool helpers:

```go
type TargetState struct {
    PodEntries   PodEntries
    MachineState NUMANodeMap

    AllowSharedCoresOverlapReclaimedCores      bool
    DisableDedicatedCoresOverlapReclaimedCores bool
}
```

Add a file-level comment: `TargetState never falls back to live state; all getters must read this candidate snapshot only.`

- [ ] **Step 5: Run state suite**

Run: `go test ./pkg/agent/qrm-plugins/cpu/dynamicpolicy/state -count=1`

Expected: PASS.

---

### Task 3: Reserve floor and bootstrap validator

**Files:**
- Create: `pkg/agent/qrm-plugins/cpu/dynamicpolicy/util/ramp_up_reclaim.go`
- Create: `pkg/agent/qrm-plugins/cpu/dynamicpolicy/util/ramp_up_reclaim_test.go`
- Modify: `pkg/agent/sysadvisor/plugin/qosaware/resource/cpu/assembler/provisionassembler/assembler_common.go`
- Modify: `pkg/agent/sysadvisor/plugin/qosaware/resource/cpu/assembler/provisionassembler/assembler_common_test.go`

- [ ] **Step 1: Write helper tests**

Cover reserve floor and cap combinations:

```go
func TestCalculateRampUpReclaimTargetByNUMA(t *testing.T) {
    tests := []struct {
        name string
        ratio float64
        eligible int
        reserve int
        cap int
        exclusive bool
        want int
        wantErr string
    }{
        {name: "reserve wins", ratio: 0.1, eligible: 20, reserve: 4, cap: 10, want: 4},
        {name: "ratio wins with ceil", ratio: 0.26, eligible: 20, reserve: 1, cap: 10, want: 6},
        {name: "target above cap rejects", ratio: 0.8, eligible: 20, reserve: 1, cap: 10, wantErr: "bootstrap target exceeds reclaim cap"},
        {name: "exclusive remainder empty rejects", ratio: 1, eligible: 20, reserve: 1, cap: 20, exclusive: true, wantErr: "exclusive ramp-up requires non-empty dedicated remainder"},
    }
}
```

- [ ] **Step 2: Run helper tests**

Run: `go test ./pkg/agent/qrm-plugins/cpu/dynamicpolicy/util -run TestCalculateRampUpReclaimTargetByNUMA -count=1`

Expected: FAIL because helper file does not exist.

- [ ] **Step 3: Implement pure helper**

Implement:

```go
func CalculateRampUpReclaimTarget(eligible, reserve, cap int, ratio float64, exclusive bool) (int, error)
```

Comment the cap invariant:

```go
// Do not clamp bootstrap to cap: a clamp would make QRM and SysAdvisor disagree
// about the hard partition that bulkhead must converge to.
```

- [ ] **Step 4: Replace duplicated reserve math**

Extract shared reserve-floor calculation used by QRM and SysAdvisor. Keep existing semantics:

```text
reserve ratio denominator = physical NUMA CPU count
initial ramp-up ratio denominator = eligible NUMA CPU count
```

- [ ] **Step 5: Run helper and assembler tests**

Run:

```bash
go test ./pkg/agent/qrm-plugins/cpu/dynamicpolicy/util -count=1
go test ./pkg/agent/sysadvisor/plugin/qosaware/resource/cpu/assembler/provisionassembler -run 'Test.*RampUp|Test.*Dedicated.*Reclaim' -count=1
```

Expected: PASS.

---

### Task 4: QRM initial planner

**Files:**
- Modify: `pkg/agent/qrm-plugins/cpu/dynamicpolicy/policy_allocation_handlers.go`
- Modify: `pkg/agent/qrm-plugins/cpu/dynamicpolicy/policy_allocation_handlers_test.go`
- Modify: `pkg/agent/qrm-plugins/cpu/dynamicpolicy/policy.go`
- Modify: `pkg/agent/qrm-plugins/cpu/dynamicpolicy/policy_test.go`

- [ ] **Step 1: Write workload matrix tests**

Add a table-driven test that exercises:

```text
shared: whole-machine target with per-NUMA floor
SNB: target only on bound NUMA
non-exclusive DNB: workload gets request, hard reclaim is removed from candidate pool
exclusive DNB: workload gets whole remaining NUMA, hard reclaim is non-empty and disjoint
```

Assert for exclusive DNB:

```go
assert.False(t, hard.IsEmpty())
assert.False(t, dedicated.IsEmpty())
assert.True(t, hard.Intersection(dedicated).IsEmpty())
assert.True(t, hard.Union(dedicated).Equals(eligible))
assert.GreaterOrEqual(t, dedicated.Size(), requestInNUMA)
```

- [ ] **Step 2: Run focused allocation tests**

Run: `MOCKEY_CHECK_GCFLAGS=false go test ./pkg/agent/qrm-plugins/cpu/dynamicpolicy -run 'Test.*InitialRampUpReclaim|Test.*ExclusiveDNB' -count=1 -timeout=30m`

Expected: FAIL because the planner does not exist.

- [ ] **Step 3: Implement planner entry point**

Add:

```go
func (p *DynamicPolicy) planInitialRampUpAllocation(
    entries state.PodEntries,
    machineState state.NUMANodeMap,
    req *pluginapi.ResourceRequest,
    ratio float64,
    reserveByNUMA map[int]int,
) (*state.TargetState, error)
```

Place a high-level comment above the function:

```go
// planInitialRampUpAllocation chooses concrete hard reclaim CPUs before the
// container is committed. QRM is the only owner of CPU IDs; SysAdvisor may size
// blocks from this target but must not choose different CPUs.
```

- [ ] **Step 4: Implement four workload branches**

Shared and SNB choose hard reclaim from eligible CPUs while respecting reserve floors. Non-exclusive DNB binds request-sized dedicated CPUs from `eligible - hard`. Exclusive DNB binds `eligible - hard` as the workload CPUSet.

Return errors with workload mode and NUMA id when any invariant fails.

- [ ] **Step 5: Update GetResourcesAllocation lifecycle**

Do not clear hard reservation when transition period expires. Report stable phase to SysAdvisor by request phase, while live state keeps `RampUp=true` and the reclaim pool `AllocationResult` remains the hard target until the stable candidate is committed as the new target state.

Add this comment near the expiry branch:

```go
// Expiry only changes the advisor phase. The reclaim pool AllocationResult
// remains the hard target until the stable candidate is committed; bulkhead then
// converges cgroups toward the committed state instead of driving the transition.
```

- [ ] **Step 6: Run allocation suite**

Run: `MOCKEY_CHECK_GCFLAGS=false go test ./pkg/agent/qrm-plugins/cpu/dynamicpolicy -run 'Test.*InitialRampUpReclaim|Test.*ResourcesAllocation' -count=1 -timeout=30m`

Expected: PASS.

---

### Task 5: Advisor request snapshot validation

**Files:**
- Modify: `pkg/agent/qrm-plugins/cpu/dynamicpolicy/policy_advisor_handler.go`
- Modify: `pkg/agent/qrm-plugins/cpu/dynamicpolicy/policy_advisor_handler_test.go`

- [ ] **Step 1: Write stale response tests**

Create two requests that differ only by one of these fields and assert the old response is rejected:

```text
RampUp phase
DNB/SNB topology
reclaim bootstrap topology
resource request quantity
dynamic config feature support
```

- [ ] **Step 2: Run stale response tests**

Run: `MOCKEY_CHECK_GCFLAGS=false go test ./pkg/agent/qrm-plugins/cpu/dynamicpolicy -run 'Test.*Validate.*Advice.*Request|Test.*Stale.*Response' -count=1 -timeout=30m`

Expected: FAIL because current validation compares only a subset.

- [ ] **Step 3: Add normalized request comparison**

Implement a helper that rebuilds the current `GetAdviceRequest` under the QRM lock and compares the complete normalized request to the request that produced the response.

Add this comment:

```go
// Complete request equality is the synchronization token for the first release.
// Until ListAndWatch gains generation ACK, any field that can influence advice
// must invalidate an older synchronous response.
```

- [ ] **Step 4: Fail closed when feature requires unsupported async path**

When `EnableRampUpReclaimHardPartition == true`, require synchronous `GetAdvice` and negotiated support. Return an explicit error before accepting legacy async advice.

- [ ] **Step 5: Run advisor handler tests**

Run: `MOCKEY_CHECK_GCFLAGS=false go test ./pkg/agent/qrm-plugins/cpu/dynamicpolicy -run 'Test.*Advice|Test.*ApplyBlocks' -count=1 -timeout=30m`

Expected: PASS.

---

### Task 6: SysAdvisor bootstrap blocks

**Files:**
- Modify: `pkg/agent/sysadvisor/plugin/qosaware/server/cpu_server.go`
- Modify: `pkg/agent/sysadvisor/plugin/qosaware/resource/cpu/advisor.go`
- Modify: `pkg/agent/sysadvisor/plugin/qosaware/resource/cpu/assembler/provisionassembler/assembler_common.go`
- Modify: `pkg/agent/sysadvisor/plugin/qosaware/resource/cpu/assembler/provisionassembler/assembler_common_test.go`
- Modify: `pkg/agent/sysadvisor/plugin/qosaware/server/cpu_server_linux_test.go`

- [ ] **Step 1: Write assembler bootstrap tests**

Add cases for active ramp-up:

```text
exclusive DNB: dedicated result = eligible - bootstrap, reclaim result = bootstrap
stable strict isolation: dedicated NUMA-exclusive + reserve > 0 still rejects
bootstrap above ReclaimedCPUMaxRatio cap rejects
bootstrap block has no overlap targets
```

- [ ] **Step 2: Run focused SysAdvisor tests**

Run: `go test ./pkg/agent/sysadvisor/plugin/qosaware/resource/cpu/assembler/provisionassembler -run 'Test.*RampUp.*Bootstrap|Test.*Exclusive.*DNB' -count=1`

Expected: FAIL because bootstrap target is not read from QRM request.

- [ ] **Step 3: Preserve QRM bootstrap target**

Read bootstrap topology from QRM request/container info and pass it to assembler without changing CPU IDs. SysAdvisor may compute block sizes and relationships only.

Add this comment near the assembler override:

```go
// During ramp-up, QRM has already selected the concrete reclaim CPUs. The
// assembler must preserve that bootstrap size so block sizing and QRM materialize
// remain a single transaction.
```

- [ ] **Step 4: Split exclusive DNB in active phase**

Allow active bootstrap phase to publish two independent, non-overlap blocks on the same NUMA. Keep the existing stable strict-isolation rejection for old semantics.

- [ ] **Step 5: Run SysAdvisor suites**

Run:

```bash
go test ./pkg/agent/sysadvisor/plugin/qosaware/resource/cpu/assembler/... -count=1
go test ./pkg/agent/sysadvisor/plugin/qosaware/server -run 'Test.*CPU' -count=1
```

Expected: PASS.

---

### Task 7: Hard-first materialization

**Files:**
- Modify: `pkg/agent/qrm-plugins/cpu/dynamicpolicy/policy_advisor_handler.go`
- Modify: `pkg/agent/qrm-plugins/cpu/dynamicpolicy/policy_advisor_handler_test.go`
- Modify: `pkg/agent/qrm-plugins/cpu/dynamicpolicy/bulkhead`

- [ ] **Step 1: Write materialization invariant tests**

Add tests that inject an advisor response where dedicated would otherwise take a hard reclaim CPU. Assert:

```go
assert.True(t, finalReclaim.ContainsAll(hardReclaim.ToSliceNoSortInt()...))
assert.True(t, finalDedicated.Intersection(hardReclaim).IsEmpty())
```

Also assert failure does not mutate live state.

- [ ] **Step 2: Run focused materialization tests**

Run: `MOCKEY_CHECK_GCFLAGS=false go test ./pkg/agent/qrm-plugins/cpu/dynamicpolicy -run 'Test.*Hard.*Reclaim|Test.*Materialize' -count=1 -timeout=30m`

Expected: FAIL because current order materializes reclaim last.

- [ ] **Step 3: Pre-bind hard reclaim**

Change materialization order:

```text
read hard reclaim from committed reclaim pool AllocationResult and active ramp-up scope
validate block result >= hard size
assign hard CPUs to reclaim block first
remove hard CPUs from available/source candidates
materialize source pool, dedicated, shared
top up reclaim with non-hard CPUs
validate final reclaim includes hard
```

Add this comment:

```go
// Hard reclaim is assigned before any primary block so a later source-pool carve
// or dedicated allocation cannot steal the CPUs required for bulkhead isolation.
```

- [ ] **Step 4: Pass candidate entries into source-pool helpers**

Change helper signatures from implicit live-state reads to explicit candidate arguments:

```go
deriveAdvisorIsolationSourcePool(block, candidateEntries)
```

Exclude all hard reclaim CPUs from carve candidates.

- [ ] **Step 5: Run QRM advisor suite**

Run: `MOCKEY_CHECK_GCFLAGS=false go test ./pkg/agent/qrm-plugins/cpu/dynamicpolicy -run 'Test.*ApplyBlocks|Test.*SourcePool|Test.*Hard' -count=1 -timeout=30m`

Expected: PASS.

---

### Task 8: Bulkhead committed-state application

**Files:**
- Modify: `pkg/agent/qrm-plugins/cpu/dynamicpolicy/util/cpuset_adjustment.go`
- Modify: `pkg/agent/qrm-plugins/cpu/dynamicpolicy/cpuset_adjustment_handler.go`
- Modify: `pkg/agent/qrm-plugins/cpu/dynamicpolicy/bulkhead/manager.go`
- Modify: `pkg/agent/qrm-plugins/cpu/dynamicpolicy/bulkhead`
- Modify: `pkg/agent/qrm-plugins/cpu/dynamicpolicy/policy_cpuset_adjustment_test.go`

- [ ] **Step 1: Write committed-state deferred test**

Simulate a bulkhead handler returning `FullyConverged=false` after state commit. Assert the committed QRM state is not rolled back and the apply path returns an explicit error so reconcile can retry against the same target state.

- [ ] **Step 2: Run focused bulkhead tests**

Run: `MOCKEY_CHECK_GCFLAGS=false go test ./pkg/agent/qrm-plugins/cpu/dynamicpolicy -run 'Test.*CPUSetAdjustment|Test.*FullyConverged|Test.*Bulkhead' -count=1 -timeout=30m`

Expected: FAIL because the context cannot yet distinguish committed-state apply from periodic best-effort reconcile.

- [ ] **Step 3: Add candidate runner**

Extend context:

```go
type CPUSetAdjustmentHandlerCtx struct {
    // existing fields
    State state.ReadonlyState
    Topology *machine.CPUTopology

    // RequireFullyConverged turns deferred physical writes into an explicit
    // apply error after state has been committed. The state remains the target
    // for later reconcile; bulkhead must not silently accept a partial partition.
    RequireFullyConverged bool
}
```

Add:

```go
func (p *DynamicPolicy) runCPUSetAdjustmentHandlersForState(
    ctx context.Context,
    target state.ReadonlyState,
    requireFullyConverged bool,
) error
```

Keep periodic reconcile using live state and `false`; allocation/advisor apply uses the newly committed state and `true`.

- [ ] **Step 4: Add hard fields to partition view**

Add `HardReclaim` and `HardReclaimPerNUMA`, populated from the committed reclaim pool `AllocationResult` and active ramp-up scope. Validate:

```text
HardReclaim ⊆ ReclaimRaw
HardReclaim ⊆ ReclaimEffective
HardReclaim ∩ Dedicated = empty
```

Normalization may still subtract ordinary transient primary overlap, but must error if the subtraction removes any hard reclaim CPU.

- [ ] **Step 5: Run bulkhead and QRM tests**

Run:

```bash
MOCKEY_CHECK_GCFLAGS=false go test ./pkg/agent/qrm-plugins/cpu/dynamicpolicy -run 'Test.*CPUSetAdjustment|Test.*Bulkhead|Test.*ApplyBlocks' -count=1 -timeout=30m
go test ./pkg/agent/qrm-plugins/cpu/dynamicpolicy/bulkhead/... -count=1
```

Expected: PASS.

---

### Task 9: Stable candidate and checkpoint migration

**Files:**
- Modify: `pkg/agent/qrm-plugins/cpu/dynamicpolicy/policy.go`
- Modify: `pkg/agent/qrm-plugins/cpu/dynamicpolicy/policy_advisor_handler.go`
- Modify: `pkg/agent/qrm-plugins/cpu/dynamicpolicy/state/state_mem.go`
- Modify: `pkg/agent/qrm-plugins/cpu/dynamicpolicy/policy_test.go`
- Modify: `pkg/agent/qrm-plugins/cpu/dynamicpolicy/state/state_test.go`

- [ ] **Step 1: Write lifecycle tests**

Cover:

```text
active request reports ramp_up=true
expired live entry reports stable request but retains hard owner
stable candidate clears RampUp and updates reclaim pool AllocationResult before materialization
stable candidate commit clears live hard before bulkhead applies the stable target
sidecar copies RampUp and topology but never owns reclaim pool AllocationResult
```

- [ ] **Step 2: Run lifecycle tests**

Run: `MOCKEY_CHECK_GCFLAGS=false go test ./pkg/agent/qrm-plugins/cpu/dynamicpolicy -run 'Test.*RampUp.*Lifecycle|Test.*Stable.*Candidate|Test.*Sidecar' -count=1 -timeout=30m`

Expected: FAIL until lifecycle changes are implemented.

- [ ] **Step 3: Implement stable candidate flow**

Use this order exactly:

```text
clone live state as stable candidate
clear RampUp on candidate
update reclaim pool AllocationResult to the stable target
materialize stable blocks against candidate
regenerate MachineState
commit candidate into live state
StoreState
run bulkhead with committed state and RequireFullyConverged=true
```

Add a block comment above the transition code explaining why QRM commits the stable target first and relies on bulkhead/reconcile to converge cgroups to that target without rolling state back.

- [ ] **Step 4: Add checkpoint validation defaults**

On restore, do not synthesize a hard reclaim target. If the feature is enabled and `RampUp=true` exists, validate the existing reclaim pool `AllocationResult` against the hard partition invariant.

Document in code:

```go
// Ramp-up hard reclaim is represented by the reclaim pool entry. Older
// checkpoints with RampUp=true are accepted only when the existing reclaim pool
// target satisfies the hard partition invariant; otherwise fail closed.
```

- [ ] **Step 5: Run lifecycle and state suites**

Run:

```bash
MOCKEY_CHECK_GCFLAGS=false go test ./pkg/agent/qrm-plugins/cpu/dynamicpolicy -run 'Test.*RampUp|Test.*Stable|Test.*Sidecar' -count=1 -timeout=30m
go test ./pkg/agent/qrm-plugins/cpu/dynamicpolicy/state -count=1
```

Expected: PASS.

---

### Task 10: Final integration and release blocker cleanup

**Files:**
- Modify: `go.mod`
- Modify: `go.sum`
- Modify: `docs/superpowers/specs/2026-07-30-ramp-up-reclaim-merged-evaluation.md` if the implementation diverges from the plan

- [ ] **Step 1: Replace local API dependency before CI-ready verification**

After the API branch is pushed and a pseudo-version is available, remove:

```text
replace github.com/kubewharf/katalyst-api => /Users/bytedance/go/src/github.com/kubewharf/katalyst-api-ramp-up-reclaim-bulkhead-integration
```

Use a remote `require` version instead.

- [ ] **Step 2: Run package verification**

Run:

```bash
go test ./pkg/config/agent/dynamic/adminqos/qrm -count=1
go test ./cmd/katalyst-agent/app/options/dynamic/adminqos/qrm -count=1
go test ./pkg/config/agent/dynamic/adminqos/advisor -count=1
go test ./pkg/agent/qrm-plugins/cpu/dynamicpolicy/state -count=1
go test ./pkg/agent/sysadvisor/plugin/qosaware/resource/cpu/assembler/... -count=1
MOCKEY_CHECK_GCFLAGS=false go test ./pkg/agent/qrm-plugins/cpu/dynamicpolicy -count=1 -timeout=30m
```

Expected: PASS.

- [ ] **Step 3: Run no-local-replace check**

Run: `go mod edit -json | grep -q '/Users/' && exit 1 || exit 0`

Expected: PASS with exit code 0 after remote API dependency is available.

- [ ] **Step 4: Review readability before final commit**

Check these high-risk areas manually:

```text
hard reclaim ownership comments
candidate/live state separation comments
stable transition ordering comments
ratio/cap fail-closed errors
source-pool helper signatures
bulkhead normalization hard reclaim errors
```

Expected: no complex hard-partition or candidate-commit logic is left without an invariant-oriented comment.

- [ ] **Step 5: Commit in logical groups**

Suggested commit groups:

```bash
git commit -m "feat(config): add ramp-up reclaim hard partition config"
git commit -m "feat(cpu): persist ramp-up hard reclaim reservation"
git commit -m "feat(cpu): plan initial ramp-up reclaim partitions"
git commit -m "feat(sysadvisor): preserve ramp-up reclaim bootstrap blocks"
git commit -m "feat(cpu): apply hard reclaim partitions atomically"
git commit -m "test(cpu): cover ramp-up reclaim hard partition invariants"
```

---

## Self-review checklist

- [ ] The plan covers API/core config, QRM state, QRM planner, SysAdvisor, materialization, bulkhead, lifecycle, checkpoint migration, and CI dependency cleanup.
- [ ] Every task has focused tests before implementation.
- [ ] Every complex state transition requires invariant-oriented comments.
- [ ] The global hard partition switch is not considered safe to enable until shared, SNB, non-exclusive DNB, and exclusive DNB all pass tests.
- [ ] Candidate helpers are explicitly passed candidate state and do not read live state.
- [ ] No task permits silent clamp, fallback overlap, or silently successful partial bulkhead apply.

# Dedicated Reclaim NUMA Isolation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpower-subagent-driven-development (recommended) or superpower-executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ensure dedicated-overlap disablement produces only NUMA-local reclaim targets that QRM can allocate without dedicated CPUs.

**Architecture:** SysAdvisor removes dedicated reclaim surplus from reclaim size when the isolation flag is enabled and rejects a NUMA whose reclaim reserve cannot be satisfied. QRM validates every NUMA-aware reclaim block against the remaining exclusive CPU set before `TakeByTopology`.

**Tech Stack:** Go, SysAdvisor provision assembler, QRM DynamicPolicy, Testify.

---

### Task 1: SysAdvisor NUMA-local capacity

**Files:**
- Modify: `pkg/agent/sysadvisor/plugin/qosaware/resource/cpu/assembler/provisionassembler/assembler_common.go`
- Test: `pkg/agent/sysadvisor/plugin/qosaware/resource/cpu/assembler/provisionassembler/assembler_common_test.go`

- [ ] **Step 1: Write failing tests**

Add table cases for overlap and non-overlap reclaim calculation with dedicated reclaim surplus and `DisableDedicatedCoresOverlapReclaimedCores=true`. Assert the resulting reclaim size excludes the dedicated surplus but retains `reservedForReclaim`; assert a dedicated-only NUMA with a positive reserve returns an error.

- [ ] **Step 2: Run the focused test**

Run: `go test ./pkg/agent/sysadvisor/plugin/qosaware/resource/cpu/assembler/provisionassembler -run 'Test.*Dedicated.*Reclaim' -count=1`

Expected: FAIL because the current calculation includes `dedicatedReclaimCoresSize`.

- [ ] **Step 3: Implement SysAdvisor capacity separation**

Condition dedicated reclaim surplus on `!DisableDedicatedCoresOverlapReclaimedCores` in both reclaim calculation paths. Validate the NUMA-local exclusive reclaim capacity against `reservedForReclaim` before publishing a pool entry.

- [ ] **Step 4: Run focused and package tests**

Run: `go test ./pkg/agent/sysadvisor/plugin/qosaware/resource/cpu/assembler/provisionassembler -count=1`

Expected: PASS.

### Task 2: QRM reclaim execution guard

**Files:**
- Modify: `pkg/agent/qrm-plugins/cpu/dynamicpolicy/policy_advisor_handler.go`
- Test: `pkg/agent/qrm-plugins/cpu/dynamicpolicy/policy_advisor_handler_test.go`

- [ ] **Step 1: Write a failing QRM test**

Construct a NUMA-aware reclaim block larger than `currentAvailableCPUs` and assert `generateReclaimBlockCPUSet` returns an error containing the NUMA id, requested size, and available size.

- [ ] **Step 2: Run the focused test**

Run: `go test ./pkg/agent/qrm-plugins/cpu/dynamicpolicy -run 'Test.*Reclaim.*Capacity' -count=1`

Expected: FAIL because the current error depends on the calculator and lacks the capacity diagnostic.

- [ ] **Step 3: Implement explicit capacity validation**

Before `TakeByTopology`, compare each NUMA-aware reclaim block result against `currentAvailableCPUs.Size()` and return a contextual error on insufficiency. Do not alter the advisor target or mutate state.

- [ ] **Step 4: Run focused and package tests**

Run: `go test ./pkg/agent/qrm-plugins/cpu/dynamicpolicy -count=1`

Expected: PASS.

### Task 3: End-to-end regression verification

**Files:**
- Test: `pkg/agent/sysadvisor/plugin/qosaware/resource/cpu/assembler/provisionassembler/assembler_common_test.go`
- Test: `pkg/agent/qrm-plugins/cpu/dynamicpolicy/policy_advisor_handler_test.go`

- [ ] **Step 1: Verify behavior matrix**

Cover `EnableReclaim` true and false, shared overlap true and false, and dedicated overlap disabled. Confirm the reserve remains NUMA-local, dedicated overlap metadata is absent, and QRM rejects impossible blocks.

- [ ] **Step 2: Run affected suites**

Run: `go test ./pkg/agent/sysadvisor/plugin/qosaware/resource/cpu/... ./pkg/agent/qrm-plugins/cpu/dynamicpolicy/... -count=1`

Expected: PASS.

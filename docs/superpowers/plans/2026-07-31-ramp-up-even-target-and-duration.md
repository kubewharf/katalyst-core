# Ramp-up Even Target and Duration Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpower-subagent-driven-development (recommended) or superpower-executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Round the ratio-derived ramp-up reclaim target down to an even CPU count and verify the 30-second ramp-up transition on a real node.

**Architecture:** Keep reserve, cap, and exclusive validation unchanged. Change only ratio-target rounding in the focused utility, then build the existing adapter/core integration and verify cold allocation plus transition timing with a NUMA-exclusive DNB probe.

**Tech Stack:** Go, testify, Katalyst QRM state, cgroup v1 E2E scripts.

---

### Task 1: Even ratio target

**Files:**
- Modify: `pkg/agent/qrm-plugins/cpu/dynamicpolicy/util/ramp_up_reclaim_test.go`
- Modify: `pkg/agent/qrm-plugins/cpu/dynamicpolicy/util/ramp_up_reclaim.go`

- [ ] Change the `eligible=20, ratio=0.26` expectation from `6` to `4`.
- [ ] Add an `eligible=96, ratio=0.2, reserve=4, cap=95` case expecting `18`.
- [ ] Run `go test ./pkg/agent/qrm-plugins/cpu/dynamicpolicy/util -run TestCalculateRampUpReclaimTarget -count=1` and confirm the old `ceil` implementation fails.
- [ ] Replace `math.Ceil(ratio*eligible)` with `math.Floor((ratio*eligible)/2)*2`, then retain `max(reserve, ratioTarget)`.
- [ ] Run the focused utility test and dynamic-policy test suite.

### Task 2: Build and deploy

**Files:**
- Temporarily modify and restore: `katalyst-adapter/go.mod`
- Build: `katalyst-adapter/output/agent.ramp-up-even-duration-e2e`

- [ ] Point adapter core/api replacements at the current local integration worktrees.
- [ ] Build `GOOS=linux GOARCH=amd64 GOFLAGS=-tags=SKIPCGO`.
- [ ] Restore `go.mod` and verify no temporary replacement remains.
- [ ] Upload through the architecture jump host, deploy to QRM and sysadvisor, and verify both `/proc/<pid>/exe` SHA values.

### Task 3: Real-node timing probe

**Files:**
- Create temporary probe under the TRAE working directory.
- Save final evidence under `qrm-bulkhead-test-artifacts/`.

- [ ] Reset, switch target to ratio `0.2`, and verify runtime flags.
- [ ] Create a fresh NUMA-exclusive DNB Pod.
- [ ] Capture the cold `ramp_up=true` state and verify `eligible=96`, `hard=18`, `DNB=78`, disjoint union equals eligible.
- [ ] Poll state every 10 ms, derive elapsed time from `InitTimestamp`, and verify dedicated ramp-up finishes on the first advisor result before the generic 30-second transition period.
- [ ] Run the strict stable target node check.
- [ ] Delete the Pod, verify remaining zero and pre-occupation classification, then final reset.
- [ ] Package logs/state, transfer through the jump host, and verify SHA, size, and tar integrity.

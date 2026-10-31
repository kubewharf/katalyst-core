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

// Package dynamicpolicy implements the CPU dynamic QRM policy: admission
// (shared/dedicated/reclaimed/system cores allocation handlers), the CPU-advisor
// desired-view reconciliation loop, hard-partition / hard-reclaim planning,
// steady fake-NUMA migration, and the crash-safe advisor post-commit state
// machine.
//
// This file pins the long-lived architectural boundaries that later refactors
// MUST NOT silently cross. They were established by the bulkhead refactor
// (see docs/proposals/... bulkhead / sysadvisor design docs) and exist to keep
// the main-flow policy reviewable and testable without re-entering the
// bulkhead planning core.
//
// # Bulkhead boundary
//
// The main flow in this package (everything except the bulkhead/ subtree) may
// depend on the bulkhead subtree ONLY through the following surface:
//
//   - bulkhead.Manager (created by bulkhead.NewManager): the facade that owns
//     the per-periodical and per-cpuset-adjustment handler runs. The main flow
//     registers RunCPUSetAdjustmentHandlers and calls RunPeriodicalHandlers; it
//     MUST NOT call into bulkhead's internal planning/partitioning types.
//   - bulkhead.ErrTopologyDeferred: the sentinel an admission handler compares
//     against when bulkhead signals that CPU topology is not yet materialized.
//   - bulkhead/model.DesiredView: the read model the advisor block planner reads.
//   - bulkhead/utils (checkpointutils): the shared crash-safe checkpoint
//     primitives CheckpointCodec, CheckpointStore, NewFileCheckpointStore and
//     ClonePendingSharedProjection. Payload semantics (version gating, checksum,
//     revision-CAS, topology-bounds) stay in the caller's codec; the store owns
//     only the atomic file protocol.
//
// Do not import any other path under dynamicpolicy/bulkhead/ (e.g. its internal
// plan/partition/solver packages) from the main flow; doing so couples the
// reviewable main flow to bulkhead internals and breaks the sharded ownership.
//
// # Transient-state write gate
//
// Speculative planning runs on state.NewTransientState (a read-only snapshot
// clone). Writes on it are gated by state.WritePermit: a transient state with a
// gate installed rejects any write that does not carry the matching permit, so a
// half-built plan can never be committed accidentally. The committed
// (stateCheckpoint) state enforces the same gate. See state/state_transient.go.
//
// # Bounded search
//
// Hard-reclaim and partition solving are exponential-search problems. Every
// such path carries an explicit operation/edge/retained-byte budget and returns
// a typed hardReclaimSearchBudgetExceeded / partition solver budget error
// instead of looping until OOM. These budgets are a DoS guard, not tuning
// knobs; do not remove them or grow them without a corresponding test that
// proves worst-case node sizes stay bounded.
package dynamicpolicy

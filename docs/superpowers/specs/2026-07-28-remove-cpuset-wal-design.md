# Remove CPUSet WAL Design

## Goal

Restore the CPU advisor update model to the no-WAL behavior of `kubewharf/main`, while retaining this branch's dedicated-overlap, isolation, and pure advisor block-planning changes.

## Boundary

This is not a branch reset. The implementation removes only the CPUSet write-ahead transaction introduced on this branch.

Retained:

- dedicated-core/reclaimed-core overlap changes;
- dedicated isolation state and configuration;
- pure `planBlocks` calculation;
- current allocation hooks and pool-size metrics;
- normal checkpointed committed state.

Removed:

- pending CPUSet transaction, admission, ownership, target-state, and target-cgroup checkpoint data;
- transaction WAL version and transaction IDs;
- transaction execution gate;
- transaction controller and its tests;
- advisor digest, pending-WAL recovery, replan, and replay paths;
- WAL compatibility and legacy transaction cleanup paths.

## Advisor Apply Behavior

`applyBlocks` runs as a single live reconciliation attempt:

1. Read a committed state snapshot.
2. Calculate the planned target state with `planBlocks`.
3. Emit pool metrics and invoke allocation hooks.
4. Rebuild target machine state after hooks.
5. Resolve changed main-container cgroup paths from the current target.
6. Apply each target CPUSet and synchronously read it back.
7. If every target matches, set and persist target pod entries and machine state.

If resolving a target, writing a cgroup, or reading it back fails or mismatches, return an error without updating committed state. Already-written cgroups are not persisted as pending work, replayed after restart, or compensated.

## State Model

`CPUPluginCheckpoint` stores only normal committed CPU state:

- policy and machine state;
- NUMA headroom and pod entries;
- overlap/isolation settings;
- state revision and existing advisor auxiliary desired state.

It does not store pending transaction state, admission reservations, ownership reservations, target CPUSet paths, or a transaction WAL version.

The State/ReadonlyState interfaces expose committed state, normal writers, dedicated isolation accessors, and auxiliary desired-state mutation only. They do not expose transaction lifecycle, pending transaction, ownership, or execution-lock methods.

## Remote Main Reference

Use `kubewharf/main` as the source of truth for the pre-WAL state/checkpoint shapes and baseline advisor apply behavior. Do not reset the branch or overwrite unrelated branch changes.

When the main implementation conflicts with current branch behavior, preserve:

- planner extraction;
- current dedicated-overlap semantics;
- allocation hooks;
- cgroup target filtering to changed main containers.

## Tests

Remove transaction-controller, pending-WAL checkpoint, restart recovery, WAL replay, transaction-ID/digest, and WAL mock tests.

Add or retain tests that verify:

- successful advisor apply writes/read-backs all resolved targets and commits planned state;
- cgroup apply failure or read-back mismatch leaves committed state unchanged;
- pool and sidecar entries are not resolved as cgroup targets;
- checkpoint round-trip restores committed state without transaction fields;
- full dynamicpolicy and state suites remain race-clean.

## Verification

Run formatting, state tests, focused advisor apply tests, full dynamicpolicy tests, and dynamicpolicy race tests. Search the dynamicpolicy subtree for deleted WAL symbols and confirm no production references remain.

# Initial Ramp-Up Reclaim CPUSet Design

> Superseded by `2026-07-30-ramp-up-reclaim-complete-design.md` and `2026-07-30-ramp-up-reclaim-merged-evaluation.md`.
> This early draft is retained only as historical context. Do not implement from this file directly: it predates `EnableRampUpReclaimHardPartition`, reclaim-pool `AllocationResult` reuse, committed-state bulkhead apply, and the synchronous `GetAdvice` first-phase decision.

## Goal

Add `InitialRampUpReclaimCPUSetRatio` so shared and dedicated workloads reserve a predictable reclaim CPUSet during ramp-up without violating per-NUMA reclaim reserves or dedicated/reclaim isolation.

## Configuration

`InitialRampUpReclaimCPUSetRatio` is a dynamic float configuration in `[0, 1]`.

It defines the reclaim CPUSet ratio during Pod ramp-up:

- regular `shared_cores`: ratio of machine allocatable CPUs;
- shared NUMA binding (SNB): ratio of allocatable CPUs in each bound NUMA;
- dedicated NUMA binding (DNB): ratio of allocatable CPUs in each bound NUMA.

The ratio affects shared workloads only when `DisableSharedCoresRampUp=false`.

For each NUMA, the reclaim target is never smaller than `reservedForReclaim`:

```text
numaTarget = max(
  reservedForReclaim[numa],
  ceil(InitialRampUpReclaimCPUSetRatio * numaAllocatableCPUCount),
)
```

For regular shared workloads, the machine target is:

```text
machineTarget = ceil(InitialRampUpReclaimCPUSetRatio * machineAllocatableCPUCount)
```

The selected machine reclaim CPUSet must also satisfy every NUMA-local reserve. A NUMA deficit is never transferred to another NUMA.

## Workload Behavior

### Regular shared cores

When ramp-up is enabled, a new Pod uses the pooled CPUSet outside the initial reclaim reservation. The reclaim CPUSet target is based on machine allocatable CPUs and must preserve every NUMA-local reserve.

When `DisableSharedCoresRampUp=true`, the ratio is ignored and the existing direct target-pool behavior remains unchanged.

### Shared NUMA binding

When ramp-up is enabled, an SNB Pod changes only its bound NUMA nodes. Each bound NUMA keeps its independently calculated initial reclaim target. The SNB allocation uses the remaining shared CPUSet in those NUMA nodes.

When shared ramp-up is disabled, the ratio is ignored and existing SNB pool allocation behavior remains unchanged.

### Non-exclusive dedicated NUMA binding

During ramp-up:

- reclaim in every bound NUMA may shrink to its initial target;
- the DNB Pod receives exactly its requested CPU count;
- DNB CPUs are selected outside the initial reclaim CPUSet;
- the request fails if the bound NUMA cannot satisfy both the Pod request and the initial reclaim target.

No deficit is borrowed from another NUMA.

### Exclusive dedicated NUMA binding

During ramp-up, each bound NUMA is divided into exactly two owners:

```text
reclaim = initial NUMA reclaim CPUSet
exclusive DNB = all remaining allocatable CPUs in the NUMA
```

The exclusive DNB receives the entire remaining NUMA, not merely its request. No other shared or dedicated workload may enter that NUMA.

The request fails when the NUMA cannot preserve its initial reclaim target.

## Overlap Rules

- `AllowSharedCoresOverlapReclaimedCores` controls stable shared/reclaim overlap.
- `DisableDedicatedCoresOverlapReclaimedCores` controls stable dedicated/reclaim overlap.
- Ramp-up initial reclaim reservation is explicit capacity, not implicit dedicated overlap.
- A non-exclusive DNB must not consume its initial reclaim CPUSet.
- An exclusive DNB owns the NUMA remainder while reclaim owns the initial reservation.

## State Transition

QRM performs the initial ramp-up allocation atomically:

1. Determine the workload type and calculation scope.
2. Calculate the per-NUMA initial reclaim targets.
3. Plan the new reclaim CPUSet and Pod CPUSet.
4. Validate all NUMA-local capacity constraints.
5. Update Pod entries and machine state only after the complete plan succeeds.

SysAdvisor consumes `RampUp=true` state and must not expand reclaim through dedicated overlap while the workload is ramping up. After ramp-up finishes, the existing advisor provision flow converges to the stable policy.

## Failure Semantics

- A NUMA that cannot meet its target rejects the allocation or advisor update.
- The target is never reduced below `reservedForReclaim`.
- Deficits are never moved across NUMA nodes.
- Failed planning does not partially shrink reclaim or update Pod allocation state.
- QRM retains its advisor reclaim-block capacity validation as a final execution guard.

## Tests

Cover:

- ratio validation and dynamic configuration propagation;
- regular shared machine-ratio ramp-up with per-NUMA reserve floors;
- `DisableSharedCoresRampUp=true` ignoring the ratio;
- SNB per-NUMA targets;
- non-exclusive DNB receiving request CPUs outside reclaim;
- non-exclusive DNB capacity rejection;
- exclusive DNB receiving the complete NUMA remainder;
- exclusive DNB capacity rejection;
- multiple NUMA nodes with no cross-NUMA deficit transfer;
- stable behavior after ramp-up completion;
- compatibility when the ratio is zero.

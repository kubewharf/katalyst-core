# Dedicated Reclaim NUMA Isolation Design

## Goal

When `DisableDedicatedCoresOverlapReclaimedCores` is enabled, prevent reclaimed workloads from using dedicated CPUs while preserving the configured `reservedForReclaim` CPUSet on every NUMA node.

## Rules

- Reclaim capacity is validated independently for each NUMA node.
- `reservedForReclaim` is a hard per-NUMA minimum and cannot be borrowed from another NUMA node.
- Dedicated CPUs never contribute reclaim CPUSet capacity when dedicated overlap is disabled.
- Shared overlap remains controlled solely by `AllowSharedCoresOverlapReclaimedCores`.
- `EnableReclaim=false` keeps reclaim CPUSet capacity at the NUMA-local `reservedForReclaim` minimum.
- `EnableReclaim=true` may expand reclaim capacity only with CPUs that can be allocated without dedicated overlap.

## Architecture

SysAdvisor remains the source of truth for reclaim capacity. Its provision assembler must remove dedicated reclaim surplus from the reclaim size whenever dedicated overlap is disabled, while continuing to publish shared overlap metadata where allowed.

QRM remains an execution guard. Before allocating a NUMA-aware reclaim block, it validates that the advisor-requested size fits the NUMA-local CPU set remaining after dedicated, shared, reserve, and non-reclaimable allocations. It returns a diagnostic error instead of silently shrinking the block.

## Error Handling

If a NUMA-local reclaim target cannot satisfy `reservedForReclaim`, SysAdvisor returns a capacity error. If an invalid advisor response still reaches QRM, QRM rejects it before state mutation. Neither layer creates an implicit dedicated overlap or borrows CPUs across NUMA nodes.

## Validation

- Dedicated-only NUMA with a nonzero reclaim reserve fails clearly when dedicated overlap is disabled.
- Shared overlap may still reduce exclusive reclaim allocation, but never permits dedicated overlap.
- Default configuration preserves legacy dedicated overlap behavior.
- QRM refuses reclaim blocks larger than NUMA-local exclusive capacity.

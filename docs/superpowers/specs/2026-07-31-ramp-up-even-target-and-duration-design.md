# Ramp-up reclaim even target and duration

## Behavior

`CalculateRampUpReclaimTarget` computes the ratio-derived target as:

```text
ratioTarget = floor((ratio * eligible) / 2) * 2
target = max(reserve, ratioTarget)
```

Only the ratio-derived value is rounded down. The reserve floor is never
rounded down. Existing ratio validation, positive-target validation, cap
validation, and exclusive-remainder validation remain unchanged.

Examples:

```text
eligible=96 ratio=0.2 reserve<=4 -> ratioTarget=18 target=18
eligible=20 ratio=0.26 reserve=1 -> ratioTarget=4 target=4
eligible=20 ratio=0.1 reserve=4 -> ratioTarget=2 target=4
```

## Tests

Unit tests cover odd raw ratio products, reserve dominance, zero ratio,
cap rejection, and exclusive remainder rejection.

The real-node probe uses a fresh NUMA-exclusive DNB Pod and captures:

- runtime flags and agent SHA;
- the cold `ramp_up=true` checkpoint;
- eligible, hard-reclaim, and exclusive-DNB CPU sets;
- `InitTimestamp`;
- the first observed `ramp_up=false` timestamp;
- stable target node-check status;
- Pod cleanup, pre-occupation classification, and final reset.

For 96 eligible CPUs, expected cold hard reclaim is 18 and the exclusive DNB
remainder is 78.

## Duration

QRM uses `transitionPeriod=30s` to decide the ramp-up phase sent in advisor
requests. Dedicated allocations have a more specific rule in the advisor
response path: ramp-up finishes when the first dedicated advisor result is
applied.

The DNB probe measures elapsed time from the persisted allocation
`InitTimestamp`, not from Pod creation. Pass criteria:

```text
cold ramp_up=true checkpoint is observed
first advisor result changes ramp_up to false
the measured duration is less than the 30s generic transition period
```

The probe records all timestamps. A 30-second minimum is not expected for
dedicated/DNB workloads under the current implementation.

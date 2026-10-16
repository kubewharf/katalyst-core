# Trace Runtime Behavior

## When To Use

Use this playbook for questions like "where does this happen" or "why is this branch taken".

## Steps

1. Identify the owning binary in `cmd/`.
2. Find the registration or options wiring in `cmd/.../app/`.
3. Trace into the owning implementation package under `pkg/`.
4. Check related config structs under `pkg/config/`.
5. Verify metrics, logs, and tests near the code path.
6. If the path is still unclear, use the local entrypoint-finder skill spec or the repo-local extension-point rule before broadening the search.

## Useful Anchors

- agent behavior often flows through `pkg/metaserver/`, `pkg/agent/`, and `pkg/config/agent/`
- controller behavior often flows through `pkg/controller/` and `cmd/katalyst-controller/app/`
- scheduler behavior often flows through `pkg/scheduler/`
- shared binary wiring often lives under `cmd/base/`
- flag and options plumbing often shows up in `options/` packages, `NewOptions`, or `AddFlags`
- behavior toggles often appear in enablement wiring before the final implementation path

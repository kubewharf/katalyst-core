# Repo Structure

`katalyst-core` is the upstream runtime repo of the Katalyst stack.

## When To Use

Use this rule when deciding where a change should live inside `katalyst-core` or whether it belongs upstream in `katalyst-api`.

## Main Areas

- `cmd/katalyst-{agent,controller,scheduler,webhook,metric}/` wires each binary.
- `pkg/agent/` contains node-local runtime logic and QRM-related code.
- `pkg/controller/` contains reconciler implementations.
- `pkg/scheduler/` contains scheduler framework logic.
- `pkg/metaserver/` contains node-local cached metadata and metrics views.
- `pkg/config/` contains config structs and options wiring.
- `build/` contains binary and image build entrypoints.

## Guidance

- Put shared API surface in `katalyst-api`, not here.
- Keep logic in the owning runtime layer.
- Keep neutral helpers in `pkg/util/`, `pkg/consts/`, or `pkg/config/` only when they remain leaf-like.

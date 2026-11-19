# Add Controller Or Plugin

## When To Use

Use this playbook for new controllers, QRM plugins, or similar extension points.

## Steps

1. Confirm the feature belongs in `katalyst-core`.
2. Add implementation in the owning package under `pkg/`.
3. Register in the owning extension point — wire into the matching `cmd/.../app/` path where applicable. Exception: scheduler plugins are registered with `app.WithPlugin` in the scheduler's main entrypoint (`cmd/katalyst-scheduler/main.go`), not via `cmd/.../app/`.
4. Add config structs or flags under `pkg/config/` and options wiring if needed.
5. Add focused tests for registration and one core behavior path.
6. Run `make fmt-strict`, `make vet`, and `make test`. Also run focused tests for changed `cmd/**` wiring and any touched package excluded from `make test`.

## Watch For

- putting public cross-repo schema or plugin contracts in `katalyst-core`
- implementing code but forgetting registration
- adding cross-layer imports

# Build Test And CI

Use the root `Makefile` as the main entrypoint.

## When To Use

Use this rule when selecting validation or build commands for runtime changes in `katalyst-core`.

## Main Local Checks

- `make fmt-strict`
- `make vet`
- `make test` for most `pkg/` changes

## Main Build Targets

- `make agent`
- `make controller`
- `make scheduler`
- `make webhook`
- `make metric`
- `make all-binaries`

## Keep In Mind

- `make test` is broader than a quick `go test` and matches repo expectations better for most runtime changes.
- `make test` excludes `pkg/scheduler`, `pkg/controller/resource-recommend`, and `pkg/util/resource-recommend`.
- Changes under those excluded paths need focused package-level validation in addition to the main local gate.
- CI also enforces `paralleltest`, build steps, and license checks.
- If a change only touches one subsystem, focused package tests are still useful before the broader local gate.

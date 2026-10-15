# Build Test And CI

Use the root `Makefile` as the main entrypoint.

## When To Use

Use this rule when selecting validation or build commands for runtime changes in `katalyst-core`.

## Main Local Checks

- `make fmt-strict`
- `make vet`
- `make test` for the selected `pkg/` package set
- focused `go test` commands for changed `cmd/**` packages and excluded `pkg/` trees

## Main Build Targets

- `make agent`
- `make controller`
- `make scheduler`
- `make webhook`
- `make metric`
- `make all-binaries`

## Keep In Mind

- `make test` runs race and coverage options over a selected `pkg/` package set; it is not a complete repository test gate.
- `make test` excludes all `cmd/**` packages plus `pkg/scheduler`, `pkg/controller/resource-recommend`, and `pkg/util/resource-recommend`.
- Changes under uncovered paths need focused package-level validation in addition to the main local gate.
- CI also enforces `paralleltest`, build steps, and license checks.
- If a change only touches one subsystem, focused package tests are still useful before the broader local gate.

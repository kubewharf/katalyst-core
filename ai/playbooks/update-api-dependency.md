# Update API Dependency

## When To Use

Use this playbook when `katalyst-core` must consume a new `katalyst-api` release.

## Steps

1. Confirm the shared schema or protocol change has landed in `katalyst-api`.
2. Bump the `katalyst-api` version in `go.mod`.
3. Run `go mod tidy`.
4. Update consuming logic in the relevant `pkg/` and `cmd/` paths.
5. Add or update focused tests.
6. Run `make fmt-strict`, `make vet`, and relevant tests.

## Watch For

- depending on an API shape that only exists locally
- combining the version bump and unrelated behavior changes without a clear review story

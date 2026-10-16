# Generated Code And Protos

Generated files must be regenerated, not patched by hand.

## When To Use

Use this rule when touching generated files, protobuf contracts, or cross-repo protocol boundaries.

## Generated Surfaces

- `zz_generated_*.go` (separate generation path — see repo scripts / `hack/`)
- `*.pb.go`
- `*.pb_test.go`
- mockgen outputs such as `provider_mock.go` (regenerate via mockgen, not `make generate-pb`)

## Guidance

- Edit the source-of-truth file first.
- Use `make generate` or `make generate-pb` to regenerate protobuf `*.pb.go` files only. These targets do not regenerate `zz_generated_*.go`.
- If a shared type or protocol should exist across repos, land it in `katalyst-api` first and bump the pin here afterward.
- Keep protocol changes backward-compatible.

## Keep In Mind

If a generated diff looks surprising, re-check the source edit and generation path before committing it.

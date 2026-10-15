# Generated Code And Protos

Files with an in-repo regeneration workflow must be regenerated, not patched by hand; generator-origin tests without a current generator need separate handling.

## When To Use

Use this rule when touching generated files, protobuf contracts, or cross-repo protocol boundaries.

## Generated Surfaces

- `*.pb.go`
- mockgen outputs such as `provider_mock.go` (regenerate via the `go:generate mockgen` directive in `pkg/agent/orm/server/podresources/types.go`, not `make generate-pb`)
- `*.pb_test.go` files are generator-origin tests, but no in-repo command currently regenerates them

## Guidance

- Edit the source-of-truth file first.
- Use `make generate` or `make generate-pb` to regenerate protobuf `*.pb.go` files only. These targets do not regenerate `*.pb_test.go` or mockgen outputs.
- Preserve generated headers in `*.pb_test.go`, but review and validate necessary changes as test code until a reproducible generation command is added.
- If a public schema or plugin contract is owned across repositories, land it in `katalyst-api` first and bump the pin here afterward. Do not move a core-owned implementation protocol merely because multiple core components consume it.
- Keep protocol changes backward-compatible.

## Keep In Mind

- CodeRabbit intentionally keeps its default `*.pb.go` exclusion. Review the owning `.proto` source, run the applicable protobuf generator, and inspect the generated diff locally instead of relying on AI review of generator output.
- Protocol generation requires network access plus external `git`, `protoc`, and `protoc-gen-gogo` executables on `PATH`.
- Not all external generator inputs and tools are pinned; review every generated diff and re-check surprising output before committing it.

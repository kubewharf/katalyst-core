# Repository Guidelines

## 1. Overview

`katalyst-core` (module `github.com/kubewharf/katalyst-core`) is the upstream implementation of the Katalyst QoS resource-management stack: node agent, control-plane controllers, custom scheduler, admission webhook, and custom-metric server. It depends on `katalyst-api` for CRD types, constants, and plugin protocols; internal downstream repos consume both upstream repos and add site-specific behavior on top.

## How To Use This File

- Use `AGENTS.md` for repo contract, invariants, and the main local validation gate.
- Use `ai/rules/` for durable repo-specific constraints.
- Use `ai/playbooks/` for common change procedures.
- Use `ai/skills/` for routing, entrypoint-finding, and validation-selection tasks.

## Local AI Layer

Use `ai/README.md` as the repo-local AI index. The local `ai/` tree holds stable `katalyst-core`-specific `rules/`, `playbooks/`, and `skills/` for structure, extension points, validation, and common implementation flows. Keep shared cross-repo policy in the workspace-level `ai/` layer, and keep volatile notes out of this repo-local layer.

## 2. Critical Rules

1. **Do not hand-edit artifacts with an in-repo regeneration workflow.** Regenerate `*.pb.go` and mockgen outputs such as `provider_mock.go` from their sources. The checked-in `*.pb_test.go` files are generator-origin tests but currently have no in-repo regeneration command; preserve their headers and review necessary changes as test code until a regeneration workflow is added.
2. **Keep public cross-repo API surface upstream.** CRD fields, canonical QoS constants/annotations, and public plugin contracts belong in `katalyst-api`. Core-owned implementation protocols, such as advisor services whose source and consumers live in this repo, remain in `katalyst-core` even when several core components use them.
3. **Avoid reverse or cyclic dependencies.** Never import any internal downstream repo; dependency flow is upstream to downstream only.
4. **Respect package boundaries.** Runtime layers (agent, controller, scheduler, webhook) should not cross-import implementation packages; keep any established shared-helper exception narrow and see section 6.
5. **Preserve backward compatibility deliberately.** Removing flags, config keys, exported symbols, or protocol fields requires an explicit compatibility and migration plan based on actual consumers; do not assume a fixed one-release deprecation window.
6. **Keep Apache-2.0 headers intact.** `make license` repairs drift.

## 3. Project Structure

- `cmd/katalyst-{agent,controller,scheduler,webhook,metric}/` — binary entrypoints and `app/` wiring.
- `pkg/agent/`, `pkg/controller/`, `pkg/scheduler/`, `pkg/webhook/` — primary runtime subsystems.
- `pkg/config/`, `pkg/metrics/`, `pkg/util/`, `pkg/consts/`, `pkg/client/`, `pkg/metaserver/` — shared support packages.
- `build/`, `hack/`, `docs/`, `examples/` — build, tooling, and reference material.

For detailed placement and extension-point routing, use `ai/rules/02-binaries-and-extension-points.md` or `ai/skills/katalyst-core-entrypoint-finder/SKILL.md`.

## 4. Build & Validation

### Local Gate

Before pushing, run the main local gate plus focused tests for paths it does not cover:

1. `make fmt-strict` — run `gofumpt -l -w .`; authoritative over `gofmt`.
2. `make vet` — run `go vet ./...`.
3. `make test` — runs `go test` with `-v -race -parallel=16 -p=16 -covermode=atomic -timeout=30m -coverprofile=coverage.txt -coverpkg=./... -gcflags=all="-l"` over selected `./pkg/...` packages. It excludes `pkg/scheduler`, `pkg/controller/resource-recommend`, and `pkg/util/resource-recommend`, and it does not test `cmd/**`.
4. Focused `go test` commands for every touched excluded package and for changed `cmd/**` packages.

### How to Build

- `make agent`, `make controller`, `make scheduler`, `make webhook`, or `make metric` — build one binary via `build-binaries TARGET=<name>`.
- `make all-binaries` — build all five binaries.
- `make all-images` — build all Docker images.
- `make generate` / `make generate-pb` — regenerate protobuf-generated `*.pb.go` files only. These targets do not regenerate `*.pb_test.go` or mockgen outputs.

Protocol generation requires network access plus external `git`, `protoc`, and `protoc-gen-gogo` executables on `PATH`. Not all external generator inputs and tools are pinned, so review the generated diff before committing it.

### CI Notes

CI runs formatting, `vet`, `golangci-lint`, build, `paralleltest`, unit tests, and license checks; release workflows build and publish images for `master`, `main`, `release-*`, and published releases. `paralleltest` is enforced for most packages, with exclusions for `pkg/scheduler`, `pkg/controller/resource-recommend`, `pkg/util/resource-recommend`, `pkg/util/machine`, and `pkg/agent/qrm-plugins/sriov/policy`.

## 5. Coding & Editing Rules

- Go toolchain: `go.mod` is the single source of truth (Go 1.18); tabs for indentation.
- Use `goimports` with local prefix `github.com/kubewharf`.
- Package names are lower-case single words; exported identifiers use MixedCaps.
- `make license` repairs Apache-2.0 header drift (see section 2, rule 6).

## 6. Import & Dependency Rules

- Avoid imports from `pkg/agent/**` into controller, scheduler, or webhook implementation packages. The existing QoS-aware node-metric reporter uses the neutral helpers in `pkg/scheduler/util`; do not broaden that exception.
- Keep `pkg/consts/**` dependency-light. Changes in `pkg/util/**` and `pkg/config/**` should follow existing subsystem-specific dependencies and must not introduce dependency cycles or imports from unrelated runtime layers.
- Avoid dependencies between independent resource plugins. Shared QRM infrastructure such as `commonstate`, `advisorsvc`, and resource-specific subpackages may follow established imports.
- Reverse imports into any internal downstream repo are forbidden (see section 2, rule 3).

## 7. Generated & Vendored Artifacts

- Files with an in-repo regeneration workflow include `*.pb.go` and mockgen outputs such as `provider_mock.go`. `make generate` / `make generate-pb` regenerate only `*.pb.go`; regenerate `provider_mock.go` via the `go:generate mockgen` directive in `pkg/agent/orm/server/podresources/types.go`.
- CodeRabbit intentionally keeps its default `*.pb.go` exclusion. Review the owning `.proto` source and verify protobuf output by running the applicable generator and inspecting the generated diff; the exclusion does not relax the requirement to commit regenerated output.
- The checked-in `*.pb_test.go` files are generator-origin tests with no current in-repo generator. Preserve their generated headers, but review and validate necessary test changes normally rather than assuming `make generate-pb` will recreate them.
- `katalyst-api` is consumed via `go.mod` pin. See section 2, rule 2 for what belongs upstream.
- This repo does not vendor any downstream repo; downstream repos consume it via module pins or deployment artifacts.

## 8. Testing

- Keep tests beside the code under test: `foo.go` and `foo_test.go`.
- In packages covered by the CI `paralleltest` job, eligible top-level tests and subtests must call `t.Parallel()`. Follow analyzer exemptions such as tests using `t.Setenv`, and rebind table-loop variables before parallel subtests capture them.
- Prefer focused subsystem tests over broad integration-by-default coverage.

Use `ai/rules/05-testing-patterns.md` for detailed test-shape guidance and expected coverage patterns.

## 9. Logging, Errors & Metrics

- Follow the surrounding package's established `klog` style; both structured and formatted calls are common. Preserve stable subsystem prefixes and key names, keep metric tags bounded, and avoid `panic` in hot paths.
- Wrap returned errors with `%w` so `errors.Is` and `errors.As` continue to work.

Use `ai/rules/06-logging-metrics-and-compatibility.md` for detailed logging style, verbosity, metric, and compatibility expectations.

## 10. Versioning & Compatibility

Backward-compatibility policy for flags, config, plugin protocols, and exported symbols is stated in section 2, rule 5. Additional operational rules:

- Do not renumber protobuf fields or add `required` fields.
- Treat the Kubernetes versions pinned and replaced in `go.mod` as the compatibility baseline for the current branch.
- Bump the `katalyst-api` pin in its own commit before depending on new fields or protocol changes.

## 11. Commit & Release

- Conventional Commits are not enforced by tooling in this repo; follow upstream `kubewharf` project conventions.
- Release workflows build and publish images for `master`, `main`, `release-*`, and published releases.
- Ownership and contribution process: see `CODEOWNERS`, `MAINTAINERS.md`, `GOVERNANCE.md`, and `CONTRIBUTING.md`.

## 12. Repo-Local References

Use the repo-local `ai/` layer for detailed ownership, wiring, and validation guidance instead of duplicating it here:

- `ai/rules/01-repo-structure.md` — repo layout and placement rules for new code.
- `ai/rules/02-binaries-and-extension-points.md` — binary ownership and registration surfaces.
- `ai/rules/03-build-test-and-ci.md` — local gate, excluded `make test` paths, and CI expectations.
- `ai/rules/04-generated-code-and-protos.md` — generated-file and protobuf handling.
- `ai/rules/05-testing-patterns.md` and `ai/rules/06-logging-metrics-and-compatibility.md` — test, logging, metric, and compatibility expectations.
- `ai/playbooks/add-controller-or-plugin.md` — repo-local flow for controllers, plugins, webhooks, and similar extension points.
- `ai/playbooks/trace-runtime-behavior.md` — trace a runtime behavior back to the owning package and wiring.
- `ai/playbooks/update-api-dependency.md` — repo-local side of a cross-repo `katalyst-api` bump.
- `ai/skills/katalyst-core-entrypoint-finder/SKILL.md` — route a requested behavior change to the owning binary, package, and registration path.
- `ai/skills/katalyst-core-validation-selector/SKILL.md` — choose the right validation commands for the touched subsystem.

For stack-wide work, this repo owns the runtime-consumer step after `katalyst-api`: bump the API dependency here, wire the change into the relevant binary, and let downstream repos consume the released result.

## 13. Code Review Checklist

1. New logic lives in the correct process layer and respects the package-boundary rules in section 6.
2. Public cross-repo API surface stays in `katalyst-api`; core-owned implementation protocols remain with their core producers and consumers, and nothing adapter-owned is introduced here.
3. Logging, error wrapping, and metric tags follow the conventions in section 9.
4. Tests cover the changed behavior; eligible top-level tests and subtests in packages covered by the CI `paralleltest` job use `t.Parallel()`, with table-loop variables safely rebound. Intentionally serial suites remain in an excluded package or require an analyzer exemption or justified CI exclusion.
5. The main local gate (`make fmt-strict`, `make vet`, and `make test`) is clean.
6. Changes under paths not covered by `make test`, including `cmd/**`, `pkg/scheduler`, and the controller/util resource-recommend trees, also have focused package-level validation.
7. Breaking changes to flags, config, or protocols follow the compatibility rules in section 10.

## 14. Common Pitfalls

Concrete symptoms that signal a critical-rule violation:

- New dependencies between otherwise independent resource plugins, or new imports between agent and controller implementation packages without an established narrow exception (violates section 6).
- Metric tags containing unbounded pod identity such as pod UID or pod name, or namespace tags added without checking boundedness and metric scope (see section 9).
- Removing or changing an established subsystem prefix, or introducing logs that violate the package's local structured logging convention (see section 9).
- `panic` in a hot path (see section 9).
- Using Kubernetes APIs outside the versions pinned by `go.mod` without an explicit compatibility plan (violates section 10).

## 15. Appendix

### Extension Points

Use `ai/rules/02-binaries-and-extension-points.md` for the detailed extension-point map, or `ai/skills/katalyst-core-entrypoint-finder/SKILL.md` when you need the owning binary and registration path for a requested behavior change.

### Glossary

- **QRM** — QoS Resource Manager; agent subsystem that admits pods and allocates CPU, memory, network, IO, GPU, and related resources.
- **ORM** — Out-of-band Resource Manager; resource-allocation path parallel to QRM with kubelet device-provider and NRI integrations.
- **SysAdvisor** — subsystem that computes headroom, reclaimed capacity, provisioning targets, and related advice.
- **Metaserver** — node-local cache of pod, node, container, and metric data.
- **Metacache** — SysAdvisor cache of derived advice, separate from the metaserver's raw view.
- **Reporter** — agent component that publishes node state back to the control plane.
- **QoS level** — `reclaimed_cores`, `shared_cores`, `dedicated_cores`, `system_cores`, chosen by the `katalyst.kubewharf.io/qos_level` pod annotation.

### Agent Notes

- Grep, do not guess. Taint keys, eviction reasons, thresholds, annotation keys, and resource-name strings should be verified from source.

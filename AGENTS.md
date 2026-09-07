# Repository Guidelines

## 1. Overview

`katalyst-core` (module `github.com/kubewharf/katalyst-core`) is the upstream implementation of the Katalyst QoS resource-management stack: node agent, control-plane controllers, custom scheduler, admission webhook, and custom-metric server. It depends on `katalyst-api` for CRD types, constants, and plugin protocols; internal downstream repos consume both upstream repos and add site-specific behavior on top.

## 2. Critical Rules

1. **Never hand-edit generated artifacts.** Do not modify `zz_generated_*.go` or `*.pb.go`; regenerate them.
2. **Keep shared API surface upstream.** If a type, CRD field, QoS annotation, or gRPC message should be shared across binaries or customized downstream, it belongs in `katalyst-api`, not here.
3. **Avoid reverse or cyclic dependencies.** Never import any internal downstream repo; dependency flow is upstream to downstream only.
4. **Respect package boundaries.** Runtime layers (agent, controller, scheduler, webhook) must not cross-import each other; see section 6.
5. **Preserve backward compatibility.** Removing flags, config keys, or exported symbols is a breaking change; deprecate for one release first. Plugin protocol changes must remain backward-compatible.
6. **Keep Apache-2.0 headers intact.** `make license` repairs drift.

## 3. Project Structure

- `cmd/katalyst-{agent,controller,scheduler,webhook,metric}/` — each binary owns `main.go` plus an `app/` package that wires options and managers.
- `pkg/agent/` — node-local logic:
  - `qrm-plugins/{cpu,memory,network,io,sriov,gpu,mb}/` — QoS Resource Manager plugins.
  - `evictionmanager/plugin/` — pluggable eviction policies.
  - `orm/` — out-of-band Resource Manager via NRI.
  - `resourcemanager/{fetcher,reporter}/` — resource fetcher and node-state reporter.
  - `audit/`, `utilcomponent/` — auditing and shared agent components.
- `pkg/agent/sysadvisor/` — advisor core, `plugin/{qosaware,metric-emitter,inference,overcommitmentaware,poweraware}`, `metacache/`, and shared types.
- `pkg/controller/{vpa,ihpa,spd,kcc,overcommit,resource-recommend,...}` — CRD reconcilers.
- `pkg/scheduler/` — scheduler framework plugins.
- `pkg/webhook/`, `pkg/config/`, `pkg/metrics/`, `pkg/util/`, `pkg/consts/`, `pkg/client/`.
- `pkg/metaserver/` — node-local cache and metric provisioners used by agent-side components.
- `build/build.sh`, `build/dockerfiles/`, `hack/`, `docs/`, `examples/`.

## 4. Build & Validation

### Local Gate

Before pushing, run `make fmt-strict && make vet && make test`, i.e.:

1. `make fmt-strict` — run `gofumpt -l -w .`; authoritative over `gofmt`.
2. `make vet` — run `go vet ./...`.
3. `make test` — runs `go test` with `-v -race -parallel=16 -p=16 -covermode=atomic -timeout=30m -coverprofile=coverage.txt -coverpkg=./... -gcflags=all="-l"` over `./pkg/...`, excluding `pkg/scheduler`, `pkg/controller/resource-recommend`, and `pkg/util/resource-recommend`.

### How to Build

- `make agent`, `make controller`, `make scheduler`, `make webhook`, or `make metric` — build one binary via `build-binaries TARGET=<name>`.
- `make all-binaries` — build all five binaries.
- `make all-images` — build all Docker images.
- `make generate` / `make generate-pb` — regenerate generated sources instead of hand-editing them.

### CI Notes

CI runs formatting, `vet`, build, `paralleltest`, unit tests, and license checks; release workflows build and publish images for `master`, `main`, `release-*`, and published releases. `paralleltest` is enforced — every `t.Run` subtest must call `t.Parallel()`.

## 5. Coding & Editing Rules

- Go toolchain: `go.mod` is the single source of truth (Go 1.18); tabs for indentation.
- Use `goimports` with local prefix `github.com/kubewharf`.
- Package names are lower-case single words; exported identifiers use MixedCaps.
- `make license` repairs Apache-2.0 header drift (see section 2, rule 6).

## 6. Import & Dependency Rules

- `pkg/agent/**` must not import `pkg/controller/**`, `pkg/scheduler/**`, or `pkg/webhook/**`.
- `pkg/util/**`, `pkg/consts/**`, and `pkg/config/**` must remain leaf packages and must not import agent/controller/scheduler/webhook code.
- Plugin packages may depend only on parent interfaces plus neutral packages such as `pkg/metaserver`, `pkg/util`, `pkg/config`, and `katalyst-api` types; no cross-plugin imports.
- Reverse imports into any internal downstream repo are forbidden (see section 2, rule 3).

## 7. Generated & Vendored Artifacts

- Generated files owned by this repo (do not hand-edit — see section 2, rule 1): `zz_generated_*.go`, `*.pb.go`. Regenerate via `make generate` / `make generate-pb`.
- `katalyst-api` is consumed via `go.mod` pin. See section 2, rule 2 for what belongs upstream.
- This repo does not vendor any downstream repo; downstream repos consume it via module pins or deployment artifacts.

## 8. Testing

- Keep tests beside the code under test: `foo.go` and `foo_test.go`.
- Allowed test tooling includes stdlib `testing`, `testify`, `gomock`, `mockey`, and `gomonkey`.
- Every `t.Run` subtest must call `t.Parallel()`; CI enforces this with `paralleltest`.
- Prefer fake metaserver and metacache implementations over broad interface mocks.
- New plugins should include registration coverage plus one core path for that plugin type such as `Allocate`, `Evict`, `Report`, `GetTopologyHints`, or `RemovePod`.

## 9. Logging, Errors & Metrics

- Use `klog.InfoS`, `klog.ErrorS`, and `klog.V(N).InfoS`; avoid `klog.Infof` for new code.
- Prefix structured log messages with a subsystem label like `[cpu_plugin]`, and keep keys stable and lowerCamel, such as `pod`, `node`, `container`, `qosLevel`, `numaID`, and `resource`.
- Standard verbosity: `V(2)` for state changes, `V(4)` for per-pod details, `V(6)` for inner loops.
- Wrap returned errors with `%w` so `errors.Is` and `errors.As` continue to work.
- Emit metrics through `pkg/metrics` with bounded-cardinality tag sets; never use pod UID or namespace as a metric tag.
- Do not `panic` in hot paths; return an error and let the manager decide.

## 10. Versioning & Compatibility

Backward-compatibility policy for flags, config, plugin protocols, and exported symbols is stated in section 2, rule 5. Additional operational rules:

- Do not renumber protobuf fields or add `required` fields.
- Minimum Kubernetes is 1.20+. Gate post-1.24 behavior behind a feature flag.
- Bump the `katalyst-api` pin in its own commit before depending on new fields or protocol changes.

## 11. Commit & Release

- Conventional Commits are not enforced by tooling in this repo; follow upstream `kubewharf` project conventions.
- Release workflows build and publish images for `master`, `main`, `release-*`, and published releases.
- Ownership and contribution process: see `CODEOWNERS`, `MAINTAINERS.md`, `GOVERNANCE.md`, and `CONTRIBUTING.md`.

## 12. Cross-Repo Change Playbooks

### Stack Layering & Dependency Direction

The Katalyst stack is layered strictly from schema to rollout:

1. `katalyst-api` — shared schema, constants, and plugin protocols.
2. `katalyst-core` — upstream runtime binaries (agent, controller, scheduler, webhook, metric).
3. Internal downstream repos — internal-only repos consume the upstream releases and produce deployment artifacts. Their contents and workflows are out of scope for this file.

Dependency direction is one-way: internal downstream <- `katalyst-core` <- `katalyst-api`. Never introduce reverse or cyclic imports. Cross-repo changes always land in `katalyst-api` first, then `katalyst-core`, then internal downstream.

### Playbook Skeleton (shared step IDs)

Cross-repo changes follow a stable skeleton with numbered step IDs. Each repo below only lists its own delta.

- **CRD-1** — Design and land the CRD field in `katalyst-api`: edit `pkg/apis/<group>/<version>/types.go`, run `make generate`, add deepcopy + JSON round-trip tests. Keep the change additive.
- **CRD-2** — Bump the `katalyst-api` pin in `katalyst-core` and wire the field into the relevant agent/controller/scheduler/webhook logic; add focused unit tests.
- **CRD-3 / CRD-4** — Downstream integration is handled by internal repos; details are out of scope here.
- **QRM-1** — Land any new resource constants or plugin-protocol changes in `katalyst-api` (`pkg/consts/`, `pkg/protocol/`). Keep protobuf tags additive.
- **QRM-2** — Implement the upstream plugin in `katalyst-core/pkg/agent/qrm-plugins/<resource>/`, register it in `cmd/katalyst-agent/app/enableagents.go`, and add config wiring under `pkg/config/agent/qrm/`.
- **QRM-3 / QRM-4** — Downstream integration is handled by internal repos; details are out of scope here.
- **CTRL-1** — Ensure the CRD is released from `katalyst-api`.
- **CTRL-2** — Add the reconciler under `katalyst-core/pkg/controller/<name>/` and register it in `cmd/katalyst-controller/app/enablecontrollers.go`.
- **CTRL-3 / CTRL-4** — Downstream integration is handled by internal repos; details are out of scope here.

### katalyst-core delta

`katalyst-core` owns **CRD-2**, **QRM-2**, and **CTRL-2**. Concretely:

- **CRD-2**: bump the `katalyst-api` pin (its own commit), wire the field into the relevant controller / scheduler / agent logic, and add focused unit tests, usually with fake metaserver inputs where appropriate.
- **QRM-2**: create `pkg/agent/qrm-plugins/<resource>/` with the plugin implementation, state handling, and focused tests. Register the plugin in `cmd/katalyst-agent/app/enableagents.go`, defaulting the new flag to previous behavior (typically off for the first release). Add config wiring under `pkg/config/agent/qrm/`. Keep naming mirror-friendly so downstream overrides can live at the mirrored path in an internal repo.
- **CTRL-2**: add the reconciler package under `pkg/controller/<name>/`; register it in `cmd/katalyst-controller/app/enablecontrollers.go`; add reconcile tests with `envtest` or a fake client, depending on scope.

## 13. Code Review Checklist

1. New logic lives in the correct process layer and respects the package-boundary rules in section 6.
2. Shared API surface stays in `katalyst-api`; nothing adapter-owned is introduced here.
3. Logging, error wrapping, and metric tags follow the conventions in section 9.
4. Tests cover the changed behavior, and every `t.Run` uses `t.Parallel()`.
5. Local validation is clean: `make fmt-strict && make vet && make test`.
6. Breaking changes to flags, config, or protocols follow the compatibility rules in section 10.

## 14. Common Pitfalls

Concrete symptoms that signal a critical-rule violation:

- Cross-plugin imports inside `pkg/agent/qrm-plugins/**`, or agent-to-controller imports (violates section 6).
- Metric tags containing pod UID or namespace, blowing up cardinality (see section 9).
- Log lines without a subsystem prefix such as `[cpu_plugin]` (see section 9).
- `panic` in a hot path (see section 9).
- Using Kubernetes >= 1.24-only APIs without a feature flag (violates section 10).

## 15. Appendix

### Extension Points

| I want to... | Edit here |
| --- | --- |
| Add a QRM plugin | `pkg/agent/qrm-plugins/<resource>/` and register it in `cmd/katalyst-agent/app/` |
| Add an eviction policy | `pkg/agent/evictionmanager/plugin/<name>/` |
| Add a sysadvisor plugin or model | `pkg/agent/sysadvisor/plugin/<name>/` |
| Add a metric provisioner | `pkg/metaserver/agent/metric/provisioner/<name>/` |
| Add a scheduler plugin | `pkg/scheduler/plugins/<name>/` and register it in `cmd/katalyst-scheduler/app/` |
| Add a controller | `pkg/controller/<name>/` and register it in `cmd/katalyst-controller/app/` |
| Add a webhook | `pkg/webhook/{mutating,validating}/<name>/` |

### Glossary

- **QRM** — QoS Resource Manager; agent subsystem that admits pods and allocates CPU, memory, network, IO, GPU, and related resources.
- **ORM** — Out-of-band Resource Manager; NRI-based enforcement path parallel to QRM.
- **SysAdvisor** — subsystem that computes headroom, reclaimed capacity, provisioning targets, and related advice.
- **Metaserver** — node-local cache of pod, node, container, and metric data.
- **Metacache** — SysAdvisor cache of derived advice, separate from the metaserver's raw view.
- **Reporter** — agent component that publishes node state back to the control plane.
- **QoS level** — `reclaimed_cores`, `shared_cores`, `dedicated_cores`, `system_cores`, chosen by the `katalyst.kubewharf.io/qos_level` pod annotation.

### Agent Notes

- Grep, do not guess. Taint keys, eviction reasons, thresholds, annotation keys, and resource-name strings should be verified from source.

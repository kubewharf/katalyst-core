# Repository Guidelines

`katalyst-core` (module `github.com/kubewharf/katalyst-core`) is the upstream implementation of the Katalyst QoS resource-management stack: node agent, control-plane controllers, custom scheduler, admission webhook, and custom-metric server. It depends on `katalyst-api` for CRD types, constants, and plugin protocols; downstream `katalyst-adapter` vendors both and layers site-specific behavior on top.

## Project Structure

- `cmd/katalyst-{agent,controller,scheduler,webhook,metric}/` - each binary owns `main.go` plus an `app/` package that wires options and managers.
- `pkg/agent/` - node-local logic:
  - `qrm-plugins/{cpu,memory,network,io,sriov,gpu,mb}/` - QoS Resource Manager plugins.
  - `evictionmanager/plugin/` - pluggable eviction policies.
  - `orm/` - out-of-band Resource Manager via NRI.
  - `resourcemanager/`, `reporter/`, `audit/`.
- `pkg/agent/sysadvisor/` - advisor core, `plugin/{qosaware,metric-emitter,inference,overcommitmentaware,poweraware}`, `metacache/`, and shared types.
- `pkg/controller/{vpa,ihpa,spd,kcc,overcommit,resource-recommend,...}` - CRD reconcilers.
- `pkg/scheduler/` - scheduler framework plugins.
- `pkg/webhook/`, `pkg/config/`, `pkg/metrics/`, `pkg/util/`, `pkg/consts/`, `pkg/client/`.
- `pkg/metaserver/` - node-local cache and metric provisioners used by agent-side components.
- `build/build.sh`, `build/dockerfiles/`, `hack/`, `docs/`, `examples/`.

## Build And Validation

### Local Quick Checks

- `make fmt-strict` - run `gofumpt -l -w .`; this is authoritative over `gofmt`.
- `make vet` - run `go vet ./...`.
- `make test` - run `go test -v -race -coverprofile=coverage.txt ./...`.
- Local gate before pushing: `make fmt-strict && make vet && make test`.

### Build And Generate

- `make agent`, `make controller`, `make scheduler`, `make webhook`, or `make metric` - build one binary via `build-binaries TARGET=<name>`.
- `make all-binaries` - build all five binaries.
- `make all-images` - build all Docker images.
- `make generate` / `make generate-pb` - regenerate generated sources instead of hand-editing them.

## Critical Rules

- Go 1.18+ with tabs for indentation; use `goimports` with local prefix `github.com/kubewharf`.
- Keep Apache-2.0 headers intact; `make license` repairs drift.
- Package names are lower-case single words; exported identifiers use MixedCaps.
- Do not hand-edit `zz_generated_*.go` or `*.pb.go`; regenerate them.
- Never import `github.com/kubewharf/katalyst-adapter`; dependency flow is upstream to downstream only.
- If a type, CRD field, QoS annotation, or gRPC message should be shared across binaries or customized by the adapter, it belongs in `katalyst-api`, not here.

## Dependency Rules

- `pkg/agent/**` must not import `pkg/controller/**`, `pkg/scheduler/**`, or `pkg/webhook/**`.
- `pkg/util/**`, `pkg/consts/**`, and `pkg/config/**` must remain leaf packages and must not import agent/controller/scheduler/webhook code.
- Plugin packages may depend only on parent interfaces plus neutral packages such as `pkg/metaserver`, `pkg/util`, `pkg/config`, and `katalyst-api` types; no cross-plugin imports.

## Versioning And Compatibility

- Prefer additive changes. Removing flags, config keys, or exported symbols is a breaking change; deprecate for one release first.
- Plugin protocol changes must remain backward-compatible. Do not renumber protobuf fields or add required fields.
- Minimum Kubernetes is 1.20+. Gate post-1.24 behavior behind a feature flag.

## Testing

- Keep tests beside the code under test: `foo.go` and `foo_test.go`.
- Allowed test tooling includes stdlib `testing`, `testify`, `gomock`, `mockey`, and `gomonkey`.
- Every `t.Run` subtest must call `t.Parallel()`; CI enforces this with `paralleltest`.
- Prefer fake metaserver and metacache implementations over broad interface mocks.
- New plugins should include registration coverage plus one core path for that plugin type such as `Allocate`, `Evict`, `Report`, `GetTopologyHints`, or `RemovePod`.

## Logging, Errors, And Metrics

- Use `klog.InfoS`, `klog.ErrorS`, and `klog.V(N).InfoS`; avoid `klog.Infof` for new code.
- Prefix structured log messages with a subsystem label like `[qrm-cpu]`, and keep keys stable and lowerCamel, such as `pod`, `node`, `container`, `qosLevel`, `numaID`, and `resource`.
- Wrap returned errors with `%w` so `errors.Is` and `errors.As` continue to work.
- Emit metrics through `pkg/metrics` with bounded-cardinality tag sets; never use pod UID or namespace as a metric tag.
- Do not `panic` in hot paths; return an error and let the manager decide.

## Extension Points

| I want to... | Edit here |
| --- | --- |
| Add a QRM plugin | `pkg/agent/qrm-plugins/<resource>/` and register it in `cmd/katalyst-agent/app/` |
| Add an eviction policy | `pkg/agent/evictionmanager/plugin/<name>/` |
| Add a sysadvisor plugin or model | `pkg/agent/sysadvisor/plugin/<name>/` |
| Add a metric provisioner | `pkg/metaserver/agent/metric/provisioner/<name>/` |
| Add a scheduler plugin | `pkg/scheduler/plugins/<name>/` and register it in `cmd/katalyst-scheduler/app/` |
| Add a controller | `pkg/controller/<name>/` and register it in `cmd/katalyst-controller/app/` |
| Add a webhook | `pkg/webhook/<name>/` |

## Change Playbooks

Cross-repo order matters: land shared schema and protocol changes in `katalyst-api` first, integrate behavior in `katalyst-core` second, mirror downstream customization in `katalyst-adapter` third, and roll out deployment defaults in `katalyst-deploy` last.

### Add A CRD Field

1. Release the field in `katalyst-api`, then bump the pinned version here.
2. Wire the field into the relevant controller, scheduler, or agent logic.
3. Add focused unit tests, usually with fake metaserver inputs where appropriate.
4. Land the `katalyst-api` bump separately before depending changes.

### Add A QRM Plugin

1. Ensure resource constants and any plugin protocol changes already exist in `katalyst-api`.
2. Create `pkg/agent/qrm-plugins/<resource>/` with the plugin implementation, state handling, and focused tests.
3. Register the plugin in `cmd/katalyst-agent/app/agent.go`, defaulting the new flag to the previous behavior, typically off for the first release.
4. Add config wiring under `pkg/config/agent/qrm/`.
5. Keep naming mirror-friendly so downstream adapter overrides can live at `katalyst-adapter/pkg/agent/qrm/<resource>/`.

### Add A Controller

1. Ensure the CRD is already released from `katalyst-api`.
2. Add the reconciler package under `pkg/controller/<name>/`.
3. Register it in `cmd/katalyst-controller/app/controllermanager.go`.
4. Add reconcile tests with `envtest` or a fake client, depending on the scope.

## Review Checklist

1. New logic lives in the correct process layer and respects the package-boundary rules above.
2. Shared API surface stays in `katalyst-api`; nothing adapter-owned is introduced here.
3. Logging, error wrapping, and metric tags follow the logging and metrics rules above.
4. Tests cover the changed behavior, and every `t.Run` uses `t.Parallel()`.
5. Local validation is clean: `make fmt-strict && make vet && make test`.
6. Breaking changes to flags, config, or protocols follow the compatibility rules above.

## CI And Governance

- CI runs formatting, vet, build, `paralleltest`, unit tests, and license checks; release workflows build and publish images for `main`, `release-*`, and tags.
- `CODEOWNERS`, `MAINTAINERS.md`, `GOVERNANCE.md`, and `CONTRIBUTING.md` describe ownership and contribution process.

## Glossary

- **QRM** - QoS Resource Manager; agent subsystem that admits pods and allocates CPU, memory, network, IO, GPU, and related resources.
- **ORM** - Out-of-band Resource Manager; NRI-based enforcement path parallel to QRM.
- **SysAdvisor** - subsystem that computes headroom, reclaimed capacity, provisioning targets, and related advice.
- **Metaserver** - node-local cache of pod, node, container, and metric data.
- **Metacache** - SysAdvisor cache of derived advice, separate from the metaserver's raw view.
- **Reporter** - agent component that publishes node state back to the control plane.
- **QoS level** - `reclaimed_cores`, `shared_cores`, `dedicated_cores`, `system_cores`, chosen by the `katalyst.kubewharf.io/qos_level` pod annotation.

## Agent Notes

- Grep, do not guess. Taint keys, eviction reasons, thresholds, annotation keys, and resource-name strings should be verified from source.

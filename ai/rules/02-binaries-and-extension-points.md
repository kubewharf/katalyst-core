# Binaries And Extension Points

Choose the extension point before editing.

## When To Use

Use this rule when a change needs to hook into a binary, controller, plugin, webhook, scheduler path, or metric path.

## Binary Entry Points

- `cmd/katalyst-agent/` for node-agent behavior
- `cmd/katalyst-controller/` for reconcilers
- `cmd/katalyst-scheduler/` for scheduler behavior
- `cmd/katalyst-webhook/` for admission paths
- `cmd/katalyst-metric/` for custom-metric collection and serving

## Common Extension Points

- QRM plugins: `pkg/agent/qrm-plugins/<resource>/`
- Eviction policies: `pkg/agent/evictionmanager/`
- SysAdvisor plugins and models: `pkg/agent/sysadvisor/`
- Controllers: `pkg/controller/<name>/`
- Scheduler logic: `pkg/scheduler/`
- Webhooks: `pkg/webhook/`
- Metric providers and stores: `pkg/custom-metric/` and `pkg/metrics/`

## Keep In Mind

Registration usually happens under the matching `cmd/.../app/` wiring files. Add implementation and registration together.

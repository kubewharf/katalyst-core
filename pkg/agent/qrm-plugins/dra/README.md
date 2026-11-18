# DRA driver instances

The generic `Manager` runs one registered plugin per configured driver. Kubelet
selects the driver's Unix endpoint from the ResourceClaim allocation. The manager
handles construction, startup rollback and reverse-order shutdown.

Embedding agents supply their compiled vendor implementations using
`RegisterProvider` and `NewManager`, or pass a private factory map to
`NewManagerWithProviders`. The latter snapshots the factory map and configuration
at construction. A `ProviderFactory` receives the resolved `PluginConfig` and
node `Runtime`, and returns a `skeleton.GenericPlugin` whose `Name()` equals the
configured driver. A factory typically builds a `ResourceBackend`, injects it
and a `ResourceSlicePublisher` into `NewStaticPolicy`, then returns
`NewRegisteredPlugin(policy, runtime.RegistrationDir)`.

Use factory closures to inject environment-specific clients or configuration.
Normalize migration defaults before creating the manager. Core does not import
vendor packages or automatically enable a top-level agent. Optional hardware
probes return `ErrUnsupportedNode`; `autoDetect` permits skipping those providers.
Unknown providers and invalid configuration fail startup.

`Publisher` provides driver-scoped ResourceSlice reconciliation and ownership
checks. It defaults to driver-qualified names; `UseGenerateName` creates slices
with the native `<node>-<driver>-` prefix and an API-assigned suffix. Shard labels
keep generated names associated with their inventory partitions across restarts.
`AdoptExisting` validates native inventory before taking ownership, and
`PreserveExistingNames` retains existing resource identity. `SliceName` remains
available for providers that need explicit names. Keep provider-specific
migration checks and state defaults in the embedding application. Each driver must retain distinct
socket, state/checkpoint, CDI and service resources.

The dual-driver tests use real Unix gRPC endpoints with fake backends and API
clients. They check registration, mixed-claim Prepare/Unprepare isolation,
ResourceSlice separation, rollback, and retry after failed shutdown.

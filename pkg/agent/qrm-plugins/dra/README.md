# DRA driver instances

The generic `Manager` runs one registered plugin per configured driver. Kubelet
selects the driver's Unix endpoint from the ResourceClaim allocation. The manager
handles construction, startup rollback and reverse-order shutdown.

Embedding agents supply their compiled vendor implementations using
`RegisterProvider` and `NewManager`, or pass a private factory map to
`NewManagerWithProviders`. The latter snapshots the factory map and configuration
at construction. A `ProviderFactory` receives the resolved `PluginConfig` and
node `Runtime`, and returns a `skeleton.GenericPlugin` whose `Name()` equals the
configured driver. A factory builds a `ResourceBackend` and a
`ResourceSlicePublisher`, then returns the registered policy directly:

```go
return NewStaticPolicy(Options{
    DriverName: config.DriverName,
    RegistrationDir: runtime.RegistrationDir,
    Backend: backend,
    ResourceSlicePublisher: publisher,
    KubeClient: kube,
    StateDirectoryConfiguration: &statedirectory.StateDirectoryConfiguration{
        StateFileDirectory: config.StateDir,
    },
})
```

`NewStaticPolicy` constructs the shared Katalyst API registration wrapper, like
CPU. Startup is asynchronous and retries initialization: successful
`Manager.Start()` does not mean the driver is ready or accepted by kubelet.
Factory errors remain visible to Manager, but asynchronous policy initialization
failures do not trigger immediate required-provider rollback or optional-provider
skipping. Normal shared-wrapper shutdown is terminal: stop it once and construct
a fresh wrapper/Manager for another run. The former custom socket ownership and
failed-shutdown retry guarantees are not provided; each driver endpoint must have
one owning process.

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
ResourceSlice separation. Separate Manager tests use synchronous fake plugins to
check rollback and failed-stop retention; they do not establish those guarantees
for the asynchronous shared registration wrapper.

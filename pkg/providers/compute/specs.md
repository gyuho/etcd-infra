# Compute Provider Specs

This file defines the required behavior for implementing `pkg/providers/compute` interfaces.

## Required Interfaces

Every provider must implement:

1. `compute.Provider` (`Lifecycle`, `PowerControl`, `Inventory`, `CapabilityReporter`)
2. `compute.Instance` for returned and queried instances

`compute.Provider` intentionally excludes reboot. Reboot is not part of current cluster lifecycle requirements and is kept out of core interfaces until a concrete workflow requires it.

## Instance Handle Contract

`compute.InstanceHandle` is provider-owned and opaque to orchestration code. Each provider uses its own native identifier:

- **Container** (Docker containers used for local development topologies): container name
- **AWS**: EC2 instance ID
- **Hetzner**: server ID string
- **Azure** (planned): VM resource ID
- **GCP** (planned): instance self-link or name

Rules:

1. `Get/Delete/ReplaceMachine/Stop/Start` must accept provider-native handles.
2. `DeleteResult.ID` must return a handle in the same domain as the input.
3. Orchestration code must not assume handle format or attempt to parse it.

## Replacement Contract

`ReplaceMachine` is a core lifecycle operation. It triggers provider-managed
replacement without changing desired capacity. `ReplaceResult` identifies the
old machine and the managed group when replacement is asynchronous. Callers use
that correlation with provider inventory, group-scaling, or readiness APIs.

Persistence and network identity are provider-specific lifecycle prerequisites.
The local container provider verifies reuse of the member's IP and named data
volume before returning. AWS accepts only an ASG member; its launch lifecycle
must be configured to reattach the stable network identity and data volume.

## PrivateIPv4 Contract

> [!NOTE]
> PrivateIPv4 semantics are provider-specific and not all providers populate this field. For cross-node communication within a cluster, prefer Tailnet IPs from session state rather than PrivateIPv4. Tailnet IPs are always available and encrypted regardless of the underlying provider network topology.

Rules:

1. Cloud VPC instances return the VPC-internal IP when available.
2. Local VMs return the guest network IP that is reachable from the host.
3. Providers without private IPs (for example Hetzner without Hetzner Networks) return empty string.
4. Callers must not assume PrivateIPv4 is always non-empty for cloud providers.
5. For cross-node communication, prefer Tailnet IPs from session state.

## Op Field Semantics: Cloud vs. Local

The compute package serves two fundamentally different provider categories. Each category uses a distinct subset of `Op` fields and silently ignores the other category's fields. This is intentional — callers build a single `Op` for cross-provider use without conditional field setting.

### Cloud Providers (AWS, Hetzner)

Cloud providers use the following `Op` fields:

- `Size` for instance type selection (for example `"t3.micro"` on AWS, `"cx21"` on Hetzner)
- `Region` / `Datacenter` for placement
- SSH fields for remote access configuration
- `VPCID` / `SubnetID` / `SecurityGroupIDs` for network placement (AWS-specific today)
- `Tags` / `TagList` / `UserData` for metadata and cloud-init scripts

The following fields are **ignored** by cloud providers: `CPUs`, `Memory`, `Disk` (sizing is via the `Size` string).

### Local Providers (Container)

The built-in local container provider uses the following `Op` fields:

- `Name` for the container and persistent-volume names
- `Image` for the container image
- one `PortMappings` entry for the etcd client port (`2379/tcp`)
- `ProviderConfig` with `local.CreateConfig.Command` for the container command

Other fields are ignored by this provider. Its cluster-scoped manager supplies
the network, ownership label, and persistent data-volume layout.

New providers should document their field mapping in their package `doc.go`. See the field applicability table in `options.go` for per-provider details.

## Capability Contract

`Capabilities()` must accurately report the features the provider actually supports.

Rules:

1. Always include baseline capabilities that are fully implemented.
2. Do not advertise optional capabilities that are not fully implemented.
3. Keep capability declarations stable and covered by tests.

## Error Contract

When a feature is unsupported for a provider, return an error that wraps `compute.ErrNotSupported`. Callers gate fallback behavior via `compute.IsNotSupported(err)`.

## Optional Extension Interfaces

Implement optional interfaces only when the provider natively supports the capability:

- `StreamingInstance` — real-time output streaming (Container, Hetzner)
- `PollableCommandInstance` — async command handles
- `FileTransferInstance` — direct file transfer (Container, Hetzner)
- `SSHInstance` — SSH connection details (Hetzner, AWS for diagnostics)
- `SSHKeyManager` — provider-level SSH key registration (Hetzner)
- `ReadinessWaiter` — poll until instance is ready (AWS: EC2 state + SSM probe)
- `InstanceMetadata` — provider-assigned tags, AZ, and instance type (AWS)
- `GroupScaler` — autoscaling group capacity operations (AWS ASG)
- `VolumeVerifier` — **infrastructure-layer**: persistent block volume attachment verification (AWS EBS, Azure Managed Disk, GCP Persistent Disk). NOT type-asserted on `compute.Provider`. AWS implements this on its `InfraManager`; orchestration accesses it via `ControlPlaneRollingReplacer.RoleVolumeVerifier()`. Future providers may implement it on their infrastructure manager (like AWS) or directly on their compute Manager.
- `CoordinationIPProvider` — **infrastructure-layer**: static/elastic public IP for coordination server (Headscale). NOT type-asserted on `compute.Provider`. Implemented by provider `headscaleinfra/` packages using native SDK calls. (AWS EIP, Azure Static Public IP, GCP External Static IP)
- `EtcdPeerInterfaceProvider` — **infrastructure-layer**: dedicated network interface for etcd peer-to-peer traffic. NOT type-asserted on `compute.Provider`. Implemented by provider infrastructure packages using native SDK calls. (AWS ENI, Azure NIC, GCP vNIC)

### Flag-Only Capabilities

The following capability has **no** corresponding optional extension interface. It is a flag-only signal used by provisioning dispatch and topology validation:

- `CapabilityKMSEncryption` — native KMS for etcd encryption at rest. Each cloud's KMS API is too different to abstract (AWS KMS, Azure Key Vault, GCP Cloud KMS). Providers use their native SDK directly.

### InstanceMetadata Methods

`InstanceMetadata` exposes three methods:

- `Tags() map[string]string` — provider-assigned key-value metadata (AWS tags, GCP labels, Azure tags).
- `AvailabilityZone() string` — placement zone (e.g., `"us-east-1a"`). Empty if no zone concept.
- `InstanceType() string` — machine type (e.g., `"t3a.medium"`, `"Standard_D2s_v3"`, `"n2-standard-2"`). Empty if the provider does not track instance type after creation.

### Design Decision: No CapabilityNetworkInfo

VPC/VNET/subnet metadata is intentionally NOT exposed through a shared compute interface. Each cloud's network API surface differs too much (AWS VPC vs Azure VNET vs GCP VPC vs Hetzner Networks) — forcing a common interface would create a leaky abstraction. Providers pass network-specific configuration via `Op.VPCID`, `Op.SubnetID`, `Op.SecurityGroupIDs`, or `Op.ProviderConfig` for cloud-specific extensions.

### PollableCommandInstance Contract

`PollableCommandInstance` provides async fire-and-poll command execution for providers where command execution is API-based rather than connection-based (Azure VM Run Command, GCP OS Login).

Rules:

1. `RunCommandAsync()` must return immediately with a `CommandHandle`.
2. `CommandHandle.Poll()` must be safe to call repeatedly until completion.
3. `CommandHandle.Cancel()` must be idempotent — cancelling an already-completed command is a no-op.
4. Poll must respect context cancellation.
5. Completed commands must return the full `ExecuteResult` with stdout, stderr, and exit code.
6. Provider implementations must handle API rate limits internally (callers should not need backoff logic).

### Caller Pattern: Synchronous vs Pollable Providers

Orchestration code that must work with both synchronous providers (AWS SSM, Hetzner SSH)
and async pollable providers (future Azure VM Run Command, GCP OS Login) should use
type assertion to select the execution path:

```go
if pollable, ok := inst.(compute.PollableCommandInstance); ok {
    handle, err := pollable.RunCommandAsync(ctx, cmd, opts)
    // poll handle until completion
} else {
    result, err := inst.RunCommand(ctx, cmd)
    // use result directly
}
```

The baseline `Instance.RunCommand` is always available. `PollableCommandInstance` is an
optimization for providers whose command APIs are natively async. Callers should never
assume one path or the other — always check via type assertion.

> [!WARNING]
> **Do NOT implement optional interfaces with stub methods that return `ErrNotSupported`.**
>
> If a capability is unsupported, omit the interface entirely. Package-level helpers (`WriteFile`, `CopyFile`, etc.) detect absence via type assertion and automatically fall back to command-based transfer. Stub implementations defeat type-assertion-based detection and break the fallback path silently.
>
> If unsupported, rely on baseline `compute.Instance` behavior instead.

## Testing Requirements

1. Add unit tests for `Capabilities()` correctness — verify every reported capability is implemented.
2. Add tests for handle-based `Get/Delete/Stop/Start` operations.
3. Add tests for optional-interface behavior and fallback paths.
4. Add tests for unsupported feature errors, verifying `ErrNotSupported` wrapping.

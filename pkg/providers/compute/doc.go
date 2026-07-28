// Package compute defines provider-agnostic interfaces for managing compute instances.
//
// WHY NAME: "compute" is the domain; provider-agnostic VM lifecycle, command execution,
// and capability reporting. Not "cloud" (containers aren't cloud). Not "vm" (too narrow,
// excludes containers). Not "instance" (noun, not domain). Not "provider" (parent dir is
// already "providers/").
//
// WHY PATH: Under pkg/providers/ because compute interfaces are the public contract that
// provider implementations (aws/, hetzner/, container/) satisfy. In pkg/ (not internal/) so
// external tools can also depend on these types.
//
// OWNS: Provider, Instance, Lifecycle, PowerControl, Inventory, CapabilityReporter, all
// optional extension interfaces (StreamingInstance, FileTransferInstance, SSHInstance,
// SSHKeyManager, ReadinessWaiter, GroupScaler, InstanceMetadata,
// PollableCommandInstance, CoordinationIPProvider, EtcdPeerInterfaceProvider, VolumeVerifier),
// Op configuration, CapabilitySet, ErrNotSupported sentinel, file-transfer helpers.
//
// FILE LAYOUT:
//   - interfaces.go: Provider-surface interfaces (type-asserted on Provider or Instance)
//   - infrastructure_interfaces.go: Infrastructure-layer interfaces (VolumeVerifier,
//     CoordinationIPProvider, EtcdPeerInterfaceProvider), NOT on Provider, implemented
//     by provider-specific infrastructure managers
//
// DOES NOT OWN: Provider implementations (aws/, hetzner/, container/), lifecycle contracts
// (internal/clusterlifecycle/providers/contracts/), registry dispatch.
//
// # Interface Hierarchy
//
// The package provides a layered interface design:
//
//	Lifecycle    - Core lifecycle: Create, Delete
//	PowerControl - Instance power operations: Stop, Start
//	Inventory    - Instance inspection: Get, List
//	Provider     - Composed manager surface (Lifecycle + PowerControl + Inventory + CapabilityReporter)
//	Instance     - Transport-agnostic execution: RunCommand, RunCommandWithOptions
//
// Inventory lookups use provider-owned instance handles (InstanceHandle), not
// a cross-provider synthetic ID format.
//
// # Transport Neutrality
//
// The core Instance interface is deliberately transport-agnostic. Providers choose
// their own execution mechanism:
//
//   - Container provider uses docker exec (local); no network transport required
//   - Hetzner uses SSH over TCP
//   - AWS may use SSH or AWS Systems Manager (SSM) Session Manager
//   - Future providers may use other mechanisms (e.g., GCP IAP, Azure Bastion)
//
// Callers interact with all providers through the same Instance interface.
// Transport details are hidden behind each provider's implementation.
//
// # Capability Gaps
//
// Some providers cannot implement every optional capability. For example, a
// provider that executes through a managed session service may not support
// direct file transfer primitives.
//
// In these cases providers should return errors that wrap ErrNotSupported:
//
//	if compute.IsNotSupported(err) {
//	    // Fall back to an alternative workflow.
//	}
//
// # Optional Extension Interfaces
//
// Providers may implement additional capabilities, checked via type assertion:
//
//	StreamingInstance       - Real-time output streaming for long-running commands.
//	PollableCommandInstance - Asynchronous command handles (reserved for Azure VM
//	                          Run Command and GCP OS Login, not yet implemented).
//	FileTransferInstance    - Direct file transfer operations.
//	SSHInstance             - SSH connection details (providers that use SSH transport).
//	SSHKeyManager           - Provider SSH key registration.
//	ReadinessWaiter         - Wait for instance to become ready (running + command-executable).
//	InstanceMetadata        - Provider-assigned metadata (tags, labels, zone).
//	GroupScaler             - Capacity-managed group operations (ASGs, node pools).
//	VolumeVerifier          - Persistent block volume attachment verification (infrastructure-layer).
//	CoordinationIPProvider    - Static/elastic public IP for coordination servers (infrastructure-layer).
//	EtcdPeerInterfaceProvider - Dedicated network interfaces for etcd peer traffic (infrastructure-layer).
//	CapabilityReporter      - Provider capability discovery via CapabilitySet.
//
// Manager-level interfaces (ReadinessWaiter, GroupScaler, InstanceMetadata, SSHKeyManager)
// are type-asserted on compute.Provider. Infrastructure-layer interfaces (VolumeVerifier,
// CoordinationIPProvider, EtcdPeerInterfaceProvider) are implemented by provider-specific
// infrastructure managers, NOT on compute.Provider. See each interface's doc for details.
//
//	CapabilitySet supports Has, All, Any, List, Len.
//	(Also part of the composed Provider interface.)
//
// Not all cloud providers use SSH. Some use managed session services (e.g., AWS SSM)
// for security reasons. Only type-assert to SSHInstance when SSH-specific information
// (user, port, key path) is genuinely needed, such as for display or diagnostics.
//
// For file operations, prefer package helpers (WriteFile, CopyFile, CopyDirectory,
// DownloadFile). They use FileTransferInstance when available and otherwise fall
// back to command-based transfer. CreateTarArchive (file_transfer.go) is now an
// exported function for use by lifecycle orchestration code. It is re-exported
// through internal/clusterlifecycle/providers/contracts/compute_surface.go so
// callers within the lifecycle layer do not import this package directly.
//
// # Usage
//
// Create instances through a provider:
//
//	instance, err := manager.Create(ctx, compute.NewCreateRequest(
//	    compute.WithName("my-vm"),
//	    compute.WithCPUs(4),
//	    compute.WithMemory("8GiB"),
//	))
//
// Execute commands on the returned instance:
//
//	result, err := instance.RunCommand(ctx, []string{"echo", "hello"})
//
// # Adapters
//
// EnsureStreaming wraps a non-streaming Instance as a StreamingInstance by
// buffering output and flushing after completion. Callers see the same results;
// only timing differs (all-at-once vs. real-time).
//
// EnsureSynchronous wraps a PollableCommandInstance as a synchronous Instance
// by polling the CommandHandle with exponential backoff. This is the bridge for
// async transport providers (Azure VM Run Command, GCP OS Login) to satisfy
// the standard Instance.RunCommand interface.
//
// # Supported Providers
//
//   - container: Local containers via Docker (Provider, Instance, StreamingInstance, FileTransferInstance)
//   - hetzner: Cloud servers via SSH (Provider, Instance, StreamingInstance, SSHInstance, SSHKeyManager)
//   - aws: EC2 instances (Provider, Instance, SSHInstance for diagnostics; primary transport is SSM, not SSH)
//   - azure (planned): Azure VMs via VM Run Command (Provider, Instance, PollableCommandInstance, GroupScaler, ReadinessWaiter)
//   - gcp (planned): GCE instances via OS Login (Provider, Instance, PollableCommandInstance, GroupScaler, ReadinessWaiter)
//
// Azure and GCP will use PollableCommandInstance for async fire-and-poll command
// execution. Orchestration code calls EnsureSynchronous() to bridge async transport
// to the standard synchronous Instance.RunCommand() interface.
//
// # Adding a New Provider
//
// See docs/add-provider.md for the 15-step checklist and internal/package-rules.md
// for directory conventions.
package compute //nolint:godoclint

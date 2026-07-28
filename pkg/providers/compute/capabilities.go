package compute

import (
	"errors"
	"fmt"
	"slices"
)

// Capability identifies a provider capability exposed for runtime negotiation.
type Capability string

// Defined Capability values for runtime-negotiated provider features.
const (
	CapabilityLifecycleCreateDelete Capability = "lifecycle-create-delete"
	CapabilityPowerControl          Capability = "power-control"
	CapabilityInventoryRead         Capability = "inventory-read"
	CapabilityCommandExecution      Capability = "command-execution"
	CapabilityCommandStreaming      Capability = "command-streaming"
	CapabilityCommandPolling        Capability = "command-polling"
	CapabilityFileTransfer          Capability = "file-transfer"
	// CapabilitySSHAccess indicates SSH connection details are available via
	// SSHInstance (host, port, user, key path). Does NOT imply SSH is the
	// primary command execution transport; AWS implements SSHInstance for
	// diagnostics while using SSM as the primary execution transport.
	CapabilitySSHAccess        Capability = "ssh-access"
	CapabilitySSHKeyManagement Capability = "ssh-key-management"
	CapabilityGroupScaling     Capability = "group-scaling"
	CapabilityReadinessWait    Capability = "readiness-wait"
	CapabilityInstanceMetadata Capability = "instance-metadata"
	// CapabilityKMSEncryption indicates the provider offers native KMS for
	// etcd encryption at rest. AWS uses AWS KMS, Azure uses Key Vault,
	// GCP uses Cloud KMS. Each provider's KMS integration is cloud-native;
	// there is no shared KMS interface (each cloud's API surface is too different).
	CapabilityKMSEncryption Capability = "kms-encryption"
	// CapabilityVolumeVerification indicates the provider supports persistent
	// block volume attachment verification (e.g., AWS EBS, Azure Managed Disk,
	// GCP Persistent Disk). Used by rollout health checks to confirm a
	// replacement instance recovered with the correct data volume attached.
	// Corresponds to the VolumeVerifier optional extension interface.
	CapabilityVolumeVerification Capability = "volume-verification"
	// CapabilityCoordinationIP indicates the provider supports static/elastic
	// public IP addresses for coordination server instances (Headscale).
	// AWS uses Elastic IPs, Azure uses Static Public IPs, GCP uses External
	// Static IPs. Corresponds to the CoordinationIPProvider extension interface.
	CapabilityCoordinationIP Capability = "coordination-ip"
	// CapabilityEtcdPeerInterface indicates the provider supports dedicated
	// network interfaces for etcd peer-to-peer traffic, bypassing the WireGuard
	// overlay for sub-millisecond Raft consensus. AWS uses ENIs, Azure uses
	// NICs, GCP uses vNICs. Corresponds to the EtcdPeerInterfaceProvider
	// extension interface.
	CapabilityEtcdPeerInterface Capability = "etcd-peer-interface"
)

// CapabilitySet is a simple membership map of capabilities.
// Invariant: all entries are true. NewCapabilitySet is the only constructor and
// only stores true values. Len() and List() rely on this invariant.
type CapabilitySet map[Capability]bool

// NewCapabilitySet builds a set from a capability list.
func NewCapabilitySet(caps ...Capability) CapabilitySet {
	set := make(CapabilitySet, len(caps))
	for _, cap := range caps {
		set[cap] = true
	}
	return set
}

// Has reports whether the capability is present in the set.
func (set CapabilitySet) Has(capability Capability) bool {
	if len(set) == 0 {
		return false
	}
	return set[capability]
}

// All reports whether every capability in caps is present in the set.
func (set CapabilitySet) All(caps ...Capability) bool {
	for _, cap := range caps {
		if !set.Has(cap) {
			return false
		}
	}
	return true
}

// Any reports whether at least one capability in caps is present in the set.
// Returns false for an empty caps list (vacuous disjunction).
func (set CapabilitySet) Any(caps ...Capability) bool {
	return slices.ContainsFunc(caps, set.Has)
}

// List returns all capabilities in the set in sorted order.
// Deterministic ordering ensures consistent logs, error messages, and test output.
func (set CapabilitySet) List() []Capability {
	if len(set) == 0 {
		return nil
	}
	caps := make([]Capability, 0, len(set))
	for cap := range set {
		caps = append(caps, cap)
	}
	slices.Sort(caps)
	return caps
}

// Len returns the number of capabilities in the set.
func (set CapabilitySet) Len() int {
	return len(set)
}

// CapabilityReporter is implemented by providers that expose their capabilities
// without requiring trial-and-error by callers.
//
// NOTE: CapabilitySet describes RUNTIME transport capabilities (what a provider's
// compute manager can do: SSH, streaming, file transfer, polling). For PROVISIONING
// lifecycle capabilities (what roles a provider can fulfill: control plane, workers,
// coordination server), see contracts.ProviderCapabilities in
// internal/clusterlifecycle/providers/contracts/provider_roles.go. New providers
// must configure both systems.
type CapabilityReporter interface {
	Capabilities() CapabilitySet
}

// ErrNotSupported indicates an operation is not supported by the provider.
//
// Callers should use errors.Is(err, ErrNotSupported) or IsNotSupported(err)
// instead of string-matching provider-specific error text.
var ErrNotSupported = errors.New("operation not supported by provider")

// IsNotSupported reports whether err indicates a provider capability gap.
func IsNotSupported(err error) bool {
	return errors.Is(err, ErrNotSupported)
}

// NotSupportedError wraps ErrNotSupported with provider/operation context.
func NotSupportedError(provider, operation string) error {
	if provider == "" && operation == "" {
		return ErrNotSupported
	}
	if provider == "" {
		return fmt.Errorf("%s: %w", operation, ErrNotSupported)
	}
	if operation == "" {
		return fmt.Errorf("%s: %w", provider, ErrNotSupported)
	}
	return fmt.Errorf("%s %s: %w", provider, operation, ErrNotSupported)
}

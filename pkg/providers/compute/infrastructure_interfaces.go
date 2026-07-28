package compute //nolint:godoclint

import "context"

// infrastructure_interfaces.go defines interfaces for provider-specific
// infrastructure managers. These are NOT part of the compute.Provider
// interface tree and are NOT type-asserted on compute.Provider or
// compute.Instance.
//
// New provider authors: you do NOT implement these on your compute.Manager.
// Implement them on your infrastructure-layer types (e.g.,
// aws/infrastructure/ec2_provisioning.go, aws/headscaleinfra/server_provisioner.go,
// aws/eni/).
//
// Orchestration code accesses these through provider-specific entry points:
//   - VolumeVerifier: via contracts.ControlPlaneRollingReplacer.RoleVolumeVerifier()
//   - CoordinationIPProvider: via provider-specific headscaleinfra packages
//   - EtcdPeerInterfaceProvider: via provider-specific infrastructure packages

// VolumeVerifier is an infrastructure-layer interface for providers that support
// persistent block storage volumes with deterministic identity (e.g., AWS EBS,
// Azure Managed Disk, GCP Persistent Disk).
//
// IMPLEMENTATION LAYER: This interface is NOT type-asserted on compute.Provider.
// It is implemented by provider-specific infrastructure managers and accessed via
// contracts.ControlPlaneRollingReplacer.RoleVolumeVerifier(). See
// aws/infrastructure/ec2_provisioning.go for the AWS reference implementation.
//
// During rolling control-plane replacement, the orchestrator uses this to confirm
// that the replacement instance has the correct data volume attached, which is critical
// for etcd data integrity. The volume is attached on the instance by the daemon
// (for example, by a boot-time helper), not by the orchestrator. This interface
// lets the orchestrator verify the outcome after boot.
type VolumeVerifier interface {
	// CheckVolumeAttachment verifies that volumeID is attached to instanceID.
	// Returns nil when the volume is attached and in the expected state.
	// Returns an error if the volume is not found, not attached, or attached
	// to a different instance.
	CheckVolumeAttachment(ctx context.Context, volumeID, instanceID string) error
}

// CoordinationIPProvider is an infrastructure-layer interface for providers that
// support static/elastic public IP addresses for coordination server instances.
//
// IMPLEMENTATION LAYER: This interface is NOT type-asserted on compute.Provider.
// It is implemented by provider-specific headscaleinfra packages that use native
// SDK calls. See aws/headscaleinfra/server_provisioner.go (which uses ec2/eips.go)
// for the AWS reference implementation.
//
// WHY: The Headscale coordination server needs a stable public IP that
// survives instance replacements (ASG self-healing, spot interruptions).
// Without a static IP, every instance replacement would change the headscale
// URL, breaking all Tailnet node connections until they re-discover the new IP.
//
// SCOPE: This interface is for coordination servers (Headscale) only.
// Tailscale SaaS mode does not provision a coordination server VM, so this
// interface is never used in that mode. Control plane and worker nodes do
// not need static IPs; they connect to the coordination server, not the
// other way around.
//
// PROVIDER MAPPING:
//   - AWS: Elastic IP (ec2:AllocateAddress / ec2:AssociateAddress)
//   - Azure: Static Public IP (network.PublicIPAddresses)
//   - GCP: External Static IP (compute.addresses)
//   - Container/Hetzner: not applicable (container provider is local, Hetzner uses Floating IPs
//     which have a different lifecycle model)
//
// BOOT-TIME RE-ASSOCIATION: On instance replacement, the daemon binary
// re-associates the static IP using
// the allocation ID baked into user-data. This is analogous to how awsebs
// re-attaches EBS volumes on boot.
type CoordinationIPProvider interface {
	// EnsureCoordinationIP allocates or reuses a static public IP for the
	// coordination server. The returned AllocationID is tracked in session
	// state and passed to cloud-init for boot-time re-association. The
	// PublicIP is used immediately for the headscale server URL and TLS
	// certificate SAN.
	//
	// This method is idempotent: repeated calls with the same ClusterID
	// return the existing allocation rather than creating duplicates.
	EnsureCoordinationIP(ctx context.Context, req CoordinationIPRequest) (*CoordinationIPResult, error)
}

// CoordinationIPRequest describes a static public IP allocation for a
// coordination server.
type CoordinationIPRequest struct {
	ClusterID    string            // Cluster identity for tag scoping
	Name         string            // Human-readable name for the allocation (e.g., "mycluster-headscale-eip")
	ResourceTags map[string]string // Ownership/environment tags propagated to the IP resource
}

// CoordinationIPResult contains the allocated static public IP's identity.
type CoordinationIPResult struct {
	AllocationID string // Provider-specific allocation handle (e.g., AWS "eipalloc-0abc123", Azure resource ID)
	PublicIP     string // The static public IP address (e.g., "54.123.45.67")
}

// EtcdPeerInterfaceProvider is an infrastructure-layer interface for providers
// that support dedicated network interfaces for etcd peer-to-peer traffic.
//
// IMPLEMENTATION LAYER: This interface is NOT type-asserted on compute.Provider.
// It is implemented by provider-specific infrastructure packages that use native
// SDK calls. See aws/eni/ (which uses ec2/eni.go) for the AWS reference
// implementation.
//
// WHY: etcd Raft consensus (port 2380) is the most latency-sensitive traffic
// in a Kubernetes cluster. Providers with VPC-internal networking (AWS ENIs,
// Azure NICs, GCP vNICs) can supply dedicated network interfaces that bypass
// the userspace WireGuard overlay, giving etcd kernel-level networking with
// sub-millisecond latency and deterministic P99s.
//
// PROVIDER OBLIGATION: Only providers with true VPC-internal networking should
// implement this interface. Providers without it (local Docker containers, Hetzner
// public-only servers) silently fall back to Tailnet IPs for etcd peer URLs.
type EtcdPeerInterfaceProvider interface {
	// EnsureEtcdPeerInterface creates or claims a dedicated network interface
	// for etcd peer traffic on the specified control plane role's subnet.
	//
	// The returned PrivateIP is used for etcd initial-cluster peer URLs and
	// must be included in PKI certificate SANs. The InterfaceID is tracked for
	// cleanup and passed to cloud-init for boot-time attachment.
	//
	// This method is idempotent: repeated calls for the same role return the
	// existing interface rather than creating duplicates.
	EnsureEtcdPeerInterface(ctx context.Context, req EtcdPeerInterfaceRequest) (*EtcdPeerInterfaceResult, error)
}

// EtcdPeerInterfaceRequest describes a dedicated network interface to create
// for etcd peer-to-peer communication on a specific control plane role.
type EtcdPeerInterfaceRequest struct {
	ClusterID       string            // Cluster identity for tag scoping
	RoleID          string            // "cp-0", "cp-1", "cp-2"
	SubnetID        string            // Must match the role's ASG subnet (AZ-pinning)
	SecurityGroupID string            // CP security group (allows etcd peer/client ports)
	ResourceTags    map[string]string // Ownership/environment tags propagated to the interface
}

// EtcdPeerInterfaceResult contains the created or claimed interface's identity.
type EtcdPeerInterfaceResult struct {
	InterfaceID string // Provider resource ID (e.g., AWS "eni-0abcd1234efgh5678", Azure NIC resource ID)
	PrivateIP   string // VPC private IP assigned to the interface (e.g., "10.0.1.42")
}

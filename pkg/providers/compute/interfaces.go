package compute //nolint:godoclint

import (
	"context"
	"io"
	"time"
)

// InstanceHandle is the provider-owned lookup key for an instance.
//
// Handles are opaque to orchestration code:
//   - Container: container name (for example "my-cluster-cp")
//   - AWS: EC2 instance ID (for example "i-0123456789abcdef0")
//   - Hetzner: server ID string (for example "12345678")
type InstanceHandle = string

// GroupHandle is the provider-owned lookup key for an autoscaling group,
// node pool, or equivalent capacity-managed instance group.
type GroupHandle = string

// InstanceState is the provider-agnostic lifecycle state for an instance.
type InstanceState string

// Defined InstanceState values for the provider-agnostic lifecycle state.
const (
	InstanceStateUnknown    InstanceState = "unknown"
	InstanceStatePending    InstanceState = "pending"
	InstanceStateRunning    InstanceState = "running"
	InstanceStateStopping   InstanceState = "stopping"
	InstanceStateStopped    InstanceState = "stopped"
	InstanceStateTerminated InstanceState = "terminated"
)

// IsTerminal reports whether this state represents a final, non-recoverable lifecycle phase.
// Only Terminated is terminal. Stopped instances can be restarted via PowerControl.Start().
func (s InstanceState) IsTerminal() bool {
	return s == InstanceStateTerminated
}

// IsRunning reports whether this state is Running.
func (s InstanceState) IsRunning() bool {
	return s == InstanceStateRunning
}

// ExecuteResult contains the outcome of a command execution.
type ExecuteResult struct {
	ExitCode int
	Stdout   string
	Stderr   string
}

// CreateRequest describes an instance create operation.
//
// Op contains provider-agnostic settings (name, size, image, network, tags,
// and provider-specific extension via ProviderConfig).
type CreateRequest struct {
	Op Op
}

// DeleteRequest describes an instance delete operation.
//
// ID is the provider-owned instance handle. Op is optional and reserved for
// provider-specific delete behavior extensions.
type DeleteRequest struct {
	ID InstanceHandle
	Op Op
}

// ReplaceRequest describes an on-demand machine replacement.
//
// ID is the provider-owned handle of the machine to replace.
type ReplaceRequest struct {
	ID InstanceHandle
}

// ReplaceResult identifies the machine being replaced and, when applicable,
// the provider-managed group creating its replacement.
type ReplaceResult struct {
	PreviousID InstanceHandle
	Group      GroupHandle
}

// PowerRequest describes an instance start/stop operation.
//
// ID is the provider-owned instance handle. Op is optional and reserved for
// provider-specific power-control behavior extensions.
type PowerRequest struct {
	ID InstanceHandle
	Op Op
}

// RunCommandOptions configures RunCommandWithOptions behavior.
// This enables provider-agnostic command execution with timeouts and stdin.
//
// For real-time output streaming, use StreamingOptions with StreamingInstance.
// The two structs share common fields (Timeout, WorkDir, Stdin, ProviderConfig)
// but StreamingOptions adds Stdout/Stderr writers for real-time output.
// EnsureStreaming() bridges the gap for providers without native streaming.
//
// Provider-specific options (e.g., AWS SSM document, Azure Run Command user)
// can be passed via ProviderConfig. Providers ignore unrecognized config types.
type RunCommandOptions struct {
	// Timeout is the maximum duration for the command.
	// Zero means use the provider's default timeout.
	Timeout time.Duration

	// WorkDir is the working directory for command execution.
	// Empty string means use the provider's default (typically "/" or home).
	WorkDir string

	// Stdin provides input to the command.
	// Nil means no stdin input.
	Stdin io.Reader

	// ProviderConfig holds provider-specific command execution options.
	// Each provider defines its own config struct (e.g., aws.SSMCommandConfig).
	// Providers silently ignore unrecognized or nil config types.
	ProviderConfig any
}

// Instance represents a compute instance with execution capabilities.
// This interface is transport-agnostic; implementations may use SSH, docker exec,
// AWS SSM, or any other mechanism to execute commands on the instance.
type Instance interface {
	// ID returns the unique identifier for this instance.
	ID() string

	// PublicIPv4 returns the public IP address, or empty if none.
	// Local containers (e.g., Docker) return empty since they have no public IP.
	PublicIPv4() string

	// PrivateIPv4 returns the provider-internal IP address, or empty if not
	// applicable or not yet resolved.
	//
	// Semantics vary by provider type:
	//   - Cloud VPC instances (AWS): VPC-internal private IP.
	//   - Local containers (Docker): container network IP reachable from the host.
	//   - Public-only cloud (Hetzner without Hetzner Networks): empty string.
	//
	// Callers must not assume this is always populated for cloud providers.
	// For cross-node communication, prefer tailnet IPs from session state.
	// PrivateIPv4 is primarily useful for VPC-internal addressing (etcd peer
	// URLs, NLB targets) and diagnostic display.
	PrivateIPv4() string

	// State returns the provider-agnostic lifecycle state.
	State() InstanceState

	// RunCommand executes a command on the instance with default options.
	RunCommand(ctx context.Context, command []string) (*ExecuteResult, error)

	// RunCommandWithOptions executes a command with explicit options.
	// Use this when you need custom timeouts, working directory, or stdin.
	RunCommandWithOptions(ctx context.Context, command []string, opts *RunCommandOptions) (*ExecuteResult, error)
}

// DeleteResult captures the outcome of a delete operation.
type DeleteResult struct {
	ID      InstanceHandle
	Deleted bool
}

// Lifecycle handles instance lifecycle operations. ReplaceMachine initiates a
// provider-managed replacement and may return before the new machine is ready.
type Lifecycle interface {
	Create(ctx context.Context, req CreateRequest) (Instance, error)
	Delete(ctx context.Context, req DeleteRequest) (DeleteResult, error)
	ReplaceMachine(ctx context.Context, req ReplaceRequest) (ReplaceResult, error)
}

// PowerControl handles instance stop/start operations.
type PowerControl interface {
	Stop(ctx context.Context, req PowerRequest) error
	Start(ctx context.Context, req PowerRequest) error
}

// Inventory provides access to existing instances.
type Inventory interface {
	Get(ctx context.Context, id InstanceHandle) (Instance, error)
	List(ctx context.Context) ([]Instance, error)
}

// Provider is the canonical compute manager surface used by orchestration code.
//
// It composes lifecycle, power-control, and inventory operations so callers can
// treat cloud and local providers uniformly.
type Provider interface {
	Lifecycle
	PowerControl
	Inventory
	CapabilityReporter
}

// ReadinessWaiter is an optional extension interface for providers that support
// waiting for an instance to become fully ready after creation.
//
// "Ready" means the instance is running AND command execution is possible.
// Providers that implement this can offer optimized readiness checks (e.g.,
// polling cloud status APIs combined with SSH/SSM connectivity probes).
//
// AWS implements this via EC2 instance-state polling combined with SSM
// connectivity probes (see pkg/providers/aws/). Future providers (Azure,
// GCP, DigitalOcean) with native readiness APIs should also implement this.
//
// Callers that need readiness waiting should type-assert:
//
//	if waiter, ok := provider.(compute.ReadinessWaiter); ok {
//	    inst, err := waiter.WaitForReady(ctx, id, 5*time.Minute)
//	}
type ReadinessWaiter interface {
	WaitForReady(ctx context.Context, id InstanceHandle, timeout time.Duration) (Instance, error)
}

// InstanceMetadata is an optional extension interface for instances that
// expose provider-assigned metadata (tags, labels, zone, instance type).
//
// Orchestration code uses metadata for topology decisions (e.g., scheduling
// constraints based on availability zone) and teardown verification (e.g.,
// cluster ownership tags). Providers without metadata simply don't implement
// this interface.
//
// Callers should type-assert:
//
//	if im, ok := instance.(compute.InstanceMetadata); ok {
//	    tags := im.Tags()
//	    zone := im.AvailabilityZone()
//	}
type InstanceMetadata interface {
	// Tags returns provider-assigned key-value metadata (AWS tags, GCP labels, Azure tags).
	Tags() map[string]string

	// AvailabilityZone returns the instance's availability zone (e.g., "us-east-1a").
	// Empty string if the provider has no zone concept.
	AvailabilityZone() string

	// InstanceType returns the provider-specific machine type or size.
	// Examples: "t3a.medium" (AWS), "Standard_D2s_v3" (Azure), "n2-standard-2" (GCP).
	// Empty string if the provider does not track instance type after creation.
	InstanceType() string
}

// GroupScaler is an optional extension interface for capacity-managed group
// operations such as autoscaling groups or node pools.
//
// AWS implements this via EC2 Auto Scaling Groups (see pkg/providers/aws/).
// Future providers with managed scaling (Azure VMSS, GCP Managed Instance
// Groups) should also implement this interface for portable group operations.
type GroupScaler interface {
	SetDesiredCapacity(ctx context.Context, group GroupHandle, desired, minCapacity, maxCapacity int) error
	WaitForDesiredCapacity(ctx context.Context, group GroupHandle, expected int, timeout time.Duration) ([]InstanceHandle, error)
	WaitForZeroCapacity(ctx context.Context, group GroupHandle, timeout time.Duration) error
}

// Infrastructure-layer interfaces (VolumeVerifier, CoordinationIPProvider,
// EtcdPeerInterfaceProvider) are in infrastructure_interfaces.go. Those
// interfaces are NOT type-asserted on compute.Provider; they are implemented
// by provider-specific infrastructure managers.

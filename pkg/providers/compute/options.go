package compute

import "maps"

// Mount describes a host-to-guest mount for local VM providers.
// Cloud providers ignore these settings.
type Mount struct {
	HostPath  string // Host filesystem path
	GuestPath string // Guest VM path
	Writable  bool   // Whether the guest can write to the mount
}

// ProvisionScript describes a provisioning script for local VM providers.
// Cloud providers ignore these settings.
type ProvisionScript struct {
	Mode   string // "system" or "user" (provider-specific)
	Script string // Script content
}

// PortMapping describes a container port published to the host.
// Only used by container providers; cloud and VM providers ignore this.
type PortMapping struct {
	ContainerPort int    // Port inside the container
	HostPort      int    // Port on the host (0 = auto-assign)
	HostIP        string // Host bind address (empty = provider default)
	Protocol      string // "tcp" or "udp" (default: "tcp")
}

// Op holds generic compute operation options.
//
// Fields are grouped by purpose:
//   - Identity: ID, Name
//   - Location: Region, Datacenter
//   - Instance type: Size (cloud) OR CPUs/Memory/Disk (local VMs)
//   - OS: Image, Arch
//   - Remote access (SSH): SSHUser, SSHPort, SSHPrivateKeyPath, SSHKeys - optional, SSH-based providers only
//   - Networking: Network, VPCID, SubnetID, SecurityGroupIDs
//   - Metadata: Tags, TagList, UserData
//   - Local VM: Mounts, ProvisionScripts
//
// # Field Applicability by Provider (Create)
//
// Providers silently ignore fields outside their domain. This table shows
// which fields each built-in provider reads during Create:
//
//	Field              | AWS          | Hetzner       | Container     | Azure (planned) | GCP (planned)
//
// +==============+===============+===============+=================+
//
//	Name               | required     | required      | required      | required        | required
//	Region             | -            | required*     | -             | required        | -
//	Datacenter         | -            | alt to Region | -             | -               | required (zone)
//	Size               | required     | required      | ignored       | required        | required
//	CPUs/Memory/Disk   | ignored      | ignored       | ignored         | ignored | ignored
//	Arch               | ignored      | ignored       | ignored         | ignored | ignored
//	Image              | required     | required      | required        | required   | required
//	SSHUser            | optional     | optional      | ignored       | ignored†        | ignored†
//	SSHPort            | optional     | optional      | ignored       | ignored†        | ignored†
//	SSHPrivateKeyPath  | optional     | optional      | ignored       | ignored†        | ignored†
//	SSHKeys            | optional(1)  | optional(N)   | ignored       | ignored†        | ignored†
//	VPCID              | required     | ignored       | ignored       | required (VNet) | required (VPC)
//	SubnetID           | optional     | ignored       | ignored       | required        | required
//	SecurityGroupIDs   | optional     | ignored       | ignored       | optional (NSG)  | optional (FW)
//	Network            | ignored      | ignored       | ignored       | ignored         | ignored
//	Tags               | optional     | optional      | ignored       | optional        | optional (labels)
//	TagList            | ignored      | optional      | ignored       | ignored         | ignored
//	UserData           | optional     | optional      | ignored       | optional        | optional
//	Mounts             | ignored      | ignored       | ignored       | ignored         | ignored
//	ProvisionScripts   | ignored      | ignored       | ignored       | ignored         | ignored
//	PortMappings       | ignored      | ignored       | required      | ignored         | ignored
//	ProviderConfig     | optional     | optional      | required      | optional        | optional
//
// * Hetzner requires Region OR Datacenter; one must be set.
// † Azure and GCP use out-of-band access (Run Command, OS Login); SSH fields are ignored.
// AWS SSHKeys accepts at most one key name (EC2 KeyName). Hetzner accepts multiple.
//
// Future providers should document their own field mapping in their package doc.go.
type Op struct {
	// Identity
	ID   string // Instance ID (for operations on existing instances)
	Name string // Instance name

	// Location (cloud providers)
	Region     string // Cloud region (e.g., "us-east-1", "eu-west-1")
	Datacenter string // Datacenter within region

	// Instance type - use Size for cloud providers, CPUs/Memory/Disk for local VMs
	Size   string // Cloud instance type (e.g., "t3.micro", "cx21")
	CPUs   int    // vCPU count (local containers or VMs)
	Memory string // Memory size (e.g., "4GiB") - local VMs
	Disk   string // Disk size (e.g., "50GiB") - local VMs
	Arch   string // Architecture (e.g., "x86_64", "aarch64")

	// OS image
	Image string // Image ID, slug, or OS version (e.g., "ami-123", "ubuntu-24-04")

	// Remote access (SSH), optional, only used by SSH-based providers.
	// Local providers (e.g., container) ignore these fields entirely.
	// Providers using alternative transports (e.g., AWS SSM) also ignore them.
	SSHUser           string   // SSH username (e.g., "root", "ubuntu")
	SSHPort           int      // SSH port (default: 22)
	SSHPrivateKeyPath string   // Path to SSH private key file
	SSHKeys           []string // SSH key IDs/names to attach at instance creation

	// Networking
	Network          string   // Network mode (local) or VPC name (cloud)
	VPCID            string   // VPC identifier
	SubnetID         string   // Subnet identifier
	SecurityGroupIDs []string // Security group IDs

	// Metadata
	Tags     map[string]string // Key-value tags
	TagList  []string          // String tags (providers without key-value support)
	UserData string            // Cloud-init or startup script

	// Local VM/container provisioning (ignored by cloud providers)
	Mounts           []Mount
	ProvisionScripts []ProvisionScript
	PortMappings     []PortMapping // Container port-to-host mappings (container providers only)

	// ProviderConfig holds provider-specific operation options.
	// Each provider defines its own config struct (e.g., OCI compartment OCID).
	// Providers silently ignore unrecognized or nil config types.
	ProviderConfig any
}

// OpOption is a functional option for configuring compute operations.
type OpOption func(*Op)

// NewOp applies the provided options and returns the resulting Op with defaults applied.
func NewOp(opts ...OpOption) Op {
	op := Op{
		SSHPort: 22,
	}
	op.applyOpts(opts)
	return op
}

// NewCreateRequest builds a create request from OpOptions.
func NewCreateRequest(opts ...OpOption) CreateRequest {
	return CreateRequest{Op: NewOp(opts...)}
}

// NewDeleteRequest builds a delete request from an instance handle and optional OpOptions.
func NewDeleteRequest(id InstanceHandle, opts ...OpOption) DeleteRequest {
	return DeleteRequest{
		ID: id,
		Op: NewOp(opts...),
	}
}

// NewReplaceRequest builds a replacement request from an instance handle.
func NewReplaceRequest(id InstanceHandle) ReplaceRequest {
	return ReplaceRequest{ID: id}
}

// NewPowerRequest builds a power-control request from an instance handle and optional OpOptions.
func NewPowerRequest(id InstanceHandle, opts ...OpOption) PowerRequest {
	return PowerRequest{
		ID: id,
		Op: NewOp(opts...),
	}
}

func (op *Op) applyOpts(opts []OpOption) {
	for _, opt := range opts {
		opt(op)
	}
}

// WithID sets the resource ID.
func WithID(id string) OpOption {
	return func(op *Op) {
		op.ID = id
	}
}

// WithName sets the resource name.
func WithName(name string) OpOption {
	return func(op *Op) {
		op.Name = name
	}
}

// WithRegion sets the region or location.
func WithRegion(region string) OpOption {
	return func(op *Op) {
		op.Region = region
	}
}

// WithDatacenter sets the datacenter name or ID.
func WithDatacenter(datacenter string) OpOption {
	return func(op *Op) {
		op.Datacenter = datacenter
	}
}

// WithSize sets the instance type or size.
func WithSize(size string) OpOption {
	return func(op *Op) {
		op.Size = size
	}
}

// WithImage sets the image identifier or slug.
func WithImage(image string) OpOption {
	return func(op *Op) {
		op.Image = image
	}
}

// WithSSHUser sets the SSH user.
func WithSSHUser(user string) OpOption {
	return func(op *Op) {
		op.SSHUser = user
	}
}

// WithSSHPort sets the SSH port.
func WithSSHPort(port int) OpOption {
	return func(op *Op) {
		op.SSHPort = port
	}
}

// WithSSHPrivateKeyPath sets the SSH private key path.
func WithSSHPrivateKeyPath(path string) OpOption {
	return func(op *Op) {
		op.SSHPrivateKeyPath = path
	}
}

// WithSSHKeys sets provider-specific SSH key identifiers (IDs, names, fingerprints).
func WithSSHKeys(keys []string) OpOption {
	return func(op *Op) {
		op.SSHKeys = cloneStringSlice(keys)
	}
}

// WithTags sets key-value tags for the resource.
func WithTags(tags map[string]string) OpOption {
	return func(op *Op) {
		op.Tags = cloneStringMap(tags)
	}
}

// WithTagList sets string tags for providers that only accept tag lists.
func WithTagList(tags []string) OpOption {
	return func(op *Op) {
		op.TagList = cloneStringSlice(tags)
	}
}

// WithUserData sets user data for instance initialization.
func WithUserData(data string) OpOption {
	return func(op *Op) {
		op.UserData = data
	}
}

// WithMounts sets host-to-guest mounts for local VM providers.
func WithMounts(mounts []Mount) OpOption {
	return func(op *Op) {
		op.Mounts = cloneMounts(mounts)
	}
}

// WithProvisionScripts sets provisioning scripts for local VM providers.
func WithProvisionScripts(scripts []ProvisionScript) OpOption {
	return func(op *Op) {
		op.ProvisionScripts = cloneProvisionScripts(scripts)
	}
}

// WithProvisionScript appends a single provisioning script.
func WithProvisionScript(mode, script string) OpOption {
	return func(op *Op) {
		op.ProvisionScripts = append(op.ProvisionScripts, ProvisionScript{Mode: mode, Script: script})
	}
}

// WithPortMappings sets container port-to-host mappings (container providers only).
func WithPortMappings(mappings []PortMapping) OpOption {
	return func(op *Op) {
		op.PortMappings = clonePortMappings(mappings)
	}
}

// WithVPCID sets the VPC identifier.
func WithVPCID(vpcID string) OpOption {
	return func(op *Op) {
		op.VPCID = vpcID
	}
}

// WithSubnetID sets the subnet identifier.
func WithSubnetID(subnetID string) OpOption {
	return func(op *Op) {
		op.SubnetID = subnetID
	}
}

// WithSecurityGroupIDs sets the security group identifiers.
func WithSecurityGroupIDs(ids []string) OpOption {
	return func(op *Op) {
		op.SecurityGroupIDs = cloneStringSlice(ids)
	}
}

// WithCPUs sets the number of CPUs for the instance.
func WithCPUs(cpus int) OpOption {
	return func(op *Op) {
		op.CPUs = cpus
	}
}

// WithMemory sets the memory allocation (e.g., "4GiB", "8192MB").
func WithMemory(memory string) OpOption {
	return func(op *Op) {
		op.Memory = memory
	}
}

// WithDisk sets the disk size (e.g., "50GiB", "100GB").
func WithDisk(disk string) OpOption {
	return func(op *Op) {
		op.Disk = disk
	}
}

// WithArch sets the target architecture (e.g., "x86_64", "aarch64").
func WithArch(arch string) OpOption {
	return func(op *Op) {
		op.Arch = arch
	}
}

// WithNetwork sets the network mode or configuration.
func WithNetwork(network string) OpOption {
	return func(op *Op) {
		op.Network = network
	}
}

// WithProviderConfig sets provider-specific operation options.
// Each provider defines its own config struct. Providers ignore unrecognized types.
func WithProviderConfig(cfg any) OpOption {
	return func(op *Op) {
		op.ProviderConfig = cfg
	}
}

func cloneMounts(in []Mount) []Mount {
	if len(in) == 0 {
		return nil
	}
	out := make([]Mount, len(in))
	copy(out, in)
	return out
}

func cloneProvisionScripts(in []ProvisionScript) []ProvisionScript {
	if len(in) == 0 {
		return nil
	}
	out := make([]ProvisionScript, len(in))
	copy(out, in)
	return out
}

func clonePortMappings(in []PortMapping) []PortMapping {
	if len(in) == 0 {
		return nil
	}
	out := make([]PortMapping, len(in))
	copy(out, in)
	return out
}

func cloneStringSlice(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, len(in))
	copy(out, in)
	return out
}

func cloneStringMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	maps.Copy(out, in)
	return out
}

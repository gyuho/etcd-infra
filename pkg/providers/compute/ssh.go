package compute

// SSHConfig contains SSH connection details for providers that use SSH.
// Providers expose this through their concrete types, not the Instance interface.
//
// Not all providers use SSH; local containers use docker exec, and some cloud
// providers may use alternative transports (e.g., AWS SSM). This type is
// only relevant for SSH-based providers like Hetzner and DigitalOcean.
type SSHConfig struct {
	User           string
	Port           int
	PrivateKeyPath string
}

// SSHInstance is an optional interface for instances that support SSH access.
// Use type assertion to check if an Instance supports SSH:
//
//	if sshInst, ok := instance.(compute.SSHInstance); ok {
//	    cfg := sshInst.SSHInfo()
//	}
type SSHInstance interface {
	Instance
	SSHInfo() SSHConfig
}

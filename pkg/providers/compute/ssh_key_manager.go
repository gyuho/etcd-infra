package compute

import "context"

// SSHKeyManager manages provider SSH keys used by instance creation.
//
// Cloud providers that require pre-registered SSH keys implement this surface.
// Local providers (for example container) usually do not.
type SSHKeyManager interface {
	// EnsureSSHKey ensures the named SSH key exists in the provider, creating it
	// from publicKey if necessary. labels are provider-native key/value metadata
	// (e.g., Hetzner labels). Providers that do not support resource labels may
	// ignore the parameter.
	EnsureSSHKey(ctx context.Context, name, publicKey string, labels map[string]string) (string, error)
}

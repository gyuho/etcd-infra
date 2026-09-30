package aws

import "git.tbd/etcd-infra/pkg/providers/compute"

// CommandConfig contains AWS-only RunCommandWithOptions settings, passed as
// compute.RunCommandOptions.ProviderConfig.
type CommandConfig struct {
	// GuestShutdown runs shutdown/poweroff/halt/reboot commands in the guest
	// over SSM. By default such commands terminate the EC2 instance instead
	// (shutdown means "instance gone" for the cluster tooling). Interactive
	// callers that forward arbitrary scripts must set this: the default
	// detection matches those words anywhere in a "bash -c" script.
	GuestShutdown bool
}

// guestShutdown reports whether opts asks for in-guest shutdown commands.
func guestShutdown(opts *compute.RunCommandOptions) bool {
	if opts == nil {
		return false
	}
	cfg, ok := opts.ProviderConfig.(CommandConfig)
	return ok && cfg.GuestShutdown
}

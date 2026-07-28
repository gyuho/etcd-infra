package compute

import "context"

// CommandHandle tracks an asynchronous command started via PollableCommandInstance.
// This is the standard pattern for providers where command execution is fire-and-poll
// rather than synchronous (AWS SSM RunCommand, Azure VM Run Command, OCI Cloud Agent).
type CommandHandle interface {
	// Poll checks the command's current status.
	// Returns (result, true, nil) when the command has completed.
	// Returns (nil, false, nil) when the command is still running.
	// Returns (nil, false, err) on polling failure.
	Poll(ctx context.Context) (*ExecuteResult, bool, error)

	// Cancel attempts to cancel the running command.
	// Returns nil if cancellation was successful or the command already finished.
	Cancel(ctx context.Context) error
}

// PollableCommandInstance extends Instance with asynchronous command execution.
// This is for providers where command execution is fire-and-poll rather than
// synchronous, e.g., AWS SSM RunCommand, Azure VM Run Command, OCI Cloud Agent.
//
// No provider implements this yet. AWS SSM's polling loop is currently embedded
// inside RunCommandWithOptions. This contract is reserved for Azure VM Run
// Command and GCP Cloud Agent (both planned providers), and for potential
// future refactoring of AWS to expose its native async execution model.
//
// Providers that support synchronous streaming (SSH, docker exec) implement
// StreamingInstance instead. PollableCommandInstance is the alternative for
// agentless/API-based command execution.
//
// Implementation guidance for planned providers:
//
//   - Azure VM Run Command: Call RunCommandInput via the Azure Compute SDK.
//     RunCommandAsync returns a CommandHandle wrapping the operation poller.
//     Poll maps the Azure async operation status to (result, done, err).
//     Cancel sends a DELETE to the running command resource.
//
//   - GCP OS Login / Cloud Agent: Call projects.instances.runCommand via
//     the Compute Engine API. RunCommandAsync returns a CommandHandle wrapping
//     the GCP operation. Poll checks the operation status via operations.get.
//     Cancel calls operations.delete or waits for natural timeout.
//
// Both patterns follow the same lifecycle: fire (RunCommandAsync) -> poll
// (CommandHandle.Poll in a backoff loop) -> collect result or cancel.
//
// Use type assertion to check for pollable support:
//
//	if pi, ok := instance.(compute.PollableCommandInstance); ok {
//	    handle, err := pi.RunCommandAsync(ctx, cmd, opts)
//	    for { result, done, _ := handle.Poll(ctx) ... }
//	}
type PollableCommandInstance interface {
	Instance

	// RunCommandAsync starts a command and returns a handle for polling completion.
	// The command runs asynchronously on the remote instance.
	RunCommandAsync(ctx context.Context, command []string, opts *RunCommandOptions) (CommandHandle, error)
}

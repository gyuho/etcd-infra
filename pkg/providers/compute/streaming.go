package compute

import (
	"context"
	"io"
	"time"
)

// StreamingOptions configures real-time output streaming for long-running commands.
// This is the streaming counterpart to RunCommandOptions. The additional
// Stdout/Stderr writers receive output as it is produced, rather than buffering
// all output until command completion. Use this when you need to see output as
// it happens (e.g., bootstrap scripts, build processes, test runs).
type StreamingOptions struct {
	// Timeout is the maximum duration for the command.
	// Zero means use the provider's default timeout.
	Timeout time.Duration

	// WorkDir is the working directory for command execution.
	// Empty string means use the provider's default.
	WorkDir string

	// Stdin provides input to the command.
	// Nil means no stdin input.
	Stdin io.Reader

	// Stdout receives real-time stdout output as it's produced.
	// Nil means discard stdout streaming (output is still captured in ExecuteResult).
	Stdout io.Writer

	// Stderr receives real-time stderr output as it's produced.
	// Nil means discard stderr streaming (output is still captured in ExecuteResult).
	Stderr io.Writer

	// ProviderConfig holds provider-specific streaming options.
	// Each provider defines its own config struct. Providers silently ignore
	// unrecognized or nil config types. This mirrors RunCommandOptions.ProviderConfig.
	ProviderConfig any
}

// StreamingInstance extends Instance with real-time output streaming support.
// Providers that support streaming implement this interface on their Instance types.
//
// Use type assertion to check for streaming support:
//
//	if si, ok := instance.(compute.StreamingInstance); ok {
//	    result, err := si.RunCommandWithStreaming(ctx, cmd, opts)
//	}
type StreamingInstance interface {
	Instance
	RunCommandWithStreaming(ctx context.Context, command []string, opts *StreamingOptions) (*ExecuteResult, error)
}

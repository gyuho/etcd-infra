package compute

import (
	"context"
	"fmt"
)

// streamingAdapter wraps a non-streaming Instance and implements
// StreamingInstance by calling RunCommandWithOptions and flushing the
// buffered output to the provided io.Writer streams after completion.
//
// This allows providers that only support poll-based command execution
// (e.g., AWS SSM) to satisfy the StreamingInstance interface. The trade-off
// is that output appears all at once when the command finishes, rather than
// streaming in real time. Callers see identical final results; only the
// streaming latency differs.
type streamingAdapter struct {
	Instance
}

// EnsureStreaming returns a StreamingInstance. If inst already implements
// StreamingInstance, it is returned as-is. Otherwise, a buffered adapter
// wraps it so that RunCommandWithStreaming works by delegating to
// RunCommandWithOptions and flushing stdout/stderr afterward.
func EnsureStreaming(inst Instance) StreamingInstance {
	if si, ok := inst.(StreamingInstance); ok {
		return si
	}
	return &streamingAdapter{Instance: inst}
}

func (a *streamingAdapter) RunCommandWithStreaming(ctx context.Context, command []string, opts *StreamingOptions) (*ExecuteResult, error) {
	var cmdOpts *RunCommandOptions
	if opts != nil {
		cmdOpts = &RunCommandOptions{
			Timeout:        opts.Timeout,
			WorkDir:        opts.WorkDir,
			Stdin:          opts.Stdin,
			ProviderConfig: opts.ProviderConfig,
		}
	}

	result, err := a.Instance.RunCommandWithOptions(ctx, command, cmdOpts)
	if err != nil {
		return result, fmt.Errorf("run command with options for streaming adapter: %w", err)
	}

	// Flush buffered output to the streaming writers so callers that
	// read from the writers (e.g., io.MultiWriter to a bytes.Buffer)
	// see the same content as with a natively streaming provider.
	if result != nil && opts != nil {
		if opts.Stdout != nil && result.Stdout != "" {
			_, _ = opts.Stdout.Write([]byte(result.Stdout))
		}
		if opts.Stderr != nil && result.Stderr != "" {
			_, _ = opts.Stderr.Write([]byte(result.Stderr))
		}
	}

	return result, nil
}

package compute

import (
	"context"
	"fmt"
	"time"
)

// pollableAdapter wraps a PollableCommandInstance and implements the
// synchronous Instance interface by calling RunCommandAsync and polling
// the returned CommandHandle in an exponential backoff loop until the
// command completes or the context expires.
//
// This is for providers where command execution is fire-and-poll rather
// than synchronous, e.g., Azure VM Run Command and GCP OS Login. These
// providers submit a command via a cloud API and must poll an operation
// resource to retrieve the result. The adapter hides that async lifecycle
// behind the standard RunCommand/RunCommandWithOptions contract so that
// orchestration code can treat all providers uniformly.
//
// The backoff starts at 500ms and doubles each iteration up to a 5s cap.
// The overall timeout comes from RunCommandOptions.Timeout (default: 5m).
type pollableAdapter struct {
	inner PollableCommandInstance
}

// pollBackoffInitial is the starting interval for the polling backoff.
const pollBackoffInitial = 500 * time.Millisecond

// pollBackoffMax is the maximum interval between polls.
const pollBackoffMax = 5 * time.Second

// pollDefaultTimeout is used when RunCommandOptions.Timeout is zero.
const pollDefaultTimeout = 5 * time.Minute

// EnsureSynchronous returns an Instance with synchronous RunCommand support.
// If inst already implements Instance but NOT PollableCommandInstance, it is
// returned as-is (it already has synchronous execution). If inst implements
// PollableCommandInstance, a pollableAdapter wraps it so that RunCommand and
// RunCommandWithOptions block until completion by polling the async handle.
//
// This mirrors EnsureStreaming: orchestration code calls EnsureSynchronous
// once and then uses the standard Instance interface without caring whether
// the underlying provider is synchronous or poll-based.
func EnsureSynchronous(inst Instance) Instance {
	pi, ok := inst.(PollableCommandInstance)
	if !ok {
		return inst
	}
	return &pollableAdapter{inner: pi}
}

func (a *pollableAdapter) ID() string           { return a.inner.ID() }
func (a *pollableAdapter) PublicIPv4() string   { return a.inner.PublicIPv4() }
func (a *pollableAdapter) PrivateIPv4() string  { return a.inner.PrivateIPv4() }
func (a *pollableAdapter) State() InstanceState { return a.inner.State() }

func (a *pollableAdapter) RunCommand(ctx context.Context, command []string) (*ExecuteResult, error) {
	return a.RunCommandWithOptions(ctx, command, nil)
}

func (a *pollableAdapter) RunCommandWithOptions(ctx context.Context, command []string, opts *RunCommandOptions) (*ExecuteResult, error) {
	timeout := pollDefaultTimeout
	if opts != nil && opts.Timeout > 0 {
		timeout = opts.Timeout
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	handle, err := a.inner.RunCommandAsync(ctx, command, opts)
	if err != nil {
		return nil, fmt.Errorf("run command async: %w", err)
	}

	return a.poll(ctx, handle)
}

// poll drives the backoff loop for an in-flight async command.
func (a *pollableAdapter) poll(ctx context.Context, handle CommandHandle) (*ExecuteResult, error) {
	interval := pollBackoffInitial
	timer := time.NewTimer(interval)
	defer timer.Stop()

	for {
		result, done, err := handle.Poll(ctx)
		if err != nil {
			return nil, fmt.Errorf("poll command: %w", err)
		}
		if done {
			return result, nil
		}

		// Reset the timer for the current interval. Drain any pending
		// fire before resetting so the next select reads the correct tick.
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timer.Reset(interval)

		select {
		case <-ctx.Done():
			// Best-effort cancel with a fresh context so the cancel RPC
			// is not immediately rejected by the already-expired parent.
			cancelCtx, cancelCancel := context.WithTimeout(context.Background(), 5*time.Second)
			_ = handle.Cancel(cancelCtx) //nolint:contextcheck // intentionally detached context for cleanup after parent expiry
			cancelCancel()
			return nil, fmt.Errorf("poll command: %w", ctx.Err())
		case <-timer.C:
		}

		// Exponential backoff capped at pollBackoffMax.
		interval *= 2
		if interval > pollBackoffMax {
			interval = pollBackoffMax
		}
	}
}

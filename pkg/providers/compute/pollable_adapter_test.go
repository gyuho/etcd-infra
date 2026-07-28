package compute_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"git.tbd/etcd-infra/pkg/providers/compute"
)

// ─── EnsureSynchronous ──────────────────────────────────────────────────────

func TestEnsureSynchronous_AlreadySynchronous(t *testing.T) {
	t.Parallel()

	orig := &fakeInstance{id: "already-sync"}
	result := compute.EnsureSynchronous(orig)

	// Should return the same instance, not wrap it.
	assert.Same(t, orig, result)
}

func TestEnsureSynchronous_WrapsPollable(t *testing.T) {
	t.Parallel()

	orig := &pollableFake{
		fakeInstance: fakeInstance{id: "pollable-vm"},
		handle: &pollHandleFake{
			maxPolls: 1,
			result:   &compute.ExecuteResult{ExitCode: 0},
		},
	}
	result := compute.EnsureSynchronous(orig)

	// Should be a different object (the adapter).
	assert.NotSame(t, orig, result)
	// But ID() should delegate to the inner instance.
	assert.Equal(t, "pollable-vm", result.ID())
}

// ─── pollableAdapter delegation ─────────────────────────────────────────────

func TestPollableAdapter_DelegatesID(t *testing.T) {
	t.Parallel()

	inst := ensureWrapped(t, "my-id", nil)
	assert.Equal(t, "my-id", inst.ID())
}

func TestPollableAdapter_DelegatesPublicIPv4(t *testing.T) {
	t.Parallel()

	orig := &pollableFake{
		fakeInstance: fakeInstance{id: "vm", publicIP: "54.1.2.3"},
		handle: &pollHandleFake{
			maxPolls: 1,
			result:   &compute.ExecuteResult{ExitCode: 0},
		},
	}
	inst := compute.EnsureSynchronous(orig)
	assert.Equal(t, "54.1.2.3", inst.PublicIPv4())
}

func TestPollableAdapter_DelegatesState(t *testing.T) {
	t.Parallel()

	orig := &pollableFake{
		fakeInstance: fakeInstance{id: "vm"},
		handle: &pollHandleFake{
			maxPolls: 1,
			result:   &compute.ExecuteResult{ExitCode: 0},
		},
	}
	inst := compute.EnsureSynchronous(orig)
	assert.Equal(t, compute.InstanceStateRunning, inst.State())
}

// ─── pollableAdapter.RunCommand ─────────────────────────────────────────────

func TestPollableAdapter_ImmediateCompletion(t *testing.T) {
	t.Parallel()

	inst := ensureWrapped(t, "fast-vm", &pollHandleFake{
		maxPolls: 1,
		result: &compute.ExecuteResult{
			ExitCode: 0,
			Stdout:   "done instantly",
		},
	})

	result, err := inst.RunCommand(context.Background(), []string{"echo", "hello"})
	require.NoError(t, err)
	assert.Equal(t, 0, result.ExitCode)
	assert.Equal(t, "done instantly", result.Stdout)
}

func TestPollableAdapter_DelayedCompletion(t *testing.T) {
	t.Parallel()

	var pollCount atomic.Int32
	inst := ensureWrapped(t, "slow-vm", &pollHandleFake{
		maxPolls: 3,
		result: &compute.ExecuteResult{
			ExitCode: 0,
			Stdout:   "finished after polling",
		},
		onPoll: func() { pollCount.Add(1) },
	})

	result, err := inst.RunCommand(context.Background(), []string{"make", "build"})
	require.NoError(t, err)
	assert.Equal(t, 0, result.ExitCode)
	assert.Equal(t, "finished after polling", result.Stdout)
	// Should have polled at least 3 times (2 not-done + 1 done).
	assert.GreaterOrEqual(t, pollCount.Load(), int32(3))
}

func TestPollableAdapter_ContextCancellation(t *testing.T) {
	t.Parallel()

	handle := &pollHandleFake{
		maxPolls: 1000, // Never completes naturally.
		result:   &compute.ExecuteResult{ExitCode: 0},
	}
	inst := ensureWrapped(t, "cancel-vm", handle)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	_, err := inst.RunCommand(ctx, []string{"sleep", "forever"})
	require.Error(t, err)
	assert.ErrorIs(t, err, context.DeadlineExceeded)
}

func TestPollableAdapter_PollError(t *testing.T) {
	t.Parallel()

	inst := ensureWrapped(t, "err-vm", &pollHandleFake{
		pollErr: errors.New("api unavailable"),
	})

	_, err := inst.RunCommand(context.Background(), []string{"cmd"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "api unavailable")
}

func TestPollableAdapter_AsyncStartError(t *testing.T) {
	t.Parallel()

	orig := &pollableFake{
		fakeInstance: fakeInstance{id: "start-err-vm"},
		asyncErr:     errors.New("rate limit exceeded"),
	}
	inst := compute.EnsureSynchronous(orig)

	_, err := inst.RunCommand(context.Background(), []string{"cmd"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "rate limit exceeded")
}

func TestPollableAdapter_NonZeroExitCode(t *testing.T) {
	t.Parallel()

	inst := ensureWrapped(t, "fail-vm", &pollHandleFake{
		maxPolls: 1,
		result: &compute.ExecuteResult{
			ExitCode: 1,
			Stderr:   "command not found",
		},
	})

	result, err := inst.RunCommand(context.Background(), []string{"bad-cmd"})
	require.NoError(t, err)
	assert.Equal(t, 1, result.ExitCode)
	assert.Equal(t, "command not found", result.Stderr)
}

func TestPollableAdapter_WithOptions_Timeout(t *testing.T) {
	t.Parallel()

	handle := &pollHandleFake{
		maxPolls: 1000, // Never completes naturally.
		result:   &compute.ExecuteResult{ExitCode: 0},
	}
	inst := ensureWrapped(t, "timeout-vm", handle)

	// Use a short custom timeout via RunCommandWithOptions.
	_, err := inst.RunCommandWithOptions(context.Background(), []string{"slow"}, &compute.RunCommandOptions{
		Timeout: 200 * time.Millisecond,
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, context.DeadlineExceeded)
}

func TestPollableAdapter_WithOptions_NilOpts(t *testing.T) {
	t.Parallel()

	inst := ensureWrapped(t, "nil-opts-vm", &pollHandleFake{
		maxPolls: 1,
		result:   &compute.ExecuteResult{ExitCode: 0, Stdout: "ok"},
	})

	result, err := inst.RunCommandWithOptions(context.Background(), []string{"echo"}, nil)
	require.NoError(t, err)
	assert.Equal(t, "ok", result.Stdout)
}

func TestPollableAdapter_CancelReceivesFreshContext(t *testing.T) {
	t.Parallel()

	handle := &pollHandleFake{
		maxPolls: 1000, // Never completes naturally.
		result:   &compute.ExecuteResult{ExitCode: 0},
	}
	orig := &pollableFake{
		fakeInstance: fakeInstance{id: "cancel-ctx-vm"},
		handle:       handle,
	}
	inst := compute.EnsureSynchronous(orig)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	_, err := inst.RunCommand(ctx, []string{"slow"})
	require.Error(t, err)
	require.ErrorIs(t, err, context.DeadlineExceeded)

	// The cancel context must NOT be the expired parent; it should be a
	// fresh context so the cancel RPC can actually reach the remote.
	require.True(t, handle.cancelCalled, "Cancel should have been called")
	assert.NoError(t, handle.cancelCtxErr, "Cancel context should not be expired at call time")
}

// ─── test helpers ────────────────────────────────────────────────────────────

// ensureWrapped creates a pollableFake with the given parameters and wraps it
// through EnsureSynchronous.
func ensureWrapped(t *testing.T, id string, handle *pollHandleFake) compute.Instance {
	t.Helper()
	orig := &pollableFake{
		fakeInstance: fakeInstance{id: id},
		handle:       handle,
	}
	return compute.EnsureSynchronous(orig)
}

// ─── test doubles (prefixed to avoid collision with streaming_adapter_test) ──

// pollHandleFake implements compute.CommandHandle for testing.
type pollHandleFake struct {
	pollCount    int
	maxPolls     int
	result       *compute.ExecuteResult
	pollErr      error
	onPoll       func()
	cancelCalled bool  // whether Cancel was called
	cancelCtxErr error // ctx.Err() captured at the moment Cancel was called
}

func (h *pollHandleFake) Poll(_ context.Context) (*compute.ExecuteResult, bool, error) {
	h.pollCount++
	if h.onPoll != nil {
		h.onPoll()
	}
	if h.pollErr != nil {
		return nil, false, h.pollErr
	}
	if h.pollCount >= h.maxPolls {
		return h.result, true, nil
	}
	return nil, false, nil
}

func (h *pollHandleFake) Cancel(ctx context.Context) error {
	h.cancelCalled = true
	h.cancelCtxErr = ctx.Err() // snapshot at call time, before caller runs cancelCancel()
	return nil
}

// pollableFake implements PollableCommandInstance for testing.
type pollableFake struct {
	fakeInstance

	handle   *pollHandleFake
	asyncErr error
}

func (f *pollableFake) RunCommandAsync(_ context.Context, _ []string, _ *compute.RunCommandOptions) (compute.CommandHandle, error) {
	if f.asyncErr != nil {
		return nil, f.asyncErr
	}
	return f.handle, nil
}

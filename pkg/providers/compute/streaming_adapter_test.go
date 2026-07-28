package compute_test

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"git.tbd/etcd-infra/pkg/providers/compute"
)

// ─── EnsureStreaming ─────────────────────────────────────────────────────────

func TestEnsureStreaming_AlreadyStreaming(t *testing.T) {
	t.Parallel()

	orig := &fakeStreamingInstance{fakeInstance: fakeInstance{id: "already-streaming"}}
	result := compute.EnsureStreaming(orig)

	// Should return the same instance, not wrap it.
	assert.Same(t, orig, result)
}

func TestEnsureStreaming_WrapsNonStreaming(t *testing.T) {
	t.Parallel()

	orig := &fakeInstance{id: "basic"}
	result := compute.EnsureStreaming(orig)

	// Should be a different object (the adapter).
	assert.NotSame(t, orig, result)
	// But ID() should delegate to the inner instance.
	assert.Equal(t, "basic", result.ID())
}

// ─── streamingAdapter.RunCommandWithStreaming ─────────────────────────────────

func TestStreamingAdapter_Success(t *testing.T) {
	t.Parallel()

	inner := &fakeInstance{
		id: "test-vm",
		result: &compute.ExecuteResult{
			ExitCode: 0,
			Stdout:   "hello world\n",
			Stderr:   "some warning\n",
		},
	}

	si := compute.EnsureStreaming(inner)

	var stdout, stderr bytes.Buffer
	result, err := si.RunCommandWithStreaming(context.Background(), []string{"echo", "hello"}, &compute.StreamingOptions{
		Timeout: 30 * time.Second,
		Stdout:  &stdout,
		Stderr:  &stderr,
	})

	require.NoError(t, err)
	assert.Equal(t, 0, result.ExitCode)
	assert.Equal(t, "hello world\n", result.Stdout)
	assert.Equal(t, "some warning\n", result.Stderr)

	// Verify output was flushed to the writers.
	assert.Equal(t, "hello world\n", stdout.String())
	assert.Equal(t, "some warning\n", stderr.String())
}

func TestStreamingAdapter_NilOpts(t *testing.T) {
	t.Parallel()

	inner := &fakeInstance{
		id:     "test-vm",
		result: &compute.ExecuteResult{ExitCode: 0, Stdout: "ok"},
	}

	si := compute.EnsureStreaming(inner)
	result, err := si.RunCommandWithStreaming(context.Background(), []string{"echo"}, nil)

	require.NoError(t, err)
	assert.Equal(t, "ok", result.Stdout)
}

func TestStreamingAdapter_NilWriters(t *testing.T) {
	t.Parallel()

	inner := &fakeInstance{
		id:     "test-vm",
		result: &compute.ExecuteResult{ExitCode: 0, Stdout: "data"},
	}

	si := compute.EnsureStreaming(inner)
	result, err := si.RunCommandWithStreaming(context.Background(), []string{"cmd"}, &compute.StreamingOptions{
		// Stdout and Stderr nil → no panic, output discarded.
	})

	require.NoError(t, err)
	assert.Equal(t, "data", result.Stdout)
}

func TestStreamingAdapter_Error(t *testing.T) {
	t.Parallel()

	inner := &fakeInstance{
		id:  "fail-vm",
		err: errors.New("connection refused"),
	}

	si := compute.EnsureStreaming(inner)
	_, err := si.RunCommandWithStreaming(context.Background(), []string{"cmd"}, &compute.StreamingOptions{
		Stdout: &bytes.Buffer{},
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "connection refused")
}

func TestStreamingAdapter_EmptyOutput(t *testing.T) {
	t.Parallel()

	inner := &fakeInstance{
		id:     "quiet-vm",
		result: &compute.ExecuteResult{ExitCode: 0, Stdout: "", Stderr: ""},
	}

	si := compute.EnsureStreaming(inner)
	var stdout, stderr bytes.Buffer
	result, err := si.RunCommandWithStreaming(context.Background(), []string{"true"}, &compute.StreamingOptions{
		Stdout: &stdout,
		Stderr: &stderr,
	})

	require.NoError(t, err)
	assert.Equal(t, 0, result.ExitCode)
	assert.Empty(t, stdout.String())
	assert.Empty(t, stderr.String())
}

func TestStreamingAdapter_DelegatesID(t *testing.T) {
	t.Parallel()

	inner := &fakeInstance{id: "my-instance-123"}
	si := compute.EnsureStreaming(inner)
	assert.Equal(t, "my-instance-123", si.ID())
}

func TestStreamingAdapter_DelegatesPublicIPv4(t *testing.T) {
	t.Parallel()

	inner := &fakeInstance{id: "vm", publicIP: "54.1.2.3"}
	si := compute.EnsureStreaming(inner)
	assert.Equal(t, "54.1.2.3", si.PublicIPv4())
}

func TestStreamingAdapter_TimeoutPassthrough(t *testing.T) {
	t.Parallel()

	var capturedOpts *compute.RunCommandOptions
	inner := &fakeInstanceWithCapture{
		fakeInstance: fakeInstance{
			id:     "vm",
			result: &compute.ExecuteResult{ExitCode: 0},
		},
		capture: func(opts *compute.RunCommandOptions) {
			capturedOpts = opts
		},
	}

	si := compute.EnsureStreaming(inner)
	_, err := si.RunCommandWithStreaming(context.Background(), []string{"cmd"}, &compute.StreamingOptions{
		Timeout: 5 * time.Minute,
		WorkDir: "/tmp",
	})

	require.NoError(t, err)
	require.NotNil(t, capturedOpts)
	assert.Equal(t, 5*time.Minute, capturedOpts.Timeout)
	assert.Equal(t, "/tmp", capturedOpts.WorkDir)
}

// ─── test doubles ────────────────────────────────────────────────────────────

type fakeInstance struct {
	id       string
	publicIP string
	result   *compute.ExecuteResult
	err      error
}

func (f *fakeInstance) ID() string          { return f.id }
func (f *fakeInstance) PublicIPv4() string  { return f.publicIP }
func (f *fakeInstance) PrivateIPv4() string { return "" }
func (f *fakeInstance) State() compute.InstanceState {
	return compute.InstanceStateRunning
}

func (f *fakeInstance) RunCommand(_ context.Context, _ []string) (*compute.ExecuteResult, error) {
	return f.result, f.err
}

func (f *fakeInstance) RunCommandWithOptions(_ context.Context, _ []string, _ *compute.RunCommandOptions) (*compute.ExecuteResult, error) {
	return f.result, f.err
}

// fakeStreamingInstance already implements StreamingInstance.
type fakeStreamingInstance struct {
	fakeInstance
}

func (f *fakeStreamingInstance) RunCommandWithStreaming(_ context.Context, _ []string, _ *compute.StreamingOptions) (*compute.ExecuteResult, error) {
	return f.result, f.err
}

// fakeInstanceWithCapture captures the RunCommandOptions for assertion.
type fakeInstanceWithCapture struct {
	fakeInstance

	capture func(opts *compute.RunCommandOptions)
}

func (f *fakeInstanceWithCapture) RunCommandWithOptions(_ context.Context, _ []string, opts *compute.RunCommandOptions) (*compute.ExecuteResult, error) {
	if f.capture != nil {
		f.capture(opts)
	}
	return f.result, f.err
}

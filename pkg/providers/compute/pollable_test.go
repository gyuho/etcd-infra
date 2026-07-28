package compute

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockCommandHandle implements the CommandHandle interface for testing.
type mockCommandHandle struct {
	pollCount   int
	maxPolls    int
	result      *ExecuteResult
	err         error
	isCancelled bool
}

func (m *mockCommandHandle) Poll(_ context.Context) (*ExecuteResult, bool, error) {
	m.pollCount++

	if m.err != nil {
		return nil, false, m.err
	}

	if m.isCancelled {
		return &ExecuteResult{ExitCode: 130, Stderr: "canceled"}, true, nil
	}

	if m.pollCount >= m.maxPolls {
		return m.result, true, nil
	}

	return nil, false, nil
}

func (m *mockCommandHandle) Cancel(_ context.Context) error {
	m.isCancelled = true
	return nil
}

func TestCommandHandle_PollUntilCompletion(t *testing.T) {
	t.Parallel()

	handle := &mockCommandHandle{
		maxPolls: 3,
		result:   &ExecuteResult{ExitCode: 0, Stdout: "done"},
	}

	// Polls 1-2: not done.
	for range 2 {
		result, done, err := handle.Poll(context.Background())
		require.NoError(t, err)
		assert.False(t, done)
		assert.Nil(t, result)
	}

	// Poll 3: done.
	result, done, err := handle.Poll(context.Background())
	require.NoError(t, err)
	assert.True(t, done)
	require.NotNil(t, result)
	assert.Equal(t, 0, result.ExitCode)
	assert.Equal(t, "done", result.Stdout)
}

func TestCommandHandle_PollError(t *testing.T) {
	t.Parallel()

	handle := &mockCommandHandle{err: assert.AnError}
	result, done, err := handle.Poll(context.Background())
	require.Error(t, err)
	assert.False(t, done)
	assert.Nil(t, result)
}

func TestCommandHandle_Cancel(t *testing.T) {
	t.Parallel()

	handle := &mockCommandHandle{maxPolls: 10}

	// Poll once: not done.
	_, done, err := handle.Poll(context.Background())
	require.NoError(t, err)
	assert.False(t, done)

	// Cancel.
	require.NoError(t, handle.Cancel(context.Background()))

	// Next poll returns the canceled result.
	result, done, err := handle.Poll(context.Background())
	require.NoError(t, err)
	assert.True(t, done)
	assert.Equal(t, 130, result.ExitCode)
}

// mockPollableInstance implements both Instance and PollableCommandInstance interfaces.
type mockPollableInstance struct {
	mockInstance

	handleToReturn CommandHandle
}

func (m *mockPollableInstance) RunCommandAsync(_ context.Context, command []string, _ *RunCommandOptions) (CommandHandle, error) {
	m.lastCommand = command
	return m.handleToReturn, nil
}

func TestPollableCommandInstance_TypeAssertion(t *testing.T) {
	t.Parallel()

	t.Run("success", func(t *testing.T) {
		t.Parallel()

		pollableInst := &mockPollableInstance{
			mockInstance:   mockInstance{id: "pollable-inst-1"},
			handleToReturn: &mockCommandHandle{maxPolls: 1, result: &ExecuteResult{ExitCode: 0}},
		}

		var inst Instance = pollableInst
		pi, ok := inst.(PollableCommandInstance)
		require.True(t, ok)

		handle, err := pi.RunCommandAsync(context.Background(), []string{"echo"}, nil)
		require.NoError(t, err)
		require.NotNil(t, handle)
	})

	t.Run("failure for non-pollable instance", func(t *testing.T) {
		t.Parallel()

		var inst Instance = &mockInstance{id: "regular-inst"}
		_, ok := inst.(PollableCommandInstance)
		assert.False(t, ok)
	})
}

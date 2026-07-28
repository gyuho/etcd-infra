package compute

import (
	"bytes"
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockStreamingInstance implements both Instance and StreamingInstance interfaces.
type mockStreamingInstance struct {
	mockInstance
}

func (m *mockStreamingInstance) RunCommandWithStreaming(_ context.Context, command []string, opts *StreamingOptions) (*ExecuteResult, error) {
	m.lastCommand = command

	// Simulate command output.
	stdout := "command output line 1\ncommand output line 2\n"
	stderr := "warning message\n"

	// Stream to provided writers if available.
	if opts != nil {
		if opts.Stdout != nil {
			_, _ = opts.Stdout.Write([]byte(stdout))
		}
		if opts.Stderr != nil {
			_, _ = opts.Stderr.Write([]byte(stderr))
		}
	}

	return &ExecuteResult{
		ExitCode: 0,
		Stdout:   stdout,
		Stderr:   stderr,
	}, nil
}

func TestStreamingInstance_TypeAssertion(t *testing.T) {
	t.Parallel()

	t.Run("success", func(t *testing.T) {
		t.Parallel()

		streamInst := &mockStreamingInstance{
			mockInstance: mockInstance{id: "stream-inst-1", publicIP: "1.2.3.4"},
		}

		var inst Instance = streamInst
		si, ok := inst.(StreamingInstance)
		require.True(t, ok, "type assertion to StreamingInstance should succeed")

		var stdout, stderr bytes.Buffer
		result, err := si.RunCommandWithStreaming(context.Background(),
			[]string{"echo", "test"}, &StreamingOptions{
				Stdout: &stdout,
				Stderr: &stderr,
			})
		require.NoError(t, err)
		assert.Equal(t, 0, result.ExitCode)
		assert.Contains(t, stdout.String(), "command output")
		assert.Contains(t, stderr.String(), "warning")
	})

	t.Run("failure for non-streaming instance", func(t *testing.T) {
		t.Parallel()

		var inst Instance = &mockInstance{id: "regular-inst"}
		_, ok := inst.(StreamingInstance)
		assert.False(t, ok)
	})
}

func TestStreamingInstance_NilOptions(t *testing.T) {
	t.Parallel()

	streamInst := &mockStreamingInstance{}
	result, err := streamInst.RunCommandWithStreaming(context.Background(),
		[]string{"echo", "test"}, nil)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, 0, result.ExitCode)
}

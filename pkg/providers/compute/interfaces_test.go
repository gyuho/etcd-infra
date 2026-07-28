package compute

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestInstanceState_IsTerminal(t *testing.T) {
	t.Parallel()

	tests := []struct {
		state    InstanceState
		terminal bool
	}{
		{InstanceStateUnknown, false},
		{InstanceStatePending, false},
		{InstanceStateRunning, false},
		{InstanceStateStopping, false},
		// Stopped is NOT terminal; instances can be restarted via PowerControl.Start().
		{InstanceStateStopped, false},
		{InstanceStateTerminated, true},
		{InstanceState(""), false},
		{InstanceState("custom"), false},
	}
	for _, tt := range tests {
		require.Equal(t, tt.terminal, tt.state.IsTerminal(), "IsTerminal(%q)", tt.state)
	}
}

func TestInstanceState_IsRunning(t *testing.T) {
	t.Parallel()

	require.True(t, InstanceStateRunning.IsRunning())
	require.False(t, InstanceStateStopped.IsRunning())
	require.False(t, InstanceStateTerminated.IsRunning())
	require.False(t, InstanceStatePending.IsRunning())
	require.False(t, InstanceState("").IsRunning())
}

// mockInstance implements the Instance interface for testing.
// It deliberately does NOT implement FileTransferInstance. Tests that need
// file-transfer capability should use mockTransferInstance from
// file_transfer_test.go instead.
type mockInstance struct {
	id          string
	publicIP    string
	privateIP   string
	state       InstanceState
	lastCommand []string
}

func (m *mockInstance) ID() string          { return m.id }
func (m *mockInstance) PublicIPv4() string  { return m.publicIP }
func (m *mockInstance) PrivateIPv4() string { return m.privateIP }
func (m *mockInstance) State() InstanceState {
	if m.state == "" {
		return InstanceStateRunning
	}
	return m.state
}

func (m *mockInstance) RunCommand(_ context.Context, command []string) (*ExecuteResult, error) {
	m.lastCommand = command
	return &ExecuteResult{ExitCode: 0, Stdout: "ok"}, nil
}

func (m *mockInstance) RunCommandWithOptions(_ context.Context, command []string, _ *RunCommandOptions) (*ExecuteResult, error) {
	m.lastCommand = command
	return &ExecuteResult{ExitCode: 0, Stdout: "ok"}, nil
}

func TestNewCreateRequest(t *testing.T) {
	t.Parallel()

	req := NewCreateRequest(
		WithID("i-123"),
		WithName("test-node"),
		WithSize("t3.micro"),
	)

	require.Equal(t, "i-123", req.Op.ID)
	require.Equal(t, "test-node", req.Op.Name)
	require.Equal(t, "t3.micro", req.Op.Size)
	require.Equal(t, 22, req.Op.SSHPort, "SSHPort should default to 22")
}

func TestNewDeleteRequest(t *testing.T) {
	t.Parallel()

	req := NewDeleteRequest("i-456", WithName("cleanup"))
	require.Equal(t, "i-456", req.ID)
	require.Equal(t, "cleanup", req.Op.Name)
	require.Equal(t, 22, req.Op.SSHPort, "SSHPort should default to 22")
}

func TestNewReplaceRequest(t *testing.T) {
	t.Parallel()

	req := NewReplaceRequest("i-456")
	require.Equal(t, "i-456", req.ID)
}

func TestNewPowerRequest(t *testing.T) {
	t.Parallel()

	req := NewPowerRequest("i-789")
	require.Equal(t, "i-789", req.ID)
	require.Equal(t, 22, req.Op.SSHPort, "SSHPort should default to 22")
}

package compute

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockSSHInstance implements both Instance and SSHInstance interfaces.
type mockSSHInstance struct {
	mockInstance

	sshConfig SSHConfig
}

func (m *mockSSHInstance) SSHInfo() SSHConfig {
	return m.sshConfig
}

func TestSSHInstance_TypeAssertion(t *testing.T) {
	t.Parallel()

	t.Run("success", func(t *testing.T) {
		t.Parallel()

		sshInst := &mockSSHInstance{
			mockInstance: mockInstance{id: "ssh-inst-1", publicIP: "1.2.3.4"},
			sshConfig: SSHConfig{
				User:           "root",
				Port:           22,
				PrivateKeyPath: "/path/to/key",
			},
		}

		var inst Instance = sshInst
		si, ok := inst.(SSHInstance)
		require.True(t, ok, "type assertion to SSHInstance should succeed")

		cfg := si.SSHInfo()
		assert.Equal(t, "root", cfg.User)
		assert.Equal(t, 22, cfg.Port)
		assert.Equal(t, "/path/to/key", cfg.PrivateKeyPath)
	})

	t.Run("failure for non-SSH instance", func(t *testing.T) {
		t.Parallel()

		var inst Instance = &mockInstance{id: "regular-inst"}
		_, ok := inst.(SSHInstance)
		assert.False(t, ok)
	})
}

//nolint:testpackage,paralleltest // Tests use package internals and shared resources.
package stress

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"git.tbd/etcd-infra/internal/etcd/stress/scenarios"
)

func TestStressConfigValidateAndSetDefaults(t *testing.T) {
	_, ids := scenarios.ListAllIDs()
	require.NotEmpty(t, ids)

	valid := &Config{
		Endpoints:     []string{"https://127.0.0.1:2379"},
		TestKeyPrefix: "etcd-infra",
		ScenarioIDs:   []string{ids[0]},
	}
	require.NoError(t, valid.ValidateAndSetDefaults())
	assert.Equal(t, 60, valid.DurationSeconds)
	assert.Equal(t, 10, valid.ConcurrentWorkers)

	tests := []struct {
		name string
		cfg  Config
		want string
	}{
		{
			name: "missing endpoints",
			cfg: Config{
				TestKeyPrefix: "etcd-infra",
				ScenarioIDs:   []string{ids[0]},
			},
			want: "no endpoints",
		},
		{
			name: "missing test key prefix",
			cfg: Config{
				Endpoints:   []string{"https://127.0.0.1:2379"},
				ScenarioIDs: []string{ids[0]},
			},
			want: "no test key prefix",
		},
		{
			name: "missing scenarios",
			cfg: Config{
				Endpoints:     []string{"https://127.0.0.1:2379"},
				TestKeyPrefix: "etcd-infra",
			},
			want: "no scenarios",
		},
		{
			name: "unknown scenario",
			cfg: Config{
				Endpoints:     []string{"https://127.0.0.1:2379"},
				TestKeyPrefix: "etcd-infra",
				ScenarioIDs:   []string{"unknown"},
			},
			want: "unknown scenario",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.cfg.ValidateAndSetDefaults()
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.want)
		})
	}
}

func TestStressConfigJSONRoundTrip(t *testing.T) {
	_, ids := scenarios.ListAllIDs()
	require.NotEmpty(t, ids)

	cfg := Config{
		Endpoints:     []string{"https://127.0.0.1:2379"},
		TestKeyPrefix: "etcd-infra",
		ScenarioIDs:   []string{ids[0]},
	}

	data, err := cfg.JSON()
	require.NoError(t, err)

	parsed, err := ParseConfigJSON(data)
	require.NoError(t, err)
	assert.Equal(t, cfg, *parsed)

	path := filepath.Join(t.TempDir(), "config.json")
	require.NoError(t, cfg.SyncJSON(path))

	loaded, err := LoadConfigJSON(path)
	require.NoError(t, err)
	assert.Equal(t, cfg, *loaded)
}

func TestStressConfigYAMLRoundTrip(t *testing.T) {
	_, ids := scenarios.ListAllIDs()
	require.NotEmpty(t, ids)

	cfg := Config{
		Endpoints:     []string{"https://127.0.0.1:2379"},
		TestKeyPrefix: "etcd-infra",
		ScenarioIDs:   []string{ids[0]},
	}

	data, err := cfg.YAML()
	require.NoError(t, err)

	parsed, err := ParseConfigYAML(data)
	require.NoError(t, err)
	assert.Equal(t, cfg, *parsed)

	path := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, cfg.SyncYAML(path))

	loaded, err := LoadConfigYAML(path)
	require.NoError(t, err)
	assert.Equal(t, cfg, *loaded)
}

func TestStressDetermineRunnerTimeout(t *testing.T) {
	t.Parallel()

	// Step timeout parameter is used when provided
	assert.Equal(t, 2*time.Second, determineRunnerTimeout("2s"))

	// Empty step timeout returns default
	assert.Equal(t, runnerTimeoutDefault, determineRunnerTimeout(""))
}

func TestStressSyncJSONCreatesDir(t *testing.T) {
	_, ids := scenarios.ListAllIDs()
	require.NotEmpty(t, ids)

	cfg := Config{
		Endpoints:     []string{"https://127.0.0.1:2379"},
		TestKeyPrefix: "etcd-infra",
		ScenarioIDs:   []string{ids[0]},
	}

	path := filepath.Join(t.TempDir(), "nested", "config.json")
	require.NoError(t, cfg.SyncJSON(path))

	loaded, err := LoadConfigJSON(path)
	require.NoError(t, err)
	assert.Equal(t, cfg, *loaded)
}

func TestStressSyncYAMLCreatesDir(t *testing.T) {
	_, ids := scenarios.ListAllIDs()
	require.NotEmpty(t, ids)

	cfg := Config{
		Endpoints:     []string{"https://127.0.0.1:2379"},
		TestKeyPrefix: "etcd-infra",
		ScenarioIDs:   []string{ids[0]},
	}

	path := filepath.Join(t.TempDir(), "nested", "config.yaml")
	require.NoError(t, cfg.SyncYAML(path))

	loaded, err := LoadConfigYAML(path)
	require.NoError(t, err)
	assert.Equal(t, cfg, *loaded)
}

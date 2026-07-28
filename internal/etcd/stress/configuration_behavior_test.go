//nolint:all // Coverage-oriented tests intentionally use broad patterns for mock-heavy branch testing.
//nolint:testpackage // Need access to internals for thorough testing.
package stress

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConfigValidateAndSetDefaultsNoEndpoints(t *testing.T) {
	t.Parallel()

	cfg := &Config{}
	err := cfg.ValidateAndSetDefaults()
	require.Error(t, err)
	require.ErrorIs(t, err, errNoEndpoints)
}

func TestConfigValidateAndSetDefaultsNoPrefix(t *testing.T) {
	t.Parallel()

	cfg := &Config{Endpoints: []string{"http://localhost:2379"}}
	err := cfg.ValidateAndSetDefaults()
	require.Error(t, err)
	require.ErrorIs(t, err, errNoTestKeyPrefix)
}

func TestConfigValidateAndSetDefaultsNoScenarios(t *testing.T) {
	t.Parallel()

	cfg := &Config{
		Endpoints:     []string{"http://localhost:2379"},
		TestKeyPrefix: "/test/",
	}
	err := cfg.ValidateAndSetDefaults()
	require.Error(t, err)
	require.ErrorIs(t, err, errNoScenarios)
}

func TestConfigValidateAndSetDefaultsInvalidScenario(t *testing.T) {
	t.Parallel()

	cfg := &Config{
		Endpoints:     []string{"http://localhost:2379"},
		TestKeyPrefix: "/test/",
		ScenarioIDs:   []string{"NONEXISTENT_XYZ"},
	}
	err := cfg.ValidateAndSetDefaults()
	require.Error(t, err)
	require.ErrorIs(t, err, errUnknownScenario)
}

func TestConfigValidateAndSetDefaultsAppliesDefaults(t *testing.T) {
	t.Parallel()

	// Get a valid stress scenario ID
	var validID string
	for k := range stressScenarioIDMap() {
		validID = k
		break
	}
	if validID == "" {
		t.Skip("no stress scenarios defined")
	}

	cfg := &Config{
		Endpoints:     []string{"http://localhost:2379"},
		TestKeyPrefix: "/test/",
		ScenarioIDs:   []string{validID},
	}
	err := cfg.ValidateAndSetDefaults()
	require.NoError(t, err)

	assert.Equal(t, 60, cfg.DurationSeconds)
	assert.Equal(t, 10, cfg.ConcurrentWorkers)
	assert.Equal(t, 64, cfg.KeySizeBytes)
	assert.Equal(t, 256, cfg.ValueSizeBytes)
	assert.InDelta(t, 0.5, cfg.MaxErrorRate, 0.001)
	assert.Equal(t, 30000, cfg.MaxLatencyMs)
}

func TestConfigJSON(t *testing.T) {
	t.Parallel()

	cfg := &Config{
		Endpoints:     []string{"http://localhost:2379"},
		TestKeyPrefix: "/test/",
		ScenarioIDs:   []string{"test"},
	}
	data, err := cfg.JSON()
	require.NoError(t, err)
	require.NotEmpty(t, data)

	parsed, err := ParseConfigJSON(data)
	require.NoError(t, err)
	assert.Equal(t, cfg.Endpoints, parsed.Endpoints)
}

func TestConfigYAML(t *testing.T) {
	t.Parallel()

	cfg := &Config{
		Endpoints:     []string{"http://localhost:2379"},
		TestKeyPrefix: "/test/",
	}
	data, err := cfg.YAML()
	require.NoError(t, err)
	require.NotEmpty(t, data)

	parsed, err := ParseConfigYAML(data)
	require.NoError(t, err)
	assert.Equal(t, cfg.Endpoints, parsed.Endpoints)
}

func TestConfigSyncJSON(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	file := filepath.Join(tmpDir, "sub", "config.json")

	cfg := &Config{
		Endpoints:     []string{"http://localhost:2379"},
		TestKeyPrefix: "/test/",
	}
	err := cfg.SyncJSON(file)
	require.NoError(t, err)

	loaded, err := LoadConfigJSON(file)
	require.NoError(t, err)
	assert.Equal(t, cfg.Endpoints, loaded.Endpoints)
}

func TestConfigSyncYAML(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	file := filepath.Join(tmpDir, "sub", "config.yaml")

	cfg := &Config{
		Endpoints:     []string{"http://localhost:2379"},
		TestKeyPrefix: "/test/",
	}
	err := cfg.SyncYAML(file)
	require.NoError(t, err)

	loaded, err := LoadConfigYAML(file)
	require.NoError(t, err)
	assert.Equal(t, cfg.Endpoints, loaded.Endpoints)
}

func TestConfigSyncJSONExistingDir(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	file := filepath.Join(tmpDir, "config.json")

	cfg := &Config{Endpoints: []string{"http://localhost:2379"}}
	err := cfg.SyncJSON(file)
	require.NoError(t, err)

	_, err = os.Stat(file)
	require.NoError(t, err)
}

func TestConfigSyncYAMLExistingDir(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	file := filepath.Join(tmpDir, "config.yaml")

	cfg := &Config{Endpoints: []string{"http://localhost:2379"}}
	err := cfg.SyncYAML(file)
	require.NoError(t, err)

	_, err = os.Stat(file)
	require.NoError(t, err)
}

func TestLoadConfigJSONNotFound(t *testing.T) {
	t.Parallel()

	_, err := LoadConfigJSON("/nonexistent/config.json")
	require.Error(t, err)
}

func TestLoadConfigYAMLNotFound(t *testing.T) {
	t.Parallel()

	_, err := LoadConfigYAML("/nonexistent/config.yaml")
	require.Error(t, err)
}

func TestParseConfigJSONInvalid(t *testing.T) {
	t.Parallel()

	_, err := ParseConfigJSON([]byte("not json"))
	require.Error(t, err)
}

func TestParseConfigYAMLInvalid(t *testing.T) {
	t.Parallel()

	_, err := ParseConfigYAML([]byte(":\ninvalid:\n  - [broken"))
	require.Error(t, err)
}

func TestResolveScenarioIDsStress(t *testing.T) {
	t.Parallel()

	// With a specific ID
	result := resolveScenarioIDs("SPECIFIC_ID")
	assert.Equal(t, []string{"SPECIFIC_ID"}, result)

	// With empty string - returns all
	result = resolveScenarioIDs("")
	assert.Positive(t, len(result))

	// With whitespace
	result = resolveScenarioIDs("  ")
	assert.Positive(t, len(result))
}

// stressScenarioIDMap is a helper to get valid stress scenario IDs for testing.
func stressScenarioIDMap() map[string]bool {
	ids := resolveScenarioIDs("")
	m := make(map[string]bool, len(ids))
	for _, id := range ids {
		m[id] = true
	}
	return m
}

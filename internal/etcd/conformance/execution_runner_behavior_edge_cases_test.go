//nolint:all // Coverage-oriented tests intentionally use broad patterns for mock-heavy branch testing.
//nolint:testpackage // Need access to internals for thorough testing.
package conformance

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"git.tbd/etcd-infra/internal/etcd/conformance/scenarios"
)

func TestNormalizeEndpointsEmptySlice(t *testing.T) {
	t.Parallel()

	result := normalizeEndpoints([]string{})
	assert.Equal(t, []string{DefaultEndpoints}, result)
}

func TestNormalizeEndpointsMixedWhitespace(t *testing.T) {
	t.Parallel()

	result := normalizeEndpoints([]string{"  ", "\t", ""})
	assert.Equal(t, []string{DefaultEndpoints}, result)
}

func TestNormalizeEndpointsTrimsSpaces(t *testing.T) {
	t.Parallel()

	result := normalizeEndpoints([]string{"  http://a:2379  ", "  http://b:2379  "})
	assert.Equal(t, []string{"http://a:2379", "http://b:2379"}, result)
}

func TestResolveScenarioIDsTrimmed(t *testing.T) {
	t.Parallel()

	// Whitespace-only should resolve all
	ids := resolveScenarioIDs("   ")
	assert.Positive(t, len(ids))
}

func TestResolveScenarioIDsSpecific(t *testing.T) {
	t.Parallel()

	ids := resolveScenarioIDs(" MY_SCENARIO ")
	assert.Equal(t, []string{"MY_SCENARIO"}, ids)
}

func TestNormalizeTestKeyPrefixWhitespace(t *testing.T) {
	t.Parallel()

	assert.Equal(t, DefaultTestKeyPrefix, normalizeTestKeyPrefix("   "))
}

func TestCountResultsEmpty(t *testing.T) {
	t.Parallel()

	passed, failed := countResults(nil)
	assert.Equal(t, 0, passed)
	assert.Equal(t, 0, failed)
}

func TestCountResultsAllPassed(t *testing.T) {
	t.Parallel()

	results := []scenarios.Result{
		{Success: true},
		{Success: true},
	}
	passed, failed := countResults(results)
	assert.Equal(t, 2, passed)
	assert.Equal(t, 0, failed)
}

func TestCountResultsAllFailed(t *testing.T) {
	t.Parallel()

	results := []scenarios.Result{
		{Success: false},
		{Success: false},
	}
	passed, failed := countResults(results)
	assert.Equal(t, 0, passed)
	assert.Equal(t, 2, failed)
}

func TestConfigValidateNoEndpoints(t *testing.T) {
	t.Parallel()

	cfg := &Config{}
	err := cfg.ValidateAndSetDefaults()
	require.Error(t, err)
	require.ErrorIs(t, err, errNoEndpoints)
}

func TestConfigValidateNoPrefix(t *testing.T) {
	t.Parallel()

	cfg := &Config{Endpoints: []string{"http://localhost:2379"}}
	err := cfg.ValidateAndSetDefaults()
	require.Error(t, err)
	require.ErrorIs(t, err, errNoTestKeyPrefix)
}

func TestConfigValidateNoScenarios(t *testing.T) {
	t.Parallel()

	cfg := &Config{
		Endpoints:     []string{"http://localhost:2379"},
		TestKeyPrefix: "/test/",
	}
	err := cfg.ValidateAndSetDefaults()
	require.Error(t, err)
	require.ErrorIs(t, err, errNoScenarios)
}

func TestConfigValidateUnknownScenario(t *testing.T) {
	t.Parallel()

	cfg := &Config{
		Endpoints:     []string{"http://localhost:2379"},
		TestKeyPrefix: "/test/",
		ScenarioIDs:   []string{"NONEXISTENT_SCENARIO_XYZ"},
	}
	err := cfg.ValidateAndSetDefaults()
	require.Error(t, err)
	require.ErrorIs(t, err, errUnknownScenario)
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

func TestConfigSyncJSONExistingDir(t *testing.T) {
	t.Parallel()

	_, ids := scenarios.ListAllIDs()
	require.NotEmpty(t, ids)

	tmpDir := t.TempDir()
	file := filepath.Join(tmpDir, "config.json")
	cfg := Config{
		Endpoints:     []string{"http://localhost:2379"},
		TestKeyPrefix: "/test/",
		ScenarioIDs:   []string{ids[0]},
	}
	require.NoError(t, cfg.SyncJSON(file))
	_, err := os.Stat(file)
	require.NoError(t, err)
}

func TestConfigSyncYAMLExistingDir(t *testing.T) {
	t.Parallel()

	_, ids := scenarios.ListAllIDs()
	require.NotEmpty(t, ids)

	tmpDir := t.TempDir()
	file := filepath.Join(tmpDir, "config.yaml")
	cfg := Config{
		Endpoints:     []string{"http://localhost:2379"},
		TestKeyPrefix: "/test/",
		ScenarioIDs:   []string{ids[0]},
	}
	require.NoError(t, cfg.SyncYAML(file))
	_, err := os.Stat(file)
	require.NoError(t, err)
}

func TestCreateRunnerReturnsNonNil(t *testing.T) {
	t.Parallel()

	cfg := &Config{
		Endpoints:     []string{"http://localhost:2379"},
		TestKeyPrefix: "/test/",
		ScenarioIDs:   []string{"GET_EMPTY_KEY"},
	}
	r := cfg.CreateRunner()
	assert.NotNil(t, r)
}

func TestGetScenarioTimeoutWatchWithCreatedNotification(t *testing.T) {
	t.Parallel()

	// WATCH_WITH_CREATED_NOTIFICATION starts with "WATCH_WITH"
	assert.Equal(t, scenarioTimeoutExtended, getScenarioTimeout("WATCH_WITH_CREATED_NOTIFICATION"))
}

func TestShouldRetryScenarioFalseForDefault(t *testing.T) {
	t.Parallel()

	assert.False(t, shouldRetryScenario("DELETE_WITH_RANGE"))
	assert.False(t, shouldRetryScenario("LEASE_GRANT_TTL_BOUNDS"))
}

func TestErrorConstants_Extra(t *testing.T) {
	t.Parallel()

	require.Error(t, errNoEndpoints)
	require.Error(t, errNoTestKeyPrefix)
	require.Error(t, errNoScenarios)
	require.Error(t, errUnknownScenario)
	assert.Contains(t, errNoEndpoints.Error(), "no endpoints")
	assert.Contains(t, errNoTestKeyPrefix.Error(), "no test key prefix")
	assert.Contains(t, errNoScenarios.Error(), "no scenarios")
	assert.Contains(t, errUnknownScenario.Error(), "unknown scenario")
}

func TestConfigConstants(t *testing.T) {
	t.Parallel()

	assert.Equal(t, 5*time.Minute, scenarioTimeoutDefault)
	assert.Equal(t, 10*time.Minute, scenarioTimeoutExtended)
	assert.Equal(t, 180*time.Second, runnerTimeoutDefault)
	assert.Equal(t, 2, scenarioRetryCount)
	assert.Equal(t, os.FileMode(0o750), os.FileMode(configDirPerm))
	assert.Equal(t, os.FileMode(0o600), os.FileMode(configFilePerm))
}

func TestRunnerDefaultTimeoutValue(t *testing.T) {
	t.Parallel()

	r := &runner{
		defaultTimeout: 99 * time.Second,
		results:        make([]scenarios.Result, 0),
	}
	assert.Equal(t, 99*time.Second, r.DefaultTimeout())
}

func TestRunnerNewCtxWithDefault(t *testing.T) {
	t.Parallel()

	r := &runner{
		defaultTimeout: 5 * time.Second,
		results:        make([]scenarios.Result, 0),
	}
	ctx, cancel := r.NewCtx()
	defer cancel()
	deadline, ok := ctx.Deadline()
	require.True(t, ok)
	assert.Positive(t, time.Until(deadline))
}

func TestRunnerNewCtxTimeoutZero(t *testing.T) {
	t.Parallel()

	r := &runner{
		defaultTimeout: 5 * time.Second,
		results:        make([]scenarios.Result, 0),
	}
	ctx, cancel := r.NewCtxTimeout(0)
	defer cancel()
	deadline, ok := ctx.Deadline()
	require.True(t, ok)
	// Should use default timeout
	assert.True(t, time.Until(deadline) > 0 && time.Until(deadline) <= 5*time.Second)
}

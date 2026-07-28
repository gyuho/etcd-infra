//nolint:all // Coverage-oriented tests intentionally use broad patterns for mock-heavy branch testing.
//nolint:testpackage // Need access to internals for thorough testing.
package stress

import (
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"git.tbd/etcd-infra/internal/etcd/stress/scenarios"
)

func TestStressRunnerNewCtxTimeoutZero(t *testing.T) {
	t.Parallel()

	r := &stressRunner{
		defaultTimeout: 42 * time.Second,
		results:        make([]scenarios.Result, 0),
		metrics:        scenarios.NewMetricsCollector(),
	}
	ctx, cancel := r.NewCtxTimeout(0)
	defer cancel()
	deadline, ok := ctx.Deadline()
	require.True(t, ok)
	// Should fall back to default
	assert.True(t, time.Until(deadline) > 0 && time.Until(deadline) <= 42*time.Second)
}

func TestStressRunnerResultsEmpty(t *testing.T) {
	t.Parallel()

	r := &stressRunner{
		results: make([]scenarios.Result, 0),
		metrics: scenarios.NewMetricsCollector(),
	}
	results := r.Results()
	assert.Empty(t, results)
}

func TestStressRunnerGenerateRandomKeyLength(t *testing.T) {
	t.Parallel()

	r := &stressRunner{
		cfg:     Config{TestKeyPrefix: "/stress/"},
		results: make([]scenarios.Result, 0),
		metrics: scenarios.NewMetricsCollector(),
	}

	// Generate multiple keys, ensure they differ
	key1 := r.GenerateRandomKey(16)
	key2 := r.GenerateRandomKey(16)
	assert.NotEqual(t, key1, key2, "random keys should differ")
	assert.Greater(t, len(key1), len("/stress/"))
}

func TestStressRunnerGetLoadGeneratorLazy(t *testing.T) {
	t.Parallel()

	r := &stressRunner{
		cfg:     Config{DurationSeconds: 30, RequestsPerSecond: 50},
		results: make([]scenarios.Result, 0),
		metrics: scenarios.NewMetricsCollector(),
	}

	// loadGen should be nil initially
	assert.Nil(t, r.loadGen)

	gen := r.GetLoadGenerator()
	assert.NotNil(t, gen)
	assert.NotNil(t, r.loadGen, "should be cached after first call")

	// Second call should return same instance
	gen2 := r.GetLoadGenerator()
	assert.Equal(t, gen, gen2)
}

func TestResolveScenarioIDsEmpty(t *testing.T) {
	t.Parallel()

	ids := resolveScenarioIDs("")
	assert.Positive(t, len(ids))
	// IDs should be sorted
	for i := 1; i < len(ids); i++ {
		assert.LessOrEqual(t, ids[i-1], ids[i], "IDs should be sorted")
	}
}

func TestResolveScenarioIDsWhitespaceOnly(t *testing.T) {
	t.Parallel()

	ids := resolveScenarioIDs("  \t  ")
	assert.Positive(t, len(ids))
}

func TestNormalizeEndpointsStressSingleEmpty(t *testing.T) {
	t.Parallel()

	result := normalizeEndpoints([]string{""})
	assert.Equal(t, []string{DefaultEndpoints}, result)
}

func TestNormalizeEndpointsStressMultipleValid(t *testing.T) {
	t.Parallel()

	result := normalizeEndpoints([]string{"http://a:2379", "http://b:2379", "http://c:2379"})
	assert.Equal(t, []string{"http://a:2379", "http://b:2379", "http://c:2379"}, result)
}

func TestCountResultsMixed(t *testing.T) {
	t.Parallel()

	results := []scenarios.Result{
		{Scenario: "a", Success: true},
		{Scenario: "b", Success: false},
		{Scenario: "c", Success: true},
		{Scenario: "d", Success: false},
		{Scenario: "e", Success: true},
	}
	passed, failed := countResults(results)
	assert.Equal(t, 3, passed)
	assert.Equal(t, 2, failed)
}

func TestCreateRunnerReturnsStressRunner(t *testing.T) {
	t.Parallel()

	cfg := &Config{
		Endpoints:     []string{"http://localhost:2379"},
		TestKeyPrefix: "/test/",
	}
	r := cfg.CreateRunner()
	assert.NotNil(t, r)
}

func TestStressRunnerGetConfigDefaults(t *testing.T) {
	t.Parallel()

	r := &stressRunner{
		cfg:     Config{},
		results: make([]scenarios.Result, 0),
		metrics: scenarios.NewMetricsCollector(),
	}
	stressCfg := r.GetConfig()
	assert.Equal(t, 0, stressCfg.DurationSeconds)
	assert.Equal(t, 0, stressCfg.ConcurrentWorkers)
}

func TestErrorConstantsStress(t *testing.T) {
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

func TestConfigConstantsStress(t *testing.T) {
	t.Parallel()

	assert.Equal(t, 10*time.Second, runnerTimeoutDefault)
	assert.Equal(t, os.FileMode(0o750), os.FileMode(configDirPerm))
	assert.Equal(t, os.FileMode(0o600), os.FileMode(configFilePerm))
}

func TestNormalizeTestKeyPrefixCustom(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "/my-prefix/", normalizeTestKeyPrefix("/my-prefix/"))
	assert.Equal(t, "custom", normalizeTestKeyPrefix(" custom "))
}

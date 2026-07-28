//nolint:testpackage // Need access to internals for thorough testing.
package conformance

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"git.tbd/etcd-infra/internal/etcd/conformance/scenarios"
)

func TestNormalizeEndpointsKeepsValid(t *testing.T) {
	t.Parallel()

	result := normalizeEndpoints([]string{"http://a:2379", "http://b:2379"})
	assert.Equal(t, []string{"http://a:2379", "http://b:2379"}, result)
}

func TestNormalizeEndpointsSkipsEmpty(t *testing.T) {
	t.Parallel()

	result := normalizeEndpoints([]string{"http://a:2379", "", "http://b:2379"})
	assert.Equal(t, []string{"http://a:2379", "http://b:2379"}, result)
}

func TestNormalizeEndpointsNilSlice(t *testing.T) {
	t.Parallel()

	result := normalizeEndpoints(nil)
	assert.Equal(t, []string{DefaultEndpoints}, result)
}

func TestNormalizeTestKeyPrefixNonEmpty(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "/custom/", normalizeTestKeyPrefix("/custom/"))
}

func TestNormalizeTestKeyPrefixEmpty(t *testing.T) {
	t.Parallel()

	assert.Equal(t, DefaultTestKeyPrefix, normalizeTestKeyPrefix(""))
}

func TestResolveScenarioIDsAll(t *testing.T) {
	t.Parallel()

	ids := resolveScenarioIDs("")
	assert.Greater(t, len(ids), 1)
	// Should be sorted
	for i := 1; i < len(ids); i++ {
		assert.LessOrEqual(t, ids[i-1], ids[i])
	}
}

func TestCountResultsMixed(t *testing.T) {
	t.Parallel()

	results := []scenarios.Result{
		{Success: true},
		{Success: false},
		{Success: true},
		{Success: false},
		{Success: false},
	}
	passed, failed := countResults(results)
	assert.Equal(t, 2, passed)
	assert.Equal(t, 3, failed)
}

func TestRunnerNewCtxTimeoutUsesDefaultForNegative(t *testing.T) {
	t.Parallel()

	r := &runner{
		defaultTimeout: 7 * time.Second,
		results:        make([]scenarios.Result, 0),
	}
	ctx, cancel := r.NewCtxTimeout(-5 * time.Second)
	defer cancel()
	deadline, ok := ctx.Deadline()
	require.True(t, ok)
	assert.LessOrEqual(t, time.Until(deadline), 7*time.Second)
	assert.Positive(t, time.Until(deadline))
}

func TestRunScenarioWithTimeoutRecordsResult(t *testing.T) {
	t.Parallel()

	r := &runner{
		cfg:            Config{Endpoints: []string{"http://localhost:2379"}, TestKeyPrefix: "/test/"},
		defaultTimeout: 5 * time.Second,
		results:        make([]scenarios.Result, 0),
	}
	var executed bool
	err := runScenarioWithTimeout(r, func(runner scenarios.Runner) {
		executed = true
		runner.RecordResult(scenarios.Result{Scenario: "test", Success: true})
	}, "TEST", 2*time.Second)
	require.NoError(t, err)
	assert.True(t, executed)
	assert.Len(t, r.Results(), 1)
}

func TestGetScenarioTimeoutAllCategories(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		scenario string
		expected time.Duration
	}{
		{"default_compact", "COMPACT", scenarioTimeoutDefault},
		{"default_get_empty", "GET_EMPTY_KEY", scenarioTimeoutDefault},
		{"mirror_updates", "MIRROR_SYNC_UPDATES", scenarioTimeoutExtended},
		{"contending_generic", "FOO_CONTENDING_BAR", scenarioTimeoutExtended},
		{"watch_with_filter", "WATCH_WITH_FILTER", scenarioTimeoutExtended},
		{"maintenance_hash", "MAINTENANCE_HASH_KV", scenarioTimeoutExtended},
		{"concurrent_lease", "LEASING_PUT_AND_GET_AND_DELETE_CONCURRENT", scenarioTimeoutExtended},
		{"with_prefix_put", "PUT_AND_GET_WITH_PREFIX", scenarioTimeoutExtended},
		{"from_key_get", "GET_WITH_FROM_KEY", scenarioTimeoutExtended},
		{"tls", scenarioTLSClientAuth, scenarioTimeoutExtended},
		{"resource", scenarioResSizeEst, scenarioTimeoutExtended},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.expected, getScenarioTimeout(tt.scenario))
		})
	}
}

func TestShouldRetryScenarioAllCases(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		scenario string
		expected bool
	}{
		{"watch_with_filter", "WATCH_WITH_FILTER", true},
		{"leasing_prefix", "LEASING_PUT_AND_DELETE_WITH_PREFIX", true},
		{"delete_from_key", "DELETE_ALL_WITH_FROM_KEY", true},
		{"tls", "TLS_CLIENT_AUTH", true},
		{"resource", "RESOURCE_SIZE_ESTIMATION", true},
		{"put_latest", "PUT_AND_GET_WITH_LATEST_REVISION", false},
		{"delete_range", "DELETE_WITH_RANGE", false},
		{"watch_cancel_inflight", "WATCH_CANCEL_INFLIGHT", false},
		{"txn_nested", "TXN_NESTED", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.expected, shouldRetryScenario(tt.scenario))
		})
	}
}

func TestCreateRunnerDefaultTimeout(t *testing.T) {
	t.Parallel()

	cfg := &Config{
		Endpoints:     []string{"http://localhost:2379"},
		TestKeyPrefix: "/prefix/",
		ScenarioIDs:   []string{"GET_EMPTY_KEY"},
	}
	r := cfg.CreateRunner()
	require.NotNil(t, r)
	assert.Positive(t, r.DefaultTimeout())
}

func TestRunnerConcurrentRecordResult(t *testing.T) {
	t.Parallel()

	r := &runner{
		defaultTimeout: 10 * time.Second,
		results:        make([]scenarios.Result, 0),
	}

	// Concurrent writes to test mutex safety
	done := make(chan struct{}, 10)
	for i := range 10 {
		go func(idx int) {
			r.RecordResult(scenarios.Result{Scenario: "s" + string(rune('0'+idx)), Success: idx%2 == 0})
			done <- struct{}{}
		}(i)
	}
	for range 10 {
		<-done
	}

	results := r.Results()
	assert.Len(t, results, 10)
}

func TestRunnerGenerateRandomKeyUnique(t *testing.T) {
	t.Parallel()

	r := &runner{
		cfg:            Config{TestKeyPrefix: "/test-prefix/"},
		defaultTimeout: 10 * time.Second,
		results:        make([]scenarios.Result, 0),
	}

	keys := make(map[string]bool)
	for range 100 {
		key := r.GenerateRandomKey(12)
		assert.NotEmpty(t, key)
		assert.Contains(t, key, "/test-prefix/")
		keys[key] = true
	}
	// All should be unique
	assert.Len(t, keys, 100)
}

func TestScenarioConstantNames(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "TLS_CLIENT_AUTH", scenarioTLSClientAuth)
	assert.Equal(t, "RESOURCE_SIZE_ESTIMATION", scenarioResSizeEst)
}

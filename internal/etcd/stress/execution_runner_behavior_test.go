//nolint:testpackage // Need access to internals for thorough testing.
package stress

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"git.tbd/etcd-infra/internal/etcd/stress/scenarios"
)

func TestStressRunnerDefaultTimeout(t *testing.T) {
	t.Parallel()

	r := &stressRunner{
		defaultTimeout: 42 * time.Second,
		results:        make([]scenarios.Result, 0),
		metrics:        scenarios.NewMetricsCollector(),
	}
	ctx, cancel := r.NewCtx()
	defer cancel()
	deadline, ok := ctx.Deadline()
	require.True(t, ok)
	assert.True(t, time.Until(deadline) > 0 && time.Until(deadline) <= 42*time.Second)
}

func TestStressRunnerNewCtxTimeoutPositive(t *testing.T) {
	t.Parallel()

	r := &stressRunner{
		defaultTimeout: 42 * time.Second,
		results:        make([]scenarios.Result, 0),
		metrics:        scenarios.NewMetricsCollector(),
	}
	ctx, cancel := r.NewCtxTimeout(3 * time.Second)
	defer cancel()
	deadline, ok := ctx.Deadline()
	require.True(t, ok)
	assert.True(t, time.Until(deadline) > 0 && time.Until(deadline) <= 3*time.Second)
}

func TestStressRunnerNewCtxTimeoutNegative(t *testing.T) {
	t.Parallel()

	r := &stressRunner{
		defaultTimeout: 42 * time.Second,
		results:        make([]scenarios.Result, 0),
		metrics:        scenarios.NewMetricsCollector(),
	}
	ctx, cancel := r.NewCtxTimeout(-5 * time.Second)
	defer cancel()
	deadline, ok := ctx.Deadline()
	require.True(t, ok)
	assert.True(t, time.Until(deadline) > 0 && time.Until(deadline) <= 42*time.Second)
}

func TestStressRunnerGenerateRandomKey_Coverage(t *testing.T) {
	t.Parallel()

	r := &stressRunner{
		cfg:     Config{TestKeyPrefix: "/stress/"},
		results: make([]scenarios.Result, 0),
		metrics: scenarios.NewMetricsCollector(),
	}
	key := r.GenerateRandomKey(8)
	assert.Contains(t, key, "/stress/")
	assert.Greater(t, len(key), len("/stress/"))
}

func TestStressRunnerRecordResult(t *testing.T) {
	t.Parallel()

	r := &stressRunner{
		results: make([]scenarios.Result, 0),
		metrics: scenarios.NewMetricsCollector(),
	}
	r.RecordResult(scenarios.Result{Scenario: "s1", Success: true})
	r.RecordResult(scenarios.Result{Scenario: "s2", Success: false})
	results := r.Results()
	assert.Len(t, results, 2)
	assert.Equal(t, "s1", results[0].Scenario)
}

func TestStressRunnerGetMetricsCollector(t *testing.T) {
	t.Parallel()

	mc := scenarios.NewMetricsCollector()
	r := &stressRunner{
		results: make([]scenarios.Result, 0),
		metrics: mc,
	}
	assert.Equal(t, mc, r.GetMetricsCollector())
}

func TestStressRunnerGetLoadGenerator(t *testing.T) {
	t.Parallel()

	r := &stressRunner{
		cfg:     Config{DurationSeconds: 10, RequestsPerSecond: 100},
		results: make([]scenarios.Result, 0),
		metrics: scenarios.NewMetricsCollector(),
	}
	gen := r.GetLoadGenerator()
	assert.NotNil(t, gen)
	// Call again to verify caching
	gen2 := r.GetLoadGenerator()
	assert.Equal(t, gen, gen2)
}

func TestStressRunnerGetConfig(t *testing.T) {
	t.Parallel()

	r := &stressRunner{
		cfg: Config{
			DurationSeconds:   30,
			ConcurrentWorkers: 5,
			RequestsPerSecond: 100,
			KeySizeBytes:      32,
			ValueSizeBytes:    128,
		},
		results: make([]scenarios.Result, 0),
		metrics: scenarios.NewMetricsCollector(),
	}
	cfg := r.GetConfig()
	assert.Equal(t, 30, cfg.DurationSeconds)
	assert.Equal(t, 5, cfg.ConcurrentWorkers)
	assert.Equal(t, 100, cfg.RequestsPerSecond)
	assert.Equal(t, 32, cfg.KeySizeBytes)
	assert.Equal(t, 128, cfg.ValueSizeBytes)
}

func TestDetermineRunnerTimeoutStressDefault(t *testing.T) {
	t.Parallel()

	timeout := determineRunnerTimeout("")
	assert.Positive(t, timeout)
}

func TestNormalizeEndpointsStress(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    []string
		expected []string
	}{
		{"nil", nil, []string{DefaultEndpoints}},
		{"empty", []string{}, []string{DefaultEndpoints}},
		{"all whitespace", []string{"  ", ""}, []string{DefaultEndpoints}},
		{"valid", []string{"http://a:2379"}, []string{"http://a:2379"}},
		{"with spaces", []string{" http://a:2379 "}, []string{"http://a:2379"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := normalizeEndpoints(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestNormalizeTestKeyPrefixStress(t *testing.T) {
	t.Parallel()

	assert.Equal(t, DefaultTestKeyPrefix, normalizeTestKeyPrefix(""))
	assert.Equal(t, DefaultTestKeyPrefix, normalizeTestKeyPrefix("  "))
	assert.Equal(t, "/custom/", normalizeTestKeyPrefix("/custom/"))
}

func TestCountResultsStress(t *testing.T) {
	t.Parallel()

	results := []scenarios.Result{
		{Success: true},
		{Success: false},
		{Success: true},
	}
	passed, failed := countResults(results)
	assert.Equal(t, 2, passed)
	assert.Equal(t, 1, failed)
}

func TestCountResultsEmpty(t *testing.T) {
	t.Parallel()

	passed, failed := countResults(nil)
	assert.Equal(t, 0, passed)
	assert.Equal(t, 0, failed)
}

func TestDefaultStressConstants(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "https://127.0.0.1:2379", DefaultEndpoints)
	assert.Equal(t, "/etcd-infra-stress/", DefaultTestKeyPrefix)
}

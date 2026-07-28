//nolint:testpackage // Need access to internals for thorough testing.
package conformance

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"git.tbd/etcd-infra/internal/etcd/conformance/scenarios"
)

func TestGetScenarioTimeoutDefault(t *testing.T) {
	t.Parallel()

	assert.Equal(t, scenarioTimeoutDefault, getScenarioTimeout("PUT_EMPTY_KEY_SHOULD_ERROR"))
	assert.Equal(t, scenarioTimeoutDefault, getScenarioTimeout("TXN_PUT_ONE"))
	assert.Equal(t, scenarioTimeoutDefault, getScenarioTimeout("COMPACT"))
	assert.Equal(t, scenarioTimeoutDefault, getScenarioTimeout("GET_EMPTY_KEY"))
}

func TestGetScenarioTimeoutExtended(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		scenario string
	}{
		{"mirror sync base", "MIRROR_SYNC_BASE"},
		{"mirror sync updates", "MIRROR_SYNC_UPDATES"},
		{"contending txn", "LEASING_PUT_AND_DELETE_RANGE_WITH_CONTENDING_TXN"},
		{"contending delete", "LEASING_PUT_AND_DELETE_RANGE_WITH_CONTENDING_DELETE"},
		{"watch with prefix", "WATCH_WITH_PREFIX"},
		{"watch with range", "WATCH_WITH_RANGE"},
		{"maintenance status", "MAINTENANCE_STATUS"},
		{"maintenance snapshot", "MAINTENANCE_SNAPSHOT"},
		{"concurrent lease ops", "LEASING_PUT_AND_GET_AND_DELETE_CONCURRENT"},
		{"with prefix", "PUT_AND_GET_WITH_PREFIX"},
		{"from key", "PUT_AND_GET_WITH_FROM_KEY"},
		{"tls client auth", "TLS_CLIENT_AUTH"},
		{"resource size est", "RESOURCE_SIZE_ESTIMATION"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, scenarioTimeoutExtended, getScenarioTimeout(tt.scenario))
		})
	}
}

func TestShouldRetryScenario(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		scenario string
		expected bool
	}{
		{"watch with prefix", "WATCH_WITH_PREFIX", true},
		{"watch with range", "WATCH_WITH_RANGE", true},
		{"with prefix leasing", "LEASING_PUT_AND_GET_WITH_PREFIX", true},
		{"from key", "PUT_AND_GET_WITH_FROM_KEY", true},
		{"tls client auth", "TLS_CLIENT_AUTH", true},
		{"resource size est", "RESOURCE_SIZE_ESTIMATION", true},
		{"regular put", "PUT_EMPTY_KEY_SHOULD_ERROR", false},
		{"regular txn", "TXN_PUT_ONE", false},
		{"compact", "COMPACT", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.expected, shouldRetryScenario(tt.scenario))
		})
	}
}

func TestRunScenarioWithTimeoutSuccess(t *testing.T) {
	t.Parallel()

	called := false
	mockRunner := &runner{
		cfg:            Config{Endpoints: []string{"http://localhost:2379"}, TestKeyPrefix: "/test/"},
		defaultTimeout: 5 * time.Second,
		results:        make([]scenarios.Result, 0),
	}
	err := runScenarioWithTimeout(mockRunner, func(_ scenarios.Runner) {
		called = true
	}, "TEST_SCENARIO", 5*time.Second)
	require.NoError(t, err)
	assert.True(t, called)
}

func TestRunScenarioWithTimeoutPanic(t *testing.T) {
	t.Parallel()

	mockRunner := &runner{
		cfg:            Config{Endpoints: []string{"http://localhost:2379"}, TestKeyPrefix: "/test/"},
		defaultTimeout: 5 * time.Second,
		results:        make([]scenarios.Result, 0),
	}
	err := runScenarioWithTimeout(mockRunner, func(_ scenarios.Runner) {
		panic("test panic")
	}, "PANIC_SCENARIO", 5*time.Second)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "panicked")
	assert.Contains(t, err.Error(), "test panic")
}

func TestRunScenarioWithTimeoutTimesOut(t *testing.T) {
	t.Parallel()

	mockRunner := &runner{
		cfg:            Config{Endpoints: []string{"http://localhost:2379"}, TestKeyPrefix: "/test/"},
		defaultTimeout: 5 * time.Second,
		results:        make([]scenarios.Result, 0),
	}
	err := runScenarioWithTimeout(mockRunner, func(_ scenarios.Runner) {
		time.Sleep(200 * time.Millisecond) // Exceeds the 50ms timeout
	}, "SLOW_SCENARIO", 50*time.Millisecond)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "timed out")
}

func TestRunnerDefaultTimeout(t *testing.T) {
	t.Parallel()

	r := &runner{
		defaultTimeout: 42 * time.Second,
		results:        make([]scenarios.Result, 0),
	}
	assert.Equal(t, 42*time.Second, r.DefaultTimeout())
}

func TestRunnerNewCtx(t *testing.T) {
	t.Parallel()

	r := &runner{
		defaultTimeout: 10 * time.Second,
		results:        make([]scenarios.Result, 0),
	}
	ctx, cancel := r.NewCtx()
	defer cancel()
	deadline, ok := ctx.Deadline()
	require.True(t, ok, "should have a deadline")
	assert.True(t, time.Until(deadline) > 0 && time.Until(deadline) <= 10*time.Second)
}

func TestRunnerNewCtxTimeoutPositive(t *testing.T) {
	t.Parallel()

	r := &runner{
		defaultTimeout: 10 * time.Second,
		results:        make([]scenarios.Result, 0),
	}
	ctx, cancel := r.NewCtxTimeout(3 * time.Second)
	defer cancel()
	deadline, ok := ctx.Deadline()
	require.True(t, ok)
	assert.True(t, time.Until(deadline) > 0 && time.Until(deadline) <= 3*time.Second)
}

func TestRunnerNewCtxTimeoutNegative(t *testing.T) {
	t.Parallel()

	r := &runner{
		defaultTimeout: 10 * time.Second,
		results:        make([]scenarios.Result, 0),
	}
	ctx, cancel := r.NewCtxTimeout(-1 * time.Second)
	defer cancel()
	deadline, ok := ctx.Deadline()
	require.True(t, ok)
	// Should fall back to default timeout
	assert.True(t, time.Until(deadline) > 0 && time.Until(deadline) <= 10*time.Second)
}

func TestRunnerRecordResult(t *testing.T) {
	t.Parallel()

	r := &runner{
		defaultTimeout: 10 * time.Second,
		results:        make([]scenarios.Result, 0),
	}
	r.RecordResult(scenarios.Result{Scenario: "s1", Success: true})
	r.RecordResult(scenarios.Result{Scenario: "s2", Success: false})

	results := r.Results()
	assert.Len(t, results, 2)
	assert.Equal(t, "s1", results[0].Scenario)
	assert.Equal(t, "s2", results[1].Scenario)
}

func TestRunnerGenerateRandomKey_Coverage(t *testing.T) {
	t.Parallel()

	r := &runner{
		cfg:            Config{TestKeyPrefix: "/test/"},
		defaultTimeout: 10 * time.Second,
		results:        make([]scenarios.Result, 0),
	}
	key := r.GenerateRandomKey(8)
	assert.Contains(t, key, "/test/")
	assert.Greater(t, len(key), len("/test/"))
}

func TestDetermineRunnerTimeoutDefault(t *testing.T) {
	t.Parallel()

	// With no env or flags, should return default
	timeout := determineRunnerTimeout("")
	assert.Positive(t, timeout, "timeout should be positive")
}

func TestScenarioRetryCount(t *testing.T) {
	t.Parallel()

	assert.Equal(t, 2, scenarioRetryCount)
}

func TestDefaultConstants(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "https://127.0.0.1:2379", DefaultEndpoints)
	assert.Equal(t, "/etcd-infra-conformance/", DefaultTestKeyPrefix)
}

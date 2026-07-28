//nolint:all // Coverage-oriented tests for uncovered branches.
package scenarios

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// failingClientRunner is a StressRunner that always returns error from NewClient.
type failingClientRunner struct {
	results []Result
}

func (r *failingClientRunner) RecordResult(rs Result) { r.results = append(r.results, rs) }
func (r *failingClientRunner) Results() StressResults { return r.results }
func (r *failingClientRunner) Cleanup() error         { return nil }
func (r *failingClientRunner) NewCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), time.Second)
}

func (r *failingClientRunner) NewCtxTimeout(time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), time.Second)
}
func (r *failingClientRunner) GenerateRandomKey(int) string { return "test-key" }
func (r *failingClientRunner) NewClient(...StressOpOption) (*clientv3.Client, error) {
	return nil, errors.New("mock client error")
}

func (r *failingClientRunner) NewPerPeerClients(...StressOpOption) ([]*clientv3.Client, error) {
	return nil, errors.New("mock peer client error")
}
func (r *failingClientRunner) GetMetricsCollector() *MetricsCollector { return NewMetricsCollector() }
func (r *failingClientRunner) GetLoadGenerator() LoadGenerator        { return NewLoadGenerator(1, 10) }
func (r *failingClientRunner) GetConfig() StressConfig {
	return StressConfig{DurationSeconds: 1, ConcurrentWorkers: 1, RequestsPerSecond: 5, ValueSizeBytes: 16, KeySizeBytes: 8}
}

// brokenEndpointRunner returns a client connected to a dead endpoint (no etcd).
// All operations fail with connection errors, exercising all error-handling branches.
type brokenEndpointRunner struct {
	results []Result
	metrics *MetricsCollector
}

func (r *brokenEndpointRunner) RecordResult(rs Result)       { r.results = append(r.results, rs) }
func (r *brokenEndpointRunner) Results() StressResults       { return r.results }
func (r *brokenEndpointRunner) Cleanup() error               { return nil }
func (r *brokenEndpointRunner) GenerateRandomKey(int) string { return "broken-key" }

func (r *brokenEndpointRunner) NewCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 100*time.Millisecond)
}

func (r *brokenEndpointRunner) NewCtxTimeout(time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 10*time.Millisecond)
}

func (r *brokenEndpointRunner) NewClient(opts ...StressOpOption) (*clientv3.Client, error) {
	return clientv3.New(clientv3.Config{
		Endpoints:   []string{"127.0.0.1:1"},
		DialTimeout: 50 * time.Millisecond,
	})
}

func (r *brokenEndpointRunner) NewPerPeerClients(opts ...StressOpOption) ([]*clientv3.Client, error) {
	cli, err := r.NewClient(opts...)
	if err != nil {
		return nil, err
	}
	return []*clientv3.Client{cli}, nil
}

func (r *brokenEndpointRunner) GetMetricsCollector() *MetricsCollector { return r.metrics }
func (r *brokenEndpointRunner) GetLoadGenerator() LoadGenerator        { return NewLoadGenerator(1, 50) }
func (r *brokenEndpointRunner) GetConfig() StressConfig {
	return StressConfig{DurationSeconds: 1, ConcurrentWorkers: 1, RequestsPerSecond: 50, ValueSizeBytes: 16, KeySizeBytes: 8}
}

func TestAllScenariosNewClientError(t *testing.T) {
	t.Parallel()

	runner := &failingClientRunner{}

	for name, fn := range StressIDStringToRunnerFunc {
		t.Run(name, func(t *testing.T) {
			fn(runner)
		})
	}

	// Also run scenarios not in the map
	extraFuncs := []struct {
		name string
		fn   func(StressRunner)
	}{
		{"COMPACT_DURING_LOAD", RunCompactDuringLoad},
		{"LEADER_ELECTION_CONTENTION", RunLeaderElectionContention},
		{"LEASE_INTENSIVE_WORKLOAD", RunLeaseIntensiveWorkload},
		{"LIST_PAGINATION_HEAVY", RunListPaginationHeavy},
		{"NAMESPACE_ISOLATION_HEAVY", RunNamespaceIsolationHeavy},
		{"OPTIMISTIC_CONCURRENCY_TXN", RunOptimisticConcurrencyTxn},
		{"TXN_MULTI_KEY_UPDATE", RunTxnMultiKeyUpdate},
		{"WATCH_BOOKMARK_HEAVY", RunWatchBookmarkHeavy},
		{"WATCH_MANY_PREFIXES", RunWatchManyPrefixes},
		{"WATCH_WITH_CHURN", RunWatchWithChurn},
	}
	for _, s := range extraFuncs {
		t.Run(s.name+"_failing", func(t *testing.T) {
			s.fn(runner)
		})
	}

	// All results should have Success=false
	for _, r := range runner.Results() {
		assert.False(t, r.Success, "scenario %s should fail with mock client error", r.Scenario)
		assert.Contains(t, r.Output, "failed to create client")
	}
}

func TestScenariosBrokenEndpointErrors(t *testing.T) {
	t.Parallel()

	// All non-watch scenarios with broken endpoint (watch scenarios hang on grpc stream retries)
	fastScenarios := []struct {
		name string
		fn   func(StressRunner)
	}{
		{"CONCURRENT_PUTS", RunConcurrentPuts},
		{"BURST_WRITES", RunBurstWrites},
		{"SUSTAINED_LOAD", RunSustainedLoad},
		{"RAMP_UP_LOAD", RunRampUpLoad},
		{"LARGE_VALUES", RunLargeValues},
		{"SEQUENTIAL_WRITES", RunSequentialWrites},
		{"RANDOM_READS", RunRandomReads},
		{"HIGH_CONTENTION", RunHighContention},
		{"MIXED_WORKLOAD", RunMixedWorkload},
		{"MANY_KEYS", RunManyKeys},
		{"COMPACT_DURING_LOAD", RunCompactDuringLoad},
		{"LIST_PAGINATION_HEAVY", RunListPaginationHeavy},
		{"OPTIMISTIC_CONCURRENCY_TXN", RunOptimisticConcurrencyTxn},
		{"TXN_MULTI_KEY_UPDATE", RunTxnMultiKeyUpdate},
		{"NAMESPACE_ISOLATION_HEAVY", RunNamespaceIsolationHeavy},
		{"LEASE_INTENSIVE_WORKLOAD", RunLeaseIntensiveWorkload},
		{"LEADER_ELECTION_CONTENTION", RunLeaderElectionContention},
	}

	for _, s := range fastScenarios {
		t.Run(s.name, func(t *testing.T) {
			t.Parallel()
			runner := &brokenEndpointRunner{metrics: NewMetricsCollector()}
			s.fn(runner)
			require.Len(t, runner.results, 1)
			// With a broken endpoint, scenario should report failure (success rate too low, etc.)
			assert.False(t, runner.results[0].Success, "scenario %s should fail with broken endpoint", s.name)
		})
	}
}

func TestStressIDStringOutOfRange(t *testing.T) {
	t.Parallel()

	s := StressID(-1).String()
	assert.Contains(t, s, "StressID(-1)")

	s2 := StressID(9999).String()
	assert.Contains(t, s2, "StressID(9999)")
}

func TestRecoverWorkerPanic(t *testing.T) {
	t.Parallel()

	ch := make(chan error, 1)
	// Launch a goroutine that panics with deferred recoverWorker
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer recoverWorker(ch, 42)
		panic("boom")
	}()
	<-done

	require.Len(t, ch, 1)
	err := <-ch
	assert.Contains(t, err.Error(), "worker 42 panic: boom")
}

func TestRunWorkersPanicRecovery(t *testing.T) {
	t.Parallel()

	errs := runWorkers(2, func(workerID int, errCh chan<- error) {
		if workerID == 0 {
			panic("worker panic")
		}
	})
	// One worker panics, one doesn't
	require.Len(t, errs, 1)
	assert.Contains(t, errs[0].Error(), "worker 0 panic")
}

func TestSyncJSONMkdirAllError(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	// Make a read-only directory, then request a subdirectory path under it
	readOnly := filepath.Join(tmpDir, "readonly")
	require.NoError(t, os.Mkdir(readOnly, 0o555))
	t.Cleanup(func() { _ = os.Chmod(readOnly, 0o755) })
	// Parent "readonly/sub" doesn't exist → os.IsNotExist, MkdirAll fails (permission denied)
	file := filepath.Join(readOnly, "sub", "results.json")

	rs := StressResults{{Scenario: "test"}}
	err := rs.SyncJSON(file)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to create directory")
}

func TestSyncYAMLMkdirAllError(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	readOnly := filepath.Join(tmpDir, "readonly")
	require.NoError(t, os.Mkdir(readOnly, 0o555))
	t.Cleanup(func() { _ = os.Chmod(readOnly, 0o755) })
	file := filepath.Join(readOnly, "sub", "results.yaml")

	rs := StressResults{{Scenario: "test"}}
	err := rs.SyncYAML(file)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to create directory")
}

func TestSyncJSONWriteFileError(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	// Create a directory with the same name as the output file
	file := filepath.Join(tmpDir, "results.json")
	require.NoError(t, os.Mkdir(file, 0o755))

	rs := StressResults{{Scenario: "test"}}
	err := rs.SyncJSON(file)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to write JSON file")
}

func TestSyncYAMLWriteFileError(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	file := filepath.Join(tmpDir, "results.yaml")
	require.NoError(t, os.Mkdir(file, 0o755))

	rs := StressResults{{Scenario: "test"}}
	err := rs.SyncYAML(file)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to write YAML file")
}

func TestMetricsResetZeroCapLatencies(t *testing.T) {
	t.Parallel()

	m := &MetricsCollector{
		errors: make(map[string]int),
	}
	// latencies is nil (cap 0), triggers the make branch in Reset
	m.Reset()
	assert.NotNil(t, m.latencies)
	assert.Equal(t, 0, len(m.latencies))
}

func TestPercentileEdgeCases(t *testing.T) {
	t.Parallel()

	sorted := []float64{1, 2, 3, 4, 5}

	// p <= 0 returns first element
	assert.Equal(t, 1.0, percentile(sorted, 0))
	assert.Equal(t, 1.0, percentile(sorted, -1))

	// p >= 1 returns last element
	assert.Equal(t, 5.0, percentile(sorted, 1.0))
	assert.Equal(t, 5.0, percentile(sorted, 2.0))

	// empty slice
	assert.Equal(t, 0.0, percentile(nil, 0.5))

	// single element
	assert.Equal(t, 42.0, percentile([]float64{42}, 0.99))
}

func TestStatisticsSuccessRateZero(t *testing.T) {
	t.Parallel()

	s := Statistics{TotalRequests: 0}
	assert.Equal(t, 0.0, s.SuccessRate())
}

func TestComputePerWorkerIntervalBothNegative(t *testing.T) {
	t.Parallel()

	assert.Equal(t, time.Duration(0), computePerWorkerInterval(-5, -3))
}

func TestSleepUntilNormalInterval(t *testing.T) {
	t.Parallel()

	start := time.Now()
	sleepUntil(time.Now().Add(time.Second), 10*time.Millisecond)
	elapsed := time.Since(start)
	assert.True(t, elapsed >= 5*time.Millisecond && elapsed < 100*time.Millisecond)
}

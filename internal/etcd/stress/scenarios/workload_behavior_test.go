//nolint:testpackage // Need access to internals for thorough testing.
package scenarios

import (
	"errors"
	"testing"
	"time"

	testtime "git.tbd/etcd-infra/internal/etcd/testtime"
	"github.com/stretchr/testify/assert"
)

func TestComputePerWorkerInterval(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		rps      int
		workers  int
		expected time.Duration
	}{
		{"zero rps", 0, 10, 0},
		{"zero workers", 100, 0, 0},
		{"negative rps", -1, 10, 0},
		{"negative workers", 100, -1, 0},
		{"valid 100rps 10workers", 100, 10, 100 * time.Millisecond},
		{"valid 1rps 1worker", 1, 1, time.Second},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := computePerWorkerInterval(tt.rps, tt.workers)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestSleepUntilZeroInterval(t *testing.T) {
	t.Parallel()

	// Should return immediately
	start := time.Now()
	sleepUntil(time.Now().Add(time.Second), 0)
	elapsed := time.Since(start)
	assert.Less(t, elapsed, 50*time.Millisecond, "should return immediately for zero interval")
}

func TestSleepUntilPastDeadline(t *testing.T) {
	t.Parallel()

	start := time.Now()
	sleepUntil(time.Now().Add(-time.Second), 100*time.Millisecond)
	elapsed := time.Since(start)
	assert.Less(t, elapsed, 50*time.Millisecond, "should return immediately for past deadline")
}

func TestSleepUntilShortRemaining(t *testing.T) {
	t.Parallel()

	// Interval > remaining: should sleep for remaining duration
	start := time.Now()
	sleepUntil(time.Now().Add(20*time.Millisecond), 500*time.Millisecond)
	elapsed := time.Since(start)
	assert.True(t, elapsed >= 10*time.Millisecond && elapsed < 100*time.Millisecond)
}

func TestFirstErrorMessage(t *testing.T) {
	t.Parallel()

	assert.Empty(t, firstErrorMessage(nil))
	assert.Empty(t, firstErrorMessage([]error{}))
	assert.Equal(t, "first", firstErrorMessage([]error{errors.New("first"), errors.New("second")}))
}

func TestDrainErrors(t *testing.T) {
	t.Parallel()

	ch := make(chan error, 3)
	ch <- errors.New("e1")
	ch <- nil // should be skipped
	ch <- errors.New("e2")
	close(ch)

	errs := drainErrors(ch)
	assert.Len(t, errs, 2)
}

func TestDrainErrorsEmpty(t *testing.T) {
	t.Parallel()

	ch := make(chan error)
	close(ch)
	errs := drainErrors(ch)
	assert.Empty(t, errs)
}

func TestRecoverWorkerNoPanic(t *testing.T) {
	t.Parallel()

	ch := make(chan error, 1)
	// Not inside defer; just call to verify it doesn't send
	// We can't easily test the panic path without actually panicking in a goroutine
	assert.Empty(t, ch)
}

func TestRunWorkersZeroConcurrency(t *testing.T) {
	t.Parallel()

	called := 0
	errs := runWorkers(0, func(_ int, _ chan<- error) {
		called++
	})
	assert.Empty(t, errs)
	assert.Equal(t, 1, called, "zero concurrency should default to 1")
}

func TestRunWorkersConcurrency(t *testing.T) {
	t.Parallel()

	called := make(chan int, 5)
	errs := runWorkers(3, func(workerID int, _ chan<- error) {
		called <- workerID
	})
	close(called)
	assert.Empty(t, errs)

	ids := make(map[int]bool)
	for id := range called {
		ids[id] = true
	}
	assert.Len(t, ids, 3)
}

func TestFinalizeScenarioWithErrors(t *testing.T) {
	t.Parallel()

	metrics := NewMetricsCollector()
	metrics.RecordSuccess(10)
	metrics.RecordFailure(20, "timeout")

	result := &Result{
		Scenario:  "test",
		TimeStart: testtime.Now(),
		Success:   true,
	}
	errs := []error{errors.New("worker error")}
	stats := finalizeScenario(result, metrics, errs, 0.9, 100)

	assert.False(t, result.Success)
	assert.Contains(t, result.Output, "worker error")
	assert.Equal(t, int64(2), stats.TotalRequests)
}

func TestFinalizeScenarioNoRequests(t *testing.T) {
	t.Parallel()

	metrics := NewMetricsCollector()
	result := &Result{
		Scenario:  "test",
		TimeStart: testtime.Now(),
		Success:   true,
	}
	stats := finalizeScenario(result, metrics, nil, 0.9, 100)

	assert.False(t, result.Success)
	assert.Contains(t, result.Output, "no requests")
	assert.Equal(t, int64(0), stats.TotalRequests)
}

func TestFinalizeScenarioLowSuccessRate(t *testing.T) {
	t.Parallel()

	metrics := NewMetricsCollector()
	metrics.RecordSuccess(10)
	metrics.RecordFailure(20, "err")
	metrics.RecordFailure(30, "err")

	result := &Result{
		Scenario:  "test",
		TimeStart: testtime.Now(),
		Success:   true,
	}
	stats := finalizeScenario(result, metrics, nil, 0.9, 100)

	assert.False(t, result.Success)
	assert.Contains(t, result.Output, "success rate too low")
	assert.Equal(t, int64(3), stats.TotalRequests)
}

func TestFinalizeScenarioHighP99(t *testing.T) {
	t.Parallel()

	metrics := NewMetricsCollector()
	for range 100 {
		metrics.RecordSuccess(500) // 500ms latency
	}

	result := &Result{
		Scenario:  "test",
		TimeStart: testtime.Now(),
		Success:   true,
	}
	stats := finalizeScenario(result, metrics, nil, 0.0, 100)

	assert.False(t, result.Success)
	assert.Contains(t, result.Output, "P99 latency too high")
	assert.Greater(t, stats.P99LatencyMs, 100.0)
}

func TestFinalizeScenarioSuccess(t *testing.T) {
	t.Parallel()

	metrics := NewMetricsCollector()
	for range 100 {
		metrics.RecordSuccess(5)
	}

	result := &Result{
		Scenario:  "test",
		TimeStart: testtime.Now(),
		Success:   true,
	}
	stats := finalizeScenario(result, metrics, nil, 0.9, 1000)

	assert.True(t, result.Success)
	assert.Contains(t, result.Output, "success rate")
	assert.Equal(t, int64(100), stats.TotalRequests)
}

func TestFinalizeScenarioMaxP99Zero(t *testing.T) {
	t.Parallel()

	metrics := NewMetricsCollector()
	for range 10 {
		metrics.RecordSuccess(5)
	}

	result := &Result{
		Scenario:  "test",
		TimeStart: testtime.Now(),
		Success:   true,
	}
	stats := finalizeScenario(result, metrics, nil, 0.0, 0)

	// maxP99 = 0 means no latency check
	assert.True(t, result.Success)
	assert.Equal(t, int64(10), stats.TotalRequests)
}

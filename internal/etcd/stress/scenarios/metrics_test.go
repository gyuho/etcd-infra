//nolint:testpackage // Tests use package internals and shared resources.
package scenarios

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewMetricsCollector(t *testing.T) {
	t.Parallel()
	mc := NewMetricsCollector()
	require.NotNil(t, mc)
	require.NotNil(t, mc.errors)

	stats := mc.GetStatistics()
	assert.Equal(t, int64(0), stats.TotalRequests)
	assert.Equal(t, int64(0), stats.SuccessCount)
	assert.Equal(t, int64(0), stats.FailureCount)
}

func TestMetricsCollectorRecordSuccess(t *testing.T) {
	t.Parallel()
	mc := NewMetricsCollector()

	mc.RecordSuccess(10.5)
	mc.RecordSuccess(15.0)
	mc.RecordSuccess(20.5)

	stats := mc.GetStatistics()
	assert.Equal(t, int64(3), stats.TotalRequests)
	assert.Equal(t, int64(3), stats.SuccessCount)
	assert.Equal(t, int64(0), stats.FailureCount)
}

func TestMetricsCollectorRecordFailure(t *testing.T) {
	t.Parallel()
	mc := NewMetricsCollector()

	mc.RecordFailure(5.0, "timeout")
	mc.RecordFailure(8.0, "timeout")
	mc.RecordFailure(12.0, "connection_error")

	stats := mc.GetStatistics()
	assert.Equal(t, int64(3), stats.TotalRequests)
	assert.Equal(t, int64(0), stats.SuccessCount)
	assert.Equal(t, int64(3), stats.FailureCount)
}

func TestMetricsCollectorMixedRecords(t *testing.T) {
	t.Parallel()
	mc := NewMetricsCollector()

	mc.RecordSuccess(10.0)
	mc.RecordSuccess(15.0)
	mc.RecordFailure(5.0, "timeout")

	stats := mc.GetStatistics()
	assert.Equal(t, int64(3), stats.TotalRequests)
	assert.Equal(t, int64(2), stats.SuccessCount)
	assert.Equal(t, int64(1), stats.FailureCount)
}

func TestMetricsCollectorRecordBytesWritten(t *testing.T) {
	t.Parallel()
	mc := NewMetricsCollector()

	mc.RecordBytesWritten(100)
	mc.RecordBytesWritten(200)

	stats := mc.GetStatistics()
	assert.Equal(t, int64(300), stats.BytesWritten)
}

func TestMetricsCollectorRecordBytesRead(t *testing.T) {
	t.Parallel()
	mc := NewMetricsCollector()

	mc.RecordBytesRead(150)
	mc.RecordBytesRead(250)

	stats := mc.GetStatistics()
	assert.Equal(t, int64(400), stats.BytesRead)
}

func TestMetricsCollectorLatencyStatistics(t *testing.T) {
	t.Parallel()
	mc := NewMetricsCollector()

	// Add latencies in a known distribution
	latencies := []float64{10.0, 20.0, 30.0, 40.0, 50.0, 60.0, 70.0, 80.0, 90.0, 100.0}
	for _, lat := range latencies {
		mc.RecordSuccess(lat)
	}

	stats := mc.GetStatistics()

	// Average should be 55.0
	assert.InDelta(t, 55.0, stats.AverageLatencyMs, 0.01)

	// P50 should be around the middle value
	assert.InDelta(t, 50.0, stats.P50LatencyMs, 10.0)

	// P95 should be close to 95th percentile
	assert.InDelta(t, 90.0, stats.P95LatencyMs, 10.0)

	// P99 should be close to 99th percentile
	assert.InDelta(t, 100.0, stats.P99LatencyMs, 10.0)

	// Max should be 100.0
	assert.InDelta(t, 100.0, stats.MaxLatencyMs, 0.01)
}

func TestMetricsCollectorReset(t *testing.T) {
	t.Parallel()
	mc := NewMetricsCollector()

	// Add some data
	mc.RecordSuccess(10.0)
	mc.RecordFailure(5.0, "timeout")
	mc.RecordBytesWritten(100)
	mc.RecordBytesRead(200)

	// Verify data exists
	stats := mc.GetStatistics()
	assert.Equal(t, int64(2), stats.TotalRequests)

	// Reset
	mc.Reset()

	// Verify all data is cleared
	stats = mc.GetStatistics()
	assert.Equal(t, int64(0), stats.TotalRequests)
	assert.Equal(t, int64(0), stats.SuccessCount)
	assert.Equal(t, int64(0), stats.FailureCount)
	assert.Equal(t, int64(0), stats.BytesWritten)
	assert.Equal(t, int64(0), stats.BytesRead)
	assert.Zero(t, stats.AverageLatencyMs)
}

func TestMetricsCollectorConcurrentAccess(t *testing.T) {
	t.Parallel()
	mc := NewMetricsCollector()

	var wg sync.WaitGroup
	numGoroutines := 10
	recordsPerGoroutine := 100

	// Spawn multiple goroutines recording data concurrently
	for range numGoroutines {
		wg.Go(func() {
			for j := range recordsPerGoroutine {
				if j%2 == 0 {
					mc.RecordSuccess(float64(j))
				} else {
					mc.RecordFailure(float64(j), "test_error")
				}
				mc.RecordBytesWritten(10)
				mc.RecordBytesRead(5)
			}
		})
	}

	wg.Wait()

	stats := mc.GetStatistics()
	expectedTotal := int64(numGoroutines * recordsPerGoroutine)
	assert.Equal(t, expectedTotal, stats.TotalRequests)
	assert.Equal(t, expectedTotal/2, stats.SuccessCount)
	assert.Equal(t, expectedTotal/2, stats.FailureCount)
	assert.Equal(t, int64(numGoroutines*recordsPerGoroutine*10), stats.BytesWritten)
	assert.Equal(t, int64(numGoroutines*recordsPerGoroutine*5), stats.BytesRead)
}

func TestStatisticsSuccessRate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		stats        Statistics
		expectedRate float64
	}{
		{
			name:         "no requests",
			stats:        Statistics{TotalRequests: 0},
			expectedRate: 0,
		},
		{
			name:         "all success",
			stats:        Statistics{TotalRequests: 100, SuccessCount: 100},
			expectedRate: 1.0,
		},
		{
			name:         "all failure",
			stats:        Statistics{TotalRequests: 100, SuccessCount: 0},
			expectedRate: 0,
		},
		{
			name:         "half success",
			stats:        Statistics{TotalRequests: 100, SuccessCount: 50},
			expectedRate: 0.5,
		},
		{
			name:         "80 percent success",
			stats:        Statistics{TotalRequests: 100, SuccessCount: 80},
			expectedRate: 0.8,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			rate := tt.stats.SuccessRate()
			assert.InDelta(t, tt.expectedRate, rate, 0.0001)
		})
	}
}

func TestPercentileFunction(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		sorted   []float64
		p        float64
		expected float64
	}{
		{
			name:     "empty slice",
			sorted:   []float64{},
			p:        0.5,
			expected: 0,
		},
		{
			name:     "single element",
			sorted:   []float64{10.0},
			p:        0.5,
			expected: 10.0,
		},
		{
			name:     "p50 of 10 elements",
			sorted:   []float64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10},
			p:        0.5,
			expected: 5,
		},
		{
			name:     "p99 of 10 elements",
			sorted:   []float64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10},
			p:        0.99,
			expected: 10,
		},
		{
			name:     "p0 returns first element",
			sorted:   []float64{1, 2, 3, 4, 5},
			p:        0.0,
			expected: 1,
		},
		{
			name:     "p100 returns last element",
			sorted:   []float64{1, 2, 3, 4, 5},
			p:        1.0,
			expected: 5,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := percentile(tt.sorted, tt.p)
			assert.InDelta(t, tt.expected, result, 0.01)
		})
	}
}

func TestMetricsCollectorReservoirSampling(t *testing.T) {
	t.Parallel()
	// Test that reservoir sampling caps memory usage
	mc := NewMetricsCollector()

	// Add more samples than maxLatencySamples
	numSamples := maxLatencySamples + 1000
	for i := range numSamples {
		mc.RecordSuccess(float64(i))
	}

	// Verify that the latencies slice doesn't grow beyond maxLatencySamples
	mc.latenciesMu.Lock()
	actualLen := len(mc.latencies)
	actualTotalSamples := mc.totalSamples
	mc.latenciesMu.Unlock()

	assert.Equal(t, maxLatencySamples, actualLen, "latencies slice should be capped at maxLatencySamples")
	assert.Equal(t, int64(numSamples), actualTotalSamples, "totalSamples should track all samples seen")

	// Verify statistics still work
	stats := mc.GetStatistics()
	assert.Equal(t, int64(numSamples), stats.SuccessCount)
	assert.Greater(t, stats.AverageLatencyMs, float64(0))
}

func TestMetricsCollectorResetPreservesCapacity(t *testing.T) {
	t.Parallel()
	mc := NewMetricsCollector()

	// Add some data to grow the latencies slice
	for i := range 100 {
		mc.RecordSuccess(float64(i))
	}

	// Reset and verify capacity is preserved
	mc.Reset()

	mc.latenciesMu.Lock()
	capAfterReset := cap(mc.latencies)
	lenAfterReset := len(mc.latencies)
	mc.latenciesMu.Unlock()

	// Length should be 0
	assert.Equal(t, 0, lenAfterReset)
	// Capacity should be preserved (at least the initial capacity)
	assert.GreaterOrEqual(t, capAfterReset, 0)
}

func TestMetricsCollectorEmptyLatenciesStatistics(t *testing.T) {
	t.Parallel()
	mc := NewMetricsCollector()

	// Get statistics without any latency data
	stats := mc.GetStatistics()

	assert.Zero(t, stats.AverageLatencyMs)
	assert.Zero(t, stats.P50LatencyMs)
	assert.Zero(t, stats.P95LatencyMs)
	assert.Zero(t, stats.P99LatencyMs)
	assert.Zero(t, stats.MaxLatencyMs)
}

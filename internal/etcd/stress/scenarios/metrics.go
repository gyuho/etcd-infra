package scenarios

import (
	"math"
	"sort"
	"sync"
	"sync/atomic"

	"git.tbd/etcd-infra/pkg/randutil"
)

const (
	// maxLatencySamples limits memory growth via reservoir sampling.
	// 100k samples provides excellent percentile accuracy while preventing unbounded growth.
	maxLatencySamples = 100_000
)

// MetricsCollector collects metrics in a thread-safe manner
// Follows cognitive load principle: single responsibility.
type MetricsCollector struct {
	// Atomic counters for lock-free updates
	successCount atomic.Int64
	failureCount atomic.Int64
	bytesWritten atomic.Int64
	bytesRead    atomic.Int64

	// Latency tracking with mutex protection
	// Uses reservoir sampling to bound memory when sample count exceeds maxLatencySamples
	latenciesMu  sync.Mutex
	latencies    []float64
	totalSamples int64 // Total samples seen (including those not in reservoir)

	// Error tracking
	errorsMu sync.RWMutex
	errors   map[string]int
}

// NewMetricsCollector creates a new metrics collector.
func NewMetricsCollector() *MetricsCollector {
	return &MetricsCollector{
		latencies: make([]float64, 0, maxLatencySamples),
		errors:    make(map[string]int),
	}
}

// RecordSuccess records a successful operation.
func (m *MetricsCollector) RecordSuccess(latencyMs float64) {
	m.successCount.Add(1)
	m.recordLatency(latencyMs)
}

// RecordFailure records a failed operation.
func (m *MetricsCollector) RecordFailure(latencyMs float64, errType string) {
	m.failureCount.Add(1)
	m.recordLatency(latencyMs)

	m.errorsMu.Lock()
	m.errors[errType]++
	m.errorsMu.Unlock()
}

// RecordBytesWritten records bytes written.
func (m *MetricsCollector) RecordBytesWritten(bytes int64) {
	m.bytesWritten.Add(bytes)
}

// RecordBytesRead records bytes read.
func (m *MetricsCollector) RecordBytesRead(bytes int64) {
	m.bytesRead.Add(bytes)
}

// Reset clears all collected metrics so the collector can be reused between scenarios.
func (m *MetricsCollector) Reset() {
	m.successCount.Store(0)
	m.failureCount.Store(0)
	m.bytesWritten.Store(0)
	m.bytesRead.Store(0)

	m.latenciesMu.Lock()
	m.totalSamples = 0
	if cap(m.latencies) == 0 {
		m.latencies = make([]float64, 0, maxLatencySamples)
	} else {
		m.latencies = m.latencies[:0]
	}
	m.latenciesMu.Unlock()

	m.errorsMu.Lock()
	m.errors = make(map[string]int)
	m.errorsMu.Unlock()
}

// Statistics holds computed statistics.
type Statistics struct {
	TotalRequests     int64
	SuccessCount      int64
	FailureCount      int64
	BytesWritten      int64
	BytesRead         int64
	AverageLatencyMs  float64
	P50LatencyMs      float64
	P95LatencyMs      float64
	P99LatencyMs      float64
	MaxLatencyMs      float64
	RequestsPerSecond float64
}

// GetStatistics computes current statistics.
func (m *MetricsCollector) GetStatistics() Statistics {
	stats := Statistics{
		SuccessCount: m.successCount.Load(),
		FailureCount: m.failureCount.Load(),
		BytesWritten: m.bytesWritten.Load(),
		BytesRead:    m.bytesRead.Load(),
	}
	stats.TotalRequests = stats.SuccessCount + stats.FailureCount

	m.latenciesMu.Lock()
	latenciesCopy := make([]float64, len(m.latencies))
	copy(latenciesCopy, m.latencies)
	m.latenciesMu.Unlock()

	if len(latenciesCopy) > 0 {
		sort.Float64s(latenciesCopy)

		// Calculate average
		var sum float64
		for _, lat := range latenciesCopy {
			sum += lat
		}
		stats.AverageLatencyMs = sum / float64(len(latenciesCopy))

		// Calculate percentiles
		stats.P50LatencyMs = percentile(latenciesCopy, 0.50)
		stats.P95LatencyMs = percentile(latenciesCopy, 0.95)
		stats.P99LatencyMs = percentile(latenciesCopy, 0.99)
		stats.MaxLatencyMs = latenciesCopy[len(latenciesCopy)-1]
	}

	return stats
}

// recordLatency adds a latency sample using reservoir sampling to prevent unbounded growth.
func (m *MetricsCollector) recordLatency(latencyMs float64) {
	m.latenciesMu.Lock()
	defer m.latenciesMu.Unlock()

	m.totalSamples++

	// Reservoir sampling: keep first maxLatencySamples, then probabilistically replace
	if len(m.latencies) < maxLatencySamples {
		m.latencies = append(m.latencies, latencyMs)
	} else {
		// With probability k/n, replace a random existing sample
		// where k = maxLatencySamples, n = totalSamples
		j := randutil.Intn(int(m.totalSamples))
		if j < maxLatencySamples {
			m.latencies[j] = latencyMs
		}
	}
}

// SuccessRate returns the success rate.
func (s Statistics) SuccessRate() float64 {
	if s.TotalRequests == 0 {
		return 0
	}

	return float64(s.SuccessCount) / float64(s.TotalRequests)
}

// percentile calculates the percentile value from sorted slice.
func percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}

	if p <= 0 {
		return sorted[0]
	}
	if p >= 1 {
		return sorted[len(sorted)-1]
	}

	rank := int(math.Ceil(p * float64(len(sorted))))
	index := rank - 1
	index = max(index, 0)
	if index >= len(sorted) {
		index = len(sorted) - 1
	}

	return sorted[index]
}

package scenarios

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestLatencyBucketsMergeAcrossCollectors: histograms from independent
// collectors sum element-wise, and the merged p99 comes from the combined
// distribution rather than a mean of per-collector p99s.
func TestLatencyBucketsMergeAcrossCollectors(t *testing.T) {
	t.Parallel()

	a, b := NewMetricsCollector(), NewMetricsCollector()
	// Collector a: one slow outlier in a fast run. Collector b: uniformly fast.
	// A mean of per-run p99s would report the outlier; the merged p99 does not.
	for range 99 {
		a.RecordSuccess(2.0)
		b.RecordSuccess(2.0)
	}
	a.RecordSuccess(50.0)

	bucketsA, bucketsB := a.LatencyBuckets(), b.LatencyBuckets()
	require.Len(t, bucketsA, latencyBucketCount)
	require.Len(t, bucketsB, latencyBucketCount)

	merged := make([]int64, latencyBucketCount)
	for i := range merged {
		merged[i] = bucketsA[i] + bucketsB[i]
	}
	var total int64
	for _, c := range merged {
		total += c
	}
	assert.Equal(t, int64(199), total)

	// Merged p99: 197th of 199 samples sits in the 2 ms bucket, well below
	// the 50 ms outlier bucket.
	p99 := mergedPercentileUpperBound(merged, 0.99)
	assert.InDelta(t, 2.0, p99, LatencyBucketUpperBoundMs(latencyBucketIndex(2.0))-2.0)
	assert.Less(t, p99, 50.0)

	// And the outlier is still visible in the merged distribution.
	assert.Positive(t, merged[latencyBucketIndex(50.0)])
}

// TestLatencyBucketIndex: bucket boundaries and clamping.
func TestLatencyBucketIndex(t *testing.T) {
	t.Parallel()

	assert.Equal(t, 0, latencyBucketIndex(0))
	assert.Equal(t, 0, latencyBucketIndex(0.0625))
	assert.Equal(t, latencyBucketCount-1, latencyBucketIndex(1e9))
	// 2 ms and 2.1 ms share a bucket (resolution is about 9%).
	assert.Equal(t, latencyBucketIndex(2.0), latencyBucketIndex(2.1))
	// One octave per eight steps: bucket i+8's lower bound is 2x bucket i's.
	assert.InDelta(t, 2.0,
		LatencyBucketUpperBoundMs(24)/LatencyBucketUpperBoundMs(16), 0.001)
}

// TestLatencyBucketsReset: Reset clears the histogram for reuse.
func TestLatencyBucketsReset(t *testing.T) {
	t.Parallel()

	m := NewMetricsCollector()
	m.RecordSuccess(1.0)
	m.Reset()
	var total int64
	for _, c := range m.LatencyBuckets() {
		total += c
	}
	assert.Zero(t, total)
}

// mergedPercentileUpperBound mirrors the benchmark aggregation: the smallest
// bucket whose cumulative count reaches p of the total, reported as that
// bucket's upper bound.
func mergedPercentileUpperBound(buckets []int64, p float64) float64 {
	var total int64
	for _, c := range buckets {
		total += c
	}
	if total == 0 {
		return 0
	}
	threshold := int64(float64(total) * p)
	var cumulative int64
	for i, c := range buckets {
		cumulative += c
		if cumulative >= threshold {
			return LatencyBucketUpperBoundMs(i)
		}
	}
	return LatencyBucketUpperBoundMs(len(buckets) - 1)
}

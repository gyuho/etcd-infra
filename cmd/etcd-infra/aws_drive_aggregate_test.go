package main

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeClientResults writes one results.jsonl record set and metric files for
// a fake stress client under dir/<name>.
func writeClientResults(t *testing.T, dir, name, jsonl string, before, after int64) {
	t.Helper()
	base := filepath.Join(dir, name)
	require.NoError(t, os.MkdirAll(base, 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(base, "results.jsonl"), []byte(jsonl), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(base, "metrics-before.txt"), []byte(itoa(before)), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(base, "metrics-after.txt"), []byte(itoa(after)), 0o600))
}

func itoa(v int64) string {
	return strings.TrimSpace(strings.ReplaceAll(strings.TrimPrefix(strings.TrimSuffix(strings.Repeat("0", 0), ""), ""), "", "") + fmt.Sprintf("%d", v))
}

func bucketIndex(ms float64) int {
	if ms <= 0.0625 {
		return 0
	}
	idx := int(math.Floor(8 * math.Log2(ms/0.0625)))
	if idx > 144 {
		idx = 144
	}
	return idx
}

func record(scenario string, requests int64, rps, avgMs float64, buckets []int64, success bool) string {
	b, _ := json.Marshal(map[string]any{
		"scenario":          scenario,
		"totalRequests":     requests,
		"failedRequests":    map[bool]int64{true: 0, false: 1}[success],
		"requestsPerSecond": rps,
		"averageLatency":    fmt.Sprintf("%gms", avgMs),
		"latencyBuckets":    buckets,
		"success":           success,
	})
	return string(b)
}

// TestAggregateDriveResultsMergesClients: two clients that ran concurrently
// merge into one fleet summary; a 50 ms outlier in one client does not move
// the merged p99 the way averaging per-client p99s would.
func TestAggregateDriveResultsMergesClients(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	fast := make([]int64, 145)
	fast[bucketIndex(2.0)] = 99
	withOutlier := make([]int64, 145)
	withOutlier[bucketIndex(2.0)] = 99
	withOutlier[bucketIndex(50.0)] = 1

	targets := []awsInstanceState{{Name: "c1"}, {Name: "c2"}, {Name: "c3-missing"}}
	writeClientResults(t, dir, "c1",
		record("A", 100, 10, 2.0, withOutlier, true)+"\n"+record("B", 50, 5, 3.0, fast, true)+"\n",
		1000, 1600)
	writeClientResults(t, dir, "c2",
		record("A", 100, 10, 2.0, fast, true)+"\n"+record("B", 50, 5, 3.0, fast, false)+"\n",
		2000, 2400)

	agg := aggregateDriveResults(dir, targets)
	assert.Equal(t, 2, agg.Clients)
	assert.Equal(t, 4, agg.Records)
	assert.Equal(t, int64(300), agg.Requests)
	assert.Equal(t, int64(1), agg.FailedRequests)
	assert.Equal(t, 30.0, agg.CombinedRequestsPerSecond)
	// Request-weighted average: (100*2 + 50*3 + 100*2 + 50*3) / 300 = 2.5 ms.
	assert.InDelta(t, 700.0/300.0, agg.AverageLatencyMs, 0.001)
	// Merged p99: 299 of 300 samples are at 2 ms, so the fleet p99 sits in the
	// 2 ms bucket even though client c1 carried the 50 ms outlier.
	assert.Less(t, agg.P99LatencyMs, 3.0)
	// Peer bytes sum across clients: (1600-1000) + (2400-2000) = 1000.
	assert.Equal(t, int64(1000), agg.PeerSentBytes)
	assert.Equal(t, []string{"B"}, agg.FailedScenarios)
}

// TestAggregateDriveResultsEmpty: no results at all yields a zero aggregate,
// not a failure.
func TestAggregateDriveResultsEmpty(t *testing.T) {
	t.Parallel()
	agg := aggregateDriveResults(t.TempDir(), []awsInstanceState{{Name: "nope"}})
	assert.Equal(t, 0, agg.Clients)
	assert.Equal(t, int64(0), agg.Requests)
	assert.Equal(t, -1.0, agg.P99LatencyMs)
}

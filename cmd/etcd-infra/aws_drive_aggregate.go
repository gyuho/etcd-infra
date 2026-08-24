package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"git.tbd/etcd-infra/internal/etcd/stress/scenarios"
)

// driveAggregate is the cross-client summary of one drive: every selected
// stress client ran the same suite against the same cluster, so their results
// merge into one fleet-wide picture instead of N independent reports.
type driveAggregate struct {
	// Clients is the number of stress clients whose results were found.
	Clients int
	// Records is the number of scenario records merged.
	Records int
	// Requests and FailedRequests sum across all clients.
	Requests       int64
	FailedRequests int64
	// CombinedRequestsPerSecond is the sum of the per-client rates: the
	// clients ran concurrently, so their rates add.
	CombinedRequestsPerSecond float64
	// AverageLatencyMs is request-weighted across every client request.
	AverageLatencyMs float64
	// P99LatencyMs comes from the merged latency histogram (bucket upper
	// bound; see metrics.go). -1 when no record carried a histogram.
	P99LatencyMs float64
	// PeerSentBytes sums the per-client member-metric deltas.
	PeerSentBytes int64
	// FailedScenarios names the scenarios with any failed record.
	FailedScenarios []string
}

// aggregateDriveResults merges the downloaded results of every selected
// stress client. Missing or malformed per-client files are tolerated: a
// client without results simply does not contribute.
func aggregateDriveResults(dir string, targets []awsInstanceState) driveAggregate {
	var agg driveAggregate
	agg.P99LatencyMs = -1

	var buckets []int64
	var weightedLatencySum float64
	failedScenarios := map[string]bool{}

	for _, client := range targets {
		base := filepath.Join(dir, client.Name)

		before := readMetricFile(filepath.Join(base, "metrics-before.txt"))
		after := readMetricFile(filepath.Join(base, "metrics-after.txt"))
		if before >= 0 && after >= 0 {
			agg.PeerSentBytes += after - before
		}

		data, err := os.ReadFile(filepath.Join(base, "results.jsonl"))
		if err != nil {
			continue
		}
		agg.Clients++
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if !strings.HasPrefix(line, "{") {
				continue
			}
			var result scenarios.Result
			if err := json.Unmarshal([]byte(line), &result); err != nil {
				continue
			}
			agg.Records++
			agg.Requests += result.TotalRequests
			agg.FailedRequests += result.FailedRequests
			agg.CombinedRequestsPerSecond += result.RequestsPerSecond
			weightedLatencySum += result.AverageLatency.Seconds() * 1000 * float64(result.TotalRequests)
			if !result.Success {
				failedScenarios[result.Scenario] = true
			}
			if len(result.LatencyBuckets) > 0 {
				if buckets == nil {
					buckets = make([]int64, len(result.LatencyBuckets))
				}
				for i, count := range result.LatencyBuckets {
					buckets[i] += count
				}
			}
		}
	}

	if agg.Requests > 0 {
		agg.AverageLatencyMs = weightedLatencySum / float64(agg.Requests)
	}
	if buckets != nil {
		agg.P99LatencyMs = mergedP99Ms(buckets)
	}
	for scenario := range failedScenarios {
		agg.FailedScenarios = append(agg.FailedScenarios, scenario)
	}
	return agg
}

// mergedP99Ms returns the upper bound of the bucket holding the fleet-wide
// 99th-percentile request. Bucket geometry: bucket i covers
// [0.0625*2^(i/8), 0.0625*2^((i+1)/8)) ms.
func mergedP99Ms(buckets []int64) float64 {
	var total int64
	for _, count := range buckets {
		total += count
	}
	if total == 0 {
		return -1
	}
	threshold := int64(float64(total) * 0.99)
	if threshold < 1 {
		threshold = 1
	}
	var cumulative int64
	for i, count := range buckets {
		cumulative += count
		if cumulative >= threshold {
			return scenarios.LatencyBucketUpperBoundMs(i)
		}
	}
	return scenarios.LatencyBucketUpperBoundMs(len(buckets) - 1)
}

// printDriveAggregate prints the cross-client summary after the per-client
// lines.
func printDriveAggregate(agg driveAggregate) {
	if agg.Clients == 0 {
		fmt.Println("aggregate: no per-client results found")
		return
	}
	fmt.Println()
	fmt.Printf("aggregate across %d stress client(s): %d scenario records, %d requests (%d failed), %.1f combined ops/s\n",
		agg.Clients, agg.Records, agg.Requests, agg.FailedRequests, agg.CombinedRequestsPerSecond)
	if agg.AverageLatencyMs > 0 {
		fmt.Printf("fleet latency: average %.2f ms", agg.AverageLatencyMs)
		if agg.P99LatencyMs > 0 {
			fmt.Printf(", p99 %.2f ms (merged histogram)", agg.P99LatencyMs)
		}
		fmt.Println()
	}
	if agg.PeerSentBytes > 0 {
		fmt.Printf("fleet peer-sent bytes: %d\n", agg.PeerSentBytes)
	}
	if len(agg.FailedScenarios) > 0 {
		fmt.Printf("failed scenarios: %s\n", strings.Join(agg.FailedScenarios, ", "))
	}
}

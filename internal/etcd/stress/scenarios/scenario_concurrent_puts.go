package scenarios

import (
	"fmt"

	testtime "git.tbd/etcd-infra/internal/etcd/testtime"

	logutil "git.tbd/etcd-infra/pkg/log"
)

// RunConcurrentPuts kube-apiserver issues overlapping GuaranteedUpdate writes via staging/src/k8s.io/apiserver/pkg/storage/etcd3/store.go; this workload mirrors the same concurrent Put pressure.
func RunConcurrentPuts(runner StressRunner) {
	logutil.S().Infow("running", "scenario", ConcurrentPuts.String())

	// Same result initialization pattern as conformance
	result := &Result{
		Scenario:  ConcurrentPuts.String(),
		TimeStart: testtime.Now(),
		Success:   true,
		Output:    "ok",
	}

	// Same defer pattern for recording as conformance
	defer func() {
		result.RecordTimeEnd(testtime.Now())
		runner.RecordResult(*result)
	}()

	// Same client creation pattern as conformance
	cli, err := runner.NewClient()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to create client: %v", err)

		return
	}
	defer func() { _ = cli.Close() }()

	// Get stress-specific components
	metrics := runner.GetMetricsCollector()
	config := runner.GetConfig()

	// Create load generator
	generator := NewLoadGeneratorWithSizes(
		config.DurationSeconds,
		config.RequestsPerSecond,
		config.KeySizeBytes,
		config.ValueSizeBytes,
	)

	logutil.S().Infow("starting concurrent workers",
		"workers", config.ConcurrentWorkers,
		"duration", config.DurationSeconds,
		"rps", config.RequestsPerSecond,
	)

	errors := runPutWorkerPool(runner, cli, metrics, generator, config.ConcurrentWorkers)
	// 10 concurrent workers over encrypted WireGuard tunnel; P99 spikes are
	// expected when Raft consensus crosses the overlay network.
	stats := finalizeScenario(result, metrics, errors, 0.80, 2500)

	if result.Success {
		result.Output = fmt.Sprintf("success rate %.2f%%, p99 %.0fms", stats.SuccessRate()*100, stats.P99LatencyMs)
	}

	logutil.S().Infow("scenario completed",
		"scenario", ConcurrentPuts.String(),
		"total_requests", result.TotalRequests,
		"success_rate", fmt.Sprintf("%.2f%%", stats.SuccessRate()*100),
		"p50_latency", result.P50Latency.Milliseconds(),
		"p99_latency", result.P99Latency.Milliseconds(),
	)
}

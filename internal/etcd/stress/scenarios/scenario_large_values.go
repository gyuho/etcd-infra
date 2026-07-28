package scenarios

import (
	"fmt"
	"strings"
	"time"

	testtime "git.tbd/etcd-infra/internal/etcd/testtime"

	logutil "git.tbd/etcd-infra/pkg/log"
)

// RunLargeValues kube-apiserver persists large Secrets and ConfigMaps through staging/src/k8s.io/apiserver/pkg/storage/etcd3/store.go; this stress case checks etcd preserves latency when handling those big payloads.
// For real stress testing on bare-metal or high-resource VMs, bump up value size and workers.
func RunLargeValues(runner StressRunner) {
	logutil.S().Infow("running", "scenario", LargeValues.String())

	result := &Result{
		Scenario:  LargeValues.String(),
		TimeStart: testtime.Now(),
		Success:   true,
		Output:    "ok",
	}
	defer func() {
		result.RecordTimeEnd(testtime.Now())
		runner.RecordResult(*result)
	}()

	cli, err := runner.NewClient()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to create client: %v", err)

		return
	}
	defer func() { _ = cli.Close() }()

	metrics := runner.GetMetricsCollector()
	metrics.Reset()
	cfg := runner.GetConfig()

	// Use reduced settings to prevent overwhelming container-based clusters.
	// For real stress testing, use workerCount(cfg) and larger values.
	workers := min(workerCount(cfg), 2) // Cap at 2 workers for large value testing
	duration := scenarioDuration(cfg)
	deadline := time.Now().Add(duration)

	// Use smaller values for VM testing (256KB instead of 1MB).
	// For real stress testing, use 1MB+ values.
	largeValueSize := max(valueSize(cfg, 256*1024), 256*1024)
	largeValue := strings.Repeat("x", largeValueSize)

	// Ensure reasonable interval between large writes
	perWorkerInterval := max(computePerWorkerInterval(cfg.RequestsPerSecond, workers), 200*time.Millisecond)

	errors := runWorkers(workers, func(workerID int, _ chan<- error) {
		for time.Now().Before(deadline) {
			key := runner.GenerateRandomKey(keySize(cfg, 16))
			_ = performPutWithMetrics(runner, cli, metrics, key, largeValue, workerID)
			sleepUntil(deadline, perWorkerInterval)
		}
	})

	// 256KB payloads have higher overhead over WireGuard due to MTU fragmentation
	// and encryption per-packet cost.
	stats := finalizeScenario(result, metrics, errors, 0.75, 8000)
	if result.Success {
		result.Output = fmt.Sprintf("large values success %.2f%%, p99 %.0fms", stats.SuccessRate()*100, stats.P99LatencyMs)
	}

	logutil.S().Infow("scenario completed",
		"scenario", LargeValues.String(),
		"total_requests", result.TotalRequests,
		"bytes_written", result.BytesWritten,
		"success_rate", fmt.Sprintf("%.2f%%", stats.SuccessRate()*100),
		"p99_latency", result.P99Latency.Milliseconds(),
	)
}

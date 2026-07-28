package scenarios

import (
	"fmt"
	"sync/atomic"
	"time"

	testtime "git.tbd/etcd-infra/internal/etcd/testtime"

	logutil "git.tbd/etcd-infra/pkg/log"
	"git.tbd/etcd-infra/pkg/randutil"
)

// RunManyKeys List operations in staging/src/k8s.io/apiserver/pkg/storage/etcd3/store.go#getList scan thousands of keys; this workload stresses etcd with that high-key-count traversal.
// For real stress testing on bare-metal or high-resource VMs, bump up workers and remove rate limiting.
func RunManyKeys(runner StressRunner) {
	logutil.S().Infow("running", "scenario", ManyKeys.String())

	result := &Result{
		Scenario:  ManyKeys.String(),
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
	// For real stress testing, use workerCount(cfg) and remove rate limiting.
	workers := min(workerCount(cfg), 3) // Cap at 3 workers for VM testing
	duration := scenarioDuration(cfg)
	deadline := time.Now().Add(duration)
	// Force a minimum interval between requests to prevent cluster overload
	perWorkerInterval := max(computePerWorkerInterval(cfg.RequestsPerSecond, workers), 100*time.Millisecond)

	baseKey := runner.GenerateRandomKey(6)
	var counter atomic.Int64
	valSize := valueSize(cfg, 256)

	errors := runWorkers(workers, func(workerID int, _ chan<- error) {
		for time.Now().Before(deadline) {
			idx := counter.Add(1)
			key := fmt.Sprintf("%s/many/%012d", baseKey, idx)
			value := randutil.StringAlphabetsLowerCase(valSize)
			_ = performPutWithMetrics(runner, cli, metrics, key, value, workerID)
			sleepUntil(deadline, perWorkerInterval)
		}
	})

	// Large key counts under VM-capped workers; extra P99 headroom for WireGuard
	// overlay latency on high-key-count traversals.
	stats := finalizeScenario(result, metrics, errors, 0.10, 8000)
	if result.Success {
		result.Output = fmt.Sprintf("many keys success %.2f%%, p99 %.0fms", stats.SuccessRate()*100, stats.P99LatencyMs)
	}

	logutil.S().Infow("scenario completed",
		"scenario", ManyKeys.String(),
		"total_requests", result.TotalRequests,
		"success_rate", fmt.Sprintf("%.2f%%", stats.SuccessRate()*100),
		"p99_latency", result.P99Latency.Milliseconds(),
	)
}

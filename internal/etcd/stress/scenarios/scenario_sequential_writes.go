package scenarios

import (
	"fmt"
	"sync/atomic"
	"time"

	testtime "git.tbd/etcd-infra/internal/etcd/testtime"

	logutil "git.tbd/etcd-infra/pkg/log"
	"git.tbd/etcd-infra/pkg/randutil"
)

// RunSequentialWrites Stateful workloads update objects sequentially via staging/src/k8s.io/apiserver/pkg/storage/etcd3/store.go; this scenario ensures etcd maintains order and latency under that pattern.
func RunSequentialWrites(runner StressRunner) {
	logutil.S().Infow("running", "scenario", SequentialWrites.String())

	result := &Result{
		Scenario:  SequentialWrites.String(),
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

	workers := workerCount(cfg)
	duration := scenarioDuration(cfg)
	deadline := time.Now().Add(duration)
	perWorkerInterval := computePerWorkerInterval(cfg.RequestsPerSecond, workers)

	var globalCounter atomic.Int64
	baseKey := runner.GenerateRandomKey(8)
	valSize := valueSize(cfg, 256)

	errors := runWorkers(workers, func(workerID int, _ chan<- error) {
		for time.Now().Before(deadline) {
			seq := globalCounter.Add(1)
			key := fmt.Sprintf("%s/sequential/%012d", baseKey, seq)
			value := randutil.StringAlphabetsLowerCase(valSize)
			_ = performPutWithMetrics(runner, cli, metrics, key, value, workerID)
			sleepUntil(deadline, perWorkerInterval)
		}
	})

	// Sequential writes serialize on Raft consensus; each write waits for quorum
	// across the WireGuard overlay, adding per-operation latency.
	stats := finalizeScenario(result, metrics, errors, 0.80, 3000)
	if result.Success {
		result.Output = fmt.Sprintf("sequential writes success %.2f%%, p99 %.0fms", stats.SuccessRate()*100, stats.P99LatencyMs)
	}

	logutil.S().Infow("scenario completed",
		"scenario", SequentialWrites.String(),
		"total_requests", result.TotalRequests,
		"success_rate", fmt.Sprintf("%.2f%%", stats.SuccessRate()*100),
		"p99_latency", result.P99Latency.Milliseconds(),
	)
}

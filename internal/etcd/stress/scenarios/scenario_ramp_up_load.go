package scenarios

import (
	"fmt"
	"math"
	"time"

	testtime "git.tbd/etcd-infra/internal/etcd/testtime"

	logutil "git.tbd/etcd-infra/pkg/log"
	"git.tbd/etcd-infra/pkg/randutil"
)

// RunRampUpLoad During cluster bootstrap kube-apiserver ramps write QPS as more controllers join, all funneled through staging/src/k8s.io/apiserver/pkg/storage/etcd3/store.go; this workload reproduces that rising GuaranteedUpdate load curve.
func RunRampUpLoad(runner StressRunner) {
	logutil.S().Infow("running", "scenario", RampUpLoad.String())

	result := &Result{
		Scenario:  RampUpLoad.String(),
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
	start := time.Now()
	deadline := start.Add(duration)

	baseRPS := cfg.RequestsPerSecond
	if baseRPS <= 0 {
		baseRPS = workers * 10
	}

	valSize := valueSize(cfg, 256)

	errors := runWorkers(workers, func(workerID int, _ chan<- error) {
		for {
			now := time.Now()
			if now.After(deadline) {
				return
			}

			elapsed := now.Sub(start)
			progress := float64(elapsed) / float64(duration)
			if progress < 0.1 {
				progress = 0.1
			} else {
				progress = min(progress, 1)
			}

			currentRPS := int(math.Max(1, progress*float64(baseRPS)))
			interval := computePerWorkerInterval(currentRPS, workers)

			key := runner.GenerateRandomKey(keySize(cfg, 10))
			value := randutil.StringAlphabetsLowerCase(valSize)
			_ = performPutWithMetrics(runner, cli, metrics, key, value, workerID)

			sleepUntil(deadline, interval)
		}
	})

	// Ramp-up phase hits cold caches and connection pools; early requests over
	// WireGuard have higher latency before the tunnel stabilizes.
	stats := finalizeScenario(result, metrics, errors, 0.80, 4000)
	if result.Success {
		result.Output = fmt.Sprintf("ramp-up success %.2f%%, p99 %.0fms", stats.SuccessRate()*100, stats.P99LatencyMs)
	}

	logutil.S().Infow("scenario completed",
		"scenario", RampUpLoad.String(),
		"total_requests", result.TotalRequests,
		"success_rate", fmt.Sprintf("%.2f%%", stats.SuccessRate()*100),
		"p99_latency", result.P99Latency.Milliseconds(),
	)
}

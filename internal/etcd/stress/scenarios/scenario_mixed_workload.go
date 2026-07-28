package scenarios

import (
	"fmt"
	"sync"
	"time"

	testtime "git.tbd/etcd-infra/internal/etcd/testtime"

	logutil "git.tbd/etcd-infra/pkg/log"
	"git.tbd/etcd-infra/pkg/randutil"
)

// RunMixedWorkload Kubernetes mixes reads, writes, and deletes across staging/src/k8s.io/apiserver/pkg/storage/etcd3/store.go and watcher.go; this scenario recreates that blended API pressure.
func RunMixedWorkload(runner StressRunner) {
	logutil.S().Infow("running", "scenario", MixedWorkload.String())

	result := &Result{
		Scenario:  MixedWorkload.String(),
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

	cfg := runner.GetConfig()
	workers := workerCount(cfg)
	duration := scenarioDuration(cfg)
	deadline := time.Now().Add(duration)

	warmup := workers * 2
	warmup = max(warmup, 20)

	keys := make([]string, 0, warmup)
	for range warmup {
		key := runner.GenerateRandomKey(keySize(cfg, 10))
		value := randutil.StringAlphabetsLowerCase(valueSize(cfg, 256))
		ctx, cancel := runner.NewCtxTimeout(5 * time.Second)
		if _, err := cli.Put(ctx, key, value); err != nil {
			logutil.S().Warnw("warmup write failed", "scenario", MixedWorkload.String(), "error", err)
		} else {
			keys = append(keys, key)
		}
		cancel()
	}

	metrics := runner.GetMetricsCollector()
	metrics.Reset()

	var keysMu sync.RWMutex
	perWorkerInterval := computePerWorkerInterval(cfg.RequestsPerSecond, workers)
	readRatio := 40 // percentage of read operations

	errors := runWorkers(workers, func(workerID int, _ chan<- error) {
		for time.Now().Before(deadline) {
			doRead := randutil.Intn(100) < readRatio

			if doRead {
				keysMu.RLock()
				count := len(keys)
				if count > 0 {
					key := keys[randutil.Intn(count)]
					keysMu.RUnlock()
					_ = performGetWithMetrics(runner, cli, metrics, key, workerID)
					sleepUntil(deadline, perWorkerInterval)

					continue
				}
				keysMu.RUnlock()
			}

			key := runner.GenerateRandomKey(keySize(cfg, 10))
			value := randutil.StringAlphabetsLowerCase(valueSize(cfg, 256))
			if err := performPutWithMetrics(runner, cli, metrics, key, value, workerID); err == nil {
				keysMu.Lock()
				keys = append(keys, key)
				keysMu.Unlock()
			}

			sleepUntil(deadline, perWorkerInterval)
		}
	})

	// Mixed read/write workload has variable latency over WireGuard; reads hit
	// quorum and writes hit Raft, both adding overlay network overhead.
	stats := finalizeScenario(result, metrics, errors, 0.85, 3000)
	if result.Success {
		result.Output = fmt.Sprintf("mixed workload success %.2f%%, p99 %.0fms", stats.SuccessRate()*100, stats.P99LatencyMs)
	}

	logutil.S().Infow("scenario completed",
		"scenario", MixedWorkload.String(),
		"total_requests", result.TotalRequests,
		"success_rate", fmt.Sprintf("%.2f%%", stats.SuccessRate()*100),
		"p99_latency", result.P99Latency.Milliseconds(),
	)
}

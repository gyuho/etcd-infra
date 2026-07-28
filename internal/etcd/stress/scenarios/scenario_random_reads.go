package scenarios

import (
	"fmt"
	"sync"
	"time"

	testtime "git.tbd/etcd-infra/internal/etcd/testtime"

	logutil "git.tbd/etcd-infra/pkg/log"
	"git.tbd/etcd-infra/pkg/randutil"
)

// RunRandomReads kube-apiserver serves Get calls through staging/src/k8s.io/apiserver/pkg/storage/etcd3/store.go#get; this workload issues random key lookups to match that read pattern.
func RunRandomReads(runner StressRunner) {
	logutil.S().Infow("running", "scenario", RandomReads.String())

	result := &Result{
		Scenario:  RandomReads.String(),
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

	warmup := workers * 3
	warmup = max(warmup, 30)

	keys := make([]string, 0, warmup)
	for range warmup {
		key := runner.GenerateRandomKey(keySize(cfg, 12))
		value := randutil.StringAlphabetsLowerCase(valueSize(cfg, 256))
		ctx, cancel := runner.NewCtxTimeout(5 * time.Second)
		if _, err := cli.Put(ctx, key, value); err != nil {
			logutil.S().Warnw("warmup write failed", "scenario", RandomReads.String(), "error", err)
		} else {
			keys = append(keys, key)
		}
		cancel()
	}

	metrics := runner.GetMetricsCollector()
	metrics.Reset()

	var keysMu sync.RWMutex
	perWorkerInterval := computePerWorkerInterval(cfg.RequestsPerSecond, workers)
	writeRatio := 20 // 20% writes to keep dataset fresh
	valSize := valueSize(cfg, 256)

	errors := runWorkers(workers, func(workerID int, _ chan<- error) {
		for time.Now().Before(deadline) {
			if randutil.Intn(100) < writeRatio {
				key := runner.GenerateRandomKey(keySize(cfg, 12))
				value := randutil.StringAlphabetsLowerCase(valSize)
				if err := performPutWithMetrics(runner, cli, metrics, key, value, workerID); err == nil {
					keysMu.Lock()
					keys = append(keys, key)
					keysMu.Unlock()
				}
				sleepUntil(deadline, perWorkerInterval)

				continue
			}

			keysMu.RLock()
			if len(keys) == 0 {
				keysMu.RUnlock()
				time.Sleep(10 * time.Millisecond)

				continue
			}
			key := keys[randutil.Intn(len(keys))]
			keysMu.RUnlock()

			_ = performGetWithMetrics(runner, cli, metrics, key, workerID)
			sleepUntil(deadline, perWorkerInterval)
		}
	})

	// Random reads have the tightest P99 expectation; WireGuard RTT and etcd
	// linearizable read quorum both contribute to tail latency.
	stats := finalizeScenario(result, metrics, errors, 0.85, 2000)
	if result.Success {
		result.Output = fmt.Sprintf("random reads success %.2f%%, p99 %.0fms", stats.SuccessRate()*100, stats.P99LatencyMs)
	}

	logutil.S().Infow("scenario completed",
		"scenario", RandomReads.String(),
		"total_requests", result.TotalRequests,
		"success_rate", fmt.Sprintf("%.2f%%", stats.SuccessRate()*100),
		"p99_latency", result.P99Latency.Milliseconds(),
	)
}

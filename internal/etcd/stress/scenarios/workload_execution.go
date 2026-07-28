package scenarios

import (
	"fmt"
	"sync"
	"time"

	testtime "git.tbd/etcd-infra/internal/etcd/testtime"
	clientv3 "go.etcd.io/etcd/client/v3"

	logutil "git.tbd/etcd-infra/pkg/log"
	"git.tbd/etcd-infra/pkg/randutil"
)

type workloadOpFunc func(workerID int, errCh chan<- error)

func runWorkers(concurrency int, fn workloadOpFunc) []error {
	if concurrency <= 0 {
		concurrency = 1
	}

	errCh := make(chan error, concurrency)

	var wg sync.WaitGroup
	for i := range concurrency {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			defer recoverWorker(errCh, workerID)

			fn(workerID, errCh)
		}(i)
	}

	wg.Wait()
	close(errCh)

	return drainErrors(errCh)
}

func runPutWorkerPool(runner StressRunner, cli *clientv3.Client, metrics *MetricsCollector, generator LoadGenerator, concurrency int) []error {
	metrics.Reset()

	cfg := runner.GetConfig()
	workCh := generator.Generate()
	defer generator.Stop()

	return runWorkers(concurrency, func(workerID int, _ chan<- error) {
		for req := range workCh {
			key := runner.GenerateRandomKey(10)
			value := req.Value
			if value == "" {
				value = randutil.StringAlphabetsLowerCase(cfg.ValueSizeBytes)
			}

			if err := performPutWithMetrics(runner, cli, metrics, key, value, workerID); err != nil {
				// Error is already logged and tracked in metrics
				// Only send to error channel for critical tracking
				logutil.S().Debugw("put operation failed in worker pool", "worker", workerID, "error", err)
			}
		}
	})
}

func performPutWithMetrics(runner StressRunner, cli *clientv3.Client, metrics *MetricsCollector, key, value string, workerID int) error {
	start := time.Now()
	ctx, cancel := runner.NewCtxTimeout(5 * time.Second)
	defer cancel()

	_, err := cli.Put(ctx, key, value)
	latencyMs := float64(time.Since(start).Milliseconds())

	if err != nil {
		metrics.RecordFailure(latencyMs, err.Error())
		logutil.S().Debugw("put failed", "key", key, "worker", workerID, "error", err)

		return fmt.Errorf("put key: %w", err)
	}

	metrics.RecordSuccess(latencyMs)
	metrics.RecordBytesWritten(int64(len(key) + len(value)))

	return nil
}

func performGetWithMetrics(runner StressRunner, cli *clientv3.Client, metrics *MetricsCollector, key string, workerID int) error {
	start := time.Now()
	ctx, cancel := runner.NewCtxTimeout(5 * time.Second)
	defer cancel()

	resp, err := cli.Get(ctx, key)
	latencyMs := float64(time.Since(start).Milliseconds())

	if err != nil {
		metrics.RecordFailure(latencyMs, err.Error())
		logutil.S().Debugw("get failed", "key", key, "worker", workerID, "error", err)

		return fmt.Errorf("failed to get key: %w", err)
	}

	metrics.RecordSuccess(latencyMs)

	var bytesRead int64
	for _, kv := range resp.Kvs {
		bytesRead += int64(len(kv.Key) + len(kv.Value))
	}
	if bytesRead > 0 {
		metrics.RecordBytesRead(bytesRead)
	}

	return nil
}

func recoverWorker(errCh chan<- error, workerID int) {
	if r := recover(); r != nil {
		errCh <- fmt.Errorf("worker %d panic: %v", workerID, r)
	}
}

func drainErrors(errCh <-chan error) []error {
	var errs []error
	for err := range errCh {
		if err != nil {
			errs = append(errs, err)
		}
	}

	return errs
}

func finalizeScenario(result *Result, metrics *MetricsCollector, errs []error, minSuccessRate, maxP99 float64) Statistics {
	stats := metrics.GetStatistics()

	result.TotalRequests = stats.TotalRequests
	result.SuccessfulRequests = stats.SuccessCount
	result.FailedRequests = stats.FailureCount
	result.AverageLatency = testtime.Duration{Duration: time.Duration(stats.AverageLatencyMs * float64(time.Millisecond))}
	result.P50Latency = testtime.Duration{Duration: time.Duration(stats.P50LatencyMs * float64(time.Millisecond))}
	result.P95Latency = testtime.Duration{Duration: time.Duration(stats.P95LatencyMs * float64(time.Millisecond))}
	result.P99Latency = testtime.Duration{Duration: time.Duration(stats.P99LatencyMs * float64(time.Millisecond))}
	result.MaxLatency = testtime.Duration{Duration: time.Duration(stats.MaxLatencyMs * float64(time.Millisecond))}
	result.BytesWritten = stats.BytesWritten
	result.BytesRead = stats.BytesRead

	if len(errs) > 0 {
		result.Success = false
		result.Output = firstErrorMessage(errs)

		return stats
	}

	if stats.TotalRequests == 0 {
		result.Success = false
		result.Output = "no requests executed"

		return stats
	}

	if stats.SuccessRate() < minSuccessRate {
		result.Success = false
		result.Output = fmt.Sprintf("success rate too low: %.2f%% (expected >= %.2f%%)", stats.SuccessRate()*100, minSuccessRate*100)

		return stats
	}

	if maxP99 > 0 && stats.P99LatencyMs > maxP99 {
		result.Success = false
		result.Output = fmt.Sprintf("P99 latency too high: %.0fms (expected <= %.0fms)", stats.P99LatencyMs, maxP99)

		return stats
	}

	result.Output = fmt.Sprintf("success rate %.2f%%, p99 %.0fms", stats.SuccessRate()*100, stats.P99LatencyMs)

	return stats
}

func firstErrorMessage(errs []error) string {
	if len(errs) == 0 {
		return ""
	}

	return errs[0].Error()
}

func computePerWorkerInterval(rps, workers int) time.Duration {
	if rps <= 0 || workers <= 0 {
		return 0
	}

	perWorker := float64(rps) / float64(workers)
	if perWorker <= 0 {
		perWorker = 1
	}

	interval := time.Duration(float64(time.Second) / perWorker)
	if interval < 0 {
		return 0
	}

	return interval
}

func sleepUntil(deadline time.Time, interval time.Duration) {
	if interval <= 0 {
		return
	}

	remaining := time.Until(deadline)
	if remaining <= 0 {
		return
	}

	if interval > remaining {
		time.Sleep(remaining)
	} else {
		time.Sleep(interval)
	}
}

package scenarios

import (
	"fmt"
	"time"

	testtime "git.tbd/etcd-infra/internal/etcd/testtime"
	clientv3 "go.etcd.io/etcd/client/v3"

	logutil "git.tbd/etcd-infra/pkg/log"
	"git.tbd/etcd-infra/pkg/randutil"
)

// RunHighContention GuaranteedUpdate in staging/src/k8s.io/apiserver/pkg/storage/etcd3/store.go uses Compare/Put loops that contend on the same key; this workload drives that high-conflict pattern.
func RunHighContention(runner StressRunner) {
	logutil.S().Infow("running", "scenario", HighContention.String())

	result := &Result{
		Scenario:  HighContention.String(),
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

	contentionKeys := make([]string, 0, intMax(3, workers))
	limit := intMax(3, workers)
	for i := range limit {
		contentionKeys = append(contentionKeys, fmt.Sprintf("%s-contention-%d", runner.GenerateRandomKey(6), i))
	}

	valSize := valueSize(cfg, 256)

	errors := runWorkers(workers, func(workerID int, _ chan<- error) {
		for time.Now().Before(deadline) {
			key := contentionKeys[randutil.Intn(len(contentionKeys))]
			value := randutil.StringAlphabetsLowerCase(valSize)

			start := time.Now()
			ctx, cancel := runner.NewCtxTimeout(5 * time.Second)
			resp, err := cli.Put(ctx, key, value, clientv3.WithPrevKV())
			latencyMs := float64(time.Since(start).Milliseconds())

			if err != nil {
				metrics.RecordFailure(latencyMs, err.Error())
				logutil.S().Debugw("put failed", "scenario", HighContention.String(), "worker", workerID, "error", err)
			} else {
				metrics.RecordSuccess(latencyMs)
				metrics.RecordBytesWritten(int64(len(key) + len(value)))
				if resp != nil && resp.PrevKv != nil {
					metrics.RecordBytesRead(int64(len(resp.PrevKv.Value)))
				}
			}
			cancel()

			sleepUntil(deadline, perWorkerInterval)
		}
	})

	// High contention on shared keys causes Raft serialization delays; each retry
	// adds a WireGuard RTT, pushing P99 higher than local etcd.
	stats := finalizeScenario(result, metrics, errors, 0.80, 3000)
	if result.Success {
		result.Output = fmt.Sprintf("high contention success %.2f%%, p99 %.0fms", stats.SuccessRate()*100, stats.P99LatencyMs)
	}

	logutil.S().Infow("scenario completed",
		"scenario", HighContention.String(),
		"total_requests", result.TotalRequests,
		"success_rate", fmt.Sprintf("%.2f%%", stats.SuccessRate()*100),
		"p99_latency", result.P99Latency.Milliseconds(),
	)
}

package scenarios

import (
	"fmt"
	"time"

	testtime "git.tbd/etcd-infra/internal/etcd/testtime"

	logutil "git.tbd/etcd-infra/pkg/log"
	"git.tbd/etcd-infra/pkg/randutil"
)

// RunBurstWrites Controllers performing resyncs push short bursts of GuaranteedUpdate traffic through staging/src/k8s.io/apiserver/pkg/storage/etcd3/store.go; this workload reproduces that write spikiness.
func RunBurstWrites(runner StressRunner) {
	logutil.S().Infow("running", "scenario", BurstWrites.String())

	result := &Result{
		Scenario:  BurstWrites.String(),
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

	duration := scenarioDuration(cfg)
	workers := workerCount(cfg)
	start := time.Now()
	deadline := start.Add(duration)

	burstDuration := 5 * time.Second
	idleDuration := 3 * time.Second
	if burstDuration >= duration {
		burstDuration = duration / 2
	}
	if burstDuration <= 0 {
		burstDuration = 2 * time.Second
	}
	if idleDuration <= 0 {
		idleDuration = time.Second
	}
	cycle := burstDuration + idleDuration

	targetRPS := cfg.RequestsPerSecond
	if targetRPS <= 0 {
		targetRPS = workers * 20
	} else {
		targetRPS = int(float64(targetRPS) * 1.5)
	}
	perWorkerInterval := computePerWorkerInterval(targetRPS, workers)

	valueBytes := valueSize(cfg, 256)

	errors := runWorkers(workers, func(workerID int, _ chan<- error) {
		for {
			now := time.Now()
			if now.After(deadline) {
				return
			}

			phase := time.Since(start) % cycle
			if phase >= burstDuration {
				sleep := cycle - phase
				if sleep > 0 {
					if remaining := deadline.Sub(now); sleep > remaining {
						sleep = remaining
					}
					time.Sleep(sleep)
				}

				continue
			}

			key := runner.GenerateRandomKey(keySize(cfg, 10))
			value := randutil.StringAlphabetsLowerCase(valueBytes)
			_ = performPutWithMetrics(runner, cli, metrics, key, value, workerID)

			sleepUntil(deadline, perWorkerInterval)
		}
	})

	// WireGuard overlay adds ~1-3ms RTT per hop; burst writes amplify tail latency
	// because all workers fire simultaneously during the burst phase.
	stats := finalizeScenario(result, metrics, errors, 0.85, 2500)
	if result.Success {
		result.Output = fmt.Sprintf("burst writes maintained %.2f%% success, p99 %.0fms", stats.SuccessRate()*100, stats.P99LatencyMs)
	}

	logutil.S().Infow("scenario completed",
		"scenario", BurstWrites.String(),
		"total_requests", result.TotalRequests,
		"success_rate", fmt.Sprintf("%.2f%%", stats.SuccessRate()*100),
		"p99_latency", result.P99Latency.Milliseconds(),
	)
}

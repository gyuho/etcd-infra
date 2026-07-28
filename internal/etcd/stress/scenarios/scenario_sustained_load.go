package scenarios

import (
	"fmt"

	testtime "git.tbd/etcd-infra/internal/etcd/testtime"

	logutil "git.tbd/etcd-infra/pkg/log"
)

// RunSustainedLoad kube-apiserver maintains a constant stream of GuaranteedUpdate writes via staging/src/k8s.io/apiserver/pkg/storage/etcd3/store.go; this workload keeps steady pressure to mirror that production pattern.
func RunSustainedLoad(runner StressRunner) {
	logutil.S().Infow("running", "scenario", SustainedLoad.String())

	result := &Result{
		Scenario:  SustainedLoad.String(),
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
	cfg := runner.GetConfig()

	rps := cfg.RequestsPerSecond
	if rps <= 0 {
		rps = workerCount(cfg) * 25
	}

	generator := NewLoadGeneratorWithSizes(
		cfg.DurationSeconds,
		rps,
		cfg.KeySizeBytes,
		cfg.ValueSizeBytes,
	)

	errors := runPutWorkerPool(runner, cli, metrics, generator, workerCount(cfg))
	// Sustained load over WireGuard accumulates latency variance as the overlay
	// processes a continuous stream of encrypted packets.
	stats := finalizeScenario(result, metrics, errors, 0.80, 4000)
	if result.Success {
		result.Output = fmt.Sprintf("sustained load success %.2f%%, p99 %.0fms", stats.SuccessRate()*100, stats.P99LatencyMs)
	}

	logutil.S().Infow("scenario completed",
		"scenario", SustainedLoad.String(),
		"total_requests", result.TotalRequests,
		"success_rate", fmt.Sprintf("%.2f%%", stats.SuccessRate()*100),
		"p99_latency", result.P99Latency.Milliseconds(),
	)
}

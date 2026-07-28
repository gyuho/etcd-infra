package scenarios

import (
	"fmt"
	"sync/atomic"
	"time"

	testtime "git.tbd/etcd-infra/internal/etcd/testtime"
	clientv3 "go.etcd.io/etcd/client/v3"

	logutil "git.tbd/etcd-infra/pkg/log"
	"git.tbd/etcd-infra/pkg/randutil"
)

// RunNamespaceIsolationHeavy staging/src/k8s.io/apiserver/pkg/storage/etcd3/store.go prefix-based isolation Kubernetes uses etcd prefixes for namespace isolation in multi-tenant clusters. This test stresses: many namespaces with concurrent isolated operations.
func RunNamespaceIsolationHeavy(runner StressRunner) {
	logutil.S().Infow("running", "scenario", NamespaceIsolationHeavy.String())

	result := &Result{
		Scenario:  NamespaceIsolationHeavy.String(),
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
	metrics := runner.GetMetricsCollector()
	metrics.Reset()

	// Simulate many namespaces (multi-tenant cluster)
	namespaceCount := 50
	basePrefix := runner.GenerateRandomKey(keySize(cfg, 8))

	// Create namespace prefixes
	namespaces := make([]string, namespaceCount)
	for i := range namespaceCount {
		namespaces[i] = fmt.Sprintf("%s/ns-%03d/", basePrefix, i)
	}

	workers := workerCount(cfg)
	workers = max(workers, 8)

	duration := scenarioDuration(cfg)
	deadline := time.Now().Add(duration)
	perWorkerInterval := computePerWorkerInterval(cfg.RequestsPerSecond, workers)

	var opsPerNamespace [50]atomic.Int64

	errs := runWorkers(workers, func(workerID int, _ chan<- error) {
		for time.Now().Before(deadline) {
			// Each worker operates on random namespaces
			nsIdx := randutil.Intn(namespaceCount)
			nsPrefix := namespaces[nsIdx]

			// Mix of operations: creates, updates, reads, deletes
			op := randutil.Intn(100)

			//nolint:gocritic,nestif // branching intentionally weights namespace operations
			if op < 40 {
				// Create/Update (40%)
				key := fmt.Sprintf("%spod-%04d", nsPrefix, randutil.Intn(100))
				value := randutil.StringAlphabetsLowerCase(valueSize(cfg, 512))
				_ = performPutWithMetrics(runner, cli, metrics, key, value, workerID)
				opsPerNamespace[nsIdx].Add(1)
			} else if op < 70 {
				// Read (30%)
				key := fmt.Sprintf("%spod-%04d", nsPrefix, randutil.Intn(100))
				_ = performGetWithMetrics(runner, cli, metrics, key, workerID)
				opsPerNamespace[nsIdx].Add(1)
			} else if op < 85 {
				// List within namespace (15%)
				ctx, cancel := runner.NewCtxTimeout(5 * time.Second)
				start := time.Now()
				_, err := cli.Get(ctx, nsPrefix, clientv3.WithPrefix(), clientv3.WithLimit(20))
				cancel()

				latencyMs := float64(time.Since(start).Milliseconds())
				if err != nil {
					metrics.RecordFailure(latencyMs, err.Error())
				} else {
					metrics.RecordSuccess(latencyMs)
				}
				opsPerNamespace[nsIdx].Add(1)
			} else {
				// Delete (15%)
				key := fmt.Sprintf("%spod-%04d", nsPrefix, randutil.Intn(100))
				ctx, cancel := runner.NewCtxTimeout(5 * time.Second)
				start := time.Now()
				_, err := cli.Delete(ctx, key)
				cancel()

				latencyMs := float64(time.Since(start).Milliseconds())
				if err != nil {
					metrics.RecordFailure(latencyMs, err.Error())
				} else {
					metrics.RecordSuccess(latencyMs)
				}
				opsPerNamespace[nsIdx].Add(1)
			}

			sleepUntil(deadline, perWorkerInterval)
		}
	})

	// Cross-prefix operations on 50 namespaces spread load across etcd's MVCC
	// keyspace; WireGuard adds per-operation overhead to every request.
	stats := finalizeScenario(result, metrics, errs, 0.85, 4000)
	if !result.Success {
		return
	}

	// Calculate namespace utilization stats
	var minOps, maxOps, totalOps int64
	minOps = opsPerNamespace[0].Load()
	for i := range namespaceCount {
		ops := opsPerNamespace[i].Load()
		totalOps += ops
		if ops < minOps {
			minOps = ops
		}
		if ops > maxOps {
			maxOps = ops
		}
	}

	avgOps := float64(totalOps) / float64(namespaceCount)
	result.Output = fmt.Sprintf(
		"namespace isolation: %d namespaces, %.0f ops/ns (min:%d, max:%d); success %.2f%%, p99 %.0fms",
		namespaceCount,
		avgOps,
		minOps,
		maxOps,
		stats.SuccessRate()*100,
		stats.P99LatencyMs,
	)
}

package scenarios

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	testtime "git.tbd/etcd-infra/internal/etcd/testtime"
	clientv3 "go.etcd.io/etcd/client/v3"

	logutil "git.tbd/etcd-infra/pkg/log"
	"git.tbd/etcd-infra/pkg/randutil"
)

// RunCompactDuringLoad stresses background compaction during active read/write workload from staging/src/k8s.io/apiserver/pkg/storage/etcd3/compact.go (Production Kubernetes clusters run compaction every 5 minutes while serving traffic).
//
//nolint:gocyclo // scenario exercises mixed operations during compaction
func RunCompactDuringLoad(runner StressRunner) {
	logutil.S().Infow("running", "scenario", CompactDuringLoad.String())

	result := &Result{
		Scenario:  CompactDuringLoad.String(),
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

	prefix := runner.GenerateRandomKey(keySize(cfg, 8))
	workers := workerCount(cfg)
	workers = max(workers, 4)

	duration := scenarioDuration(cfg)
	deadline := time.Now().Add(duration)
	perWorkerInterval := computePerWorkerInterval(cfg.RequestsPerSecond, workers)

	var compactionCount, compactionErrors atomic.Int64

	// Background compaction goroutine
	compactCtx, compactCancel := context.WithCancel(context.Background())
	compactDone := make(chan struct{})

	go func() {
		defer close(compactDone)

		// Compact every 10 seconds (more frequent than real K8s for stress testing)
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-compactCtx.Done():
				return
			case <-ticker.C:
				// Get current revision
				ctx, cancel := context.WithTimeout(compactCtx, 5*time.Second)
				statusResp, err := cli.Status(ctx, cli.Endpoints()[0])
				cancel()

				if err != nil {
					logutil.S().Debugw("status failed during compaction", "scenario", CompactDuringLoad.String(), "error", err)
					compactionErrors.Add(1)

					continue
				}

				currentRev := statusResp.Header.Revision

				// Compact to revision slightly behind current (keep some history)
				if currentRev > 100 {
					targetRev := currentRev - 50

					ctx, cancel := context.WithTimeout(compactCtx, 10*time.Second)
					_, err := cli.Compact(ctx, targetRev)
					cancel()

					if err != nil {
						logutil.S().Debugw("compaction failed", "scenario", CompactDuringLoad.String(),
							"target_rev", targetRev, "error", err)
						compactionErrors.Add(1)
					} else {
						compactionCount.Add(1)
						logutil.S().Debugw("compaction succeeded", "scenario", CompactDuringLoad.String(),
							"target_rev", targetRev)
					}
				}
			}
		}
	}()

	// Mixed workload: reads and writes during compaction
	keyCount := 100
	keys := make([]string, keyCount)
	for i := range keyCount {
		keys[i] = fmt.Sprintf("%s/key-%04d", prefix, i)
	}

	// Seed initial data
	for _, key := range keys {
		ctx, cancel := runner.NewCtxTimeout(5 * time.Second)
		_, _ = cli.Put(ctx, key, randutil.StringAlphabetsLowerCase(valueSize(cfg, 256)))
		cancel()
	}

	errs := runWorkers(workers, func(workerID int, _ chan<- error) {
		for time.Now().Before(deadline) {
			key := keys[randutil.Intn(len(keys))]

			// Mix of operations: 50% writes, 30% reads, 20% historical reads
			op := randutil.Intn(10)

			//nolint:gocritic,nestif // branching here is intentional for operation mix distribution
			if op < 5 {
				// Write
				value := randutil.StringAlphabetsLowerCase(valueSize(cfg, 256))
				_ = performPutWithMetrics(runner, cli, metrics, key, value, workerID)
			} else if op < 8 {
				// Current read
				ctx, cancel := runner.NewCtxTimeout(5 * time.Second)
				start := time.Now()
				_, err := cli.Get(ctx, key)
				cancel()

				latencyMs := float64(time.Since(start).Milliseconds())

				if err != nil {
					metrics.RecordFailure(latencyMs, err.Error())
				} else {
					metrics.RecordSuccess(latencyMs)
				}
			} else {
				// Historical read (may hit compacted revision)
				ctx, cancel := runner.NewCtxTimeout(5 * time.Second)
				start := time.Now()
				statusResp, err := cli.Status(ctx, cli.Endpoints()[0])
				cancel()

				if err != nil {
					latencyMs := float64(time.Since(start).Milliseconds())
					metrics.RecordFailure(latencyMs, err.Error())
					sleepUntil(deadline, perWorkerInterval)

					continue
				}

				currentRev := statusResp.Header.Revision
				if currentRev > 200 {
					// Try to read from an old revision
					historicalRev := currentRev - int64(randutil.Intn(150)+50)

					ctx, cancel := runner.NewCtxTimeout(5 * time.Second)
					start := time.Now()
					_, err := cli.Get(ctx, key, clientv3.WithRev(historicalRev))
					cancel()

					latencyMs := float64(time.Since(start).Milliseconds())

					if err != nil {
						// Expected error if revision was compacted. Use Contains
						// because the raw gRPC error wraps the message as
						// "rpc error: code = OutOfRange desc = etcdserver: mvcc: ..."
						if strings.Contains(err.Error(), "mvcc: required revision has been compacted") {
							metrics.RecordSuccess(latencyMs) // Don't count as failure
						} else {
							metrics.RecordFailure(latencyMs, err.Error())
						}
					} else {
						metrics.RecordSuccess(latencyMs)
					}
				}
			}

			sleepUntil(deadline, perWorkerInterval)
		}
	})

	// Stop compaction
	compactCancel()
	<-compactDone

	// Compaction I/O competes with WireGuard tunnel processing; historical reads
	// at compacted revisions are expected and counted as success.
	stats := finalizeScenario(result, metrics, errs, 0.80, 5000)
	if !result.Success {
		return
	}

	if compactionCount.Load() == 0 {
		logutil.S().Warnw("no compactions completed", "scenario", CompactDuringLoad.String())
	}

	result.Output = fmt.Sprintf(
		"workload with %d compactions (%d errors); success %.2f%%, p99 %.0fms",
		compactionCount.Load(),
		compactionErrors.Load(),
		stats.SuccessRate()*100,
		stats.P99LatencyMs,
	)
}

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

// RunOptimisticConcurrencyTxn staging/src/k8s.io/apiserver/pkg/storage/etcd3/store.go "GuaranteedUpdate" Kubernetes uses ModRevision-based optimistic concurrency for all resource updates. This test stresses: read-modify-write with retry loops under high contention.
func RunOptimisticConcurrencyTxn(runner StressRunner) {
	logutil.S().Infow("running", "scenario", OptimisticConcurrencyTxn.String())

	result := &Result{
		Scenario:  OptimisticConcurrencyTxn.String(),
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

	// Create hotspot keys that multiple workers will contend on
	hotspotCount := 20
	prefix := runner.GenerateRandomKey(keySize(cfg, 8))
	hotspotKeys := make([]string, hotspotCount)
	for i := range hotspotCount {
		hotspotKeys[i] = fmt.Sprintf("%s/resource-%02d", prefix, i)

		// Seed initial value
		ctx, cancel := runner.NewCtxTimeout(5 * time.Second)
		_, err := cli.Put(ctx, hotspotKeys[i], "0")
		cancel()
		if err != nil {
			logutil.S().Warnw("seed put failed", "scenario", OptimisticConcurrencyTxn.String(),
				"key", hotspotKeys[i], "error", err)
		}
	}

	workers := workerCount(cfg)
	workers = max(workers, 4)

	duration := scenarioDuration(cfg)
	deadline := time.Now().Add(duration)

	var totalAttempts, successfulUpdates, retriesNeeded, conflictsDetected atomic.Int64
	perWorkerInterval := computePerWorkerInterval(cfg.RequestsPerSecond, workers)

	errs := runWorkers(workers, func(_ int, _ chan<- error) {
		for time.Now().Before(deadline) {
			// Select a hotspot key
			key := hotspotKeys[randutil.Intn(len(hotspotKeys))]

			// Optimistic concurrency loop (similar to GuaranteedUpdate)
			const maxRetries = 10
			var succeeded bool
			var readLatencyMs float64

			for attempt := 0; attempt < maxRetries && time.Now().Before(deadline); attempt++ {
				totalAttempts.Add(1)

				if attempt > 0 {
					retriesNeeded.Add(1)
					// Backoff before retry
					time.Sleep(time.Duration(randutil.Intn(10)+1) * time.Millisecond)
				}

				// Read current value and mod_revision
				ctx, cancel := runner.NewCtxTimeout(5 * time.Second)
				startRead := time.Now()
				getResp, err := cli.Get(ctx, key)
				cancel()

				readLatencyMs = float64(time.Since(startRead).Milliseconds())

				if err != nil {
					metrics.RecordFailure(readLatencyMs, err.Error())

					break
				}

				if len(getResp.Kvs) == 0 {
					// Key doesn't exist, create it
					createCtx, createCancel := runner.NewCtxTimeout(5 * time.Second)
					startCreate := time.Now()
					txnResp, txnErr := cli.Txn(createCtx).
						If(clientv3.Compare(clientv3.Version(key), "=", 0)).
						Then(clientv3.OpPut(key, "1")).
						Commit()
					createCancel()

					txnLatencyMs := float64(time.Since(startCreate).Milliseconds())
					totalLatencyMs := readLatencyMs + txnLatencyMs

					if txnErr != nil {
						metrics.RecordFailure(totalLatencyMs, txnErr.Error())

						break
					}

					if txnResp.Succeeded {
						successfulUpdates.Add(1)
						metrics.RecordSuccess(totalLatencyMs)
						succeeded = true

						break
					}

					// Conflict on create, retry
					conflictsDetected.Add(1)

					continue
				}

				// Update existing value
				kv := getResp.Kvs[0]
				currentModRev := kv.ModRevision

				// Simulate modification (increment counter)
				newValue := randutil.StringAlphabetsLowerCase(valueSize(cfg, 256))

				// Transaction: check mod_revision hasn't changed, then update
				ctx, cancel = runner.NewCtxTimeout(5 * time.Second)
				startTxn := time.Now()
				txnResp, err := cli.Txn(ctx).
					If(clientv3.Compare(clientv3.ModRevision(key), "=", currentModRev)).
					Then(clientv3.OpPut(key, newValue)).
					Else(clientv3.OpGet(key)).
					Commit()
				cancel()

				txnLatencyMs := float64(time.Since(startTxn).Milliseconds())
				totalLatencyMs := readLatencyMs + txnLatencyMs

				if err != nil {
					metrics.RecordFailure(totalLatencyMs, err.Error())

					break
				}

				if txnResp.Succeeded {
					successfulUpdates.Add(1)
					metrics.RecordSuccess(totalLatencyMs)
					succeeded = true

					break
				}

				// Conflict detected, retry
				conflictsDetected.Add(1)
			}

			if !succeeded {
				// Failed after retries - record as failure
				metrics.RecordFailure(readLatencyMs, "max retries exceeded")
			}

			sleepUntil(deadline, perWorkerInterval)
		}
	})

	// Optimistic read-modify-write requires Get+Txn (two round-trips over
	// WireGuard); retries on conflict add further RTTs.
	stats := finalizeScenario(result, metrics, errs, 0.80, 5000)
	if !result.Success {
		return
	}

	if successfulUpdates.Load() == 0 {
		result.Success = false
		result.Output = "no successful updates"

		return
	}

	avgRetriesPerUpdate := float64(retriesNeeded.Load()) / float64(successfulUpdates.Load())
	conflictRate := float64(conflictsDetected.Load()) / float64(totalAttempts.Load()) * 100

	result.Output = fmt.Sprintf(
		"optimistic txn: %d updates, %.2f retries/update, %.1f%% conflicts; success %.2f%%, p99 %.0fms",
		successfulUpdates.Load(),
		avgRetriesPerUpdate,
		conflictRate,
		stats.SuccessRate()*100,
		stats.P99LatencyMs,
	)
}

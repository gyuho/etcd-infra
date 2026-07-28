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

// RunTxnMultiKeyUpdate staging/src/k8s.io/apiserver/pkg/storage/etcd3/store.go atomic multi-key updates Kubernetes uses transactions to atomically update related keys (e.g., Pod + Pod/status). This test stresses: multi-key transactions under high concurrency.
func RunTxnMultiKeyUpdate(runner StressRunner) {
	logutil.S().Infow("running", "scenario", TxnMultiKeyUpdate.String())

	result := &Result{
		Scenario:  TxnMultiKeyUpdate.String(),
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

	// Seed some base resources
	resourceCount := 50
	for i := range resourceCount {
		baseKey := fmt.Sprintf("%s/pod-%04d", prefix, i)
		statusKey := fmt.Sprintf("%s/pod-%04d/status", prefix, i)

		ctx, cancel := runner.NewCtxTimeout(5 * time.Second)
		_, _ = cli.Put(ctx, baseKey, "spec-v0")
		_, _ = cli.Put(ctx, statusKey, "running")
		cancel()
	}

	workers := workerCount(cfg)
	workers = max(workers, 4)

	duration := scenarioDuration(cfg)
	deadline := time.Now().Add(duration)
	perWorkerInterval := computePerWorkerInterval(cfg.RequestsPerSecond, workers)

	var multiKeyTxns, singleKeyTxns, txnSuccesses, txnConflicts atomic.Int64

	errs := runWorkers(workers, func(_ int, _ chan<- error) {
		for time.Now().Before(deadline) {
			resourceID := randutil.Intn(resourceCount)
			baseKey := fmt.Sprintf("%s/pod-%04d", prefix, resourceID)
			statusKey := fmt.Sprintf("%s/pod-%04d/status", prefix, resourceID)

			// Decide operation type
			op := randutil.Intn(100)

			//nolint:gocritic,nestif // branching intentionally weights multi-key vs single-key txns
			if op < 60 {
				// Multi-key atomic update (60%): update both base and status using optimistic compare
				specValue := randutil.StringAlphabetsLowerCase(valueSize(cfg, 256))
				statusValue := randutil.StringAlphabetsLowerCase(valueSize(cfg, 128))

				ctx, cancel := runner.NewCtxTimeout(5 * time.Second)
				getResp, err := cli.Get(ctx, baseKey)
				cancel()
				if err != nil {
					metrics.RecordFailure(0, err.Error())
					sleepUntil(deadline, perWorkerInterval)

					continue
				}

				start := time.Now()
				ctx, cancel = runner.NewCtxTimeout(5 * time.Second)

				var txnResp *clientv3.TxnResponse
				if len(getResp.Kvs) == 0 {
					// Key was removed; recreate both keys atomically if still absent
					txnResp, err = cli.Txn(ctx).
						If(
							clientv3.Compare(clientv3.CreateRevision(baseKey), "=", 0),
							clientv3.Compare(clientv3.CreateRevision(statusKey), "=", 0),
						).
						Then(
							clientv3.OpPut(baseKey, specValue),
							clientv3.OpPut(statusKey, statusValue),
						).
						Commit()
				} else {
					currentModRev := getResp.Kvs[0].ModRevision
					txnResp, err = cli.Txn(ctx).
						If(clientv3.Compare(clientv3.ModRevision(baseKey), "=", currentModRev)).
						Then(
							clientv3.OpPut(baseKey, specValue),
							clientv3.OpPut(statusKey, statusValue),
						).
						Else(clientv3.OpGet(baseKey)).
						Commit()
				}
				cancel()

				latencyMs := float64(time.Since(start).Milliseconds())

				if err != nil {
					metrics.RecordFailure(latencyMs, err.Error())
				} else if txnResp.Succeeded {
					multiKeyTxns.Add(1)
					txnSuccesses.Add(1)
					metrics.RecordSuccess(latencyMs)
				} else {
					txnConflicts.Add(1)
					metrics.RecordSuccess(latencyMs)
				}
			} else if op < 90 {
				// Single-key conditional update (30%): update with ModRevision check
				ctx, cancel := runner.NewCtxTimeout(5 * time.Second)
				getResp, err := cli.Get(ctx, baseKey)
				cancel()

				if err != nil || len(getResp.Kvs) == 0 {
					sleepUntil(deadline, perWorkerInterval)

					continue
				}

				currentModRev := getResp.Kvs[0].ModRevision
				newValue := randutil.StringAlphabetsLowerCase(valueSize(cfg, 256))

				ctx, cancel = runner.NewCtxTimeout(5 * time.Second)
				start := time.Now()

				txnResp, err := cli.Txn(ctx).
					If(clientv3.Compare(clientv3.ModRevision(baseKey), "=", currentModRev)).
					Then(clientv3.OpPut(baseKey, newValue)).
					Else(clientv3.OpGet(baseKey)).
					Commit()
				cancel()

				latencyMs := float64(time.Since(start).Milliseconds())

				if err != nil {
					metrics.RecordFailure(latencyMs, err.Error())
				} else if txnResp.Succeeded {
					singleKeyTxns.Add(1)
					txnSuccesses.Add(1)
					metrics.RecordSuccess(latencyMs)
				} else {
					txnConflicts.Add(1)
					metrics.RecordSuccess(latencyMs) // Conflict is expected, not an error
				}
			} else {
				// Multi-key delete (10%): delete both base and status atomically
				ctx, cancel := runner.NewCtxTimeout(5 * time.Second)
				start := time.Now()

				txnResp, err := cli.Txn(ctx).
					Then(
						clientv3.OpDelete(baseKey),
						clientv3.OpDelete(statusKey),
					).
					Commit()
				cancel()

				latencyMs := float64(time.Since(start).Milliseconds())

				if err != nil {
					metrics.RecordFailure(latencyMs, err.Error())
				} else if txnResp.Succeeded {
					multiKeyTxns.Add(1)
					txnSuccesses.Add(1)
					metrics.RecordSuccess(latencyMs)
				}
			}

			sleepUntil(deadline, perWorkerInterval)
		}
	})

	// Multi-key transactions involve Get + Txn(multi-op); compound latency over
	// WireGuard; conflict retries add further overlay round-trips.
	stats := finalizeScenario(result, metrics, errs, 0.80, 4000)
	if !result.Success {
		return
	}

	totalTxns := multiKeyTxns.Load() + singleKeyTxns.Load()
	if totalTxns == 0 {
		result.Success = false
		result.Output = "no transactions completed"

		return
	}

	conflictRate := float64(txnConflicts.Load()) / float64(totalTxns+txnConflicts.Load()) * 100

	result.Output = fmt.Sprintf(
		"multi-key txn: %d multi-key, %d single-key, %.1f%% conflicts; success %.2f%%, p99 %.0fms",
		multiKeyTxns.Load(),
		singleKeyTxns.Load(),
		conflictRate,
		stats.SuccessRate()*100,
		stats.P99LatencyMs,
	)
}

package scenarios

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	testtime "git.tbd/etcd-infra/internal/etcd/testtime"
	clientv3 "go.etcd.io/etcd/client/v3"

	logutil "git.tbd/etcd-infra/pkg/log"
	"git.tbd/etcd-infra/pkg/randutil"
)

// RunWatchWithChurn Pod lifecycle controllers continuously create and tear down watches via staging/src/k8s.io/apiserver/pkg/storage/etcd3/watcher.go; this workload mimics that churn with aggressive watch create/cancel cycles and concurrent events.
func RunWatchWithChurn(runner StressRunner) {
	logutil.S().Infow("running", "scenario", WatchWithChurn.String())

	result := &Result{
		Scenario:  WatchWithChurn.String(),
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
	duration := scenarioDuration(cfg)
	deadline := time.Now().Add(duration)

	var watchCreated, watchCanceled, eventsReceived, watchErrors atomic.Int64

	// Generate continuous background updates
	updateCtx, updateCancel := context.WithCancel(context.Background())
	var updateWg sync.WaitGroup

	updateWorkers := 2
	for range updateWorkers {
		updateWg.Go(func() {
			for {
				select {
				case <-updateCtx.Done():
					return
				default:
					key := fmt.Sprintf("%s/obj-%04d", prefix, randutil.Intn(50))
					value := randutil.StringAlphabetsLowerCase(valueSize(cfg, 256))

					//nolint:contextcheck // timeout context for short-lived operation within goroutine
					ctx, cancel := runner.NewCtxTimeout(5 * time.Second)
					_, _ = cli.Put(ctx, key, value)
					cancel()

					time.Sleep(50 * time.Millisecond)
				}
			}
		})
	}

	// Churn workers: continuously create and cancel watches
	workers := workerCount(cfg)
	workers = max(workers, 4)

	errs := runWorkers(workers, func(workerID int, _ chan<- error) {
		for time.Now().Before(deadline) {
			// Create a watch
			watchCtx, watchCancel := context.WithCancel(context.Background())
			watcher := clientv3.NewWatcher(cli)

			watchCreated.Add(1)

			ch := watcher.Watch(
				watchCtx,
				prefix,
				clientv3.WithPrefix(),
			)

			// Keep watch alive for a short time (simulate pod lifecycle)
			watchLifetime := time.Duration(randutil.Intn(2000)+500) * time.Millisecond
			watchDeadline := time.Now().Add(watchLifetime)

			// Consume events from this watch
			eventLoop := true
			for eventLoop && time.Now().Before(watchDeadline) {
				select {
				case <-time.After(watchLifetime):
					eventLoop = false
				case resp, ok := <-ch:
					if !ok {
						eventLoop = false

						break
					}
					if resp.Err() != nil {
						watchErrors.Add(1)
						logutil.S().Debugw("watch error during churn", "scenario", WatchWithChurn.String(),
							"worker", workerID, "error", resp.Err())
						eventLoop = false

						break
					}
					if len(resp.Events) > 0 {
						eventsReceived.Add(int64(len(resp.Events)))
					}
				}
			}

			// Cancel watch and close watcher
			watchCancel()
			watchCanceled.Add(1)

			if err := watcher.Close(); err != nil {
				logutil.S().Debugw("watcher close error", "scenario", WatchWithChurn.String(),
					"worker", workerID, "error", err)
			}

			// Brief pause before creating next watch
			time.Sleep(time.Duration(randutil.Intn(100)+50) * time.Millisecond)

			// Track metrics for watch lifecycle (create/cancel counts as success)
			watchLatencyMs := float64(watchLifetime.Milliseconds())
			metrics.RecordSuccess(watchLatencyMs)

			if time.Now().After(deadline) {
				break
			}
		}
	})

	// Stop background updates
	updateCancel()
	updateWg.Wait()

	// Watch create/cancel cycles over WireGuard; aggressive churn amplifies
	// connection setup/teardown overhead on the encrypted tunnel.
	stats := finalizeScenario(result, metrics, errs, 0.90, 5000)
	if !result.Success {
		return
	}

	if watchCreated.Load() == 0 {
		result.Success = false
		result.Output = "no watches created"

		return
	}

	if watchCreated.Load() != watchCanceled.Load() {
		logutil.S().Warnw("watch create/cancel mismatch", "scenario", WatchWithChurn.String(),
			"created", watchCreated.Load(), "canceled", watchCanceled.Load())
	}

	if watchErrors.Load() > int64(float64(watchCreated.Load())*0.05) {
		result.Success = false
		result.Output = fmt.Sprintf("too many watch errors: %d (%.1f%%)",
			watchErrors.Load(),
			float64(watchErrors.Load())/float64(watchCreated.Load())*100)

		return
	}

	avgEventsPerWatch := float64(eventsReceived.Load()) / float64(watchCreated.Load())

	result.Output = fmt.Sprintf(
		"watch churn: %d created, %d canceled, %d events (%.1f/watch), %d errors; success %.2f%%, avg %.0fms",
		watchCreated.Load(),
		watchCanceled.Load(),
		eventsReceived.Load(),
		avgEventsPerWatch,
		watchErrors.Load(),
		stats.SuccessRate()*100,
		stats.P99LatencyMs,
	)
}

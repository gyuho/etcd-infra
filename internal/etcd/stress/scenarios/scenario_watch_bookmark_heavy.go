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

// RunWatchBookmarkHeavy staging/src/k8s.io/apiserver/pkg/storage/etcd3/watcher.go bookmarks Kubernetes informers use bookmarks (progress notifications) to track watch progress without transferring data. This test stresses: many watches with frequent bookmark requests.
func RunWatchBookmarkHeavy(runner StressRunner) {
	logutil.S().Infow("running", "scenario", WatchBookmarkHeavy.String())

	result := &Result{
		Scenario:  WatchBookmarkHeavy.String(),
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

	// Create multiple watch prefixes (simulating different informers)
	watchCount := 8
	prefix := runner.GenerateRandomKey(keySize(cfg, 8))
	watchPrefixes := make([]string, watchCount)
	for i := range watchCount {
		watchPrefixes[i] = fmt.Sprintf("%s/type-%d/", prefix, i)
	}

	duration := scenarioDuration(cfg)
	deadline := time.Now().Add(duration)

	var bookmarksReceived, eventsReceived, progressRequests, watchErrors atomic.Int64

	watchCtx, watchCancel := context.WithCancel(context.Background())
	var watchWg sync.WaitGroup

	// Start watchers with bookmark/progress notify enabled
	for i, watchPrefix := range watchPrefixes {
		watchWg.Add(1)
		go func(watchID int, prefix string) {
			defer watchWg.Done()

			watcher := clientv3.NewWatcher(cli)
			defer func() {
				if err := watcher.Close(); err != nil {
					logutil.S().Debugw("watcher close error", "scenario", WatchBookmarkHeavy.String(),
						"watch", watchID, "error", err)
				}
			}()

			ch := watcher.Watch(
				watchCtx,
				prefix,
				clientv3.WithPrefix(),
				clientv3.WithProgressNotify(),
			)

			// Immediately request progress
			if err := watcher.RequestProgress(watchCtx); err != nil {
				logutil.S().Debugw("initial progress request failed", "scenario", WatchBookmarkHeavy.String(),
					"watch", watchID, "error", err)
			} else {
				progressRequests.Add(1)
			}

			// Periodically request progress (simulating informer catch-up)
			progressTicker := time.NewTicker(3 * time.Second)
			defer progressTicker.Stop()

			for {
				select {
				case <-watchCtx.Done():
					return

				case <-progressTicker.C:
					if err := watcher.RequestProgress(watchCtx); err != nil {
						logutil.S().Debugw("progress request failed", "scenario", WatchBookmarkHeavy.String(),
							"watch", watchID, "error", err)
					} else {
						progressRequests.Add(1)
					}

				case resp, ok := <-ch:
					if !ok {
						return
					}

					if resp.Err() != nil {
						watchErrors.Add(1)
						logutil.S().Debugw("watch error", "scenario", WatchBookmarkHeavy.String(),
							"watch", watchID, "error", resp.Err())

						return
					}

					if resp.IsProgressNotify() {
						bookmarksReceived.Add(1)
					} else if len(resp.Events) > 0 {
						eventsReceived.Add(int64(len(resp.Events)))
					}
				}
			}
		}(i, watchPrefix)
	}

	// Wait for watchers to start
	time.Sleep(500 * time.Millisecond)

	// Generate workload: writes to all prefixes
	workers := workerCount(cfg)
	workers = max(workers, 2)

	perWorkerInterval := computePerWorkerInterval(cfg.RequestsPerSecond, workers)

	errs := runWorkers(workers, func(workerID int, _ chan<- error) {
		for time.Now().Before(deadline) {
			// Write to random prefix
			prefix := watchPrefixes[randutil.Intn(len(watchPrefixes))]
			key := fmt.Sprintf("%sobj-%04d", prefix, randutil.Intn(50))
			value := randutil.StringAlphabetsLowerCase(valueSize(cfg, 256))

			_ = performPutWithMetrics(runner, cli, metrics, key, value, workerID)
			sleepUntil(deadline, perWorkerInterval)
		}
	})

	// Stop watchers
	watchCancel()
	watchWg.Wait()

	// Progress notify requests and bookmark responses each cross the WireGuard
	// tunnel; 8 concurrent watch streams compound the overlay overhead.
	stats := finalizeScenario(result, metrics, errs, 0.85, 3000)
	if !result.Success {
		return
	}

	if bookmarksReceived.Load() == 0 {
		result.Success = false
		result.Output = "no bookmarks/progress notifications received"

		return
	}

	if watchErrors.Load() > 0 {
		logutil.S().Warnw("watch errors encountered", "scenario", WatchBookmarkHeavy.String(),
			"errors", watchErrors.Load())
	}

	avgBookmarksPerWatch := float64(bookmarksReceived.Load()) / float64(watchCount)
	avgEventsPerWatch := float64(eventsReceived.Load()) / float64(watchCount)

	result.Output = fmt.Sprintf(
		"bookmark-heavy: %d watches, %d bookmarks (%.0f/watch), %d events (%.0f/watch), %d progress requests; success %.2f%%, p99 %.0fms",
		watchCount,
		bookmarksReceived.Load(),
		avgBookmarksPerWatch,
		eventsReceived.Load(),
		avgEventsPerWatch,
		progressRequests.Load(),
		stats.SuccessRate()*100,
		stats.P99LatencyMs,
	)
}

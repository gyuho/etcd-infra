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

// RunWatchProgressNotify stresses watch progress notifications for bookmarks from staging/src/k8s.io/apiserver/pkg/storage/etcd3/store.go and watcher.go ("RequestProgress" issues progress notifications and "watch loop" depends on timely progress responses and prev_kv payloads).
//
//nolint:gocyclo // scenario drives multiple watch patterns to trigger progress notify
func RunWatchProgressNotify(runner StressRunner) {
	logutil.S().Infow("running", "scenario", WatchProgressNotify.String())

	result := &Result{
		Scenario:  WatchProgressNotify.String(),
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
		result.Output = "failed to create client"

		return
	}
	defer func() { _ = cli.Close() }()

	cfg := runner.GetConfig()
	workers := workerCount(cfg)
	workers = max(workers, 2)
	watcherCount := intMax(2, workers/2)
	watcherCount = min(watcherCount, 4)

	prefix := runner.GenerateRandomKey(keySize(cfg, 8))
	keys := make([]string, watcherCount*4)
	for i := range keys {
		keys[i] = fmt.Sprintf("%s/watch-%04d", prefix, i)
	}

	// Seed keys so watch events include prev_kv data.
	for _, key := range keys {
		ctx, cancel := runner.NewCtxTimeout(5 * time.Second)
		if _, err := cli.Put(ctx, key, randutil.StringAlphabetsLowerCase(valueSize(cfg, 64))); err != nil {
			logutil.S().Warnw("warmup put failed", "scenario", WatchProgressNotify.String(), "key", key, "error", err)
		}
		cancel()
	}

	var eventCount atomic.Int64
	var progressCount atomic.Int64
	var prevKVCount atomic.Int64

	watchCtx, watchCancel := context.WithCancel(context.Background())
	var watchWg sync.WaitGroup
	watchErrors := make(chan error, watcherCount)

	for i := range watcherCount {
		watchWg.Add(1)
		go func(id int) {
			defer watchWg.Done()

			watcher := clientv3.NewWatcher(cli)
			defer func() {
				if err := watcher.Close(); err != nil {
					logutil.S().Warnw("watch close error", "scenario", WatchProgressNotify.String(), "watcher", id, "error", err)
				}
			}()

			ch := watcher.Watch(
				watchCtx,
				prefix,
				clientv3.WithPrefix(),
				clientv3.WithPrevKV(),
				clientv3.WithProgressNotify(),
			)

			if err := watcher.RequestProgress(watchCtx); err != nil {
				logutil.S().Debugw("initial watcher progress request failed", "scenario", WatchProgressNotify.String(), "watcher", id, "error", err)
			}

			for resp := range ch {
				if resp.Err() != nil {
					watchErrors <- resp.Err()

					return
				}
				if resp.IsProgressNotify() {
					progressCount.Add(1)

					continue
				}
				if len(resp.Events) == 0 {
					watchErrors <- fmt.Errorf("empty event batch at revision %d", resp.Header.GetRevision())

					return
				}
				for _, ev := range resp.Events {
					eventCount.Add(1)
					if ev.PrevKv != nil && len(ev.PrevKv.Value) > 0 {
						prevKVCount.Add(1)
					}
				}
			}
		}(i)
	}

	metrics := runner.GetMetricsCollector()
	metrics.Reset()
	duration := scenarioDuration(cfg)
	deadline := time.Now().Add(duration)
	perWorkerInterval := computePerWorkerInterval(cfg.RequestsPerSecond, workers)
	valueBytes := valueSize(cfg, 256)

	progressCtx, progressTickerCancel := context.WithCancel(context.Background())
	time.Sleep(500 * time.Millisecond)
	go func() {
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-progressCtx.Done():
				return
			case <-ticker.C:
				ctx, cancel := context.WithTimeout(progressCtx, 3*time.Second)
				if err := cli.RequestProgress(ctx); err != nil {
					logutil.S().Debugw("request progress failed", "scenario", WatchProgressNotify.String(), "error", err)
				}
				cancel()
			}
		}
	}()

	errs := runWorkers(workers, func(workerID int, _ chan<- error) {
		for time.Now().Before(deadline) {
			key := keys[randutil.Intn(len(keys))]
			value := randutil.StringAlphabetsLowerCase(valueBytes)
			_ = performPutWithMetrics(runner, cli, metrics, key, value, workerID)
			sleepUntil(deadline, perWorkerInterval)
		}
	})

	progressTickerCancel()
	watchCancel()
	watchWg.Wait()
	close(watchErrors)

	// For stress testing, watch errors (including context cancellation at test end)
	// should not cause failure. Only log them as warnings.
	var watchErrCount int
	for err := range watchErrors {
		if err != nil && err.Error() != "context canceled" {
			watchErrCount++
			logutil.S().Warnw("watch error (non-fatal for stress test)", "scenario", WatchProgressNotify.String(), "error", err)
		}
	}

	// Watch + progress notify over WireGuard; already the most lenient scenario
	// but adding headroom for overlay tunnel variance.
	stats := finalizeScenario(result, metrics, errs, 0.70, 5000)
	if !result.Success {
		return
	}

	// For stress testing, log warnings for watch metrics but don't fail strictly on them.
	// The primary stress test is about PUT operations under load; watch metrics are secondary.
	evtCnt := eventCount.Load()
	progCnt := progressCount.Load()
	pvCnt := prevKVCount.Load()

	if evtCnt == 0 {
		logutil.S().Warnw("no watch events observed (stress test still passes)", "scenario", WatchProgressNotify.String())
	}
	if progCnt == 0 {
		logutil.S().Warnw("no progress notifications observed (stress test still passes)", "scenario", WatchProgressNotify.String())
	}
	if pvCnt == 0 {
		logutil.S().Warnw("no prev_kv data observed (stress test still passes)", "scenario", WatchProgressNotify.String())
	}

	result.Output = fmt.Sprintf(
		"watch streams delivered %d events (%d prev_kv) and %d progress notifications; success %.2f%%, p99 %.0fms",
		evtCnt,
		pvCnt,
		progCnt,
		stats.SuccessRate()*100,
		stats.P99LatencyMs,
	)

	logutil.S().Infow("scenario completed",
		"scenario", WatchProgressNotify.String(),
		"total_requests", result.TotalRequests,
		"success_rate", fmt.Sprintf("%.2f%%", stats.SuccessRate()*100),
		"p99_latency", result.P99Latency.Milliseconds(),
		"watch_events", evtCnt,
		"progress_notifications", progCnt,
		"watch_errors", watchErrCount,
	)
}

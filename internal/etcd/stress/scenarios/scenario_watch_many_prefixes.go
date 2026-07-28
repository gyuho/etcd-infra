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

// RunWatchManyPrefixes staging/src/k8s.io/apiserver/pkg/storage/etcd3/watcher.go Kubernetes controllers watch multiple resource types concurrently. This test stresses: many concurrent watches on different prefixes with continuous events.
func RunWatchManyPrefixes(runner StressRunner) {
	logutil.S().Infow("running", "scenario", WatchManyPrefixes.String())

	result := &Result{
		Scenario:  WatchManyPrefixes.String(),
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

	// Simulate multiple resource types (pods, services, deployments, etc.)
	resourceTypes := []string{"pods", "services", "deployments", "replicasets", "configmaps", "secrets"}
	basePrefix := runner.GenerateRandomKey(keySize(cfg, 8))

	// Create watch prefixes
	watchPrefixes := make([]string, len(resourceTypes))
	for i, rt := range resourceTypes {
		watchPrefixes[i] = fmt.Sprintf("%s/%s/", basePrefix, rt)
	}

	var eventCount, watchErrors atomic.Int64
	watchCtx, watchCancel := context.WithCancel(context.Background())
	var watchWg sync.WaitGroup

	// Start watch goroutines - one per resource type
	for i, prefix := range watchPrefixes {
		watchWg.Add(1)
		go func(watchID int, prefix string) {
			defer watchWg.Done()

			watcher := clientv3.NewWatcher(cli)
			defer func() {
				if err := watcher.Close(); err != nil {
					logutil.S().Debugw("watch close error", "scenario", WatchManyPrefixes.String(),
						"watch", watchID, "error", err)
				}
			}()

			ch := watcher.Watch(
				watchCtx,
				prefix,
				clientv3.WithPrefix(),
				clientv3.WithPrevKV(),
			)

			for {
				select {
				case <-watchCtx.Done():
					return
				case resp, ok := <-ch:
					if !ok {
						return
					}
					if resp.Err() != nil {
						watchErrors.Add(1)
						logutil.S().Debugw("watch error", "scenario", WatchManyPrefixes.String(),
							"watch", watchID, "error", resp.Err())

						return
					}
					if len(resp.Events) > 0 {
						eventCount.Add(int64(len(resp.Events)))
					}
				}
			}
		}(i, prefix)
	}

	// Wait for watches to be established
	time.Sleep(500 * time.Millisecond)

	// Generate workload across all resource types
	workers := workerCount(cfg)
	workers = max(workers, 4)

	duration := scenarioDuration(cfg)
	deadline := time.Now().Add(duration)
	perWorkerInterval := computePerWorkerInterval(cfg.RequestsPerSecond, workers)

	errs := runWorkers(workers, func(workerID int, _ chan<- error) {
		for time.Now().Before(deadline) {
			// Random resource type and key
			prefix := watchPrefixes[randutil.Intn(len(watchPrefixes))]
			key := fmt.Sprintf("%sobj-%04d", prefix, randutil.Intn(100))
			value := randutil.StringAlphabetsLowerCase(valueSize(cfg, 256))

			_ = performPutWithMetrics(runner, cli, metrics, key, value, workerID)
			sleepUntil(deadline, perWorkerInterval)
		}
	})

	// Stop watches
	watchCancel()
	watchWg.Wait()

	// Many concurrent watch streams multiplexed over WireGuard; write latency
	// increases as etcd fans out events to all watchers.
	stats := finalizeScenario(result, metrics, errs, 0.85, 3000)
	if !result.Success {
		return
	}

	if eventCount.Load() == 0 {
		result.Success = false
		result.Output = "no watch events received"

		return
	}

	if watchErrors.Load() > 0 {
		result.Success = false
		result.Output = fmt.Sprintf("watch errors encountered: %d", watchErrors.Load())

		return
	}

	avgEventsPerPrefix := float64(eventCount.Load()) / float64(len(watchPrefixes))

	result.Output = fmt.Sprintf(
		"watch %d prefixes: received %d events (%.0f/prefix); success %.2f%%, p99 %.0fms",
		len(watchPrefixes),
		eventCount.Load(),
		avgEventsPerPrefix,
		stats.SuccessRate()*100,
		stats.P99LatencyMs,
	)
}

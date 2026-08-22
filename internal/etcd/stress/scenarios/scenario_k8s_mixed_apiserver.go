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

// RunK8sMixedApiserver composes the real kube-apiserver shape in one
// scenario: informer list+watches on several resource collections, steady
// GET reads, bursty pod-churn writes, and node-lease renewals, all
// concurrently. This is the workload mix where leader-aware mutation routing
// and watch fan-out interact.
func RunK8sMixedApiserver(runner StressRunner) {
	logutil.S().Infow("running", "scenario", K8sMixedApiserver.String())

	result := &Result{
		Scenario:  K8sMixedApiserver.String(),
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

	root := runner.GenerateRandomKey(keySize(cfg, 8)) + "/registry"
	resources := []string{"pods", "endpointslices", "configmaps"}
	duration := scenarioDuration(cfg)
	deadline := time.Now().Add(duration)

	var eventsReceived, watchErrors, renewals atomic.Int64

	// Informer fan-out: two watchers per resource collection.
	watchCtx, watchCancel := context.WithCancel(context.Background())
	defer watchCancel()
	for _, resource := range resources {
		for range 2 {
			watcher := clientv3.NewWatcher(cli)
			ch := watcher.Watch(watchCtx, root+"/"+resource, clientv3.WithPrefix())
			go func() {
				for resp := range ch {
					if err := resp.Err(); err != nil {
						watchErrors.Add(1)
						continue
					}
					eventsReceived.Add(int64(len(resp.Events)))
				}
			}()
		}
	}

	// All roles run concurrently: runWorkers blocks until its workers finish,
	// so each role gets its own goroutine.
	var wg sync.WaitGroup
	var readerErrs, writerErrs, leaseErrs []error

	// Readers: steady cache-miss GETs.
	wg.Go(func() {
		readerErrs = runWorkers(4, func(workerID int, _ chan<- error) {
			for time.Now().Before(deadline) {
				key := fmt.Sprintf("%s/pods/ns-%02d/pod-%04d", root, randutil.Intn(8), randutil.Intn(200))
				//nolint:contextcheck // timeout context for short-lived operation within goroutine
				ctx, cancel := runner.NewCtxTimeout(testtime.ScaleDuration(10 * time.Second))
				start := time.Now()
				if _, err := cli.Get(ctx, key); err != nil {
					metrics.RecordFailure(float64(time.Since(start).Milliseconds()), err.Error())
				} else {
					metrics.RecordSuccess(float64(time.Since(start).Milliseconds()))
				}
				cancel()
				time.Sleep(30 * time.Millisecond)
			}
		})
	})

	// Churn writers: bursty pod lifecycle writes.
	wg.Go(func() {
		writerErrs = runWorkers(3, func(workerID int, _ chan<- error) {
			for time.Now().Before(deadline) {
				key := fmt.Sprintf("%s/pods/ns-%02d/pod-%04d", root, randutil.Intn(8), randutil.Intn(200))
				value := randutil.StringAlphabetsLowerCase(valueSize(cfg, 3072))

				//nolint:contextcheck // timeout context for short-lived operation within goroutine
				ctx, cancel := runner.NewCtxTimeout(testtime.ScaleDuration(10 * time.Second))
				start := time.Now()
				if _, err := cli.Put(ctx, key, value); err != nil {
					metrics.RecordFailure(float64(time.Since(start).Milliseconds()), err.Error())
				} else {
					metrics.RecordSuccess(float64(time.Since(start).Milliseconds()))
					metrics.RecordBytesWritten(int64(len(value)))
				}
				cancel()
				time.Sleep(50 * time.Millisecond)
			}
		})
	})

	// Node lease renewals: one lease per writer interval.
	wg.Go(func() {
		leaseErrs = runWorkers(2, func(workerID int, _ chan<- error) {
			key := fmt.Sprintf("%s/leases/kube-node-lease/node-%02d", root, workerID)
			ctx, cancel := runner.NewCtxTimeout(testtime.ScaleDuration(10 * time.Second))
			lease, err := cli.Grant(ctx, 10)
			cancel()
			if err != nil {
				return
			}
			ctx, cancel = runner.NewCtxTimeout(testtime.ScaleDuration(10 * time.Second))
			_, err = cli.Put(ctx, key, fmt.Sprintf("node-%02d", workerID), clientv3.WithLease(lease.ID))
			cancel()
			if err != nil {
				return
			}
			defer func() {
				ctx, cancel := runner.NewCtxTimeout(testtime.ScaleDuration(10 * time.Second))
				_, _ = cli.Revoke(ctx, lease.ID)
				cancel()
			}()
			for time.Now().Before(deadline) {
				//nolint:contextcheck // timeout context for short-lived operation within goroutine
				ctx, cancel := runner.NewCtxTimeout(testtime.ScaleDuration(10 * time.Second))
				start := time.Now()
				if _, err := cli.KeepAliveOnce(ctx, lease.ID); err != nil {
					metrics.RecordFailure(float64(time.Since(start).Milliseconds()), err.Error())
				} else {
					metrics.RecordSuccess(float64(time.Since(start).Milliseconds()))
					renewals.Add(1)
				}
				cancel()
				time.Sleep(time.Second)
			}
		})
	})

	wg.Wait()
	watchCancel()
	errs := append(append(readerErrs, writerErrs...), leaseErrs...)
	events := eventsReceived.Load()
	watchErrs := watchErrors.Load()

	stats := finalizeScenario(result, metrics, errs, 0.90, 5000)
	if result.Success {
		if watchErrs > 0 {
			result.Success = false
			result.Output = fmt.Sprintf("watch errors: %d", watchErrs)
		} else if events == 0 {
			result.Success = false
			result.Output = "informers received no events during the mixed workload"
		} else {
			result.Output = fmt.Sprintf("apiserver mix success %.2f%%, p99 %.0fms, informer events %d, lease renewals %d",
				stats.SuccessRate()*100, stats.P99LatencyMs, events, renewals.Load())
		}
	}

	logutil.S().Infow("scenario completed",
		"scenario", K8sMixedApiserver.String(),
		"events", events,
		"watch_errors", watchErrs,
		"renewals", renewals.Load(),
	)
}

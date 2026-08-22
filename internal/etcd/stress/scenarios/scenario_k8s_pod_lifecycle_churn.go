package scenarios

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	testtime "git.tbd/etcd-infra/internal/etcd/testtime"
	clientv3 "go.etcd.io/etcd/client/v3"

	logutil "git.tbd/etcd-infra/pkg/log"
	"git.tbd/etcd-infra/pkg/randutil"
)

// RunK8sPodLifecycleChurn models kube-apiserver pod churn at scale: writers
// create pod-shaped objects under /registry/pods/<ns>/<name> (a few KB each,
// matching real pod specs), update them twice (status subresource writes),
// and delete them, while long-lived prefix watches consume the collection —
// the informer pattern from staging/src/k8s.io/apiserver/pkg/storage/etcd3/watcher.go.
func RunK8sPodLifecycleChurn(runner StressRunner) {
	logutil.S().Infow("running", "scenario", K8sPodLifecycleChurn.String())

	result := &Result{
		Scenario:  K8sPodLifecycleChurn.String(),
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

	prefix := runner.GenerateRandomKey(keySize(cfg, 8)) + "/registry/pods"
	duration := scenarioDuration(cfg)
	deadline := time.Now().Add(duration)

	var eventsReceived, watchErrors atomic.Int64

	// Informer watches: long-lived prefix watches on the collection.
	watchCtx, watchCancel := context.WithCancel(context.Background())
	defer watchCancel()
	for range 4 {
		watcher := clientv3.NewWatcher(cli)
		ch := watcher.Watch(watchCtx, prefix, clientv3.WithPrefix())
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

	// Pod churn workers: create, two status updates, delete.
	workers := max(workerCount(cfg), 4)
	errs := runWorkers(workers, func(workerID int, _ chan<- error) {
		for time.Now().Before(deadline) {
			key := fmt.Sprintf("%s/ns-%02d/pod-%04d", prefix, randutil.Intn(8), randutil.Intn(200))
			value := randutil.StringAlphabetsLowerCase(valueSize(cfg, 3072))

			//nolint:contextcheck // timeout context for short-lived operation within goroutine
			ctx, cancel := runner.NewCtxTimeout(testtime.ScaleDuration(10 * time.Second))
			start := time.Now()
			if _, err := cli.Put(ctx, key, value); err != nil {
				metrics.RecordFailure(float64(time.Since(start).Milliseconds()), err.Error())
				cancel()
				continue
			}
			metrics.RecordSuccess(float64(time.Since(start).Milliseconds()))
			metrics.RecordBytesWritten(int64(len(value)))
			cancel()

			for range 2 {
				ctx, cancel := runner.NewCtxTimeout(testtime.ScaleDuration(10 * time.Second))
				start := time.Now()
				if _, err := cli.Put(ctx, key, value+"-status"); err != nil {
					metrics.RecordFailure(float64(time.Since(start).Milliseconds()), err.Error())
				} else {
					metrics.RecordSuccess(float64(time.Since(start).Milliseconds()))
				}
				cancel()
			}

			ctx, cancel = runner.NewCtxTimeout(testtime.ScaleDuration(10 * time.Second))
			start = time.Now()
			if _, err := cli.Delete(ctx, key); err != nil {
				metrics.RecordFailure(float64(time.Since(start).Milliseconds()), err.Error())
			} else {
				metrics.RecordSuccess(float64(time.Since(start).Milliseconds()))
			}
			cancel()

			time.Sleep(20 * time.Millisecond)
		}
	})

	watchCancel()
	events := eventsReceived.Load()
	watchErrs := watchErrors.Load()

	stats := finalizeScenario(result, metrics, errs, 0.95, 4000)
	if result.Success {
		if watchErrs > 0 {
			result.Success = false
			result.Output = fmt.Sprintf("watch errors: %d", watchErrs)
		} else if events == 0 {
			result.Success = false
			result.Output = "informers received no events during churn"
		} else {
			result.Output = fmt.Sprintf("pod lifecycle churn success %.2f%%, p99 %.0fms, informer events %d",
				stats.SuccessRate()*100, stats.P99LatencyMs, events)
		}
	}

	logutil.S().Infow("scenario completed",
		"scenario", K8sPodLifecycleChurn.String(),
		"events", events,
		"watch_errors", watchErrs,
	)
}

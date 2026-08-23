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

// RunK8sJobStorm drives a burst of pod creates and deletes in a short window.
//
// WHAT: every worker creates pod-shaped objects (a few KB, matching real pod
// specs) under /registry/pods/<ns>/<name> as fast as it can, then deletes
// them, for the whole scenario window. There is no pacing sleep — the point
// is the burst. Long-lived prefix watches consume the collection throughout.
//
// WHY: AI and ML workloads turn Kubernetes writes from a steady stream into
// storms. Gang schedulers (Kueue, JobSet, TrainJob) create thousands of pods
// in seconds when a training job starts and delete them when it ends;
// inference autoscalers churn pods and EndpointSlices on traffic spikes. This
// is the write-rate spike where leader-aware mutation routing must prove that
// pinning all writes to the leader does not degrade latency or throughput.
//
// HOW: writers run create, one status update, delete, with no sleep between
// cycles. Informers watch the collection. The scenario records every
// operation's latency and asserts the success rate, the p99 budget, and that
// the informers saw events with no watch errors.
func RunK8sJobStorm(runner StressRunner) {
	logutil.S().Infow("running", "scenario", K8sJobStorm.String())

	result := &Result{
		Scenario:  K8sJobStorm.String(),
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

	// Storm workers: create, one status update, delete, no pacing. The full
	// worker count drives the burst — this is the one scenario that does not
	// cap its writers.
	workers := max(workerCount(cfg), 8)
	errs := runWorkers(workers, func(workerID int, _ chan<- error) {
		for time.Now().Before(deadline) {
			key := fmt.Sprintf("%s/ns-%02d/pod-%06d", prefix, randutil.Intn(8), randutil.Intn(10000))
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

			ctx, cancel = runner.NewCtxTimeout(testtime.ScaleDuration(10 * time.Second))
			start = time.Now()
			if _, err := cli.Put(ctx, key, value+"-status"); err != nil {
				metrics.RecordFailure(float64(time.Since(start).Milliseconds()), err.Error())
			} else {
				metrics.RecordSuccess(float64(time.Since(start).Milliseconds()))
			}
			cancel()

			ctx, cancel = runner.NewCtxTimeout(testtime.ScaleDuration(10 * time.Second))
			start = time.Now()
			if _, err := cli.Delete(ctx, key); err != nil {
				metrics.RecordFailure(float64(time.Since(start).Milliseconds()), err.Error())
			} else {
				metrics.RecordSuccess(float64(time.Since(start).Milliseconds()))
			}
			cancel()
		}
	})

	watchCancel()
	events := eventsReceived.Load()
	watchErrs := watchErrors.Load()

	stats := finalizeScenario(result, metrics, errs, 0.95, 8000)
	if result.Success {
		if watchErrs > 0 {
			result.Success = false
			result.Output = fmt.Sprintf("watch errors: %d", watchErrs)
		} else if events == 0 {
			result.Success = false
			result.Output = "informers received no events during the job storm"
		} else {
			result.Output = fmt.Sprintf("job storm success %.2f%%, p99 %.0fms, informer events %d",
				stats.SuccessRate()*100, stats.P99LatencyMs, events)
		}
	}

	logutil.S().Infow("scenario completed",
		"scenario", K8sJobStorm.String(),
		"events", events,
		"watch_errors", watchErrs,
	)
}

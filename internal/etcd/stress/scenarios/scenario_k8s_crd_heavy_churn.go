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

// RunK8sCRDHeavyChurn models clusters with many CRDs: CustomResourceDefinition
// objects carry large OpenAPI validation schemas (tens to hundreds of KB), so
// the apiserver's writes under
// /registry/apiextensions.k8s.io/customresourcedefinitions are large-value
// churn — create, schema updates, delete — with informer watches on the
// collection. This is the payload shape that dominates write bandwidth and
// snapshot pressure in CRD-heavy clusters.
func RunK8sCRDHeavyChurn(runner StressRunner) {
	logutil.S().Infow("running", "scenario", K8sCRDHeavyChurn.String())

	result := &Result{
		Scenario:  K8sCRDHeavyChurn.String(),
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

	prefix := runner.GenerateRandomKey(keySize(cfg, 8)) + "/registry/apiextensions.k8s.io/customresourcedefinitions"
	duration := scenarioDuration(cfg)
	deadline := time.Now().Add(duration)

	var eventsReceived, watchErrors atomic.Int64

	// Informer watches on the CRD collection.
	watchCtx, watchCancel := context.WithCancel(context.Background())
	defer watchCancel()
	for range 3 {
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

	// CRD churn workers: large values — 64KB typical, 256KB for the biggest
	// validation schemas. CRD churn is a lifecycle workload like pod churn:
	// even operator storms install CRDs at ~1/s, so the writers are capped
	// and paced — the scenario's point is the payload size, not the rate.
	// (The throughput-flood scenarios own raw scale.)
	workers := min(max(workerCount(cfg), 2), 4)
	errs := runWorkers(workers, func(workerID int, _ chan<- error) {
		for time.Now().Before(deadline) {
			key := fmt.Sprintf("%s/crd-%04d", prefix, randutil.Intn(50))
			size := valueSize(cfg, 65536)
			if randutil.Intn(10) == 0 {
				size = 262144
			}
			value := randutil.StringAlphabetsLowerCase(size)

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
			if _, err := cli.Put(ctx, key, value+"-v2"); err != nil {
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

			time.Sleep(250 * time.Millisecond)
		}
	})

	watchCancel()
	events := eventsReceived.Load()
	watchErrs := watchErrors.Load()

	stats := finalizeScenario(result, metrics, errs, 0.90, 8000)
	if result.Success {
		if watchErrs > 0 {
			result.Success = false
			result.Output = fmt.Sprintf("watch errors: %d", watchErrs)
		} else if events == 0 {
			result.Success = false
			result.Output = "informers received no events during CRD churn"
		} else {
			result.Output = fmt.Sprintf("CRD churn success %.2f%%, p99 %.0fms, informer events %d",
				stats.SuccessRate()*100, stats.P99LatencyMs, events)
		}
	}

	logutil.S().Infow("scenario completed",
		"scenario", K8sCRDHeavyChurn.String(),
		"events", events,
		"watch_errors", watchErrs,
	)
}

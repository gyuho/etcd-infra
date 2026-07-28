package scenarios

import (
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	testtime "git.tbd/etcd-infra/internal/etcd/testtime"
	clientv3 "go.etcd.io/etcd/client/v3"

	logutil "git.tbd/etcd-infra/pkg/log"
	"git.tbd/etcd-infra/pkg/randutil"
)

// RunListPaginationHeavy staging/src/k8s.io/apiserver/pkg/storage/etcd3/store.go "GetList" with continue Kubernetes list operations with pagination under concurrent updates. Simulates: kubectl get pods all-namespaces with thousands of pods.
func RunListPaginationHeavy(runner StressRunner) {
	logutil.S().Infow("running", "scenario", ListPaginationHeavy.String())

	result := &Result{
		Scenario:  ListPaginationHeavy.String(),
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

	// Seed many keys across multiple prefixes (namespaces)
	namespaceCount := 10
	keysPerNamespace := 200

	prefix := runner.GenerateRandomKey(keySize(cfg, 8))
	logutil.S().Infow("seeding keys", "scenario", ListPaginationHeavy.String(),
		"namespaces", namespaceCount, "keys_per_ns", keysPerNamespace)

	// Seed keys
	for ns := range namespaceCount {
		for i := range keysPerNamespace {
			key := fmt.Sprintf("%s/ns-%02d/pod-%04d", prefix, ns, i)
			value := randutil.StringAlphabetsLowerCase(valueSize(cfg, 512))

			ctx, cancel := runner.NewCtxTimeout(5 * time.Second)
			_, err := cli.Put(ctx, key, value)
			cancel()

			if err != nil {
				logutil.S().Warnw("seed put failed", "scenario", ListPaginationHeavy.String(),
					"key", key, "error", err)
			}
		}
	}

	workers := workerCount(cfg)
	workers = max(workers, 2)

	// Split workers: 70% doing paginated lists, 30% doing concurrent updates.
	listWorkers := (workers * 7) / 10
	listWorkers = max(listWorkers, 1)
	updateWorkers := workers - listWorkers

	duration := scenarioDuration(cfg)

	// Set deadline AFTER seeding completes. Seeding 2000 keys across WireGuard
	// can take significant time; if we set the deadline before seeding, the
	// update workers (which run blocking via runWorkers) consume the remaining
	// time and list workers never get a chance to execute.
	deadline := time.Now().Add(duration)

	var listOperations, listPages, listKeys, updateOperations atomic.Int64
	perWorkerInterval := computePerWorkerInterval(cfg.RequestsPerSecond, workers)

	// Run update and list workers CONCURRENTLY. runWorkers() is blocking
	// (it calls wg.Wait() before returning), so launching them sequentially
	// would starve list workers of time. Instead, we launch both groups in
	// parallel goroutines and collect errors from both.
	var updateErrs, listErrs []error
	var wgGroups sync.WaitGroup
	wgGroups.Add(2)

	// Update workers: continuous background updates
	go func() {
		defer wgGroups.Done()
		updateErrs = runWorkers(updateWorkers, func(workerID int, _ chan<- error) {
			for time.Now().Before(deadline) {
				ns := randutil.Intn(namespaceCount)
				idx := randutil.Intn(keysPerNamespace)
				key := fmt.Sprintf("%s/ns-%02d/pod-%04d", prefix, ns, idx)
				value := randutil.StringAlphabetsLowerCase(valueSize(cfg, 512))

				_ = performPutWithMetrics(runner, cli, metrics, key, value, workerID)
				updateOperations.Add(1)
				sleepUntil(deadline, perWorkerInterval)
			}
		})
	}()

	// List workers: paginated list operations running concurrently with updates
	const pageSize = 50
	go func() {
		defer wgGroups.Done()
		listErrs = runWorkers(listWorkers, func(_ int, _ chan<- error) {
			for time.Now().Before(deadline) {
				// Random namespace to list
				ns := randutil.Intn(namespaceCount)
				nsPrefix := fmt.Sprintf("%s/ns-%02d/", prefix, ns)

				ctx, cancel := runner.NewCtxTimeout(10 * time.Second)
				startList := time.Now()

				// First page
				resp, err := cli.Get(
					ctx,
					nsPrefix,
					clientv3.WithPrefix(),
					clientv3.WithLimit(pageSize),
					clientv3.WithSort(clientv3.SortByKey, clientv3.SortAscend),
				)
				cancel()

				latencyMs := float64(time.Since(startList).Milliseconds())

				if err != nil {
					metrics.RecordFailure(latencyMs, err.Error())
					sleepUntil(deadline, perWorkerInterval)

					continue
				}

				listOperations.Add(1)
				pageCount := 1
				totalKeys := len(resp.Kvs)
				listKeys.Add(int64(len(resp.Kvs)))

				// Continue pagination
				for resp.More {
					if time.Now().After(deadline) {
						break
					}

					lastKey := resp.Kvs[len(resp.Kvs)-1].Key
					nextStart := append([]byte(nil), lastKey...)
					nextStart = append(nextStart, 0x00)

					ctx, cancel := runner.NewCtxTimeout(10 * time.Second)
					startPage := time.Now()

					resp, err = cli.Get(
						ctx,
						string(nextStart),
						clientv3.WithRange(clientv3.GetPrefixRangeEnd(nsPrefix)),
						clientv3.WithLimit(pageSize),
						clientv3.WithSort(clientv3.SortByKey, clientv3.SortAscend),
					)
					cancel()

					pageLatencyMs := float64(time.Since(startPage).Milliseconds())
					latencyMs += pageLatencyMs

					if err != nil {
						metrics.RecordFailure(pageLatencyMs, err.Error())

						break
					}

					pageCount++
					totalKeys += len(resp.Kvs)
					listKeys.Add(int64(len(resp.Kvs)))
				}

				listPages.Add(int64(pageCount))
				metrics.RecordSuccess(latencyMs)
				sleepUntil(deadline, perWorkerInterval*time.Duration(pageCount))
			}
		})
	}()

	wgGroups.Wait()

	errs := make([]error, 0, len(listErrs)+len(updateErrs))
	errs = append(errs, listErrs...)
	errs = append(errs, updateErrs...)
	// Paginated lists multiply per-page latency; each continuation request crosses
	// the WireGuard tunnel, so multi-page lists have amplified total latency.
	stats := finalizeScenario(result, metrics, errs, 0.80, 5000)
	if !result.Success {
		return
	}

	if listOperations.Load() == 0 {
		result.Success = false
		result.Output = "no list operations completed"

		return
	}

	avgPagesPerList := float64(listPages.Load()) / float64(listOperations.Load())
	avgKeysPerList := float64(listKeys.Load()) / float64(listOperations.Load())

	result.Output = fmt.Sprintf(
		"paginated lists: %d operations, %.1f pages/list, %.0f keys/list, %d updates; success %.2f%%, p99 %.0fms",
		listOperations.Load(),
		avgPagesPerList,
		avgKeysPerList,
		updateOperations.Load(),
		stats.SuccessRate()*100,
		stats.P99LatencyMs,
	)
}

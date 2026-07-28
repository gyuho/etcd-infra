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

// RunLeaseIntensiveWorkload staging/src/k8s.io/apiserver/pkg/storage/etcd3/lease_manager.go Kubernetes uses leases extensively for Event objects and coordination. This test stresses: continuous lease grant, attach to keys, renewal, and expiry.
func RunLeaseIntensiveWorkload(runner StressRunner) {
	logutil.S().Infow("running", "scenario", LeaseIntensiveWorkload.String())

	result := &Result{
		Scenario:  LeaseIntensiveWorkload.String(),
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

	workers := workerCount(cfg)
	workers = max(workers, 4)

	// Lease lifecycle: short TTL (5s) like Event objects
	const leaseTTL = 5

	duration := scenarioDuration(cfg)
	deadline := time.Now().Add(duration)
	prefix := runner.GenerateRandomKey(keySize(cfg, 8))

	var leaseGranted, leaseAttached, leaseKeepalive, leaseExpired atomic.Int64
	perWorkerInterval := computePerWorkerInterval(cfg.RequestsPerSecond, workers)

	// Track active leases for renewal
	type activeLease struct {
		id     clientv3.LeaseID
		cancel context.CancelFunc
	}
	var leaseMu sync.Mutex
	activeLeases := make(map[int]*activeLease)

	errs := runWorkers(workers, func(workerID int, _ chan<- error) {
		for time.Now().Before(deadline) {
			// Grant new lease
			ctx, cancel := runner.NewCtxTimeout(5 * time.Second)
			startGrant := time.Now()
			leaseResp, err := cli.Grant(ctx, leaseTTL)
			cancel()

			latencyMs := float64(time.Since(startGrant).Milliseconds())

			if err != nil {
				metrics.RecordFailure(latencyMs, err.Error())
				sleepUntil(deadline, perWorkerInterval)

				continue
			}

			leaseGranted.Add(1)
			leaseID := leaseResp.ID

			// Attach key to lease (like Event object creation)
			key := fmt.Sprintf("%s/event-%d-%d", prefix, workerID, leaseGranted.Load())
			value := randutil.StringAlphabetsLowerCase(valueSize(cfg, 128))

			ctx, cancel = runner.NewCtxTimeout(5 * time.Second)
			startPut := time.Now()
			_, err = cli.Put(ctx, key, value, clientv3.WithLease(leaseID))
			cancel()

			putLatencyMs := float64(time.Since(startPut).Milliseconds())

			if err != nil {
				metrics.RecordFailure(putLatencyMs, err.Error())
				sleepUntil(deadline, perWorkerInterval)

				continue
			}

			leaseAttached.Add(1)
			// Record total latency for grant + put
			totalLatencyMs := latencyMs + putLatencyMs
			metrics.RecordSuccess(totalLatencyMs)

			// Keep alive for 2-3 cycles, then let expire (simulating Event TTL)
			keepaliveCtx, keepaliveCancel := context.WithCancel(context.Background())

			leaseMu.Lock()
			activeLeases[workerID] = &activeLease{
				id:     leaseID,
				cancel: keepaliveCancel,
			}
			leaseMu.Unlock()

			go func(leaseID clientv3.LeaseID, workerID int) {
				keepaliveCh, kaErr := cli.KeepAlive(keepaliveCtx, leaseID)
				if kaErr != nil {
					logutil.S().Debugw("keepalive failed", "scenario", LeaseIntensiveWorkload.String(), "error", kaErr)

					return
				}

				renewalCount := 0
				maxRenewals := randutil.Intn(3) + 2 // 2-4 renewals

				for {
					select {
					case <-keepaliveCtx.Done():
						return
					case ka, ok := <-keepaliveCh:
						if !ok {
							return
						}
						if ka == nil {
							continue
						}
						leaseKeepalive.Add(1)
						renewalCount++

						if renewalCount >= maxRenewals {
							// Stop renewal, let lease expire (simulate Event TTL)
							leaseMu.Lock()
							if al, exists := activeLeases[workerID]; exists && al.id == leaseID {
								al.cancel()
								delete(activeLeases, workerID)
							}
							leaseMu.Unlock()
							leaseExpired.Add(1)

							return
						}
					}
				}
			}(leaseID, workerID)

			sleepUntil(deadline, perWorkerInterval)
		}
	})

	// Cancel all remaining leases
	leaseMu.Lock()
	for _, al := range activeLeases {
		al.cancel()
	}
	leaseMu.Unlock()

	// Wait a bit for keepalive goroutines to finish
	time.Sleep(500 * time.Millisecond)

	// Lease grant + put is a compound operation; each step crosses the WireGuard
	// tunnel, doubling the effective RTT for a single logical operation.
	stats := finalizeScenario(result, metrics, errs, 0.85, 3000)
	if !result.Success {
		return
	}

	if leaseGranted.Load() == 0 {
		result.Success = false
		result.Output = "no leases granted"

		return
	}

	if leaseAttached.Load() == 0 {
		result.Success = false
		result.Output = "no keys attached to leases"

		return
	}

	result.Output = fmt.Sprintf(
		"lease intensive workload: granted %d, attached %d, renewals %d, expired %d; success %.2f%%, p99 %.0fms",
		leaseGranted.Load(),
		leaseAttached.Load(),
		leaseKeepalive.Load(),
		leaseExpired.Load(),
		stats.SuccessRate()*100,
		stats.P99LatencyMs,
	)
}

package scenarios

import (
	"fmt"
	"sync/atomic"
	"time"

	testtime "git.tbd/etcd-infra/internal/etcd/testtime"
	clientv3 "go.etcd.io/etcd/client/v3"

	logutil "git.tbd/etcd-infra/pkg/log"
)

// RunK8sNodeHeartbeatLeases models kubelet node heartbeats: every node keeps
// a short-TTL lease under /registry/leases/kube-node-lease/<node> and renews
// it on a fixed interval (the node-lease pattern from
// staging/src/k8s.io/kubelet/pkg/nodelease). A steady stream of small,
// latency-sensitive renewals.
func RunK8sNodeHeartbeatLeases(runner StressRunner) {
	logutil.S().Infow("running", "scenario", K8sNodeHeartbeatLeases.String())

	result := &Result{
		Scenario:  K8sNodeHeartbeatLeases.String(),
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

	prefix := runner.GenerateRandomKey(keySize(cfg, 8)) + "/registry/leases/kube-node-lease"
	duration := scenarioDuration(cfg)
	deadline := time.Now().Add(duration)

	nodes := 8
	var renewals atomic.Int64

	errs := runWorkers(nodes, func(workerID int, _ chan<- error) {
		key := fmt.Sprintf("%s/node-%02d", prefix, workerID)

		ctx, cancel := runner.NewCtxTimeout(5 * time.Second)
		lease, err := cli.Grant(ctx, 10)
		cancel()
		if err != nil {
			return
		}
		// The node lease object is itself a lease-attached key.
		ctx, cancel = runner.NewCtxTimeout(5 * time.Second)
		_, err = cli.Put(ctx, key, fmt.Sprintf("node-%02d", workerID), clientv3.WithLease(lease.ID))
		cancel()
		if err != nil {
			return
		}
		defer func() {
			ctx, cancel := runner.NewCtxTimeout(5 * time.Second)
			_, _ = cli.Revoke(ctx, lease.ID)
			cancel()
		}()

		for time.Now().Before(deadline) {
			//nolint:contextcheck // timeout context for short-lived operation within goroutine
			ctx, cancel := runner.NewCtxTimeout(5 * time.Second)
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

	stats := finalizeScenario(result, metrics, errs, 0.99, 3000)
	if result.Success {
		result.Output = fmt.Sprintf("node heartbeats: %d renewals across %d nodes, success %.2f%%, p99 %.0fms",
			renewals.Load(), nodes, stats.SuccessRate()*100, stats.P99LatencyMs)
	}

	logutil.S().Infow("scenario completed",
		"scenario", K8sNodeHeartbeatLeases.String(),
		"renewals", renewals.Load(),
	)
}

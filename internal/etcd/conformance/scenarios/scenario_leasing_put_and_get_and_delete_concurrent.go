package scenarios

import (
	"fmt"
	"time"

	testtime "git.tbd/etcd-infra/internal/etcd/testtime"
	clientv3 "go.etcd.io/etcd/client/v3"
	"go.etcd.io/etcd/client/v3/leasing"

	logutil "git.tbd/etcd-infra/pkg/log"
)

// RunLeasingPutAndGetAndDeleteConcurrent tests the LeasingPutAndGetAndDeleteConcurrent scenario.
func RunLeasingPutAndGetAndDeleteConcurrent(runner Runner) {
	logutil.S().Infow("running", "scenario", LeasingPutAndGetAndDeleteConcurrent.String())

	result := &Result{
		Scenario:  LeasingPutAndGetAndDeleteConcurrent.String(),
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

	testPfx := runner.GenerateRandomKey(10)

	lKVs := make([]clientv3.KV, 8)
	cleanupFuncs := make([]func(), 0, len(lKVs))
	for i := range lKVs {
		lKV, closeLKV, newKVErr := leasing.NewKV(cli, testPfx)
		if newKVErr != nil {
			result.Success = false
			result.Output = fmt.Sprintf("leasing.NewKV failed (%v)", newKVErr)

			return
		}
		cleanupFuncs = append(cleanupFuncs, closeLKV)
		lKVs[i] = lKV
	}
	defer func() {
		for _, cleanup := range cleanupFuncs {
			cleanup()
		}
	}()

	testKey := runner.GenerateRandomKey(10)

	getDel := func(kv clientv3.KV) error {
		ctx, cancel := runner.NewCtx()
		_, opErr := kv.Put(ctx, testKey, "abc")
		cancel()
		if opErr != nil {
			return fmt.Errorf("failed to put: %w", opErr)
		}

		time.Sleep(time.Millisecond)

		ctx, cancel = runner.NewCtx()
		_, getErr := kv.Get(ctx, testKey)
		cancel()
		if getErr != nil {
			return fmt.Errorf("failed to get: %w", getErr)
		}

		ctx, cancel = runner.NewCtx()
		_, deleteErr := kv.Delete(ctx, testKey)
		cancel()
		if deleteErr != nil {
			return fmt.Errorf("failed to delete: %w", deleteErr)
		}

		time.Sleep(2 * time.Millisecond)

		return nil
	}

	workerCount := len(lKVs)
	errc := make(chan error, workerCount)
	for range workerCount {
		go func() {
			for _, kv := range lKVs {
				workerErr := getDel(kv)
				if workerErr != nil {
					errc <- workerErr

					return
				}
			}
			errc <- nil
		}()
	}
	// Use a generous timeout (90s) to accommodate cloud/VPN environments (Tailscale/Headscale)
	// where concurrent leasing operations have significantly higher latency.
	// 8 workers doing Put/Get/Delete for 8 KVs each over high-latency networks requires more time.
	// The slow-path multiplier extends it for SSM port-forwarding (observed:
	// 6/8 workers timed out in a full-suite run through the bastion tunnels).
	timeout := time.After(testtime.ScaleDuration(90 * time.Second))
	for i := range workerCount {
		var workerErr error
		select {
		case workerErr = <-errc:
			if workerErr != nil {
				result.Success = false
				result.Output = fmt.Sprintf("worker %d/%d failed: %v", i+1, workerCount, workerErr)

				return
			}
		case <-timeout:
			result.Success = false
			result.Output = fmt.Sprintf("timed out waiting for operations (received %d/%d)", i, workerCount)

			return
		}
	}

	ctx, cancel := runner.NewCtx()
	resp, err := lKVs[0].Get(ctx, testKey)
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to get: %v", err)

		return
	}
	if len(resp.Kvs) > 0 {
		result.Success = false
		result.Output = fmt.Sprintf("key %s is not deleted", testKey)

		return
	}

	ctx, cancel = runner.NewCtx()
	gresp, err := cli.Get(ctx, testKey)
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to get: %v", err)

		return
	}
	if len(gresp.Kvs) > 0 {
		result.Success = false
		result.Output = fmt.Sprintf("key %s is not deleted", testKey)

		return
	}
}

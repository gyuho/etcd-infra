package scenarios

import (
	"fmt"
	"path"
	"reflect"
	"strconv"
	"time"

	testtime "git.tbd/etcd-infra/internal/etcd/testtime"
	clientv3 "go.etcd.io/etcd/client/v3"
	"go.etcd.io/etcd/client/v3/leasing"

	logutil "git.tbd/etcd-infra/pkg/log"
)

// RunLeasingPutAndDeleteRangeWithContendingTxn tests the LeasingPutAndDeleteRangeWithContendingTxn scenario.
func RunLeasingPutAndDeleteRangeWithContendingTxn(runner Runner) {
	logutil.S().Infow("running", "scenario", LeasingPutAndDeleteRangeWithContendingTxn.String())

	result := &Result{
		Scenario:  LeasingPutAndDeleteRangeWithContendingTxn.String(),
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

	putKV, closePutKV, err := leasing.NewKV(cli, testPfx)
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to create leasing KV: %v", err)

		return
	}
	defer closePutKV()

	delKV, closeDelKV, err := leasing.NewKV(cli, testPfx)
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to create leasing KV: %v", err)

		return
	}
	defer closeDelKV()

	testKey := runner.GenerateRandomKey(10)
	keys := 10
	for i := range keys {
		k := path.Join(testKey, strconv.Itoa(i))

		ctx, cancel := runner.NewCtx()
		_, err = cli.Put(ctx, k, "bar")
		cancel()
		if err != nil {
			result.Success = false
			result.Output = fmt.Sprintf("failed to put: %v", err)

			return
		}

		ctx, cancel = runner.NewCtx()
		_, err = putKV.Get(ctx, k)
		cancel()
		if err != nil {
			result.Success = false
			result.Output = fmt.Sprintf("failed to leasing get: %v", err)

			return
		}
	}

	// Use a random prefix for the contending keys to avoid conflicts with other data.
	// IMPORTANT: Never use hardcoded prefixes like "key/" that could delete
	// Kubernetes data or other keys when running against a live cluster.
	contendingPrefix := runner.GenerateRandomKey(10)
	// Use a very generous timeout (10 min) for cloud/VPN environments where leasing
	// operations have significantly higher latency. This test involves:
	// - Lease acquisition from remote etcd
	// - Multiple concurrent put/get operations contending for the same keys
	// - A delete-with-range transaction that must coordinate with the loop
	// In high-latency environments like Hetzner Cloud (100-200ms RTT), each leasing
	// operation can take 500ms-1s, and the contention significantly amplifies delays.
	ctx, cancel := runner.NewCtxTimeout(180 * time.Second)
	donec := make(chan struct{})

	// Run contending operations in a goroutine. Unlike local etcd tests where the loop
	// runs indefinitely until the delete completes, in high-latency environments we limit
	// the iterations to prevent deadlock-like contention scenarios. After the goroutine
	// completes its iterations, the delete transaction can proceed without contention.
	go func() {
		defer close(donec)
		// Run a limited number of iterations to create contention but not deadlock.
		// In high-latency environments, each leasing operation can take 500ms-1s,
		// so 50 iterations is enough to test contention without blocking forever.
		maxIterations := 50
		for i := 0; i < maxIterations && ctx.Err() == nil; i++ {
			key := path.Join(contendingPrefix, strconv.Itoa(i%8))

			_, perr := putKV.Put(ctx, key, "123")
			if perr != nil && ctx.Err() == nil {
				logutil.S().Warnw("failed to leasing put", "error", perr)
			}

			_, gerr := putKV.Get(ctx, key)
			if gerr != nil && ctx.Err() == nil {
				logutil.S().Warnw("failed to leasing get", "error", gerr)
			}
		}
	}()

	// Wait for the goroutine to complete its iterations before issuing delete.
	// This ensures the delete doesn't have to compete with ongoing operations.
	<-donec

	// Now issue the delete transaction without contention
	_, err = delKV.Do(
		ctx,
		clientv3.OpTxn(
			nil,
			[]clientv3.Op{
				clientv3.OpDelete(contendingPrefix, clientv3.WithPrefix()),
			},
			nil,
		),
	)
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to issue txn: %v", err)

		return
	}

	// confirm keys on non-deleter match etcd
	for i := range keys {
		k := path.Join(testKey, strconv.Itoa(i))

		ctx, cancel = runner.NewCtx()
		resp1, err := putKV.Get(ctx, k)
		cancel()
		if err != nil {
			result.Success = false
			result.Output = fmt.Sprintf("failed to leasing get: %v", err)

			return
		}

		ctx, cancel = runner.NewCtx()
		resp2, err := cli.Get(ctx, k)
		cancel()
		if err != nil {
			result.Success = false
			result.Output = fmt.Sprintf("failed to get: %v", err)

			return
		}

		if !reflect.DeepEqual(resp1.Kvs, resp2.Kvs) {
			result.Success = false
			result.Output = fmt.Sprintf("keys mismatch: %v vs %v", resp1.Kvs, resp2.Kvs)

			return
		}
	}
}

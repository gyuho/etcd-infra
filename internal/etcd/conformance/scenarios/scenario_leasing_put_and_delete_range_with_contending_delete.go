package scenarios

import (
	"fmt"
	"path"
	"reflect"
	"strconv"

	testtime "git.tbd/etcd-infra/internal/etcd/testtime"
	clientv3 "go.etcd.io/etcd/client/v3"
	"go.etcd.io/etcd/client/v3/leasing"

	logutil "git.tbd/etcd-infra/pkg/log"
)

// RunLeasingPutAndDeleteRangeWithContendingDelete tests the LeasingPutAndDeleteRangeWithContendingDelete scenario.
func RunLeasingPutAndDeleteRangeWithContendingDelete(runner Runner) {
	logutil.S().Infow("running", "scenario", LeasingPutAndDeleteRangeWithContendingDelete.String())

	result := &Result{
		Scenario:  LeasingPutAndDeleteRangeWithContendingDelete.String(),
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
	// IMPORTANT: Never use hardcoded prefixes like "key/" that could conflict
	// with Kubernetes data or other keys when running against a live cluster.
	contendingPrefix := runner.GenerateRandomKey(10)
	ctx, cancel := runner.NewCtx()
	donec := make(chan struct{})
	go func() {
		defer close(donec)
		for i := 0; ctx.Err() == nil; i++ {
			key := path.Join(contendingPrefix, strconv.Itoa(i%8))

			_, perr := putKV.Put(ctx, key, "123")
			if perr != nil {
				logutil.S().Warnw("failed to leasing put", "error", perr)
			}

			_, gerr := putKV.Get(ctx, key)
			if gerr != nil {
				logutil.S().Warnw("failed to leasing get", "error", perr)
			}
		}
	}()
	_, err = delKV.Do(
		ctx,
		clientv3.OpDelete(testKey, clientv3.WithPrefix()),
	)
	cancel()
	<-donec
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to delete range: %v", err)

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

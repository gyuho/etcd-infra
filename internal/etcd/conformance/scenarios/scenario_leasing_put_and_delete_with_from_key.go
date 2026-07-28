package scenarios

import (
	"context"
	"fmt"
	"path"
	"strconv"
	"time"

	testtime "git.tbd/etcd-infra/internal/etcd/testtime"
	clientv3 "go.etcd.io/etcd/client/v3"
	"go.etcd.io/etcd/client/v3/leasing"

	logutil "git.tbd/etcd-infra/pkg/log"
)

// RunLeasingPutAndDeleteWithFromKey tests the LeasingPutAndDeleteWithFromKey scenario.
func RunLeasingPutAndDeleteWithFromKey(runner Runner) {
	logutil.S().Infow("running", "scenario", LeasingPutAndDeleteWithFromKey.String())

	result := &Result{
		Scenario:  LeasingPutAndDeleteWithFromKey.String(),
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

	lKV, closeLKV, err := leasing.NewKV(cli, testPfx)
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to create leasing KV: %v", err)

		return
	}
	defer closeLKV()

	testKey := runner.GenerateRandomKey(10)

	keys := 10
	for i := range keys {
		ctx, cancel := runner.NewCtx()
		_, err = cli.Put(ctx, path.Join(testKey, strconv.Itoa(i)), "bar")
		cancel()
		if err != nil {
			result.Success = false
			result.Output = fmt.Sprintf("failed to put: %v", err)

			return
		}
	}

	// trigger cache update
	ctx, cancel := runner.NewCtx()
	_, err = lKV.Get(ctx, path.Join(testKey, "1"))
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to leasing get: %v", err)

		return
	}

	// Delete keys within the test range only.
	// IMPORTANT: Using WithFromKey() alone would delete from testKey forward through
	// the ENTIRE keyspace, including Kubernetes data under /registry/. We use
	// WithRange(endKey) to bound the deletion to only test keys.
	endKey := testKey + "\xff"
	ctx, cancel = runner.NewCtx()
	opResp, err := lKV.Do(ctx, clientv3.OpDelete(testKey, clientv3.WithRange(endKey)))
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to leasing delete range: %v", err)

		return
	}
	delRev := opResp.Del().Header.GetRevision()

	// confirm keys are invalidated from cache and deleted on etcd
	for i := range keys {
		ctx, cancel = runner.NewCtx()
		gresp, err := lKV.Get(ctx, path.Join(testKey, strconv.Itoa(i)))
		cancel()
		if err != nil {
			result.Success = false
			result.Output = fmt.Sprintf("failed to leasing get: %v", err)

			return
		}
		if len(gresp.Kvs) > 0 {
			result.Success = false
			result.Output = fmt.Sprintf("key %s is not deleted", path.Join(testKey, strconv.Itoa(i)))

			return
		}
	}

	cctx, ccancel := context.WithCancel(context.Background())
	defer ccancel()

	// confirm keys were deleted atomically
	wch := cli.Watch(
		cctx,
		testKey,
		clientv3.WithRev(delRev),
		clientv3.WithPrefix(),
	)
	select {
	case wr := <-wch:
		if len(wr.Events) != keys {
			result.Success = false
			result.Output = fmt.Sprintf("expected %d keys to be deleted, got %d", keys, len(wr.Events))

			return
		}
	case <-time.After(5 * time.Second):
		result.Success = false
		result.Output = watchEventTimeoutDeleteMsg

		return
	}
}

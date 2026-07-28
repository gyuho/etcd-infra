package scenarios

import (
	"context"
	"fmt"
	"path"
	"time"

	testtime "git.tbd/etcd-infra/internal/etcd/testtime"
	clientv3 "go.etcd.io/etcd/client/v3"
	"go.etcd.io/etcd/client/v3/leasing"

	logutil "git.tbd/etcd-infra/pkg/log"
)

// RunLeasingTxnNonOwnerPut verifies a transaction from a non-owner path.
//
//nolint:gocyclo // Scenario walks multiple transaction branches.
func RunLeasingTxnNonOwnerPut(runner Runner) {
	logutil.S().Infow("running", "scenario", LeasingTxnNonOwnerPut.String())

	result := &Result{
		Scenario:  LeasingTxnNonOwnerPut.String(),
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

	lKV1, closeLKV1, err := leasing.NewKV(cli, testPfx)
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to create leasing KV: %v", err)

		return
	}
	defer closeLKV1()

	lKV2, closeLKV2, err := leasing.NewKV(cli, testPfx)
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to create leasing KV: %v", err)

		return
	}
	defer closeLKV2()

	k1 := runner.GenerateRandomKey(10)
	k2 := path.Join(k1, "2")
	ctx, cancel := runner.NewCtx()
	_, err = cli.Put(ctx, k1, "bar1")
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to put: %v", err)

		return
	}

	ctx, cancel = runner.NewCtx()
	_, err = cli.Put(ctx, k2, "bar2")
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to put: %v", err)

		return
	}

	// cache in lKV1
	ctx, cancel = runner.NewCtx()
	_, err = lKV1.Get(ctx, k1)
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to get: %v", err)

		return
	}

	ctx, cancel = runner.NewCtx()
	_, err = lKV1.Get(ctx, k2)
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to get: %v", err)

		return
	}

	// invalidate via lKV2 Txn
	k3 := path.Join(k1, "3")
	opArray := make([]clientv3.Op, 0, 1)
	opArray = append(opArray, clientv3.OpPut(k2, "bar2.1"))
	ctx, cancel = runner.NewCtx()
	tresp, err := lKV2.Txn(ctx).
		Then(
			clientv3.OpTxn(nil, opArray, nil),
			clientv3.OpPut(k1, "bar1.1"),
			clientv3.OpPut(k3, "bar3"), // + a key not in any cache
		).
		Commit()
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to txn: %v", err)

		return
	}
	if !tresp.Succeeded || len(tresp.Responses) != 3 {
		result.Success = false
		result.Output = fmt.Sprintf("txn failed: %+v", tresp)

		return
	}

	// check cache was invalidated
	ctx, cancel = runner.NewCtx()
	gresp, err := lKV1.Get(ctx, k1)
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to get: %v", err)

		return
	}

	if len(gresp.Kvs) != 1 || string(gresp.Kvs[0].Value) != "bar1.1" {
		result.Success = false
		result.Output = fmt.Sprintf("cache mismatch: %+v", gresp)

		return
	}

	ctx, cancel = runner.NewCtx()
	gresp, err = lKV1.Get(ctx, k2)
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to get: %v", err)

		return
	}

	if len(gresp.Kvs) != 1 || string(gresp.Kvs[0].Value) != "bar2.1" {
		result.Success = false
		result.Output = fmt.Sprintf("cache mismatch: %+v", gresp)

		return
	}

	cctx, ccancel := context.WithCancel(context.Background())
	defer ccancel()

	// check puts were applied and are all in the same revision
	wch := cli.Watch(
		cctx,
		k1,
		clientv3.WithRev(tresp.Header.GetRevision()),
		clientv3.WithPrefix(),
	)
	var wr clientv3.WatchResponse
	select {
	case wr = <-wch:
	case <-time.After(5 * time.Second):
		result.Success = false
		result.Output = watchEventTimeoutMsg

		return
	}
	c := 0
	for _, ev := range wr.Events {
		if ev.Kv.ModRevision == tresp.Header.GetRevision() {
			c++
		}
	}
	if c != 3 {
		result.Success = false
		result.Output = fmt.Sprintf("expected 3 events, got %d", c)

		return
	}
}

package scenarios

import (
	"errors"
	"fmt"

	testtime "git.tbd/etcd-infra/internal/etcd/testtime"
	"go.etcd.io/etcd/api/v3/v3rpc/rpctypes"
	clientv3 "go.etcd.io/etcd/client/v3"

	logutil "git.tbd/etcd-infra/pkg/log"
)

// RunPutWithIgnoreLease tests the PutWithIgnoreLease scenario.
func RunPutWithIgnoreLease(runner Runner) {
	logutil.S().Infow("running", "scenario", PutWithIgnoreLease.String())

	result := &Result{
		Scenario:  PutWithIgnoreLease.String(),
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

	ctx, cancel := runner.NewCtx()
	lresp, err := cli.Grant(ctx, 10)
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to grant lease: %v", err)

		return
	}
	leaseID := lresp.ID

	testKey := runner.GenerateRandomKey(10)

	ctx, cancel = runner.NewCtx()
	_, err = cli.Put(ctx, testKey, "bar1", clientv3.WithIgnoreLease())
	cancel()
	if !errors.Is(err, rpctypes.ErrKeyNotFound) {
		result.Success = false
		result.Output = fmt.Sprintf("expected ErrKeyNotFound, got %v", err)

		return
	}

	ctx, cancel = runner.NewCtx()
	_, err = cli.Put(ctx, testKey, "bar2", clientv3.WithLease(leaseID))
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to put with lease: %v", err)

		return
	}

	ctx, cancel = runner.NewCtx()
	_, err = cli.Put(ctx, testKey, "bar3", clientv3.WithIgnoreLease())
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to put with ignore lease: %v", err)

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
	if len(gresp.Kvs) != 1 {
		result.Success = false
		result.Output = fmt.Sprintf("unexpected number of keys: %d", len(gresp.Kvs))

		return
	}
	if string(gresp.Kvs[0].Value) != "bar3" {
		result.Success = false
		result.Output = fmt.Sprintf("unexpected value: %q", string(gresp.Kvs[0].Value))

		return
	}
	if gresp.Kvs[0].Version != 2 {
		result.Success = false
		result.Output = fmt.Sprintf("unexpected version: %d", gresp.Kvs[0].Version)

		return
	}
	if gresp.Kvs[0].Lease != int64(leaseID) {
		result.Success = false
		result.Output = fmt.Sprintf("unexpected lease: %d, expected %d", gresp.Kvs[0].Lease, leaseID)

		return
	}
}

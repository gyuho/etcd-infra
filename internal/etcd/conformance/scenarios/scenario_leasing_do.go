package scenarios

import (
	"fmt"

	testtime "git.tbd/etcd-infra/internal/etcd/testtime"
	clientv3 "go.etcd.io/etcd/client/v3"
	"go.etcd.io/etcd/client/v3/leasing"

	logutil "git.tbd/etcd-infra/pkg/log"
)

// RunLeasingDo tests the LeasingDo scenario.
func RunLeasingDo(runner Runner) {
	logutil.S().Infow("running", "scenario", LeasingDo.String())

	result := &Result{
		Scenario:  LeasingDo.String(),
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

	// Use random test key prefix to avoid conflicts with other data.
	// IMPORTANT: Never use hardcoded prefixes like "a" that could delete
	// Kubernetes data or other keys when running against a live cluster.
	testKey := runner.GenerateRandomKey(10)

	ops := []clientv3.Op{
		clientv3.OpTxn(nil, nil, nil),
		clientv3.OpGet(testKey),
		clientv3.OpPut(testKey+"/abc", "v"),
		clientv3.OpDelete(testKey, clientv3.WithPrefix()),
		clientv3.OpTxn(nil, nil, nil),
	}
	for i := range ops {
		op := &ops[i]
		ctx, cancel := runner.NewCtx()
		resp, doErr := lKV.Do(ctx, *op)
		cancel()
		if doErr != nil {
			result.Success = false
			result.Output = fmt.Sprintf("#%d: Do failed (%v)", i, doErr)

			return
		}
		switch {
		case op.IsGet() && resp.Get() == nil:
			result.Success = false
			result.Output = fmt.Sprintf("#%d: get but nil get response", i)
		case op.IsPut() && resp.Put() == nil:
			result.Success = false
			result.Output = fmt.Sprintf("#%d: put op but nil get response", i)
		case op.IsDelete() && resp.Del() == nil:
			result.Success = false
			result.Output = fmt.Sprintf("#%d: delete op but nil delete response", i)
		case op.IsTxn() && resp.Txn() == nil:
			result.Success = false
			result.Output = fmt.Sprintf("#%d: txn op but nil txn response", i)
		}
	}

	ctx, cancel := runner.NewCtx()
	gresp, err := cli.Get(ctx, testKey, clientv3.WithPrefix())
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("Get failed (%v)", err)

		return
	}
	if len(gresp.Kvs) != 0 {
		result.Success = false
		result.Output = fmt.Sprintf("expected no keys, got %+v", gresp.Kvs)

		return
	}
}

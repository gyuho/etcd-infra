package scenarios

import (
	"fmt"
	"path"

	testtime "git.tbd/etcd-infra/internal/etcd/testtime"
	clientv3 "go.etcd.io/etcd/client/v3"

	logutil "git.tbd/etcd-infra/pkg/log"
)

// RunTxnNested tests the TxnNested scenario.
func RunTxnNested(runner Runner) {
	logutil.S().Infow("running", "scenario", TxnNested.String())

	result := &Result{
		Scenario:  TxnNested.String(),
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

	testKey := runner.GenerateRandomKey(10)

	ctx, cancel := runner.NewCtx()
	tresp, err := cli.Txn(ctx).
		If(clientv3.Compare(clientv3.Version(testKey), "=", 0)).
		Then(
			clientv3.OpPut(testKey, "bar"),
			clientv3.OpTxn(nil, []clientv3.Op{clientv3.OpPut(path.Join(testKey, "abc"), "123")}, nil)).
		Else(clientv3.OpPut(testKey, "baz")).
		Commit()
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to execute txn: %v", err)

		return
	}
	if len(tresp.Responses) != 2 {
		result.Success = false
		result.Output = fmt.Sprintf("expected 2 responses, got %d", len(tresp.Responses))

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
		result.Output = fmt.Sprintf("expected 1 key, got %d", len(gresp.Kvs))

		return
	}
	if string(gresp.Kvs[0].Value) != "bar" {
		result.Success = false
		result.Output = "expected value bar, got " + string(gresp.Kvs[0].Value)

		return
	}

	ctx, cancel = runner.NewCtx()
	gresp, err = cli.Get(ctx, path.Join(testKey, "abc"))
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to get: %v", err)

		return
	}
	if len(gresp.Kvs) != 1 {
		result.Success = false
		result.Output = fmt.Sprintf("expected 1 key, got %d", len(gresp.Kvs))

		return
	}
	if string(gresp.Kvs[0].Value) != "123" {
		result.Success = false
		result.Output = "expected value 123, got " + string(gresp.Kvs[0].Value)

		return
	}
}

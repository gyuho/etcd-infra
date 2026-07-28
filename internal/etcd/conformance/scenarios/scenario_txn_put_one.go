package scenarios

import (
	"fmt"

	testtime "git.tbd/etcd-infra/internal/etcd/testtime"
	clientv3 "go.etcd.io/etcd/client/v3"

	logutil "git.tbd/etcd-infra/pkg/log"
)

// RunTxnPutOne tests the TxnPutOne scenario.
func RunTxnPutOne(runner Runner) {
	logutil.S().Infow("running", "scenario", TxnPutOne.String())

	result := &Result{
		Scenario:  TxnPutOne.String(),
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
	_, err = cli.Txn(ctx).
		Then(clientv3.OpPut(testKey, "xyz")).
		Commit()
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to execute txn: %v", err)

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
	if string(gresp.Kvs[0].Value) != "xyz" {
		result.Success = false
		result.Output = "expected value xyz, got " + string(gresp.Kvs[0].Value)

		return
	}

	ctx, cancel = runner.NewCtx()
	_, err = cli.Txn(ctx).
		// txn value comparisons are lexical
		If(clientv3.Compare(clientv3.Value(testKey), ">", "abc")).
		Then(clientv3.OpPut(testKey, "THEN")). // "Then" should run, since "xyz" > "abc"
		Else(clientv3.OpPut(testKey, "ELSE")). // "Else" should not run
		Commit()
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to execute txn: %v", err)

		return
	}

	ctx, cancel = runner.NewCtx()
	gresp, err = cli.Get(ctx, testKey)
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
	if string(gresp.Kvs[0].Value) != "THEN" {
		result.Success = false
		result.Output = "expected value THEN, got " + string(gresp.Kvs[0].Value)

		return
	}
}

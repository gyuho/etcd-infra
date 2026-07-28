package scenarios

import (
	"fmt"

	testtime "git.tbd/etcd-infra/internal/etcd/testtime"
	clientv3 "go.etcd.io/etcd/client/v3"

	logutil "git.tbd/etcd-infra/pkg/log"
)

// RunTxnCompareCreaterevision tests CreateRevision compare for distinguishing creates from updates.
// This pattern is used by Kubernetes store.go#create to ensure a key doesn't already exist.
// ref. "clientv3/clientv3util/ExampleKeyMissing".
func RunTxnCompareCreaterevision(runner Runner) {
	logutil.S().Infow("running", "scenario", TxnCompareCreaterevision.String())

	result := &Result{
		Scenario:  TxnCompareCreaterevision.String(),
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

	// Test 1: CreateRevision == 0 compare should succeed for non-existent key
	ctx, cancel := runner.NewCtx()
	txnResp, err := cli.Txn(ctx).
		If(clientv3.Compare(clientv3.CreateRevision(testKey), "=", 0)).
		Then(clientv3.OpPut(testKey, "created")).
		Else(clientv3.OpGet(testKey)).
		Commit()
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to execute create txn: %v", err)

		return
	}
	if !txnResp.Succeeded {
		result.Success = false
		result.Output = "expected CreateRevision==0 compare to succeed for new key"

		return
	}
	if len(txnResp.Responses) != 1 || txnResp.Responses[0].GetResponsePut() == nil {
		result.Success = false
		result.Output = "expected Put response from successful create"

		return
	}
	createRev := txnResp.Header.Revision

	// Verify the key was created
	ctx, cancel = runner.NewCtx()
	getResp, err := cli.Get(ctx, testKey)
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to get created key: %v", err)

		return
	}
	if len(getResp.Kvs) != 1 {
		result.Success = false
		result.Output = fmt.Sprintf("expected 1 key, got %d", len(getResp.Kvs))

		return
	}
	if getResp.Kvs[0].CreateRevision != createRev {
		result.Success = false
		result.Output = fmt.Sprintf("expected CreateRevision %d, got %d", createRev, getResp.Kvs[0].CreateRevision)

		return
	}

	// Test 2: CreateRevision == 0 compare should fail for existing key
	ctx, cancel = runner.NewCtx()
	txnResp, err = cli.Txn(ctx).
		If(clientv3.Compare(clientv3.CreateRevision(testKey), "=", 0)).
		Then(clientv3.OpPut(testKey, "should-not-overwrite")).
		Else(clientv3.OpGet(testKey)).
		Commit()
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to execute duplicate create txn: %v", err)

		return
	}
	if txnResp.Succeeded {
		result.Success = false
		result.Output = "expected CreateRevision==0 compare to fail for existing key"

		return
	}
	if len(txnResp.Responses) != 1 || txnResp.Responses[0].GetResponseRange() == nil {
		result.Success = false
		result.Output = "expected Range response from failed create"

		return
	}
	rangeResp := txnResp.Responses[0].GetResponseRange()
	if string(rangeResp.Kvs[0].Value) != "created" {
		result.Success = false
		result.Output = "expected original value 'created', got " + string(rangeResp.Kvs[0].Value)

		return
	}

	// Test 3: CreateRevision > 0 compare should succeed for existing key
	ctx, cancel = runner.NewCtx()
	txnResp, err = cli.Txn(ctx).
		If(clientv3.Compare(clientv3.CreateRevision(testKey), ">", 0)).
		Then(clientv3.OpPut(testKey, "updated")).
		Commit()
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to execute update txn: %v", err)

		return
	}
	if !txnResp.Succeeded {
		result.Success = false
		result.Output = "expected CreateRevision>0 compare to succeed for existing key"

		return
	}

	// Verify the key was updated but CreateRevision is unchanged
	ctx, cancel = runner.NewCtx()
	getResp, err = cli.Get(ctx, testKey)
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to get updated key: %v", err)

		return
	}
	if string(getResp.Kvs[0].Value) != "updated" {
		result.Success = false
		result.Output = "expected value 'updated', got " + string(getResp.Kvs[0].Value)

		return
	}
	if getResp.Kvs[0].CreateRevision != createRev {
		result.Success = false
		result.Output = fmt.Sprintf("CreateRevision should remain %d after update, got %d", createRev, getResp.Kvs[0].CreateRevision)

		return
	}
	if getResp.Kvs[0].ModRevision == createRev {
		result.Success = false
		result.Output = "ModRevision should have changed after update"

		return
	}

	// Test 4: CreateRevision == specific value compare
	ctx, cancel = runner.NewCtx()
	txnResp, err = cli.Txn(ctx).
		If(clientv3.Compare(clientv3.CreateRevision(testKey), "=", createRev)).
		Then(clientv3.OpPut(testKey, "matched-create-rev")).
		Commit()
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to execute specific create rev txn: %v", err)

		return
	}
	if !txnResp.Succeeded {
		result.Success = false
		result.Output = fmt.Sprintf("expected CreateRevision==%d compare to succeed", createRev)

		return
	}
}

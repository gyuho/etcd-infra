package scenarios

import (
	"fmt"

	testtime "git.tbd/etcd-infra/internal/etcd/testtime"
	clientv3 "go.etcd.io/etcd/client/v3"

	logutil "git.tbd/etcd-infra/pkg/log"
)

// RunTxnCompareVersion tests Version compare for optimistic locking semantics.
// Version counts the number of times a key has been modified since creation.
// This pattern is used by Kubernetes for detecting concurrent modifications.
func RunTxnCompareVersion(runner Runner) {
	logutil.S().Infow("running", "scenario", TxnCompareVersion.String())

	result := &Result{
		Scenario:  TxnCompareVersion.String(),
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

	// Create initial key - Version should be 1
	ctx, cancel := runner.NewCtx()
	_, err = cli.Put(ctx, testKey, "v1")
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to put v1: %v", err)

		return
	}

	// Verify Version is 1
	ctx, cancel = runner.NewCtx()
	getResp, err := cli.Get(ctx, testKey)
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to get: %v", err)

		return
	}
	if getResp.Kvs[0].Version != 1 {
		result.Success = false
		result.Output = fmt.Sprintf("expected Version 1 after create, got %d", getResp.Kvs[0].Version)

		return
	}

	// Test 1: Version == 1 compare should succeed
	ctx, cancel = runner.NewCtx()
	txnResp, err := cli.Txn(ctx).
		If(clientv3.Compare(clientv3.Version(testKey), "=", 1)).
		Then(clientv3.OpPut(testKey, "v2")).
		Else(clientv3.OpGet(testKey)).
		Commit()
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to execute version 1 txn: %v", err)

		return
	}
	if !txnResp.Succeeded {
		result.Success = false
		result.Output = "expected Version==1 compare to succeed"

		return
	}

	// Verify Version is now 2
	ctx, cancel = runner.NewCtx()
	getResp, err = cli.Get(ctx, testKey)
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to get after update: %v", err)

		return
	}
	if getResp.Kvs[0].Version != 2 {
		result.Success = false
		result.Output = fmt.Sprintf("expected Version 2 after update, got %d", getResp.Kvs[0].Version)

		return
	}

	// Test 2: Stale version compare should fail
	ctx, cancel = runner.NewCtx()
	txnResp, err = cli.Txn(ctx).
		If(clientv3.Compare(clientv3.Version(testKey), "=", 1)).
		Then(clientv3.OpPut(testKey, "should-not-apply")).
		Else(clientv3.OpGet(testKey)).
		Commit()
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to execute stale version txn: %v", err)

		return
	}
	if txnResp.Succeeded {
		result.Success = false
		result.Output = "expected stale Version==1 compare to fail"

		return
	}

	// Verify value is still v2
	rangeResp := txnResp.Responses[0].GetResponseRange()
	if string(rangeResp.Kvs[0].Value) != valueV2 {
		result.Success = false
		result.Output = fmt.Sprintf("expected value %s, got %s", valueV2, string(rangeResp.Kvs[0].Value))

		return
	}

	// Test 3: Version > 0 compare for existence check
	ctx, cancel = runner.NewCtx()
	txnResp, err = cli.Txn(ctx).
		If(clientv3.Compare(clientv3.Version(testKey), ">", 0)).
		Then(clientv3.OpPut(testKey, "v3")).
		Commit()
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to execute version > 0 txn: %v", err)

		return
	}
	if !txnResp.Succeeded {
		result.Success = false
		result.Output = "expected Version>0 compare to succeed for existing key"

		return
	}

	// Test 4: Version == 0 for non-existent key
	nonExistentKey := runner.GenerateRandomKey(10)
	ctx, cancel = runner.NewCtx()
	txnResp, err = cli.Txn(ctx).
		If(clientv3.Compare(clientv3.Version(nonExistentKey), "=", 0)).
		Then(clientv3.OpPut(nonExistentKey, "created")).
		Commit()
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to execute version == 0 txn: %v", err)

		return
	}
	if !txnResp.Succeeded {
		result.Success = false
		result.Output = "expected Version==0 compare to succeed for non-existent key"

		return
	}

	// Verify Version after deletion resets to 0
	ctx, cancel = runner.NewCtx()
	_, err = cli.Delete(ctx, nonExistentKey)
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to delete: %v", err)

		return
	}

	ctx, cancel = runner.NewCtx()
	txnResp, err = cli.Txn(ctx).
		If(clientv3.Compare(clientv3.Version(nonExistentKey), "=", 0)).
		Then(clientv3.OpPut(nonExistentKey, "recreated")).
		Commit()
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to execute post-delete version txn: %v", err)

		return
	}
	if !txnResp.Succeeded {
		result.Success = false
		result.Output = "expected Version==0 compare to succeed after delete"

		return
	}
}

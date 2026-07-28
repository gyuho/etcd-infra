package scenarios

import (
	"fmt"

	testtime "git.tbd/etcd-infra/internal/etcd/testtime"
	clientv3 "go.etcd.io/etcd/client/v3"

	logutil "git.tbd/etcd-infra/pkg/log"
)

// RunTxnCompareValue tests Value compare for UID-based precondition checks.
// This pattern is used by Kubernetes store.go#Delete to validate UID matches before deleting.
func RunTxnCompareValue(runner Runner) {
	logutil.S().Infow("running", "scenario", TxnCompareValue.String())

	result := &Result{
		Scenario:  TxnCompareValue.String(),
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
	uid := "uid-12345-abcde"
	wrongUID := "uid-wrong-xxxxx"

	// Create key with embedded UID
	ctx, cancel := runner.NewCtx()
	_, err = cli.Put(ctx, testKey, uid)
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to put: %v", err)

		return
	}

	// Test 1: Correct value compare should succeed
	ctx, cancel = runner.NewCtx()
	txnResp, err := cli.Txn(ctx).
		If(clientv3.Compare(clientv3.Value(testKey), "=", uid)).
		Then(clientv3.OpDelete(testKey)).
		Else(clientv3.OpGet(testKey)).
		Commit()
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to execute correct value txn: %v", err)

		return
	}
	if !txnResp.Succeeded {
		result.Success = false
		result.Output = "expected correct value compare to succeed"

		return
	}
	if len(txnResp.Responses) != 1 || txnResp.Responses[0].GetResponseDeleteRange() == nil {
		result.Success = false
		result.Output = "expected delete response"

		return
	}
	if txnResp.Responses[0].GetResponseDeleteRange().Deleted != 1 {
		result.Success = false
		result.Output = "expected 1 key to be deleted"

		return
	}

	// Recreate for next tests
	ctx, cancel = runner.NewCtx()
	_, err = cli.Put(ctx, testKey, uid)
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to recreate: %v", err)

		return
	}

	// Test 2: Wrong value compare should fail
	ctx, cancel = runner.NewCtx()
	txnResp, err = cli.Txn(ctx).
		If(clientv3.Compare(clientv3.Value(testKey), "=", wrongUID)).
		Then(clientv3.OpDelete(testKey)).
		Else(clientv3.OpGet(testKey)).
		Commit()
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to execute wrong value txn: %v", err)

		return
	}
	if txnResp.Succeeded {
		result.Success = false
		result.Output = "expected wrong value compare to fail"

		return
	}
	rangeResp := txnResp.Responses[0].GetResponseRange()
	if rangeResp == nil || len(rangeResp.Kvs) != 1 {
		result.Success = false
		result.Output = "expected range response with 1 key"

		return
	}
	if string(rangeResp.Kvs[0].Value) != uid {
		result.Success = false
		result.Output = fmt.Sprintf("expected original value %s, got %s", uid, string(rangeResp.Kvs[0].Value))

		return
	}

	// Verify key still exists
	ctx, cancel = runner.NewCtx()
	getResp, err := cli.Get(ctx, testKey)
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to get after failed delete: %v", err)

		return
	}
	if len(getResp.Kvs) != 1 {
		result.Success = false
		result.Output = "key should not have been deleted with wrong UID"

		return
	}

	// Test 3: Value != compare
	ctx, cancel = runner.NewCtx()
	txnResp, err = cli.Txn(ctx).
		If(clientv3.Compare(clientv3.Value(testKey), "!=", wrongUID)).
		Then(clientv3.OpPut(testKey, "updated-after-ne-check")).
		Commit()
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to execute != value txn: %v", err)

		return
	}
	if !txnResp.Succeeded {
		result.Success = false
		result.Output = "expected != compare to succeed when values differ"

		return
	}

	// Test 4: Combined ModRevision and Value compare (common K8s pattern)
	ctx, cancel = runner.NewCtx()
	getResp, err = cli.Get(ctx, testKey)
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to get for combined check: %v", err)

		return
	}
	modRev := getResp.Kvs[0].ModRevision
	currentValue := string(getResp.Kvs[0].Value)

	ctx, cancel = runner.NewCtx()
	txnResp, err = cli.Txn(ctx).
		If(
			clientv3.Compare(clientv3.ModRevision(testKey), "=", modRev),
			clientv3.Compare(clientv3.Value(testKey), "=", currentValue),
		).
		Then(clientv3.OpDelete(testKey)).
		Commit()
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to execute combined txn: %v", err)

		return
	}
	if !txnResp.Succeeded {
		result.Success = false
		result.Output = "expected combined ModRevision+Value compare to succeed"

		return
	}

	// Verify deletion
	ctx, cancel = runner.NewCtx()
	getResp, err = cli.Get(ctx, testKey)
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to verify deletion: %v", err)

		return
	}
	if len(getResp.Kvs) != 0 {
		result.Success = false
		result.Output = "key should have been deleted"

		return
	}
}

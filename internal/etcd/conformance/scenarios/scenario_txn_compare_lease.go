package scenarios

import (
	"fmt"

	testtime "git.tbd/etcd-infra/internal/etcd/testtime"
	clientv3 "go.etcd.io/etcd/client/v3"

	logutil "git.tbd/etcd-infra/pkg/log"
)

// RunTxnCompareLease tests Lease compare operations in transactions.
// This allows conditional operations based on whether a key has a specific lease attached.
// ref. "clientv3/integration/TestTxnLease".
func RunTxnCompareLease(runner Runner) {
	logutil.S().Infow("running", "scenario", TxnCompareLease.String())

	result := &Result{
		Scenario:  TxnCompareLease.String(),
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

	// Create a lease
	ctx, cancel := runner.NewCtx()
	leaseResp, err := cli.Grant(ctx, 60)
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to grant lease: %v", err)

		return
	}
	leaseID := leaseResp.ID

	// Put key with lease
	ctx, cancel = runner.NewCtx()
	_, err = cli.Put(ctx, testKey, "value-with-lease", clientv3.WithLease(leaseID))
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to put with lease: %v", err)

		return
	}

	// Test 1: Compare with correct lease should succeed
	ctx, cancel = runner.NewCtx()
	txnResp, err := cli.Txn(ctx).
		If(clientv3.Compare(clientv3.LeaseValue(testKey), "=", leaseID)).
		Then(clientv3.OpPut(testKey, "updated-with-same-lease", clientv3.WithLease(leaseID))).
		Else(clientv3.OpGet(testKey)).
		Commit()
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to execute lease compare txn: %v", err)

		return
	}
	if !txnResp.Succeeded {
		result.Success = false
		result.Output = "expected lease compare to succeed with matching lease"

		return
	}

	// Test 2: Compare with wrong lease should fail
	ctx, cancel = runner.NewCtx()
	wrongLeaseResp, err := cli.Grant(ctx, 60)
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to grant wrong lease: %v", err)

		return
	}
	wrongLeaseID := wrongLeaseResp.ID

	ctx, cancel = runner.NewCtx()
	txnResp, err = cli.Txn(ctx).
		If(clientv3.Compare(clientv3.LeaseValue(testKey), "=", wrongLeaseID)).
		Then(clientv3.OpPut(testKey, "should-not-happen")).
		Else(clientv3.OpGet(testKey)).
		Commit()
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to execute wrong lease compare txn: %v", err)

		return
	}
	if txnResp.Succeeded {
		result.Success = false
		result.Output = "expected lease compare to fail with non-matching lease"

		return
	}
	if len(txnResp.Responses) != 1 || txnResp.Responses[0].GetResponseRange() == nil {
		result.Success = false
		result.Output = "expected Get response from else branch"

		return
	}
	rangeResp := txnResp.Responses[0].GetResponseRange()
	if string(rangeResp.Kvs[0].Value) != "updated-with-same-lease" {
		result.Success = false
		result.Output = fmt.Sprintf("expected value 'updated-with-same-lease', got '%s'", string(rangeResp.Kvs[0].Value))

		return
	}

	// Test 3: Compare with no lease (lease ID 0) for key without lease
	testKey2 := runner.GenerateRandomKey(10)
	ctx, cancel = runner.NewCtx()
	_, err = cli.Put(ctx, testKey2, "no-lease-value")
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to put without lease: %v", err)

		return
	}

	ctx, cancel = runner.NewCtx()
	txnResp, err = cli.Txn(ctx).
		If(clientv3.Compare(clientv3.LeaseValue(testKey2), "=", 0)).
		Then(clientv3.OpPut(testKey2, "confirmed-no-lease")).
		Else(clientv3.OpGet(testKey2)).
		Commit()
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to execute no-lease compare txn: %v", err)

		return
	}
	if !txnResp.Succeeded {
		result.Success = false
		result.Output = "expected no-lease compare to succeed for key without lease"

		return
	}

	// Clean up
	ctx, cancel = runner.NewCtx()
	_, _ = cli.Revoke(ctx, leaseID)
	cancel()
	ctx, cancel = runner.NewCtx()
	_, _ = cli.Revoke(ctx, wrongLeaseID)
	cancel()

	result.Output = "lease compare transactions work correctly for matching, non-matching, and no-lease cases"
}

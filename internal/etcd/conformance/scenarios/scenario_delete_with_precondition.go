package scenarios

import (
	"fmt"

	testtime "git.tbd/etcd-infra/internal/etcd/testtime"
	clientv3 "go.etcd.io/etcd/client/v3"

	logutil "git.tbd/etcd-infra/pkg/log"
)

// RunDeleteWithPrecondition validates delete preconditions using ModRevision and value compares.
func RunDeleteWithPrecondition(runner Runner) {
	logutil.S().Infow("running", "scenario", DeleteWithPrecondition.String())

	result := &Result{
		Scenario:  DeleteWithPrecondition.String(),
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
	putResp, err := cli.Put(ctx, testKey, "uid=abc")
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to put: %v", err)

		return
	}
	rev := putResp.Header.GetRevision()

	ctx, cancel = runner.NewCtx()
	txnResp, err := cli.Txn(ctx).
		If(
			clientv3.Compare(clientv3.ModRevision(testKey), "=", rev),
			clientv3.Compare(clientv3.Value(testKey), "=", "uid=abc"),
		).
		Then(clientv3.OpDelete(testKey)).
		Commit()
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to run delete txn: %v", err)

		return
	}
	if !txnResp.Succeeded {
		result.Success = false
		result.Output = "delete txn should have succeeded with matching preconditions"

		return
	}
	if len(txnResp.Responses) != 1 || txnResp.Responses[0].GetResponseDeleteRange() == nil {
		result.Success = false
		result.Output = "delete txn missing delete response"

		return
	}
	if txnResp.Responses[0].GetResponseDeleteRange().Deleted != 1 {
		result.Success = false
		result.Output = fmt.Sprintf("expected delete to remove 1 key, got %d", txnResp.Responses[0].GetResponseDeleteRange().Deleted)

		return
	}

	ctx, cancel = runner.NewCtx()
	getResp, err := cli.Get(ctx, testKey)
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to get after delete: %v", err)

		return
	}
	if len(getResp.Kvs) != 0 {
		result.Success = false
		result.Output = fmt.Sprintf("expected key to be deleted, got %d entries", len(getResp.Kvs))

		return
	}

	ctx, cancel = runner.NewCtx()
	_, err = cli.Put(ctx, testKey, "uid=xyz")
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to re-put: %v", err)

		return
	}

	ctx, cancel = runner.NewCtx()
	txnResp, err = cli.Txn(ctx).
		If(
			clientv3.Compare(clientv3.ModRevision(testKey), "=", rev),
			clientv3.Compare(clientv3.Value(testKey), "=", "uid=abc"),
		).
		Then(clientv3.OpDelete(testKey)).
		Else(clientv3.OpGet(testKey)).
		Commit()
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to run stale delete txn: %v", err)

		return
	}
	if txnResp.Succeeded {
		result.Success = false
		result.Output = "delete txn should have failed with stale preconditions"

		return
	}
	if len(txnResp.Responses) != 1 || txnResp.Responses[0].GetResponseRange() == nil {
		result.Success = false
		result.Output = "stale delete txn missing range response"

		return
	}
	if len(txnResp.Responses[0].GetResponseRange().Kvs) != 1 {
		result.Success = false
		result.Output = fmt.Sprintf("expected stale delete txn to return key, got %d", len(txnResp.Responses[0].GetResponseRange().Kvs))

		return
	}
	if string(txnResp.Responses[0].GetResponseRange().Kvs[0].Value) != "uid=xyz" {
		result.Success = false
		result.Output = fmt.Sprintf("expected stored value uid=xyz, got %q", string(txnResp.Responses[0].GetResponseRange().Kvs[0].Value))

		return
	}
}

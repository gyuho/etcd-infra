package scenarios

import (
	"fmt"

	testtime "git.tbd/etcd-infra/internal/etcd/testtime"
	clientv3 "go.etcd.io/etcd/client/v3"

	logutil "git.tbd/etcd-infra/pkg/log"
)

// RunTxnCompareModrevision tests the TxnCompareModrevision scenario.
func RunTxnCompareModrevision(runner Runner) {
	logutil.S().Infow("running", "scenario", TxnCompareModrevision.String())

	result := &Result{
		Scenario:  TxnCompareModrevision.String(),
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
	putResp, err := cli.Put(ctx, testKey, "v1")
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to put v1: %v", err)

		return
	}
	initialRev := putResp.Header.Revision

	ctx, cancel = runner.NewCtx()
	tresp, err := cli.Txn(ctx).
		If(clientv3.Compare(clientv3.ModRevision(testKey), "=", initialRev)).
		Then(clientv3.OpPut(testKey, "v2")).
		Else(clientv3.OpGet(testKey)).
		Commit()
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to execute txn: %v", err)

		return
	}
	if !tresp.Succeeded {
		result.Success = false
		result.Output = "expected initial ModRevision compare to succeed"

		return
	}
	if len(tresp.Responses) != 1 || tresp.Responses[0].GetResponsePut() == nil {
		result.Success = false
		result.Output = "expected single Put response from successful txn"

		return
	}

	ctx, cancel = runner.NewCtx()
	getResp, err := cli.Get(ctx, testKey)
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to get v2: %v", err)

		return
	}
	if len(getResp.Kvs) != 1 {
		result.Success = false
		result.Output = fmt.Sprintf("expected 1 key after update, got %d", len(getResp.Kvs))

		return
	}
	if string(getResp.Kvs[0].Value) != valueV2 {
		result.Success = false
		result.Output = fmt.Sprintf("expected %s, got %s", valueV2, string(getResp.Kvs[0].Value))

		return
	}
	currentModRev := getResp.Kvs[0].ModRevision

	ctx, cancel = runner.NewCtx()
	tresp, err = cli.Txn(ctx).
		If(clientv3.Compare(clientv3.ModRevision(testKey), "=", initialRev)).
		Then(clientv3.OpPut(testKey, "v3")).
		Else(clientv3.OpGet(testKey)).
		Commit()
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to execute stale txn: %v", err)

		return
	}
	if tresp.Succeeded {
		result.Success = false
		result.Output = "expected stale ModRevision compare to fail"

		return
	}
	if len(tresp.Responses) != 1 || tresp.Responses[0].GetResponseRange() == nil {
		result.Success = false
		result.Output = "expected single Range response from failed txn"

		return
	}
	rangeResp := tresp.Responses[0].GetResponseRange()
	if len(rangeResp.Kvs) != 1 {
		result.Success = false
		result.Output = fmt.Sprintf("expected 1 key from failed txn, got %d", len(rangeResp.Kvs))

		return
	}
	if string(rangeResp.Kvs[0].Value) != valueV2 {
		result.Success = false
		result.Output = fmt.Sprintf("expected current value %s, got %s", valueV2, string(rangeResp.Kvs[0].Value))

		return
	}
	if rangeResp.Kvs[0].ModRevision != currentModRev {
		result.Success = false
		result.Output = fmt.Sprintf("expected ModRevision %d, got %d", currentModRev, rangeResp.Kvs[0].ModRevision)

		return
	}
}

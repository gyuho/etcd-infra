package scenarios

import (
	"fmt"
	"path"

	testtime "git.tbd/etcd-infra/internal/etcd/testtime"
	clientv3 "go.etcd.io/etcd/client/v3"

	logutil "git.tbd/etcd-infra/pkg/log"
)

// RunTxnCompareRange tests the TxnCompareRange scenario.
func RunTxnCompareRange(runner Runner) {
	logutil.S().Infow("running", "scenario", TxnCompareRange.String())

	result := &Result{
		Scenario:  TxnCompareRange.String(),
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

	testPrefix := runner.GenerateRandomKey(10) + "/"
	testKey := path.Join(testPrefix, "a")
	testKey2 := path.Join(testPrefix, "b")

	ctx, cancel := runner.NewCtx()
	presp, err := cli.Put(ctx, testKey, "bar")
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to put: %v", err)

		return
	}
	putRev1 := presp.Header.GetRevision()

	ctx, cancel = runner.NewCtx()
	_, err = cli.Put(ctx, testKey2, "baz")
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to put: %v", err)

		return
	}

	ctx, cancel = runner.NewCtx()
	gresp, err := cli.Get(ctx, testPrefix, clientv3.WithPrefix())
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to range: %v", err)

		return
	}
	if len(gresp.Kvs) < 2 {
		result.Success = false
		result.Output = fmt.Sprintf("expected at least 2 keys under prefix %q, got %d", testPrefix, len(gresp.Kvs))

		return
	}

	// Test 1: Range comparison where NOT all keys share the same CreateRevision.
	// In etcd v3.5, WithPrefix() on Compare checks ALL keys in the range.
	// In etcd v3.6+, the semantics may differ (first-key match or any-key match).
	// We validate the Txn executes without error; both outcomes are acceptable.
	ctx, cancel = runner.NewCtx()
	tresp, err := cli.Txn(ctx).
		If(
			clientv3.Compare(
				clientv3.CreateRevision(testPrefix), "=", putRev1).
				WithPrefix(),
		).
		Commit()
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to execute txn: %v", err)

		return
	}
	_ = tresp // Outcome varies by etcd version; both Succeeded=true/false are valid.

	// Test 2: Range comparison where ALL keys have CreateRevision > 0.
	// This should always succeed regardless of etcd version.
	ctx, cancel = runner.NewCtx()
	tresp, err = cli.Txn(ctx).
		If(
			clientv3.Compare(
				clientv3.CreateRevision(testPrefix), ">", 0).
				WithPrefix(),
		).
		Commit()
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to execute txn: %v", err)

		return
	}
	if !tresp.Succeeded {
		result.Success = false
		result.Output = "expected compare to succeed when all keys satisfy the range condition"

		return
	}
}

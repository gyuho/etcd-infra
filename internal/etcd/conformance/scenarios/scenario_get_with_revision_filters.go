package scenarios

import (
	"fmt"

	testtime "git.tbd/etcd-infra/internal/etcd/testtime"
	clientv3 "go.etcd.io/etcd/client/v3"

	logutil "git.tbd/etcd-infra/pkg/log"
)

// RunGetWithRevisionFilters verifies RangeRequest revision filters used by Kubernetes LIST requests
// with resourceVersionMatch=NotOlderThan to ensure min/max create and mod revision bounds are honored.
func RunGetWithRevisionFilters(runner Runner) {
	logutil.S().Infow("running", "scenario", GetWithRevisionFilters.String())

	result := &Result{
		Scenario:  GetWithRevisionFilters.String(),
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

	prefix := runner.GenerateRandomKey(12)
	keyA := prefix + "/a"
	keyB := prefix + "/b"

	ctx, cancel := runner.NewCtx()
	putA1, err := cli.Put(ctx, keyA, "value-a1")
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to seed keyA: %v", err)

		return
	}
	revA1 := putA1.Header.Revision

	ctx, cancel = runner.NewCtx()
	putB1, err := cli.Put(ctx, keyB, "value-b1")
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to seed keyB: %v", err)

		return
	}
	revB1 := putB1.Header.Revision

	ctx, cancel = runner.NewCtx()
	putA2, err := cli.Put(ctx, keyA, "value-a2")
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to update keyA: %v", err)

		return
	}
	revA2 := putA2.Header.Revision

	if revA2 <= revB1 {
		result.Success = false
		result.Output = "expected keyA second revision to be greater than keyB revision"

		return
	}

	if err := expectSingleKey(cli, runner, prefix, keyA,
		clientv3.WithPrefix(),
		clientv3.WithMinModRev(revA2)); err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("min mod revision filter failed: %v", err)

		return
	}

	if err := expectSingleKey(cli, runner, prefix, keyB,
		clientv3.WithPrefix(),
		clientv3.WithMaxModRev(revB1)); err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("max mod revision filter failed: %v", err)

		return
	}

	if err := expectSingleKey(cli, runner, prefix, keyB,
		clientv3.WithPrefix(),
		clientv3.WithMinCreateRev(revB1)); err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("min create revision filter failed: %v", err)

		return
	}

	if err := expectSingleKey(cli, runner, prefix, keyA,
		clientv3.WithPrefix(),
		clientv3.WithMaxCreateRev(revA1)); err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("max create revision filter failed: %v", err)

		return
	}
}

func expectSingleKey(cli *clientv3.Client, runner Runner, prefix, wantKey string, opts ...clientv3.OpOption) error {
	ctx, cancel := runner.NewCtx()
	defer cancel()
	resp, err := cli.Get(ctx, prefix, opts...)
	if err != nil {
		return fmt.Errorf("get failed: %w", err)
	}
	if len(resp.Kvs) != 1 {
		return fmt.Errorf("expected 1 kv, got %d", len(resp.Kvs))
	}
	if string(resp.Kvs[0].Key) != wantKey {
		return fmt.Errorf("expected key %s, got %s", wantKey, string(resp.Kvs[0].Key))
	}

	return nil
}

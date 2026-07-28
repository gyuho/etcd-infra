package scenarios

import (
	"fmt"

	testtime "git.tbd/etcd-infra/internal/etcd/testtime"
	clientv3 "go.etcd.io/etcd/client/v3"

	logutil "git.tbd/etcd-infra/pkg/log"
)

// RunPutWithPrevKv verifies Put operations return previous key-value when WithPrevKV is specified.
// Kubernetes uses WithPrevKV on puts to verify previous values for conflict detection and audit trails.
// ref. "clientv3/integration/TestKVPut".
func RunPutWithPrevKv(runner Runner) {
	logutil.S().Infow("running", "scenario", PutWithPrevKv.String())

	result := &Result{
		Scenario:  PutWithPrevKv.String(),
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

	// Test 1: First put should have no PrevKv
	ctx, cancel := runner.NewCtx()
	putResp, err := cli.Put(ctx, testKey, valueV1, clientv3.WithPrevKV())
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to put %s: %v", valueV1, err)

		return
	}
	if putResp.PrevKv != nil {
		result.Success = false
		result.Output = "expected no PrevKv on first put"

		return
	}
	createRev := putResp.Header.Revision

	// Test 2: Second put should return previous value
	ctx, cancel = runner.NewCtx()
	putResp, err = cli.Put(ctx, testKey, valueV2, clientv3.WithPrevKV())
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to put %s: %v", valueV2, err)

		return
	}
	if putResp.PrevKv == nil {
		result.Success = false
		result.Output = "expected PrevKv on update"

		return
	}
	if string(putResp.PrevKv.Key) != testKey {
		result.Success = false
		result.Output = fmt.Sprintf("expected PrevKv key %s, got %s", testKey, string(putResp.PrevKv.Key))

		return
	}
	if string(putResp.PrevKv.Value) != valueV1 {
		result.Success = false
		result.Output = fmt.Sprintf("expected PrevKv value %s, got %s", valueV1, string(putResp.PrevKv.Value))

		return
	}
	if putResp.PrevKv.CreateRevision != createRev {
		result.Success = false
		result.Output = fmt.Sprintf("expected PrevKv CreateRevision %d, got %d",
			createRev, putResp.PrevKv.CreateRevision)

		return
	}
	if putResp.PrevKv.ModRevision != createRev {
		result.Success = false
		result.Output = fmt.Sprintf("expected PrevKv ModRevision %d (first version), got %d",
			createRev, putResp.PrevKv.ModRevision)

		return
	}
	if putResp.PrevKv.Version != 1 {
		result.Success = false
		result.Output = fmt.Sprintf("expected PrevKv Version 1, got %d", putResp.PrevKv.Version)

		return
	}
	modRev2 := putResp.Header.Revision

	// Test 3: Third put should return the second value
	ctx, cancel = runner.NewCtx()
	putResp, err = cli.Put(ctx, testKey, "v3", clientv3.WithPrevKV())
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to put v3: %v", err)

		return
	}
	if putResp.PrevKv == nil {
		result.Success = false
		result.Output = "expected PrevKv on third put"

		return
	}
	if string(putResp.PrevKv.Value) != valueV2 {
		result.Success = false
		result.Output = fmt.Sprintf("expected PrevKv value %s, got %s", valueV2, string(putResp.PrevKv.Value))

		return
	}
	if putResp.PrevKv.ModRevision != modRev2 {
		result.Success = false
		result.Output = fmt.Sprintf("expected PrevKv ModRevision %d, got %d",
			modRev2, putResp.PrevKv.ModRevision)

		return
	}
	if putResp.PrevKv.Version != 2 {
		result.Success = false
		result.Output = fmt.Sprintf("expected PrevKv Version 2, got %d", putResp.PrevKv.Version)

		return
	}

	// Test 4: Put without WithPrevKV should not return PrevKv
	ctx, cancel = runner.NewCtx()
	putResp, err = cli.Put(ctx, testKey, "v4")
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to put v4: %v", err)

		return
	}
	if putResp.PrevKv != nil {
		result.Success = false
		result.Output = "expected no PrevKv when option not specified"

		return
	}

	// Test 5: Put to new key should have no PrevKv
	newKey := runner.GenerateRandomKey(10)
	ctx, cancel = runner.NewCtx()
	putResp, err = cli.Put(ctx, newKey, "new-value", clientv3.WithPrevKV())
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to put new key: %v", err)

		return
	}
	if putResp.PrevKv != nil {
		result.Success = false
		result.Output = "expected no PrevKv for new key"

		return
	}

	// Test 6: Put with lease should preserve PrevKv (without lease)
	testKeyWithLease := runner.GenerateRandomKey(10)
	ctx, cancel = runner.NewCtx()
	lease, err := cli.Grant(ctx, 60)
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to grant lease: %v", err)

		return
	}

	// First put without lease
	ctx, cancel = runner.NewCtx()
	_, err = cli.Put(ctx, testKeyWithLease, "no-lease")
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to put without lease: %v", err)

		return
	}

	// Second put with lease should return PrevKv
	ctx, cancel = runner.NewCtx()
	putResp, err = cli.Put(ctx, testKeyWithLease, "with-lease", clientv3.WithPrevKV(), clientv3.WithLease(lease.ID))
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to put with lease: %v", err)

		return
	}
	if putResp.PrevKv == nil {
		result.Success = false
		result.Output = "expected PrevKv when adding lease"

		return
	}
	if string(putResp.PrevKv.Value) != "no-lease" {
		result.Success = false
		result.Output = "expected PrevKv value no-lease, got " + string(putResp.PrevKv.Value)

		return
	}
	// PrevKv should have no lease (Lease == 0)
	if putResp.PrevKv.Lease != 0 {
		result.Success = false
		result.Output = fmt.Sprintf("expected PrevKv Lease 0, got %d", putResp.PrevKv.Lease)

		return
	}
}

package scenarios

import (
	"fmt"

	testtime "git.tbd/etcd-infra/internal/etcd/testtime"
	clientv3 "go.etcd.io/etcd/client/v3"

	logutil "git.tbd/etcd-infra/pkg/log"
)

// RunPutWithLeaseAttach verifies attaching a lease to an existing key.
// This is used when updating TTL behavior on existing resources.
// ref. "clientv3/integration/TestKVPutWithLease".
func RunPutWithLeaseAttach(runner Runner) {
	logutil.S().Infow("running", "scenario", PutWithLeaseAttach.String())

	result := &Result{
		Scenario:  PutWithLeaseAttach.String(),
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

	// Create key without lease
	ctx, cancel := runner.NewCtx()
	putResp, err := cli.Put(ctx, testKey, "value-no-lease")
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to put without lease: %v", err)

		return
	}
	initialRev := putResp.Header.Revision

	// Verify key has no lease
	ctx, cancel = runner.NewCtx()
	getResp, err := cli.Get(ctx, testKey)
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to get: %v", err)

		return
	}
	if getResp.Kvs[0].Lease != 0 {
		result.Success = false
		result.Output = fmt.Sprintf("expected no lease, got %d", getResp.Kvs[0].Lease)

		return
	}

	// Create a lease
	ctx, cancel = runner.NewCtx()
	leaseResp, err := cli.Grant(ctx, 60)
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to grant lease: %v", err)

		return
	}
	leaseID := leaseResp.ID

	// Attach lease to existing key
	ctx, cancel = runner.NewCtx()
	putResp, err = cli.Put(ctx, testKey, "value-with-lease", clientv3.WithLease(leaseID))
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to put with lease: %v", err)

		return
	}

	// Revision should have increased
	if putResp.Header.Revision <= initialRev {
		result.Success = false
		result.Output = fmt.Sprintf("expected revision to increase from %d, got %d", initialRev, putResp.Header.Revision)

		return
	}

	// Verify key now has lease
	ctx, cancel = runner.NewCtx()
	getResp, err = cli.Get(ctx, testKey)
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to get after lease attach: %v", err)

		return
	}
	if getResp.Kvs[0].Lease != int64(leaseID) {
		result.Success = false
		result.Output = fmt.Sprintf("expected lease %d, got %d", leaseID, getResp.Kvs[0].Lease)

		return
	}
	if string(getResp.Kvs[0].Value) != "value-with-lease" {
		result.Success = false
		result.Output = fmt.Sprintf("expected value 'value-with-lease', got '%s'", string(getResp.Kvs[0].Value))

		return
	}

	// Test: Attach different lease to same key
	ctx, cancel = runner.NewCtx()
	lease2Resp, err := cli.Grant(ctx, 120)
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to grant second lease: %v", err)

		return
	}
	leaseID2 := lease2Resp.ID

	ctx, cancel = runner.NewCtx()
	_, err = cli.Put(ctx, testKey, "value-new-lease", clientv3.WithLease(leaseID2))
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to attach new lease: %v", err)

		return
	}

	// Verify new lease is attached
	ctx, cancel = runner.NewCtx()
	getResp, err = cli.Get(ctx, testKey)
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to get after second lease attach: %v", err)

		return
	}
	if getResp.Kvs[0].Lease != int64(leaseID2) {
		result.Success = false
		result.Output = fmt.Sprintf("expected lease %d, got %d", leaseID2, getResp.Kvs[0].Lease)

		return
	}

	// Test: Remove lease by putting with no lease
	ctx, cancel = runner.NewCtx()
	_, err = cli.Put(ctx, testKey, "value-no-lease-again")
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to put without lease: %v", err)

		return
	}

	// Verify lease is removed
	ctx, cancel = runner.NewCtx()
	getResp, err = cli.Get(ctx, testKey)
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to get after lease removal: %v", err)

		return
	}
	if getResp.Kvs[0].Lease != 0 {
		result.Success = false
		result.Output = fmt.Sprintf("expected no lease after removal, got %d", getResp.Kvs[0].Lease)

		return
	}

	// Clean up
	ctx, cancel = runner.NewCtx()
	_, _ = cli.Revoke(ctx, leaseID)
	cancel()
	ctx, cancel = runner.NewCtx()
	_, _ = cli.Revoke(ctx, leaseID2)
	cancel()

	result.Output = "lease attach, switch, and removal work correctly"
}

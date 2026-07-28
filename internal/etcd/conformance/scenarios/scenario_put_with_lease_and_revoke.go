package scenarios

import (
	"bytes"
	"fmt"

	testtime "git.tbd/etcd-infra/internal/etcd/testtime"
	clientv3 "go.etcd.io/etcd/client/v3"

	logutil "git.tbd/etcd-infra/pkg/log"
)

// RunPutWithLeaseAndRevoke tests the PutWithLeaseAndRevoke scenario.
func RunPutWithLeaseAndRevoke(runner Runner) {
	logutil.S().Infow("running", "scenario", PutWithLeaseAndRevoke.String())

	result := &Result{
		Scenario:  PutWithLeaseAndRevoke.String(),
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

	ttl := int64(60)
	ctx, cancel := runner.NewCtx()
	lresp, err := cli.Grant(ctx, ttl)
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to grant lease: %v", err)

		return
	}
	leaseID := lresp.ID

	testKey := runner.GenerateRandomKey(10)

	ctx, cancel = runner.NewCtx()
	_, err = cli.Put(ctx, testKey, "bar", clientv3.WithLease(leaseID))
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to put: %v", err)

		return
	}

	ctx, cancel = runner.NewCtx()
	gresp, err := cli.Get(ctx, testKey)
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to get: %v", err)

		return
	}
	if len(gresp.Kvs) != 1 {
		result.Success = false
		result.Output = fmt.Sprintf("expected 1 key, got %d", len(gresp.Kvs))

		return
	}
	if !bytes.Equal(gresp.Kvs[0].Key, []byte(testKey)) {
		result.Success = false
		result.Output = fmt.Sprintf("expected key '%s', got %s", testKey, gresp.Kvs[0].Key)

		return
	}
	if !bytes.Equal(gresp.Kvs[0].Value, []byte("bar")) {
		result.Success = false
		result.Output = fmt.Sprintf("expected value 'bar', got %s", gresp.Kvs[0].Value)

		return
	}

	ctx, cancel = runner.NewCtx()
	_, err = cli.Revoke(ctx, leaseID)
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to revoke lease: %v", err)

		return
	}

	ctx, cancel = runner.NewCtx()
	tresp, err := cli.TimeToLive(ctx, leaseID, clientv3.WithAttachedKeys())
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to get lease ttl: %v", err)

		return
	}
	if tresp.TTL != -1 {
		result.Success = false
		result.Output = fmt.Sprintf("expired lease expects ttl -1, got %d", tresp.TTL)

		return
	}

	ctx, cancel = runner.NewCtx()
	gresp, err = cli.Get(ctx, testKey)
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to get: %v", err)

		return
	}
	if len(gresp.Kvs) != 0 {
		result.Success = false
		result.Output = fmt.Sprintf("expected 0 key, got %d", len(gresp.Kvs))

		return
	}
}

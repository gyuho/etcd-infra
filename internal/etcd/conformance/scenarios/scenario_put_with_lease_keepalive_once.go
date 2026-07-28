package scenarios

import (
	"bytes"
	"errors"
	"fmt"
	"time"

	testtime "git.tbd/etcd-infra/internal/etcd/testtime"
	"go.etcd.io/etcd/api/v3/v3rpc/rpctypes"
	clientv3 "go.etcd.io/etcd/client/v3"

	logutil "git.tbd/etcd-infra/pkg/log"
)

// RunPutWithLeaseKeepaliveOnce tests the PutWithLeaseKeepaliveOnce scenario.
func RunPutWithLeaseKeepaliveOnce(runner Runner) {
	logutil.S().Infow("running", "scenario", PutWithLeaseKeepaliveOnce.String())

	result := &Result{
		Scenario:  PutWithLeaseKeepaliveOnce.String(),
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

	ctx, cancel := runner.NewCtx()
	_, err = cli.KeepAliveOnce(ctx, clientv3.LeaseID(0))
	cancel()
	if !errors.Is(err, rpctypes.ErrLeaseNotFound) {
		result.Success = false
		result.Output = fmt.Sprintf("unexpected error: %v", err)

		return
	}

	// Keep the TTL short so the countdowns complete quickly but still allow the
	// lease to be extended by a single keep-alive call.
	ttl := int64(3)

	ctx, cancel = runner.NewCtx()
	lresp, err := cli.Grant(ctx, ttl)
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to grant: %v", err)

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

	if waitErr := waitForLeaseTTL(cli, runner, leaseID, time.Duration(ttl)*time.Second, func(currentTTL int64) bool {
		return currentTTL > 0 && currentTTL < ttl
	}); waitErr != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to observe lease countdown: %v", waitErr)

		return
	}

	ctx, cancel = runner.NewCtx()
	kresp, err := cli.KeepAliveOnce(ctx, leaseID)
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to keep alive once: %v", err)

		return
	}
	if kresp.TTL == -1 {
		result.Success = false
		result.Output = fmt.Sprintf("unexpected expired lease (lease ID %d or %x)", leaseID, leaseID)

		return
	}

	if waitErr := waitForLeaseTTL(cli, runner, leaseID, time.Duration(ttl)*time.Second, func(currentTTL int64) bool {
		return currentTTL > 0 && currentTTL < ttl
	}); waitErr != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to observe lease countdown after keepalive: %v", waitErr)

		return
	}

	ctx, cancel = runner.NewCtx()
	tresp, err := cli.TimeToLive(ctx, leaseID, clientv3.WithAttachedKeys())
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to get time to live: %v", err)

		return
	}
	if tresp.TTL == -1 {
		result.Success = false
		result.Output = fmt.Sprintf("unexpected expired lease (lease ID %d or %x)", leaseID, leaseID)

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
		result.Output = fmt.Sprintf("unexpected number of keys: %d", len(gresp.Kvs))

		return
	}
	if !bytes.Equal(gresp.Kvs[0].Key, []byte(testKey)) {
		result.Success = false
		result.Output = "unexpected key"

		return
	}
	if !bytes.Equal(gresp.Kvs[0].Value, []byte("bar")) {
		result.Success = false
		result.Output = "unexpected value"

		return
	}

	if err := waitForKeysToExpire(cli, runner, []keyValue{{k: testKey}}, time.Duration(ttl)*time.Second+3*time.Second); err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("lease did not expire as expected: %v", err)

		return
	}
}

func waitForLeaseTTL(cli *clientv3.Client, runner Runner, leaseID clientv3.LeaseID, timeout time.Duration, predicate func(int64) bool) error {
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	for {
		ctx, cancel := runner.NewCtx()
		tresp, err := cli.TimeToLive(ctx, leaseID)
		cancel()
		if err != nil {
			return fmt.Errorf("TimeToLive failed: %w", err)
		}
		if predicate(tresp.TTL) {
			return nil
		}

		select {
		case <-ticker.C:
		case <-timer.C:
			return errors.New("timeout waiting for lease TTL condition")
		}
	}
}

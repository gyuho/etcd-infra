package scenarios

import (
	"bytes"
	"context"
	"fmt"
	"time"

	testtime "git.tbd/etcd-infra/internal/etcd/testtime"
	clientv3 "go.etcd.io/etcd/client/v3"

	logutil "git.tbd/etcd-infra/pkg/log"
)

const keepAliveChannelClosedMsg = "keep alive channel closed"

// RunPutWithLeaseKeepalive verifies keep-alive behavior for leased keys.
//
//nolint:gocyclo // Scenario covers multiple lease edge cases.
func RunPutWithLeaseKeepalive(runner Runner) {
	logutil.S().Infow("running", "scenario", PutWithLeaseKeepalive.String())

	result := &Result{
		Scenario:  PutWithLeaseKeepalive.String(),
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

	// Use a shorter TTL to keep the scenario responsive while still
	// exercising automatic lease renewals.
	ttl := int64(4)
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
	presp, err := cli.Put(ctx, testKey, "bar", clientv3.WithLease(leaseID))
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to put: %v", err)

		return
	}
	putRev := presp.Header.GetRevision()
	logutil.S().Infow("put", "revision", putRev)

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
	if putRev != gresp.Kvs[0].ModRevision {
		result.Success = false
		result.Output = fmt.Sprintf("expected mod revision %d, got %d", putRev, gresp.Kvs[0].ModRevision)

		return
	}
	if leaseID != clientv3.LeaseID(gresp.Kvs[0].Lease) {
		result.Success = false
		result.Output = fmt.Sprintf("expected lease id %d, got %d", leaseID, gresp.Kvs[0].Lease)

		return
	}

	cctx, ccancel := context.WithCancel(context.Background())
	defer ccancel()
	kch, err := cli.KeepAlive(cctx, leaseID)
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to keep alive: %v", err)

		return
	}

	// first keep-alive event should return quickly
	select {
	case <-time.After(2 * time.Second):
		result.Success = false
		result.Output = "failed to receive first keep alive event in time"

		return

	case kresp, open := <-kch:
		if !open {
			result.Success = false
			result.Output = keepAliveChannelClosedMsg

			return
		}
		if kresp.TTL == -1 {
			result.Success = false
			result.Output = "unexpected expired lease with TTL -1"

			return
		}
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
		result.Output = fmt.Sprintf("unexpected expired lease with TTL -1 (lease ID %d or %x)", leaseID, leaseID)

		return
	}

	// second keep-alive event should arrive well before the lease would expire
	keepAliveDeadline := time.Second + (time.Duration(ttl) * time.Second / 2)
	select {
	case <-time.After(keepAliveDeadline):
		result.Success = false
		result.Output = "failed to receive second keep alive event in time"

		return

	case kresp, open := <-kch:
		if !open {
			result.Success = false
			result.Output = keepAliveChannelClosedMsg

			return
		}
		if leaseID != kresp.ID {
			result.Success = false
			result.Output = fmt.Sprintf("expected lease id %d, got %d", leaseID, kresp.ID)

			return
		}

		// keep alive increments revision
		if putRev > kresp.Revision {
			result.Success = false
			result.Output = fmt.Sprintf("expected lease revision to be put revision %d <= %d", putRev, kresp.Revision)

			return
		}
	}

	// key and lease should not get deleted, expect it to be renewed automatic
	ctx, cancel = runner.NewCtx()
	tresp, err = cli.TimeToLive(ctx, leaseID, clientv3.WithAttachedKeys())
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to get time to live: %v", err)

		return
	}
	if tresp.TTL == -1 {
		result.Success = false
		result.Output = fmt.Sprintf("unexpected expired lease with TTL -1 (lease ID %d or %x)", leaseID, leaseID)

		return
	}
	if putRev > tresp.Revision {
		result.Success = false
		result.Output = fmt.Sprintf("expected revision %d <= %d", putRev, tresp.Revision)

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
	logutil.S().Infow("latest", "putRevHeader", putRev, "getRevHeader", gresp.Header.Revision)
}

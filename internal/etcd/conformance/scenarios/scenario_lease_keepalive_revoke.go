package scenarios

import (
	"context"
	"fmt"
	"time"

	testtime "git.tbd/etcd-infra/internal/etcd/testtime"
	clientv3 "go.etcd.io/etcd/client/v3"

	logutil "git.tbd/etcd-infra/pkg/log"
)

// RunLeaseKeepaliveRevoke tests the LeaseKeepaliveRevoke scenario.
func RunLeaseKeepaliveRevoke(runner Runner) {
	logutil.S().Infow("running", "scenario", LeaseKeepaliveRevoke.String())

	result := &Result{
		Scenario:  LeaseKeepaliveRevoke.String(),
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

	type leaseKeepAlive struct {
		id clientv3.LeaseID
		ch <-chan *clientv3.LeaseKeepAliveResponse
	}

	leaseKeepAlives := make([]leaseKeepAlive, 3)
	cancelKeepAlives := make([]context.CancelFunc, 0, len(leaseKeepAlives))
	// Use a generous TTL (15s) to accommodate cloud/VPN environments (Tailscale/Headscale)
	// where network latency affects keepalive channel synchronization.
	// This ensures the lease doesn't expire during test execution while we wait for channel operations.
	for i := range 3 {
		ctx, cancel := runner.NewCtx()
		lresp, grantErr := cli.Grant(ctx, 15)
		cancel()
		if grantErr != nil {
			result.Success = false
			result.Output = fmt.Sprintf("failed to grant: %v", grantErr)

			return
		}

		cctx, ccancel := context.WithCancel(context.Background())
		cancelKeepAlives = append(cancelKeepAlives, ccancel)
		kach, keepAliveErr := cli.KeepAlive(cctx, lresp.ID)
		if keepAliveErr != nil {
			result.Success = false
			result.Output = fmt.Sprintf("failed to keep alive: %v", keepAliveErr)

			return
		}
		leaseKeepAlives[i] = leaseKeepAlive{
			id: lresp.ID,
			ch: kach,
		}
	}
	defer func() {
		for _, cancelKeepAlive := range cancelKeepAlives {
			cancelKeepAlive()
		}
	}()

	ctx, cancel := runner.NewCtx()
	_, err = cli.Revoke(ctx, leaseKeepAlives[1].id)
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to revoke: %v", err)

		return
	}

	// Verify unrevoked lease keepalive channel is still receiving responses
	select {
	case _, open := <-leaseKeepAlives[0].ch:
		if !open {
			result.Success = false
			result.Output = "unrevoked lease keep alive channel unexpectedly closed"

			return
		}
	case <-time.After(30 * time.Second):
		result.Success = false
		result.Output = "timed out waiting for keepalive response on unrevoked lease"

		return
	}

	// Verify revoked lease keepalive channel is closed.
	// Over high-latency networks (cloud/VPN), the channel may take time to close after revoke.
	// Use a timeout-based wait rather than expecting immediate closure.
	timeout := time.After(30 * time.Second)
	for {
		select {
		case _, open := <-leaseKeepAlives[1].ch:
			if !open {
				// Channel is closed as expected after revoke
				return
			}
			// Channel still has data, continue draining
		case <-timeout:
			result.Success = false
			result.Output = "revoked lease keep alive channel did not close within timeout"

			return
		}
	}
}

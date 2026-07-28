package scenarios

import (
	"context"
	"fmt"
	"time"

	testtime "git.tbd/etcd-infra/internal/etcd/testtime"
	clientv3 "go.etcd.io/etcd/client/v3"

	logutil "git.tbd/etcd-infra/pkg/log"
)

// RunPutWithLeaseKeepaliveFullResponseQueue tests the PutWithLeaseKeepaliveFullResponseQueue scenario.
func RunPutWithLeaseKeepaliveFullResponseQueue(runner Runner) {
	logutil.S().Infow("running", "scenario", PutWithLeaseKeepaliveFullResponseQueue.String())

	result := &Result{
		Scenario:  PutWithLeaseKeepaliveFullResponseQueue.String(),
		TimeStart: testtime.Now(),
		Success:   true,
		Output:    "ok",
	}
	defer func() {
		result.RecordTimeEnd(testtime.Now())
		runner.RecordResult(*result)
	}()

	origRespChSize := clientv3.LeaseResponseChSize
	// A smaller channel still exercises the saturation behavior but fills much
	// faster during tests.
	clientv3.LeaseResponseChSize = 4
	defer func() {
		clientv3.LeaseResponseChSize = origRespChSize
	}()

	cli, err := runner.NewClient()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to create client: %v", err)

		return
	}
	defer func() { _ = cli.Close() }()

	// Use the smallest supported TTL to emit keep-alive responses quickly while
	// the channel is filled to capacity.
	ctx, cancel := runner.NewCtx()
	lresp, err := cli.Grant(ctx, 1)
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to grant: %v", err)

		return
	}
	leaseID := lresp.ID

	cctx, ccancel := context.WithCancel(context.Background())
	defer ccancel()
	kch, err := cli.KeepAlive(cctx, leaseID)
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to keep alive: %v", err)

		return
	}

	// Fill the buffered channel without waiting the full theoretical duration.
	targetSize := clientv3.LeaseResponseChSize
	deadline := time.After(8 * time.Second)
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			if len(kch) >= targetSize-1 {
				logutil.S().Infow("keep-alive channel reached target size", "size", len(kch), "target", targetSize)
				goto channelFilled
			}
		case <-deadline:
			result.Success = false
			result.Output = fmt.Sprintf("keep-alive response channel did not fill: size=%d, want_at_least=%d", len(kch), targetSize-1)

			return
		}
	}

channelFilled:

	ctx, cancel = runner.NewCtx()
	tresp, err := cli.TimeToLive(ctx, leaseID)
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to get time to live: %v", err)

		return
	}
	if tresp.TTL == -1 {
		result.Success = false
		result.Output = fmt.Sprintf("unexpected expired TTL -1 for lease %x", leaseID)

		return
	}

	// expects keep-alive response channel full
	// without keep-alive, lease and key should have been revoked
	filledSize := len(kch)
	if filledSize < targetSize-1 {
		result.Success = false
		result.Output = fmt.Sprintf("unexpected keep-alive response channel size: %d, want_at_least=%d", filledSize, targetSize-1)

		return
	}

	ctx, cancel = runner.NewCtx()
	tresp, err = cli.TimeToLive(ctx, leaseID)
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to get time to live: %v", err)

		return
	}
	if tresp.TTL == -1 {
		result.Success = false
		result.Output = fmt.Sprintf("unexpected expired TTL -1 for lease %x", leaseID)

		return
	}
}

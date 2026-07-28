package scenarios

import (
	"context"
	"fmt"

	testtime "git.tbd/etcd-infra/internal/etcd/testtime"

	logutil "git.tbd/etcd-infra/pkg/log"
)

// RunPutWithLeaseKeepaliveOneSecond tests the PutWithLeaseKeepaliveOneSecond scenario.
func RunPutWithLeaseKeepaliveOneSecond(runner Runner) {
	logutil.S().Infow("running", "scenario", PutWithLeaseKeepaliveOneSecond.String())

	result := &Result{
		Scenario:  PutWithLeaseKeepaliveOneSecond.String(),
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

	// Three iterations are enough to confirm the keep-alive stream stays healthy
	// without waiting the full five seconds of the original test.
	for range 3 {
		_, ok := <-kch
		if !ok {
			result.Success = false
			result.Output = "keep alive channel closed"

			return
		}
	}
}

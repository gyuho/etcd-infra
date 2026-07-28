package scenarios

import (
	"context"
	"errors"
	"fmt"
	"time"

	testtime "git.tbd/etcd-infra/internal/etcd/testtime"
	"go.etcd.io/etcd/api/v3/v3rpc/rpctypes"
	clientv3 "go.etcd.io/etcd/client/v3"

	logutil "git.tbd/etcd-infra/pkg/log"
)

// RunWatchWithCompactedRevision tests the WatchWithCompactedRevision scenario.
func RunWatchWithCompactedRevision(runner Runner) {
	logutil.S().Infow("running", "scenario", WatchWithCompactedRevision.String())

	result := &Result{
		Scenario:  WatchWithCompactedRevision.String(),
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
	ctx, cancel := runner.NewCtx()
	presp, err := cli.Put(ctx, testKey, "bar")
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to put: %v", err)

		return
	}
	putRev := presp.Header.GetRevision()

	ctx, cancel = runner.NewCtx()
	_, err = cli.Compact(ctx, putRev, clientv3.WithCompactPhysical())
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to compact: %v", err)

		return
	}

	cctx, ccancel := context.WithCancel(context.Background())
	defer ccancel()

	wch := cli.Watch(cctx, testKey, clientv3.WithRev(putRev-1))
	if wch == nil {
		result.Success = false
		result.Output = watchCreateFailedMsg

		return
	}

	select {
	case wr, open := <-wch:
		if !open {
			result.Success = false
			result.Output = watchChannelClosedMsg

			return
		}
		if !wr.Canceled {
			result.Success = false
			result.Output = "expected watch cancellation due to compacted revision, but not canceled"

			return
		}
		if !errors.Is(wr.Err(), rpctypes.ErrCompacted) {
			result.Success = false
			result.Output = fmt.Sprintf("expected ErrCompacted, but got: %v", wr.Err())

			return
		}
		if wr.CompactRevision != putRev {
			result.Success = false
			result.Output = fmt.Sprintf("expected compact revision %d, but got %d", putRev, wr.CompactRevision)

			return
		}

	case <-time.After(time.Second):
		result.Success = false
		result.Output = "first, took too long to receive from watch channel"

		return
	}

	// got the error; should close next
	select {
	case wr, open := <-wch:
		if open {
			result.Success = false
			result.Output = "watch channel is not closed"

			return
		}
		if wr.Canceled {
			result.Success = false
			result.Output = watchChannelCanceledMsg

			return
		}
		if wr.Err() != nil {
			result.Success = false
			result.Output = fmt.Sprintf("unexpected error: %v", wr.Err())

			return
		}

	case <-time.After(time.Second):
		result.Success = false
		result.Output = "second, took too long to receive from watch channel"

		return
	}

	ctx, cancel = runner.NewCtx()
	_, err = cli.Compact(ctx, putRev+100)
	cancel()
	if !errors.Is(err, rpctypes.ErrFutureRev) {
		result.Success = false
		result.Output = fmt.Sprintf("expected ErrFutureRev, but got: %v", err)

		return
	}
}

package scenarios

import (
	"errors"
	"fmt"
	"time"

	testtime "git.tbd/etcd-infra/internal/etcd/testtime"
	"go.etcd.io/etcd/api/v3/v3rpc/rpctypes"
	clientv3 "go.etcd.io/etcd/client/v3"

	logutil "git.tbd/etcd-infra/pkg/log"
)

// RunErrorCompacted verifies ErrCompacted is returned for reads and watches at compacted revisions.
// ref. "clientv3/integration/TestKVCompactError"
// ref. "clientv3/integration/TestWatchCompactRevision".
func RunErrorCompacted(runner Runner) {
	logutil.S().Infow("running", "scenario", ErrorCompacted.String())

	result := &Result{
		Scenario:  ErrorCompacted.String(),
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

	// Write initial value
	ctx, cancel := runner.NewCtx()
	putResp1, err := cli.Put(ctx, testKey, "v1")
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to put v1: %v", err)

		return
	}
	rev1 := putResp1.Header.Revision

	// Write second value
	ctx, cancel = runner.NewCtx()
	putResp2, err := cli.Put(ctx, testKey, "v2")
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to put v2: %v", err)

		return
	}
	rev2 := putResp2.Header.Revision

	// Compact up to rev2, making rev1 unavailable
	ctx, cancel = runner.NewCtx()
	_, err = cli.Compact(ctx, rev2)
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to compact: %v", err)

		return
	}

	// Test 1: Get at compacted revision should return ErrCompacted
	ctx, cancel = runner.NewCtx()
	_, err = cli.Get(ctx, testKey, clientv3.WithRev(rev1))
	cancel()
	if !errors.Is(err, rpctypes.ErrCompacted) {
		result.Success = false
		result.Output = fmt.Sprintf("expected ErrCompacted for get at rev %d, got: %v", rev1, err)

		return
	}

	// Test 2: Watch at compacted revision should return ErrCompacted
	wctx, wcancel := runner.NewCtxTimeout(5 * time.Second)
	defer wcancel()
	wch := cli.Watch(wctx, testKey, clientv3.WithRev(rev1))
	if wch == nil {
		result.Success = false
		result.Output = watchCreateFailedMsg

		return
	}

	select {
	case wr, open := <-wch:
		if !open {
			result.Success = false
			result.Output = watchChannelClosedUnexpectedlyMsg

			return
		}
		if wr.Err() == nil || !errors.Is(wr.Err(), rpctypes.ErrCompacted) {
			result.Success = false
			result.Output = fmt.Sprintf("expected ErrCompacted for watch at rev %d, got: %v", rev1, wr.Err())

			return
		}
	case <-time.After(5 * time.Second):
		result.Success = false
		result.Output = "timed out waiting for watch error response"

		return
	}

	// Test 3: Double compact should return ErrCompacted
	ctx, cancel = runner.NewCtx()
	_, err = cli.Compact(ctx, rev2)
	cancel()
	if !errors.Is(err, rpctypes.ErrCompacted) {
		result.Success = false
		result.Output = fmt.Sprintf("expected ErrCompacted for double compact, got: %v", err)

		return
	}

	// Verify rev2 is still accessible
	ctx, cancel = runner.NewCtx()
	getResp, err := cli.Get(ctx, testKey, clientv3.WithRev(rev2))
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to get at rev2: %v", err)

		return
	}
	if len(getResp.Kvs) != 1 || string(getResp.Kvs[0].Value) != "v2" {
		result.Success = false
		result.Output = "expected v2 to still be accessible at compacted revision"

		return
	}
}

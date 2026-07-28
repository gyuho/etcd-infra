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

// RunErrorFutureRev verifies ErrFutureRev is returned when requesting a future revision.
// ref. "clientv3/integration/TestKVCompactError".
func RunErrorFutureRev(runner Runner) {
	logutil.S().Infow("running", "scenario", ErrorFutureRev.String())

	result := &Result{
		Scenario:  ErrorFutureRev.String(),
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

	// Write a value to get current revision
	ctx, cancel := runner.NewCtx()
	putResp, err := cli.Put(ctx, testKey, "value")
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to put: %v", err)

		return
	}
	currentRev := putResp.Header.Revision
	futureRev := currentRev + 1000

	// Test 1: Get at future revision should return ErrFutureRev
	ctx, cancel = runner.NewCtx()
	_, err = cli.Get(ctx, testKey, clientv3.WithRev(futureRev))
	cancel()
	if !errors.Is(err, rpctypes.ErrFutureRev) {
		result.Success = false
		result.Output = fmt.Sprintf("expected ErrFutureRev for get at rev %d, got: %v", futureRev, err)

		return
	}

	// Test 2: Watch at future revision should eventually receive the event when data arrives
	// or return error depending on etcd version behavior
	wctx, wcancel := runner.NewCtxTimeout(5 * time.Second)
	defer wcancel()

	// Watch slightly ahead of current revision
	watchRev := currentRev + 1
	wch := cli.Watch(wctx, testKey, clientv3.WithRev(watchRev))
	if wch == nil {
		result.Success = false
		result.Output = watchCreateFailedMsg

		return
	}

	// Write another value that should trigger the watch
	ctx, cancel = runner.NewCtx()
	_, err = cli.Put(ctx, testKey, "value2")
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to put value2: %v", err)

		return
	}

	// Wait for watch event or timeout
	select {
	case wr, open := <-wch:
		if !open {
			result.Success = false
			result.Output = "watch channel closed unexpectedly"

			return
		}
		if wr.Err() != nil {
			result.Success = false
			result.Output = fmt.Sprintf("unexpected watch error: %v", wr.Err())

			return
		}
		if len(wr.Events) != 1 {
			result.Success = false
			result.Output = fmt.Sprintf("expected 1 event, got %d", len(wr.Events))

			return
		}
		if string(wr.Events[0].Kv.Value) != "value2" {
			result.Success = false
			result.Output = "expected value2, got " + string(wr.Events[0].Kv.Value)

			return
		}
	case <-time.After(5 * time.Second):
		result.Success = false
		result.Output = "timed out waiting for watch event"

		return
	}

	// Test 3: Compact at future revision should return ErrFutureRev
	ctx, cancel = runner.NewCtx()
	_, err = cli.Compact(ctx, futureRev)
	cancel()
	if !errors.Is(err, rpctypes.ErrFutureRev) {
		result.Success = false
		result.Output = fmt.Sprintf("expected ErrFutureRev for compact at rev %d, got: %v", futureRev, err)

		return
	}
}

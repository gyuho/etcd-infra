package scenarios

import (
	"context"
	"fmt"
	"time"

	testtime "git.tbd/etcd-infra/internal/etcd/testtime"

	logutil "git.tbd/etcd-infra/pkg/log"
)

// RunWatchCancelClose tests the WatchCancelClose scenario.
func RunWatchCancelClose(runner Runner) {
	logutil.S().Infow("running", "scenario", WatchCancelClose.String())

	result := &Result{
		Scenario:  WatchCancelClose.String(),
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

	cctx, ccancel := context.WithCancel(context.Background())
	defer ccancel()

	testKey := runner.GenerateRandomKey(10)
	wch := cli.Watch(cctx, testKey)
	if wch == nil {
		result.Success = false
		result.Output = watchCreateFailedMsg

		return
	}

	if err := cli.Watcher.Close(); err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to close watcher: %v", err)

		return
	}

	select {
	case wr, open := <-wch:
		if open {
			result.Success = false
			result.Output = "watch channel is not closed after watcher close"

			return
		}
		if wr.Canceled {
			// context cancel does not cancel watcher
			result.Success = false
			result.Output = watchChannelCanceledAfterContextMsg

			return
		}

	case <-time.After(time.Second):
		result.Success = false
		result.Output = "watch cancellation took too long to close the channel"

		return
	}
}

package scenarios

import (
	"context"
	"fmt"
	"time"

	testtime "git.tbd/etcd-infra/internal/etcd/testtime"

	logutil "git.tbd/etcd-infra/pkg/log"
)

// RunWatchCancelInit tests the WatchCancelInit scenario.
func RunWatchCancelInit(runner Runner) {
	logutil.S().Infow("running", "scenario", WatchCancelInit.String())

	result := &Result{
		Scenario:  WatchCancelInit.String(),
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

	ctx, cancel := context.WithCancel(context.Background())
	testKey := runner.GenerateRandomKey(10)
	wch := cli.Watch(ctx, testKey)
	if wch == nil {
		result.Success = false
		result.Output = watchCreateFailedMsg
		cancel()

		return
	}

	cancel()

	select {
	case wr, open := <-wch:
		if open {
			result.Success = false
			result.Output = "watch channel is not closed after context cancel"

			return
		}
		if wr.Canceled {
			// context cancel does not cancel watcher
			result.Success = false
			result.Output = "watch channel is canceled after context cancel"

			return
		}

	case <-time.After(time.Second):
		result.Success = false
		result.Output = "took too long receive from canceled watch channel"

		return
	}
}

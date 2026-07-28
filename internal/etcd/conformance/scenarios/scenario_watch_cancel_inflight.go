package scenarios

import (
	"fmt"
	"time"

	testtime "git.tbd/etcd-infra/internal/etcd/testtime"

	logutil "git.tbd/etcd-infra/pkg/log"
)

// RunWatchCancelInflight tests the WatchCancelInflight scenario.
func RunWatchCancelInflight(runner Runner) {
	logutil.S().Infow("running", "scenario", WatchCancelInflight.String())

	result := &Result{
		Scenario:  WatchCancelInflight.String(),
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

	// whole test shouldn't take more than 15 seconds
	ctx, cancel := runner.NewCtxTimeout(15 * time.Second)

	wch := cli.Watch(ctx, testKey)
	if wch == nil {
		result.Success = false
		result.Output = watchCreateFailedMsg
		cancel()

		return
	}

	_, err = cli.Put(ctx, testKey, "bar")
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to put: %v", err)
		cancel()

		return
	}

	cancel()

	select {
	case _, open := <-wch:
		if !open {
			logutil.S().Info("watcher closed before PUT completion; ok")

			break
		}

		select {
		case wr, open := <-wch:
			if open {
				result.Success = false
				result.Output = watchChannelNotClosedAfterCancelMsg

				return
			}
			if wr.Canceled {
				result.Success = false
				result.Output = watchChannelCanceledMsg

				return
			}

		case <-time.After(time.Second):
			result.Success = false
			result.Output = "second, took too long receive from canceled watch channel"

			return
		}

	case <-time.After(time.Second):
		result.Success = false
		result.Output = "first, took too long receive from canceled watch channel"

		return
	}
}

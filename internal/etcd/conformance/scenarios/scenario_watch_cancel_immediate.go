package scenarios

import (
	"context"
	"fmt"

	testtime "git.tbd/etcd-infra/internal/etcd/testtime"
	clientv3 "go.etcd.io/etcd/client/v3"

	logutil "git.tbd/etcd-infra/pkg/log"
)

// RunWatchCancelImmediate tests the WatchCancelImmediate scenario.
func RunWatchCancelImmediate(runner Runner) {
	logutil.S().Infow("running", "scenario", WatchCancelImmediate.String())

	result := &Result{
		Scenario:  WatchCancelImmediate.String(),
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
	cancel()
	testKey := runner.GenerateRandomKey(10)
	wch := cli.Watch(ctx, testKey)
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

	// The client embeds a watcher instance. Once closed, future Watch calls on the same
	// client return a closed channel. Recreate the watcher so the remainder of the
	// scenario (and any later scenarios reusing this client) exercises the intended
	// behavior instead of short-circuiting on the closed watcher.
	cli.Watcher = clientv3.NewWatcher(cli)

	select {
	case wr, open := <-wch:
		if open {
			result.Success = false
			result.Output = watchChannelNotClosedAfterWatcherMsg

			return
		}
		if wr.Canceled {
			// context cancel does not cancel watcher
			result.Success = false
			result.Output = watchChannelCanceledAfterContextMsg

			return
		}

	default:
		result.Success = false
		result.Output = watchChannelShouldNotBlockMsg

		return
	}

	ctx, cancel = context.WithCancel(context.Background())
	wch = cli.Watch(ctx, testKey, clientv3.WithCreatedNotify())
	wr, open := <-wch
	if !open {
		result.Success = false
		result.Output = watchChannelClosedMsg
		cancel()

		return
	}
	if !wr.Created {
		result.Success = false
		result.Output = watchChannelShouldNotBeCreatedMsg
		cancel()

		return
	}
	if wr.Canceled {
		result.Success = false
		result.Output = watchChannelCanceledMsg
		cancel()

		return
	}

	cancel()

	wr, open = <-wch
	if open {
		result.Success = false
		result.Output = watchChannelNotClosedAfterCancelMsg

		return
	}
	if wr.Created {
		result.Success = false
		result.Output = watchChannelCanceledCreatedMsg

		return
	}
	if wr.Canceled {
		// context cancel does not cancel watcher
		result.Success = false
		result.Output = watchChannelCanceledAfterContextMsg

		return
	}

	// watcher with canceled context
	wch = cli.Watch(ctx, testKey)
	wr, open = <-wch
	if open {
		result.Success = false
		result.Output = watchChannelNotClosedAfterCancelMsg

		return
	}
	if wr.Created {
		result.Success = false
		result.Output = watchChannelCanceledContextCreateMsg

		return
	}
	if wr.Canceled {
		result.Success = false
		result.Output = watchChannelCanceledContextCancelMsg

		return
	}
}

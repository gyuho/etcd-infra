package scenarios

import (
	"context"
	"fmt"
	"time"

	testtime "git.tbd/etcd-infra/internal/etcd/testtime"
	clientv3 "go.etcd.io/etcd/client/v3"

	logutil "git.tbd/etcd-infra/pkg/log"
)

// RunWatchWithCreatedNotification tests the WatchWithCreatedNotification scenario.
func RunWatchWithCreatedNotification(runner Runner) {
	logutil.S().Infow("running", "scenario", WatchWithCreatedNotification.String())

	result := &Result{
		Scenario:  WatchWithCreatedNotification.String(),
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

	cctx, ccancel := context.WithCancel(context.Background())
	defer ccancel()

	wch := cli.Watch(cctx, testKey, clientv3.WithCreatedNotify())
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
		if !wr.Created {
			result.Success = false
			result.Output = fmt.Sprintf("expected created event, got %+v", wr)

			return
		}

	case <-time.After(100 * time.Millisecond):
		result.Success = false
		result.Output = "took too long to receive created event"

		return
	}
}

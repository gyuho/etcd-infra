package scenarios

import (
	"bytes"
	"context"
	"fmt"
	"path"
	"time"

	testtime "git.tbd/etcd-infra/internal/etcd/testtime"
	clientv3 "go.etcd.io/etcd/client/v3"

	logutil "git.tbd/etcd-infra/pkg/log"
)

// RunWatchWithPrefix tests the WatchWithPrefix scenario.
func RunWatchWithPrefix(runner Runner) {
	logutil.S().Infow("running", "scenario", WatchWithPrefix.String())

	result := &Result{
		Scenario:  WatchWithPrefix.String(),
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

	wch := cli.Watch(cctx, testKey, clientv3.WithPrefix())
	if wch == nil {
		result.Success = false
		result.Output = watchCreateFailedMsg

		return
	}

	for _, k := range []string{
		testKey,
		path.Join(testKey, "a"),
		path.Join(testKey, "b"),
		path.Join(testKey, "c"),
	} {
		ctx, cancel := runner.NewCtx()
		_, err := cli.Put(ctx, k, "bar")
		cancel()
		if err != nil {
			result.Success = false
			result.Output = fmt.Sprintf("failed to put: %v", err)

			return
		}

		select {
		case wr, open := <-wch:
			if !open {
				result.Success = false
				result.Output = watchChannelClosedMsg

				return
			}
			if len(wr.Events) != 1 {
				result.Success = false
				result.Output = fmt.Sprintf("expected 1 event, got %+v", wr)

				return
			}

			ev := wr.Events[0]
			if !bytes.Equal([]byte("bar"), ev.Kv.Value) {
				result.Success = false
				result.Output = fmt.Sprintf("expected value 'bar', got %q", ev.Kv.Value)

				return
			}

		case <-time.After(runner.DefaultTimeout()):
			result.Success = false
			result.Output = "took too long to receive events"

			return
		}
	}
}

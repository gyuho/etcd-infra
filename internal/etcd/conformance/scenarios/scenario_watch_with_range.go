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

// RunWatchWithRange tests the WatchWithRange scenario.
func RunWatchWithRange(runner Runner) {
	logutil.S().Infow("running", "scenario", WatchWithRange.String())

	result := &Result{
		Scenario:  WatchWithRange.String(),
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
	fromKey, toKey := testKey, path.Join(testKey, "d")

	wch := cli.Watch(cctx, fromKey, clientv3.WithRange(toKey))
	if wch == nil {
		result.Success = false
		result.Output = watchCreateFailedMsg

		return
	}

	for _, v := range []struct {
		key          string
		expectEvents bool
	}{
		{key: testKey, expectEvents: true},                  // within range
		{key: path.Join(testKey, "a"), expectEvents: true},  // within range
		{key: path.Join(testKey, "b"), expectEvents: true},  // within range
		{key: path.Join(testKey, "c"), expectEvents: true},  // within range
		{key: path.Join(testKey, "d"), expectEvents: false}, // out of range
	} {
		ctx, cancel := runner.NewCtx()
		_, err := cli.Put(ctx, v.key, "bar")
		cancel()
		if err != nil {
			result.Success = false
			result.Output = fmt.Sprintf("failed to put: %v", err)

			return
		}

		select {
		case wr, open := <-wch:
			if !v.expectEvents {
				result.Success = false
				result.Output = fmt.Sprintf("watch channel received events for %q (%+v), but expected none", v.key, wr)

				return
			}

			if !open {
				result.Success = false
				result.Output = watchChannelClosedMsg

				return
			}

			ev := wr.Events[0]
			if !bytes.Equal([]byte("bar"), ev.Kv.Value) {
				result.Success = false
				result.Output = fmt.Sprintf("expected value %q, got %q", "bar", string(ev.Kv.Value))

				return
			}

		case <-time.After(time.Second):
			if v.expectEvents {
				result.Success = false
				result.Output = fmt.Sprintf("expected events, but watch channel did not receive any events after put %q", v.key)

				return
			}
		}
	}
}

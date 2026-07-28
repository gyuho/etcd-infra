package scenarios

import (
	"fmt"
	"time"

	testtime "git.tbd/etcd-infra/internal/etcd/testtime"
	clientv3 "go.etcd.io/etcd/client/v3"

	logutil "git.tbd/etcd-infra/pkg/log"
)

// RunGetWithRequireLeader covers clientv3.WithRequireLeader usage for linearizable operations.
func RunGetWithRequireLeader(runner Runner) {
	logutil.S().Infow("running", "scenario", GetWithRequireLeader.String())

	result := &Result{
		Scenario:  GetWithRequireLeader.String(),
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
	testVal := "leader-value"

	ctx, cancel := runner.NewCtx()
	if _, putErr := cli.Put(ctx, testKey, testVal); putErr != nil {
		cancel()
		result.Success = false
		result.Output = fmt.Sprintf("failed to seed key: %v", putErr)

		return
	}
	cancel()

	readCtx, cancel := runner.NewCtx()
	readCtx = clientv3.WithRequireLeader(readCtx)
	gresp, err := cli.Get(readCtx, testKey)
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("Get with require leader failed: %v", err)

		return
	}
	if len(gresp.Kvs) != 1 || string(gresp.Kvs[0].Value) != testVal {
		result.Success = false
		result.Output = "Get with require leader returned unexpected value"

		return
	}

	watcher := clientv3.NewWatcher(cli)
	defer func() {
		if err := watcher.Close(); err != nil {
			logutil.S().Warnw("failed to close watcher", "error", err)
		}
	}()

	watchCtx, cancel := runner.NewCtx()
	watchCtx = clientv3.WithRequireLeader(watchCtx)
	watchCh := watcher.Watch(
		watchCtx,
		testKey,
		clientv3.WithPrevKV(),
	)

	updateVal := "leader-updated"
	ctx, putCancel := runner.NewCtx()
	if _, err := cli.Put(ctx, testKey, updateVal); err != nil {
		putCancel()
		cancel()
		result.Success = false
		result.Output = fmt.Sprintf("failed to update key: %v", err)

		return
	}
	putCancel()

	for {
		select {
		case resp := <-watchCh:
			if resp.Err() != nil {
				cancel()
				result.Success = false
				result.Output = fmt.Sprintf("watch returned error: %v", resp.Err())

				return
			}
			if len(resp.Events) == 0 {
				continue
			}
			for _, ev := range resp.Events {
				if string(ev.Kv.Value) != updateVal {
					continue
				}
				if ev.PrevKv == nil || string(ev.PrevKv.Value) != testVal {
					cancel()
					result.Success = false
					result.Output = "watch event missing prev_kv value"

					return
				}
				cancel()
				result.Output = "WithRequireLeader context successfully read and watched key"

				return
			}
		case <-time.After(5 * time.Second):
			cancel()
			result.Success = false
			result.Output = "timed out waiting for watch event under require leader"

			return
		}
	}
}

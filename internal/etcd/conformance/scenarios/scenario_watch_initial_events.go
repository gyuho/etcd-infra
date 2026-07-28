package scenarios

import (
	"fmt"
	"path"
	"time"

	testtime "git.tbd/etcd-infra/internal/etcd/testtime"
	"go.etcd.io/etcd/api/v3/mvccpb"
	clientv3 "go.etcd.io/etcd/client/v3"

	logutil "git.tbd/etcd-infra/pkg/log"
)

// RunWatchInitialEvents verifies that watches with initial events deliver existing data.
// Kubernetes informers rely on this to populate caches before processing live updates.
// Ref: clientv3/integration/TestWatchFromZero.
func RunWatchInitialEvents(runner Runner) {
	logutil.S().Infow("running", "scenario", WatchInitialEvents.String())

	result := &Result{
		Scenario:  WatchInitialEvents.String(),
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

	prefix := runner.GenerateRandomKey(10)
	keys := []string{
		path.Join(prefix, "key1"),
		path.Join(prefix, "key2"),
		path.Join(prefix, "key3"),
	}

	// Capture a safe starting revision to avoid compaction errors.
	ctx, cancel := runner.NewCtx()
	startResp, err := cli.Get(ctx, prefix, clientv3.WithPrefix(), clientv3.WithCountOnly())
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to get start revision: %v", err)

		return
	}
	startRev := startResp.Header.Revision

	// Create initial data before watch
	for i, key := range keys {
		ctx, cancel = runner.NewCtx()
		_, err = cli.Put(ctx, key, fmt.Sprintf("value-%d", i))
		cancel()
		if err != nil {
			result.Success = false
			result.Output = fmt.Sprintf("failed to put %s: %v", key, err)

			return
		}
	}

	// Start watch from a known-safe revision to get the initial events.
	wctx, wcancel := runner.NewCtxTimeout(10 * time.Second)
	defer wcancel()

	wch := cli.Watch(wctx, prefix,
		clientv3.WithPrefix(),
		clientv3.WithRev(startRev),
		clientv3.WithCreatedNotify(),
	)
	if wch == nil {
		result.Success = false
		result.Output = watchCreateFailedMsg

		return
	}

	// First should be created notification
	select {
	case wr := <-wch:
		if wr.Err() != nil {
			result.Success = false
			result.Output = fmt.Sprintf("watch error: %v", wr.Err())

			return
		}
		if !wr.Created {
			result.Success = false
			result.Output = "expected Created notification first"

			return
		}
	case <-time.After(5 * time.Second):
		result.Success = false
		result.Output = "timeout waiting for created notification"

		return
	}

	// Collect initial events
	receivedKeys := make(map[string]bool)
	timeout := time.After(5 * time.Second)

collectLoop:
	for {
		select {
		case wr := <-wch:
			if wr.Err() != nil {
				result.Success = false
				result.Output = fmt.Sprintf("watch error: %v", wr.Err())

				return
			}
			for _, ev := range wr.Events {
				if ev.Type == mvccpb.PUT {
					receivedKeys[string(ev.Kv.Key)] = true
				}
			}
			// Check if we got all keys
			if len(receivedKeys) >= len(keys) {
				break collectLoop
			}
		case <-timeout:
			result.Success = false
			result.Output = fmt.Sprintf("timeout: received %d/%d initial events", len(receivedKeys), len(keys))

			return
		}
	}

	// Verify all initial keys were delivered
	for _, key := range keys {
		if !receivedKeys[key] {
			result.Success = false
			result.Output = "missing initial event for key " + key

			return
		}
	}

	// Now add a new key and verify live event delivery
	newKey := path.Join(prefix, "key4")
	ctx, cancel = runner.NewCtx()
	_, err = cli.Put(ctx, newKey, "value-4")
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to put new key: %v", err)

		return
	}

	// Should receive the new key event
	select {
	case wr := <-wch:
		if wr.Err() != nil {
			result.Success = false
			result.Output = fmt.Sprintf("watch error on new key: %v", wr.Err())

			return
		}
		found := false
		for _, ev := range wr.Events {
			if string(ev.Kv.Key) == newKey && ev.Type == mvccpb.PUT {
				found = true

				break
			}
		}
		if !found {
			result.Success = false
			result.Output = "did not receive live event for new key"

			return
		}
	case <-time.After(5 * time.Second):
		result.Success = false
		result.Output = "timeout waiting for live event"

		return
	}

	result.Output = fmt.Sprintf("received %d initial events and 1 live event correctly", len(keys))
}

package scenarios

import (
	"context"
	"fmt"
	"time"

	testtime "git.tbd/etcd-infra/internal/etcd/testtime"
	clientv3 "go.etcd.io/etcd/client/v3"

	logutil "git.tbd/etcd-infra/pkg/log"
)

// RunWatchResumeAfterDisconnect validates watch resumption after network disruption
// similar to kube-apiserver watcher behavior (staging/src/k8s.io/apiserver/pkg/storage/etcd3/watcher.go).
//
//nolint:gocyclo // Scenario covers multiple disconnect/reconnect flows.
func RunWatchResumeAfterDisconnect(runner Runner) {
	logutil.S().Infow("running", "scenario", WatchResumeAfterDisconnect.String())

	result := &Result{
		Scenario:  WatchResumeAfterDisconnect.String(),
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
	defer func() {
		_ = cli.Close()
	}()

	testKey := runner.GenerateRandomKey(10)
	testPrefix := runner.GenerateRandomKey(10) + "/"

	// Put initial values to establish revision
	ctx, cancel := runner.NewCtx()
	putResp1, err := cli.Put(ctx, testKey, "initial")
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed initial put: %v", err)

		return
	}

	ctx, cancel = runner.NewCtx()
	putResp2, err := cli.Put(ctx, testPrefix+"key1", "value1")
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to put prefix key1: %v", err)

		return
	}

	// Start watch from current revision
	watchCtx, watchCancel := context.WithCancel(context.Background())
	defer watchCancel()

	startRev := putResp2.Header.Revision + 1
	watchChan := cli.Watch(watchCtx, testKey, clientv3.WithRev(startRev))

	// Also start a prefix watch with progress notify
	_ = cli.Watch(watchCtx, testPrefix,
		clientv3.WithPrefix(),
		clientv3.WithRev(putResp1.Header.Revision),
		clientv3.WithProgressNotify())

	// Collect initial watch responses
	select {
	case wresp := <-watchChan:
		if wresp.Err() != nil {
			result.Success = false
			result.Output = fmt.Sprintf("watch 1 error during init: %v", wresp.Err())

			return
		}
	case <-time.After(1 * time.Second):
		// OK - no events expected yet
	}

	// Put events while watch is active
	ctx, cancel = runner.NewCtx()
	putResp3, err := cli.Put(ctx, testKey, "update1")
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to put update1: %v", err)

		return
	}

	// Should receive the event
	select {
	case wresp := <-watchChan:
		if wresp.Err() != nil {
			result.Success = false
			result.Output = fmt.Sprintf("watch error: %v", wresp.Err())

			return
		}
		if len(wresp.Events) != 1 {
			result.Success = false
			result.Output = fmt.Sprintf("expected 1 event, got %d", len(wresp.Events))

			return
		}
		if string(wresp.Events[0].Kv.Value) != "update1" {
			result.Success = false
			result.Output = "unexpected event value"

			return
		}
	case <-time.After(2 * time.Second):
		result.Success = false
		result.Output = "timeout waiting for watch event"

		return
	}

	// Simulate disconnection by canceling watch context
	watchCancel()

	// Put events while disconnected
	ctx, cancel = runner.NewCtx()
	_, err = cli.Put(ctx, testKey, "update2")
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to put update2: %v", err)

		return
	}

	ctx, cancel = runner.NewCtx()
	putResp4, err := cli.Put(ctx, testKey, "update3")
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to put update3: %v", err)

		return
	}

	ctx, cancel = runner.NewCtx()
	_, err = cli.Put(ctx, testPrefix+"key2", "value2")
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to put prefix key2: %v", err)

		return
	}

	// Resume watch from last known revision
	watchCtx2, watchCancel2 := context.WithCancel(context.Background())
	defer watchCancel2()

	// Resume from revision after update1
	resumeRev := putResp3.Header.Revision + 1
	watchChan3 := cli.Watch(watchCtx2, testKey, clientv3.WithRev(resumeRev))

	// Should receive missed events
	eventCount := 0
	expectedEvents := 2 // update2 and update3

	for eventCount < expectedEvents {
		select {
		case wresp := <-watchChan3:
			if wresp.Err() != nil {
				result.Success = false
				result.Output = fmt.Sprintf("resumed watch error: %v", wresp.Err())

				return
			}
			for _, ev := range wresp.Events {
				eventCount++
				if eventCount == 1 && string(ev.Kv.Value) != "update2" {
					result.Success = false
					result.Output = fmt.Sprintf("expected update2, got %s", ev.Kv.Value)

					return
				}
				if eventCount == 2 && string(ev.Kv.Value) != "update3" {
					result.Success = false
					result.Output = fmt.Sprintf("expected update3, got %s", ev.Kv.Value)

					return
				}
			}
		case <-time.After(3 * time.Second):
			result.Success = false
			result.Output = fmt.Sprintf("timeout waiting for resumed events, got %d/%d", eventCount, expectedEvents)

			return
		}
	}

	// Test resuming from compacted revision (should get error)
	// First compact to current revision
	ctx, cancel = runner.NewCtx()
	_, err = cli.Compact(ctx, putResp4.Header.Revision)
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to compact: %v", err)

		return
	}

	// Try to watch from old revision (before compaction)
	watchChan4 := cli.Watch(watchCtx2, testKey, clientv3.WithRev(1))

	select {
	case wresp := <-watchChan4:
		if wresp.Err() == nil && wresp.CompactRevision == 0 {
			result.Success = false
			result.Output = "expected compaction error when watching from compacted revision"

			return
		}
		// Good - got compaction notification
	case <-time.After(2 * time.Second):
		result.Success = false
		result.Output = "timeout waiting for compaction error"

		return
	}

	// Test watch with RequestProgress
	ctx, cancel = runner.NewCtx()
	err = cli.RequestProgress(ctx)
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to request progress: %v", err)

		return
	}

	result.Output = fmt.Sprintf("successfully resumed watch after disconnect, received %d missed events", eventCount)
}

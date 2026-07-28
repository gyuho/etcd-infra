package scenarios

import (
	"fmt"
	"time"

	testtime "git.tbd/etcd-infra/internal/etcd/testtime"
	"go.etcd.io/etcd/api/v3/mvccpb"
	clientv3 "go.etcd.io/etcd/client/v3"

	logutil "git.tbd/etcd-infra/pkg/log"
)

// RunWatchBookmarkDelivery verifies watch bookmark events are delivered correctly.
// Kubernetes uses bookmarks to maintain accurate resourceVersion checkpoints for informers.
// ref. "clientv3/integration/TestWatchWithProgressNotify".
func RunWatchBookmarkDelivery(runner Runner) {
	logutil.S().Infow("running", "scenario", WatchBookmarkDelivery.String())

	result := &Result{
		Scenario:  WatchBookmarkDelivery.String(),
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
	prefix := testKey + "/"

	// Create initial data
	ctx, cancel := runner.NewCtx()
	putResp, err := cli.Put(ctx, prefix+"key1", "value1")
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to put initial key: %v", err)

		return
	}
	startRev := putResp.Header.Revision

	// Test 1: Watch with WithCreatedNotify should receive creation event first
	wctx, wcancel := runner.NewCtxTimeout(10 * time.Second)
	defer wcancel()

	wch := cli.Watch(wctx, prefix,
		clientv3.WithPrefix(),
		clientv3.WithCreatedNotify(),
		clientv3.WithRev(startRev),
	)
	if wch == nil {
		result.Success = false
		result.Output = watchCreateFailedMsg

		return
	}

	// First event should be the watch created notification
	select {
	case wr, open := <-wch:
		if !open {
			result.Success = false
			result.Output = "watch channel closed before created notification"

			return
		}
		if wr.Err() != nil {
			result.Success = false
			result.Output = fmt.Sprintf("watch error: %v", wr.Err())

			return
		}
		if !wr.Created {
			result.Success = false
			result.Output = "expected Created notification as first event"

			return
		}
		// Created notification should have the compact revision info
		if wr.CompactRevision != 0 && wr.CompactRevision > startRev {
			result.Success = false
			result.Output = fmt.Sprintf("unexpected CompactRevision %d > startRev %d", wr.CompactRevision, startRev)

			return
		}
	case <-time.After(5 * time.Second):
		result.Success = false
		result.Output = "timed out waiting for Created notification"

		return
	}

	// Write another key to trigger an event
	ctx, cancel = runner.NewCtx()
	putResp2, err := cli.Put(ctx, prefix+"key2", "value2")
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to put key2: %v", err)

		return
	}

	// Should receive a put event for key2 (other events may arrive too).
	key2 := prefix + "key2"
	key2Seen := false
	timeout := time.After(5 * time.Second)
	for !key2Seen {
		select {
		case wr, open := <-wch:
			if !open {
				result.Success = false
				result.Output = "watch channel closed before key2 event"

				return
			}
			if wr.Err() != nil {
				result.Success = false
				result.Output = fmt.Sprintf("watch error on put: %v", wr.Err())

				return
			}
			for _, ev := range wr.Events {
				if ev.Type != mvccpb.PUT {
					continue
				}
				if string(ev.Kv.Key) == key2 {
					key2Seen = true
					if wr.Header.Revision < putResp2.Header.Revision {
						result.Success = false
						result.Output = fmt.Sprintf("expected revision >= %d, got %d",
							putResp2.Header.Revision, wr.Header.Revision)

						return
					}

					break
				}
			}
		case <-timeout:
			result.Success = false
			result.Output = "timed out waiting for key2 put event"

			return
		}
	}

	// Test 2: WithProgressNotify should deliver bookmark events
	wctx2, wcancel2 := runner.NewCtxTimeout(15 * time.Second)
	defer wcancel2()

	// Start watch with progress notify enabled
	wch2 := cli.Watch(wctx2, prefix,
		clientv3.WithPrefix(),
		clientv3.WithProgressNotify(),
		clientv3.WithCreatedNotify(),
	)
	if wch2 == nil {
		result.Success = false
		result.Output = "failed to create watch with progress notify"

		return
	}

	// Skip the created notification
	select {
	case wr, open := <-wch2:
		if !open {
			result.Success = false
			result.Output = "watch2 channel closed before created notification"

			return
		}
		if !wr.Created {
			result.Success = false
			result.Output = "expected Created notification for watch2"

			return
		}
	case <-time.After(5 * time.Second):
		result.Success = false
		result.Output = "timed out waiting for watch2 Created notification"

		return
	}

	// Request progress manually
	ctx, cancel = runner.NewCtx()
	err = cli.RequestProgress(ctx)
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to request progress: %v", err)

		return
	}

	// Wait for progress notification (bookmark) - may take a few seconds
	progressReceived := false
	progressTimeout := time.After(10 * time.Second)
	for !progressReceived {
		select {
		case wr, open := <-wch2:
			if !open {
				result.Success = false
				result.Output = "watch2 channel closed before progress notification"

				return
			}
			if wr.Err() != nil {
				result.Success = false
				result.Output = fmt.Sprintf("watch2 error: %v", wr.Err())

				return
			}
			// Progress notification has IsProgressNotify true and no events
			if wr.IsProgressNotify() {
				progressReceived = true
				// Bookmark should have accurate revision
				if wr.Header.Revision < putResp2.Header.Revision {
					result.Success = false
					result.Output = fmt.Sprintf("progress revision %d < last put revision %d",
						wr.Header.Revision, putResp2.Header.Revision)

					return
				}
			}
			// Could receive other events, continue waiting
		case <-progressTimeout:
			result.Success = false
			result.Output = "timed out waiting for progress notification"

			return
		}
	}
}

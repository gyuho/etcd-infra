package scenarios

import (
	"errors"
	"fmt"
	"time"

	testtime "git.tbd/etcd-infra/internal/etcd/testtime"
	"go.etcd.io/etcd/api/v3/mvccpb"
	clientv3 "go.etcd.io/etcd/client/v3"

	logutil "git.tbd/etcd-infra/pkg/log"
)

var (
	errWatchChannelClosed = errors.New(watchChannelClosedMsg)
	errWatchEventTimeout  = errors.New("timed out waiting for watch event")
)

// RunWatchWithPrevKv tests the WatchWithPrevKv scenario.
func RunWatchWithPrevKv(runner Runner) {
	logutil.S().Infow("running", "scenario", WatchWithPrevKv.String())

	result := &Result{
		Scenario:  WatchWithPrevKv.String(),
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

	wctx, wcancel := runner.NewCtxTimeout(10 * time.Second)
	defer wcancel()

	testKey := runner.GenerateRandomKey(10)
	wch := cli.Watch(wctx, testKey, clientv3.WithPrevKV())
	if wch == nil {
		result.Success = false
		result.Output = watchCreateFailedMsg

		return
	}

	waitEvent := func(timeout time.Duration) (*clientv3.Event, int64, error) {
		deadline := time.After(timeout)
		for {
			select {
			case wr, open := <-wch:
				if !open {
					return nil, 0, errWatchChannelClosed
				}
				if wr.Err() != nil {
					return nil, 0, fmt.Errorf("watch error: %w", wr.Err())
				}
				if len(wr.Events) == 0 {
					continue
				}
				if len(wr.Events) != 1 {
					return nil, 0, fmt.Errorf("expected 1 event, got %d", len(wr.Events))
				}

				return wr.Events[0], wr.Header.GetRevision(), nil
			case <-deadline:
				return nil, 0, errWatchEventTimeout
			}
		}
	}

	ctx, cancel := runner.NewCtx()
	putResp1, err := cli.Put(ctx, testKey, "v1")
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to put v1: %v", err)

		return
	}

	ev, rev, err := waitEvent(5 * time.Second)
	if err != nil {
		result.Success = false
		result.Output = err.Error()

		return
	}
	// Watch header revision must be >= put revision (monotonic).
	if rev < putResp1.Header.Revision {
		result.Success = false
		result.Output = fmt.Sprintf("revision went backward: put %d, watch %d", putResp1.Header.Revision, rev)

		return
	}
	if ev.Type != mvccpb.PUT {
		result.Success = false
		result.Output = fmt.Sprintf("expected PUT event, got %v", ev.Type)

		return
	}
	if ev.Kv == nil || string(ev.Kv.Key) != testKey || string(ev.Kv.Value) != valueV1 {
		result.Success = false
		result.Output = "unexpected KV contents for initial put"

		return
	}
	if ev.Kv.CreateRevision != putResp1.Header.Revision || ev.Kv.ModRevision != putResp1.Header.Revision || ev.Kv.Version != 1 {
		result.Success = false
		result.Output = fmt.Sprintf(
			"unexpected KV metadata for initial put: create=%d mod=%d version=%d",
			ev.Kv.CreateRevision,
			ev.Kv.ModRevision,
			ev.Kv.Version,
		)

		return
	}
	if ev.PrevKv != nil {
		result.Success = false
		result.Output = "expected no PrevKv on initial put"

		return
	}

	ctx, cancel = runner.NewCtx()
	putResp2, err := cli.Put(ctx, testKey, "v2")
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to put v2: %v", err)

		return
	}

	ev, rev, err = waitEvent(5 * time.Second)
	if err != nil {
		result.Success = false
		result.Output = err.Error()

		return
	}
	// Watch header revision must be >= put revision (monotonic).
	if rev < putResp2.Header.Revision {
		result.Success = false
		result.Output = fmt.Sprintf("revision went backward: put %d, watch %d", putResp2.Header.Revision, rev)

		return
	}
	if ev.Type != mvccpb.PUT {
		result.Success = false
		result.Output = fmt.Sprintf("expected PUT update event, got %v", ev.Type)

		return
	}
	if ev.PrevKv == nil || string(ev.PrevKv.Value) != "v1" {
		result.Success = false
		result.Output = "expected PrevKv with v1 on update"

		return
	}
	if ev.PrevKv.ModRevision != putResp1.Header.Revision || ev.PrevKv.Version != 1 {
		result.Success = false
		result.Output = fmt.Sprintf(
			"unexpected PrevKv metadata on update: mod=%d version=%d",
			ev.PrevKv.ModRevision,
			ev.PrevKv.Version,
		)

		return
	}
	if ev.Kv == nil || string(ev.Kv.Value) != valueV2 {
		result.Success = false
		result.Output = "unexpected KV contents for update"

		return
	}
	if ev.Kv.CreateRevision != putResp1.Header.Revision || ev.Kv.ModRevision != putResp2.Header.Revision || ev.Kv.Version != 2 {
		result.Success = false
		result.Output = fmt.Sprintf(
			"unexpected KV metadata for update: create=%d mod=%d version=%d",
			ev.Kv.CreateRevision,
			ev.Kv.ModRevision,
			ev.Kv.Version,
		)

		return
	}

	ctx, cancel = runner.NewCtx()
	delResp, err := cli.Delete(ctx, testKey)
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to delete: %v", err)

		return
	}

	ev, rev, err = waitEvent(5 * time.Second)
	if err != nil {
		result.Success = false
		result.Output = err.Error()

		return
	}
	// Watch header revision must be >= delete revision (monotonic).
	if rev < delResp.Header.Revision {
		result.Success = false
		result.Output = fmt.Sprintf("revision went backward: delete %d, watch %d", delResp.Header.Revision, rev)

		return
	}
	if ev.Type != mvccpb.DELETE {
		result.Success = false
		result.Output = fmt.Sprintf("expected DELETE event, got %v", ev.Type)

		return
	}
	if ev.Kv == nil || string(ev.Kv.Key) != testKey {
		result.Success = false
		result.Output = "unexpected KV contents for delete"

		return
	}
	if ev.PrevKv == nil || string(ev.PrevKv.Value) != valueV2 {
		result.Success = false
		result.Output = fmt.Sprintf("expected PrevKv with %s on delete", valueV2)

		return
	}
	if ev.PrevKv.ModRevision != putResp2.Header.Revision || ev.PrevKv.Version != 2 {
		result.Success = false
		result.Output = fmt.Sprintf(
			"unexpected PrevKv metadata on delete: mod=%d version=%d",
			ev.PrevKv.ModRevision,
			ev.PrevKv.Version,
		)

		return
	}
}

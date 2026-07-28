package scenarios

import (
	"context"
	"fmt"
	"reflect"
	"time"

	testtime "git.tbd/etcd-infra/internal/etcd/testtime"
	"go.etcd.io/etcd/api/v3/mvccpb"
	clientv3 "go.etcd.io/etcd/client/v3"

	logutil "git.tbd/etcd-infra/pkg/log"
)

// RunWatchWithRequestProgress tests the WatchWithRequestProgress scenario.
func RunWatchWithRequestProgress(runner Runner) {
	logutil.S().Infow("running", "scenario", WatchWithRequestProgress.String())

	result := &Result{
		Scenario:  WatchWithRequestProgress.String(),
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

	createRev := int64(0)
	for idx, tt := range []struct {
		watchPfxs []string
	}{
		{watchPfxs: []string{testKey}},
		{watchPfxs: []string{testKey, testKey}},
	} {
		wchs := make([]clientv3.WatchChan, len(tt.watchPfxs))
		for idx, pfx := range tt.watchPfxs {
			wchs[idx] = cli.Watch(cctx, pfx, clientv3.WithPrefix())
		}

		ctx, cancel := runner.NewCtx()
		presp, err := cli.Put(ctx, testKey, "bar")
		cancel()
		if err != nil {
			result.Success = false
			result.Output = fmt.Sprintf("failed to put: %v", err)

			return
		}
		putRev := presp.Header.GetRevision()
		if createRev == 0 {
			createRev = putRev
		}

		evs := []*clientv3.Event{
			{
				Type: mvccpb.PUT,
				Kv:   &mvccpb.KeyValue{Key: []byte(testKey), Value: []byte("bar"), CreateRevision: createRev, ModRevision: putRev, Version: int64(idx + 1)},
			},
		}

		for _, wch := range wchs {
			select {
			case wr, open := <-wch: // wait for notification
				if !open {
					result.Success = false
					result.Output = watchChannelClosedMsg

					return
				}
				if !reflect.DeepEqual(wr.Events, evs) {
					result.Success = false
					result.Output = fmt.Sprintf("expected events %+v, got %+v", evs, wr.Events)

					return
				}

			case <-time.After(5 * time.Second):
				result.Success = false
				result.Output = "watch channel did not receive any events after put"

				return
			}
		}

		// write a key that's not being watched, thus no watch event trigger
		ctx, cancel = runner.NewCtx()
		presp, err = cli.Put(ctx, runner.GenerateRandomKey(12), "bar")
		cancel()
		if err != nil {
			result.Success = false
			result.Output = fmt.Sprintf("failed to put: %v", err)

			return
		}
		putRev = presp.Header.GetRevision()

		// In a multi-node etcd cluster, there's a brief replication window where
		// the node serving the watch stream may not have replicated the latest
		// revision yet. We retry a few times to allow for propagation.
		const maxProgressRetries = 3
		for _, wch := range wchs {
			var progressOK bool
			for attempt := range maxProgressRetries {
				if err = cli.RequestProgress(context.TODO()); err != nil {
					result.Success = false
					result.Output = fmt.Sprintf("failed to request progress: %v", err)

					return
				}

				select {
				case wr, open := <-wch: // wait for notification
					if !open {
						result.Success = false
						result.Output = watchChannelClosedMsg

						return
					}
					if !wr.IsProgressNotify() {
						result.Success = false
						result.Output = "expected progress notify, but got none"

						return
					}
					// Enforce monotonic revision increase: the progress notification
					// revision must be >= the put revision. On warm Kubernetes clusters,
					// background writes (lease renewals, endpoint updates, node heartbeats)
					// continuously increment the revision. We do not enforce a specific
					// tolerance; only that revisions are moving forward.
					switch {
					case wr.Header.GetRevision() >= putRev:
						progressOK = true
					case attempt < maxProgressRetries-1:
						// Replication may still be in flight. Wait briefly before retrying.
						time.Sleep(50 * time.Millisecond)
						continue
					default:
						result.Success = false
						result.Output = fmt.Sprintf("revision went backward after %d retries: put revision %d, progress revision %d", maxProgressRetries, putRev, wr.Header.GetRevision())

						return
					}

				case <-time.After(10 * time.Second):
					result.Success = false
					result.Output = "watch channel did not receive any events after put + request progress"

					return
				}

				if progressOK {
					break
				}
			}
		}
	}
}

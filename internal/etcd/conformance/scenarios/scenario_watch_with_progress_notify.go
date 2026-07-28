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

// RunWatchWithProgressNotify tests the WatchWithProgressNotify scenario.
func RunWatchWithProgressNotify(runner Runner) {
	logutil.S().Infow("running", "scenario", WatchWithProgressNotify.String())

	result := &Result{
		Scenario:  WatchWithProgressNotify.String(),
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
	wch := cli.Watch(cctx, testKey, clientv3.WithProgressNotify())
	if wch == nil {
		result.Success = false
		result.Output = watchCreateFailedMsg

		return
	}

	ctx, cancel := runner.NewCtx()
	gresp, err := cli.Get(ctx, testKey)
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to get current revision: %v", err)

		return
	}
	currentRev := gresp.Header.GetRevision()

	ctx, cancel = runner.NewCtx()
	if progressErr := cli.RequestProgress(ctx); progressErr != nil {
		cancel()
		result.Success = false
		result.Output = fmt.Sprintf("failed to request progress: %v", progressErr)

		return
	}
	cancel()

	select {
	case wr, open := <-wch:
		if !open {
			result.Success = false
			result.Output = watchChannelClosedMsg

			return
		}
		// Enforce monotonic revision: progress notification revision must be >= current revision.
		// On warm clusters, background writes continuously increment the revision.
		if wr.Header.GetRevision() < currentRev {
			result.Success = false
			result.Output = fmt.Sprintf("revision went backward: current %d, progress %d", currentRev, wr.Header.GetRevision())

			return
		}
		if !wr.IsProgressNotify() {
			result.Success = false
			result.Output = "expected progress notify"

			return
		}
		if len(wr.Events) > 0 {
			result.Success = false
			result.Output = fmt.Sprintf("expected no event from progress notify, got %+v", wr.Events)

			return
		}

	case <-time.After(10 * time.Second):
		result.Success = false
		result.Output = "watch channel did not receive any events with progress notify"

		return
	}

	ctx, cancel = runner.NewCtx()
	presp, err := cli.Put(ctx, testKey, "bar")
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to put: %v", err)

		return
	}
	putRev := presp.Header.GetRevision()

	evs := []*clientv3.Event{
		{
			Type: mvccpb.PUT,
			Kv:   &mvccpb.KeyValue{Key: []byte(testKey), Value: []byte("bar"), CreateRevision: putRev, ModRevision: putRev, Version: 1},
		},
	}
	select {
	case wr, open := <-wch:
		if !open {
			result.Success = false
			result.Output = watchChannelClosedMsg

			return
		}
		// Header revision must be >= putRev (monotonic). On warm clusters,
		// background writes can push the server revision higher between the
		// put and the watch event delivery.
		if wr.Header.GetRevision() < putRev {
			result.Success = false
			result.Output = fmt.Sprintf("revision went backward: put revision %d, watch header revision %d", putRev, wr.Header.GetRevision())

			return
		}
		if wr.IsProgressNotify() {
			result.Success = false
			result.Output = "unexpected progress notify after put"

			return
		}
		if !reflect.DeepEqual(wr.Events, evs) {
			result.Success = false
			result.Output = fmt.Sprintf("expected events %+v, got %+v", evs, wr.Events)

			return
		}

	case <-time.After(5 * time.Second):
		result.Success = false
		result.Output = watchChannelDidNotReceiveEventsMsg

		return
	}
}

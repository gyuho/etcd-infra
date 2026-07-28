package scenarios

import (
	"fmt"
	"reflect"
	"time"

	testtime "git.tbd/etcd-infra/internal/etcd/testtime"
	"go.etcd.io/etcd/api/v3/mvccpb"
	clientv3 "go.etcd.io/etcd/client/v3"

	logutil "git.tbd/etcd-infra/pkg/log"
)

// RunWatchAndPutWithOldRevision tests the WatchAndPutWithOldRevision scenario.
func RunWatchAndPutWithOldRevision(runner Runner) {
	logutil.S().Infow("running", "scenario", WatchAndPutWithOldRevision.String())

	result := &Result{
		Scenario:  WatchAndPutWithOldRevision.String(),
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
	ctx, cancel := runner.NewCtx()
	presp, err := cli.Put(ctx, testKey, "bar1")
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to put: %v", err)

		return
	}
	putRev1 := presp.Header.GetRevision()

	// whole test shouldn't take more than 15 seconds
	cctx, ccancel := runner.NewCtxTimeout(15 * time.Second)
	defer ccancel()

	wch := cli.Watch(cctx, testKey, clientv3.WithRev(putRev1))
	if wch == nil {
		result.Success = false
		result.Output = watchCreateFailedMsg

		return
	}

	select {
	case <-time.After(5 * time.Second):
		result.Success = false
		result.Output = "watch took too long"

		return

	case wr, open := <-wch:
		if !open {
			result.Success = false
			result.Output = "watch channel closed"

			return
		}
		evs := []*clientv3.Event{
			{
				Type: mvccpb.PUT,
				Kv: &mvccpb.KeyValue{
					Key:            []byte(testKey),
					Value:          []byte("bar1"),
					CreateRevision: putRev1,
					ModRevision:    putRev1,
					Version:        1,
				},
			},
		}
		if !reflect.DeepEqual(wr.Events, evs) {
			result.Success = false
			result.Output = fmt.Sprintf("watch response mismatch, expected %+v, got %+v", evs, wr.Events)

			return
		}
	}

	ctx, cancel = runner.NewCtx()
	presp, err = cli.Put(ctx, testKey, "bar2")
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to put: %v", err)

		return
	}
	putRev2 := presp.Header.GetRevision()

	for idx, tt := range []struct {
		watchRev int64
		events   []*clientv3.Event
	}{
		{ // revision before second PUT
			watchRev: putRev1,
			events: []*clientv3.Event{
				{
					Type: mvccpb.PUT,
					Kv:   &mvccpb.KeyValue{Key: []byte(testKey), Value: []byte("bar1"), CreateRevision: putRev1, ModRevision: putRev1, Version: 1},
				},
				{
					Type: mvccpb.PUT,
					Kv:   &mvccpb.KeyValue{Key: []byte(testKey), Value: []byte("bar2"), CreateRevision: putRev1, ModRevision: putRev2, Version: 2},
				},
			},
		},
		{ // revision before first PUT
			watchRev: putRev1 - 1,
			events: []*clientv3.Event{
				{
					Type: mvccpb.PUT,
					Kv:   &mvccpb.KeyValue{Key: []byte(testKey), Value: []byte("bar1"), CreateRevision: putRev1, ModRevision: putRev1, Version: 1},
				},
				{
					Type: mvccpb.PUT,
					Kv:   &mvccpb.KeyValue{Key: []byte(testKey), Value: []byte("bar2"), CreateRevision: putRev1, ModRevision: putRev2, Version: 2},
				},
			},
		},
	} {
		wch := cli.Watch(cctx, testKey, clientv3.WithRev(tt.watchRev))
		wr, open := <-wch
		if !open {
			result.Success = false
			result.Output = fmt.Sprintf("#%d: watch channel closed", idx)

			return
		}
		if !reflect.DeepEqual(wr.Events, tt.events) {
			result.Success = false
			result.Output = fmt.Sprintf("#%d: watch response mismatch, expected %+v, got %+v", idx, tt.events, wr.Events)

			return
		}
	}
}

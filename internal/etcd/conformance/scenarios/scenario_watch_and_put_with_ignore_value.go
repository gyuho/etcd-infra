package scenarios

import (
	"context"
	"fmt"

	testtime "git.tbd/etcd-infra/internal/etcd/testtime"
	"go.etcd.io/etcd/api/v3/mvccpb"
	clientv3 "go.etcd.io/etcd/client/v3"
	"google.golang.org/protobuf/proto"

	logutil "git.tbd/etcd-infra/pkg/log"
)

// RunWatchAndPutWithIgnoreValue tests the WatchAndPutWithIgnoreValue scenario.
func RunWatchAndPutWithIgnoreValue(runner Runner) {
	logutil.S().Infow("running", "scenario", WatchAndPutWithIgnoreValue.String())

	result := &Result{
		Scenario:  WatchAndPutWithIgnoreValue.String(),
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
	wch := cli.Watch(cctx, testKey)
	if wch == nil {
		result.Success = false
		result.Output = watchCreateFailedMsg

		return
	}

	ctx, cancel := runner.NewCtx()
	presp1, err := cli.Put(ctx, testKey, "bar")
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to put: %v", err)

		return
	}

	watchResp := <-wch
	// Watch header revision must be >= put revision (monotonic).
	if watchResp.Header.Revision < presp1.Header.Revision {
		result.Success = false
		result.Output = fmt.Sprintf("revision went backward: put %d, watch %d", presp1.Header.Revision, watchResp.Header.Revision)

		return
	}
	if len(watchResp.Events) != 1 {
		result.Success = false
		result.Output = fmt.Sprintf("expected 1 event, got %d", len(watchResp.Events))

		return
	}

	ev1 := &clientv3.Event{
		Type: clientv3.EventTypePut,
		Kv: &mvccpb.KeyValue{
			Key:            []byte(testKey),
			Value:          []byte("bar"),
			CreateRevision: presp1.Header.Revision,
			ModRevision:    presp1.Header.Revision,
			Version:        1,
		},
	}
	ev2 := watchResp.Events[0]
	if !proto.Equal(ev1, ev2) {
		result.Success = false
		result.Output = fmt.Sprintf("events mismatch, expected %+v, got %+v", ev1, ev2)

		return
	}

	ctx, cancel = runner.NewCtx()
	presp2, err := cli.Put(ctx, testKey, "", clientv3.WithIgnoreValue())
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to put: %v", err)

		return
	}

	watchResp = <-wch

	// Watch header revision must be >= put revision (monotonic).
	if watchResp.Header.Revision < presp2.Header.Revision {
		result.Success = false
		result.Output = fmt.Sprintf("revision went backward: put %d, watch %d", presp2.Header.Revision, watchResp.Header.Revision)

		return
	}
	if len(watchResp.Events) != 1 {
		result.Success = false
		result.Output = fmt.Sprintf("expected 1 event, got %d", len(watchResp.Events))

		return
	}

	ev3 := &clientv3.Event{
		Type: clientv3.EventTypePut,
		Kv: &mvccpb.KeyValue{
			Key:            []byte(testKey),
			Value:          []byte("bar"),
			CreateRevision: presp1.Header.Revision,
			ModRevision:    presp2.Header.Revision,
			Version:        2,
		},
	}
	ev4 := watchResp.Events[0]
	if !proto.Equal(ev3, ev4) {
		result.Success = false
		result.Output = fmt.Sprintf("events mismatch, expected %+v, got %+v", ev3, ev4)

		return
	}
}

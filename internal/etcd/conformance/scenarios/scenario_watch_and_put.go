package scenarios

import (
	"fmt"
	"time"

	testtime "git.tbd/etcd-infra/internal/etcd/testtime"
	"go.etcd.io/etcd/api/v3/mvccpb"
	clientv3 "go.etcd.io/etcd/client/v3"
	"google.golang.org/protobuf/proto"

	logutil "git.tbd/etcd-infra/pkg/log"
)

// RunWatchAndPut tests the WatchAndPut scenario.
func RunWatchAndPut(runner Runner) {
	logutil.S().Infow("running", "scenario", WatchAndPut.String())

	result := &Result{
		Scenario:  WatchAndPut.String(),
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

	// whole test shouldn't take more than 15 seconds
	cctx, ccancel := runner.NewCtxTimeout(15 * time.Second)
	defer ccancel()

	testKey := runner.GenerateRandomKey(10)
	wch := cli.Watch(cctx, testKey)
	if wch == nil {
		result.Success = false
		result.Output = watchCreateFailedMsg

		return
	}

	ctx, cancel := runner.NewCtx()
	presp, err := cli.Put(ctx, testKey, "bar")
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to put: %v", err)

		return
	}

	wr, open := <-wch
	if !open {
		result.Success = false
		result.Output = watchChannelClosedMsg2

		return
	}
	// Watch header revision must be >= put revision (monotonic). On warm clusters,
	// background writes can push the server revision higher between put and watch delivery.
	if wr.Header.GetRevision() < presp.Header.GetRevision() {
		result.Success = false
		result.Output = fmt.Sprintf("revision went backward: put revision %d, watch revision %d", presp.Header.GetRevision(), wr.Header.GetRevision())

		return
	}
	if len(wr.Events) != 1 {
		result.Success = false
		result.Output = fmt.Sprintf("expected 1 event, got %d", len(wr.Events))

		return
	}

	ev := wr.Events[0]
	expected := &clientv3.Event{
		Type: clientv3.EventTypePut,
		Kv: &mvccpb.KeyValue{
			Key:            []byte(testKey),
			Value:          []byte("bar"),
			CreateRevision: presp.Header.Revision,
			ModRevision:    presp.Header.Revision,
			Version:        1,
		},
	}
	if !proto.Equal(expected, ev) {
		result.Success = false
		result.Output = fmt.Sprintf("expected %+v, got %+v", expected, ev)

		return
	}
}

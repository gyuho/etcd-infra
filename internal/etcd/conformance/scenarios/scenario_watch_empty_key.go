package scenarios

import (
	"context"
	"fmt"
	"time"

	testtime "git.tbd/etcd-infra/internal/etcd/testtime"
	"go.etcd.io/etcd/api/v3/mvccpb"
	clientv3 "go.etcd.io/etcd/client/v3"
	"google.golang.org/protobuf/proto"

	logutil "git.tbd/etcd-infra/pkg/log"
)

// RunWatchEmptyKey tests the WatchEmptyKey scenario.
func RunWatchEmptyKey(runner Runner) {
	logutil.S().Infow("running", "scenario", WatchEmptyKey.String())

	result := &Result{
		Scenario:  WatchEmptyKey.String(),
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

	wch := cli.Watch(cctx, "")
	if wch == nil {
		result.Success = false
		result.Output = watchCreateFailedMsg

		return
	}

	select {
	case wr, open := <-wch:
		result.Success = false
		result.Output = fmt.Sprintf("unexpected watch response %+v (open %v)", wr, open)
	case <-time.After(time.Second):
	}

	// Use a unique prefix to watch only our test key, avoiding interference
	// from other writes in a shared Kubernetes cluster.
	testPrefix := runner.GenerateRandomKey(10) + "/"
	testKey := testPrefix + "bar"

	// Watch our specific prefix before putting
	wch = cli.Watch(cctx, testPrefix, clientv3.WithPrefix())
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
	putRev := presp.Header.GetRevision()

	// Wait for our event with timeout
	select {
	case wr, open := <-wch:
		if !open {
			result.Success = false
			result.Output = watchChannelClosedMsg

			return
		}
		if wr.Canceled {
			result.Success = false
			result.Output = watchChannelCanceledMsg

			return
		}
		if len(wr.Events) != 1 {
			result.Success = false
			result.Output = fmt.Sprintf("unexpected number of events %d", len(wr.Events))

			return
		}

		ev := wr.Events[0]
		expected := &clientv3.Event{
			Type: clientv3.EventTypePut,
			Kv: &mvccpb.KeyValue{
				Key:            []byte(testKey),
				Value:          []byte("bar"),
				CreateRevision: putRev,
				ModRevision:    putRev,
				Version:        1,
			},
		}
		if !proto.Equal(ev, expected) {
			result.Success = false
			result.Output = fmt.Sprintf("unexpected event %+v, expected %+v", ev, expected)

			return
		}
	case <-time.After(30 * time.Second):
		result.Success = false
		result.Output = watchEventTimeoutMsg

		return
	}
}

package scenarios

import (
	"context"
	"fmt"
	"reflect"
	"strconv"
	"time"

	testtime "git.tbd/etcd-infra/internal/etcd/testtime"
	"go.etcd.io/etcd/api/v3/mvccpb"
	clientv3 "go.etcd.io/etcd/client/v3"
	"google.golang.org/grpc/metadata"

	logutil "git.tbd/etcd-infra/pkg/log"
	"git.tbd/etcd-infra/pkg/randutil"
)

// RunWatchCancelOverlappingContext tests the WatchCancelOverlappingContext scenario.
func RunWatchCancelOverlappingContext(runner Runner) {
	logutil.S().Infow("running", "scenario", WatchCancelOverlappingContext.String())

	result := &Result{
		Scenario:  WatchCancelOverlappingContext.String(),
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

	watchers := 10
	streams := 4
	streamCtxs := make([]context.Context, streams)
	for idx := range streamCtxs {
		// gRPC metadata keys must be lowercase letters, digits, and -_.
		// Using a sanitized prefix avoids validation errors when opening watch streams.
		mdKey := fmt.Sprintf("stream-%02d", idx)
		md := metadata.Pairs(mdKey, strconv.Itoa(idx))
		streamCtxs[idx] = metadata.NewOutgoingContext(context.Background(), md)
	}

	testKey := runner.GenerateRandomKey(30)
	ctx, cancel := runner.NewCtx()
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
			Kv: &mvccpb.KeyValue{
				Key:            []byte(testKey),
				Value:          []byte("bar"),
				CreateRevision: putRev,
				ModRevision:    putRev,
				Version:        1,
			},
		},
	}
	watchTimeout := 10 * time.Second
	for i := range watchers {
		streamIdx := randutil.Intn(len(streamCtxs))

		ctx, cancel := context.WithCancel(streamCtxs[streamIdx])
		wch := cli.Watch(ctx, testKey, clientv3.WithRev(putRev))
		if wch == nil {
			result.Success = false
			result.Output = watchCreateFailedMsg
			cancel()

			return
		}

		select {
		case wr, open := <-wch:
			cancel()
			if !open {
				result.Success = false
				result.Output = fmt.Sprintf("watch channel %d is closed", i)

				return
			}
			if !reflect.DeepEqual(wr.Events, evs) {
				result.Success = false
				result.Output = fmt.Sprintf("expected watch events %+v, got %+v", evs, wr.Events)

				return
			}

		case <-time.After(watchTimeout):
			cancel()
			result.Success = false
			result.Output = fmt.Sprintf("watch %d took too long to receive events", i)

			return
		}
	}
}

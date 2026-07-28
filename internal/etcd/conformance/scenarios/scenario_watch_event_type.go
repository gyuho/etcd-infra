package scenarios

import (
	"context"
	"fmt"
	"path"
	"time"

	testtime "git.tbd/etcd-infra/internal/etcd/testtime"
	"go.etcd.io/etcd/api/v3/mvccpb"
	clientv3 "go.etcd.io/etcd/client/v3"

	logutil "git.tbd/etcd-infra/pkg/log"
)

// RunWatchEventType tests the WatchEventType scenario.
func RunWatchEventType(runner Runner) {
	logutil.S().Infow("running", "scenario", WatchEventType.String())

	result := &Result{
		Scenario:  WatchEventType.String(),
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
	wch := cli.Watch(cctx, testKey, clientv3.WithPrefix())
	if wch == nil {
		result.Success = false
		result.Output = watchCreateFailedMsg

		return
	}

	ctx, cancel := runner.NewCtx()
	_, err = cli.Put(ctx, path.Join(testKey, "delete-key"), "bar1")
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to put: %v", err)

		return
	}

	ctx, cancel = runner.NewCtx()
	_, err = cli.Put(ctx, path.Join(testKey, "delete-key"), "bar2")
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to put: %v", err)

		return
	}

	ctx, cancel = runner.NewCtx()
	_, err = cli.Delete(ctx, path.Join(testKey, "delete-key"))
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to delete: %v", err)

		return
	}

	ctx, cancel = runner.NewCtx()
	lresp, err := cli.Grant(ctx, 1)
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to grant lease: %v", err)

		return
	}
	leaseID := lresp.ID

	ctx, cancel = runner.NewCtx()
	_, err = cli.Put(ctx, path.Join(testKey, "expire"), "foo", clientv3.WithLease(leaseID))
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to put: %v", err)

		return
	}

	tests := []struct {
		et       mvccpb.Event_EventType
		isCreate bool
		isModify bool
	}{
		{
			et:       clientv3.EventTypePut,
			isCreate: true,
			isModify: false,
		},
		{
			et:       clientv3.EventTypePut,
			isCreate: false,
			isModify: true,
		},
		{
			et:       clientv3.EventTypeDelete,
			isCreate: false,
			isModify: false,
		},
		{
			et:       clientv3.EventTypePut,
			isCreate: true,
			isModify: false,
		},
		{
			et:       clientv3.EventTypeDelete,
			isCreate: false,
			isModify: false,
		},
	}
	var received []*clientv3.Event
	for {
		select {
		case wr, open := <-wch:
			if !open {
				result.Success = false
				result.Output = watchChannelClosedMsg

				return
			}
			received = append(received, wr.Events...)

		case <-time.After(10 * time.Second):
			result.Success = false
			result.Output = fmt.Sprintf("expected %d events and then break out loop", len(tests))

			return
		}

		if len(received) == len(tests) {
			break
		}
	}
	for i, tt := range tests {
		ev := received[i]
		if tt.et != ev.Type {
			result.Success = false
			result.Output = fmt.Sprintf("expected event type %v, got %v", tt.et, ev.Type)

			return
		}
		if tt.isCreate && !ev.IsCreate() {
			result.Success = false
			result.Output = fmt.Sprintf("expected event %v to be created", ev)

			return
		}
		if tt.isModify && !ev.IsModify() {
			result.Success = false
			result.Output = fmt.Sprintf("expected event %v to be modified", ev)

			return
		}
	}
}

package scenarios

import (
	"context"
	"fmt"
	"reflect"
	"time"

	testtime "git.tbd/etcd-infra/internal/etcd/testtime"
	"go.etcd.io/etcd/api/v3/mvccpb"
	"go.etcd.io/etcd/client/v3/mirror"

	logutil "git.tbd/etcd-infra/pkg/log"
)

// RunMirrorSyncUpdates tests the MirrorSyncUpdates scenario.
func RunMirrorSyncUpdates(runner Runner) {
	logutil.S().Infow("running", "scenario", MirrorSyncUpdates.String())

	result := &Result{
		Scenario:  MirrorSyncUpdates.String(),
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
	putRev := presp.Header.GetRevision()

	wkvs := []*mvccpb.KeyValue{
		{
			Key:            []byte(testKey),
			Value:          []byte("bar1"),
			CreateRevision: putRev,
			ModRevision:    putRev,
			Version:        1,
		},
	}

	cctx, ccancel := context.WithCancel(context.Background())
	defer ccancel()

	syncer := mirror.NewSyncer(cli, testKey, 0)
	rch, errc := syncer.SyncBase(cctx)

	// Read from rch with timeout
	timeout := time.After(3 * time.Second)
readLoop:
	for {
		select {
		case rv, open := <-rch:
			if !open {
				break readLoop
			}
			if !reflect.DeepEqual(rv.Kvs, wkvs) {
				result.Success = false
				result.Output = fmt.Sprintf("expected %v, got %v", wkvs, rv.Kvs)

				return
			}
		case <-timeout:
			result.Success = false
			result.Output = "timed out waiting for sync response"

			return
		}
	}

	// Read from errc with timeout
	errTimeout := time.After(5 * time.Second)
errLoop:
	for {
		select {
		case syncErr, open := <-errc:
			if !open {
				break errLoop
			}
			result.Success = false
			result.Output = fmt.Sprintf("failed to sync: %v", syncErr)

			return
		case <-errTimeout:
			result.Success = false
			result.Output = "timed out waiting for sync error channel"

			return
		}
	}

	updateCh := syncer.SyncUpdates(cctx)

	ctx, cancel = runner.NewCtx()
	presp2, err := cli.Put(ctx, testKey, "bar2")
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to put: %v", err)

		return
	}
	putRev2 := presp2.Header.GetRevision()

	select {
	case uv := <-updateCh:
		wkv := &mvccpb.KeyValue{
			Key:            []byte(testKey),
			Value:          []byte("bar2"),
			CreateRevision: putRev,
			ModRevision:    putRev2, // Use actual revision from second Put, not putRev+1
			Version:        2,
		}
		if !reflect.DeepEqual(uv.Events[0].Kv, wkv) {
			result.Success = false
			result.Output = fmt.Sprintf("expected key-value %+v, want %+v", uv.Events[0].Kv, wkv)

			return
		}

	case <-time.After(time.Second):
		result.Success = false
		result.Output = "timed out waiting for update"

		return
	}
}

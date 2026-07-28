package scenarios

import (
	"fmt"
	"time"

	testtime "git.tbd/etcd-infra/internal/etcd/testtime"
	"go.etcd.io/etcd/api/v3/mvccpb"
	clientv3 "go.etcd.io/etcd/client/v3"

	logutil "git.tbd/etcd-infra/pkg/log"
)

// RunModRevisionConsistency verifies ModRevision is stable and correctly updated on individual keys.
// Kubernetes uses ModRevision for optimistic concurrency control in GuaranteedUpdate.
func RunModRevisionConsistency(runner Runner) {
	logutil.S().Infow("running", "scenario", ModRevisionConsistency.String())

	result := &Result{
		Scenario:  ModRevisionConsistency.String(),
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

	// Test 1: CreateRevision == ModRevision on first put
	ctx, cancel := runner.NewCtx()
	putResp, err := cli.Put(ctx, testKey, "v1")
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to put v1: %v", err)

		return
	}
	createRev := putResp.Header.Revision

	ctx, cancel = runner.NewCtx()
	getResp, err := cli.Get(ctx, testKey)
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to get after create: %v", err)

		return
	}
	if len(getResp.Kvs) != 1 {
		result.Success = false
		result.Output = "expected 1 kv"

		return
	}
	kv := getResp.Kvs[0]
	if kv.CreateRevision != createRev {
		result.Success = false
		result.Output = fmt.Sprintf("expected CreateRevision %d, got %d", createRev, kv.CreateRevision)

		return
	}
	if kv.ModRevision != createRev {
		result.Success = false
		result.Output = fmt.Sprintf("expected ModRevision %d == CreateRevision on first put, got %d",
			createRev, kv.ModRevision)

		return
	}
	if kv.Version != 1 {
		result.Success = false
		result.Output = fmt.Sprintf("expected Version 1 on first put, got %d", kv.Version)

		return
	}

	// Test 2: ModRevision updates on subsequent puts, CreateRevision stays same
	ctx, cancel = runner.NewCtx()
	putResp2, err := cli.Put(ctx, testKey, valueV2)
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to put %s: %v", valueV2, err)

		return
	}
	modRev2 := putResp2.Header.Revision

	ctx, cancel = runner.NewCtx()
	getResp, err = cli.Get(ctx, testKey)
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to get after update: %v", err)

		return
	}
	kv = getResp.Kvs[0]
	if kv.CreateRevision != createRev {
		result.Success = false
		result.Output = fmt.Sprintf("CreateRevision should remain %d after update, got %d",
			createRev, kv.CreateRevision)

		return
	}
	if kv.ModRevision != modRev2 {
		result.Success = false
		result.Output = fmt.Sprintf("expected ModRevision %d after update, got %d", modRev2, kv.ModRevision)

		return
	}
	if kv.ModRevision <= createRev {
		result.Success = false
		result.Output = fmt.Sprintf("ModRevision %d should be > CreateRevision %d after update",
			kv.ModRevision, createRev)

		return
	}
	if kv.Version != 2 {
		result.Success = false
		result.Output = fmt.Sprintf("expected Version 2 after update, got %d", kv.Version)

		return
	}

	// Test 3: Multiple reads should return consistent ModRevision
	for i := range 5 {
		ctx, cancel = runner.NewCtx()
		resp, getErr := cli.Get(ctx, testKey)
		cancel()
		if getErr != nil {
			result.Success = false
			result.Output = fmt.Sprintf("failed to get (iteration %d): %v", i, getErr)

			return
		}
		if resp.Kvs[0].ModRevision != modRev2 {
			result.Success = false
			result.Output = fmt.Sprintf("ModRevision inconsistent on read %d: expected %d, got %d",
				i, modRev2, resp.Kvs[0].ModRevision)

			return
		}
	}

	// Test 4: Watch events should include accurate ModRevision
	wctx, wcancel := runner.NewCtxTimeout(10 * time.Second)
	defer wcancel()

	// Watch from current revision
	wch := cli.Watch(wctx, testKey, clientv3.WithRev(modRev2+1))
	if wch == nil {
		result.Success = false
		result.Output = watchCreateFailedMsg

		return
	}

	ctx, cancel = runner.NewCtx()
	putResp3, err := cli.Put(ctx, testKey, "v3")
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to put v3: %v", err)

		return
	}
	modRev3 := putResp3.Header.Revision

	select {
	case wr, open := <-wch:
		if !open {
			result.Success = false
			result.Output = watchChannelClosedMsg2

			return
		}
		if wr.Err() != nil {
			result.Success = false
			result.Output = fmt.Sprintf("watch error: %v", wr.Err())

			return
		}
		if len(wr.Events) != 1 {
			result.Success = false
			result.Output = fmt.Sprintf("expected 1 event, got %d", len(wr.Events))

			return
		}
		ev := wr.Events[0]
		if ev.Type != mvccpb.PUT {
			result.Success = false
			result.Output = fmt.Sprintf("expected PUT event, got %v", ev.Type)

			return
		}
		if ev.Kv.ModRevision != modRev3 {
			result.Success = false
			result.Output = fmt.Sprintf("watch event ModRevision expected %d, got %d",
				modRev3, ev.Kv.ModRevision)

			return
		}
		if ev.Kv.CreateRevision != createRev {
			result.Success = false
			result.Output = fmt.Sprintf("watch event CreateRevision expected %d, got %d",
				createRev, ev.Kv.CreateRevision)

			return
		}
	case <-time.After(5 * time.Second):
		result.Success = false
		result.Output = watchEventTimeoutMsg

		return
	}

	// Test 5: Reading at specific revision should return ModRevision as of that point
	ctx, cancel = runner.NewCtx()
	getResp, err = cli.Get(ctx, testKey, clientv3.WithRev(modRev2))
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to get at old revision: %v", err)

		return
	}
	if getResp.Kvs[0].ModRevision != modRev2 {
		result.Success = false
		result.Output = fmt.Sprintf("historical read at rev %d should show ModRevision %d, got %d",
			modRev2, modRev2, getResp.Kvs[0].ModRevision)

		return
	}
	if string(getResp.Kvs[0].Value) != valueV2 {
		result.Success = false
		result.Output = fmt.Sprintf("historical read at rev %d should return %s, got %s",
			modRev2, valueV2, string(getResp.Kvs[0].Value))

		return
	}

	// Test 6: After delete and recreate, new CreateRevision and ModRevision
	ctx, cancel = runner.NewCtx()
	_, err = cli.Delete(ctx, testKey)
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to delete: %v", err)

		return
	}

	ctx, cancel = runner.NewCtx()
	putResp4, err := cli.Put(ctx, testKey, "v-recreated")
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to recreate: %v", err)

		return
	}
	newCreateRev := putResp4.Header.Revision

	ctx, cancel = runner.NewCtx()
	getResp, err = cli.Get(ctx, testKey)
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to get recreated: %v", err)

		return
	}
	kv = getResp.Kvs[0]
	if kv.CreateRevision != newCreateRev {
		result.Success = false
		result.Output = fmt.Sprintf("recreated key should have new CreateRevision %d, got %d",
			newCreateRev, kv.CreateRevision)

		return
	}
	if kv.ModRevision != newCreateRev {
		result.Success = false
		result.Output = fmt.Sprintf("recreated key ModRevision should equal CreateRevision %d, got %d",
			newCreateRev, kv.ModRevision)

		return
	}
	if kv.Version != 1 {
		result.Success = false
		result.Output = fmt.Sprintf("recreated key Version should be 1, got %d", kv.Version)

		return
	}
}

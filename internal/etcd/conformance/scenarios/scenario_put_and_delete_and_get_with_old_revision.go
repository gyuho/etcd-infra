package scenarios

import (
	"bytes"
	"fmt"

	testtime "git.tbd/etcd-infra/internal/etcd/testtime"
	clientv3 "go.etcd.io/etcd/client/v3"

	logutil "git.tbd/etcd-infra/pkg/log"
)

// RunPutAndDeleteAndGetWithOldRevision validates deletes and reads against historical revisions.
//
//nolint:gocyclo // Scenario executes multiple revision paths for coverage.
func RunPutAndDeleteAndGetWithOldRevision(runner Runner) {
	logutil.S().Infow("running", "scenario", PutAndDeleteAndGetWithOldRevision.String())

	result := &Result{
		Scenario:  PutAndDeleteAndGetWithOldRevision.String(),
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
	putRev1 := presp.Header.Revision

	ctx, cancel = runner.NewCtx()
	presp, err = cli.Put(ctx, testKey, "bar2")
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to put: %v", err)

		return
	}
	putRev2 := presp.Header.Revision

	// Only verify that putRev2 > putRev1.
	// In a running cluster, other operations may increment the revision between PUTs,
	// so we cannot assume exactly +1 increment.
	if putRev2 <= putRev1 {
		result.Success = false
		result.Output = fmt.Sprintf("expected putRev2 %d > putRev1 %d", putRev2, putRev1)

		return
	}

	ctx, cancel = runner.NewCtx()
	gresp, err := cli.Get(ctx, testKey, clientv3.WithRev(putRev1))
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to get at putRev1: %v", err)

		return
	}
	if len(gresp.Kvs) != 1 {
		result.Success = false
		result.Output = fmt.Sprintf("expected 1 key, but got %d", len(gresp.Kvs))

		return
	}
	if gresp.Kvs[0].ModRevision != putRev1 {
		result.Success = false
		result.Output = fmt.Sprintf("expected modRevision %d == putRev1 %d", gresp.Kvs[0].ModRevision, putRev1)

		return
	}

	ctx, cancel = runner.NewCtx()
	gresp, err = cli.Get(ctx, testKey, clientv3.WithRev(putRev2))
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to get at putRev2: %v", err)

		return
	}
	if len(gresp.Kvs) != 1 {
		result.Success = false
		result.Output = fmt.Sprintf("expected 1 key, but got %d", len(gresp.Kvs))

		return
	}
	if gresp.Kvs[0].ModRevision != putRev2 {
		result.Success = false
		result.Output = fmt.Sprintf("expected modRevision %d == putRev2 %d", gresp.Kvs[0].ModRevision, putRev2)

		return
	}

	ctx, cancel = runner.NewCtx()
	dresp, err := cli.Delete(ctx, testKey, clientv3.WithPrevKV())
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to delete: %v", err)

		return
	}
	if dresp.Deleted != 1 {
		result.Success = false
		result.Output = fmt.Sprintf("expected deleted 1 key, but got %d", dresp.Deleted)

		return
	}
	if len(dresp.PrevKvs) != 1 {
		result.Success = false
		result.Output = fmt.Sprintf("expected 1 prevKvs, but got %d", len(dresp.PrevKvs))

		return
	}
	if !bytes.Equal(dresp.PrevKvs[0].Key, []byte(testKey)) {
		result.Success = false
		result.Output = fmt.Sprintf("expected key %s, but got %s", testKey, string(dresp.PrevKvs[0].Key))

		return
	}
	if !bytes.Equal(dresp.PrevKvs[0].Value, []byte("bar2")) {
		result.Success = false
		result.Output = fmt.Sprintf("expected value bar2, but got %s", dresp.PrevKvs[0].Value)

		return
	}
	// Only verify that the delete revision is greater than putRev2.
	// In a running cluster, other operations may increment the revision between operations.
	if dresp.Header.Revision <= putRev2 {
		result.Success = false
		result.Output = fmt.Sprintf("expected delete revision %d > putRev2 %d", dresp.Header.Revision, putRev2)

		return
	}

	ctx, cancel = runner.NewCtx()
	gresp, err = cli.Get(ctx, testKey, clientv3.WithRev(putRev1))
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to get at putRev1 after delete: %v", err)

		return
	}
	if len(gresp.Kvs) != 1 {
		result.Success = false
		result.Output = fmt.Sprintf("expected 1 key, but got %d", len(gresp.Kvs))

		return
	}
	if gresp.Kvs[0].ModRevision != putRev1 {
		result.Success = false
		result.Output = fmt.Sprintf("expected modRevision %d == putRev1 %d", gresp.Kvs[0].ModRevision, putRev1)

		return
	}

	ctx, cancel = runner.NewCtx()
	gresp, err = cli.Get(ctx, testKey)
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to get latest: %v", err)

		return
	}
	if len(gresp.Kvs) != 0 {
		result.Success = false
		result.Output = fmt.Sprintf("expected 0 key, but got %d", len(gresp.Kvs))

		return
	}
}

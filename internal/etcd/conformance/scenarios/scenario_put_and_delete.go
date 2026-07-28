package scenarios

import (
	"fmt"

	testtime "git.tbd/etcd-infra/internal/etcd/testtime"
	clientv3 "go.etcd.io/etcd/client/v3"

	logutil "git.tbd/etcd-infra/pkg/log"
)

// RunPutAndDelete tests the PutAndDelete scenario.
func RunPutAndDelete(runner Runner) {
	logutil.S().Infow("running", "scenario", PutAndDelete.String())

	result := &Result{
		Scenario:  PutAndDelete.String(),
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
	presp, err := cli.Put(ctx, testKey, "bar")
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to put: %v", err)

		return
	}
	putRev := presp.Header.GetRevision()

	ctx, cancel = runner.NewCtx()
	gresp, err := cli.Get(ctx, testKey, clientv3.WithRev(putRev))
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to get: %v", err)

		return
	}
	getRev := gresp.Header.GetRevision()

	// The header revision should be at least putRev.
	// On shared clusters or cloud environments with concurrent operations,
	// the header revision may be higher than putRev due to other writes.
	// This is expected behavior - we just verify we're not getting a stale response.
	if getRev < putRev {
		result.Success = false
		result.Output = fmt.Sprintf("got stale revision: expected >= %d, got %d", putRev, getRev)

		return
	}

	ctx, cancel = runner.NewCtx()
	dresp, err := cli.Delete(ctx, testKey)
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to delete: %v", err)

		return
	}
	if dresp.Deleted != 1 {
		result.Success = false
		result.Output = fmt.Sprintf("expected deleted 1, got %d", dresp.Deleted)

		return
	}
	delRev := dresp.Header.GetRevision()

	// In a live Kubernetes cluster, other writes may occur between our put and delete,
	// so we only verify that deletion happened after the put (delRev > putRev).
	if delRev <= putRev {
		result.Success = false
		result.Output = fmt.Sprintf("expected delRev > putRev, got delRev=%d putRev=%d", delRev, putRev)

		return
	}

	// Get at the put revision to verify the key was still there at that point
	ctx, cancel = runner.NewCtx()
	gresp, err = cli.Get(ctx, testKey, clientv3.WithRev(putRev))
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to get at putRev: %v", err)

		return
	}
	// The response revision reflects the server's current revision, not the requested revision,
	// so we don't compare it to delRev (other writes may have occurred).
	if len(gresp.Kvs) != 1 {
		result.Success = false
		result.Output = fmt.Sprintf("expected 1 key at putRev, got %d", len(gresp.Kvs))

		return
	}

	// Verify the key is gone after deletion
	ctx, cancel = runner.NewCtx()
	gresp, err = cli.Get(ctx, testKey)
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to get after delete: %v", err)

		return
	}
	if len(gresp.Kvs) > 0 {
		result.Success = false
		result.Output = "expected empty response after delete, but got non-empty response"

		return
	}
	// The response revision may be higher than delRev due to other writes,
	// which is expected in a shared etcd cluster (e.g., with Kubernetes).
	if gresp.Header.GetRevision() < delRev {
		result.Success = false
		result.Output = fmt.Sprintf("expected getRev >= delRev, got getRev=%d delRev=%d", gresp.Header.GetRevision(), delRev)

		return
	}
}

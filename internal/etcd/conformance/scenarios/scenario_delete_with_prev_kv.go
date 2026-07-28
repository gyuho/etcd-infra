package scenarios

import (
	"fmt"

	testtime "git.tbd/etcd-infra/internal/etcd/testtime"
	clientv3 "go.etcd.io/etcd/client/v3"

	logutil "git.tbd/etcd-infra/pkg/log"
)

// RunDeleteWithPrevKv verifies delete operations return previous key-value when WithPrevKV is specified.
// This is important for Kubernetes to verify the deleted object matches expectations.
// ref. "clientv3/integration/TestDeleteWithPrevKV".
func RunDeleteWithPrevKv(runner Runner) {
	logutil.S().Infow("running", "scenario", DeleteWithPrevKv.String())

	result := &Result{
		Scenario:  DeleteWithPrevKv.String(),
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

	// Create initial value
	ctx, cancel := runner.NewCtx()
	putResp, err := cli.Put(ctx, testKey, "original-value")
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to put: %v", err)

		return
	}
	createRev := putResp.Header.Revision

	// Update value
	ctx, cancel = runner.NewCtx()
	putResp, err = cli.Put(ctx, testKey, "updated-value")
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to update: %v", err)

		return
	}
	modRev := putResp.Header.Revision

	// Delete with WithPrevKV
	ctx, cancel = runner.NewCtx()
	delResp, err := cli.Delete(ctx, testKey, clientv3.WithPrevKV())
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to delete: %v", err)

		return
	}

	if delResp.Deleted != 1 {
		result.Success = false
		result.Output = fmt.Sprintf("expected 1 deleted, got %d", delResp.Deleted)

		return
	}

	if len(delResp.PrevKvs) != 1 {
		result.Success = false
		result.Output = fmt.Sprintf("expected 1 prev kv, got %d", len(delResp.PrevKvs))

		return
	}

	prevKv := delResp.PrevKvs[0]
	if string(prevKv.Key) != testKey {
		result.Success = false
		result.Output = fmt.Sprintf("expected prev key %s, got %s", testKey, string(prevKv.Key))

		return
	}
	if string(prevKv.Value) != "updated-value" {
		result.Success = false
		result.Output = "expected prev value 'updated-value', got " + string(prevKv.Value)

		return
	}
	if prevKv.CreateRevision != createRev {
		result.Success = false
		result.Output = fmt.Sprintf("expected prev CreateRevision %d, got %d", createRev, prevKv.CreateRevision)

		return
	}
	if prevKv.ModRevision != modRev {
		result.Success = false
		result.Output = fmt.Sprintf("expected prev ModRevision %d, got %d", modRev, prevKv.ModRevision)

		return
	}
	if prevKv.Version != 2 {
		result.Success = false
		result.Output = fmt.Sprintf("expected prev Version 2, got %d", prevKv.Version)

		return
	}

	// Test 2: Delete non-existent key should not return PrevKvs
	ctx, cancel = runner.NewCtx()
	delResp, err = cli.Delete(ctx, testKey, clientv3.WithPrevKV())
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to delete non-existent: %v", err)

		return
	}
	if delResp.Deleted != 0 {
		result.Success = false
		result.Output = fmt.Sprintf("expected 0 deleted for non-existent key, got %d", delResp.Deleted)

		return
	}
	if len(delResp.PrevKvs) != 0 {
		result.Success = false
		result.Output = fmt.Sprintf("expected 0 prev kvs for non-existent key, got %d", len(delResp.PrevKvs))

		return
	}

	// Test 3: Prefix delete with WithPrevKV
	prefix := runner.GenerateRandomKey(10)
	keys := []string{prefix + "/a", prefix + "/b", prefix + "/c"}
	for i, k := range keys {
		ctx, cancel = runner.NewCtx()
		_, err = cli.Put(ctx, k, fmt.Sprintf("value-%d", i))
		cancel()
		if err != nil {
			result.Success = false
			result.Output = fmt.Sprintf("failed to put %s: %v", k, err)

			return
		}
	}

	ctx, cancel = runner.NewCtx()
	delResp, err = cli.Delete(ctx, prefix, clientv3.WithPrefix(), clientv3.WithPrevKV())
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to prefix delete: %v", err)

		return
	}

	if delResp.Deleted != int64(len(keys)) {
		result.Success = false
		result.Output = fmt.Sprintf("expected %d deleted, got %d", len(keys), delResp.Deleted)

		return
	}
	if len(delResp.PrevKvs) != len(keys) {
		result.Success = false
		result.Output = fmt.Sprintf("expected %d prev kvs, got %d", len(keys), len(delResp.PrevKvs))

		return
	}

	// Verify all previous values are returned
	prevKeySet := make(map[string]string)
	for _, kv := range delResp.PrevKvs {
		prevKeySet[string(kv.Key)] = string(kv.Value)
	}
	for i, k := range keys {
		expectedValue := fmt.Sprintf("value-%d", i)
		if v, ok := prevKeySet[k]; !ok {
			result.Success = false
			result.Output = "missing prev kv for " + k

			return
		} else if v != expectedValue {
			result.Success = false
			result.Output = fmt.Sprintf("expected value %s for %s, got %s", expectedValue, k, v)

			return
		}
	}
}

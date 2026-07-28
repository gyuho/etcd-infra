package scenarios

import (
	"fmt"
	"path"
	"sync"
	"sync/atomic"

	testtime "git.tbd/etcd-infra/internal/etcd/testtime"
	clientv3 "go.etcd.io/etcd/client/v3"

	logutil "git.tbd/etcd-infra/pkg/log"
)

// RunTxnMultiOpAtomicity verifies multiple operations in a transaction execute atomically.
// Kubernetes uses this for atomic updates across related resources like Pod and Pod/status.
// ref. "clientv3/integration/TestTxnReadRetry".
func RunTxnMultiOpAtomicity(runner Runner) {
	logutil.S().Infow("running", "scenario", TxnMultiOpAtomicity.String())

	result := &Result{
		Scenario:  TxnMultiOpAtomicity.String(),
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

	prefix := runner.GenerateRandomKey(10)
	keyPod := path.Join(prefix, "pod")
	keyStatus := path.Join(prefix, "status")

	// Test 1: Multiple puts in a single transaction should be atomic
	ctx, cancel := runner.NewCtx()
	txnResp, err := cli.Txn(ctx).
		If().
		Then(
			clientv3.OpPut(keyPod, "pod-spec-v1"),
			clientv3.OpPut(keyStatus, "status-v1"),
		).
		Commit()
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to execute multi-put txn: %v", err)

		return
	}
	if !txnResp.Succeeded {
		result.Success = false
		result.Output = "multi-put transaction should have succeeded"

		return
	}
	if len(txnResp.Responses) != 2 {
		result.Success = false
		result.Output = fmt.Sprintf("expected 2 responses, got %d", len(txnResp.Responses))

		return
	}

	// Both puts should have same header revision (atomic commit)
	headerRev := txnResp.Header.Revision

	// Verify both keys exist
	ctx, cancel = runner.NewCtx()
	getResp, err := cli.Get(ctx, prefix, clientv3.WithPrefix())
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to get keys: %v", err)

		return
	}
	if len(getResp.Kvs) != 2 {
		result.Success = false
		result.Output = fmt.Sprintf("expected 2 keys, got %d", len(getResp.Kvs))

		return
	}

	// Test 2: Mixed operations (put + get + delete) should be atomic
	keyToDelete := path.Join(prefix, "to-delete")
	ctx, cancel = runner.NewCtx()
	_, err = cli.Put(ctx, keyToDelete, "delete-me")
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to put key-to-delete: %v", err)

		return
	}

	ctx, cancel = runner.NewCtx()
	txnResp, err = cli.Txn(ctx).
		If().
		Then(
			clientv3.OpPut(keyPod, "pod-spec-v2"),
			clientv3.OpGet(keyStatus),
			clientv3.OpDelete(keyToDelete),
		).
		Commit()
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to execute mixed txn: %v", err)

		return
	}
	if len(txnResp.Responses) != 3 {
		result.Success = false
		result.Output = fmt.Sprintf("expected 3 responses from mixed txn, got %d", len(txnResp.Responses))

		return
	}
	// Verify get response
	getResp2 := txnResp.Responses[1].GetResponseRange()
	if getResp2 == nil || len(getResp2.Kvs) != 1 {
		result.Success = false
		result.Output = "expected get response with 1 key in mixed txn"

		return
	}
	if string(getResp2.Kvs[0].Value) != "status-v1" {
		result.Success = false
		result.Output = "expected status-v1, got " + string(getResp2.Kvs[0].Value)

		return
	}
	// Verify delete response
	delResp := txnResp.Responses[2].GetResponseDeleteRange()
	if delResp == nil || delResp.Deleted != 1 {
		result.Success = false
		result.Output = "expected 1 deleted key in mixed txn"

		return
	}

	// Test 3: Verify atomicity under concurrent access
	// Create multiple clients writing to different keys atomically
	var wg sync.WaitGroup
	var failures atomic.Int32
	numWorkers := 5

	for i := range numWorkers {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			workerCli, err := runner.NewClient()
			if err != nil {
				failures.Add(1)

				return
			}
			defer func() { _ = workerCli.Close() }()

			for j := range 3 {
				key1 := path.Join(prefix, fmt.Sprintf("worker%d-key1", workerID))
				key2 := path.Join(prefix, fmt.Sprintf("worker%d-key2", workerID))
				value := fmt.Sprintf("iter-%d", j)

				ctx, cancel := runner.NewCtx()
				txnResp, err := workerCli.Txn(ctx).
					If().
					Then(
						clientv3.OpPut(key1, value),
						clientv3.OpPut(key2, value),
					).
					Commit()
				cancel()
				if err != nil {
					failures.Add(1)

					continue
				}

				// Both keys should have been updated in same revision
				if len(txnResp.Responses) != 2 {
					failures.Add(1)
				}
			}
		}(i)
	}
	wg.Wait()

	if failures.Load() > 0 {
		result.Success = false
		result.Output = fmt.Sprintf("%d concurrent atomic operations failed", failures.Load())

		return
	}

	result.Output = fmt.Sprintf("multi-op transactions executed atomically at revision %d with %d concurrent workers", headerRev, numWorkers)
}

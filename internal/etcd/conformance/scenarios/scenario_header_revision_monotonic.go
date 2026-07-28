package scenarios

import (
	"fmt"
	"sync"
	"sync/atomic"

	testtime "git.tbd/etcd-infra/internal/etcd/testtime"
	clientv3 "go.etcd.io/etcd/client/v3"

	logutil "git.tbd/etcd-infra/pkg/log"
)

// RunHeaderRevisionMonotonic verifies that Header.Revision never regresses across operations.
// Kubernetes relies on monotonically increasing revisions for resourceVersion ordering guarantees.
func RunHeaderRevisionMonotonic(runner Runner) {
	logutil.S().Infow("running", "scenario", HeaderRevisionMonotonic.String())

	result := &Result{
		Scenario:  HeaderRevisionMonotonic.String(),
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
	var lastRevision int64

	// Test 1: Sequential operations should have monotonically increasing revisions
	operations := 20
	for i := range operations {
		ctx, cancel := runner.NewCtx()
		putResp, putErr := cli.Put(ctx, testKey, fmt.Sprintf("value-%d", i))
		cancel()
		if putErr != nil {
			result.Success = false
			result.Output = fmt.Sprintf("failed to put at iteration %d: %v", i, putErr)

			return
		}

		currentRev := putResp.Header.Revision
		if currentRev <= lastRevision {
			result.Success = false
			result.Output = fmt.Sprintf("revision regressed: iteration %d has %d <= previous %d",
				i, currentRev, lastRevision)

			return
		}
		lastRevision = currentRev
	}

	// Test 2: Get operations should return current revision
	ctx, cancel := runner.NewCtx()
	getResp, err := cli.Get(ctx, testKey)
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to get: %v", err)

		return
	}
	if getResp.Header.Revision < lastRevision {
		result.Success = false
		result.Output = fmt.Sprintf("get revision %d < last put revision %d",
			getResp.Header.Revision, lastRevision)

		return
	}

	// Test 3: Delete should increment revision
	ctx, cancel = runner.NewCtx()
	delResp, err := cli.Delete(ctx, testKey)
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to delete: %v", err)

		return
	}
	if delResp.Header.Revision <= lastRevision {
		result.Success = false
		result.Output = fmt.Sprintf("delete revision %d <= last put revision %d",
			delResp.Header.Revision, lastRevision)

		return
	}

	// Test 4: Concurrent operations should still have monotonic cluster revision
	prefix := runner.GenerateRandomKey(10)
	var wg sync.WaitGroup
	var maxRev atomic.Int64
	errCh := make(chan string, 10)
	numConcurrent := 10

	for i := range numConcurrent {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			key := fmt.Sprintf("%s/key%d", prefix, idx)
			putCtx, putCancel := runner.NewCtx()
			resp, putErr := cli.Put(putCtx, key, fmt.Sprintf("concurrent-%d", idx))
			putCancel()
			if putErr != nil {
				errCh <- fmt.Sprintf("concurrent put %d failed: %v", idx, putErr)

				return
			}
			// Track max revision seen
			for {
				old := maxRev.Load()
				if resp.Header.Revision <= old {
					break
				}
				if maxRev.CompareAndSwap(old, resp.Header.Revision) {
					break
				}
			}
		}(i)
	}
	wg.Wait()
	close(errCh)

	for errMsg := range errCh {
		result.Success = false
		result.Output = errMsg

		return
	}

	// Test 5: Verify list returns consistent revision
	ctx, cancel = runner.NewCtx()
	listResp, err := cli.Get(ctx, prefix, clientv3.WithPrefix())
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to list: %v", err)

		return
	}
	if listResp.Header.Revision < maxRev.Load() {
		result.Success = false
		result.Output = fmt.Sprintf("list revision %d < max concurrent revision %d",
			listResp.Header.Revision, maxRev.Load())

		return
	}

	// Verify we got all keys
	if len(listResp.Kvs) != numConcurrent {
		result.Success = false
		result.Output = fmt.Sprintf("expected %d keys, got %d", numConcurrent, len(listResp.Kvs))

		return
	}

	// Test 6: Txn response should have consistent revision
	txnKey := runner.GenerateRandomKey(10)
	ctx, cancel = runner.NewCtx()
	_, err = cli.Put(ctx, txnKey, "initial")
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to put txn key: %v", err)

		return
	}

	ctx, cancel = runner.NewCtx()
	txnResp, err := cli.Txn(ctx).
		If(clientv3.Compare(clientv3.Version(txnKey), ">", 0)).
		Then(clientv3.OpPut(txnKey, "updated"), clientv3.OpGet(txnKey)).
		Commit()
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed txn: %v", err)

		return
	}
	if !txnResp.Succeeded {
		result.Success = false
		result.Output = "txn should have succeeded"

		return
	}

	// All responses in txn should reference the same header revision
	if txnResp.Header.Revision <= listResp.Header.Revision {
		result.Success = false
		result.Output = fmt.Sprintf("txn revision %d <= list revision %d (expected increase)",
			txnResp.Header.Revision, listResp.Header.Revision)

		return
	}
}

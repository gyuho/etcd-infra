package scenarios

import (
	"fmt"
	"path"
	"time"

	testtime "git.tbd/etcd-infra/internal/etcd/testtime"
	"go.etcd.io/etcd/api/v3/mvccpb"
	clientv3 "go.etcd.io/etcd/client/v3"

	logutil "git.tbd/etcd-infra/pkg/log"
)

// RunWatchOrderingGuarantee verifies watch events are delivered in strict modification order.
// Kubernetes relies on ordered event delivery for consistent cache updates in controllers.
// ref. "clientv3/integration/TestWatchEventOrdering".
func RunWatchOrderingGuarantee(runner Runner) {
	logutil.S().Infow("running", "scenario", WatchOrderingGuarantee.String())

	result := &Result{
		Scenario:  WatchOrderingGuarantee.String(),
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

	// Start watch before any modifications
	wctx, wcancel := runner.NewCtxTimeout(15 * time.Second)
	defer wcancel()

	wch := cli.Watch(wctx, prefix,
		clientv3.WithPrefix(),
		clientv3.WithCreatedNotify(),
	)
	if wch == nil {
		result.Success = false
		result.Output = watchCreateFailedMsg

		return
	}

	// Wait for watch creation
	select {
	case wr := <-wch:
		if !wr.Created {
			result.Success = false
			result.Output = "expected Created notification"

			return
		}
	case <-time.After(5 * time.Second):
		result.Success = false
		result.Output = "timeout waiting for watch creation"

		return
	}

	// Perform sequential modifications on multiple keys
	numOps := 10
	expectedRevisions := make([]int64, 0, numOps)
	for i := range numOps {
		key := path.Join(prefix, fmt.Sprintf("key%d", i%3)) // Rotate among 3 keys
		ctx, cancel := runner.NewCtx()
		putResp, err := cli.Put(ctx, key, fmt.Sprintf("value-%d", i))
		cancel()
		if err != nil {
			result.Success = false
			result.Output = fmt.Sprintf("failed to put at iteration %d: %v", i, err)

			return
		}
		expectedRevisions = append(expectedRevisions, putResp.Header.Revision)
	}

	// Collect watch events
	receivedRevisions := make([]int64, 0, numOps)
	timeout := time.After(5 * time.Second)

collectLoop:
	for len(receivedRevisions) < numOps {
		select {
		case wr := <-wch:
			if wr.Err() != nil {
				result.Success = false
				result.Output = fmt.Sprintf("watch error: %v", wr.Err())

				return
			}
			for _, ev := range wr.Events {
				if ev.Type == mvccpb.PUT {
					receivedRevisions = append(receivedRevisions, ev.Kv.ModRevision)
				}
			}
		case <-timeout:
			result.Success = false
			result.Output = fmt.Sprintf("timeout: received %d/%d events", len(receivedRevisions), numOps)

			return
		}
		if len(receivedRevisions) >= numOps {
			break collectLoop
		}
	}

	// Verify ordering: events must arrive in strictly increasing revision order
	var lastRev int64
	for i, rev := range receivedRevisions {
		if rev <= lastRev {
			result.Success = false
			result.Output = fmt.Sprintf("ordering violation at index %d: rev %d <= previous %d", i, rev, lastRev)

			return
		}
		lastRev = rev
	}

	// Verify we received the exact revisions we created
	if len(receivedRevisions) != len(expectedRevisions) {
		result.Success = false
		result.Output = fmt.Sprintf("expected %d events, got %d", len(expectedRevisions), len(receivedRevisions))

		return
	}

	for i := range receivedRevisions {
		if receivedRevisions[i] != expectedRevisions[i] {
			result.Success = false
			result.Output = fmt.Sprintf("revision mismatch at index %d: expected %d, got %d",
				i, expectedRevisions[i], receivedRevisions[i])

			return
		}
	}

	// Test 2: Verify ordering with deletes mixed in
	prefix2 := runner.GenerateRandomKey(10)
	key1 := path.Join(prefix2, "key1")
	key2 := path.Join(prefix2, "key2")

	wctx2, wcancel2 := runner.NewCtxTimeout(10 * time.Second)
	defer wcancel2()

	wch2 := cli.Watch(wctx2, prefix2, clientv3.WithPrefix(), clientv3.WithCreatedNotify())
	select {
	case wr := <-wch2:
		if !wr.Created {
			result.Success = false
			result.Output = "expected Created notification for second watch"

			return
		}
	case <-time.After(5 * time.Second):
		result.Success = false
		result.Output = "timeout waiting for second watch creation"

		return
	}

	// Interleaved puts and deletes
	operations := []struct {
		op  string
		key string
	}{
		{"put", key1},
		{"put", key2},
		{"delete", key1},
		{"put", key1},
		{"delete", key2},
	}

	mixedRevisions := make([]int64, 0, len(operations))
	for _, operation := range operations {
		var rev int64
		ctx, cancel := runner.NewCtx()
		if operation.op == "put" {
			putResp, err := cli.Put(ctx, operation.key, "value")
			if err != nil {
				cancel()
				result.Success = false
				result.Output = fmt.Sprintf("failed to put %s: %v", operation.key, err)

				return
			}
			rev = putResp.Header.Revision
		} else {
			delResp, err := cli.Delete(ctx, operation.key)
			if err != nil {
				cancel()
				result.Success = false
				result.Output = fmt.Sprintf("failed to delete %s: %v", operation.key, err)

				return
			}
			rev = delResp.Header.Revision
		}
		cancel()
		mixedRevisions = append(mixedRevisions, rev) //nolint:staticcheck // result assignment needed for slice growth
	}

	// Collect mixed operation events
	receivedMixed := make([]int64, 0, len(operations))
	timeout = time.After(5 * time.Second)
	for len(receivedMixed) < len(operations) {
		select {
		case wr := <-wch2:
			if wr.Err() != nil {
				result.Success = false
				result.Output = fmt.Sprintf("watch error on mixed ops: %v", wr.Err())

				return
			}
			for _, ev := range wr.Events {
				receivedMixed = append(receivedMixed, ev.Kv.ModRevision)
			}
		case <-timeout:
			result.Success = false
			result.Output = fmt.Sprintf("timeout on mixed ops: received %d/%d events", len(receivedMixed), len(operations))

			return
		}
	}

	// Verify mixed operation ordering
	lastRev = 0
	for i, rev := range receivedMixed {
		if rev <= lastRev {
			result.Success = false
			result.Output = fmt.Sprintf("mixed ops ordering violation at index %d: rev %d <= previous %d", i, rev, lastRev)

			return
		}
		lastRev = rev
	}

	result.Output = fmt.Sprintf("watch events delivered in strict order across %d pure ops and %d mixed ops", numOps, len(operations))
}

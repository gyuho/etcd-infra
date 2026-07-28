package scenarios

import (
	"errors"
	"fmt"

	testtime "git.tbd/etcd-infra/internal/etcd/testtime"
	"go.etcd.io/etcd/api/v3/v3rpc/rpctypes"
	clientv3 "go.etcd.io/etcd/client/v3"

	logutil "git.tbd/etcd-infra/pkg/log"
)

// RunCompactRevisionRetention verifies compaction preserves data at and after the compaction revision.
// Kubernetes expects data at and after the compaction revision to remain accessible for watch resumption.
// ref. "clientv3/integration/TestKVCompact".
func RunCompactRevisionRetention(runner Runner) {
	logutil.S().Infow("running", "scenario", CompactRevisionRetention.String())

	result := &Result{
		Scenario:  CompactRevisionRetention.String(),
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

	// Create multiple revisions
	revisions := make([]int64, 0, 5)
	for i := range 5 {
		ctx, cancel := runner.NewCtx()
		putResp, putErr := cli.Put(ctx, testKey, fmt.Sprintf("value-%d", i))
		cancel()
		if putErr != nil {
			result.Success = false
			result.Output = fmt.Sprintf("failed to put value-%d: %v", i, putErr)

			return
		}
		revisions = append(revisions, putResp.Header.Revision)
	}

	// Compact at revision 3 (middle)
	compactRev := revisions[2]
	ctx, cancel := runner.NewCtx()
	_, err = cli.Compact(ctx, compactRev)
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to compact at rev %d: %v", compactRev, err)

		return
	}

	// Test 1: Read at revision before compact should fail with ErrCompacted
	ctx, cancel = runner.NewCtx()
	_, err = cli.Get(ctx, testKey, clientv3.WithRev(revisions[0]))
	cancel()
	if !errors.Is(err, rpctypes.ErrCompacted) {
		result.Success = false
		result.Output = fmt.Sprintf("expected ErrCompacted for rev %d, got: %v", revisions[0], err)

		return
	}

	// Test 2: Read at revision before compact (but after first) should also fail
	ctx, cancel = runner.NewCtx()
	_, err = cli.Get(ctx, testKey, clientv3.WithRev(revisions[1]))
	cancel()
	if !errors.Is(err, rpctypes.ErrCompacted) {
		result.Success = false
		result.Output = fmt.Sprintf("expected ErrCompacted for rev %d, got: %v", revisions[1], err)

		return
	}

	// Test 3: Read at compact revision should succeed
	ctx, cancel = runner.NewCtx()
	getResp, err := cli.Get(ctx, testKey, clientv3.WithRev(compactRev))
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to get at compact rev %d: %v", compactRev, err)

		return
	}
	if len(getResp.Kvs) != 1 {
		result.Success = false
		result.Output = fmt.Sprintf("expected 1 key at compact rev, got %d", len(getResp.Kvs))

		return
	}
	if string(getResp.Kvs[0].Value) != "value-2" {
		result.Success = false
		result.Output = "expected value-2 at compact rev, got " + string(getResp.Kvs[0].Value)

		return
	}

	// Test 4: Read at revision after compact should succeed
	for i := 3; i < len(revisions); i++ {
		ctx, cancel = runner.NewCtx()
		getResp, err = cli.Get(ctx, testKey, clientv3.WithRev(revisions[i]))
		cancel()
		if err != nil {
			result.Success = false
			result.Output = fmt.Sprintf("failed to get at rev %d (post-compact): %v", revisions[i], err)

			return
		}
		if len(getResp.Kvs) != 1 {
			result.Success = false
			result.Output = fmt.Sprintf("expected 1 key at rev %d, got %d", revisions[i], len(getResp.Kvs))

			return
		}
		expectedValue := fmt.Sprintf("value-%d", i)
		if string(getResp.Kvs[0].Value) != expectedValue {
			result.Success = false
			result.Output = fmt.Sprintf("expected %s at rev %d, got %s", expectedValue, revisions[i], string(getResp.Kvs[0].Value))

			return
		}
	}

	// Test 5: Latest read should succeed
	ctx, cancel = runner.NewCtx()
	getResp, err = cli.Get(ctx, testKey)
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to get latest: %v", err)

		return
	}
	if string(getResp.Kvs[0].Value) != "value-4" {
		result.Success = false
		result.Output = "expected latest value-4, got " + string(getResp.Kvs[0].Value)

		return
	}

	result.Output = fmt.Sprintf("compaction at rev %d correctly preserved data at and after that revision", compactRev)
}

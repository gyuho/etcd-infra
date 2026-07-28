package scenarios

import (
	"fmt"
	"path"

	testtime "git.tbd/etcd-infra/internal/etcd/testtime"
	clientv3 "go.etcd.io/etcd/client/v3"

	logutil "git.tbd/etcd-infra/pkg/log"
)

// RunGetWithLimitAndCount validates Range WithLimit/WithCountOnly semantics used by kube-apiserver list.
//
//nolint:gocyclo // Scenario intentionally walks multiple pagination paths.
func RunGetWithLimitAndCount(runner Runner) {
	logutil.S().Infow("running", "scenario", GetWithLimitAndCount.String())

	result := &Result{
		Scenario:  GetWithLimitAndCount.String(),
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
	keys := []string{
		path.Join(prefix, "000"),
		path.Join(prefix, "001"),
		path.Join(prefix, "002"),
		path.Join(prefix, "003"),
		path.Join(prefix, "004"),
	}

	var latestRevision int64
	for i, key := range keys {
		ctx, cancel := runner.NewCtx()
		presp, putErr := cli.Put(ctx, key, fmt.Sprintf("value-%d", i))
		cancel()
		if putErr != nil {
			result.Success = false
			result.Output = fmt.Sprintf("failed to seed key %q: %v", key, putErr)

			return
		}
		latestRevision = presp.Header.GetRevision()
	}

	// Use revision pinning to ensure consistent reads across multiple queries.
	// In a shared etcd cluster (e.g., with Kubernetes), other writes may increase the revision.
	ctx, cancel := runner.NewCtx()
	limitedResp, err := cli.Get(
		ctx,
		prefix,
		clientv3.WithPrefix(),
		clientv3.WithLimit(3),
		clientv3.WithKeysOnly(),
		clientv3.WithSort(clientv3.SortByKey, clientv3.SortAscend),
		clientv3.WithRev(latestRevision), // Pin to our known revision for consistency
	)
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to run limited range: %v", err)

		return
	}
	// Response revision may be >= latestRevision due to concurrent writes, which is acceptable.
	if limitedResp.Header.GetRevision() < latestRevision {
		result.Success = false
		result.Output = fmt.Sprintf("unexpected revision from limited range, want >= %d got %d", latestRevision, limitedResp.Header.GetRevision())

		return
	}
	if len(limitedResp.Kvs) != 3 {
		result.Success = false
		result.Output = fmt.Sprintf("limited range returned %d keys, want 3", len(limitedResp.Kvs))

		return
	}
	for idx, kv := range limitedResp.Kvs {
		expectedKey := keys[idx]
		if string(kv.Key) != expectedKey {
			result.Success = false
			result.Output = fmt.Sprintf("limited range key mismatch at index %d: want %q got %q", idx, expectedKey, string(kv.Key))

			return
		}
		if len(kv.Value) != 0 {
			result.Success = false
			result.Output = fmt.Sprintf("expected keys-only response for %q", string(kv.Key))

			return
		}
	}
	if limitedResp.Count != int64(len(keys)) {
		result.Success = false
		result.Output = fmt.Sprintf("limited range total count mismatch: want %d got %d", len(keys), limitedResp.Count)

		return
	}
	if !limitedResp.More {
		result.Success = false
		result.Output = "limited range missing More flag for additional keys"

		return
	}

	nextStart := append([]byte(nil), limitedResp.Kvs[len(limitedResp.Kvs)-1].Key...)
	nextStart = append(nextStart, 0x00)
	ctx, cancel = runner.NewCtx()
	rangeResp, err := cli.Get(
		ctx,
		string(nextStart),
		clientv3.WithRange(clientv3.GetPrefixRangeEnd(prefix)),
		clientv3.WithLimit(3),
		clientv3.WithKeysOnly(),
		clientv3.WithSort(clientv3.SortByKey, clientv3.SortAscend),
		clientv3.WithRev(latestRevision), // Pin to our known revision for consistency
	)
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to run follow-up range: %v", err)

		return
	}
	// Response revision may be >= latestRevision due to concurrent writes.
	if rangeResp.Header.GetRevision() < latestRevision {
		result.Success = false
		result.Output = fmt.Sprintf("unexpected revision from follow-up range, want >= %d got %d", latestRevision, rangeResp.Header.GetRevision())

		return
	}

	expectedRemaining := keys[len(limitedResp.Kvs):]
	if len(rangeResp.Kvs) != len(expectedRemaining) {
		result.Success = false
		result.Output = fmt.Sprintf("follow-up range returned %d keys, want %d", len(rangeResp.Kvs), len(expectedRemaining))

		return
	}
	for idx, kv := range rangeResp.Kvs {
		expectedKey := expectedRemaining[idx]
		if string(kv.Key) != expectedKey {
			result.Success = false
			result.Output = fmt.Sprintf("follow-up range key mismatch at index %d: want %q got %q", idx, expectedKey, string(kv.Key))

			return
		}
		if len(kv.Value) != 0 {
			result.Success = false
			result.Output = fmt.Sprintf("expected keys-only response for %q in follow-up range", string(kv.Key))

			return
		}
	}
	if rangeResp.Count != int64(len(rangeResp.Kvs)) {
		result.Success = false
		result.Output = fmt.Sprintf("follow-up range count mismatch: want %d got %d", len(rangeResp.Kvs), rangeResp.Count)

		return
	}
	if rangeResp.More {
		result.Success = false
		result.Output = "follow-up range unexpectedly reported More=true"

		return
	}

	ctx, cancel = runner.NewCtx()
	countResp, err := cli.Get(ctx, prefix, clientv3.WithPrefix(), clientv3.WithCountOnly(), clientv3.WithRev(latestRevision))
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to run count-only range: %v", err)

		return
	}
	// Response revision may be >= latestRevision due to concurrent writes.
	if countResp.Header.GetRevision() < latestRevision {
		result.Success = false
		result.Output = fmt.Sprintf("unexpected revision from count-only range, want >= %d got %d", latestRevision, countResp.Header.GetRevision())

		return
	}
	if countResp.Count != int64(len(keys)) {
		result.Success = false
		result.Output = fmt.Sprintf("count-only range mismatch: want %d got %d", len(keys), countResp.Count)

		return
	}
	if len(countResp.Kvs) != 0 {
		result.Success = false
		result.Output = fmt.Sprintf("count-only range should not return key data, got %d entries", len(countResp.Kvs))

		return
	}
	if countResp.More {
		result.Success = false
		result.Output = "count-only range unexpectedly reported More=true"

		return
	}

	result.Output = fmt.Sprintf(
		"prefix pagination returned %d+%d keys with consistent revision %d and total count %d",
		len(limitedResp.Kvs),
		len(rangeResp.Kvs),
		latestRevision,
		len(keys),
	)
}

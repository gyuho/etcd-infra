package scenarios

import (
	"bytes"
	"errors"
	"fmt"
	"sort"
	"time"

	testtime "git.tbd/etcd-infra/internal/etcd/testtime"
	"go.etcd.io/etcd/api/v3/mvccpb"
	"go.etcd.io/etcd/api/v3/v3rpc/rpctypes"
	clientv3 "go.etcd.io/etcd/client/v3"
	"go.uber.org/zap"

	logutil "git.tbd/etcd-infra/pkg/log"
)

// RunPutAndGetWithSort tests the PutAndGetWithSort scenario.
// This test verifies that etcd correctly stores and returns keys sorted by key name.
// It is designed to be deterministic even when running on an active Kubernetes cluster
// where concurrent etcd writes may occur between operations.
func RunPutAndGetWithSort(runner Runner) {
	logutil.S().Infow("running", "scenario", PutAndGetWithSort.String())

	result := &Result{
		Scenario:  PutAndGetWithSort.String(),
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

	// Clean up any existing test keys from previous runs.
	// IMPORTANT: Using "\x00" with WithFromKey() would delete ALL keys in etcd,
	// including Kubernetes data under /registry/. Always scope deletions
	// to the test prefix when running against a live cluster.
	ctx, cancel := runner.NewCtx()
	dresp, err := cli.Delete(ctx, testKey, clientv3.WithPrefix())
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to cleanup test keys: %v", err)

		return
	}

	latestRev := dresp.Header.GetRevision()

	compactStart := time.Now()

	ctx, cancel = runner.NewCtx()
	_, err = cli.Compact(
		ctx,
		latestRev,
		clientv3.WithCompactPhysical(),
	)
	cancel()
	if err != nil && !errors.Is(err, rpctypes.ErrCompacted) {
		result.Success = false
		result.Output = fmt.Sprintf("failed to compact: %v", err)

		return
	}
	logutil.S().Info("discarded historical revisions", zap.Duration("took", time.Since(compactStart)))

	// Create test key-value pairs and PUT them to etcd.
	//
	// RACE CONDITION FIX:
	// Previously, we pre-computed expected revisions as `lastRev + i + 1` before
	// issuing PUTs. This caused flaky failures on active Kubernetes clusters because:
	//   1. We'd capture lastRev from a GET
	//   2. Kubernetes would make concurrent writes (e.g., lease renewals, controller updates)
	//   3. Our PUTs would get higher revisions than expected
	//   4. The comparison would fail with revision mismatches
	//
	// FIX: Capture the actual revision from each PUT response. This is deterministic
	// because etcd guarantees each PUT response contains the exact revision assigned
	// to that write operation. Concurrent writes don't affect our captured values.
	// This is deterministic: no matter what other writes happen concurrently,
	// this specific PUT's revision is recorded in the response.
	expectedKVs := make([]*mvccpb.KeyValue, 0, 10)

	for i := range 10 {
		kv := createKV(testKey, fmt.Sprintf("hello%02d", i), "bar")

		putCtx, putCancel := runner.NewCtx()
		presp, putErr := cli.Put(putCtx, kv.k, kv.v)
		putCancel()
		if putErr != nil {
			result.Success = false
			result.Output = fmt.Sprintf("failed to put key %s: %v", kv.k, putErr)

			return
		}

		// Capture the actual revision from the PUT response.
		// presp.Header.Revision is the cluster-wide revision AFTER this PUT completed.
		// For a new key, CreateRevision == ModRevision == this revision.
		// This is deterministic: no matter what other writes happen concurrently,
		// this specific PUT's revision is recorded in the response.
		actualRev := presp.Header.GetRevision()
		expectedKVs = append(expectedKVs, &mvccpb.KeyValue{
			Key:            []byte(kv.k),
			Value:          []byte(kv.v),
			CreateRevision: actualRev, // Revision when key was first created
			ModRevision:    actualRev, // Revision of last modification (same for new keys)
			Version:        1,         // Number of modifications (1 for new keys)
		})
	}

	// Sort expected KVs by key descending to match our GET query's sort order.
	// This test verifies that etcd's SortByKey + SortDescend works correctly.
	sort.Slice(expectedKVs, func(i, j int) bool {
		return string(expectedKVs[i].Key) > string(expectedKVs[j].Key)
	})

	ctx, cancel = runner.NewCtx()
	gresp, err := cli.Get(ctx,
		testKey,
		clientv3.WithPrefix(),
		clientv3.WithSort(clientv3.SortByKey, clientv3.SortDescend),
	)
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to get: %v", err)

		return
	}
	logutil.S().Infow("GET success", "prefix", testKey, "revision", gresp.Header.Revision, "count", len(gresp.Kvs))

	// Verify we got the expected number of keys.
	// This ensures no keys were lost and no extra keys appeared.
	if len(gresp.Kvs) != len(expectedKVs) {
		result.Success = false
		result.Output = fmt.Sprintf("GET response count mismatch: expected %d keys, got %d", len(expectedKVs), len(gresp.Kvs))

		return
	}

	// Compare each key-value pair field-by-field.
	// We verify all mvccpb.KeyValue fields that we set during PUT:
	//   - Key: the full key path
	//   - Value: the stored value
	//   - CreateRevision: when the key was first created
	//   - ModRevision: when the key was last modified
	//   - Version: number of modifications to this key
	for i, expected := range expectedKVs {
		actual := gresp.Kvs[i]

		// Verify key matches (should be in sorted order)
		if !bytes.Equal(expected.Key, actual.Key) {
			result.Success = false
			result.Output = fmt.Sprintf("key mismatch at index %d: expected %q, got %q", i, string(expected.Key), string(actual.Key))

			return
		}

		// Verify value was stored correctly
		if !bytes.Equal(expected.Value, actual.Value) {
			result.Success = false
			result.Output = fmt.Sprintf("value mismatch for key %q: expected %q, got %q", string(expected.Key), string(expected.Value), string(actual.Value))

			return
		}

		// Verify CreateRevision matches what we captured during PUT.
		// This is the core check that was previously racy.
		if expected.CreateRevision != actual.CreateRevision {
			result.Success = false
			result.Output = fmt.Sprintf("CreateRevision mismatch for key %q: expected %d, got %d", string(expected.Key), expected.CreateRevision, actual.CreateRevision)

			return
		}

		// For newly created keys, ModRevision should equal CreateRevision
		if expected.ModRevision != actual.ModRevision {
			result.Success = false
			result.Output = fmt.Sprintf("ModRevision mismatch for key %q: expected %d, got %d", string(expected.Key), expected.ModRevision, actual.ModRevision)

			return
		}

		// Version should be 1 for newly created keys (first write)
		if expected.Version != actual.Version {
			result.Success = false
			result.Output = fmt.Sprintf("Version mismatch for key %q: expected %d, got %d", string(expected.Key), expected.Version, actual.Version)

			return
		}
	}

	// Additional check: verify the GET response is actually sorted in descending order.
	// This validates etcd's SortByKey + SortDescend functionality.
	for i := 1; i < len(gresp.Kvs); i++ {
		prevKey := string(gresp.Kvs[i-1].Key)
		currKey := string(gresp.Kvs[i].Key)
		// In descending order, each key should be "greater than" the next
		if prevKey < currKey {
			result.Success = false
			result.Output = fmt.Sprintf("keys not in descending order: %q should come after %q", prevKey, currKey)

			return
		}
	}
}

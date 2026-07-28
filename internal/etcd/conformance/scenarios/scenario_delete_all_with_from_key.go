package scenarios

import (
	"fmt"
	"path"
	"strconv"
	"time"

	testtime "git.tbd/etcd-infra/internal/etcd/testtime"
	clientv3 "go.etcd.io/etcd/client/v3"

	logutil "git.tbd/etcd-infra/pkg/log"
)

// RunDeleteAllWithFromKey tests the DeleteAllWithFromKey scenario.
func RunDeleteAllWithFromKey(runner Runner) {
	logutil.S().Infow("running", "scenario", DeleteAllWithFromKey.String())

	result := &Result{
		Scenario:  DeleteAllWithFromKey.String(),
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

	keysN := 10
	for i := range keysN {
		k := path.Join(testKey, strconv.Itoa(i))

		ctx, cancel := runner.NewCtx()
		_, err = cli.Put(ctx, k, "bar")
		cancel()
		if err != nil {
			result.Success = false
			result.Output = fmt.Sprintf("failed to put: %v", err)

			return
		}
	}

	// Use extended timeout for WithFromKey() queries which return ALL keys from the given key
	// onwards in lexicographical order. In production etcd with Kubernetes data (/registry/...),
	// this can return thousands of keys. Cross-DC WireGuard networks amplify this latency.
	// Use 3x the default timeout (3 * 90s = 270s = 4.5 minutes) for WithFromKey operations.
	fromKeyTimeout := max(3*runner.DefaultTimeout(),
		// minimum 3 minutes for WithFromKey
		180*time.Second)
	ctx, cancel := runner.NewCtxTimeout(fromKeyTimeout)
	gresp, err := cli.Get(ctx, testKey, clientv3.WithFromKey())
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to get: %v", err)

		return
	}
	if len(gresp.Kvs) < keysN {
		result.Success = false
		result.Output = fmt.Sprintf("expected >= %d keys, got %d", keysN, len(gresp.Kvs))

		return
	}

	// Delete keys from testKey forward within the test range.
	// IMPORTANT: Using "\x00" with WithFromKey() would delete ALL keys in etcd,
	// including Kubernetes data under /registry/. Always scope deletions
	// to the test prefix when running against a live cluster.
	//
	// We use testKey with WithRange(endKey) to delete only the test keys.
	// The end key is testKey with '\xff' appended to cover all keys starting with testKey.
	endKey := testKey + "\xff"
	ctx, cancel = runner.NewCtx()
	dresp, err := cli.Delete(ctx, testKey, clientv3.WithRange(endKey))
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to delete: %v", err)

		return
	}
	if dresp.Deleted < int64(keysN) {
		result.Success = false
		result.Output = fmt.Sprintf("expected %d keys to be deleted, got %d", keysN, dresp.Deleted)

		return
	}

	// Verify all keys under testKey are deleted (not the entire keyspace).
	ctx, cancel = runner.NewCtx()
	gresp, err = cli.Get(ctx, testKey, clientv3.WithPrefix())
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to get: %v", err)

		return
	}
	if len(gresp.Kvs) != 0 {
		result.Success = false
		result.Output = fmt.Sprintf("expected 0 keys, got %d", len(gresp.Kvs))

		return
	}
}

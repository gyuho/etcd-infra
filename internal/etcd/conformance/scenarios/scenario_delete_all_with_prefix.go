package scenarios

import (
	"fmt"
	"path"
	"strconv"

	testtime "git.tbd/etcd-infra/internal/etcd/testtime"
	clientv3 "go.etcd.io/etcd/client/v3"

	logutil "git.tbd/etcd-infra/pkg/log"
)

// RunDeleteAllWithPrefix tests the DeleteAllWithPrefix scenario.
func RunDeleteAllWithPrefix(runner Runner) {
	logutil.S().Infow("running", "scenario", DeleteAllWithPrefix.String())

	result := &Result{
		Scenario:  DeleteAllWithPrefix.String(),
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

	// Use runner's default timeout for WithFromKey() queries which can return large datasets
	// in high-latency cloud/VPN environments (e.g., cross-DC WireGuard networks).
	ctx, cancel := runner.NewCtx()
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

	// Delete keys using testKey prefix only (NOT the entire keyspace).
	// IMPORTANT: Using "" with WithPrefix() would delete ALL keys in etcd,
	// including Kubernetes data under /registry/. Always scope deletions
	// to the test prefix when running against a live cluster.
	ctx, cancel = runner.NewCtx()
	dresp, err := cli.Delete(ctx, testKey, clientv3.WithPrefix())
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

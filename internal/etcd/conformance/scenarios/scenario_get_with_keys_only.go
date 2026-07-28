package scenarios

import (
	"fmt"
	"path"
	"strings"

	testtime "git.tbd/etcd-infra/internal/etcd/testtime"
	clientv3 "go.etcd.io/etcd/client/v3"

	logutil "git.tbd/etcd-infra/pkg/log"
)

// RunGetWithKeysOnly verifies WithKeysOnly option returns keys without values.
// This is useful for efficient enumeration when values are not needed.
// ref. "clientv3/integration/TestKVGetKeysOnly".
func RunGetWithKeysOnly(runner Runner) {
	logutil.S().Infow("running", "scenario", GetWithKeysOnly.String())

	result := &Result{
		Scenario:  GetWithKeysOnly.String(),
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

	// Create multiple keys with large values
	largeValue := strings.Repeat("x", 1024) // 1KB value
	keys := []string{
		path.Join(prefix, "key1"),
		path.Join(prefix, "key2"),
		path.Join(prefix, "key3"),
	}

	for _, key := range keys {
		ctx, cancel := runner.NewCtx()
		_, err = cli.Put(ctx, key, largeValue)
		cancel()
		if err != nil {
			result.Success = false
			result.Output = fmt.Sprintf("failed to put %s: %v", key, err)

			return
		}
	}

	// Test 1: Get with keys only should return empty values
	ctx, cancel := runner.NewCtx()
	keysOnlyResp, err := cli.Get(ctx, prefix, clientv3.WithPrefix(), clientv3.WithKeysOnly())
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to get with keys only: %v", err)

		return
	}

	if len(keysOnlyResp.Kvs) != len(keys) {
		result.Success = false
		result.Output = fmt.Sprintf("expected %d keys, got %d", len(keys), len(keysOnlyResp.Kvs))

		return
	}

	for i, kv := range keysOnlyResp.Kvs {
		if string(kv.Key) != keys[i] {
			result.Success = false
			result.Output = fmt.Sprintf("expected key %s, got %s", keys[i], string(kv.Key))

			return
		}
		if len(kv.Value) != 0 {
			result.Success = false
			result.Output = fmt.Sprintf("expected empty value for keys-only, got %d bytes", len(kv.Value))

			return
		}
		// Metadata should still be present
		if kv.CreateRevision == 0 {
			result.Success = false
			result.Output = "expected CreateRevision to be non-zero even with keys-only"

			return
		}
		if kv.ModRevision == 0 {
			result.Success = false
			result.Output = "expected ModRevision to be non-zero even with keys-only"

			return
		}
	}

	// Test 2: Compare with regular get to show values are normally present
	ctx, cancel = runner.NewCtx()
	regularResp, err := cli.Get(ctx, prefix, clientv3.WithPrefix())
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to get without keys only: %v", err)

		return
	}

	for i, kv := range regularResp.Kvs {
		if len(kv.Value) != 1024 {
			result.Success = false
			result.Output = fmt.Sprintf("expected 1024 byte value for key %d, got %d", i, len(kv.Value))

			return
		}
	}

	// Test 3: Keys-only with single key get
	ctx, cancel = runner.NewCtx()
	singleResp, err := cli.Get(ctx, keys[0], clientv3.WithKeysOnly())
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to get single key with keys only: %v", err)

		return
	}

	if len(singleResp.Kvs) != 1 {
		result.Success = false
		result.Output = fmt.Sprintf("expected 1 key, got %d", len(singleResp.Kvs))

		return
	}
	if len(singleResp.Kvs[0].Value) != 0 {
		result.Success = false
		result.Output = "expected empty value for single key with keys-only"

		return
	}

	result.Output = fmt.Sprintf("keys-only returned %d keys without values, metadata preserved", len(keys))
}

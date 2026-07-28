package scenarios

import (
	"errors"
	"fmt"
	"path"
	"reflect"
	"time"

	testtime "git.tbd/etcd-infra/internal/etcd/testtime"
	"go.etcd.io/etcd/api/v3/mvccpb"
	"go.etcd.io/etcd/api/v3/v3rpc/rpctypes"
	clientv3 "go.etcd.io/etcd/client/v3"
	"go.uber.org/zap"

	logutil "git.tbd/etcd-infra/pkg/log"
)

// RunDeleteWithRange tests the DeleteWithRange scenario.
func RunDeleteWithRange(runner Runner) {
	logutil.S().Infow("running", "scenario", DeleteWithRange.String())

	result := &Result{
		Scenario:  DeleteWithRange.String(),
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

	keysN := 10
	kvs1 := []*mvccpb.KeyValue{}
	for i := range keysN {
		k := path.Join(testKey, fmt.Sprintf("%02d", i))

		putCtx, putCancel := runner.NewCtx()
		presp, putErr := cli.Put(putCtx, k, "")
		putCancel()
		if putErr != nil {
			result.Success = false
			result.Output = fmt.Sprintf("failed to put: %v", putErr)

			return
		}

		kvs1 = append(kvs1, &mvccpb.KeyValue{
			Key:            []byte(k),
			Value:          nil,
			CreateRevision: presp.Header.GetRevision(),
			ModRevision:    presp.Header.GetRevision(),
			Version:        1,
		})
	}

	ctx, cancel = runner.NewCtx()
	dresp, err = cli.Delete(
		ctx,
		string(kvs1[3].Key),
		clientv3.WithRange(string(kvs1[7].Key)),
	)
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to put: %v", err)

		return
	}
	if dresp.Deleted != 4 {
		result.Success = false
		result.Output = fmt.Sprintf("expected to delete 4, got %d", dresp.Deleted)

		return
	}

	kvs2 := make([]*mvccpb.KeyValue, len(kvs1)-4)
	copy(kvs2, kvs1[:3])
	copy(kvs2[3:], kvs1[7:])

	ctx, cancel = runner.NewCtx()
	gresp, err := cli.Get(ctx, testKey, clientv3.WithPrefix())
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to get: %v", err)

		return
	}
	if !reflect.DeepEqual(gresp.Kvs, kvs2) {
		result.Success = false
		result.Output = fmt.Sprintf("expected %+v, got %+v", kvs2, gresp.Kvs)

		return
	}

	// Test various delete range scenarios using keys scoped to our test prefix.
	// IMPORTANT: All test keys are scoped to testKey prefix to avoid deleting
	// Kubernetes data or other keys when running against a live cluster.
	rangeTestKey := runner.GenerateRandomKey(10)
	keyA := path.Join(rangeTestKey, "a")
	keyB := path.Join(rangeTestKey, "b")
	keyC := path.Join(rangeTestKey, "c")
	keyCabc := path.Join(rangeTestKey, "c/abc")
	keyD := path.Join(rangeTestKey, "d")

	tests := []struct {
		key   string
		opts  []clientv3.OpOption
		wkeys []string
	}{
		{ // [a, c) - delete keys from a up to (but not including) c
			key:   keyA,
			opts:  []clientv3.OpOption{clientv3.WithRange(keyC)},
			wkeys: []string{keyC, keyCabc, keyD},
		},
		{ // c* - delete all keys with prefix c
			key:   keyC,
			opts:  []clientv3.OpOption{clientv3.WithPrefix()},
			wkeys: []string{keyA, keyB, keyD},
		},
		{ // [c, end of test range) - delete from c to end of test prefix
			// This tests WithFromKey semantics but bounded to our test range
			key:   keyC,
			opts:  []clientv3.OpOption{clientv3.WithRange(rangeTestKey + "\xff")},
			wkeys: []string{keyA, keyB},
		},
		{ // * - delete all test keys (bounded to test prefix)
			key:   rangeTestKey,
			opts:  []clientv3.OpOption{clientv3.WithPrefix()},
			wkeys: []string{},
		},
	}
	for i, tt := range tests {
		keySet := []string{keyA, keyB, keyC, keyCabc, keyD}
		for j, key := range keySet {
			ctx, cancel = runner.NewCtx()
			_, putErr := cli.Put(ctx, key, "")
			cancel()
			if putErr != nil {
				result.Success = false
				result.Output = fmt.Sprintf("#%d-#%d: failed to put: %v", i, j, putErr)

				return
			}
		}

		ctx, cancel = runner.NewCtx()
		_, err = cli.Delete(ctx, tt.key, tt.opts...)
		cancel()
		if err != nil {
			result.Success = false
			result.Output = fmt.Sprintf("#%d: failed to delete: %v", i, err)

			return
		}

		ctx, cancel = runner.NewCtx()
		resp, err := cli.Get(ctx, rangeTestKey, clientv3.WithPrefix())
		cancel()
		if err != nil {
			result.Success = false
			result.Output = fmt.Sprintf("#%d: failed to get: %v", i, err)

			return
		}
		keys := make([]string, 0, len(resp.Kvs))
		for _, kv := range resp.Kvs {
			keys = append(keys, string(kv.Key))
		}
		if !reflect.DeepEqual(tt.wkeys, keys) {
			result.Success = false
			result.Output = fmt.Sprintf("#%d: expected %+v, got %+v", i, tt.wkeys, keys)

			return
		}
	}
}

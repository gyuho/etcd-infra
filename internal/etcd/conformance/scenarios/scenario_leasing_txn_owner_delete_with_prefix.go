package scenarios

import (
	"fmt"
	"time"

	testtime "git.tbd/etcd-infra/internal/etcd/testtime"
	clientv3 "go.etcd.io/etcd/client/v3"
	"go.etcd.io/etcd/client/v3/leasing"

	logutil "git.tbd/etcd-infra/pkg/log"
	"git.tbd/etcd-infra/pkg/randutil"
)

// RunLeasingTxnOwnerDeleteWithPrefix tests the LeasingTxnOwnerDeleteWithPrefix scenario.
func RunLeasingTxnOwnerDeleteWithPrefix(runner Runner) {
	logutil.S().Infow("running", "scenario", LeasingTxnOwnerDeleteWithPrefix.String())

	result := &Result{
		Scenario:  LeasingTxnOwnerDeleteWithPrefix.String(),
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

	testPfx := runner.GenerateRandomKey(10)
	keyPrefix := testPfx + "-k-"

	lKV, closeLKV, err := leasing.NewKV(cli, testPfx)
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to create leasing KV: %v", err)

		return
	}
	defer closeLKV()

	keyCount := randutil.Intn(10) + 1
	for i := range keyCount {
		k := fmt.Sprintf("%s%d", keyPrefix, i)
		ctx, cancel := runner.NewCtx()
		_, err = lKV.Put(ctx, k, k+k)
		cancel()
		if err != nil {
			result.Success = false
			result.Output = fmt.Sprintf("failed to put: %v", err)

			return
		}
	}

	// cache in lKV
	resp, err := waitForLeasingCacheSize(runner, lKV, keyPrefix, keyCount, 3*time.Second)
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("cache mismatch: %v (last response: %+v)", err, resp)

		return
	}

	ctx, cancel := runner.NewCtx()
	_, err = lKV.Txn(ctx).Then(clientv3.OpDelete(keyPrefix, clientv3.WithPrefix())).Commit()
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to txn: %v", err)

		return
	}

	resp, err = waitForLeasingCacheSize(runner, lKV, keyPrefix, 0, 3*time.Second)
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("cache mismatch after delete: %v (last response: %+v)", err, resp)

		return
	}
}

func waitForLeasingCacheSize(runner Runner, lKV clientv3.KV, key string, expected int, timeout time.Duration) (*clientv3.GetResponse, error) {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	var lastResp *clientv3.GetResponse
	for {
		ctx, cancel := runner.NewCtx()
		resp, err := lKV.Get(ctx, key, clientv3.WithPrefix())
		cancel()
		if err != nil {
			return nil, fmt.Errorf("failed to get keys with prefix: %w", err)
		}
		lastResp = resp
		if len(resp.Kvs) == expected {
			return resp, nil
		}
		select {
		case <-ticker.C:
		case <-deadline.C:
			return lastResp, fmt.Errorf("expected %d keys, got %d", expected, len(lastResp.Kvs))
		}
	}
}

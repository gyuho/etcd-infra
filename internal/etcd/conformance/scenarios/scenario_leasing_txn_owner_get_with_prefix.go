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

// RunLeasingTxnOwnerGetWithPrefix tests the LeasingTxnOwnerGetWithPrefix scenario.
func RunLeasingTxnOwnerGetWithPrefix(runner Runner) {
	logutil.S().Infow("running", "scenario", LeasingTxnOwnerGetWithPrefix.String())

	result := &Result{
		Scenario:  LeasingTxnOwnerGetWithPrefix.String(),
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
		_, putErr := lKV.Put(ctx, k, k+k)
		cancel()
		if putErr != nil {
			result.Success = false
			result.Output = fmt.Sprintf("failed to put: %v", putErr)

			return
		}
	}
	if _, waitErr := waitForLeasingCacheSize(runner, lKV, keyPrefix, keyCount, 3*time.Second); waitErr != nil {
		result.Success = false
		result.Output = fmt.Sprintf("cache mismatch before txn: %v", waitErr)

		return
	}

	ctx, cancel := runner.NewCtx()
	tresp, err := lKV.Txn(ctx).Then(clientv3.OpGet(keyPrefix, clientv3.WithPrefix())).Commit()
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to txn: %v", err)

		return
	}
	if resp := tresp.Responses[0].GetResponseRange(); len(resp.Kvs) != keyCount {
		result.Success = false
		result.Output = fmt.Sprintf("cache mismatch, expected %d, got %d keys: %+v", keyCount, len(resp.Kvs), resp)

		return
	}
}

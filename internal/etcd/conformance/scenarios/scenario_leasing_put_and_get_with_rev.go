package scenarios

import (
	"fmt"

	testtime "git.tbd/etcd-infra/internal/etcd/testtime"
	clientv3 "go.etcd.io/etcd/client/v3"
	"go.etcd.io/etcd/client/v3/leasing"

	logutil "git.tbd/etcd-infra/pkg/log"
)

const (
	leasingHistoricValue = "bar1"
	leasingCurrentValue  = "bar2"
)

// RunLeasingPutAndGetWithRev tests the LeasingPutAndGetWithRev scenario.
func RunLeasingPutAndGetWithRev(runner Runner) {
	logutil.S().Infow("running", "scenario", LeasingPutAndGetWithRev.String())

	result := &Result{
		Scenario:  LeasingPutAndGetWithRev.String(),
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

	ctx, cancel := runner.NewCtx()
	presp, err := cli.Put(ctx, testKey, leasingHistoricValue)
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to put: %v", err)

		return
	}
	putRev1 := presp.Header.GetRevision()

	ctx, cancel = runner.NewCtx()
	_, err = cli.Put(ctx, testKey, leasingCurrentValue)
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to put: %v", err)

		return
	}

	testPfx := runner.GenerateRandomKey(10)

	lKV, closeLKV, err := leasing.NewKV(cli, testPfx)
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to create leasing KV: %v", err)

		return
	}
	defer closeLKV()

	// check historic revision
	ctx, cancel = runner.NewCtx()
	getResp, err := lKV.Get(ctx, testKey, clientv3.WithRev(putRev1))
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to get: %v", err)

		return
	}

	if len(getResp.Kvs) != 1 || string(getResp.Kvs[0].Value) != leasingHistoricValue {
		result.Success = false
		result.Output = fmt.Sprintf("key count mismatch: %d vs %d", len(getResp.Kvs), 1)

		return
	}

	// check current revision
	ctx, cancel = runner.NewCtx()
	getResp, err = lKV.Get(ctx, testKey)
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to get: %v", err)

		return
	}

	if len(getResp.Kvs) != 1 || string(getResp.Kvs[0].Value) != leasingCurrentValue {
		result.Success = false
		result.Output = fmt.Sprintf("key count mismatch: %d vs %d", len(getResp.Kvs), 1)

		return
	}
}

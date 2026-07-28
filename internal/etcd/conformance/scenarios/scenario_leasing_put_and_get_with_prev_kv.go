package scenarios

import (
	"fmt"

	testtime "git.tbd/etcd-infra/internal/etcd/testtime"
	clientv3 "go.etcd.io/etcd/client/v3"
	"go.etcd.io/etcd/client/v3/leasing"

	logutil "git.tbd/etcd-infra/pkg/log"
)

const (
	leasingPrevValue = "bar1"
)

// RunLeasingPutAndGetWithPrevKv tests the LeasingPutAndGetWithPrevKv scenario.
func RunLeasingPutAndGetWithPrevKv(runner Runner) {
	logutil.S().Infow("running", "scenario", LeasingPutAndGetWithPrevKv.String())

	result := &Result{
		Scenario:  LeasingPutAndGetWithPrevKv.String(),
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
	_, err = cli.Put(ctx, testKey, leasingPrevValue)
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

	// acquire leasing key
	ctx, cancel = runner.NewCtx()
	_, err = lKV.Get(ctx, testKey)
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to get: %v", err)

		return
	}

	ctx, cancel = runner.NewCtx()
	resp, err := lKV.Put(ctx, testKey, leasingNewValue, clientv3.WithPrevKV())
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to put: %v", err)

		return
	}
	if resp.PrevKv == nil || string(resp.PrevKv.Value) != leasingPrevValue {
		result.Success = false
		result.Output = fmt.Sprintf("prev kv mismatch: %+v", resp.PrevKv)

		return
	}
}

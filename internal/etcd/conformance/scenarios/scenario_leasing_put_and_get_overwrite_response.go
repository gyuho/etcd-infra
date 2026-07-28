package scenarios

import (
	"fmt"

	testtime "git.tbd/etcd-infra/internal/etcd/testtime"
	"go.etcd.io/etcd/client/v3/leasing"

	logutil "git.tbd/etcd-infra/pkg/log"
)

// RunLeasingPutAndGetOverwriteResponse tests the LeasingPutAndGetOverwriteResponse scenario.
func RunLeasingPutAndGetOverwriteResponse(runner Runner) {
	logutil.S().Infow("running", "scenario", LeasingPutAndGetOverwriteResponse.String())

	result := &Result{
		Scenario:  LeasingPutAndGetOverwriteResponse.String(),
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
	_, err = cli.Put(ctx, testKey, leasingValueBar)
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to put: %v", err)

		return
	}

	lKV, closeLKV, err := leasing.NewKV(cli, testKey)
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to create leasing KV: %v", err)

		return
	}
	defer closeLKV()

	// trigger cache update
	ctx, cancel = runner.NewCtx()
	resp, err := lKV.Get(ctx, testKey)
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to get: %v", err)

		return
	}

	if len(resp.Kvs) == 0 {
		result.Success = false
		result.Output = "expected at least one key-value pair"

		return
	}

	resp.Kvs[0].Key[0] = '0'
	resp.Kvs[0].Value[0] = '0'

	ctx, cancel = runner.NewCtx()
	resp, err = lKV.Get(ctx, testKey)
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to get: %v", err)

		return
	}

	if len(resp.Kvs) == 0 {
		result.Success = false
		result.Output = "expected at least one key-value pair after overwrite"

		return
	}

	k, v := string(resp.Kvs[0].Key), string(resp.Kvs[0].Value)
	if k != testKey {
		result.Success = false
		result.Output = fmt.Sprintf("key mismatch: %s vs %s", k, testKey)

		return
	}
	if v != leasingValueBar {
		result.Success = false
		result.Output = fmt.Sprintf("value mismatch: %s vs %s", v, leasingValueBar)

		return
	}
}

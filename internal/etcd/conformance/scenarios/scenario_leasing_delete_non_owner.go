package scenarios

import (
	"fmt"

	testtime "git.tbd/etcd-infra/internal/etcd/testtime"
	"go.etcd.io/etcd/client/v3/leasing"

	logutil "git.tbd/etcd-infra/pkg/log"
)

// RunLeasingDeleteNonOwner tests the LeasingDeleteNonOwner scenario.
func RunLeasingDeleteNonOwner(runner Runner) {
	logutil.S().Infow("running", "scenario", LeasingDeleteNonOwner.String())

	result := &Result{
		Scenario:  LeasingDeleteNonOwner.String(),
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

	lKV1, closeLKV1, err := leasing.NewKV(cli, testPfx)
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to create leasing KV: %v", err)

		return
	}
	defer closeLKV1()

	lKV2, closeLKV2, err := leasing.NewKV(cli, testPfx)
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to create leasing KV: %v", err)

		return
	}
	defer closeLKV2()

	testKey := runner.GenerateRandomKey(10)

	ctx, cancel := runner.NewCtx()
	_, err = cli.Put(ctx, testKey, "bar")
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to put: %v", err)

		return
	}

	// acquire ownership
	ctx, cancel = runner.NewCtx()
	_, err = lKV1.Get(ctx, testKey)
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to leasing get: %v", err)

		return
	}

	// delete via non-owner
	ctx, cancel = runner.NewCtx()
	_, err = lKV2.Delete(ctx, testKey)
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to leasing delete: %v", err)

		return
	}

	// key should be removed from lKV1
	ctx, cancel = runner.NewCtx()
	resp, err := lKV1.Get(ctx, testKey)
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to leasing get: %v", err)

		return
	}
	if len(resp.Kvs) > 0 {
		result.Success = false
		result.Output = fmt.Sprintf("expected no key, got %+v", resp.Kvs)

		return
	}
}

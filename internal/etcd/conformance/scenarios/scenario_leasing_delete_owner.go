package scenarios

import (
	"fmt"

	testtime "git.tbd/etcd-infra/internal/etcd/testtime"
	"go.etcd.io/etcd/client/v3/leasing"

	logutil "git.tbd/etcd-infra/pkg/log"
)

// RunLeasingDeleteOwner tests the LeasingDeleteOwner scenario.
func RunLeasingDeleteOwner(runner Runner) {
	logutil.S().Infow("running", "scenario", LeasingDeleteOwner.String())

	result := &Result{
		Scenario:  LeasingDeleteOwner.String(),
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

	lKV, closeLKV, err := leasing.NewKV(cli, testPfx)
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to create leasing KV: %v", err)

		return
	}
	defer closeLKV()

	testKey := runner.GenerateRandomKey(10)

	ctx, cancel := runner.NewCtx()
	_, err = cli.Put(ctx, testKey, "bar")
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to put: %v", err)

		return
	}

	// get+own / delete / get
	ctx, cancel = runner.NewCtx()
	_, err = lKV.Get(ctx, testKey)
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to leasing get: %v", err)

		return
	}

	// delete via owner
	ctx, cancel = runner.NewCtx()
	_, err = lKV.Delete(ctx, testKey)
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to leasing delete: %v", err)

		return
	}

	ctx, cancel = runner.NewCtx()
	resp, err := lKV.Get(ctx, testKey)
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to leasing get: %v", err)

		return
	}
	if len(resp.Kvs) != 0 {
		result.Success = false
		result.Output = fmt.Sprintf("expected no key, got %+v", resp.Kvs)

		return
	}

	// try to double delete
	ctx, cancel = runner.NewCtx()
	_, err = lKV.Delete(ctx, testKey)
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("redundant delete should not return error, got %v", err)

		return
	}
}

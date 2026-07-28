package scenarios

import (
	"fmt"

	testtime "git.tbd/etcd-infra/internal/etcd/testtime"
	"go.etcd.io/etcd/client/v3/leasing"

	logutil "git.tbd/etcd-infra/pkg/log"
)

const (
	baseKeyABC             = "abc"
	leasingValueDef        = "def"
	leasingUpdatedValueGhi = "ghi"
)

// RunLeasingPutAndGet tests the LeasingPutAndGet scenario.
func RunLeasingPutAndGet(runner Runner) {
	logutil.S().Infow("running", "scenario", LeasingPutAndGet.String())

	result := &Result{
		Scenario:  LeasingPutAndGet.String(),
		TimeStart: testtime.Now(),
		Success:   true,
		Output:    "ok",
	}
	defer func() {
		result.RecordTimeEnd(testtime.Now())
		runner.RecordResult(*result)
	}()

	cli1, err := runner.NewClient()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to create client: %v", err)

		return
	}
	defer func() { _ = cli1.Close() }()

	cli2, err := runner.NewClient()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to create client: %v", err)

		return
	}
	defer func() { _ = cli2.Close() }()

	cli3, err := runner.NewClient()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to create client: %v", err)

		return
	}
	defer func() { _ = cli3.Close() }()

	testPfx := runner.GenerateRandomKey(10)

	lKV1, closeLKV1, err := leasing.NewKV(cli1, testPfx)
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to create leasing KV: %v", err)

		return
	}
	defer closeLKV1()

	lKV2, closeLKV2, err := leasing.NewKV(cli2, testPfx)
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to create leasing KV: %v", err)

		return
	}
	defer closeLKV2()

	lKV3, closeLKV3, err := leasing.NewKV(cli3, testPfx)
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to create leasing KV: %v", err)

		return
	}
	defer closeLKV3()

	// Use prefixed keys to ensure test isolation in live clusters
	testKeyABC := testPfx + "/" + baseKeyABC

	ctx, cancel := runner.NewCtx()
	resp, err := lKV1.Get(ctx, testKeyABC)
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to get: %v", err)

		return
	}
	if len(resp.Kvs) > 0 {
		result.Success = false
		result.Output = fmt.Sprintf("key count mismatch: %d vs %d", len(resp.Kvs), 0)

		return
	}
	ctx, cancel = runner.NewCtx()
	_, err = lKV1.Put(ctx, testKeyABC, leasingValueDef)
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to put: %v", err)

		return
	}

	ctx, cancel = runner.NewCtx()
	resp, err = lKV2.Get(ctx, testKeyABC)
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to get: %v", err)

		return
	}
	if string(resp.Kvs[0].Key) != testKeyABC {
		result.Success = false
		result.Output = fmt.Sprintf("key mismatch: %s vs %s", string(resp.Kvs[0].Key), testKeyABC)

		return
	}
	if string(resp.Kvs[0].Value) != leasingValueDef {
		result.Success = false
		result.Output = fmt.Sprintf("value mismatch: %s vs %s", string(resp.Kvs[0].Value), leasingValueDef)

		return
	}

	ctx, cancel = runner.NewCtx()
	_, err = lKV3.Get(ctx, testKeyABC)
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to get: %v", err)

		return
	}

	ctx, cancel = runner.NewCtx()
	_, err = lKV2.Put(ctx, testKeyABC, leasingUpdatedValueGhi)
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to put: %v", err)

		return
	}

	ctx, cancel = runner.NewCtx()
	resp, err = lKV3.Get(ctx, testKeyABC)
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to get: %v", err)

		return
	}
	if string(resp.Kvs[0].Key) != testKeyABC {
		result.Success = false
		result.Output = fmt.Sprintf("key mismatch: %s vs %s", string(resp.Kvs[0].Key), testKeyABC)

		return
	}
	if string(resp.Kvs[0].Value) != leasingUpdatedValueGhi {
		result.Success = false
		result.Output = fmt.Sprintf("value mismatch: %s vs %s", string(resp.Kvs[0].Value), leasingUpdatedValueGhi)

		return
	}
}

package scenarios

import (
	"fmt"

	testtime "git.tbd/etcd-infra/internal/etcd/testtime"
	clientv3 "go.etcd.io/etcd/client/v3"
	"go.etcd.io/etcd/client/v3/leasing"

	logutil "git.tbd/etcd-infra/pkg/log"
	"git.tbd/etcd-infra/pkg/randutil"
)

// RunLeasingPutAndGetWithOpts tests the LeasingPutAndGetWithOpts scenario.
func RunLeasingPutAndGetWithOpts(runner Runner) {
	logutil.S().Infow("running", "scenario", LeasingPutAndGetWithOpts.String())

	result := &Result{
		Scenario:  LeasingPutAndGetWithOpts.String(),
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
	_, err = cli.Put(ctx, testKey, "bar1")
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

	// acquire leasing key to cache
	ctx, cancel = runner.NewCtx()
	_, err = lKV.Get(ctx, testKey)
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to get: %v", err)

		return
	}

	opts := []clientv3.OpOption{
		clientv3.WithKeysOnly(),
		clientv3.WithLimit(1),
		clientv3.WithMinCreateRev(1),
		clientv3.WithMinModRev(1),
		clientv3.WithSort(clientv3.SortByKey, clientv3.SortAscend),
		clientv3.WithSerializable(),
	}
	for _, opt := range opts {
		ctx, cancel = runner.NewCtx()
		_, err = lKV.Get(ctx, testKey, opt)
		cancel()
		if err != nil {
			result.Success = false
			result.Output = fmt.Sprintf("failed to get: %v", err)

			return
		}
	}

	getOpts := []clientv3.OpOption{}
	for range opts {
		getOpts = append(getOpts, opts[randutil.Intn(len(opts))])
	}
	getOpts = getOpts[:randutil.Intn(len(opts))]

	ctx, cancel = runner.NewCtx()
	_, err = lKV.Get(ctx, testKey, getOpts...)
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to get: %v", err)

		return
	}
}

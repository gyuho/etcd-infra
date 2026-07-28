package scenarios

import (
	"fmt"
	"path"

	testtime "git.tbd/etcd-infra/internal/etcd/testtime"
	clientv3 "go.etcd.io/etcd/client/v3"
	"go.etcd.io/etcd/client/v3/leasing"

	logutil "git.tbd/etcd-infra/pkg/log"
)

// RunLeasingPutAndGetWithPrefix tests the LeasingPutAndGetWithPrefix scenario.
func RunLeasingPutAndGetWithPrefix(runner Runner) {
	logutil.S().Infow("running", "scenario", LeasingPutAndGetWithPrefix.String())

	result := &Result{
		Scenario:  LeasingPutAndGetWithPrefix.String(),
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
	keys := []string{
		path.Join(testKey, "a"),
		path.Join(testKey, "b"),
		path.Join(testKey, "a", "a"),
	}
	for _, k := range keys {
		ctx, cancel := runner.NewCtx()
		_, err = cli.Put(ctx, k, "bar")
		cancel()
		if err != nil {
			result.Success = false
			result.Output = fmt.Sprintf("failed to put: %v", err)

			return
		}
	}

	ctx, cancel := runner.NewCtx()
	gresp, err := lKV.Get(ctx, testKey, clientv3.WithPrefix())
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to get: %v", err)

		return
	}
	if len(gresp.Kvs) != 3 {
		result.Success = false
		result.Output = fmt.Sprintf("key count mismatch: %d vs %d", len(gresp.Kvs), 3)

		return
	}

	// load into cache
	ctx, cancel = runner.NewCtx()
	_, err = lKV.Get(ctx, path.Join(testKey, "a"))
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to get: %v", err)

		return
	}

	// get when prefix is also a cached key
	ctx, cancel = runner.NewCtx()
	gresp, err = lKV.Get(ctx, path.Join(testKey, "a"), clientv3.WithPrefix())
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to get: %v", err)

		return
	}
	if len(gresp.Kvs) != 2 {
		result.Success = false
		result.Output = fmt.Sprintf("key count mismatch: %d vs %d", len(gresp.Kvs), 2)

		return
	}
}

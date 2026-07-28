package scenarios

import (
	"fmt"

	testtime "git.tbd/etcd-infra/internal/etcd/testtime"
	clientv3 "go.etcd.io/etcd/client/v3"

	logutil "git.tbd/etcd-infra/pkg/log"
)

// RunPutAndDeleteWithPrefix tests the PutAndDeleteWithPrefix scenario.
func RunPutAndDeleteWithPrefix(runner Runner) {
	logutil.S().Infow("running", "scenario", PutAndDeleteWithPrefix.String())

	result := &Result{
		Scenario:  PutAndDeleteWithPrefix.String(),
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
	testKeyValues := make([]keyValue, 0)
	for i := range 10 {
		kv := createKV(testKey, fmt.Sprintf("hello%02d", i), "bar")
		testKeyValues = append(testKeyValues, kv)
	}

	for _, kv := range testKeyValues {
		ctx, cancel := runner.NewCtx()
		_, err := cli.Put(ctx, kv.k, kv.v)
		cancel()
		if err != nil {
			result.Success = false
			result.Output = fmt.Sprintf("failed to put: %v", err)

			return
		}
	}

	ctx, cancel := runner.NewCtx()
	dresp, derr := cli.Delete(ctx, testKey, clientv3.WithPrefix())
	cancel()
	if derr != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to delete: %v", derr)

		return
	}
	if dresp.Deleted != int64(len(testKeyValues)) {
		result.Success = false
		result.Output = fmt.Sprintf("expected deleted %d, got %d", len(testKeyValues), dresp.Deleted)

		return
	}

	ctx, cancel = runner.NewCtx()
	gresp, gerr := cli.Get(ctx, testKey, clientv3.WithPrefix())
	cancel()
	if gerr != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to get: %v", gerr)

		return
	}
	if len(gresp.Kvs) != 0 {
		result.Success = false
		result.Output = "expected empty get response, but got non-empty response"

		return
	}
}

package scenarios

import (
	"fmt"

	testtime "git.tbd/etcd-infra/internal/etcd/testtime"
	"go.etcd.io/etcd/client/v3/leasing"
	"google.golang.org/protobuf/proto"

	logutil "git.tbd/etcd-infra/pkg/log"
)

// RunLeasingPutAndGetInvalidateNew tests the LeasingPutAndGetInvalidateNew scenario.
func RunLeasingPutAndGetInvalidateNew(runner Runner) {
	logutil.S().Infow("running", "scenario", LeasingPutAndGetInvalidateNew.String())

	result := &Result{
		Scenario:  LeasingPutAndGetInvalidateNew.String(),
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

	// trigger cache update
	ctx, cancel := runner.NewCtx()
	_, err = lKV.Get(ctx, testKey)
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to get: %v", err)

		return
	}

	ctx, cancel = runner.NewCtx()
	_, err = lKV.Put(ctx, testKey, "bar")
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to put: %v", err)

		return
	}

	// compare get between leasing and plain client with no cache
	ctx, cancel = runner.NewCtx()
	gresp1, err := cli.Get(ctx, testKey)
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to get: %v", err)

		return
	}

	ctx, cancel = runner.NewCtx()
	gresp2, err := lKV.Get(ctx, testKey)
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to get: %v", err)

		return
	}

	if len(gresp1.Kvs) != len(gresp2.Kvs) {
		result.Success = false
		result.Output = fmt.Sprintf("get response length mismatch: %d vs %d", len(gresp1.Kvs), len(gresp2.Kvs))

		return
	}
	for i := range gresp1.Kvs {
		if !proto.Equal(gresp1.Kvs[i], gresp2.Kvs[i]) {
			result.Success = false
			result.Output = fmt.Sprintf("get result mismatch: %+v vs %+v", gresp1, gresp2)

			return
		}
	}
}

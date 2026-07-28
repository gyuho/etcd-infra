package scenarios

import (
	"fmt"

	testtime "git.tbd/etcd-infra/internal/etcd/testtime"
	"go.etcd.io/etcd/api/v3/mvccpb"
	clientv3 "go.etcd.io/etcd/client/v3"
	"google.golang.org/protobuf/proto"

	logutil "git.tbd/etcd-infra/pkg/log"
)

// RunPutAndGetWithOp tests the PutAndGetWithOp scenario.
func RunPutAndGetWithOp(runner Runner) {
	logutil.S().Infow("running", "scenario", PutAndGetWithOp.String())

	result := &Result{
		Scenario:  PutAndGetWithOp.String(),
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
	presp, err := cli.Do(ctx, clientv3.OpPut(testKey, "bar"))
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to do for op put: %v", err)

		return
	}
	lastRev := presp.Put().Header.GetRevision()

	ctx, cancel = runner.NewCtx()
	gresp, err := cli.Do(ctx, clientv3.OpGet(testKey))
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to do for op get: %v", err)

		return
	}

	kv1 := &mvccpb.KeyValue{
		Key:            []byte(testKey),
		Value:          []byte("bar"),
		CreateRevision: lastRev,
		ModRevision:    lastRev,
		Version:        1,
	}
	kv2 := gresp.Get().Kvs[0]
	if !proto.Equal(kv1, kv2) {
		result.Success = false
		result.Output = fmt.Sprintf("returned key-value pairs are not the same: %+v != %+v", kv1, kv2)

		return
	}
}

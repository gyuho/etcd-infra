package scenarios

import (
	"bytes"
	"fmt"

	testtime "git.tbd/etcd-infra/internal/etcd/testtime"
	clientv3 "go.etcd.io/etcd/client/v3"

	logutil "git.tbd/etcd-infra/pkg/log"
)

// RunPutAndGetWithLatestRevision tests the PutAndGetWithLatestRevision scenario.
func RunPutAndGetWithLatestRevision(runner Runner) {
	logutil.S().Infow("running", "scenario", PutAndGetWithLatestRevision.String())

	result := &Result{
		Scenario:  PutAndGetWithLatestRevision.String(),
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
	testVal := leasingValueBar

	ctx, cancel := runner.NewCtx()
	presp, err := cli.Put(ctx, testKey, testVal)
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to put: %v", err)

		return
	}

	putRev := presp.Header.GetRevision()

	var gresp *clientv3.GetResponse
	ctx, cancel = runner.NewCtx()
	gresp, err = cli.Get(ctx, testKey)
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to get: %v", err)

		return
	}
	if gresp.Kvs[0].ModRevision != putRev {
		result.Success = false
		result.Output = fmt.Sprintf("GET mod revision expected %d, got %d", putRev, gresp.Kvs[0].ModRevision)

		return
	}
	if !bytes.Equal(gresp.Kvs[0].Value, []byte(testVal)) {
		result.Success = false
		result.Output = fmt.Sprintf("GET value expected %s, got %s", testVal, gresp.Kvs[0].Value)

		return
	}

	result.Success = true
	result.Output = "ok"
}

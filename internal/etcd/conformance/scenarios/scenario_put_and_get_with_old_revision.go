package scenarios

import (
	"bytes"
	"fmt"

	testtime "git.tbd/etcd-infra/internal/etcd/testtime"
	clientv3 "go.etcd.io/etcd/client/v3"

	logutil "git.tbd/etcd-infra/pkg/log"
)

// RunPutAndGetWithOldRevision tests the PutAndGetWithOldRevision scenario.
func RunPutAndGetWithOldRevision(runner Runner) {
	logutil.S().Infow("running", "scenario", PutAndGetWithOldRevision.String())

	result := &Result{
		Scenario:  PutAndGetWithOldRevision.String(),
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
	presp, err := cli.Put(ctx, testKey, "old")
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to put key: %v", err)

		return
	}
	oldPutRev := presp.Header.GetRevision()
	logutil.S().Infow("PUT success", "key", testKey, "oldPutRev", oldPutRev)

	ctx, cancel = runner.NewCtx()
	presp, err = cli.Put(ctx, testKey, "new")
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to put key: %v", err)

		return
	}
	newPutRev := presp.Header.GetRevision()
	logutil.S().Infow("PUT success", "key", testKey, "newPutRev", newPutRev)

	ctx, cancel = runner.NewCtx()
	gresp, err := cli.Get(ctx, testKey, clientv3.WithRev(oldPutRev))
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to get key: %v", err)

		return
	}

	// The header revision should be at least newPutRev.
	// On shared clusters or cloud environments with concurrent operations,
	// the header revision may be higher than newPutRev due to other writes.
	// This is expected behavior - we just verify we're not getting a stale response.
	if gresp.Header.GetRevision() < newPutRev {
		result.Success = false
		result.Output = fmt.Sprintf("got stale revision in the get header: %v (expected >= %v)", gresp.Header.GetRevision(), newPutRev)

		return
	}
	if gresp.Kvs[0].ModRevision != oldPutRev {
		result.Success = false
		result.Output = fmt.Sprintf("got wrong mod revision in the get response: %v", gresp.Kvs[0].ModRevision)

		return
	}
	if !bytes.Equal(gresp.Kvs[0].Value, []byte("old")) {
		result.Success = false
		result.Output = fmt.Sprintf("got wrong value: %v", string(gresp.Kvs[0].Value))

		return
	}
}

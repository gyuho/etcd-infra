package scenarios

import (
	"fmt"
	"path"

	testtime "git.tbd/etcd-infra/internal/etcd/testtime"
	"go.etcd.io/etcd/client/v3/namespace"

	logutil "git.tbd/etcd-infra/pkg/log"
)

// RunPutAndGetWithNamespace tests the PutAndGetWithNamespace scenario.
func RunPutAndGetWithNamespace(runner Runner) {
	logutil.S().Infow("running", "scenario", PutAndGetWithNamespace.String())

	result := &Result{
		Scenario:  PutAndGetWithNamespace.String(),
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

	nsKV := namespace.NewKV(cli.KV, testKey+"/")

	ctx, cancel := runner.NewCtx()
	_, err = nsKV.Put(ctx, "abc", leasingValueBar)
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to put: %v", err)

		return
	}

	ctx, cancel = runner.NewCtx()
	gresp, err := nsKV.Get(ctx, "abc")
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to get: %v", err)

		return
	}
	if string(gresp.Kvs[0].Key) != "abc" {
		result.Success = false
		result.Output = fmt.Sprintf("expected key=%q, got key=%q", "abc", gresp.Kvs[0].Key)

		return
	}
	if string(gresp.Kvs[0].Value) != leasingValueBar {
		result.Success = false
		result.Output = fmt.Sprintf("expected value=%q, got value=%q", leasingValueBar, gresp.Kvs[0].Value)

		return
	}

	ctx, cancel = runner.NewCtx()
	gresp, err = cli.Get(ctx, path.Join(testKey, "abc"))
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to get: %v", err)

		return
	}
	if string(gresp.Kvs[0].Key) != path.Join(testKey, "abc") {
		result.Success = false
		result.Output = fmt.Sprintf("expected key=%q, got key=%q", path.Join(testKey, "abc"), gresp.Kvs[0].Key)

		return
	}
	if string(gresp.Kvs[0].Value) != leasingValueBar {
		result.Success = false
		result.Output = fmt.Sprintf("expected value=%q, got value=%q", leasingValueBar, gresp.Kvs[0].Value)

		return
	}
}

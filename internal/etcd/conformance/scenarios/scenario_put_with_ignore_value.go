package scenarios

import (
	"bytes"
	"errors"
	"fmt"

	testtime "git.tbd/etcd-infra/internal/etcd/testtime"
	"go.etcd.io/etcd/api/v3/v3rpc/rpctypes"
	clientv3 "go.etcd.io/etcd/client/v3"

	logutil "git.tbd/etcd-infra/pkg/log"
)

// RunPutWithIgnoreValue tests the PutWithIgnoreValue scenario.
func RunPutWithIgnoreValue(runner Runner) {
	logutil.S().Infow("running", "scenario", PutWithIgnoreValue.String())

	result := &Result{
		Scenario:  PutWithIgnoreValue.String(),
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
	_, err = cli.Put(ctx, testKey, "", clientv3.WithIgnoreValue())
	cancel()
	if !errors.Is(err, rpctypes.ErrKeyNotFound) {
		result.Success = false
		result.Output = fmt.Sprintf("unexpected error %v, expected %v", err, rpctypes.ErrKeyNotFound)

		return
	}

	ctx, cancel = runner.NewCtx()
	_, err = cli.Put(ctx, testKey, "bar")
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to put: %v", err)

		return
	}

	ctx, cancel = runner.NewCtx()
	_, err = cli.Put(ctx, testKey, "", clientv3.WithIgnoreValue())
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to put: %v", err)

		return
	}

	ctx, cancel = runner.NewCtx()
	gresp, err := cli.Get(ctx, testKey)
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to put: %v", err)

		return
	}
	if len(gresp.Kvs) != 1 {
		result.Success = false
		result.Output = fmt.Sprintf("unexpected number of keys: %d", len(gresp.Kvs))

		return
	}
	if !bytes.Equal(gresp.Kvs[0].Key, []byte(testKey)) {
		result.Success = false
		result.Output = fmt.Sprintf("unexpected key: %q", string(gresp.Kvs[0].Key))

		return
	}
	if !bytes.Equal(gresp.Kvs[0].Value, []byte("bar")) {
		result.Success = false
		result.Output = fmt.Sprintf("unexpected value: %q", string(gresp.Kvs[0].Value))

		return
	}
	if gresp.Kvs[0].Version != 2 {
		result.Success = false
		result.Output = fmt.Sprintf("unexpected version: %d", gresp.Kvs[0].Version)

		return
	}
}

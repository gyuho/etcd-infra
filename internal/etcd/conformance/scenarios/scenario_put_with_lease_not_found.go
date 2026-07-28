package scenarios

import (
	"errors"
	"fmt"

	testtime "git.tbd/etcd-infra/internal/etcd/testtime"
	"go.etcd.io/etcd/api/v3/v3rpc/rpctypes"
	clientv3 "go.etcd.io/etcd/client/v3"

	logutil "git.tbd/etcd-infra/pkg/log"
)

// RunPutWithLeaseNotFound tests the PutWithLeaseNotFound scenario.
func RunPutWithLeaseNotFound(runner Runner) {
	logutil.S().Infow("running", "scenario", PutWithLeaseNotFound.String())

	result := &Result{
		Scenario:  PutWithLeaseNotFound.String(),
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
	_, err = cli.Put(ctx, testKey, "bar", clientv3.WithLease(clientv3.LeaseID(500)))
	cancel()
	if !errors.Is(err, rpctypes.ErrLeaseNotFound) {
		result.Success = false
		result.Output = fmt.Sprintf("expected ErrLeaseNotFound, got %v", err)

		return
	}
}

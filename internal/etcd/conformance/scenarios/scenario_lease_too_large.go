package scenarios

import (
	"errors"
	"fmt"

	testtime "git.tbd/etcd-infra/internal/etcd/testtime"
	"go.etcd.io/etcd/api/v3/v3rpc/rpctypes"
	clientv3 "go.etcd.io/etcd/client/v3"

	logutil "git.tbd/etcd-infra/pkg/log"
)

// RunLeaseTooLarge tests the LeaseTooLarge scenario.
func RunLeaseTooLarge(runner Runner) {
	logutil.S().Infow("running", "scenario", LeaseTooLarge.String())

	result := &Result{
		Scenario:  LeaseTooLarge.String(),
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

	ctx, cancel := runner.NewCtx()
	_, err = cli.Grant(ctx, clientv3.MaxLeaseTTL+1)
	cancel()
	if !errors.Is(err, rpctypes.ErrLeaseTTLTooLarge) {
		result.Success = false
		result.Output = fmt.Sprintf("expected ErrLeaseTTLTooLarge, got: %v", err)

		return
	}
}

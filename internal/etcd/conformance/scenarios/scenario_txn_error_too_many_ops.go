package scenarios

import (
	"errors"
	"fmt"

	testtime "git.tbd/etcd-infra/internal/etcd/testtime"
	"go.etcd.io/etcd/api/v3/v3rpc/rpctypes"
	clientv3 "go.etcd.io/etcd/client/v3"

	logutil "git.tbd/etcd-infra/pkg/log"
)

// RunTxnErrorTooManyOps tests the TxnErrorTooManyOps scenario.
func RunTxnErrorTooManyOps(runner Runner) {
	logutil.S().Infow("running", "scenario", TxnErrorTooManyOps.String())

	result := &Result{
		Scenario:  TxnErrorTooManyOps.String(),
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

	// ref. go.etcd.io/etcd/server/v3/server/embed.DefaultMaxTxnOps is 128
	ops := make([]clientv3.Op, int(128+10))
	for i := range ops {
		ops[i] = clientv3.OpPut(fmt.Sprintf("%s%d", testKey, i), "")
	}

	ctx, cancel := runner.NewCtx()
	_, err = cli.Txn(ctx).Then(ops...).Commit()
	cancel()
	if !errors.Is(err, rpctypes.ErrTooManyOps) {
		result.Success = false
		result.Output = fmt.Sprintf("expected %v, got %v", rpctypes.ErrTooManyOps, err)

		return
	}
}

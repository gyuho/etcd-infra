package scenarios

import (
	"errors"
	"fmt"

	testtime "git.tbd/etcd-infra/internal/etcd/testtime"
	"go.etcd.io/etcd/api/v3/v3rpc/rpctypes"

	logutil "git.tbd/etcd-infra/pkg/log"
)

// RunPutEmptyKeyShouldError tests the PutEmptyKeyShouldError scenario.
func RunPutEmptyKeyShouldError(runner Runner) {
	logutil.S().Infow("running", "scenario", PutEmptyKeyShouldError.String())

	result := &Result{
		Scenario:  PutEmptyKeyShouldError.String(),
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
	_, err = cli.Put(ctx, "", "value")
	cancel()
	if !errors.Is(err, rpctypes.ErrEmptyKey) {
		result.Success = false
		result.Output = fmt.Sprintf("expected error %v, got %v", rpctypes.ErrEmptyKey, err)

		return
	}
}

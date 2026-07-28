package scenarios

import (
	"errors"
	"fmt"
	"strings"

	testtime "git.tbd/etcd-infra/internal/etcd/testtime"
	"go.etcd.io/etcd/api/v3/v3rpc/rpctypes"

	logutil "git.tbd/etcd-infra/pkg/log"
)

// RunPutLargeShouldError tests the PutLargeShouldError scenario.
func RunPutLargeShouldError(runner Runner) {
	logutil.S().Infow("running", "scenario", PutLargeShouldError.String())

	result := &Result{
		Scenario:  PutLargeShouldError.String(),
		TimeStart: testtime.Now(),
		Success:   true,
		Output:    "ok",
	}
	defer func() {
		result.RecordTimeEnd(testtime.Now())
		runner.RecordResult(*result)
	}()

	cliWithDefaultLimit, err := runner.NewClient()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to create client: %v", err)

		return
	}
	defer func() { _ = cliWithDefaultLimit.Close() }()

	// hard coded max in v3_server.go
	maxReqBytes := 1.5 * 1024 * 1024

	testKey := runner.GenerateRandomKey(10)
	testValue := strings.Repeat("0", int(maxReqBytes+100))

	ctx, cancel := runner.NewCtx()
	_, err = cliWithDefaultLimit.Put(ctx, testKey, testValue)
	cancel()
	if !errors.Is(err, rpctypes.ErrRequestTooLarge) {
		result.Success = false
		result.Output = fmt.Sprintf("expected error %v, got %v", rpctypes.ErrRequestTooLarge, err)

		return
	}

	cliWithLargeLimit, err := runner.NewClient(WithMaxCallSendMsgSize(5 * 1024 * 1024))
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to create client: %v", err)

		return
	}
	defer func() { _ = cliWithLargeLimit.Close() }()

	ctx, cancel = runner.NewCtx()
	_, err = cliWithLargeLimit.Put(ctx, testKey, strings.Repeat("0", 7*1024*1024))
	cancel()
	exp := "code = ResourceExhausted desc = trying to send message larger than max ("
	if !strings.Contains(err.Error(), exp) {
		result.Success = false
		result.Output = fmt.Sprintf("expected %q, got %v", exp, err)

		return
	}
}

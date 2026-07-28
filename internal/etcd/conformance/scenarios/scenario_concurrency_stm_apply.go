package scenarios

import (
	"errors"
	"fmt"
	"path"
	"strconv"
	"time"

	testtime "git.tbd/etcd-infra/internal/etcd/testtime"
	"go.etcd.io/etcd/api/v3/v3rpc/rpctypes"
	"go.etcd.io/etcd/client/v3/concurrency"

	logutil "git.tbd/etcd-infra/pkg/log"
	"git.tbd/etcd-infra/pkg/randutil"
)

// RunConcurrencyStmApply tests the ConcurrencyStmApply scenario.
func RunConcurrencyStmApply(runner Runner) {
	logutil.S().Infow("running", "scenario", ConcurrencyStmApply.String())

	result := &Result{
		Scenario:  ConcurrencyStmApply.String(),
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

	// set up "accounts"
	totalAccounts := 5
	for i := range totalAccounts {
		k := path.Join(testKey, fmt.Sprintf("%05d", i))
		ctx, cancel := runner.NewCtx()
		_, err := cli.Put(ctx, k, "100")
		cancel()
		if err != nil {
			result.Success = false
			result.Output = fmt.Sprintf("failed to put: %v", err)

			return
		}
	}

	exchange := func(stm concurrency.STM) error {
		from, to := randutil.Intn(totalAccounts), randutil.Intn(totalAccounts)
		if from == to {
			// nothing to do
			return nil
		}
		// read values
		acctA, acctB := path.Join(testKey, fmt.Sprintf("%05d", from)), path.Join(testKey, fmt.Sprintf("%05d", to))
		valA, err := strconv.ParseInt(stm.Get(acctA), 10, 64)
		if err != nil {
			return fmt.Errorf("failed to get key: %w", err)
		}
		valB, err := strconv.ParseInt(stm.Get(acctB), 10, 64)
		if err != nil {
			return fmt.Errorf("failed to get key: %w", err)
		}

		// transfer amount
		xfer := valA / 2
		valA, valB = valA-xfer, valB+xfer

		// write back
		stm.Put(acctA, strconv.FormatInt(valA, 10))
		stm.Put(acctB, strconv.FormatInt(valB, 10))

		return nil
	}

	// concurrently exchange values between accounts
	errc := make(chan error, 10)
	for range 10 {
		go func() {
			var serr error
			for range 3 {
				_, serr = concurrency.NewSTM(cli, exchange)
				if serr == nil {
					break
				}
				if !errors.Is(serr, rpctypes.ErrFutureRev) {
					break
				}
				time.Sleep(50 * time.Millisecond)
			}
			errc <- serr
		}()
	}
	timeout := time.After(10 * time.Second)
	for i := range 10 {
		var err error
		select {
		case err = <-errc:
			if err != nil {
				result.Success = false
				result.Output = fmt.Sprintf("failed to apply: %v", err)

				return
			}
		case <-timeout:
			result.Success = false
			result.Output = fmt.Sprintf("timed out waiting for STM applies (received %d/10)", i)

			return
		}
	}
}

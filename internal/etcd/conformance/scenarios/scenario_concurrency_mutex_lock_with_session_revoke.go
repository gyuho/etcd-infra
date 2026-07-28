package scenarios

import (
	"context"
	"errors"
	"fmt"
	"time"

	testtime "git.tbd/etcd-infra/internal/etcd/testtime"
	"go.etcd.io/etcd/api/v3/v3rpc/rpctypes"
	"go.etcd.io/etcd/client/v3/concurrency"

	logutil "git.tbd/etcd-infra/pkg/log"
)

// RunConcurrencyMutexLockWithSessionRevoke tests the ConcurrencyMutexLockWithSessionRevoke scenario.
func RunConcurrencyMutexLockWithSessionRevoke(runner Runner) {
	logutil.S().Infow("running", "scenario", ConcurrencyMutexLockWithSessionRevoke.String())

	result := &Result{
		Scenario:  ConcurrencyMutexLockWithSessionRevoke.String(),
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

	// create two separate sessions for lock competition
	s1, err := concurrency.NewSession(cli)
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to create session: %v", err)

		return
	}
	defer func() { _ = s1.Close() }()

	s2, err := concurrency.NewSession(cli)
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to create session: %v", err)

		return
	}
	defer func() { _ = s2.Close() }()

	testKey := runner.GenerateRandomKey(10)

	m1 := concurrency.NewMutex(s1, testKey)
	m2 := concurrency.NewMutex(s2, testKey)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err = m1.Lock(ctx); err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to lock: %v", err)

		return
	}

	m2Locked := make(chan error)
	go func() {
		if lockErr := m2.Lock(ctx); !errors.Is(lockErr, rpctypes.ErrLeaseNotFound) && !errors.Is(lockErr, concurrency.ErrSessionExpired) {
			m2Locked <- fmt.Errorf("m2.Lock failed: %w", lockErr)
		} else {
			m2Locked <- nil
		}
	}()

	if err = s2.Close(); err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to close session: %v", err)

		return
	}

	if err = m1.Unlock(ctx); err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to unlock: %v", err)

		return
	}

	select {
	case err = <-m2Locked:
		if err != nil {
			result.Success = false
			result.Output = err.Error()

			return
		}

	case <-time.After(5 * time.Second):
		result.Success = false
		result.Output = "timed out waiting for m2 to release the lock"

		return
	}
}

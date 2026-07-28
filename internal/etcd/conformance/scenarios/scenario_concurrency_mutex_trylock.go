package scenarios

import (
	"context"
	"errors"
	"fmt"
	"time"

	testtime "git.tbd/etcd-infra/internal/etcd/testtime"
	"go.etcd.io/etcd/client/v3/concurrency"

	logutil "git.tbd/etcd-infra/pkg/log"
)

// RunConcurrencyMutexTrylock tests the ConcurrencyMutexTrylock scenario.
func RunConcurrencyMutexTrylock(runner Runner) {
	logutil.S().Infow("running", "scenario", ConcurrencyMutexTrylock.String())

	result := &Result{
		Scenario:  ConcurrencyMutexTrylock.String(),
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

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	m1 := concurrency.NewMutex(s1, testKey)
	if err = m1.Lock(ctx); err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to lock: %v", err)

		return
	}

	m2 := concurrency.NewMutex(s2, testKey)
	if err = m2.TryLock(ctx); !errors.Is(err, concurrency.ErrLocked) {
		result.Success = false
		result.Output = fmt.Sprintf("expected ErrLocked, got %v", err)

		return
	}

	if err = m1.Unlock(ctx); err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to unlock: %v", err)

		return
	}

	if err = m2.TryLock(ctx); err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to trylock: %v", err)

		return
	}
}

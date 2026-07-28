package scenarios

import (
	"context"
	"fmt"
	"time"

	testtime "git.tbd/etcd-infra/internal/etcd/testtime"
	"go.etcd.io/etcd/client/v3/concurrency"

	logutil "git.tbd/etcd-infra/pkg/log"
)

// RunConcurrencyElectionCampaign tests the ConcurrencyElectionCampaign scenario.
func RunConcurrencyElectionCampaign(runner Runner) {
	logutil.S().Infow("running", "scenario", ConcurrencyElectionCampaign.String())

	result := &Result{
		Scenario:  ConcurrencyElectionCampaign.String(),
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

	// create two separate sessions for campaign competition
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

	e1 := concurrency.NewElection(s1, testKey)
	e2 := concurrency.NewElection(s2, testKey)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Step 1: e2 becomes leader
	if err := e2.Campaign(ctx, "e2"); err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("e2.Campaign failed: %v", err)

		return
	}

	select {
	case observed := <-e2.Observe(ctx):
		if string(observed.Kvs[0].Value) != "e2" {
			result.Success = false
			result.Output = fmt.Sprintf("expected e2, got %s", observed.Kvs[0].Value)

			return
		}
	case <-time.After(5 * time.Second):
		result.Success = false
		result.Output = "timed out waiting to observe e2 leadership"

		return
	}

	// Step 2: launch e1 campaign that should block until e2 resigns
	errCh := make(chan error, 1)
	go func() {
		errCh <- e1.Campaign(ctx, "e1")
	}()

	select {
	case err := <-errCh:
		if err == nil {
			result.Success = false
			result.Output = "e1 unexpectedly acquired leadership while e2 held it"
		} else {
			result.Success = false
			result.Output = fmt.Sprintf("e1.Campaign returned early: %v", err)
		}

		return
	case <-time.After(300 * time.Millisecond):
		// expected: still blocked
	}

	// Step 3: resign e2 so e1 can take over
	if err := e2.Resign(ctx); err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("e2.Resign failed: %v", err)

		return
	}

	if err := <-errCh; err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("e1 failed to campaign after e2 resignation: %v", err)

		return
	}

	select {
	case observed := <-e1.Observe(ctx):
		if string(observed.Kvs[0].Value) != "e1" {
			result.Success = false
			result.Output = fmt.Sprintf("expected e1, got %s", observed.Kvs[0].Value)

			return
		}
	case <-time.After(5 * time.Second):
		result.Success = false
		result.Output = "timed out waiting to observe e1 leadership"

		return
	}

	if err := e1.Resign(ctx); err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("e1.Resign failed: %v", err)

		return
	}
}

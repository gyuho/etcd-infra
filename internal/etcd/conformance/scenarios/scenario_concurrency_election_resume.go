package scenarios

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	testtime "git.tbd/etcd-infra/internal/etcd/testtime"
	"go.etcd.io/etcd/client/v3/concurrency"

	logutil "git.tbd/etcd-infra/pkg/log"
)

var errUnexpectedObserverChannel = errors.New("unexpected open observer channel")

// RunConcurrencyElectionResume tests the ConcurrencyElectionResume scenario.
func RunConcurrencyElectionResume(runner Runner) {
	logutil.S().Infow("running", "scenario", ConcurrencyElectionResume.String())

	result := &Result{
		Scenario:  ConcurrencyElectionResume.String(),
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

	s, err := concurrency.NewSession(cli)
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to create session: %v", err)

		return
	}
	defer func() { _ = s.Close() }()

	testKey := runner.GenerateRandomKey(10)
	e := concurrency.NewElection(s, testKey)

	// entire test should never take more than 10 seconds
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// become leader
	if err = e.Campaign(ctx, candidate1); err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to campaign: %v", err)

		return
	}

	elected, err := e.Leader(ctx)
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to get leader: %v", err)

		return
	}
	k, v := string(elected.Kvs[0].Key), string(elected.Kvs[0].Value)
	if !strings.HasPrefix(k, testKey) {
		result.Success = false
		result.Output = fmt.Sprintf("expected key to have prefix %s, got %s", testKey, k)

		return
	}
	if v != candidate1 {
		result.Success = false
		result.Output = fmt.Sprintf("expected value %s, got %s", candidate1, v)
	}

	e = concurrency.ResumeElection(
		s,
		testKey,
		k,
		elected.Kvs[0].CreateRevision,
	)

	rch := make(chan clientResponse)
	go func() {
		observed := e.Observe(ctx)
		rch <- clientResponse{}
		for {
			resp, open := <-observed
			if !open {
				rch <- clientResponse{err: errUnexpectedObserverChannel}

				return
			}

			// skip; same candidate has been elected
			if string(resp.Kvs[0].Value) == "candidate1" {
				continue
			}

			rch <- clientResponse{getResp: resp}

			return
		}
	}()

	// wait until observe goroutine is running
	<-rch

	// put some random data to generate a change event, this put should be
	// ignored by Observe() because it is not under the election prefix
	_, err = cli.Put(ctx, "foo", "bar")
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to put: %v", err)

		return
	}

	// resign as leader
	if resignErr := e.Resign(ctx); resignErr != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to resign: %v", resignErr)

		return
	}

	// become another leader
	if err = e.Campaign(ctx, "candidate2"); err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to campaign: %v", err)

		return
	}

	// expects leader change
	select {
	case resp := <-rch:
		if resp.err != nil {
			result.Success = false
			result.Output = fmt.Sprintf("failed to observe leader change: %v", resp.err)
		}

		k, v := string(resp.getResp.Kvs[0].Key), string(resp.getResp.Kvs[0].Value)
		if !strings.HasPrefix(k, testKey) {
			result.Success = false
			result.Output = fmt.Sprintf("expected key to have prefix %s, got %s", testKey, k)

			return
		}

		if v != "candidate2" {
			result.Success = false
			result.Output = "expected value candidate2, got " + v

			return
		}

	case <-time.After(5 * time.Second):
		result.Success = false
		result.Output = "timed out waiting for leader change"

		return
	}
}

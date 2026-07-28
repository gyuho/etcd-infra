//nolint:testpackage // Tests use package internals and shared resources.
package scenarios

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// failingRunner is a Runner that always returns an error from NewClient.
// It is used to exercise the early-exit error paths in every scenario
// function without needing a live etcd cluster.
type failingRunner struct {
	resultsMu sync.Mutex
	results   Results
}

func (r *failingRunner) RecordResult(rs Result) {
	r.resultsMu.Lock()
	defer r.resultsMu.Unlock()
	r.results = append(r.results, rs)
}

func (r *failingRunner) Results() Results {
	r.resultsMu.Lock()
	defer r.resultsMu.Unlock()
	return append(Results(nil), r.results...)
}

func (r *failingRunner) Cleanup() error { return nil }

func (r *failingRunner) DefaultTimeout() time.Duration { return 5 * time.Second }

func (r *failingRunner) NewCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 5*time.Second)
}

func (r *failingRunner) NewCtxTimeout(timeout time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), timeout)
}

func (r *failingRunner) GenerateRandomKey(n int) string {
	s := make([]byte, n)
	for i := range s {
		s[i] = 'a'
	}
	return "test-" + string(s)
}

func (r *failingRunner) NewClient(_ ...OpOption) (*clientv3.Client, error) {
	return nil, errors.New("injected client error")
}

func (r *failingRunner) NewPerPeerClients(_ ...OpOption) ([]*clientv3.Client, error) {
	return nil, errors.New("injected per-peer client error")
}

// TestAllScenariosHandleClientError verifies that every registered scenario
// function gracefully records a failure (instead of panicking) when NewClient
// returns an error. This exercises the early-exit error path present in
// virtually every RUN_* function.
func TestAllScenariosHandleClientError(t *testing.T) {
	t.Parallel()

	for name, fn := range IDStringToRunnerFunc {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			r := &failingRunner{}
			fn(r)

			results := r.Results()
			require.Len(t, results, 1, "scenario %s must record exactly one result", name)
			assert.False(t, results[0].Success, "scenario %s must record failure when client creation fails", name)
			assert.NotEmpty(t, results[0].Output, "scenario %s must record non-empty output on failure", name)
		})
	}
}

// TestScenariosRecordResultOnClientError is a bulk smoke-test: run every
// scenario with the failing runner and confirm at least one failure result
// is recorded across all of them, with no panics.
func TestScenariosRecordResultOnClientError(t *testing.T) {
	t.Parallel()

	runner := &failingRunner{}
	for _, fn := range IDStringToRunnerFunc {
		fn(runner)
	}

	results := runner.Results()
	// Every scenario must have produced at least one result.
	assert.Len(t, results, len(IDStringToRunnerFunc))
	for _, r := range results {
		assert.False(t, r.Success, "expected all results to be failures")
	}
}

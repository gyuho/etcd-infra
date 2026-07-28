//nolint:testpackage,paralleltest // Tests use package internals and shared resources.
package conformance

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"git.tbd/etcd-infra/internal/etcd/conformance/scenarios"
)

func TestRunnerNewCtxTimeoutUsesDefault(t *testing.T) {
	r := &runner{defaultTimeout: 200 * time.Millisecond}

	ctx, cancel := r.NewCtxTimeout(0)
	defer cancel()

	deadline, ok := ctx.Deadline()
	require.True(t, ok)
	remaining := time.Until(deadline)
	require.Greater(t, remaining, 0*time.Millisecond)
	require.LessOrEqual(t, remaining, r.defaultTimeout)
}

func TestRunnerNewCtxTimeoutCustom(t *testing.T) {
	r := &runner{defaultTimeout: 200 * time.Millisecond}

	ctx, cancel := r.NewCtxTimeout(50 * time.Millisecond)
	defer cancel()

	deadline, ok := ctx.Deadline()
	require.True(t, ok)
	remaining := time.Until(deadline)
	require.Greater(t, remaining, 0*time.Millisecond)
	require.LessOrEqual(t, remaining, 60*time.Millisecond)
}

func TestRunnerGenerateRandomKey(t *testing.T) {
	r := &runner{cfg: Config{TestKeyPrefix: "etcd-infra"}}

	key := r.GenerateRandomKey(8)
	require.True(t, strings.HasPrefix(key, "etcd-infra/"))
	require.Len(t, strings.TrimPrefix(key, "etcd-infra/"), 8)
}

func TestRunnerRecordResults(t *testing.T) {
	r := &runner{}

	r.RecordResult(scenarios.Result{Scenario: "one"})
	r.RecordResult(scenarios.Result{Scenario: "two"})

	results := r.Results()
	require.Len(t, results, 2)
	require.Equal(t, "one", results[0].Scenario)
	require.Equal(t, "two", results[1].Scenario)
}

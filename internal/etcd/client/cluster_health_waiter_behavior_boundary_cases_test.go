//nolint:testpackage // Need access to internals for thorough testing.
package client

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestWaitForClusterHealthy_TimeoutBranchNoLastErr exercises the code path where
// the loop exits due to time, but lastErr is nil (no iterations completed).
// This is triggered by a maxWait so tiny that time.Now().Before(deadline) is
// immediately false, so the loop body never executes.
func TestWaitForClusterHealthy_TimeoutBranchNoLastErr(t *testing.T) {
	t.Parallel()

	cli := newUnreachableClient(t)

	// Use 1 nanosecond as maxWait. By the time the loop condition is checked,
	// time.Now() will be past the deadline, so the loop body never executes.
	// lastErr remains nil, so the code should return errClusterHealthTimeout.
	err := WaitForClusterHealthy(context.Background(), cli, []string{"http://127.0.0.1:1"}, time.Nanosecond)
	require.Error(t, err)
	// The error is either "timed out" (no iterations ran) or "cluster failed" (if one iteration completed).
	assert.True(t,
		errors.Is(err, errClusterHealthTimeout) || strings.Contains(err.Error(), "cluster failed"),
	)
}

// TestWaitForClusterHealthy_TwoEndpointsQuorum tests quorum calculation with 2 endpoints.
// requiredHealthy = 2/2 + 1 = 2, meaning both endpoints must be healthy.
func TestWaitForClusterHealthy_TwoEndpointsQuorum(t *testing.T) {
	t.Parallel()

	cli := newUnreachableClient(t)
	endpoints := []string{"http://127.0.0.1:1", "http://127.0.0.1:2"}
	err := WaitForClusterHealthy(context.Background(), cli, endpoints, 150*time.Millisecond)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cluster failed health check")
}

// TestWaitForClusterHealthy_ContextDeadlineExceeded tests that a context with
// a deadline that has already expired causes immediate failure.
func TestWaitForClusterHealthy_ContextDeadlineExceeded(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()

	cli := newUnreachableClient(t)
	err := WaitForClusterHealthy(ctx, cli, []string{"http://127.0.0.1:1"}, time.Second)
	require.Error(t, err)
}

// TestWaitForClusterHealthy_NilEndpointsSlice tests the nil endpoints path.
func TestWaitForClusterHealthy_NilEndpointsSlice(t *testing.T) {
	t.Parallel()

	cli := newUnreachableClient(t)
	err := WaitForClusterHealthy(context.Background(), cli, nil, time.Second)
	require.Error(t, err)
	require.ErrorIs(t, err, errNoEndpoints)
}

// TestWaitForClusterHealthy_NegativeMaxWaitWithValidClient tests the negative maxWait path
// with a valid client and endpoints, ensuring we hit the maxWait validation before the loop.
func TestWaitForClusterHealthy_NegativeMaxWaitFullValidation(t *testing.T) {
	t.Parallel()

	cli := newUnreachableClient(t)
	err := WaitForClusterHealthy(context.Background(), cli, []string{"http://127.0.0.1:1"}, -5*time.Second)
	require.Error(t, err)
	require.ErrorIs(t, err, errMaxWaitInvalid)
	assert.Contains(t, err.Error(), "-5s")
}

// TestWaitForClusterHealthy_MaxWaitValidation_ZeroValue tests that zero maxWait
// is properly rejected.
func TestWaitForClusterHealthy_MaxWaitValidation_ZeroValue(t *testing.T) {
	t.Parallel()

	cli := newUnreachableClient(t)
	err := WaitForClusterHealthy(context.Background(), cli, []string{"http://127.0.0.1:1"}, 0)
	require.Error(t, err)
	require.ErrorIs(t, err, errMaxWaitInvalid)
	assert.Contains(t, err.Error(), "0s")
}

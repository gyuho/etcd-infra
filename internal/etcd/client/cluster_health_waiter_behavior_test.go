//nolint:all // Coverage-oriented tests intentionally use broad patterns for mock-heavy branch testing.
//nolint:testpackage // Tests use package internals and shared resources.
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

func TestWaitForClusterHealthy_NegativeMaxWaitWithClient(t *testing.T) {
	t.Parallel()

	cli := newUnreachableClient(t)
	err := WaitForClusterHealthy(context.Background(), cli, []string{"http://127.0.0.1:1"}, -time.Second)
	require.Error(t, err)
	require.ErrorIs(t, err, errMaxWaitInvalid)
	assert.Contains(t, err.Error(), "maxWait must be greater than 0")
}

func TestErrorVariables(t *testing.T) {
	t.Parallel()

	require.Error(t, errClientNil)
	require.Error(t, errNoEndpoints)
	require.Error(t, errMaxWaitInvalid)
	require.Error(t, errClusterHealthTimeout)
	assert.Contains(t, errClientNil.Error(), "nil")
	assert.Contains(t, errNoEndpoints.Error(), "no endpoints")
	assert.Contains(t, errMaxWaitInvalid.Error(), "maxWait")
	assert.Contains(t, errClusterHealthTimeout.Error(), "timed out")
}

func TestQuorumConstants(t *testing.T) {
	t.Parallel()

	assert.Equal(t, 2, quorumDivisor)
	assert.Equal(t, 1, quorumOffset)
}

func TestWaitForClusterHealthy_TimeoutNoLastErr(t *testing.T) {
	t.Parallel()

	// Use a very short timeout with a client that has endpoints
	// but the client will reach the timeout before polling
	cli := newUnreachableClient(t)

	// With a tiny timeout, the loop should run at least once and lastErr will be set
	// But we're testing the timeout error message specifically
	err := WaitForClusterHealthy(context.Background(), cli, []string{"http://127.0.0.1:1"}, 100*time.Millisecond)
	require.Error(t, err)
	// Should contain either "cluster failed" or "timed out"
	assert.True(t,
		errors.Is(err, errClusterHealthTimeout) || strings.Contains(err.Error(), "cluster failed"),
	)
}

func TestWaitForClusterHealthy_MultipleUnhealthyEndpoints(t *testing.T) {
	t.Parallel()

	cli := newUnreachableClient(t)
	endpoints := []string{"http://127.0.0.1:1", "http://127.0.0.1:2", "http://127.0.0.1:3"}
	err := WaitForClusterHealthy(context.Background(), cli, endpoints, 200*time.Millisecond)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cluster failed health check")
}

func TestWaitForClusterHealthy_CanceledContext(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	cli := newUnreachableClient(t)
	err := WaitForClusterHealthy(ctx, cli, []string{"http://127.0.0.1:1"}, time.Second)
	// Should fail because context is canceled and Status calls will fail
	require.Error(t, err)
}

// TestWaitForClusterHealthy_ContextCancelledDuringSleep verifies that cancelling
// the parent context interrupts the poll sleep rather than blocking for the full
// DefaultClusterHealthPollInterval. This exercises the select{} path added to
// replace time.Sleep.
func TestWaitForClusterHealthy_ContextCancelledDuringSleep(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cli := newUnreachableClient(t)

	// Cancel after one poll iteration completes (the Status call fails fast
	// because the dialer is blocked), so the select picks up ctx.Done().
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	err := WaitForClusterHealthy(ctx, cli, []string{"http://127.0.0.1:1"}, 10*time.Second)
	elapsed := time.Since(start)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "interrupted")
	// Should return well before the 10-second maxWait.
	assert.Less(t, elapsed, 2*time.Second, "context cancellation should interrupt the poll sleep")
}

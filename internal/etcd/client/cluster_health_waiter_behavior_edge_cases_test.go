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

func TestWaitForClusterHealthy_EmptySliceEndpoints(t *testing.T) {
	t.Parallel()

	cli := newUnreachableClient(t)
	err := WaitForClusterHealthy(context.Background(), cli, []string{}, time.Second)
	require.Error(t, err)
	require.ErrorIs(t, err, errNoEndpoints)
}

func TestWaitForClusterHealthy_ZeroMaxWaitWithClient(t *testing.T) {
	t.Parallel()

	cli := newUnreachableClient(t)
	err := WaitForClusterHealthy(context.Background(), cli, []string{"http://127.0.0.1:1"}, 0)
	require.Error(t, err)
	require.ErrorIs(t, err, errMaxWaitInvalid)
}

func TestWaitForClusterHealthy_SingleEndpointQuorum(t *testing.T) {
	t.Parallel()

	// Single endpoint: requiredHealthy = 1/2 + 1 = 1
	// With unreachable endpoint, should fail with lastErr set
	cli := newUnreachableClient(t)
	err := WaitForClusterHealthy(context.Background(), cli, []string{"http://127.0.0.1:1"}, 100*time.Millisecond)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cluster failed health check")
}

func TestWaitForClusterHealthy_FiveEndpointsQuorum(t *testing.T) {
	t.Parallel()

	// Five endpoints: requiredHealthy = 5/2 + 1 = 3
	cli := newUnreachableClient(t)
	endpoints := []string{
		"http://127.0.0.1:1",
		"http://127.0.0.1:2",
		"http://127.0.0.1:3",
		"http://127.0.0.1:4",
		"http://127.0.0.1:5",
	}
	err := WaitForClusterHealthy(context.Background(), cli, endpoints, 100*time.Millisecond)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cluster failed health check")
}

func TestWaitForClusterHealthy_VeryShortTimeout(t *testing.T) {
	t.Parallel()

	// Test with 1ms timeout - the loop body may not even execute once
	// because the Status call takes longer than the deadline
	cli := newUnreachableClient(t)
	err := WaitForClusterHealthy(context.Background(), cli, []string{"http://127.0.0.1:1"}, time.Millisecond)
	require.Error(t, err)
	// The error is either "cluster failed" (lastErr set) or "timed out" (loop body never ran).
	assert.True(t,
		errors.Is(err, errClusterHealthTimeout) || strings.Contains(err.Error(), "cluster failed"),
	)
}

func TestQuorumCalculation(t *testing.T) {
	t.Parallel()

	// Quorum formula: len(endpoints)/2 + 1 (simple majority).
	// Endpoints are validated non-empty before this calculation runs.
	assert.Equal(t, 1, 1/quorumDivisor+quorumOffset)
	assert.Equal(t, 2, 2/quorumDivisor+quorumOffset)
	assert.Equal(t, 2, 3/quorumDivisor+quorumOffset)
	assert.Equal(t, 3, 4/quorumDivisor+quorumOffset)
	assert.Equal(t, 3, 5/quorumDivisor+quorumOffset)
}

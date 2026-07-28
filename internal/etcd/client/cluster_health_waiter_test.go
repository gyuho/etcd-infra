//nolint:testpackage // Tests use package internals and shared resources.
package client

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	clientv3 "go.etcd.io/etcd/client/v3"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

var errDialBlockedForTests = errors.New("dial blocked for tests")

func newUnreachableClient(t *testing.T) *clientv3.Client {
	t.Helper()

	dialer := func(_ context.Context, _ string) (net.Conn, error) {
		return nil, errDialBlockedForTests
	}
	cli, err := clientv3.New(clientv3.Config{
		Endpoints:   []string{"http://127.0.0.1:1"},
		DialTimeout: 50 * time.Millisecond,
		DialOptions: []grpc.DialOption{
			grpc.WithContextDialer(dialer),
			grpc.WithTransportCredentials(insecure.NewCredentials()),
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = cli.Close() })

	return cli
}

func TestWaitForClusterHealthy_NilClient(t *testing.T) {
	t.Parallel()

	err := WaitForClusterHealthy(context.Background(), nil, []string{"http://localhost:2379"}, time.Second)
	require.Error(t, err)
	require.Contains(t, err.Error(), "client is nil")
}

func TestWaitForClusterHealthy_NoEndpoints(t *testing.T) {
	t.Parallel()

	// We can't easily create a real etcd client without a server,
	// but we can test the error conditions
	err := WaitForClusterHealthy(context.Background(), nil, []string{}, time.Second)
	require.Error(t, err)
	require.Contains(t, err.Error(), "client is nil")
}

func TestWaitForClusterHealthy_NoEndpointsWithClient(t *testing.T) {
	t.Parallel()

	cli := newUnreachableClient(t)
	err := WaitForClusterHealthy(context.Background(), cli, nil, time.Second)
	require.Error(t, err)
	require.Contains(t, err.Error(), "no endpoints")
}

func TestWaitForClusterHealthy_ZeroMaxWait(t *testing.T) {
	t.Parallel()

	err := WaitForClusterHealthy(context.Background(), nil, []string{"http://localhost:2379"}, 0)
	require.Error(t, err)
	require.Contains(t, err.Error(), "client is nil")
}

func TestWaitForClusterHealthy_NegativeMaxWait(t *testing.T) {
	t.Parallel()

	err := WaitForClusterHealthy(context.Background(), nil, []string{"http://localhost:2379"}, -time.Second)
	require.Error(t, err)
	require.Contains(t, err.Error(), "client is nil")
}

func TestWaitForClusterHealthy_InvalidMaxWaitWithClient(t *testing.T) {
	t.Parallel()

	cli := newUnreachableClient(t)
	err := WaitForClusterHealthy(context.Background(), cli, []string{"http://127.0.0.1:1"}, 0)
	require.Error(t, err)
	require.Contains(t, err.Error(), "maxWait must be greater than 0")
}

func TestWaitForClusterHealthy_UnhealthyEndpoints(t *testing.T) {
	t.Parallel()

	cli := newUnreachableClient(t)
	err := WaitForClusterHealthy(context.Background(), cli, []string{"http://127.0.0.1:1"}, 250*time.Millisecond)
	require.Error(t, err)
	require.Contains(t, err.Error(), "cluster failed health check")
}

func TestDefaultConstants(t *testing.T) {
	t.Parallel()

	require.Equal(t, 200*time.Millisecond, DefaultClusterHealthPollInterval)
	require.Equal(t, 2*time.Second, DefaultClusterHealthStatusTimeout)
}

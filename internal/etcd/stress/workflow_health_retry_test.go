//nolint:testpackage,paralleltest // Mockey patches global functions
package stress

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	clientv3 "go.etcd.io/etcd/client/v3"

	"git.tbd/etcd-infra/internal/etcd/stress/scenarios"
)

// mockStressRunner is a test double for scenarios.StressRunner.
type mockStressRunner struct {
	newClientFunc func() (*clientv3.Client, error)
}

func (m *mockStressRunner) NewClient(_ ...scenarios.StressOpOption) (*clientv3.Client, error) {
	if m.newClientFunc != nil {
		return m.newClientFunc()
	}
	return &clientv3.Client{}, nil
}

func (m *mockStressRunner) NewCtxTimeout(timeout time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), timeout)
}

func (m *mockStressRunner) NewPerPeerClients(_ ...scenarios.StressOpOption) ([]*clientv3.Client, error) {
	return nil, nil
}

func (m *mockStressRunner) RecordResult(_ scenarios.Result) {
}

func (m *mockStressRunner) Results() scenarios.StressResults {
	return nil
}

func (m *mockStressRunner) Cleanup() error {
	return nil
}

func (m *mockStressRunner) NewCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 30*time.Second)
}

func (m *mockStressRunner) GenerateRandomKey(_ int) string {
	return "test-key"
}

func (m *mockStressRunner) GetMetricsCollector() *scenarios.MetricsCollector {
	return nil
}

func (m *mockStressRunner) GetLoadGenerator() scenarios.LoadGenerator {
	return nil
}

func (m *mockStressRunner) GetConfig() scenarios.StressConfig {
	return scenarios.StressConfig{}
}

// TestWaitForClusterHealth_NewClientError tests error creating client.
func TestWaitForClusterHealth_NewClientError(t *testing.T) {
	t.Parallel()

	mockRunner := &mockStressRunner{
		newClientFunc: func() (*clientv3.Client, error) {
			return nil, errors.New("connection refused")
		},
	}

	err := waitForClusterHealth(mockRunner, 100*time.Millisecond)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cluster not healthy")
	assert.Contains(t, err.Error(), "connection refused")
}

// TestWaitForClusterHealth_RetryOnClientError tests retry behavior.
func TestWaitForClusterHealth_RetryOnClientError(t *testing.T) {
	t.Parallel()

	attemptCount := 0
	mockRunner := &mockStressRunner{
		newClientFunc: func() (*clientv3.Client, error) {
			attemptCount++
			return nil, errors.New("connection refused")
		},
	}

	err := waitForClusterHealth(mockRunner, 3*time.Second)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "cluster not healthy")
	assert.Contains(t, err.Error(), "connection refused")

	// Function sleeps 2 seconds between retries, so with a 3s timeout,
	// it should make 2 attempts (initial + 1 retry after 2s sleep)
	assert.GreaterOrEqual(t, attemptCount, 1, "should attempt at least once")
	assert.LessOrEqual(t, attemptCount, 2, "with 3s timeout should not exceed 2 attempts")
}

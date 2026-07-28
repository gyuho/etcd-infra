package client

import (
	"context"
	"errors"
	"fmt"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"
)

const (
	// DefaultClusterHealthPollInterval is the wait between health probes.
	DefaultClusterHealthPollInterval = 200 * time.Millisecond
	// DefaultClusterHealthStatusTimeout is the timeout per endpoint status request.
	DefaultClusterHealthStatusTimeout = 2 * time.Second
	quorumDivisor                     = 2
	quorumOffset                      = 1
)

var (
	errClientNil            = errors.New("client is nil")
	errNoEndpoints          = errors.New("no endpoints provided for health check")
	errMaxWaitInvalid       = errors.New("maxWait must be greater than 0")
	errClusterHealthTimeout = errors.New("cluster health check timed out")
)

// WaitForClusterHealthy checks that a quorum of endpoints responds to Status within the given maxWait.
func WaitForClusterHealthy(ctx context.Context, cli *clientv3.Client, endpoints []string, maxWait time.Duration) error {
	if cli == nil {
		return errClientNil
	}
	if len(endpoints) == 0 {
		return errNoEndpoints
	}
	if maxWait <= 0 {
		return fmt.Errorf("%w: %s", errMaxWaitInvalid, maxWait)
	}

	deadline := time.Now().Add(maxWait)
	requiredHealthy := len(endpoints)/quorumDivisor + quorumOffset

	var lastErr error
	for time.Now().Before(deadline) {
		healthy := 0
		for _, ep := range endpoints {
			statusCtx, cancel := context.WithTimeout(ctx, DefaultClusterHealthStatusTimeout)
			_, err := cli.Status(statusCtx, ep)
			cancel()
			if err == nil {
				healthy++
			} else {
				lastErr = err
			}
		}

		if healthy >= requiredHealthy {
			return nil
		}

		// Sleep between polls, but respect context cancellation and deadline.
		select {
		case <-time.After(DefaultClusterHealthPollInterval):
		case <-ctx.Done():
			return fmt.Errorf("cluster health check interrupted: %w", ctx.Err())
		}
	}

	if lastErr != nil {
		return fmt.Errorf("cluster failed health check: %w", lastErr)
	}

	return fmt.Errorf("%w after %s", errClusterHealthTimeout, maxWait)
}

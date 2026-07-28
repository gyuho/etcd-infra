package scenarios

import (
	"context"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"
)

// Runner executes etcd conformance scenarios against a live cluster.
type Runner interface {
	// Records the result of the scenario.
	RecordResult(result Result)

	// Returns the current scenario results.
	Results() Results

	// Cleans up all the test data.
	Cleanup() error

	// Returns the default timeout for operations.
	// This is useful for scenarios that need to set timers (e.g., time.After)
	// with appropriate timeouts for cross-datacenter/VPN environments.
	DefaultTimeout() time.Duration

	// Creates a default context with a default timeout.
	NewCtx() (context.Context, context.CancelFunc)

	// Creates a context with a specified timeout.
	NewCtxTimeout(timeout time.Duration) (context.Context, context.CancelFunc)

	// Generates a test key with a random suffix of the length n.
	GenerateRandomKey(n int) string

	// Creates a single client with all cluster endpoints.
	NewClient(opts ...OpOption) (*clientv3.Client, error)

	// Creates a client for each peer.
	NewPerPeerClients(opts ...OpOption) ([]*clientv3.Client, error)
}

// Op holds etcd client connection options for scenario execution.
type Op struct {
	DialTimeout        time.Duration
	MaxCallSendMsgSize int
	MaxCallRecvMsgSize int
}

// OpOption configures an Op via functional options.
type OpOption func(*Op)

// ApplyOpts applies all options and sets defaults for unset fields.
func (o *Op) ApplyOpts(opts []OpOption) {
	for _, opt := range opts {
		opt(o)
	}
	if o.DialTimeout <= 0 {
		o.DialTimeout = 5 * time.Second
	}
}

// WithDialTimeout sets the gRPC dial timeout for etcd client connections.
func WithDialTimeout(d time.Duration) OpOption {
	return func(o *Op) { o.DialTimeout = d }
}

// WithMaxCallSendMsgSize sets the maximum gRPC send message size in bytes.
func WithMaxCallSendMsgSize(size int) OpOption {
	return func(o *Op) { o.MaxCallSendMsgSize = size }
}

// WithMaxCallRecvMsgSize sets the maximum gRPC receive message size in bytes.
func WithMaxCallRecvMsgSize(size int) OpOption {
	return func(o *Op) { o.MaxCallRecvMsgSize = size }
}

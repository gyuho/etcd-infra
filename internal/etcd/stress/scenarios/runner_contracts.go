package scenarios

import (
	"context"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"
)

// StressRunner mirrors conformance Runner interface exactly.
type StressRunner interface {
	// Records the result of the scenario - same as conformance
	RecordResult(result Result)

	// Returns the current scenario results - same as conformance
	Results() StressResults

	// Cleans up all the test data - same as conformance
	Cleanup() error

	// Creates a default context with a default timeout - same as conformance
	NewCtx() (context.Context, context.CancelFunc)

	// Creates a context with a specified timeout - same as conformance
	NewCtxTimeout(timeout time.Duration) (context.Context, context.CancelFunc)

	// Generates a test key with a random suffix of the length n - same as conformance
	GenerateRandomKey(n int) string

	// Creates a single client created with all client endpoints - same as conformance
	NewClient(opts ...StressOpOption) (*clientv3.Client, error)

	// Creates a client for each peer - same as conformance
	NewPerPeerClients(opts ...StressOpOption) ([]*clientv3.Client, error)

	// Stress-specific additions
	GetMetricsCollector() *MetricsCollector
	GetLoadGenerator() LoadGenerator
	GetConfig() StressConfig
}

// StressOp mirrors conformance Op pattern exactly.
type StressOp struct {
	DialTimeout        time.Duration
	MaxCallSendMsgSize int
	MaxCallRecvMsgSize int
	// Stress-specific additions
	RateLimitPerSecond int
	MaxConcurrency     int
}

// StressOpOption mirrors conformance OpOption pattern exactly.
type StressOpOption func(*StressOp)

// ApplyOpts applies all options and sets defaults for unset fields.
func (o *StressOp) ApplyOpts(opts []StressOpOption) {
	for _, opt := range opts {
		opt(o)
	}
	// Same default pattern as conformance
	if o.DialTimeout <= 0 {
		o.DialTimeout = 5 * time.Second
	}
	if o.MaxConcurrency <= 0 {
		o.MaxConcurrency = 100
	}
}

// WithDialTimeout sets the dial timeout option for stress operations, following the same pattern as conformance.
func WithDialTimeout(d time.Duration) StressOpOption {
	return func(o *StressOp) { o.DialTimeout = d }
}

// WithMaxCallSendMsgSize sets the maximum gRPC send message size in bytes.
func WithMaxCallSendMsgSize(size int) StressOpOption {
	return func(o *StressOp) { o.MaxCallSendMsgSize = size }
}

// WithMaxCallRecvMsgSize sets the maximum gRPC receive message size in bytes.
func WithMaxCallRecvMsgSize(size int) StressOpOption {
	return func(o *StressOp) { o.MaxCallRecvMsgSize = size }
}

// WithRateLimit sets the maximum requests per second for stress load generation.
func WithRateLimit(rps int) StressOpOption {
	return func(o *StressOp) { o.RateLimitPerSecond = rps }
}

// WithMaxConcurrency sets the maximum number of concurrent stress workers.
func WithMaxConcurrency(maxConcurrency int) StressOpOption {
	return func(o *StressOp) { o.MaxConcurrency = maxConcurrency }
}

// StressConfig holds stress-specific configuration.
type StressConfig struct {
	DurationSeconds   int
	ConcurrentWorkers int
	RequestsPerSecond int
	KeySizeBytes      int
	ValueSizeBytes    int
}

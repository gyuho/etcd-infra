//nolint:testpackage // Need access to internals for thorough testing.
package scenarios

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestStressOpApplyOptsDefaults_Coverage(t *testing.T) {
	t.Parallel()

	op := &StressOp{}
	op.ApplyOpts(nil)
	assert.Equal(t, 5*time.Second, op.DialTimeout)
	assert.Equal(t, 100, op.MaxConcurrency)
}

func TestStressOpApplyOptsWithOptions(t *testing.T) {
	t.Parallel()

	op := &StressOp{}
	op.ApplyOpts([]StressOpOption{
		WithDialTimeout(10 * time.Second),
		WithMaxCallSendMsgSize(4096),
		WithMaxCallRecvMsgSize(8192),
		WithRateLimit(500),
		WithMaxConcurrency(50),
	})
	assert.Equal(t, 10*time.Second, op.DialTimeout)
	assert.Equal(t, 4096, op.MaxCallSendMsgSize)
	assert.Equal(t, 8192, op.MaxCallRecvMsgSize)
	assert.Equal(t, 500, op.RateLimitPerSecond)
	assert.Equal(t, 50, op.MaxConcurrency)
}

func TestStressOpApplyOptsNegativeTimeout(t *testing.T) {
	t.Parallel()

	op := &StressOp{}
	op.ApplyOpts([]StressOpOption{
		WithDialTimeout(-1 * time.Second),
	})
	assert.Equal(t, 5*time.Second, op.DialTimeout)
}

func TestStressOpApplyOptsNegativeConcurrency(t *testing.T) {
	t.Parallel()

	op := &StressOp{}
	op.ApplyOpts([]StressOpOption{
		WithMaxConcurrency(-5),
	})
	assert.Equal(t, 100, op.MaxConcurrency)
}

func TestWithDialTimeoutStress(t *testing.T) {
	t.Parallel()

	opt := WithDialTimeout(30 * time.Second)
	op := &StressOp{}
	opt(op)
	assert.Equal(t, 30*time.Second, op.DialTimeout)
}

func TestWithMaxCallSendMsgSizeStress(t *testing.T) {
	t.Parallel()

	opt := WithMaxCallSendMsgSize(1024)
	op := &StressOp{}
	opt(op)
	assert.Equal(t, 1024, op.MaxCallSendMsgSize)
}

func TestWithMaxCallRecvMsgSizeStress(t *testing.T) {
	t.Parallel()

	opt := WithMaxCallRecvMsgSize(2048)
	op := &StressOp{}
	opt(op)
	assert.Equal(t, 2048, op.MaxCallRecvMsgSize)
}

func TestWithRateLimit(t *testing.T) {
	t.Parallel()

	opt := WithRateLimit(1000)
	op := &StressOp{}
	opt(op)
	assert.Equal(t, 1000, op.RateLimitPerSecond)
}

func TestWithMaxConcurrency(t *testing.T) {
	t.Parallel()

	opt := WithMaxConcurrency(200)
	op := &StressOp{}
	opt(op)
	assert.Equal(t, 200, op.MaxConcurrency)
}

func TestStressConfigStruct(t *testing.T) {
	t.Parallel()

	cfg := StressConfig{
		DurationSeconds:   60,
		ConcurrentWorkers: 10,
		RequestsPerSecond: 100,
		KeySizeBytes:      64,
		ValueSizeBytes:    256,
	}
	assert.Equal(t, 60, cfg.DurationSeconds)
	assert.Equal(t, 10, cfg.ConcurrentWorkers)
	assert.Equal(t, 100, cfg.RequestsPerSecond)
	assert.Equal(t, 64, cfg.KeySizeBytes)
	assert.Equal(t, 256, cfg.ValueSizeBytes)
}

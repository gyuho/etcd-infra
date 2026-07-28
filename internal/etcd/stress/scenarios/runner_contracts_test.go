//nolint:testpackage // Tests use package internals and shared resources.
package scenarios

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStressOpApplyOptsDefaults(t *testing.T) {
	t.Parallel()
	// When no options provided, defaults should be applied
	op := &StressOp{}
	op.ApplyOpts(nil)

	assert.Equal(t, 5*time.Second, op.DialTimeout, "default dial timeout should be 5s")
	assert.Equal(t, 100, op.MaxConcurrency, "default max concurrency should be 100")
}

func TestStressOpApplyOptsWithDialTimeout(t *testing.T) {
	t.Parallel()
	op := &StressOp{}
	op.ApplyOpts([]StressOpOption{WithDialTimeout(10 * time.Second)})

	assert.Equal(t, 10*time.Second, op.DialTimeout)
	// MaxConcurrency should still get default
	assert.Equal(t, 100, op.MaxConcurrency)
}

func TestStressOpApplyOptsWithMaxCallSendMsgSize(t *testing.T) {
	t.Parallel()
	op := &StressOp{}
	op.ApplyOpts([]StressOpOption{WithMaxCallSendMsgSize(1024 * 1024)})

	assert.Equal(t, 1024*1024, op.MaxCallSendMsgSize)
}

func TestStressOpApplyOptsWithMaxCallRecvMsgSize(t *testing.T) {
	t.Parallel()
	op := &StressOp{}
	op.ApplyOpts([]StressOpOption{WithMaxCallRecvMsgSize(2 * 1024 * 1024)})

	assert.Equal(t, 2*1024*1024, op.MaxCallRecvMsgSize)
}

func TestStressOpApplyOptsWithRateLimit(t *testing.T) {
	t.Parallel()
	op := &StressOp{}
	op.ApplyOpts([]StressOpOption{WithRateLimit(500)})

	assert.Equal(t, 500, op.RateLimitPerSecond)
}

func TestStressOpApplyOptsWithMaxConcurrency(t *testing.T) {
	t.Parallel()
	op := &StressOp{}
	op.ApplyOpts([]StressOpOption{WithMaxConcurrency(50)})

	assert.Equal(t, 50, op.MaxConcurrency)
}

func TestStressOpApplyOptsMultipleOptions(t *testing.T) {
	t.Parallel()
	// Test applying multiple options at once
	op := &StressOp{}
	op.ApplyOpts([]StressOpOption{
		WithDialTimeout(15 * time.Second),
		WithMaxCallSendMsgSize(512 * 1024),
		WithMaxCallRecvMsgSize(1024 * 1024),
		WithRateLimit(1000),
		WithMaxConcurrency(200),
	})

	assert.Equal(t, 15*time.Second, op.DialTimeout)
	assert.Equal(t, 512*1024, op.MaxCallSendMsgSize)
	assert.Equal(t, 1024*1024, op.MaxCallRecvMsgSize)
	assert.Equal(t, 1000, op.RateLimitPerSecond)
	assert.Equal(t, 200, op.MaxConcurrency)
}

func TestStressOpApplyOptsZeroDialTimeout(t *testing.T) {
	t.Parallel()
	// When dial timeout is explicitly set to 0 (or not set), default should apply
	op := &StressOp{}
	op.ApplyOpts([]StressOpOption{WithDialTimeout(0)})

	// 0 should trigger the default
	assert.Equal(t, 5*time.Second, op.DialTimeout)
}

func TestStressOpApplyOptsNegativeDialTimeout(t *testing.T) {
	t.Parallel()
	// Negative dial timeout should trigger default
	op := &StressOp{}
	op.ApplyOpts([]StressOpOption{WithDialTimeout(-1 * time.Second)})

	assert.Equal(t, 5*time.Second, op.DialTimeout)
}

func TestStressOpApplyOptsZeroMaxConcurrency(t *testing.T) {
	t.Parallel()
	// When max concurrency is explicitly set to 0, default should apply
	op := &StressOp{}
	op.ApplyOpts([]StressOpOption{WithMaxConcurrency(0)})

	assert.Equal(t, 100, op.MaxConcurrency)
}

func TestStressOpApplyOptsNegativeMaxConcurrency(t *testing.T) {
	t.Parallel()
	// Negative max concurrency should trigger default
	op := &StressOp{}
	op.ApplyOpts([]StressOpOption{WithMaxConcurrency(-1)})

	assert.Equal(t, 100, op.MaxConcurrency)
}

func TestStressConfigFields(t *testing.T) {
	t.Parallel()
	// Test that StressConfig struct fields work correctly
	cfg := StressConfig{
		DurationSeconds:   60,
		ConcurrentWorkers: 10,
		RequestsPerSecond: 100,
		KeySizeBytes:      64,
		ValueSizeBytes:    256,
	}

	require.Equal(t, 60, cfg.DurationSeconds)
	require.Equal(t, 10, cfg.ConcurrentWorkers)
	require.Equal(t, 100, cfg.RequestsPerSecond)
	require.Equal(t, 64, cfg.KeySizeBytes)
	require.Equal(t, 256, cfg.ValueSizeBytes)
}

//nolint:testpackage // Tests use package internals and shared resources.
package scenarios

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestOpApplyOptsDefaults(t *testing.T) {
	t.Parallel()
	// When no options provided, defaults should be applied
	op := &Op{}
	op.ApplyOpts(nil)

	assert.Equal(t, 5*time.Second, op.DialTimeout, "default dial timeout should be 5s")
}

func TestOpApplyOptsWithDialTimeout(t *testing.T) {
	t.Parallel()
	op := &Op{}
	op.ApplyOpts([]OpOption{WithDialTimeout(10 * time.Second)})

	assert.Equal(t, 10*time.Second, op.DialTimeout)
}

func TestOpApplyOptsWithMaxCallSendMsgSize(t *testing.T) {
	t.Parallel()
	op := &Op{}
	op.ApplyOpts([]OpOption{WithMaxCallSendMsgSize(1024 * 1024)})

	assert.Equal(t, 1024*1024, op.MaxCallSendMsgSize)
	// DialTimeout should get default
	assert.Equal(t, 5*time.Second, op.DialTimeout)
}

func TestOpApplyOptsWithMaxCallRecvMsgSize(t *testing.T) {
	t.Parallel()
	op := &Op{}
	op.ApplyOpts([]OpOption{WithMaxCallRecvMsgSize(2 * 1024 * 1024)})

	assert.Equal(t, 2*1024*1024, op.MaxCallRecvMsgSize)
}

func TestOpApplyOptsMultipleOptions(t *testing.T) {
	t.Parallel()
	// Test applying multiple options at once
	op := &Op{}
	op.ApplyOpts([]OpOption{
		WithDialTimeout(15 * time.Second),
		WithMaxCallSendMsgSize(512 * 1024),
		WithMaxCallRecvMsgSize(1024 * 1024),
	})

	assert.Equal(t, 15*time.Second, op.DialTimeout)
	assert.Equal(t, 512*1024, op.MaxCallSendMsgSize)
	assert.Equal(t, 1024*1024, op.MaxCallRecvMsgSize)
}

func TestOpApplyOptsZeroDialTimeout(t *testing.T) {
	t.Parallel()
	// When dial timeout is explicitly set to 0, default should apply
	op := &Op{}
	op.ApplyOpts([]OpOption{WithDialTimeout(0)})

	// 0 should trigger the default
	assert.Equal(t, 5*time.Second, op.DialTimeout)
}

func TestOpApplyOptsNegativeDialTimeout(t *testing.T) {
	t.Parallel()
	// Negative dial timeout should trigger default
	op := &Op{}
	op.ApplyOpts([]OpOption{WithDialTimeout(-1 * time.Second)})

	assert.Equal(t, 5*time.Second, op.DialTimeout)
}

func TestOpApplyOptsEmptySlice(t *testing.T) {
	t.Parallel()
	// Empty slice should still apply defaults
	op := &Op{}
	op.ApplyOpts([]OpOption{})

	assert.Equal(t, 5*time.Second, op.DialTimeout)
}

func TestOpApplyOptsPreservesExistingValues(t *testing.T) {
	t.Parallel()
	// Test that only specified options are overwritten
	op := &Op{
		DialTimeout:        20 * time.Second,
		MaxCallSendMsgSize: 100,
		MaxCallRecvMsgSize: 200,
	}

	// Only apply MaxCallSendMsgSize
	op.ApplyOpts([]OpOption{WithMaxCallSendMsgSize(500)})

	// DialTimeout should remain unchanged since it was already > 0
	assert.Equal(t, 20*time.Second, op.DialTimeout)
	// MaxCallSendMsgSize should be updated
	assert.Equal(t, 500, op.MaxCallSendMsgSize)
	// MaxCallRecvMsgSize should remain unchanged
	assert.Equal(t, 200, op.MaxCallRecvMsgSize)
}

func TestOpStructFields(t *testing.T) {
	t.Parallel()
	// Test that Op struct fields work correctly
	op := Op{
		DialTimeout:        30 * time.Second,
		MaxCallSendMsgSize: 4 * 1024 * 1024,
		MaxCallRecvMsgSize: 8 * 1024 * 1024,
	}

	assert.Equal(t, 30*time.Second, op.DialTimeout)
	assert.Equal(t, 4*1024*1024, op.MaxCallSendMsgSize)
	assert.Equal(t, 8*1024*1024, op.MaxCallRecvMsgSize)
}

func TestWithDialTimeoutOption(t *testing.T) {
	t.Parallel()
	// Test that the option function works correctly in isolation
	opt := WithDialTimeout(7 * time.Second)
	op := &Op{}
	opt(op)

	assert.Equal(t, 7*time.Second, op.DialTimeout)
}

func TestWithMaxCallSendMsgSizeOption(t *testing.T) {
	t.Parallel()
	// Test that the option function works correctly in isolation
	opt := WithMaxCallSendMsgSize(123456)
	op := &Op{}
	opt(op)

	assert.Equal(t, 123456, op.MaxCallSendMsgSize)
}

func TestWithMaxCallRecvMsgSizeOption(t *testing.T) {
	t.Parallel()
	// Test that the option function works correctly in isolation
	opt := WithMaxCallRecvMsgSize(654321)
	op := &Op{}
	opt(op)

	assert.Equal(t, 654321, op.MaxCallRecvMsgSize)
}

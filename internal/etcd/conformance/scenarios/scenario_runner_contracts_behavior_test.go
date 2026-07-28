//nolint:testpackage // Need access to internals for thorough testing.
package scenarios

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestOpApplyOptsDefaultsFromNil(t *testing.T) {
	t.Parallel()

	op := &Op{}
	op.ApplyOpts(nil)
	assert.Equal(t, 5*time.Second, op.DialTimeout, "default DialTimeout should be 5s")
	assert.Equal(t, 0, op.MaxCallSendMsgSize)
	assert.Equal(t, 0, op.MaxCallRecvMsgSize)
}

func TestOpApplyOptsCombinedOptions(t *testing.T) {
	t.Parallel()

	op := &Op{}
	op.ApplyOpts([]OpOption{
		WithDialTimeout(10 * time.Second),
		WithMaxCallSendMsgSize(4096),
		WithMaxCallRecvMsgSize(8192),
	})
	assert.Equal(t, 10*time.Second, op.DialTimeout)
	assert.Equal(t, 4096, op.MaxCallSendMsgSize)
	assert.Equal(t, 8192, op.MaxCallRecvMsgSize)
}

func TestOpApplyOptsNegativeTimeoutFallsBack(t *testing.T) {
	t.Parallel()

	op := &Op{}
	op.ApplyOpts([]OpOption{
		WithDialTimeout(-1 * time.Second),
	})
	assert.Equal(t, 5*time.Second, op.DialTimeout, "negative timeout should use default")
}

func TestOpApplyOptsZeroTimeoutFallsBack(t *testing.T) {
	t.Parallel()

	op := &Op{}
	op.ApplyOpts([]OpOption{
		WithDialTimeout(0),
	})
	assert.Equal(t, 5*time.Second, op.DialTimeout, "zero timeout should use default")
}

func TestWithDialTimeoutDirectApply(t *testing.T) {
	t.Parallel()

	opt := WithDialTimeout(30 * time.Second)
	op := &Op{}
	opt(op)
	assert.Equal(t, 30*time.Second, op.DialTimeout)
}

func TestWithMaxCallSendMsgSizeDirectApply(t *testing.T) {
	t.Parallel()

	opt := WithMaxCallSendMsgSize(1024)
	op := &Op{}
	opt(op)
	assert.Equal(t, 1024, op.MaxCallSendMsgSize)
}

func TestWithMaxCallRecvMsgSizeDirectApply(t *testing.T) {
	t.Parallel()

	opt := WithMaxCallRecvMsgSize(2048)
	op := &Op{}
	opt(op)
	assert.Equal(t, 2048, op.MaxCallRecvMsgSize)
}

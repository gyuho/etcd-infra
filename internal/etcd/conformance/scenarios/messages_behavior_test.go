//nolint:testpackage // Need access to internals for thorough testing.
package scenarios

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestMessageConstants(t *testing.T) {
	t.Parallel()

	// Verify message constants are non-empty strings.
	assert.NotEmpty(t, watchCreateFailedMsg)
	assert.NotEmpty(t, watchChannelClosedMsg)
	assert.NotEmpty(t, watchChannelCanceledMsg)
	assert.NotEmpty(t, watchChannelNotClosedAfterCancelMsg)
	assert.NotEmpty(t, watchChannelCanceledAfterContextMsg)
	assert.NotEmpty(t, watchChannelNotClosedAfterWatcherMsg)
	assert.NotEmpty(t, watchChannelShouldNotBlockMsg)
	assert.NotEmpty(t, watchChannelShouldNotBeCreatedMsg)
	assert.NotEmpty(t, watchChannelCanceledContextCreateMsg)
	assert.NotEmpty(t, watchChannelCanceledContextCancelMsg)
	assert.NotEmpty(t, watchChannelCanceledCreatedMsg)
	assert.NotEmpty(t, watchChannelDidNotReceiveEventsMsg)
	assert.NotEmpty(t, memberAddLearnerErrorMsg)
	assert.NotEmpty(t, watchChannelClosedUnexpectedlyMsg)
	assert.NotEmpty(t, candidate1)
	assert.NotEmpty(t, lockTimeoutMsg)
	assert.NotEmpty(t, watchEventTimeoutDeleteMsg)
	assert.NotEmpty(t, watchEventTimeoutMsg)
	assert.NotEmpty(t, syncResponseTimeoutMsg)
	assert.NotEmpty(t, syncErrorChannelTimeoutMsg)
	assert.NotEmpty(t, watchChannelClosedMsg2)
	assert.NotEmpty(t, noPeerClientsMsg)
	assert.NotEmpty(t, valueV1)
	assert.NotEmpty(t, valueV2)
	assert.NotEmpty(t, leasingNewValue)
}

func TestLeasingValueBarConstant(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "bar", leasingValueBar)
}

package testtime

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestParseSlowPathMultiplier(t *testing.T) {
	t.Parallel()

	assert.Equal(t, 1.0, parseSlowPathMultiplier(""))
	assert.Equal(t, 2.0, parseSlowPathMultiplier("2"))
	assert.Equal(t, 1.5, parseSlowPathMultiplier("1.5"))
	// Invalid or tightening values fall back to no scaling.
	assert.Equal(t, 1.0, parseSlowPathMultiplier("abc"))
	assert.Equal(t, 1.0, parseSlowPathMultiplier("0.5"))
	assert.Equal(t, 1.0, parseSlowPathMultiplier("0"))
}

func TestScaleDurationDefault(t *testing.T) {
	t.Parallel()
	// No env set in tests: identity.
	assert.Equal(t, 90*time.Second, ScaleDuration(90*time.Second))
}

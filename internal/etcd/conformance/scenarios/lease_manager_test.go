//nolint:all // Coverage-oriented tests intentionally use broad patterns for mock-heavy branch testing.
//nolint:testpackage // Tests use package internals and shared resources.
package scenarios

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewTestLeaseManagerDefaults(t *testing.T) {
	t.Parallel()
	lm := newTestLeaseManager(nil, 60, 0.1, 10)
	require.NotNil(t, lm)
	assert.Equal(t, int64(60), lm.leaseReuseDurationSeconds)
	assert.Equal(t, 0.1, lm.leaseReuseDurationPercent)
	assert.Equal(t, int64(10), lm.leaseMaxAttachedObjectCnt)
}

func TestNewTestLeaseManagerZeroMaxObject(t *testing.T) {
	t.Parallel()
	// When maxObjectCount <= 0, it should default to 1
	lm := newTestLeaseManager(nil, 60, 0.1, 0)
	require.NotNil(t, lm)
	assert.Equal(t, int64(1), lm.leaseMaxAttachedObjectCnt)

	lm2 := newTestLeaseManager(nil, 60, 0.1, -5)
	require.NotNil(t, lm2)
	assert.Equal(t, int64(1), lm2.leaseMaxAttachedObjectCnt)
}

func TestReuseDurationSecondsLocked(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name            string
		reuseSeconds    int64
		reusePercent    float64
		ttl             int64
		expectedSeconds int64
	}{
		{
			name:            "percent smaller than max",
			reuseSeconds:    60,
			reusePercent:    0.1,
			ttl:             100,
			expectedSeconds: 10, // 0.1 * 100 = 10, min(10, 60) = 10
		},
		{
			name:            "percent larger than max",
			reuseSeconds:    5,
			reusePercent:    0.5,
			ttl:             100,
			expectedSeconds: 5, // 0.5 * 100 = 50, min(50, 5) = 5
		},
		{
			name:            "zero percent",
			reuseSeconds:    60,
			reusePercent:    0.0,
			ttl:             100,
			expectedSeconds: 0, // 0.0 * 100 = 0, min(0, 60) = 0
		},
		{
			name:            "zero ttl",
			reuseSeconds:    60,
			reusePercent:    0.5,
			ttl:             0,
			expectedSeconds: 0, // 0.5 * 0 = 0, min(0, 60) = 0
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			lm := newTestLeaseManager(nil, tt.reuseSeconds, tt.reusePercent, 10)
			result := lm.reuseDurationSecondsLocked(tt.ttl)
			assert.Equal(t, tt.expectedSeconds, result)
		})
	}
}

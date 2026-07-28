//nolint:testpackage // Tests use package internals and shared resources.
package scenarios

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestIDStringKnownValues(t *testing.T) {
	t.Parallel()
	tests := []struct {
		id       ID
		expected string
	}{
		{PutEmptyKeyShouldError, "PUT_EMPTY_KEY_SHOULD_ERROR"},
		{PutLargeShouldError, "PUT_LARGE_SHOULD_ERROR"},
		{Compact, "COMPACT"},
		{TxnPutOne, "TXN_PUT_ONE"},
		{WatchAndPut, "WATCH_AND_PUT"},
		{MaintenanceHashKv, "MAINTENANCE_HASH_KV"},
	}
	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.expected, tt.id.String())
		})
	}
}

func TestIDStringOutOfRange(t *testing.T) {
	t.Parallel()
	// Negative ID should fallback to numeric representation
	negID := ID(-1)
	assert.Contains(t, negID.String(), "ID(-1)")

	// ID way above max should fallback to numeric representation
	hugeID := ID(9999)
	assert.Contains(t, hugeID.String(), "ID(9999)")
}

func TestIDStringAllIDsRoundTrip(t *testing.T) {
	t.Parallel()
	ids, names := ListAllIDs()
	for i, id := range ids {
		t.Run(names[i], func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, names[i], id.String())
		})
	}
}

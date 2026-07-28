//nolint:testpackage // Need access to internals for thorough testing.
package scenarios

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestListAllIDs(t *testing.T) {
	t.Parallel()

	ids, ss := ListAllIDs()
	require.NotEmpty(t, ids, "should have scenario IDs")
	require.NotEmpty(t, ss, "should have scenario strings")

	// Verify PutEmptyKeyShouldError is first (iota=0)
	assert.Equal(t, PutEmptyKeyShouldError, ids[0])

	// Check all IDs are unique
	seen := make(map[ID]bool)
	for _, id := range ids {
		assert.False(t, seen[id], "duplicate ID: %v", id)
		seen[id] = true
	}
}

func TestValidID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		id       string
		expected bool
	}{
		{"valid PUT", "PUT_EMPTY_KEY_SHOULD_ERROR", true},
		{"valid WATCH", "WATCH_AND_PUT", true},
		{"valid TXN", "TXN_PUT_ONE", true},
		{"valid COMPACT", "COMPACT", true},
		{"invalid empty", "", false},
		{"invalid unknown", "NONEXISTENT_SCENARIO_XYZ", false},
		{"invalid substring PUT", "PUT", false},
		{"invalid substring WATCH", "WATCH", false},
		{"invalid partial prefix", "PUT_EMPTY", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.expected, ValidID(tt.id))
		})
	}
}

func TestIDStringToIDMapConsistency(t *testing.T) {
	t.Parallel()

	// Every entry in IDStringToRunnerFunc should also be in IDStringToID
	for k := range IDStringToRunnerFunc {
		_, exists := IDStringToID[k]
		assert.True(t, exists, "IDStringToRunnerFunc key %q not in IDStringToID", k)
	}
}

func TestIDToRunnerMapNotEmpty(t *testing.T) {
	t.Parallel()

	assert.NotEmpty(t, IDToRunner, "IDToRunner should not be empty")
}

func TestIDStringerNotEmpty(t *testing.T) {
	t.Parallel()

	// Test that String() returns non-empty for known IDs
	assert.NotEmpty(t, PutEmptyKeyShouldError.String())
	assert.NotEmpty(t, WatchAndPut.String())
	assert.NotEmpty(t, TxnPutOne.String())
}

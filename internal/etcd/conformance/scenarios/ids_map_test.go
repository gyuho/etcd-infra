//nolint:testpackage // Tests use package internals and shared resources.
package scenarios

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIDStringToIDMapComplete(t *testing.T) {
	t.Parallel()
	ids, names := ListAllIDs()
	require.Len(t, IDStringToID, len(ids), "IDStringToID map size mismatch with ListAllIDs")
	for i, name := range names {
		id, ok := IDStringToID[name]
		require.True(t, ok, "IDStringToID missing key: %s", name)
		assert.Equal(t, ids[i], id, "IDStringToID[%s] mismatch", name)
	}
}

func TestIDStringToRunnerFuncMapComplete(t *testing.T) {
	t.Parallel()
	ids, names := ListAllIDs()
	require.Len(t, IDStringToRunnerFunc, len(ids), "IDStringToRunnerFunc map size mismatch")
	for _, name := range names {
		fn, ok := IDStringToRunnerFunc[name]
		require.True(t, ok, "IDStringToRunnerFunc missing key: %s", name)
		assert.NotNil(t, fn, "IDStringToRunnerFunc[%s] is nil", name)
	}
}

func TestIDToRunnerMapComplete(t *testing.T) {
	t.Parallel()
	ids, _ := ListAllIDs()
	require.Len(t, IDToRunner, len(ids), "IDToRunner map size mismatch")
	for _, id := range ids {
		fn, ok := IDToRunner[id]
		require.True(t, ok, "IDToRunner missing key: %v", id)
		assert.NotNil(t, fn, "IDToRunner[%v] is nil", id)
	}
}

func TestMapsConsistency(t *testing.T) {
	t.Parallel()
	// Verify that IDStringToID and IDStringToRunnerFunc have the same keys
	for name := range IDStringToID {
		_, ok := IDStringToRunnerFunc[name]
		assert.True(t, ok, "IDStringToRunnerFunc missing key that IDStringToID has: %s", name)
	}
	for name := range IDStringToRunnerFunc {
		_, ok := IDStringToID[name]
		assert.True(t, ok, "IDStringToID missing key that IDStringToRunnerFunc has: %s", name)
	}
}

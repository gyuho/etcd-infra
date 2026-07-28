//nolint:testpackage // Need access to internals for thorough testing.
package scenarios

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStressListAllIDs(t *testing.T) {
	t.Parallel()

	ids, ss := ListAllIDs()
	require.NotEmpty(t, ids)
	require.NotEmpty(t, ss)
	assert.Len(t, ss, len(ids))

	// First ID should be StressID(0)
	assert.Equal(t, StressID(0), ids[0])
}

func TestValidStressIDValid(t *testing.T) {
	t.Parallel()

	// Use the first entry from the generated map
	for k := range StressIDStringToID {
		assert.True(t, ValidStressID(k), "should be valid: %s", k)
		break
	}
}

func TestValidStressIDInvalid(t *testing.T) {
	t.Parallel()

	assert.False(t, ValidStressID("NONEXISTENT_SCENARIO_XYZ"))
	assert.False(t, ValidStressID(""))
}

func TestStressIDStringToIDMapNotEmpty(t *testing.T) {
	t.Parallel()

	assert.NotEmpty(t, StressIDStringToID)
}

func TestStressIDStringToRunnerFuncMapNotEmpty(t *testing.T) {
	t.Parallel()

	assert.NotEmpty(t, StressIDStringToRunnerFunc)
}

func TestStressIDToRunnerMapNotEmpty(t *testing.T) {
	t.Parallel()

	assert.NotEmpty(t, StressIDToRunner)
}

func TestStressIDMapConsistency(t *testing.T) {
	t.Parallel()

	for k := range StressIDStringToRunnerFunc {
		_, exists := StressIDStringToID[k]
		assert.True(t, exists, "StressIDStringToRunnerFunc key %q not in StressIDStringToID", k)
	}
}

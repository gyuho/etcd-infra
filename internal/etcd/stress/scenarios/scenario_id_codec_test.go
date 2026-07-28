//nolint:testpackage // Tests use package internals and shared resources.
package scenarios

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestListAllIDsAndValidStressID(t *testing.T) {
	t.Parallel()
	ids, names := ListAllIDs()
	require.NotEmpty(t, ids)
	require.NotEmpty(t, names)
	require.Len(t, names, len(ids))

	require.True(t, ValidStressID(names[0]))
	require.False(t, ValidStressID("UNKNOWN_ID"))
}

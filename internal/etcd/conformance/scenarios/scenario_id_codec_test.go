//nolint:testpackage // Tests use package internals and shared resources.
package scenarios

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestListAllIDsAndValidID(t *testing.T) {
	t.Parallel()
	ids, names := ListAllIDs()
	require.NotEmpty(t, ids)
	require.NotEmpty(t, names)
	require.Len(t, names, len(ids))

	// Every listed name must be a valid ID.
	for _, name := range names {
		require.True(t, ValidID(name), "expected ValidID(%q) to be true", name)
	}

	// Substrings must not pass validation.
	require.False(t, ValidID("UNKNOWN_ID"))
	require.False(t, ValidID(""))
	require.False(t, ValidID("PUT"))
}

package installer

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolveLatestVersion(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/latest" {
			http.Redirect(w, r, "/tag/v3.7.1", http.StatusFound)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)

	version, err := resolveLatestVersion(context.Background(), server.Client(), server.URL+"/latest")
	require.NoError(t, err)
	assert.Equal(t, "v3.7.1", version)
}

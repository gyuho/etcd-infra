//nolint:testpackage // Need access to internals for thorough testing.
package scenarios

import (
	"path"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCreateKVWithPrefix(t *testing.T) {
	t.Parallel()

	kv := createKV("/prefix", "mykey", "myval")
	assert.Equal(t, path.Join("/prefix", "mykey"), kv.k)
	assert.Equal(t, "myval", kv.v)
}

func TestCreateKVEmptyPrefixPath(t *testing.T) {
	t.Parallel()

	kv := createKV("", "key", "val")
	assert.Equal(t, "key", kv.k)
	assert.Equal(t, "val", kv.v)
}

func TestKeyValueStructFields(t *testing.T) {
	t.Parallel()

	kv := keyValue{k: "foo", v: "bar"}
	assert.Equal(t, "foo", kv.k)
	assert.Equal(t, "bar", kv.v)
}

func TestClientResponseStructFields(t *testing.T) {
	t.Parallel()

	cr := clientResponse{
		key:             "test-key",
		getRevRequested: 42,
		err:             nil,
	}
	assert.Equal(t, "test-key", cr.key)
	assert.Equal(t, int64(42), cr.getRevRequested)
	require.NoError(t, cr.err)
}

//nolint:testpackage // Tests use package internals and shared resources.
package scenarios

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEncodeContinueTokenRoundTrip(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		revision int64
		key      []byte
	}{
		{"simple key", 42, []byte("mykey")},
		{"revision 0", 0, []byte("key")},
		{"large revision", 999999999, []byte("some/prefix/key")},
		{"single char key", 1, []byte("k")},
		{"numeric key", 10, []byte("12345")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			token := encodeContinueToken(tt.revision, tt.key)
			require.NotEmpty(t, token)

			gotRev, gotKey, err := decodeContinueToken(token)
			require.NoError(t, err)
			assert.Equal(t, tt.revision, gotRev)
			assert.Equal(t, tt.key, gotKey)
		})
	}
}

func TestDecodeContinueTokenInvalidEncoding(t *testing.T) {
	t.Parallel()
	_, _, err := decodeContinueToken("not-base64!!!")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid continue token encoding")
}

func TestDecodeContinueTokenInvalidFormat(t *testing.T) {
	t.Parallel()
	// Valid base64 but invalid format inside
	_, _, err := decodeContinueToken("aGVsbG8=") // "hello" in base64
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid continue token format")
}

func TestDecodeContinueTokenInvalidKeyEncoding(t *testing.T) {
	t.Parallel()
	// Outer base64 decodes to "42/!!!!", valid format but inner key is not valid base64.
	// base64("42/!!!!") = "NDIvISEhIQ=="
	_, _, err := decodeContinueToken("NDIvISEhIQ==")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid continue token key encoding")
}

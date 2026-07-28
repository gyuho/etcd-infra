package compute

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIsNotSupported(t *testing.T) {
	t.Parallel()

	require.True(t, IsNotSupported(ErrNotSupported))
	require.True(t, IsNotSupported(NotSupportedError("aws", "CopyFile")))
	require.False(t, IsNotSupported(errors.New("other error")))
	require.False(t, IsNotSupported(nil))
}

func TestNotSupportedError(t *testing.T) {
	t.Parallel()

	t.Run("both provider and operation", func(t *testing.T) {
		t.Parallel()
		err := NotSupportedError("aws", "WriteFile")
		require.ErrorIs(t, err, ErrNotSupported)
		assert.Contains(t, err.Error(), "aws")
		assert.Contains(t, err.Error(), "WriteFile")
	})

	t.Run("both empty", func(t *testing.T) {
		t.Parallel()
		err := NotSupportedError("", "")
		require.Same(t, ErrNotSupported, err, "should return sentinel directly when both args empty")
	})

	t.Run("provider only", func(t *testing.T) {
		t.Parallel()
		err := NotSupportedError("hetzner", "")
		require.ErrorIs(t, err, ErrNotSupported)
		assert.Contains(t, err.Error(), "hetzner")
	})

	t.Run("operation only", func(t *testing.T) {
		t.Parallel()
		err := NotSupportedError("", "DownloadFile")
		require.ErrorIs(t, err, ErrNotSupported)
		assert.Contains(t, err.Error(), "DownloadFile")
	})
}

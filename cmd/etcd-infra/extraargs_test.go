package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseExtraArgsFile(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "args")
	content := `# private-branch experiment flags
--snapshot-count=10

--log-level=info
--experimental-flag-with-spaces=a b c
`
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))

	args, err := parseExtraArgsFile(path)
	require.NoError(t, err)
	assert.Equal(t, []string{
		"--snapshot-count=10",
		"--log-level=info",
		"--experimental-flag-with-spaces=a b c",
	}, args)
}

func TestParseExtraArgsFileRejectsNonFlagLine(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "args")
	require.NoError(t, os.WriteFile(path, []byte("--snapshot-count=10\nnot-a-flag\n"), 0o600))

	_, err := parseExtraArgsFile(path)
	require.ErrorContains(t, err, "line 2")
	require.ErrorContains(t, err, "must be a single flag argument")
}

func TestParseExtraArgsFileMissing(t *testing.T) {
	t.Parallel()

	_, err := parseExtraArgsFile(filepath.Join(t.TempDir(), "does-not-exist"))
	require.ErrorContains(t, err, "read extra args file")
}

func TestResolveExtraArgsEmpty(t *testing.T) {
	t.Parallel()

	args, err := resolveExtraArgs("", "")
	require.NoError(t, err)
	assert.Empty(t, args)

	args, err = resolveExtraArgs("", "  ")
	require.NoError(t, err)
	assert.Empty(t, args, "whitespace-only file path must be ignored")
}

func TestResolveExtraArgsFileThenInline(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "args")
	require.NoError(t, os.WriteFile(path, []byte("--from-file=1\n"), 0o600))

	args, err := resolveExtraArgs("--inline=2 --other=3", path)
	require.NoError(t, err)
	// File entries come first so a repeated inline flag overrides them.
	assert.Equal(t, []string{"--from-file=1", "--inline=2", "--other=3"}, args)
}

func TestResolveExtraArgsPropagatesFileError(t *testing.T) {
	t.Parallel()

	_, err := resolveExtraArgs("--inline=2", filepath.Join(t.TempDir(), "missing"))
	require.ErrorContains(t, err, "read extra args file")
}

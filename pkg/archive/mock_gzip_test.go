package archive

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/bytedance/mockey"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGzipFile_IOCopyError(t *testing.T) {
	// Cannot run in parallel: patches io.Copy via mockey.
	defer mockey.Mock(io.Copy).To(func(_ io.Writer, _ io.Reader) (int64, error) {
		return 0, errors.New("copy error")
	}).Build().UnPatch()

	dir := t.TempDir()
	srcPath := filepath.Join(dir, "source.txt")
	gzPath := filepath.Join(dir, "source.txt.gz")

	require.NoError(t, os.WriteFile(srcPath, []byte("test data"), 0o644)) //nolint:gosec // Intentional permission/operation

	err := GzipFile(srcPath, gzPath)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "compress")
}

func TestGunzipFile_IOCopyError(t *testing.T) {
	// Cannot run in parallel: patches io.Copy via mockey.

	dir := t.TempDir()
	srcPath := filepath.Join(dir, "source.txt")
	gzPath := filepath.Join(dir, "source.txt.gz")
	dstPath := filepath.Join(dir, "dest.txt")

	// Create a valid gzip file first
	require.NoError(t, os.WriteFile(srcPath, []byte("test data for gunzip"), 0o644)) //nolint:gosec // Intentional permission/operation
	require.NoError(t, GzipFile(srcPath, gzPath))

	// Patch io.Copy to fail during gunzip.
	defer mockey.Mock(io.Copy).To(func(_ io.Writer, _ io.Reader) (int64, error) {
		return 0, errors.New("copy error")
	}).Build().UnPatch()

	err := GunzipFile(gzPath, dstPath)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "decompress")
}

func TestGunzipFile_InvalidGzipHeader(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	badGzPath := filepath.Join(dir, "bad.gz")
	dstPath := filepath.Join(dir, "dest.txt")

	// Write a file that looks like gzip but has invalid data
	badData := make([]byte, 0, 37)
	badData = append(badData, 0x1f, 0x8b, 0x08, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0xff)
	badData = append(badData, []byte("this is not valid gzip data")...)
	require.NoError(t, os.WriteFile(badGzPath, badData, 0o644)) //nolint:gosec // Intentional permission/operation

	err := GunzipFile(badGzPath, dstPath)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "decompress")
}

func TestDecompressDirectory_GunzipError(t *testing.T) {
	// Cannot run in parallel - mocks global function

	dir := t.TempDir()

	// Create a file that will be detected as gzip-compressed
	srcPath := filepath.Join(dir, "binary")
	require.NoError(t, os.WriteFile(srcPath, []byte("test"), 0o755)) //nolint:gosec // Intentional permission/operation
	gzPath := srcPath + ".gz"
	require.NoError(t, GzipFile(srcPath, gzPath))
	require.NoError(t, os.Rename(gzPath, srcPath))

	// Mock GunzipFile to return an error
	mocker := mockey.Mock(GunzipFile).Return(errors.New("gunzip failed")).Build()
	defer mocker.UnPatch()

	err := DecompressDirectory(dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "decompress")
}

func TestDecompressDirectory_ChmodError(t *testing.T) {
	// Cannot run in parallel - mocks os.Chmod

	dir := t.TempDir()

	// Create a gzip-compressed file
	srcPath := filepath.Join(dir, "binary")
	require.NoError(t, os.WriteFile(srcPath, []byte("test"), 0o755)) //nolint:gosec // Intentional permission/operation
	gzPath := srcPath + ".gz"
	require.NoError(t, GzipFile(srcPath, gzPath))
	require.NoError(t, os.Rename(gzPath, srcPath))

	// Mock os.Chmod to fail
	mocker := mockey.Mock(os.Chmod).Return(errors.New("chmod failed")).Build()
	defer mocker.UnPatch()

	err := DecompressDirectory(dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "chmod")
}

func TestDecompressDirectory_RenameError(t *testing.T) {
	// Cannot run in parallel - mocks os.Rename

	dir := t.TempDir()

	// Create a gzip-compressed file
	srcPath := filepath.Join(dir, "binary")
	require.NoError(t, os.WriteFile(srcPath, []byte("test"), 0o755)) //nolint:gosec // Intentional permission/operation
	gzPath := srcPath + ".gz"
	require.NoError(t, GzipFile(srcPath, gzPath))
	require.NoError(t, os.Rename(gzPath, srcPath))

	// Mock os.Rename to fail
	mocker := mockey.Mock(os.Rename).Return(errors.New("rename failed")).Build()
	defer mocker.UnPatch()

	err := DecompressDirectory(dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "rename")
}

func TestGzipReader_OpenError(t *testing.T) {
	t.Parallel()

	_, err := GzipReader("/nonexistent/file")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "open")
}

func TestGunzipFile_OpenError(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	dstPath := filepath.Join(dir, "dest.txt")

	err := GunzipFile("/nonexistent/file.gz", dstPath)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "open")
}

func TestGunzipFile_CreateError(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	srcPath := filepath.Join(dir, "source.txt")
	gzPath := filepath.Join(dir, "source.txt.gz")

	// Create a valid gzip file
	require.NoError(t, os.WriteFile(srcPath, []byte("test"), 0o644)) //nolint:gosec // Intentional permission/operation
	require.NoError(t, GzipFile(srcPath, gzPath))

	// Try to write to an invalid destination (directory without write permission)
	badDir := filepath.Join(dir, "readonly")
	require.NoError(t, os.Mkdir(badDir, 0o444)) //nolint:gosec // Intentional permission/operation
	dstPath := filepath.Join(badDir, "dest.txt")

	err := GunzipFile(gzPath, dstPath)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "create")
}

func TestGzipFile_CreateError(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	srcPath := filepath.Join(dir, "source.txt")
	require.NoError(t, os.WriteFile(srcPath, []byte("test"), 0o644)) //nolint:gosec // Intentional permission/operation

	// Try to write to an invalid destination
	badDir := filepath.Join(dir, "readonly")
	require.NoError(t, os.Mkdir(badDir, 0o444)) //nolint:gosec // Intentional permission/operation
	gzPath := filepath.Join(badDir, "dest.gz")

	err := GzipFile(srcPath, gzPath)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "create")
}

func TestGunzipFile_GzipReaderError(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	badPath := filepath.Join(dir, "notgzip.gz")
	dstPath := filepath.Join(dir, "dest.txt")

	// Write non-gzip data
	require.NoError(t, os.WriteFile(badPath, []byte("not gzip data"), 0o644)) //nolint:gosec // Intentional permission/operation

	err := GunzipFile(badPath, dstPath)
	require.Error(t, err)
	// The error should mention decompression failure
}

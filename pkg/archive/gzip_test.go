package archive

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGzipGunzipRoundTrip(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	srcPath := filepath.Join(dir, "original")
	gzPath := filepath.Join(dir, "original.gz")
	dstPath := filepath.Join(dir, "restored")

	// Create a test file with known content.
	original := []byte("Hello, this is test data for gzip round-trip compression.\n")
	require.NoError(t, os.WriteFile(srcPath, original, 0o644)) //nolint:gosec // Intentional permission/operation

	// Compress.
	require.NoError(t, GzipFile(srcPath, gzPath))

	// Verify the compressed file exists and is smaller than original (or at least exists).
	info, err := os.Stat(gzPath)
	require.NoError(t, err)
	assert.Positive(t, info.Size())

	// Decompress.
	require.NoError(t, GunzipFile(gzPath, dstPath))

	// Verify round-trip.
	restored, err := os.ReadFile(dstPath)
	require.NoError(t, err)
	assert.Equal(t, original, restored)
}

func TestGzipFile_LargeData(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	srcPath := filepath.Join(dir, "large")
	gzPath := filepath.Join(dir, "large.gz")
	dstPath := filepath.Join(dir, "large-restored")

	// Create a larger file (~1MB of repeated data).
	data := make([]byte, 1<<20)
	for i := range data {
		data[i] = byte(i % 256)
	}
	require.NoError(t, os.WriteFile(srcPath, data, 0o644)) //nolint:gosec // Intentional permission/operation

	require.NoError(t, GzipFile(srcPath, gzPath))

	// Compressed should be smaller than original for repeated data.
	srcInfo, _ := os.Stat(srcPath)
	gzInfo, _ := os.Stat(gzPath)
	assert.Less(t, gzInfo.Size(), srcInfo.Size())

	require.NoError(t, GunzipFile(gzPath, dstPath))

	restored, err := os.ReadFile(dstPath)
	require.NoError(t, err)
	assert.Equal(t, data, restored)
}

func TestGzipReader(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	srcPath := filepath.Join(dir, "file")
	data := []byte("streaming gzip content test")
	require.NoError(t, os.WriteFile(srcPath, data, 0o644)) //nolint:gosec // Intentional permission/operation

	reader, err := GzipReader(srcPath)
	require.NoError(t, err)
	defer func() { _ = reader.Close() }()

	// Read all compressed bytes.
	compressed, err := io.ReadAll(reader)
	require.NoError(t, err)
	assert.NotEmpty(t, compressed)

	// Write compressed bytes to a file and decompress to verify.
	gzPath := filepath.Join(dir, "file.gz")
	require.NoError(t, os.WriteFile(gzPath, compressed, 0o644)) //nolint:gosec // Intentional permission/operation

	dstPath := filepath.Join(dir, "file-restored")
	require.NoError(t, GunzipFile(gzPath, dstPath))

	restored, err := os.ReadFile(dstPath)
	require.NoError(t, err)
	assert.Equal(t, data, restored)
}

func TestGzipFile_NonExistentSource(t *testing.T) {
	t.Parallel()

	err := GzipFile("/nonexistent/file", "/tmp/out.gz")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "open source")
}

func TestGunzipFile_NonExistentSource(t *testing.T) {
	t.Parallel()

	err := GunzipFile("/nonexistent/file.gz", "/tmp/out")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "open source")
}

func TestGzipReader_NonExistentSource(t *testing.T) {
	t.Parallel()

	_, err := GzipReader("/nonexistent/file")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "open")
}

func TestIsAlreadyCompressed(t *testing.T) {
	t.Parallel()

	tests := []struct {
		path     string
		expected bool
	}{
		{"file.tar.gz", true},
		{"file.gz", true},
		{"file.tgz", true},
		{"file.zip", true},
		{"file.bz2", true},
		{"file.xz", true},
		{"file.zst", true},
		{"file.tar", true},
		{"etcd", false},
		{"file.bin", false},
		{"FILE.GZ", true},
		{"file.TAR.GZ", true},
	}

	for _, tc := range tests {
		t.Run(tc.path, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.expected, IsAlreadyCompressed(tc.path))
		})
	}
}

func TestGzipFile_InvalidDestination(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	srcPath := filepath.Join(dir, "source")
	require.NoError(t, os.WriteFile(srcPath, []byte("test"), 0o644)) //nolint:gosec // Intentional permission/operation

	// Try to create gzipped file in a non-existent directory
	err := GzipFile(srcPath, "/nonexistent/directory/file.gz")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "create destination")
}

func TestGunzipFile_InvalidDestination(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	srcPath := filepath.Join(dir, "source")
	gzPath := filepath.Join(dir, "source.gz")

	// Create a valid gzipped file
	require.NoError(t, os.WriteFile(srcPath, []byte("test"), 0o644)) //nolint:gosec // Intentional permission/operation
	require.NoError(t, GzipFile(srcPath, gzPath))

	// Try to decompress to a non-existent directory
	err := GunzipFile(gzPath, "/nonexistent/directory/file")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "create destination")
}

func TestGunzipFile_InvalidGzipData(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	badGzPath := filepath.Join(dir, "bad.gz")
	dstPath := filepath.Join(dir, "output")

	// Create a file with invalid gzip data
	require.NoError(t, os.WriteFile(badGzPath, []byte("not gzip data"), 0o644)) //nolint:gosec // Intentional permission/operation

	// Should fail to create gzip reader
	err := GunzipFile(badGzPath, dstPath)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "create gzip reader")
}

func TestGzipReader_ReadError(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	srcPath := filepath.Join(dir, "file")

	// Create a small test file
	require.NoError(t, os.WriteFile(srcPath, []byte("test"), 0o644)) //nolint:gosec // Intentional permission/operation

	reader, err := GzipReader(srcPath)
	require.NoError(t, err)

	// Read some data to verify it works
	buf := make([]byte, 10)
	n, err := reader.Read(buf)
	require.NoError(t, err)
	assert.Positive(t, n)

	// Close the reader
	err = reader.Close()
	require.NoError(t, err)

	// Try to read after close - should fail
	_, err = reader.Read(buf)
	require.Error(t, err)
}

func TestGzipFile_EmptyFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	srcPath := filepath.Join(dir, "empty")
	gzPath := filepath.Join(dir, "empty.gz")
	dstPath := filepath.Join(dir, "empty-restored")

	// Create an empty file
	require.NoError(t, os.WriteFile(srcPath, []byte{}, 0o644)) //nolint:gosec // Intentional permission/operation

	// Compress empty file
	require.NoError(t, GzipFile(srcPath, gzPath))

	// Verify compressed file exists
	info, err := os.Stat(gzPath)
	require.NoError(t, err)
	assert.Positive(t, info.Size())

	// Decompress
	require.NoError(t, GunzipFile(gzPath, dstPath))

	// Verify result is empty
	restored, err := os.ReadFile(dstPath)
	require.NoError(t, err)
	assert.Empty(t, restored)
}

func TestGzipReader_EmptyFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	srcPath := filepath.Join(dir, "empty")

	// Create an empty file
	require.NoError(t, os.WriteFile(srcPath, []byte{}, 0o644)) //nolint:gosec // Intentional permission/operation

	reader, err := GzipReader(srcPath)
	require.NoError(t, err)
	defer func() { _ = reader.Close() }()

	// Read compressed data
	compressed, err := io.ReadAll(reader)
	require.NoError(t, err)
	assert.NotEmpty(t, compressed)

	// Verify we can decompress it back
	gzPath := filepath.Join(dir, "empty.gz")
	require.NoError(t, os.WriteFile(gzPath, compressed, 0o644)) //nolint:gosec // Intentional permission/operation

	dstPath := filepath.Join(dir, "restored")
	require.NoError(t, GunzipFile(gzPath, dstPath))

	restored, err := os.ReadFile(dstPath)
	require.NoError(t, err)
	assert.Empty(t, restored)
}

func TestIsGzipCompressed(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	// Create a plain text file (not gzip).
	plainPath := filepath.Join(dir, "plain.txt")
	require.NoError(t, os.WriteFile(plainPath, []byte("hello world"), 0o644)) //nolint:gosec // Intentional permission/operation
	ok, err := IsGzipCompressed(plainPath)
	require.NoError(t, err)
	assert.False(t, ok)

	// Create a gzip-compressed file.
	gzPath := filepath.Join(dir, "data.bin")
	require.NoError(t, GzipFile(plainPath, gzPath))
	ok, err = IsGzipCompressed(gzPath)
	require.NoError(t, err)
	assert.True(t, ok)

	// Empty file (< 2 bytes) should return false.
	emptyPath := filepath.Join(dir, "empty")
	require.NoError(t, os.WriteFile(emptyPath, []byte{}, 0o644)) //nolint:gosec // Intentional permission/operation
	ok, err = IsGzipCompressed(emptyPath)
	require.NoError(t, err)
	assert.False(t, ok)

	// Single byte file should return false.
	oneBytePath := filepath.Join(dir, "one")
	require.NoError(t, os.WriteFile(oneBytePath, []byte{0x1f}, 0o644)) //nolint:gosec // Intentional permission/operation
	ok, err = IsGzipCompressed(oneBytePath)
	require.NoError(t, err)
	assert.False(t, ok)

	// Non-existent file should return error.
	_, err = IsGzipCompressed("/nonexistent/file")
	require.Error(t, err)
}

func TestDecompressDirectory(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	// Create directory structure mimicking artifact layout.
	binDir := filepath.Join(dir, "bin")
	require.NoError(t, os.MkdirAll(binDir, 0o755)) //nolint:gosec // Intentional permission/operation

	// 1. Binary file: will be gzip-compressed with original name.
	binaryData := []byte("#!/bin/sh\necho hello\n")
	binaryPath := filepath.Join(binDir, "etcd-linux-x86_64")
	require.NoError(t, os.WriteFile(binaryPath, binaryData, 0o755)) //nolint:gosec // Intentional permission/operation
	// Compress in-place (simulates upload+download: content is gzip, name is original).
	gzTmp := binaryPath + ".gz"
	require.NoError(t, GzipFile(binaryPath, gzTmp))
	require.NoError(t, os.Rename(gzTmp, binaryPath))
	require.NoError(t, os.Chmod(binaryPath, 0o755)) //nolint:gosec // Intentional permission/operation

	// 2. Plain text file (not compressed), should be left as-is.
	textData := []byte("build-cache-hash-12345")
	textPath := filepath.Join(dir, "content-hash.txt")
	require.NoError(t, os.WriteFile(textPath, textData, 0o644)) //nolint:gosec // Intentional permission/operation

	// 3. Natively-compressed archive (.tgz), should be skipped.
	tgzData := []byte{0x1f, 0x8b, 0x08, 0x00} // gzip magic + fake data
	tgzPath := filepath.Join(dir, "tailscale-v1.76.1-linux-x86_64.tgz")
	require.NoError(t, os.WriteFile(tgzPath, tgzData, 0o644)) //nolint:gosec // Intentional permission/operation

	// 4. Another binary, also gzip-compressed with original name.
	binary2Data := []byte("another binary content here")
	binary2Path := filepath.Join(binDir, "etcd-v3.6.8-linux-x86_64")
	require.NoError(t, os.WriteFile(binary2Path, binary2Data, 0o755)) //nolint:gosec // Intentional permission/operation
	gzTmp2 := binary2Path + ".gz"
	require.NoError(t, GzipFile(binary2Path, gzTmp2))
	require.NoError(t, os.Rename(gzTmp2, binary2Path))
	require.NoError(t, os.Chmod(binary2Path, 0o755)) //nolint:gosec // Intentional permission/operation

	// Run DecompressDirectory.
	require.NoError(t, DecompressDirectory(dir))

	// Verify binary was decompressed back to original content.
	got, err := os.ReadFile(binaryPath)
	require.NoError(t, err)
	assert.Equal(t, binaryData, got)

	// Verify permissions are preserved (0755).
	info, err := os.Stat(binaryPath)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o755), info.Mode().Perm())

	// Verify plain text file is unchanged.
	got, err = os.ReadFile(textPath)
	require.NoError(t, err)
	assert.Equal(t, textData, got)

	// Verify .tgz file is untouched (still has gzip magic).
	got, err = os.ReadFile(tgzPath)
	require.NoError(t, err)
	assert.Equal(t, tgzData, got)

	// Verify second binary was decompressed.
	got, err = os.ReadFile(binary2Path)
	require.NoError(t, err)
	assert.Equal(t, binary2Data, got)

	// Verify permissions on second binary.
	info, err = os.Stat(binary2Path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o755), info.Mode().Perm())
}

func TestDecompressDirectory_Idempotent(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	// Create a file that's already decompressed (plain text).
	data := []byte("already decompressed content")
	path := filepath.Join(dir, "already-plain")
	require.NoError(t, os.WriteFile(path, data, 0o644)) //nolint:gosec // Intentional permission/operation

	// Running DecompressDirectory on an already-decompressed directory is a no-op.
	require.NoError(t, DecompressDirectory(dir))

	got, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, data, got)
}

func TestDecompressDirectory_NonExistent(t *testing.T) {
	t.Parallel()

	err := DecompressDirectory("/nonexistent/directory")
	require.Error(t, err)
}

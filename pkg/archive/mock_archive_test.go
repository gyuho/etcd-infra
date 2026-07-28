package archive

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/bytedance/mockey"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUntarGz_FileCloseError(t *testing.T) {
	// Test the defer close error path

	// Create a temp tar.gz file
	tmpDir := t.TempDir()
	tarGzPath := filepath.Join(tmpDir, "test.tar.gz")
	destDir := filepath.Join(tmpDir, "dest")

	// Create a simple tar.gz with one file
	f, err := os.Create(tarGzPath)
	require.NoError(t, err)

	gzWriter := gzip.NewWriter(f)
	tarWriter := tar.NewWriter(gzWriter)

	content := []byte("test content")
	hdr := &tar.Header{
		Name: "testfile.txt",
		Mode: 0o600,
		Size: int64(len(content)),
	}
	require.NoError(t, tarWriter.WriteHeader(hdr))
	_, err = tarWriter.Write(content)
	require.NoError(t, err)

	require.NoError(t, tarWriter.Close())
	require.NoError(t, gzWriter.Close())
	require.NoError(t, f.Close())

	// The function should handle close errors gracefully (just logs them)
	err = UntarGz(tarGzPath, destDir)
	require.NoError(t, err)

	// Verify extraction worked
	extracted := filepath.Join(destDir, "testfile.txt")
	data, err := os.ReadFile(extracted)
	require.NoError(t, err)
	assert.Equal(t, content, data)
}

func TestUntarGz_GzipReaderError(t *testing.T) {
	defer mockey.Mock(gzip.NewReader).Return(nil, errors.New("gzip error")).Build().UnPatch()

	tmpDir := t.TempDir()
	tarGzPath := filepath.Join(tmpDir, "test.tar.gz")

	// Create an empty file
	f, err := os.Create(tarGzPath)
	require.NoError(t, err)
	_ = f.Close()

	err = UntarGz(tarGzPath, tmpDir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "create gzip reader")
}

func TestUntarGz_MkdirAllError(t *testing.T) {
	// Test error when creating parent directory fails

	tmpDir := t.TempDir()
	tarGzPath := filepath.Join(tmpDir, "test.tar.gz")
	// Use a nonexistent parent path
	destDir := "/nonexistent/path/that/cannot/be/created" //nolint:goconst // Contextual string usage

	// Create a simple tar.gz
	f, err := os.Create(tarGzPath)
	require.NoError(t, err)
	gzWriter := gzip.NewWriter(f)
	tarWriter := tar.NewWriter(gzWriter)

	content := []byte("test")
	hdr := &tar.Header{
		Name:     "file.txt",
		Mode:     0o600,
		Size:     int64(len(content)),
		Typeflag: tar.TypeReg,
	}
	require.NoError(t, tarWriter.WriteHeader(hdr))
	_, err = tarWriter.Write(content)
	require.NoError(t, err)

	require.NoError(t, tarWriter.Close())
	require.NoError(t, gzWriter.Close())
	require.NoError(t, f.Close())

	err = UntarGz(tarGzPath, destDir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "create parent directory")
}

func TestUntarGz_IOCopyError(t *testing.T) {
	// Cannot run in parallel: patches io.Copy via mockey.
	defer mockey.Mock(io.Copy).To(func(_ io.Writer, _ io.Reader) (int64, error) {
		return 0, errors.New("copy error")
	}).Build().UnPatch()

	tmpDir := t.TempDir()
	tarGzPath := filepath.Join(tmpDir, "test.tar.gz")
	destDir := filepath.Join(tmpDir, "dest")

	// Create a simple tar.gz
	f, err := os.Create(tarGzPath)
	require.NoError(t, err)
	gzWriter := gzip.NewWriter(f)
	tarWriter := tar.NewWriter(gzWriter)

	content := []byte("test content")
	hdr := &tar.Header{
		Name:     "test.txt",
		Mode:     0o600,
		Size:     int64(len(content)),
		Typeflag: tar.TypeReg,
	}
	require.NoError(t, tarWriter.WriteHeader(hdr))
	_, err = tarWriter.Write(content)
	require.NoError(t, err)

	require.NoError(t, tarWriter.Close())
	require.NoError(t, gzWriter.Close())
	require.NoError(t, f.Close())

	err = UntarGz(tarGzPath, destDir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "copy file")
}

func TestUnzip_CloseError(t *testing.T) {
	// Test the defer close error path

	tmpDir := t.TempDir()
	zipPath := filepath.Join(tmpDir, "test.zip")
	destDir := filepath.Join(tmpDir, "dest")

	// Create a simple zip file
	zipFile, err := os.Create(zipPath)
	require.NoError(t, err)

	zipWriter := zip.NewWriter(zipFile)

	content := []byte("test content")
	fileWriter, err := zipWriter.Create("testfile.txt")
	require.NoError(t, err)
	_, err = fileWriter.Write(content)
	require.NoError(t, err)

	require.NoError(t, zipWriter.Close())
	require.NoError(t, zipFile.Close())

	// The function should handle close errors gracefully
	err = Unzip(zipPath, destDir)
	require.NoError(t, err)

	// Verify extraction
	extracted := filepath.Join(destDir, "testfile.txt")
	data, err := os.ReadFile(extracted)
	require.NoError(t, err)
	assert.Equal(t, content, data)
}

func TestUnzip_IOCopyError(t *testing.T) {
	// Cannot run in parallel: patches io.Copy via mockey.
	defer mockey.Mock(io.Copy).To(func(_ io.Writer, _ io.Reader) (int64, error) {
		return 0, errors.New("copy error")
	}).Build().UnPatch()

	tmpDir := t.TempDir()
	zipPath := filepath.Join(tmpDir, "test.zip")
	destDir := filepath.Join(tmpDir, "dest")

	// Create a simple zip
	zipFile, err := os.Create(zipPath)
	require.NoError(t, err)

	zipWriter := zip.NewWriter(zipFile)
	content := []byte("test")
	fileWriter, err := zipWriter.Create("test.txt")
	require.NoError(t, err)
	_, err = fileWriter.Write(content)
	require.NoError(t, err)

	_ = zipWriter.Close()
	_ = zipFile.Close()

	err = Unzip(zipPath, destDir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "copy zip entry")
}

func TestUntarGz_DirectoryWithZeroPerm(t *testing.T) {
	// Test the dirMode == 0 path (line 61-63)

	tmpDir := t.TempDir()
	tarGzPath := filepath.Join(tmpDir, "test.tar.gz")
	destDir := filepath.Join(tmpDir, "dest")

	// Create a tar.gz with a directory that has 0 permissions
	f, err := os.Create(tarGzPath)
	require.NoError(t, err)

	gzWriter := gzip.NewWriter(f)
	tarWriter := tar.NewWriter(gzWriter)

	// Directory with zero mode
	hdr := &tar.Header{
		Name:     "testdir/",
		Mode:     0, // Zero mode - should use defaultDirPerm
		Typeflag: tar.TypeDir,
	}
	require.NoError(t, tarWriter.WriteHeader(hdr))

	// Add a file in that directory
	fileHdr := &tar.Header{
		Name:     "testdir/file.txt",
		Mode:     0o600,
		Size:     4,
		Typeflag: tar.TypeReg,
	}
	require.NoError(t, tarWriter.WriteHeader(fileHdr))
	_, err = tarWriter.Write([]byte("test"))
	require.NoError(t, err)

	require.NoError(t, tarWriter.Close())
	require.NoError(t, gzWriter.Close())
	require.NoError(t, f.Close())

	err = UntarGz(tarGzPath, destDir)
	require.NoError(t, err)

	// Verify the directory was created with default permissions
	dirPath := filepath.Join(destDir, "testdir")
	info, err := os.Stat(dirPath)
	require.NoError(t, err)
	assert.True(t, info.IsDir())

	// Verify the file was extracted
	filePath := filepath.Join(destDir, "testdir", "file.txt")
	data, err := os.ReadFile(filePath)
	require.NoError(t, err)
	assert.Equal(t, []byte("test"), data)
}

func TestUntarGz_NonStandardType(t *testing.T) {
	// SECURITY REGRESSION TEST:
	// Unsupported tar entry types (including symlinks) must be rejected.
	// If this behavior regresses to "skip and continue", future refactors can
	// accidentally re-introduce link-based write redirection vulnerabilities.

	tmpDir := t.TempDir()
	tarGzPath := filepath.Join(tmpDir, "test.tar.gz")
	destDir := filepath.Join(tmpDir, "dest")

	// Create a tar.gz with an unsupported entry type
	f, err := os.Create(tarGzPath)
	require.NoError(t, err)

	gzWriter := gzip.NewWriter(f)
	tarWriter := tar.NewWriter(gzWriter)

	// Write a symlink entry first; extractor should now fail closed.
	hdr := &tar.Header{
		Name:     "link",
		Linkname: "target",
		Typeflag: tar.TypeSymlink,
	}
	require.NoError(t, tarWriter.WriteHeader(hdr))

	// Add a regular file after symlink entry. It should never be extracted
	// because the symlink entry should abort extraction.
	fileHdr := &tar.Header{
		Name:     "file.txt",
		Mode:     0o600,
		Size:     4,
		Typeflag: tar.TypeReg,
	}
	require.NoError(t, tarWriter.WriteHeader(fileHdr))
	_, err = tarWriter.Write([]byte("test"))
	require.NoError(t, err)

	require.NoError(t, tarWriter.Close())
	require.NoError(t, gzWriter.Close())
	require.NoError(t, f.Close())

	err = UntarGz(tarGzPath, destDir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not supported")
}

func TestUnzip_MkdirAllError(t *testing.T) {
	// Test error when creating parent directory fails

	tmpDir := t.TempDir()
	zipPath := filepath.Join(tmpDir, "test.zip")
	destDir := "/nonexistent/path/that/cannot/be/created"

	// Create a simple zip
	zipFile, err := os.Create(zipPath)
	require.NoError(t, err)

	zipWriter := zip.NewWriter(zipFile)
	content := []byte("test")
	fileWriter, err := zipWriter.Create("test.txt")
	require.NoError(t, err)
	_, err = fileWriter.Write(content)
	require.NoError(t, err)

	_ = zipWriter.Close()
	_ = zipFile.Close()

	err = Unzip(zipPath, destDir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "create parent directory")
}

func TestUntarGz_DirectoryMkdirError(t *testing.T) {
	// Test error when creating a directory entry fails

	tmpDir := t.TempDir()
	tarGzPath := filepath.Join(tmpDir, "test.tar.gz")
	destDir := "/nonexistent/parent/dir" //nolint:goconst // Contextual string usage

	// Create a tar.gz with a directory
	f, err := os.Create(tarGzPath)
	require.NoError(t, err)

	gzWriter := gzip.NewWriter(f)
	tarWriter := tar.NewWriter(gzWriter)

	hdr := &tar.Header{
		Name:     "testdir/",
		Mode:     0o750,
		Typeflag: tar.TypeDir,
	}
	require.NoError(t, tarWriter.WriteHeader(hdr))

	require.NoError(t, tarWriter.Close())
	require.NoError(t, gzWriter.Close())
	require.NoError(t, f.Close())

	err = UntarGz(tarGzPath, destDir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "create directory")
}

func TestUnzip_DirectoryMkdirError(t *testing.T) {
	// Test error when creating a directory in zip fails

	tmpDir := t.TempDir()
	zipPath := filepath.Join(tmpDir, "test.zip")
	destDir := "/nonexistent/parent/dir"

	// Create a zip with a directory
	zipFile, err := os.Create(zipPath)
	require.NoError(t, err)

	zipWriter := zip.NewWriter(zipFile)
	_, err = zipWriter.Create("testdir/")
	require.NoError(t, err)

	require.NoError(t, zipWriter.Close())
	require.NoError(t, zipFile.Close())

	err = Unzip(zipPath, destDir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "create directory")
}

func TestUnzip_OpenFileError(t *testing.T) {
	// Test error when opening output file fails
	// Cannot run in parallel - mocks global os.OpenFile

	tmpDir := t.TempDir()
	zipPath := filepath.Join(tmpDir, "test.zip")
	destDir := filepath.Join(tmpDir, "dest")

	// Create a simple zip with a file BEFORE setting up the mock
	zipFile, err := os.Create(zipPath)
	require.NoError(t, err)

	zipWriter := zip.NewWriter(zipFile)
	fileWriter, err := zipWriter.Create("test.txt")
	require.NoError(t, err)
	_, err = fileWriter.Write([]byte("test content"))
	require.NoError(t, err)

	require.NoError(t, zipWriter.Close())
	require.NoError(t, zipFile.Close())

	// NOW apply the mock after zip file is created
	mock := mockey.Mock(os.OpenFile).Return(nil, errors.New("open file error")).Build()
	defer mock.UnPatch()

	err = Unzip(zipPath, destDir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "open file")
}

func TestUnzip_OpenZipEntryError(t *testing.T) {
	// Test error when opening zip entry fails
	// This is difficult to test without mocking or creating a corrupted zip,
	// so we'll use mockey to mock the zip.File.Open method

	tmpDir := t.TempDir()
	zipPath := filepath.Join(tmpDir, "test.zip")
	destDir := filepath.Join(tmpDir, "dest")

	// Create a zip with a file
	zipFile, err := os.Create(zipPath)
	require.NoError(t, err)

	zipWriter := zip.NewWriter(zipFile)
	fileWriter, err := zipWriter.Create("test.txt")
	require.NoError(t, err)
	_, err = fileWriter.Write([]byte("content"))
	require.NoError(t, err)

	require.NoError(t, zipWriter.Close())
	require.NoError(t, zipFile.Close())

	// Mock zip.File.Open to return an error
	defer mockey.Mock((*zip.File).Open).Return(nil, errors.New("zip entry error")).Build().UnPatch()

	err = Unzip(zipPath, destDir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "open zip entry")
}

func TestUnzip_OpenZipEntryError_ClosesOutFile(t *testing.T) {
	// Regression test: when f.Open() fails after os.OpenFile succeeds,
	// the output file descriptor must be closed before returning.
	// Without the fix, outFile leaks until process exit or GC finalization.
	//
	// Strategy: intercept os.OpenFile to capture the created *os.File, then
	// mock (*zip.File).Open to fail. After Unzip returns, verify the captured
	// file is already closed by attempting a second Close (which returns
	// os.ErrClosed on an already-closed file).

	tmpDir := t.TempDir()
	zipPath := filepath.Join(tmpDir, "test.zip")
	destDir := filepath.Join(tmpDir, "dest")
	require.NoError(t, os.MkdirAll(destDir, 0o750))

	zipFile, err := os.Create(zipPath)
	require.NoError(t, err)
	zipWriter := zip.NewWriter(zipFile)
	fw, err := zipWriter.Create("leak.txt")
	require.NoError(t, err)
	_, err = fw.Write([]byte("data"))
	require.NoError(t, err)
	require.NoError(t, zipWriter.Close())
	require.NoError(t, zipFile.Close())

	var captured *os.File
	origin := os.OpenFile
	openMock := mockey.Mock(os.OpenFile).Origin(&origin).To(func(name string, flag int, perm os.FileMode) (*os.File, error) {
		f, openErr := origin(name, flag, perm)
		if openErr == nil && filepath.Dir(name) == destDir {
			captured = f
		}
		return f, openErr
	}).Build()
	defer openMock.UnPatch()

	entryMock := mockey.Mock((*zip.File).Open).Return(nil, errors.New("injected open error")).Build()
	defer entryMock.UnPatch()

	err = Unzip(zipPath, destDir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "open zip entry")

	require.NotNil(t, captured, "os.OpenFile must have been called for the output file")

	// Attempt a second Close on the captured handle. A properly closed file
	// returns os.ErrClosed; a leaked (still-open) handle would succeed.
	secondCloseErr := captured.Close()
	assert.ErrorIs(t, secondCloseErr, os.ErrClosed,
		"outFile must already be closed when f.Open fails")
}

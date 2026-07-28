//nolint:testpackage,paralleltest,funlen // Tests use package internals and sequential subtests.
package archive

import (
	"archive/zip"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

const (
	nestedPath    = "a/b/c/d/file.txt"
	nestedContent = "nested content"
)

func TestUnzip(t *testing.T) {
	t.Run("extracts files from zip archive", func(t *testing.T) {
		tmpDir := t.TempDir()

		// Create a zip file with test content
		zipPath := filepath.Join(tmpDir, "test.zip")
		createTestZip(t, zipPath, map[string]string{
			"file1.txt":        "content1",
			"subdir/file2.txt": "content2",
		})

		// Extract to a destination directory
		destDir := filepath.Join(tmpDir, "extracted")
		err := os.MkdirAll(destDir, 0o750)
		require.NoError(t, err)

		err = Unzip(zipPath, destDir)
		require.NoError(t, err)

		// Verify extracted files
		content1, err := os.ReadFile(filepath.Join(destDir, "file1.txt"))
		require.NoError(t, err)
		require.Equal(t, "content1", string(content1))

		content2, err := os.ReadFile(filepath.Join(destDir, "subdir", "file2.txt"))
		require.NoError(t, err)
		require.Equal(t, "content2", string(content2))
	})

	t.Run("returns error for non-existent file", func(t *testing.T) {
		tmpDir := t.TempDir()
		err := Unzip("/nonexistent/file.zip", tmpDir)
		require.Error(t, err)
	})

	t.Run("returns error for invalid zip file", func(t *testing.T) {
		tmpDir := t.TempDir()
		invalidFile := filepath.Join(tmpDir, "invalid.zip")
		err := os.WriteFile(invalidFile, []byte("not a zip file"), 0o600)
		require.NoError(t, err)

		err = Unzip(invalidFile, tmpDir)
		require.Error(t, err)
	})

	t.Run("extracts directory entries", func(t *testing.T) {
		tmpDir := t.TempDir()

		zipPath := filepath.Join(tmpDir, "withdir.zip")
		createTestZipWithDir(t, zipPath, "mydir")

		destDir := filepath.Join(tmpDir, "extracted")
		err := os.MkdirAll(destDir, 0o750)
		require.NoError(t, err)

		err = Unzip(zipPath, destDir)
		require.NoError(t, err)

		// Verify directory exists
		info, err := os.Stat(filepath.Join(destDir, "mydir"))
		require.NoError(t, err)
		require.True(t, info.IsDir())
	})

	t.Run("preserves file permissions", func(t *testing.T) {
		tmpDir := t.TempDir()

		zipPath := filepath.Join(tmpDir, "withperms.zip")
		createTestZipWithMode(t, zipPath, "executable.sh", 0o750, "#!/bin/bash\necho hello")

		destDir := filepath.Join(tmpDir, "extracted")
		err := os.MkdirAll(destDir, 0o750)
		require.NoError(t, err)

		err = Unzip(zipPath, destDir)
		require.NoError(t, err)

		info, err := os.Stat(filepath.Join(destDir, "executable.sh"))
		require.NoError(t, err)
		// Check that executable bit is set
		require.NotEqual(t, os.FileMode(0), info.Mode()&0o100)
	})

	t.Run("returns error when destination is a file", func(t *testing.T) {
		tmpDir := t.TempDir()

		zipPath := filepath.Join(tmpDir, "test.zip")
		createTestZip(t, zipPath, map[string]string{
			"file1.txt": "content1",
		})

		destFile := filepath.Join(tmpDir, "destfile")
		err := os.WriteFile(destFile, []byte("not a dir"), 0o600)
		require.NoError(t, err)

		err = Unzip(zipPath, destFile)
		require.Error(t, err)
	})

	t.Run("returns error when file path is a directory", func(t *testing.T) {
		tmpDir := t.TempDir()

		zipPath := filepath.Join(tmpDir, "test.zip")
		createTestZip(t, zipPath, map[string]string{
			"file1.txt": "content1",
		})

		destDir := filepath.Join(tmpDir, "extracted")
		err := os.MkdirAll(filepath.Join(destDir, "file1.txt"), 0o750)
		require.NoError(t, err)

		err = Unzip(zipPath, destDir)
		require.Error(t, err)
	})

	t.Run("returns error when directory entry target is blocked", func(t *testing.T) {
		tmpDir := t.TempDir()

		zipPath := filepath.Join(tmpDir, "withdir.zip")
		createTestZipWithDir(t, zipPath, "blocked")

		destFile := filepath.Join(tmpDir, "destfile")
		err := os.WriteFile(destFile, []byte("not a dir"), 0o600)
		require.NoError(t, err)

		err = Unzip(zipPath, destFile)
		require.Error(t, err)
	})
}

// createTestZip creates a zip file with the given file contents.
func createTestZip(t *testing.T, path string, files map[string]string) {
	t.Helper()

	f, err := os.Create(path)
	require.NoError(t, err)
	defer func() { _ = f.Close() }()

	w := zip.NewWriter(f)
	defer func() { _ = w.Close() }()

	for name, content := range files {
		fw, createErr := w.Create(name)
		require.NoError(t, createErr)

		_, err = fw.Write([]byte(content))
		require.NoError(t, err)
	}
}

// createTestZipWithDir creates a zip file with a directory entry.
func createTestZipWithDir(t *testing.T, path, dirName string) {
	t.Helper()

	f, err := os.Create(path)
	require.NoError(t, err)
	defer func() { _ = f.Close() }()

	w := zip.NewWriter(f)
	defer func() { _ = w.Close() }()

	// Create directory entry (must end with /)
	_, err = w.Create(dirName + "/")
	require.NoError(t, err)
}

// createTestZipWithMode creates a zip file with a file having specific mode.
func createTestZipWithMode(t *testing.T, path, fileName string, mode os.FileMode, content string) {
	t.Helper()

	f, err := os.Create(path)
	require.NoError(t, err)
	defer func() { _ = f.Close() }()

	w := zip.NewWriter(f)
	defer func() { _ = w.Close() }()

	header := &zip.FileHeader{
		Name:   fileName,
		Method: zip.Deflate,
	}
	header.SetMode(mode)

	fw, createErr := w.CreateHeader(header)
	require.NoError(t, createErr)

	_, err = fw.Write([]byte(content))
	require.NoError(t, err)
}

func TestUnzip_EmptyArchive(t *testing.T) {
	// Test extracting an empty zip archive
	tmpDir := t.TempDir()

	zipPath := filepath.Join(tmpDir, "empty.zip")
	f, err := os.Create(zipPath)
	require.NoError(t, err)
	w := zip.NewWriter(f)
	require.NoError(t, w.Close())
	require.NoError(t, f.Close())

	destDir := filepath.Join(tmpDir, "extracted")
	err = os.MkdirAll(destDir, 0o750)
	require.NoError(t, err)

	err = Unzip(zipPath, destDir)
	require.NoError(t, err)
}

func TestUnzip_MultipleFiles(t *testing.T) {
	// Test extracting multiple files to ensure all are processed correctly
	tmpDir := t.TempDir()

	zipPath := filepath.Join(tmpDir, "multi.zip")

	f, err := os.Create(zipPath)
	require.NoError(t, err)
	w := zip.NewWriter(f)

	files := []struct {
		name    string
		content string
	}{
		{"a.txt", "content a"},
		{"b.txt", "content b"},
		{"c.txt", "content c"},
	}

	for _, file := range files {
		fw, createErr := w.Create(file.name)
		require.NoError(t, createErr)
		_, err = fw.Write([]byte(file.content))
		require.NoError(t, err)
	}

	require.NoError(t, w.Close())
	require.NoError(t, f.Close())

	destDir := filepath.Join(tmpDir, "extracted")
	err = os.MkdirAll(destDir, 0o750)
	require.NoError(t, err)

	err = Unzip(zipPath, destDir)
	require.NoError(t, err)

	// Verify all files
	for _, file := range files {
		content, err := os.ReadFile(filepath.Join(destDir, file.name))
		require.NoError(t, err)
		require.Equal(t, file.content, string(content))
	}
}

func TestUnzip_NestedDirectories(t *testing.T) {
	// Test extracting files in deeply nested directories
	tmpDir := t.TempDir()

	zipPath := filepath.Join(tmpDir, "nested.zip")

	f, err := os.Create(zipPath)
	require.NoError(t, err)
	w := zip.NewWriter(f)

	content := nestedContent

	fw, createErr := w.Create(nestedPath)
	require.NoError(t, createErr)
	_, err = fw.Write([]byte(content))
	require.NoError(t, err)

	require.NoError(t, w.Close())
	require.NoError(t, f.Close())

	destDir := filepath.Join(tmpDir, "extracted")
	err = os.MkdirAll(destDir, 0o750)
	require.NoError(t, err)

	err = Unzip(zipPath, destDir)
	require.NoError(t, err)

	// Verify nested file
	data, err := os.ReadFile(filepath.Join(destDir, nestedPath))
	require.NoError(t, err)
	require.Equal(t, content, string(data))
}

func TestUnzip_LargeFile(t *testing.T) {
	// Test extracting a larger file to verify io.Copy works correctly
	tmpDir := t.TempDir()

	zipPath := filepath.Join(tmpDir, "large.zip")

	// Create 100KB of content
	largeContent := make([]byte, 100*1024)
	for i := range largeContent {
		largeContent[i] = byte(i % 256)
	}

	f, err := os.Create(zipPath)
	require.NoError(t, err)
	w := zip.NewWriter(f)

	fw, createErr := w.Create("largefile.bin")
	require.NoError(t, createErr)
	_, err = fw.Write(largeContent)
	require.NoError(t, err)

	require.NoError(t, w.Close())
	require.NoError(t, f.Close())

	destDir := filepath.Join(tmpDir, "extracted")
	err = os.MkdirAll(destDir, 0o750)
	require.NoError(t, err)

	err = Unzip(zipPath, destDir)
	require.NoError(t, err)

	// Verify large file content
	data, err := os.ReadFile(filepath.Join(destDir, "largefile.bin"))
	require.NoError(t, err)
	require.Equal(t, largeContent, data)
}

func TestUnzip_NestedDirectory(t *testing.T) {
	// Test that nested directory entries are properly created
	tmpDir := t.TempDir()

	zipPath := filepath.Join(tmpDir, "nesteddir.zip")

	f, err := os.Create(zipPath)
	require.NoError(t, err)
	w := zip.NewWriter(f)

	// Create directory entries explicitly with proper mode
	parentHeader := &zip.FileHeader{Name: "parent/"}
	parentHeader.SetMode(0o755 | os.ModeDir)
	_, err = w.CreateHeader(parentHeader)
	require.NoError(t, err)

	childHeader := &zip.FileHeader{Name: "parent/child/"}
	childHeader.SetMode(0o755 | os.ModeDir)
	_, err = w.CreateHeader(childHeader)
	require.NoError(t, err)

	// Create a file in the nested directory
	fw, createErr := w.Create("parent/child/file.txt")
	require.NoError(t, createErr)
	_, err = fw.Write([]byte("content"))
	require.NoError(t, err)

	require.NoError(t, w.Close())
	require.NoError(t, f.Close())

	destDir := filepath.Join(tmpDir, "extracted")
	err = os.MkdirAll(destDir, 0o750)
	require.NoError(t, err)

	err = Unzip(zipPath, destDir)
	require.NoError(t, err)

	// Verify directories and file exist
	info, err := os.Stat(filepath.Join(destDir, "parent"))
	require.NoError(t, err)
	require.True(t, info.IsDir())

	info, err = os.Stat(filepath.Join(destDir, "parent", "child"))
	require.NoError(t, err)
	require.True(t, info.IsDir())

	data, err := os.ReadFile(filepath.Join(destDir, "parent", "child", "file.txt"))
	require.NoError(t, err)
	require.Equal(t, "content", string(data))
}

func TestUnzip_RejectsTraversalAndAbsolutePaths(t *testing.T) {
	// SECURITY REGRESSION TEST:
	// ZIP extraction must be destination-confined. If these cases pass, the
	// extractor is vulnerable to Zip Slip arbitrary write.
	tmpDir := t.TempDir()
	destDir := filepath.Join(tmpDir, "extract")
	require.NoError(t, os.MkdirAll(destDir, 0o750))

	t.Run("rejects parent traversal", func(t *testing.T) {
		zipPath := filepath.Join(tmpDir, "traversal.zip")
		createTestZip(t, zipPath, map[string]string{
			"../../evil.txt": "evil",
		})
		err := Unzip(zipPath, destDir)
		require.Error(t, err)
		require.Contains(t, err.Error(), "escapes destination")
		_, statErr := os.Stat(filepath.Join(tmpDir, "evil.txt"))
		require.True(t, os.IsNotExist(statErr))
	})

	t.Run("rejects absolute path", func(t *testing.T) {
		zipPath := filepath.Join(tmpDir, "absolute.zip")
		createTestZip(t, zipPath, map[string]string{
			"/tmp/evil.txt": "evil",
		})
		err := Unzip(zipPath, destDir)
		require.Error(t, err)
		require.Contains(t, err.Error(), "must be relative")
	})

	t.Run("rejects null byte in entry name", func(t *testing.T) {
		// SECURITY REGRESSION TEST (CWE-158):
		// Null bytes can cause C/OS-level path truncation that diverges from
		// Go's string-based validation. Defense-in-depth demands explicit
		// rejection at the Go layer.
		//
		// Note: Go's archive/zip may encode null bytes differently across
		// versions, so we test secureExtractPath directly.
		_, err := secureExtractPath(destDir, "safe\x00evil.txt")
		require.Error(t, err)
		require.Contains(t, err.Error(), "null byte")
	})
}

func TestUnzip_RejectsSymlinkDestinationRoot(t *testing.T) {
	// SECURITY REGRESSION TEST:
	// A symlinked extraction root can pivot all writes to another filesystem path.
	// Unzip must reject this root before creating any files.
	tmpDir := t.TempDir()
	realDest := filepath.Join(tmpDir, "real-dest")
	require.NoError(t, os.MkdirAll(realDest, 0o750))

	symlinkDest := filepath.Join(tmpDir, "dest-link")
	require.NoError(t, os.Symlink(realDest, symlinkDest))

	zipPath := filepath.Join(tmpDir, "archive.zip")
	createTestZip(t, zipPath, map[string]string{"safe.txt": "data"})

	err := Unzip(zipPath, symlinkDest)
	require.Error(t, err)
	require.Contains(t, err.Error(), "destination root")

	_, statErr := os.Stat(filepath.Join(realDest, "safe.txt"))
	require.True(t, os.IsNotExist(statErr))
}

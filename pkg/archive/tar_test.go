//nolint:testpackage,paralleltest,funlen // Tests use package internals and sequential subtests.
package archive

import (
	"archive/tar"
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTarGzSingleFile(t *testing.T) {
	t.Run("round-trip with UntarGz", func(t *testing.T) {
		tmpDir := t.TempDir()

		// Create a source binary with executable permissions.
		srcPath := filepath.Join(tmpDir, "etcd")
		require.NoError(t, os.WriteFile(srcPath, []byte("fake-etcd-binary"), 0o600))
		require.NoError(t, os.Chmod(srcPath, 0o755)) //nolint:gosec // Intentional permission

		// Tar+gzip the file.
		archivePath := filepath.Join(tmpDir, "etcd.tar.gz")
		require.NoError(t, TarGzSingleFile(srcPath, archivePath))

		// Archive must exist and be non-empty.
		info, err := os.Stat(archivePath)
		require.NoError(t, err)
		require.Positive(t, info.Size())

		// Round-trip: extract and compare.
		extractDir := filepath.Join(tmpDir, "extracted")
		require.NoError(t, os.MkdirAll(extractDir, 0o750))
		require.NoError(t, UntarGz(archivePath, extractDir))

		got, err := os.ReadFile(filepath.Join(extractDir, "etcd"))
		require.NoError(t, err)
		require.Equal(t, "fake-etcd-binary", string(got))
	})

	t.Run("preserves file permissions", func(t *testing.T) {
		tmpDir := t.TempDir()

		srcPath := filepath.Join(tmpDir, "mybin")
		require.NoError(t, os.WriteFile(srcPath, []byte("data"), 0o600))
		require.NoError(t, os.Chmod(srcPath, 0o755)) //nolint:gosec // Intentional permission

		archivePath := filepath.Join(tmpDir, "mybin.tar.gz")
		require.NoError(t, TarGzSingleFile(srcPath, archivePath))

		extractDir := filepath.Join(tmpDir, "extracted")
		require.NoError(t, os.MkdirAll(extractDir, 0o750))
		require.NoError(t, UntarGz(archivePath, extractDir))

		info, err := os.Stat(filepath.Join(extractDir, "mybin"))
		require.NoError(t, err)
		require.Equal(t, os.FileMode(0o755), info.Mode().Perm())
	})

	t.Run("uses base name for tar entry", func(t *testing.T) {
		tmpDir := t.TempDir()

		// Source in a nested directory; tar entry should be just the base name.
		nestedDir := filepath.Join(tmpDir, "a", "b")
		require.NoError(t, os.MkdirAll(nestedDir, 0o750))
		srcPath := filepath.Join(nestedDir, "etcd")
		require.NoError(t, os.WriteFile(srcPath, []byte("etcd-binary"), 0o600))
		require.NoError(t, os.Chmod(srcPath, 0o755)) //nolint:gosec // Intentional permission

		archivePath := filepath.Join(tmpDir, "etcd.tar.gz")
		require.NoError(t, TarGzSingleFile(srcPath, archivePath))

		// Verify the tar entry name by reading the header directly.
		f, err := os.Open(archivePath)
		require.NoError(t, err)
		defer func() { _ = f.Close() }()
		gr, err := gzip.NewReader(f)
		require.NoError(t, err)
		defer func() { _ = gr.Close() }()
		tr := tar.NewReader(gr)
		hdr, err := tr.Next()
		require.NoError(t, err)
		require.Equal(t, "etcd", hdr.Name)
	})

	t.Run("error on non-existent source", func(t *testing.T) {
		tmpDir := t.TempDir()
		err := TarGzSingleFile("/no/such/file", filepath.Join(tmpDir, "out.tar.gz"))
		require.Error(t, err)
	})

	t.Run("error on directory source", func(t *testing.T) {
		tmpDir := t.TempDir()
		err := TarGzSingleFile(tmpDir, filepath.Join(tmpDir, "out.tar.gz"))
		require.Error(t, err)
		require.Contains(t, err.Error(), "directory")
	})
}

func TestUntarGz(t *testing.T) {
	t.Run("extracts files from tar.gz archive", func(t *testing.T) {
		// Create a temp directory for our test archive
		tmpDir := t.TempDir()

		// Create a tar.gz file with test content
		tarGzPath := filepath.Join(tmpDir, "test.tar.gz")
		createTestTarGz(t, tarGzPath, map[string]string{
			"file1.txt":        "content1",
			"subdir/file2.txt": "content2",
		})

		// Extract to a destination directory
		destDir := filepath.Join(tmpDir, "extracted")
		err := os.MkdirAll(destDir, 0o750)
		require.NoError(t, err)

		err = UntarGz(tarGzPath, destDir)
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
		err := UntarGz("/nonexistent/file.tar.gz", tmpDir)
		require.Error(t, err)
	})

	t.Run("returns error for invalid gzip file", func(t *testing.T) {
		tmpDir := t.TempDir()
		invalidFile := filepath.Join(tmpDir, "invalid.tar.gz")
		err := os.WriteFile(invalidFile, []byte("not a gzip file"), 0o600)
		require.NoError(t, err)

		err = UntarGz(invalidFile, tmpDir)
		require.Error(t, err)
	})

	t.Run("returns error for invalid tar stream", func(t *testing.T) {
		tmpDir := t.TempDir()
		tarGzPath := filepath.Join(tmpDir, "bad.tar.gz")

		f, err := os.Create(tarGzPath)
		require.NoError(t, err)
		gw := gzip.NewWriter(f)
		_, err = gw.Write([]byte("not a tar archive"))
		require.NoError(t, err)
		require.NoError(t, gw.Close())
		require.NoError(t, f.Close())

		err = UntarGz(tarGzPath, tmpDir)
		require.Error(t, err)
	})

	t.Run("extracts directory with proper permissions", func(t *testing.T) {
		tmpDir := t.TempDir()

		// Create archive with a directory entry
		tarGzPath := filepath.Join(tmpDir, "withdir.tar.gz")
		createTestTarGzWithDir(t, tarGzPath, "mydir", 0o750)

		destDir := filepath.Join(tmpDir, "extracted")
		err := os.MkdirAll(destDir, 0o750)
		require.NoError(t, err)

		err = UntarGz(tarGzPath, destDir)
		require.NoError(t, err)

		// Verify directory exists
		info, err := os.Stat(filepath.Join(destDir, "mydir"))
		require.NoError(t, err)
		require.True(t, info.IsDir())
	})

	t.Run("defaults directory mode when header mode is zero", func(t *testing.T) {
		tmpDir := t.TempDir()

		tarGzPath := filepath.Join(tmpDir, "zeromode.tar.gz")
		createTestTarGzWithDir(t, tarGzPath, "zeromode", 0)

		destDir := filepath.Join(tmpDir, "extracted")
		err := os.MkdirAll(destDir, 0o750)
		require.NoError(t, err)

		err = UntarGz(tarGzPath, destDir)
		require.NoError(t, err)

		info, err := os.Stat(filepath.Join(destDir, "zeromode"))
		require.NoError(t, err)
		require.True(t, info.IsDir())
	})

	t.Run("returns error when destination is a file", func(t *testing.T) {
		tmpDir := t.TempDir()

		tarGzPath := filepath.Join(tmpDir, "test.tar.gz")
		createTestTarGz(t, tarGzPath, map[string]string{"file1.txt": "content1"})

		destFile := filepath.Join(tmpDir, "destfile")
		err := os.WriteFile(destFile, []byte("not a dir"), 0o600)
		require.NoError(t, err)

		err = UntarGz(tarGzPath, destFile)
		require.Error(t, err)
	})

	t.Run("returns error when file path is a directory", func(t *testing.T) {
		tmpDir := t.TempDir()

		tarGzPath := filepath.Join(tmpDir, "test.tar.gz")
		createTestTarGz(t, tarGzPath, map[string]string{
			"file1.txt": "content1",
		})

		destDir := filepath.Join(tmpDir, "extracted")
		err := os.MkdirAll(filepath.Join(destDir, "file1.txt"), 0o750)
		require.NoError(t, err)

		err = UntarGz(tarGzPath, destDir)
		require.Error(t, err)
	})
}

// createTestTarGz creates a tar.gz file with the given file contents.
func createTestTarGz(t *testing.T, path string, files map[string]string) {
	t.Helper()

	f, err := os.Create(path)
	require.NoError(t, err)
	defer func() { _ = f.Close() }()

	gw := gzip.NewWriter(f)
	defer func() { _ = gw.Close() }()

	tw := tar.NewWriter(gw)
	defer func() { _ = tw.Close() }()

	for name, content := range files {
		// Create parent directories if needed
		dir := filepath.Dir(name)
		if dir != "." {
			hdr := &tar.Header{
				Name:     dir + "/",
				Mode:     0o755,
				Typeflag: tar.TypeDir,
			}
			err := tw.WriteHeader(hdr)
			require.NoError(t, err)
		}

		hdr := &tar.Header{
			Name: name,
			Mode: 0o644,
			Size: int64(len(content)),
		}
		err := tw.WriteHeader(hdr)
		require.NoError(t, err)

		_, err = tw.Write([]byte(content))
		require.NoError(t, err)
	}
}

// createTestTarGzWithDir creates a tar.gz file with a directory entry.
func createTestTarGzWithDir(t *testing.T, path, dirName string, mode os.FileMode) {
	t.Helper()

	f, err := os.Create(path)
	require.NoError(t, err)
	defer func() { _ = f.Close() }()

	gw := gzip.NewWriter(f)
	defer func() { _ = gw.Close() }()

	tw := tar.NewWriter(gw)
	defer func() { _ = tw.Close() }()

	hdr := &tar.Header{
		Name:     dirName + "/",
		Mode:     int64(mode),
		Typeflag: tar.TypeDir,
	}
	err = tw.WriteHeader(hdr)
	require.NoError(t, err)
}

func TestUntarGz_PreservesExecutePermissions(t *testing.T) {
	// Regression test: UntarGz must preserve file permissions from the tar
	// header. Without this, binaries like containerd lose their execute bits
	// because os.Create defaults to 0666 (umask-applied → 0644).
	tmpDir := t.TempDir()

	tarGzPath := filepath.Join(tmpDir, "bins.tar.gz")

	// Create a tar.gz with executable binaries (0755) and a regular file (0644).
	f, err := os.Create(tarGzPath)
	require.NoError(t, err)
	gw := gzip.NewWriter(f)
	tw := tar.NewWriter(gw)

	entries := []struct {
		name string
		mode int64
		body string
	}{
		{"containerd", 0o755, "fake-containerd-binary"},
		{"ctr", 0o755, "fake-ctr-binary"},
		{"config.toml", 0o644, "some-config"},
	}
	for _, e := range entries {
		hdr := &tar.Header{Name: e.name, Mode: e.mode, Size: int64(len(e.body))}
		require.NoError(t, tw.WriteHeader(hdr))
		_, err = tw.Write([]byte(e.body))
		require.NoError(t, err)
	}
	require.NoError(t, tw.Close())
	require.NoError(t, gw.Close())
	require.NoError(t, f.Close())

	destDir := filepath.Join(tmpDir, "extracted")
	require.NoError(t, os.MkdirAll(destDir, 0o750))

	err = UntarGz(tarGzPath, destDir)
	require.NoError(t, err)

	for _, e := range entries {
		info, statErr := os.Stat(filepath.Join(destDir, e.name))
		require.NoError(t, statErr, "file %s should exist", e.name)
		got := info.Mode().Perm()
		want := os.FileMode(e.mode) //nolint:gosec // Intentional permission/operation
		require.Equal(t, want, got, "file %s: want perm %o, got %o", e.name, want, got)
	}
}

func TestUntarGz_SymlinkRejected(t *testing.T) {
	// SECURITY REGRESSION TEST:
	// Symlink entries must be rejected, not silently ignored.
	// If we allow them, archives can redirect extraction writes outside the
	// destination root via symlink pivots.
	tmpDir := t.TempDir()

	tarGzPath := filepath.Join(tmpDir, "withsymlink.tar.gz")

	// Create a tar.gz file with a symlink entry
	f, err := os.Create(tarGzPath)
	require.NoError(t, err)
	gw := gzip.NewWriter(f)
	tw := tar.NewWriter(gw)

	hdr := &tar.Header{
		Name:     "mylink",
		Linkname: "/some/target",
		Mode:     0o777,
		Typeflag: tar.TypeSymlink,
	}
	require.NoError(t, tw.WriteHeader(hdr))
	require.NoError(t, tw.Close())
	require.NoError(t, gw.Close())
	require.NoError(t, f.Close())

	destDir := filepath.Join(tmpDir, "extracted")
	err = os.MkdirAll(destDir, 0o750)
	require.NoError(t, err)

	err = UntarGz(tarGzPath, destDir)
	require.Error(t, err)
	require.Contains(t, err.Error(), "not supported")
}

func TestUntarGz_EmptyArchive(t *testing.T) {
	// Test extracting an empty tar.gz archive
	tmpDir := t.TempDir()

	tarGzPath := filepath.Join(tmpDir, "empty.tar.gz")
	f, err := os.Create(tarGzPath)
	require.NoError(t, err)
	gw := gzip.NewWriter(f)
	tw := tar.NewWriter(gw)
	require.NoError(t, tw.Close())
	require.NoError(t, gw.Close())
	require.NoError(t, f.Close())

	destDir := filepath.Join(tmpDir, "extracted")
	err = os.MkdirAll(destDir, 0o750)
	require.NoError(t, err)

	err = UntarGz(tarGzPath, destDir)
	require.NoError(t, err)
}

func TestUntarGz_MultipleFiles(t *testing.T) {
	// Test extracting multiple files to ensure all are processed correctly
	tmpDir := t.TempDir()

	tarGzPath := filepath.Join(tmpDir, "multi.tar.gz")

	// Create archive with multiple files in specific order
	f, err := os.Create(tarGzPath)
	require.NoError(t, err)
	gw := gzip.NewWriter(f)
	tw := tar.NewWriter(gw)

	files := []struct {
		name    string
		content string
	}{
		{"a.txt", "content a"},
		{"b.txt", "content b"},
		{"c.txt", "content c"},
	}

	for _, file := range files {
		hdr := &tar.Header{
			Name: file.name,
			Mode: 0o644,
			Size: int64(len(file.content)),
		}
		require.NoError(t, tw.WriteHeader(hdr))
		_, err = tw.Write([]byte(file.content))
		require.NoError(t, err)
	}

	require.NoError(t, tw.Close())
	require.NoError(t, gw.Close())
	require.NoError(t, f.Close())

	destDir := filepath.Join(tmpDir, "extracted")
	err = os.MkdirAll(destDir, 0o750)
	require.NoError(t, err)

	err = UntarGz(tarGzPath, destDir)
	require.NoError(t, err)

	// Verify all files
	for _, file := range files {
		content, err := os.ReadFile(filepath.Join(destDir, file.name))
		require.NoError(t, err)
		require.Equal(t, file.content, string(content))
	}
}

func TestUntarGz_RejectsTraversalAndAbsolutePaths(t *testing.T) {
	// SECURITY REGRESSION TEST:
	// Tar extraction must never write outside dest. These checks defend against
	// Zip Slip payloads that target host files.
	tmpDir := t.TempDir()
	destDir := filepath.Join(tmpDir, "extract")
	require.NoError(t, os.MkdirAll(destDir, 0o750))

	t.Run("rejects parent traversal", func(t *testing.T) {
		tarGzPath := filepath.Join(tmpDir, "traversal.tar.gz")
		createTestTarGz(t, tarGzPath, map[string]string{
			"../../evil.txt": "evil",
		})
		err := UntarGz(tarGzPath, destDir)
		require.Error(t, err)
		require.Contains(t, err.Error(), "escapes destination")
		_, statErr := os.Stat(filepath.Join(tmpDir, "evil.txt"))
		require.True(t, os.IsNotExist(statErr))
	})

	t.Run("rejects absolute path", func(t *testing.T) {
		tarGzPath := filepath.Join(tmpDir, "absolute.tar.gz")
		createTestTarGz(t, tarGzPath, map[string]string{
			"/tmp/evil.txt": "evil",
		})
		err := UntarGz(tarGzPath, destDir)
		require.Error(t, err)
		require.Contains(t, err.Error(), "must be relative")
	})

	t.Run("rejects null byte in entry name", func(t *testing.T) {
		// SECURITY REGRESSION TEST (CWE-158):
		// Null bytes in filenames can truncate paths at the C/OS level while Go
		// strings see the full value, creating a mismatch between validation
		// and actual filesystem writes. This test ensures defense-in-depth
		// rejection at the Go layer.
		//
		// Note: Go's archive/tar refuses to encode null bytes in PAX headers,
		// so we test secureExtractPath directly instead of round-tripping.
		_, err := secureExtractPath(destDir, "safe\x00evil.txt")
		require.Error(t, err)
		require.Contains(t, err.Error(), "null byte")
	})
}

func TestUntarGz_RejectsSymlinkDestinationRoot(t *testing.T) {
	// SECURITY REGRESSION TEST:
	// A symlinked extraction root can redirect all archive writes outside the
	// expected destination. Extraction must fail closed in this case.
	tmpDir := t.TempDir()
	realDest := filepath.Join(tmpDir, "real-dest")
	require.NoError(t, os.MkdirAll(realDest, 0o750))

	symlinkDest := filepath.Join(tmpDir, "dest-link")
	require.NoError(t, os.Symlink(realDest, symlinkDest))

	tarGzPath := filepath.Join(tmpDir, "archive.tar.gz")
	createTestTarGz(t, tarGzPath, map[string]string{"safe.txt": "data"})

	err := UntarGz(tarGzPath, symlinkDest)
	require.Error(t, err)
	require.Contains(t, err.Error(), "destination root")

	_, statErr := os.Stat(filepath.Join(realDest, "safe.txt"))
	require.True(t, os.IsNotExist(statErr))
}

func TestUntarGz_NestedDirectories(t *testing.T) {
	// Test extracting files in deeply nested directories
	tmpDir := t.TempDir()

	tarGzPath := filepath.Join(tmpDir, "nested.tar.gz")

	f, err := os.Create(tarGzPath)
	require.NoError(t, err)
	gw := gzip.NewWriter(f)
	tw := tar.NewWriter(gw)

	// Create a file in a deeply nested directory
	nestedPath := "a/b/c/d/file.txt"
	content := "nested content"

	hdr := &tar.Header{
		Name: nestedPath,
		Mode: 0o644,
		Size: int64(len(content)),
	}
	require.NoError(t, tw.WriteHeader(hdr))
	_, err = tw.Write([]byte(content))
	require.NoError(t, err)

	require.NoError(t, tw.Close())
	require.NoError(t, gw.Close())
	require.NoError(t, f.Close())

	destDir := filepath.Join(tmpDir, "extracted")
	err = os.MkdirAll(destDir, 0o750)
	require.NoError(t, err)

	err = UntarGz(tarGzPath, destDir)
	require.NoError(t, err)

	// Verify nested file
	data, err := os.ReadFile(filepath.Join(destDir, nestedPath))
	require.NoError(t, err)
	require.Equal(t, content, string(data))
}

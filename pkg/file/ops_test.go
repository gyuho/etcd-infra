//nolint:testpackage // Need access to internals
package file

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExists(t *testing.T) {
	t.Parallel()

	f, err := os.CreateTemp(t.TempDir(), "test-file-exists")
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = os.Remove(f.Name())
	})

	exists, err := Exists(f.Name())
	require.NoError(t, err)
	assert.True(t, exists, "Exists() returned false for existing file")

	nonExistentFile := "non-existent-file.txt"
	exists, err = Exists(nonExistentFile)
	require.NoError(t, err)
	assert.False(t, exists, "Exists() returned true for non-existent file")
}

func TestDirExists(t *testing.T) {
	t.Parallel()

	t.Run("DirExists", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		exists, err := DirExists(dir)
		require.NoError(t, err)
		assert.True(t, exists, "DirExists() returned false for existing directory")
	})
	t.Run("DirectoryDoesNotExist", func(t *testing.T) {
		t.Parallel()
		exists, err := DirExists("non-existent-dir")
		require.NoError(t, err)
		assert.False(t, exists, "DirExists() returned true for non-existent directory")
	})
	t.Run("FileIsDirectory", func(t *testing.T) {
		t.Parallel()
		f, err := os.CreateTemp(t.TempDir(), "test-file")
		require.NoError(t, err)
		defer func() { _ = os.Remove(f.Name()) }()
		exists, err := DirExists(f.Name())
		require.NoError(t, err)
		assert.False(t, exists, "DirExists() returned true for a file")
	})
}

func TestWriteAtomic(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		filePath string
		data     []byte
		wantErr  bool
	}{
		{
			name:     "valid write",
			filePath: "testfile.txt",
			data:     []byte("Hello, World!"),
			wantErr:  false,
		},
		{
			name:     "empty data",
			filePath: "emptyfile.txt",
			data:     []byte(""),
			wantErr:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := WriteAtomic(tt.filePath, tt.data)
			if tt.wantErr {
				require.Error(t, err)

				return
			}
			require.NoError(t, err)

			content, err := os.ReadFile(tt.filePath)
			require.NoError(t, err, "Failed to read file")
			assert.Equal(t, string(tt.data), string(content))
			_ = os.Remove(tt.filePath) // Clean up
		})
	}
}

func TestWriteAtomic_WithNestedDir(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	nestedPath := filepath.Join(tmpDir, "nested", "file.txt")

	// Should fail because nested directory doesn't exist
	err := WriteAtomic(nestedPath, []byte("test"))
	require.Error(t, err, "expected error when parent directory doesn't exist")
}

func TestWriteAtomic_OverwriteExisting(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "existing.txt")

	// Create initial file
	err := WriteAtomic(filePath, []byte("initial"))
	require.NoError(t, err)

	// Overwrite with new content
	err = WriteAtomic(filePath, []byte("updated"))
	require.NoError(t, err)

	content, err := os.ReadFile(filePath)
	require.NoError(t, err)
	assert.Equal(t, "updated", string(content))
}

func TestWriteAtomic_NotRegularFile(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	dirPath := filepath.Join(tmpDir, "isdir")

	// Create a directory at the target path
	err := os.Mkdir(dirPath, 0o750)
	require.NoError(t, err)

	// Should fail because target exists but is not a regular file
	err = WriteAtomic(dirPath, []byte("test"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a regular file")
}

func TestExists_SymlinkToFile(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "realfile.txt")
	linkPath := filepath.Join(tmpDir, "symlink.txt")

	// Create a real file
	err := os.WriteFile(filePath, []byte("content"), 0o600)
	require.NoError(t, err)

	// Create symlink to the file
	err = os.Symlink(filePath, linkPath)
	require.NoError(t, err)

	exists, err := Exists(linkPath)
	require.NoError(t, err)
	assert.True(t, exists, "symlink to existing file should return true")
}

func TestExists_BrokenSymlink(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	linkPath := filepath.Join(tmpDir, "broken_symlink.txt")

	// Create symlink to non-existent file
	err := os.Symlink(filepath.Join(tmpDir, "nonexistent"), linkPath)
	require.NoError(t, err)

	exists, err := Exists(linkPath)
	// Broken symlink should return false (target doesn't exist)
	require.NoError(t, err)
	assert.False(t, exists, "broken symlink should return false")
}

func TestDirExists_Symlink(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	realDir := filepath.Join(tmpDir, "realdir")
	linkPath := filepath.Join(tmpDir, "dirlink")

	// Create a real directory
	err := os.Mkdir(realDir, 0o750)
	require.NoError(t, err)

	// Create symlink to the directory
	err = os.Symlink(realDir, linkPath)
	require.NoError(t, err)

	exists, err := DirExists(linkPath)
	require.NoError(t, err)
	assert.True(t, exists, "symlink to existing directory should return true")
}

type fakeTempFile struct {
	name      string
	writeErr  error
	syncErr   error
	closeErr  error
	closeHits int
}

func (f *fakeTempFile) Write(p []byte) (int, error) {
	if f.writeErr != nil {
		return 0, f.writeErr
	}

	return len(p), nil
}

func (f *fakeTempFile) Sync() error {
	return f.syncErr
}

func (f *fakeTempFile) Close() error {
	f.closeHits++

	return f.closeErr
}

func (f *fakeTempFile) Name() string {
	return f.name
}

func TestWriteAtomicWithErrors(t *testing.T) {
	t.Parallel()

	runTest := func(t *testing.T, name string, tmp *fakeTempFile, ops fileOps, checkCleanup func()) {
		t.Helper()
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			err := writeFileAtomicPermWith("target", []byte("data"), permFile, ops)
			require.Error(t, err)
			require.GreaterOrEqual(t, tmp.closeHits, 1)
			if checkCleanup != nil {
				checkCleanup()
			}
		})
	}

	tmp := &fakeTempFile{name: "tempfile", writeErr: fakeError("write")}
	removed := false
	ops := fileOps{
		stat: func(string) (os.FileInfo, error) { return nil, os.ErrNotExist },
		createTemp: func(string, string) (tempFile, error) {
			return tmp, nil
		},
		chmod:  func(string, os.FileMode) error { return nil },
		rename: func(string, string) error { return nil },
		remove: func(string) error {
			removed = true

			return nil
		},
	}
	runTest(t, "write error triggers cleanup", tmp, ops, func() {
		require.True(t, removed)
	})

	tmpSync := &fakeTempFile{name: "tempfile", syncErr: fakeError("sync")}
	removedSync := false
	opsSync := fileOps{
		stat: func(string) (os.FileInfo, error) { return nil, os.ErrNotExist },
		createTemp: func(string, string) (tempFile, error) {
			return tmpSync, nil
		},
		chmod:  func(string, os.FileMode) error { return nil },
		rename: func(string, string) error { return nil },
		remove: func(string) error {
			removedSync = true

			return nil
		},
	}
	runTest(t, "sync error triggers cleanup", tmpSync, opsSync, func() {
		require.True(t, removedSync)
	})

	tmpClose := &fakeTempFile{name: "tempfile", closeErr: fakeError("close")}
	removedClose := false
	opsClose := fileOps{
		stat: func(string) (os.FileInfo, error) { return nil, os.ErrNotExist },
		createTemp: func(string, string) (tempFile, error) {
			return tmpClose, nil
		},
		chmod:  func(string, os.FileMode) error { return nil },
		rename: func(string, string) error { return nil },
		remove: func(string) error {
			removedClose = true

			return nil
		},
	}
	runTest(t, "close error returns error", tmpClose, opsClose, func() {
		require.True(t, removedClose)
	})

	tmpRename := &fakeTempFile{name: "tempfile"}
	removedRename := false
	opsRename := fileOps{
		stat: func(string) (os.FileInfo, error) { return nil, os.ErrNotExist },
		createTemp: func(string, string) (tempFile, error) {
			return tmpRename, nil
		},
		chmod:  func(string, os.FileMode) error { return nil },
		rename: func(string, string) error { return fakeError("rename") },
		remove: func(string) error {
			removedRename = true

			return nil
		},
	}
	runTest(t, "rename error returns error", tmpRename, opsRename, func() {
		require.True(t, removedRename)
	})
}

type fakeError string

func (e fakeError) Error() string { return string(e) }

func TestWriteAtomicWith_CreateTempError(t *testing.T) {
	t.Parallel()

	// Test that createTemp error is propagated
	ops := fileOps{
		stat: func(string) (os.FileInfo, error) { return nil, os.ErrNotExist },
		createTemp: func(string, string) (tempFile, error) {
			return nil, fakeError("createTemp failed")
		},
		chmod:  func(string, os.FileMode) error { return nil },
		rename: func(string, string) error { return nil },
		remove: func(string) error { return nil },
	}

	err := writeFileAtomicPermWith("target", []byte("data"), permFile, ops)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "createTemp failed")
}

func TestWriteAtomicWith_StatError(t *testing.T) {
	t.Parallel()

	// When stat returns an error other than os.ErrNotExist, it should still
	// proceed because the code only checks for "not a regular file".
	ops := fileOps{
		stat: func(string) (os.FileInfo, error) {
			// Return permission denied - should still proceed
			return nil, os.ErrPermission
		},
		createTemp: func(string, string) (tempFile, error) {
			return &fakeTempFile{name: "tempfile"}, nil
		},
		chmod:  func(string, os.FileMode) error { return nil },
		rename: func(string, string) error { return nil },
		remove: func(string) error { return nil },
	}

	// The function should proceed even when stat returns an error
	// (as long as it does not find a non-regular file)
	err := writeFileAtomicPermWith("target", []byte("data"), permFile, ops)
	require.NoError(t, err)
}

func TestWriteAtomic_LargeData(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "largefile.bin")

	// Create 1MB of data
	largeData := make([]byte, 1024*1024)
	for i := range largeData {
		largeData[i] = byte(i % 256)
	}

	err := WriteAtomic(filePath, largeData)
	require.NoError(t, err)

	content, err := os.ReadFile(filePath)
	require.NoError(t, err)
	require.Equal(t, largeData, content)
}

func TestWriteAtomic_EmptyFile(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "empty.txt")

	err := WriteAtomic(filePath, []byte{})
	require.NoError(t, err)

	content, err := os.ReadFile(filePath)
	require.NoError(t, err)
	require.Empty(t, content)
}

func TestDirExists_BrokenSymlink(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	linkPath := filepath.Join(tmpDir, "broken_symlink")

	// Create symlink to non-existent directory
	err := os.Symlink(filepath.Join(tmpDir, "nonexistent"), linkPath)
	require.NoError(t, err)

	// Broken symlink should return false
	exists, err := DirExists(linkPath)
	require.NoError(t, err)
	assert.False(t, exists, "broken symlink should return false")
}

func TestCopyFile_Success(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()

	src := filepath.Join(tmpDir, "src.bin")
	require.NoError(t, os.WriteFile(src, []byte("hello world"), 0o600))

	dst := filepath.Join(tmpDir, "subdir", "dst.bin")
	err := CopyFile(src, dst)
	require.NoError(t, err)

	got, err := os.ReadFile(dst)
	require.NoError(t, err)
	assert.Equal(t, "hello world", string(got))
}

func TestCopyFile_OverwriteExisting(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()

	src := filepath.Join(tmpDir, "src.bin")
	require.NoError(t, os.WriteFile(src, []byte("new content"), 0o600))

	dst := filepath.Join(tmpDir, "dst.bin")
	require.NoError(t, os.WriteFile(dst, []byte("old content"), 0o600))

	err := CopyFile(src, dst)
	require.NoError(t, err)

	got, err := os.ReadFile(dst)
	require.NoError(t, err)
	assert.Equal(t, "new content", string(got))
}

func TestCopyFile_SrcNotFound(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	err := CopyFile(filepath.Join(tmpDir, "missing"), filepath.Join(tmpDir, "dst"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "open source file")
}

func TestCopyFile_MkdirAllError(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	src := filepath.Join(tmpDir, "src")
	require.NoError(t, os.WriteFile(src, []byte("data"), 0o600))

	// /proc/1/ is not writable so MkdirAll will fail.
	err := CopyFile(src, "/proc/1/impossible/dst")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "create parent directory")
}

func TestCopyFile_CreateError(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	src := filepath.Join(tmpDir, "src")
	require.NoError(t, os.WriteFile(src, []byte("data"), 0o600))

	// Make parent dir read-only so os.Create fails.
	readOnlyDir := filepath.Join(tmpDir, "readonly")
	require.NoError(t, os.MkdirAll(readOnlyDir, 0o750))
	require.NoError(t, os.Chmod(readOnlyDir, 0o555)) //nolint:gosec // intentionally read-only for test
	t.Cleanup(func() {
		_ = os.Chmod(readOnlyDir, 0o755) //nolint:gosec // restore writability for cleanup
	})

	err := CopyFile(src, filepath.Join(readOnlyDir, "dst"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "create destination file")
}

func TestRemoveIfExists_FileExists(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "removeme.txt")
	require.NoError(t, os.WriteFile(filePath, []byte("data"), 0o600))

	err := RemoveIfExists(filePath)
	require.NoError(t, err)

	exists, err := Exists(filePath)
	require.NoError(t, err)
	assert.False(t, exists, "file should no longer exist after RemoveIfExists")
}

func TestRemoveIfExists_FileNotExists(t *testing.T) {
	t.Parallel()

	err := RemoveIfExists(filepath.Join(t.TempDir(), "nonexistent.txt"))
	require.NoError(t, err)
}

func TestRemoveIfExists_EmptyDir(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	dirPath := filepath.Join(tmpDir, "emptydir")
	require.NoError(t, os.Mkdir(dirPath, 0o750))

	err := RemoveIfExists(dirPath)
	require.NoError(t, err)

	exists, err := Exists(dirPath)
	require.NoError(t, err)
	assert.False(t, exists, "empty directory should no longer exist after RemoveIfExists")
}

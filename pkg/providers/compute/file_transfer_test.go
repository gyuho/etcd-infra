package compute

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type mockTransferInstance struct {
	id string

	runResult *ExecuteResult
	runErr    error
	lastCmd   []string

	writeFileErr error
	writeCalls   int
}

func (m *mockTransferInstance) ID() string          { return m.id }
func (m *mockTransferInstance) PublicIPv4() string  { return "" }
func (m *mockTransferInstance) PrivateIPv4() string { return "" }
func (m *mockTransferInstance) State() InstanceState {
	return InstanceStateRunning
}

func (m *mockTransferInstance) RunCommand(ctx context.Context, command []string) (*ExecuteResult, error) {
	return m.RunCommandWithOptions(ctx, command, nil)
}

func (m *mockTransferInstance) RunCommandWithOptions(_ context.Context, command []string, _ *RunCommandOptions) (*ExecuteResult, error) {
	m.lastCmd = append([]string(nil), command...)
	if m.runErr != nil {
		return nil, m.runErr
	}
	if m.runResult != nil {
		return m.runResult, nil
	}
	return &ExecuteResult{ExitCode: 0}, nil
}

func (m *mockTransferInstance) WriteFile(context.Context, string, string) error {
	m.writeCalls++
	return m.writeFileErr
}

func (m *mockTransferInstance) CopyFile(context.Context, string, string) error { return m.writeFileErr }
func (m *mockTransferInstance) CopyDirectory(context.Context, string, string) error {
	return m.writeFileErr
}

func (m *mockTransferInstance) DownloadFile(context.Context, string, string) error {
	return m.writeFileErr
}

// effectiveTimeout tests

func TestEffectiveTimeout_UsesContextDeadline(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	got := effectiveTimeout(ctx, 99*time.Second)
	// Should be close to 10s (the context deadline), not the 99s fallback.
	require.LessOrEqual(t, got, 10*time.Second)
	require.Greater(t, got, 9*time.Second)
}

func TestEffectiveTimeout_UsesFallbackWithoutDeadline(t *testing.T) {
	t.Parallel()

	got := effectiveTimeout(context.Background(), 42*time.Second)
	require.Equal(t, 42*time.Second, got)
}

func TestEffectiveTimeout_UsesFallbackWhenDeadlinePassed(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-1*time.Second))
	defer cancel()

	got := effectiveTimeout(ctx, 15*time.Second)
	require.Equal(t, 15*time.Second, got, "should fall back when deadline already passed")
}

func TestWriteFileFallbackNonZeroExit(t *testing.T) {
	t.Parallel()

	inst := &mockTransferInstance{
		id:           "node-exit",
		writeFileErr: NotSupportedError("mock", "WriteFile"),
		runResult:    &ExecuteResult{ExitCode: 1, Stderr: "permission denied"},
	}
	err := WriteFile(context.Background(), inst, "/tmp/a", "data")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "permission denied")
}

func TestWriteFileFallbackRunError(t *testing.T) {
	t.Parallel()

	inst := &mockTransferInstance{
		id:           "node-runerr",
		writeFileErr: NotSupportedError("mock", "WriteFile"),
		runErr:       errors.New("transport closed"),
	}
	err := WriteFile(context.Background(), inst, "/tmp/a", "data")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "transport closed")
}

func TestCopyFileLocalReadError(t *testing.T) {
	t.Parallel()

	inst := &mockTransferInstance{
		id:           "node-nofile",
		writeFileErr: NotSupportedError("mock", "CopyFile"),
	}
	err := CopyFile(context.Background(), inst, "/nonexistent/path.txt", "/tmp/dest")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "read local file")
}

func TestDownloadFileCreatesParentDir(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	localPath := filepath.Join(tmpDir, "sub", "dir", "file.txt")

	inst := &mockTransferInstance{
		id:           "node-mkdir",
		writeFileErr: NotSupportedError("mock", "DownloadFile"),
		runResult:    &ExecuteResult{ExitCode: 0, Stdout: "data"},
	}
	err := DownloadFile(context.Background(), inst, "/remote/file", localPath)
	require.NoError(t, err)

	data, readErr := os.ReadFile(localPath)
	require.NoError(t, readErr)
	assert.Equal(t, "data", string(data))
}

func TestCopyDirectoryRejectsRegularFile(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	regularFile := filepath.Join(tmpDir, "file.txt")
	require.NoError(t, os.WriteFile(regularFile, []byte("data"), 0o600))

	inst := &mockTransferInstance{
		id:           "node-notdir",
		writeFileErr: NotSupportedError("mock", "CopyDirectory"),
	}
	err := CopyDirectory(context.Background(), inst, regularFile, "/remote/dir")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a directory")
}

func TestCopyDirectoryFallback(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "f.txt"), []byte("x"), 0o600))

	inst := &mockTransferInstance{
		id:           "node-dir",
		writeFileErr: NotSupportedError("mock", "CopyDirectory"),
		runResult:    &ExecuteResult{ExitCode: 0},
	}
	err := CopyDirectory(context.Background(), inst, dir, "/remote/dest")
	require.NoError(t, err)
	require.NotEmpty(t, inst.lastCmd)
	assert.Equal(t, "sh", inst.lastCmd[0])
}

func TestCopyDirectoryFallbackNonZeroExit(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "f.txt"), []byte("x"), 0o600))

	inst := &mockTransferInstance{
		id:           "node-dirfail",
		writeFileErr: NotSupportedError("mock", "CopyDirectory"),
		runResult:    &ExecuteResult{ExitCode: 2, Stderr: "tar: error"},
	}
	err := CopyDirectory(context.Background(), inst, dir, "/remote/dest")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "tar: error")
}

// WriteFile on a plain Instance (no FileTransferInstance) uses the fallback path.
func TestWriteFileUsesCommandFallbackForPlainInstance(t *testing.T) {
	t.Parallel()

	inst := &mockInstance{id: "plain-node"}
	err := WriteFile(context.Background(), inst, "/tmp/test.txt", "content")
	require.NoError(t, err)
	// mockInstance.RunCommandWithOptions returns ExitCode 0.
	// Verify the fallback shell command was invoked.
	require.NotEmpty(t, inst.lastCommand)
	assert.Equal(t, "sh", inst.lastCommand[0])
}

func TestWriteFileUsesCapability(t *testing.T) {
	t.Parallel()

	inst := &mockTransferInstance{id: "node-1"}
	err := WriteFile(context.Background(), inst, "/tmp/a", "hello")
	require.NoError(t, err)
	require.Equal(t, 1, inst.writeCalls)
}

func TestWriteFileFallsBackWhenNotSupported(t *testing.T) {
	t.Parallel()

	inst := &mockTransferInstance{
		id:           "node-2",
		writeFileErr: NotSupportedError("mock", "WriteFile"),
		runResult:    &ExecuteResult{ExitCode: 0},
	}
	err := WriteFile(context.Background(), inst, "/tmp/a", "hello")
	require.NoError(t, err)
	require.NotEmpty(t, inst.lastCmd)
	require.Equal(t, "sh", inst.lastCmd[0])
}

func TestWriteFileReturnsCapabilityError(t *testing.T) {
	t.Parallel()

	inst := &mockTransferInstance{
		id:           "node-3",
		writeFileErr: errors.New("boom"),
	}
	err := WriteFile(context.Background(), inst, "/tmp/a", "hello")
	require.Error(t, err)
	require.Contains(t, err.Error(), "boom")
}

func TestCopyFileFallback(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	localPath := filepath.Join(tmpDir, "local.txt")
	require.NoError(t, os.WriteFile(localPath, []byte("content"), 0o600))

	inst := &mockTransferInstance{
		id:           "node-4",
		writeFileErr: NotSupportedError("mock", "CopyFile"),
		runResult:    &ExecuteResult{ExitCode: 0},
	}
	err := CopyFile(context.Background(), inst, localPath, "/tmp/remote.txt")
	require.NoError(t, err)
	require.NotEmpty(t, inst.lastCmd)
}

func TestDownloadFileFallback(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	localPath := filepath.Join(tmpDir, "downloaded.txt")

	inst := &mockTransferInstance{
		id:           "node-5",
		writeFileErr: NotSupportedError("mock", "DownloadFile"),
		runResult: &ExecuteResult{
			ExitCode: 0,
			Stdout:   "downloaded-content",
		},
	}
	err := DownloadFile(context.Background(), inst, "/tmp/remote.txt", localPath)
	require.NoError(t, err)

	data, readErr := os.ReadFile(localPath)
	require.NoError(t, readErr)
	require.Equal(t, "downloaded-content", strings.TrimSpace(string(data)))
}

// ─── Path validation tests ──────────────────────────────────────────────────

func TestWriteFileRejectsEmptyPath(t *testing.T) {
	t.Parallel()

	inst := &mockTransferInstance{id: "node-v"}
	require.ErrorContains(t, WriteFile(context.Background(), inst, "", "data"), "path is required")
	require.ErrorContains(t, WriteFile(context.Background(), inst, "   ", "data"), "path is required")
}

func TestWriteFileRejectsNilInstance(t *testing.T) {
	t.Parallel()

	require.ErrorContains(t, WriteFile(context.Background(), nil, "/tmp/a", "data"), "instance is required")
}

func TestCopyFileRejectsEmptyPaths(t *testing.T) {
	t.Parallel()

	inst := &mockTransferInstance{id: "node-v"}
	require.ErrorContains(t, CopyFile(context.Background(), inst, "", "/remote"), "local path is required")
	require.ErrorContains(t, CopyFile(context.Background(), inst, "/local", ""), "remote path is required")
	require.ErrorContains(t, CopyFile(context.Background(), inst, "  ", "/remote"), "local path is required")
	require.ErrorContains(t, CopyFile(context.Background(), inst, "/local", "  "), "remote path is required")
}

func TestCopyFileRejectsNilInstance(t *testing.T) {
	t.Parallel()

	require.ErrorContains(t, CopyFile(context.Background(), nil, "/a", "/b"), "instance is required")
}

func TestCopyDirectoryRejectsEmptyPaths(t *testing.T) {
	t.Parallel()

	inst := &mockTransferInstance{id: "node-v"}
	require.ErrorContains(t, CopyDirectory(context.Background(), inst, "", "/remote"), "local path is required")
	require.ErrorContains(t, CopyDirectory(context.Background(), inst, "/local", ""), "remote path is required")
}

func TestCopyDirectoryRejectsNilInstance(t *testing.T) {
	t.Parallel()

	require.ErrorContains(t, CopyDirectory(context.Background(), nil, "/a", "/b"), "instance is required")
}

func TestDownloadFileRejectsEmptyPaths(t *testing.T) {
	t.Parallel()

	inst := &mockTransferInstance{id: "node-v"}
	require.ErrorContains(t, DownloadFile(context.Background(), inst, "", "/local"), "remote path is required")
	require.ErrorContains(t, DownloadFile(context.Background(), inst, "/remote", ""), "local path is required")
}

func TestDownloadFileRejectsNilInstance(t *testing.T) {
	t.Parallel()

	require.ErrorContains(t, DownloadFile(context.Background(), nil, "/a", "/b"), "instance is required")
}

// ─── CreateTarArchive tests ─────────────────────────────────────────────────

func TestCreateTarArchive_RegularFiles(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "sub"), 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hello"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "sub", "b.txt"), []byte("world"), 0o600))

	var buf bytes.Buffer
	require.NoError(t, CreateTarArchive(dir, &buf))

	// Verify the tar contains both files.
	tr := tar.NewReader(&buf)
	names := map[string]string{}
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		require.NoError(t, err)
		if hdr.Typeflag == tar.TypeReg {
			data, readErr := io.ReadAll(tr)
			require.NoError(t, readErr)
			names[hdr.Name] = string(data)
		}
	}
	assert.Equal(t, "hello", names["a.txt"])
	assert.Equal(t, "world", names["sub/b.txt"])
}

func TestCreateTarArchive_Symlink(t *testing.T) {
	t.Parallel()

	if runtime.GOOS == "windows" {
		t.Skip("symlinks require elevated privileges on Windows")
	}

	dir := t.TempDir()
	target := filepath.Join(dir, "target.txt")
	link := filepath.Join(dir, "link.txt")
	require.NoError(t, os.WriteFile(target, []byte("data"), 0o600))
	require.NoError(t, os.Symlink("target.txt", link))

	var buf bytes.Buffer
	require.NoError(t, CreateTarArchive(dir, &buf))

	tr := tar.NewReader(&buf)
	found := false
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		require.NoError(t, err)
		if hdr.Name == "link.txt" {
			assert.Equal(t, byte(tar.TypeSymlink), hdr.Typeflag, "symlink should be recorded as TypeSymlink")
			assert.Equal(t, "target.txt", hdr.Linkname, "symlink target should be preserved")
			found = true
		}
	}
	require.True(t, found, "symlink entry should be present in tar")
}

func TestCreateTarArchive_EmptyDir(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "empty"), 0o750))

	var buf bytes.Buffer
	require.NoError(t, CreateTarArchive(dir, &buf))

	tr := tar.NewReader(&buf)
	found := false
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		require.NoError(t, err)
		if hdr.Name == "empty/" || hdr.Name == "empty" {
			assert.Equal(t, byte(tar.TypeDir), hdr.Typeflag)
			found = true
		}
	}
	require.True(t, found, "empty directory should be recorded in tar")
}

func TestCreateTarArchive_MissingRoot(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	err := CreateTarArchive(filepath.Join(t.TempDir(), "missing"), &buf)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "walk directory")
}

func TestCreateTarArchive_CloseErrorSurfaces(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "f.txt"), []byte("x"), 0o600))

	// A writer that fails on writes beyond a small initial buffer simulates
	// a broken pipe or full disk when the tar trailer is flushed.
	w := &limitedWriter{limit: 1} // Allow almost nothing.
	err := CreateTarArchive(dir, w)
	require.Error(t, err, "should surface write/close error")
}

// limitedWriter returns an error after limit bytes have been written.
type limitedWriter struct {
	written int
	limit   int
}

func (w *limitedWriter) Write(p []byte) (int, error) {
	if w.written+len(p) > w.limit {
		return 0, errors.New("write limit exceeded")
	}
	w.written += len(p)
	return len(p), nil
}

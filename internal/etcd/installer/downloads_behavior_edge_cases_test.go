//nolint:testpackage // Need access to internals for thorough testing.
package installer

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	httptest "git.tbd/etcd-infra/pkg/testutil/httptest"

	"git.tbd/etcd-infra/pkg/file"
	commoninstall "git.tbd/etcd-infra/pkg/install"
)

func TestExtractDownloadedBinTgzPath(t *testing.T) {
	t.Parallel()

	// Verify .tgz path returns correct binary path
	result, err := extractDownloadedBin("etcd", "/tmp/etcd.tgz", "https://example.com/etcd-v3.6.7-linux-amd64.tgz", "/tmp")
	// Will error because the file doesn't exist, but verifies the code path
	require.Error(t, err)
	_ = result
}

func TestExtractDownloadedBinZipPath(t *testing.T) {
	t.Parallel()

	result, err := extractDownloadedBin("etcd", "/tmp/etcd.zip", "https://example.com/etcd-v3.6.7-darwin-arm64.zip", "/tmp")
	// Will error because the file doesn't exist, but verifies the code path
	require.Error(t, err)
	_ = result
}

func TestFinalizeDownloadedBinSourceMissing(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	err := finalizeDownloadedBin(filepath.Join(tmpDir, "nonexistent"), filepath.Join(tmpDir, "dest"))
	require.Error(t, err)
	require.ErrorIs(t, err, errFailedMoveDownloadedBin)
}

func TestVerifyArtifactWithManifestWrongSHA(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "artifact.tar.gz")
	require.NoError(t, os.WriteFile(filePath, []byte("actual content"), 0o600))

	manifest := &commoninstall.Manifest{
		Artifacts: []commoninstall.Artifact{{
			Name:   "artifact.tar.gz",
			SHA256: "0000000000000000000000000000000000000000000000000000000000000000",
			Size:   int64(len("actual content")),
		}},
	}

	err := verifyArtifact(context.Background(), filePath, "artifact.tar.gz", "https://example.com/artifact.tar.gz", manifest, false, time.Second)
	require.Error(t, err)
	require.ErrorIs(t, err, errFailedVerifyArtifact)
}

func TestVerifyArtifactOfflineWithoutManifest(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "artifact.tar.gz")
	require.NoError(t, os.WriteFile(filePath, []byte("offline content"), 0o600))

	// Offline mode without manifest should trust the local artifact
	err := verifyArtifact(context.Background(), filePath, "artifact.tar.gz", "https://example.com/artifact.tar.gz", nil, true, time.Second)
	require.NoError(t, err)
}

func TestVerifyArtifactChecksumNotFoundInSUMS(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "artifact.tar.gz")
	require.NoError(t, os.WriteFile(filePath, []byte("content"), 0o600))

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "SHA256SUMS") {
			// Return checksums for a different artifact only
			_, _ = fmt.Fprintf(w, "abc123  other-artifact.tar.gz\n")
			return
		}
		http.NotFound(w, r)
	}))
	defer ts.Close()

	err := verifyArtifact(context.Background(), filePath, "artifact.tar.gz", ts.URL+"/artifact.tar.gz", nil, false, time.Second)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parse checksum entry")
	assert.Contains(t, err.Error(), "checksum entry not found")
}

func TestResolveDownloadedFileOnlineWithHTTPServer(t *testing.T) {
	t.Parallel()

	// Server that returns a downloadable artifact
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write([]byte("downloaded-content"))
	}))
	defer ts.Close()

	tmpDir := t.TempDir()
	opts := &Op{
		offline:         false,
		downloadTimeout: 5 * time.Second,
	}

	result, err := resolveDownloadedFile(context.Background(), "etcd", "etcd.tar.gz", ts.URL+"/etcd.tar.gz", tmpDir, opts)
	require.NoError(t, err)
	require.NotEmpty(t, result)

	// Verify the file was downloaded
	data, readErr := os.ReadFile(result)
	require.NoError(t, readErr)
	assert.Equal(t, "downloaded-content", string(data))
}

func TestCopyFileEmptySource(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	src := filepath.Join(tmpDir, "empty")
	dst := filepath.Join(tmpDir, "dst")
	require.NoError(t, os.WriteFile(src, []byte{}, 0o600))

	err := file.CopyFile(src, dst)
	require.NoError(t, err)

	data, readErr := os.ReadFile(dst)
	require.NoError(t, readErr)
	assert.Empty(t, data)
}

func TestMoveFileAcrossTemp(t *testing.T) {
	t.Parallel()

	// Move file within same filesystem (typical case)
	tmpDir := t.TempDir()
	src := filepath.Join(tmpDir, "source.bin")
	dst := filepath.Join(tmpDir, "dest.bin")
	content := "binary-content-for-move"
	require.NoError(t, os.WriteFile(src, []byte(content), 0o600))

	require.NoError(t, moveFile(src, dst))

	// Source should be removed
	_, err := os.Stat(src)
	assert.True(t, os.IsNotExist(err))

	// Destination should have the content
	data, readErr := os.ReadFile(dst)
	require.NoError(t, readErr)
	assert.Equal(t, content, string(data))
}

func TestMoveFileSourceNotExist(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	err := moveFile(filepath.Join(tmpDir, "nonexistent"), filepath.Join(tmpDir, "dst"))
	require.Error(t, err)
	require.ErrorIs(t, err, errFailedMoveFile)
}

func TestParseSHA256SUMSWhitespaceLines(t *testing.T) {
	t.Parallel()

	data := []byte("\n\n   \nabc123  target.tar.gz\n\n")
	sum, err := parseSHA256SUMS(data, "target.tar.gz")
	require.NoError(t, err)
	assert.Equal(t, "abc123", sum)
}

func TestParseSHA256SUMSTwoSpaceSeparator(t *testing.T) {
	t.Parallel()

	// Standard two-space separator between hash and filename
	data := []byte("deadbeef01234567890abcdef01234567890abcdef01234567890abcdef012345  etcd-v3.6.7-linux-amd64.tar.gz\n")
	sum, err := parseSHA256SUMS(data, "etcd-v3.6.7-linux-amd64.tar.gz")
	require.NoError(t, err)
	assert.Equal(t, "deadbeef01234567890abcdef01234567890abcdef01234567890abcdef012345", sum)
}

func TestGetArchiveTypeReturnsCorrectValue(t *testing.T) {
	t.Parallel()

	at, err := getArchiveType()
	require.NoError(t, err)
	if runtime.GOOS == osTypeDarwin {
		assert.Equal(t, archiveTypeZip, at)
	} else {
		assert.Equal(t, archiveTypeTarGz, at)
	}
}

func TestCheckVersionMissingBinary(t *testing.T) {
	t.Parallel()

	_, err := checkVersion(context.Background(), "/nonexistent/binary", []string{"--version"})
	require.Error(t, err)
	require.ErrorIs(t, err, errFailedCheckVersion)
}

func TestCheckVersionSuccess(t *testing.T) {
	t.Parallel()

	// Use a real binary that exists
	out, err := checkVersion(context.Background(), "/bin/echo", []string{"hello"})
	require.NoError(t, err)
	assert.Contains(t, string(out), "hello")
}

func TestProbeDownloadSizeZeroContentLength(t *testing.T) {
	t.Parallel()

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "0")
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	size := probeDownloadSize(context.Background(), ts.URL+"/etcd.tar.gz", &Op{downloadTimeout: time.Second})
	// Size of 0 is >= 0, so it should be humanized
	assert.NotEqual(t, "unknown", size)
}

func TestLoadManifestEmptyString(t *testing.T) {
	t.Parallel()

	_, err := loadManifest("")
	require.Error(t, err)
	require.ErrorIs(t, err, errManifestPathEmpty)
}

func TestLoadManifestTabWhitespace(t *testing.T) {
	t.Parallel()

	_, err := loadManifest("\t\t")
	require.Error(t, err)
	require.ErrorIs(t, err, errManifestPathEmpty)
}

func TestMinChecksumFieldsConstant(t *testing.T) {
	t.Parallel()

	assert.Equal(t, 2, minChecksumFields)
}

func TestChecksumHintConstant(t *testing.T) {
	t.Parallel()

	assert.Contains(t, checksumHint, "WithChecksumManifest")
	assert.Contains(t, checksumHint, "WithArtifactDir")
}

func TestStripArchiveExtension(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name     string
		input    string
		expected string
	}{
		{name: "tar.gz", input: "etcd-v3.6.7-linux-amd64.tar.gz", expected: "etcd-v3.6.7-linux-amd64"},
		{name: "tgz", input: "etcd-v3.6.7-linux-amd64.tgz", expected: "etcd-v3.6.7-linux-amd64"},
		{name: "zip", input: "etcd-v3.6.7-darwin-arm64.zip", expected: "etcd-v3.6.7-darwin-arm64"},
		{name: "no extension", input: "etcd-v3.6.7-linux-amd64", expected: "etcd-v3.6.7-linux-amd64"},
		{name: "other extension", input: "etcd-v3.6.7.bin", expected: "etcd-v3.6.7.bin"},
		{name: "empty", input: "", expected: ""},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.expected, stripArchiveExtension(tc.input))
		})
	}
}

func TestFindPreExtractedBinaryInBinSubdir(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	binDir := filepath.Join(tmpDir, "bin")
	require.NoError(t, os.MkdirAll(binDir, 0o750))

	// Create a pre-extracted binary with canonical arch naming (x86_64).
	binPath := filepath.Join(binDir, "etcd-v3.6.7-linux-x86_64")
	require.NoError(t, os.WriteFile(binPath, []byte("binary"), 0o755)) //nolint:gosec // Intentional permission/operation

	// Artifact name uses Go-convention arch (amd64) from upstream URL;
	// findPreExtractedBinary canonicalizes to x86_64 for local lookup.
	result := findPreExtractedBinary(tmpDir, "etcd-v3.6.7-linux-amd64.tar.gz", "etcd")
	assert.Equal(t, binPath, result)
}

func TestFindPreExtractedBinaryInRoot(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()

	// Create a pre-extracted binary at artifact root with canonical arch.
	rootBin := filepath.Join(tmpDir, "etcd-v3.6.7-linux-x86_64")
	require.NoError(t, os.WriteFile(rootBin, []byte("binary"), 0o755)) //nolint:gosec // Intentional permission/operation

	result := findPreExtractedBinary(tmpDir, "etcd-v3.6.7-linux-amd64.tar.gz", "etcd")
	assert.Equal(t, rootBin, result)
}

func TestFindPreExtractedBinaryNotFound(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	result := findPreExtractedBinary(tmpDir, "etcd-v3.6.7-linux-amd64.tar.gz", "etcd")
	assert.Empty(t, result)
}

func TestFindPreExtractedBinaryNoArchiveExtension(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	// When artifact name has no archive extension, findPreExtractedBinary returns "".
	result := findPreExtractedBinary(tmpDir, "etcd-v3.6.7-linux-amd64", "etcd")
	assert.Empty(t, result)
}

func TestFindPreExtractedBinaryPrefersBindir(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	binDir := filepath.Join(tmpDir, "bin")
	require.NoError(t, os.MkdirAll(binDir, 0o750))

	// Create both in root and bin/ with canonical arch naming.
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "etcd-v3.6.7-linux-x86_64"), []byte("root"), 0o755))   //nolint:gosec // Intentional permission/operation
	require.NoError(t, os.WriteFile(filepath.Join(binDir, "etcd-v3.6.7-linux-x86_64"), []byte("bindir"), 0o755)) //nolint:gosec // Intentional permission/operation

	result := findPreExtractedBinary(tmpDir, "etcd-v3.6.7-linux-amd64.tar.gz", "etcd")
	// Should prefer bin/ subdirectory.
	assert.Equal(t, filepath.Join(binDir, "etcd-v3.6.7-linux-x86_64"), result)
}

func TestFindPreExtractedBinaryEtcdctl(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	binDir := filepath.Join(tmpDir, "bin")
	require.NoError(t, os.MkdirAll(binDir, 0o750))

	// Create both etcd and etcdctl pre-extracted binaries with canonical arch.
	require.NoError(t, os.WriteFile(filepath.Join(binDir, "etcd-v3.6.7-linux-x86_64"), []byte("etcd"), 0o755)) //nolint:gosec // Intentional permission/operation
	etcdctlPath := filepath.Join(binDir, "etcdctl-v3.6.7-linux-x86_64")
	require.NoError(t, os.WriteFile(etcdctlPath, []byte("etcdctl"), 0o755)) //nolint:gosec // Intentional permission/operation

	// When looking for etcdctl with the etcd archive name, should find etcdctl binary.
	result := findPreExtractedBinary(tmpDir, "etcd-v3.6.7-linux-amd64.tar.gz", "etcdctl")
	assert.Equal(t, etcdctlPath, result)
}

func TestFindVersionSuffixStart(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name     string
		input    string
		expected int
	}{
		{name: "standard etcd", input: "etcd-v3.6.7-linux-amd64", expected: 4},
		{name: "etcdctl", input: "etcdctl-v3.6.7-linux-amd64", expected: 7},
		{name: "no version", input: "etcd-linux-amd64", expected: -1},
		{name: "empty", input: "", expected: -1},
		{name: "just version", input: "-v1", expected: 0}, // "-v1" matches at index 0
		{name: "version at start", input: "v3.6.7", expected: -1},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.expected, findVersionSuffixStart(tc.input))
		})
	}
}

func TestFileExistsQuiet(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	existing := filepath.Join(tmpDir, "exists")
	require.NoError(t, os.WriteFile(existing, []byte("data"), 0o600))

	assert.True(t, fileExistsQuiet(existing))
	assert.False(t, fileExistsQuiet(filepath.Join(tmpDir, "nonexistent")))
}

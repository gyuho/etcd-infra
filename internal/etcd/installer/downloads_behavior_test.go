//nolint:all // Coverage-oriented tests intentionally use broad patterns for mock-heavy branch testing.
//nolint:testpackage // Need access to internals for thorough testing.
package installer

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"git.tbd/etcd-infra/pkg/file"
)

func TestResolveDownloadURLNilOptions(t *testing.T) {
	t.Parallel()

	_, err := resolveDownloadURL(nil)
	require.Error(t, err)
	require.ErrorIs(t, err, errDownloadOptsNil)
}

func TestResolveDownloadURLCustom(t *testing.T) {
	t.Parallel()

	opts := &Op{downloadURL: "https://example.com/etcd.tar.gz"}
	u, err := resolveDownloadURL(opts)
	require.NoError(t, err)
	assert.Equal(t, "https://example.com/etcd.tar.gz", u)
}

func TestResolveDownloadURLWithVersionOption(t *testing.T) {
	t.Parallel()

	opts := &Op{version: "v3.6.8"}
	u, err := resolveDownloadURL(opts)
	require.NoError(t, err)
	assert.Contains(t, u, "github.com/etcd-io/etcd")
	assert.Contains(t, u, "v3.6.8")
}

func TestResolveDownloadURLNoVersionOrURL(t *testing.T) {
	t.Parallel()

	opts := &Op{}
	_, err := resolveDownloadURL(opts)
	require.ErrorIs(t, err, errVersionRequired)
}

func TestProbeDownloadSizeNilOptions_Coverage(t *testing.T) {
	t.Parallel()

	size := probeDownloadSize(context.Background(), "https://example.com", nil)
	assert.Equal(t, "unknown", size)
}

func TestProbeDownloadSizeOffline_Coverage(t *testing.T) {
	t.Parallel()

	opts := &Op{offline: true}
	size := probeDownloadSize(context.Background(), "https://example.com", opts)
	assert.Equal(t, "unknown", size)
}

func TestResolveDownloadedFileNilOptions_Coverage(t *testing.T) {
	t.Parallel()

	_, err := resolveDownloadedFile(context.Background(), "etcd", "etcd.tar.gz", "https://example.com", "/tmp", nil)
	require.Error(t, err)
	require.ErrorIs(t, err, errDownloadOptsNil)
}

func TestResolveDownloadedFileOfflineNoDirSet(t *testing.T) {
	t.Parallel()

	opts := &Op{offline: true, artifactDir: ""}
	_, err := resolveDownloadedFile(context.Background(), "etcd", "etcd.tar.gz", "https://example.com", "/tmp", opts)
	require.Error(t, err)
	require.ErrorIs(t, err, errOfflineArtifactDirUnset)
}

func TestResolveDownloadedFileOfflineNotFound(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	opts := &Op{offline: true, artifactDir: tmpDir}
	_, err := resolveDownloadedFile(context.Background(), "etcd", "nonexistent.tar.gz", "https://example.com", tmpDir, opts)
	require.Error(t, err)
	require.ErrorIs(t, err, errOfflineArtifactNotFound)
}

func TestResolveDownloadedFileLocalArtifact(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	artifactPath := filepath.Join(tmpDir, "etcd.tar.gz")
	require.NoError(t, os.WriteFile(artifactPath, []byte("fake"), 0o600))

	opts := &Op{artifactDir: tmpDir}
	result, err := resolveDownloadedFile(context.Background(), "etcd", "etcd.tar.gz", "https://example.com", tmpDir, opts)
	require.NoError(t, err)
	assert.Equal(t, artifactPath, result)
}

func TestExtractDownloadedBinDefault_Coverage(t *testing.T) {
	t.Parallel()

	result, err := extractDownloadedBin("etcd", "/tmp/etcd", "https://example.com/etcd", "/tmp")
	require.NoError(t, err)
	assert.Equal(t, "/tmp/etcd", result)
}

func TestExtractDownloadedBinTarGz_Coverage(t *testing.T) {
	t.Parallel()

	result, err := extractDownloadedBin("etcd", "/tmp/etcd.tar.gz", "https://example.com/etcd-v3.6.5-linux-amd64.tar.gz", "/tmp")
	// Will error because the file doesn't exist, but tests the path
	require.Error(t, err)
	_ = result
}

func TestFinalizeDownloadedBin_Coverage(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	src := filepath.Join(tmpDir, "etcd-src")
	dst := filepath.Join(tmpDir, "etcd-dst")
	require.NoError(t, os.WriteFile(src, []byte("binary"), 0o600))

	err := finalizeDownloadedBin(src, dst)
	require.NoError(t, err)

	info, err := os.Stat(dst)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(binaryPerm), info.Mode().Perm())
}

func TestRetrySuccess(t *testing.T) {
	t.Parallel()

	calls := 0
	err := retry(3, 10*time.Millisecond, func() error {
		calls++
		return nil
	})
	require.NoError(t, err)
	assert.Equal(t, 1, calls)
}

func TestRetryEventualSuccess(t *testing.T) {
	t.Parallel()

	calls := 0
	err := retry(3, 10*time.Millisecond, func() error {
		calls++
		if calls < 3 {
			return assert.AnError
		}
		return nil
	})
	require.NoError(t, err)
	assert.Equal(t, 3, calls)
}

func TestRetryAllFail(t *testing.T) {
	t.Parallel()

	calls := 0
	err := retry(2, 10*time.Millisecond, func() error {
		calls++
		return assert.AnError
	})
	require.Error(t, err)
	assert.Equal(t, 2, calls)
}

func TestRetryZeroAttempts(t *testing.T) {
	t.Parallel()

	calls := 0
	err := retry(0, 10*time.Millisecond, func() error {
		calls++
		return assert.AnError
	})
	require.Error(t, err)
	assert.Equal(t, 1, calls, "zero attempts should default to 1")
}

func TestRetryNegativeDelay_Coverage(t *testing.T) {
	t.Parallel()

	calls := 0
	err := retry(2, -1*time.Second, func() error {
		calls++
		if calls < 2 {
			return assert.AnError
		}
		return nil
	})
	require.NoError(t, err)
	assert.Equal(t, 2, calls)
}

func TestLoadManifestEmptyPath(t *testing.T) {
	t.Parallel()

	_, err := loadManifest("")
	require.Error(t, err)
	require.ErrorIs(t, err, errManifestPathEmpty)
}

func TestLoadManifestWhitespacePath(t *testing.T) {
	t.Parallel()

	_, err := loadManifest("  ")
	require.Error(t, err)
	require.ErrorIs(t, err, errManifestPathEmpty)
}

func TestLoadManifestNonexistent(t *testing.T) {
	t.Parallel()

	_, err := loadManifest("/nonexistent/manifest.json")
	require.Error(t, err)
	require.ErrorIs(t, err, errFailedLoadManifest)
}

func TestFileExistsTrue(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	file := filepath.Join(tmpDir, "exists.txt")
	require.NoError(t, os.WriteFile(file, []byte("data"), 0o600))

	exists, err := fileExists(file)
	require.NoError(t, err)
	assert.True(t, exists)
}

func TestFileExistsFalse(t *testing.T) {
	t.Parallel()

	exists, err := fileExists("/nonexistent/file.txt")
	require.NoError(t, err)
	assert.False(t, exists)
}

func TestCopyFile(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	src := filepath.Join(tmpDir, "src.txt")
	dst := filepath.Join(tmpDir, "dst.txt")
	content := []byte("test content")
	require.NoError(t, os.WriteFile(src, content, 0o600))

	err := file.CopyFile(src, dst)
	require.NoError(t, err)

	data, err := os.ReadFile(dst)
	require.NoError(t, err)
	assert.Equal(t, content, data)
}

func TestCopyFileSrcNotFound(t *testing.T) {
	t.Parallel()

	err := file.CopyFile("/nonexistent/src", "/tmp/dst")
	require.Error(t, err)
}

func TestMoveFile(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	src := filepath.Join(tmpDir, "src.txt")
	dst := filepath.Join(tmpDir, "dst.txt")
	require.NoError(t, os.WriteFile(src, []byte("data"), 0o600))

	err := moveFile(src, dst)
	require.NoError(t, err)

	_, err = os.Stat(src)
	assert.True(t, os.IsNotExist(err), "source should be removed after move")

	data, err := os.ReadFile(dst)
	require.NoError(t, err)
	assert.Equal(t, []byte("data"), data)
}

func TestParseSHA256SUMS_Coverage(t *testing.T) {
	t.Parallel()

	data := []byte("abc123  etcd-v3.6.5.tar.gz\ndef456  etcdctl-v3.6.5.tar.gz\n")

	hash, err := parseSHA256SUMS(data, "etcd-v3.6.5.tar.gz")
	require.NoError(t, err)
	assert.Equal(t, "abc123", hash)

	hash2, err := parseSHA256SUMS(data, "etcdctl-v3.6.5.tar.gz")
	require.NoError(t, err)
	assert.Equal(t, "def456", hash2)
}

func TestParseSHA256SUMSNotFound(t *testing.T) {
	t.Parallel()

	data := []byte("abc123  etcd-v3.6.5.tar.gz\n")
	_, err := parseSHA256SUMS(data, "nonexistent.tar.gz")
	require.Error(t, err)
	require.ErrorIs(t, err, errChecksumEntryNotFound)
}

func TestParseSHA256SUMSEmpty(t *testing.T) {
	t.Parallel()

	_, err := parseSHA256SUMS([]byte(""), "anything")
	require.Error(t, err)
	require.ErrorIs(t, err, errChecksumEntryNotFound)
}

func TestParseSHA256SUMSMalformed(t *testing.T) {
	t.Parallel()

	data := []byte("onlyonefield\n")
	_, err := parseSHA256SUMS(data, "anything")
	require.Error(t, err)
	require.ErrorIs(t, err, errChecksumEntryNotFound)
}

func TestBuildDownloadURL_Coverage(t *testing.T) {
	t.Parallel()

	u, err := buildDownloadURL("v3.6.8")
	require.NoError(t, err)
	assert.Contains(t, u, "etcd-io/etcd")
	assert.Contains(t, u, "v3.6.8")
}

func TestGetOSType(t *testing.T) {
	t.Parallel()

	osType, err := getOSType()
	require.NoError(t, err)
	assert.Equal(t, runtime.GOOS, osType)
}

func TestGetArchType(t *testing.T) {
	t.Parallel()

	archType, err := getArchType()
	require.NoError(t, err)
	assert.Contains(t, []string{"amd64", "arm64"}, archType)
}

func TestGetArchiveType(t *testing.T) {
	t.Parallel()

	archiveType, err := getArchiveType()
	require.NoError(t, err)
	if runtime.GOOS == osTypeDarwin {
		assert.Equal(t, archiveTypeZip, archiveType)
	} else {
		assert.Equal(t, archiveTypeTarGz, archiveType)
	}
}

func TestErrorConstants(t *testing.T) {
	t.Parallel()

	require.Error(t, errManifestPathEmpty)
	require.Error(t, errDownloadOptsNil)
	require.Error(t, errOfflineArtifactDirUnset)
	require.Error(t, errOfflineArtifactNotFound)
	require.Error(t, errUnsupportedOS)
	require.Error(t, errUnsupportedArchitecture)
	require.Error(t, errChecksumManifestMissing)
	require.Error(t, errChecksumEntryNotFound)
	require.Error(t, errFailedDownloadSizeProbe)
	require.Error(t, errFailedParseDownloadURL)
	require.Error(t, errFailedDownloadChecksum)
	require.Error(t, errFailedVerifyArtifact)
	require.Error(t, errFailedMoveDownloadedBin)
	require.Error(t, errFailedSetBinaryMode)
	require.Error(t, errFailedLoadManifest)
	require.Error(t, errFailedCheckFileExistence)
	require.Error(t, errFailedMoveFile)
	require.Error(t, errFailedCheckVersion)
}

func TestErrVersionRequired(t *testing.T) {
	t.Parallel()

	assert.NotEmpty(t, errVersionRequired.Error())
}

func TestDownloadRetryConstants(t *testing.T) {
	t.Parallel()

	assert.Equal(t, 3, downloadRetryAttempts)
	assert.Equal(t, 2*time.Second, downloadRetryDelay)
	assert.Equal(t, os.FileMode(0o755), os.FileMode(binaryPerm))
}

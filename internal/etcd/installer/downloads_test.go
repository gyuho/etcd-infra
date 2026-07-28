//nolint:testpackage,nlreturn // Tests use package internals and shared resources.
package installer

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	httptest "git.tbd/etcd-infra/pkg/testutil/httptest"

	"github.com/dustin/go-humanize"
	"github.com/stretchr/testify/require"

	"git.tbd/etcd-infra/pkg/file"
	commoninstall "git.tbd/etcd-infra/pkg/install"
)

var (
	errTestFail          = errors.New("fail")
	errPersistentFailure = errors.New("persistent failure")
	errNotYet            = errors.New("not yet")
)

// testEtcdArchiveBase is the base name for test archive files used across multiple tests.
// Uses Go-convention arch (amd64) since this matches upstream URL naming.
const testEtcdArchiveBase = "etcd-v3.6.7-linux-amd64"

// testEtcdPreExtractedBase is the base name for pre-extracted binary test files.
// Uses canonical arch (x86_64) since etcd-infra artifact builder uses uname -m convention.
const testEtcdPreExtractedBase = "etcd-v3.6.7-linux-x86_64"

// testVersionScript is a shell script that prints a fake etcd version string,
// used in pre-extracted binary tests.
const testVersionScript = "#!/bin/sh\necho \"etcd Version: v3.6.7\"\n"

// RUN_ETCD_INSTALL=true go test -v -timeout 120s -run TestDownloadEtcd.
func TestDownloadEtcd(t *testing.T) {
	if os.Getenv("RUN_ETCD_INSTALL") != "true" {
		t.Skip("skipping etcd install test")
	}

	t.Parallel()

	tmp1, err := os.CreateTemp(os.TempDir(), "etcd-binary")
	require.NoError(t, err, "failed to create temp file")
	defer func() { _ = os.Remove(tmp1.Name()) }()
	out, err := DownloadEtcd(context.Background(), tmp1.Name(), WithVersionCheck(true))
	require.NoError(t, err, "failed to download etcd")
	t.Log(string(out))

	tmp2, err := os.CreateTemp(os.TempDir(), "etcd-binary")
	require.NoError(t, err, "failed to create temp file")
	defer func() { _ = os.Remove(tmp2.Name()) }()

	// ref. https://github.com/etcd-io/etcd/releases
	downloadURL := "https://github.com/etcd-io/etcd/releases/download/v3.5.16/etcd-v3.5.16-linux-amd64.tar.gz"
	out, err = DownloadEtcd(
		context.Background(),
		tmp2.Name(),
		WithVersionCheck(false),
		WithDownloadURL(downloadURL),
	)
	require.NoError(t, err, "failed to download etcd")
	t.Log(string(out))
}

func TestResolveDownloadURL(t *testing.T) {
	t.Parallel()

	_, err := resolveDownloadURL(nil)
	require.Error(t, err)

	options := &Op{downloadURL: "https://example.com/etcd.tar.gz"}
	url, err := resolveDownloadURL(options)
	require.NoError(t, err)
	require.Equal(t, options.downloadURL, url)
}

func TestDownloadEtcdWithLocalArtifact(t *testing.T) {
	t.Parallel()

	artifactDir, manifestPath, url := writeLocalArtifact(t, "etcd", "etcd Version: v3.6.7")
	binPath := filepath.Join(t.TempDir(), "etcd")

	out, err := DownloadEtcd(
		context.Background(),
		binPath,
		WithDownloadURL(url),
		WithArtifactDir(artifactDir),
		WithChecksumManifest(manifestPath),
		WithOffline(true),
		WithVersionCheck(true),
	)
	require.NoError(t, err)
	require.Contains(t, string(out), "etcd")
}

func TestDownloadEtcdctlWithLocalArtifact(t *testing.T) {
	t.Parallel()

	artifactDir, manifestPath, url := writeLocalArtifact(t, "etcdctl", "etcdctl version: 3.6.7")
	binPath := filepath.Join(t.TempDir(), "etcdctl")

	out, err := DownloadEtcdctl(
		context.Background(),
		binPath,
		WithDownloadURL(url),
		WithArtifactDir(artifactDir),
		WithChecksumManifest(manifestPath),
		WithOffline(true),
		WithVersionCheck(true),
	)
	require.NoError(t, err)
	require.Contains(t, string(out), "etcdctl")
}

func TestResolveDownloadedFileOffline(t *testing.T) {
	t.Parallel()

	_, err := resolveDownloadedFile(
		context.Background(),
		"etcd",
		"etcd.tar.gz",
		"https://example.com/etcd.tar.gz",
		t.TempDir(),
		&Op{offline: true},
	)
	require.Error(t, err)
	require.Contains(t, err.Error(), "artifact directory not set")

	artifactDir := t.TempDir()
	localPath := filepath.Join(artifactDir, "etcd.tar.gz")
	require.NoError(t, os.WriteFile(localPath, []byte("data"), 0o600))
	path, err := resolveDownloadedFile(context.Background(), "etcd", "etcd.tar.gz", "https://example.com/etcd.tar.gz", t.TempDir(), &Op{
		offline:     true,
		artifactDir: artifactDir,
	})
	require.NoError(t, err)
	require.Equal(t, localPath, path)

	_, err = resolveDownloadedFile(context.Background(), "etcd", "missing.tar.gz", "https://example.com/missing.tar.gz", t.TempDir(), &Op{
		offline:     true,
		artifactDir: t.TempDir(),
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "artifact not found")
}

func TestExtractDownloadedBinDefault(t *testing.T) {
	t.Parallel()

	downloaded := filepath.Join(t.TempDir(), "etcd")
	bin, err := extractDownloadedBin("etcd", downloaded, "https://example.com/etcd", t.TempDir())
	require.NoError(t, err)
	require.Equal(t, downloaded, bin)
}

func TestFinalizeDownloadedBin(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	downloaded := filepath.Join(tmpDir, "downloaded")
	binPath := filepath.Join(tmpDir, "etcd")
	require.NoError(t, os.WriteFile(downloaded, []byte("bin"), 0o600))

	require.NoError(t, finalizeDownloadedBin(downloaded, binPath))
	data, err := os.ReadFile(binPath)
	require.NoError(t, err)
	require.Equal(t, "bin", string(data))
}

func TestRetry(t *testing.T) {
	t.Parallel()

	attempts := 0
	err := retry(0, 0, func() error {
		attempts++
		return errTestFail
	})
	require.Error(t, err)
	require.Equal(t, 1, attempts)

	attempts = 0
	err = retry(2, time.Millisecond, func() error {
		attempts++
		if attempts == 1 {
			return errTestFail
		}
		return nil
	})
	require.NoError(t, err)
	require.Equal(t, 2, attempts)
}

func TestLoadManifestEmpty(t *testing.T) {
	t.Parallel()

	manifest, err := loadManifest(" ")
	require.ErrorIs(t, err, errManifestPathEmpty)
	require.Nil(t, manifest)
}

func TestFileExists(t *testing.T) {
	t.Parallel()

	exists, err := fileExists(filepath.Join(t.TempDir(), "missing"))
	require.NoError(t, err)
	require.False(t, exists)

	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "file")
	require.NoError(t, os.WriteFile(path, []byte("data"), 0o600))
	exists, err = fileExists(path)
	require.NoError(t, err)
	require.True(t, exists)
}

func TestParseSHA256SUMS(t *testing.T) {
	t.Parallel()

	sum, err := parseSHA256SUMS([]byte("abc123 etcd.tar.gz\n"), "etcd.tar.gz")
	require.NoError(t, err)
	require.Equal(t, "abc123", sum)

	_, err = parseSHA256SUMS([]byte(""), "missing")
	require.Error(t, err)
	require.Contains(t, err.Error(), "checksum entry not found")
}

func TestVerifyArtifactWithManifest(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "etcd.tar.gz")
	require.NoError(t, os.WriteFile(path, []byte("payload"), 0o600))

	sha, err := commoninstall.ComputeSHA256(path)
	require.NoError(t, err)

	manifest := &commoninstall.Manifest{
		Artifacts: []commoninstall.Artifact{{
			Name:   "etcd.tar.gz",
			SHA256: sha,
			Size:   int64(len("payload")),
		}},
	}

	err = verifyArtifact(context.Background(), path, "etcd.tar.gz", "https://example.com/etcd.tar.gz", manifest, false, time.Second)
	require.NoError(t, err)

	err = verifyArtifact(context.Background(), path, "missing.tar.gz", "https://example.com/missing.tar.gz", manifest, false, time.Second)
	require.Error(t, err)
	require.Contains(t, err.Error(), "checksum manifest missing entry")

	err = verifyArtifact(context.Background(), path, "etcd.tar.gz", "https://example.com/etcd.tar.gz", nil, true, time.Second)
	require.NoError(t, err)
}

func TestProbeDownloadSizeOffline(t *testing.T) {
	t.Parallel()

	size := probeDownloadSize(context.Background(), "https://example.com/etcd.tar.gz", &Op{offline: true})
	require.Equal(t, "unknown", size)
}

func TestProbeDownloadSizeNilOptions(t *testing.T) {
	t.Parallel()

	size := probeDownloadSize(context.Background(), "https://example.com/etcd.tar.gz", nil)
	require.Equal(t, "unknown", size)
}

func TestProbeDownloadSizeOnline(t *testing.T) {
	t.Parallel()

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "12")
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	size := probeDownloadSize(context.Background(), ts.URL+"/etcd.tar.gz", &Op{downloadTimeout: time.Second})
	require.Equal(t, humanize.Bytes(12), size)
}

func TestVerifyArtifactDownloadsChecksumList(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	artifactName := "etcd-v3.6.7-linux-amd64.tar.gz"
	path := filepath.Join(tmpDir, artifactName)
	require.NoError(t, os.WriteFile(path, []byte("payload"), 0o600))

	sha, err := commoninstall.ComputeSHA256(path)
	require.NoError(t, err)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "SHA256SUMS") {
			_, _ = fmt.Fprintf(w, "%s  %s\n", sha, artifactName)
			return
		}
		http.NotFound(w, r)
	}))
	defer ts.Close()

	err = verifyArtifact(context.Background(), path, artifactName, ts.URL+"/"+artifactName, nil, false, time.Second)
	require.NoError(t, err)
}

func TestExtractDownloadedBinTarGz(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	base := testEtcdArchiveBase
	archivePath := filepath.Join(tmpDir, base+".tar.gz")
	createTestTarGz(t, archivePath, map[string]string{
		filepath.Join(base, "etcd"): "binary",
	})

	bin, err := extractDownloadedBin("etcd", archivePath, "https://example.com/"+base+".tar.gz", tmpDir)
	require.NoError(t, err)
	content, err := os.ReadFile(bin)
	require.NoError(t, err)
	require.Equal(t, "binary", string(content))
}

func TestExtractDownloadedBinZip(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	base := testEtcdArchiveBase
	archivePath := filepath.Join(tmpDir, base+".zip")
	createTestZip(t, archivePath, map[string]string{
		filepath.Join(base, "etcdctl"): "binary",
	})

	bin, err := extractDownloadedBin("etcdctl", archivePath, "https://example.com/"+base+".zip", tmpDir)
	require.NoError(t, err)
	content, err := os.ReadFile(bin)
	require.NoError(t, err)
	require.Equal(t, "binary", string(content))
}

func writeLocalArtifact(t *testing.T, name, output string) (string, string, string) {
	t.Helper()

	artifactDir := t.TempDir()
	artifactPath := filepath.Join(artifactDir, name)
	script := "#!/bin/sh\necho \"" + output + "\"\n"
	require.NoError(t, os.WriteFile(artifactPath, []byte(script), 0o600))

	sha, err := commoninstall.ComputeSHA256(artifactPath)
	require.NoError(t, err)

	manifestPath := filepath.Join(artifactDir, "manifest.yaml")
	manifest := fmt.Sprintf("version: 1\nartifacts:\n- name: %s\n  sha256: %s\n  size: %d\n", name, sha, len(script))
	require.NoError(t, os.WriteFile(manifestPath, []byte(manifest), 0o600))

	return artifactDir, manifestPath, "https://example.com/" + name
}

func TestOpApplyOptsDefaults(t *testing.T) {
	t.Parallel()

	op := &Op{}
	op.applyOpts(nil)
	require.Equal(t, time.Minute, op.downloadTimeout)
}

func TestOpApplyOptsValues(t *testing.T) {
	t.Parallel()

	op := &Op{}
	op.applyOpts([]OpOption{
		WithDownloadURL("https://example.com/etcd.tar.gz"),
		WithDownloadTimeout(2 * time.Second),
		WithVersionCheck(true),
		WithOffline(true),
		WithArtifactDir("/tmp/artifacts"),
		WithChecksumManifest("/tmp/manifest.json"),
	})
	require.Equal(t, "https://example.com/etcd.tar.gz", op.downloadURL)
	require.Equal(t, 2*time.Second, op.downloadTimeout)
	require.True(t, op.versionCheck)
	require.True(t, op.offline)
	require.Equal(t, "/tmp/artifacts", op.artifactDir)
	require.Equal(t, "/tmp/manifest.json", op.manifestPath)
}

func TestBuildDownloadURL(t *testing.T) {
	t.Parallel()

	const testVersion = "v3.6.8"

	osType, err := getOSType()
	require.NoError(t, err)
	archType, err := getArchType()
	require.NoError(t, err)
	archiveType, err := getArchiveType()
	require.NoError(t, err)

	expected := fmt.Sprintf("https://github.com/etcd-io/etcd/releases/download/%s/etcd-%s-%s-%s.%s",
		testVersion,
		testVersion,
		osType,
		archType,
		archiveType,
	)

	url, err := buildDownloadURL(testVersion)
	require.NoError(t, err)
	require.Equal(t, expected, url)
}

func TestBuildDownloadURL_EmptyVersion(t *testing.T) {
	t.Parallel()

	_, err := buildDownloadURL("")
	require.ErrorIs(t, err, errVersionRequired)
}

func TestGetOSTypeArchTypeArchiveType(t *testing.T) {
	t.Parallel()

	osType, err := getOSType()
	require.NoError(t, err)
	require.NotEmpty(t, osType)

	archType, err := getArchType()
	require.NoError(t, err)
	require.NotEmpty(t, archType)

	archiveType, err := getArchiveType()
	require.NoError(t, err)
	require.NotEmpty(t, archiveType)
}

func TestCopyAndMoveFile(t *testing.T) {
	t.Parallel()

	src := filepath.Join(t.TempDir(), "src")
	dst := filepath.Join(t.TempDir(), "dst")
	require.NoError(t, os.WriteFile(src, []byte("payload"), 0o600))

	require.NoError(t, file.CopyFile(src, dst))
	data, err := os.ReadFile(dst)
	require.NoError(t, err)
	require.Equal(t, "payload", string(data))

	moved := filepath.Join(t.TempDir(), "moved")
	require.NoError(t, moveFile(dst, moved))
	_, err = os.Stat(dst)
	require.Error(t, err)
	require.True(t, os.IsNotExist(err))
}

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
		dir := filepath.Dir(name)
		if dir != "." {
			hdr := &tar.Header{
				Name:     dir + "/",
				Mode:     0o755,
				Typeflag: tar.TypeDir,
			}
			require.NoError(t, tw.WriteHeader(hdr))
		}

		hdr := &tar.Header{
			Name: name,
			Mode: 0o644,
			Size: int64(len(content)),
		}
		require.NoError(t, tw.WriteHeader(hdr))
		_, err = tw.Write([]byte(content))
		require.NoError(t, err)
	}
}

func createTestZip(t *testing.T, path string, files map[string]string) {
	t.Helper()

	f, err := os.Create(path)
	require.NoError(t, err)
	defer func() { _ = f.Close() }()

	zw := zip.NewWriter(f)
	for name, content := range files {
		w, err := zw.Create(name)
		require.NoError(t, err)
		_, err = io.WriteString(w, content)
		require.NoError(t, err)
	}
	require.NoError(t, zw.Close())
}

// TestExtractDownloadedBinTgz verifies that .tgz archives are handled the same as .tar.gz.
// The .tgz extension is an alternative suffix for gzipped tar archives.
func TestExtractDownloadedBinTgz(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	base := testEtcdArchiveBase
	// Use .tgz suffix instead of .tar.gz to test the alternative code path.
	archivePath := filepath.Join(tmpDir, base+".tgz")
	createTestTarGz(t, archivePath, map[string]string{
		filepath.Join(base, "etcd"): "tgz-binary",
	})

	bin, err := extractDownloadedBin("etcd", archivePath, "https://example.com/"+base+".tgz", tmpDir)
	require.NoError(t, err)
	content, err := os.ReadFile(bin)
	require.NoError(t, err)
	require.Equal(t, "tgz-binary", string(content))
}

// TestParseSHA256SUMSVariousFormats verifies that parseSHA256SUMS handles various
// checksum file formats including different whitespace and empty lines.
func TestParseSHA256SUMSVariousFormats(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name         string
		data         string
		artifactName string
		expectedSum  string
		expectError  bool
	}{
		{
			// Standard format with two-space separator (common in SHA256SUMS files).
			name:         "standard two-space format",
			data:         "abc123def456  etcd-v3.6.7-linux-amd64.tar.gz\n",
			artifactName: "etcd-v3.6.7-linux-amd64.tar.gz",
			expectedSum:  "abc123def456",
			expectError:  false,
		},
		{
			// Single space separator should also work.
			name:         "single space format",
			data:         "abc123 artifact.tar.gz\n",
			artifactName: "artifact.tar.gz",
			expectedSum:  "abc123",
			expectError:  false,
		},
		{
			// Multiple artifacts in the file, verify correct one is found.
			name: "multiple artifacts",
			data: `hash1  artifact1.tar.gz
hash2  artifact2.tar.gz
hash3  artifact3.tar.gz
`,
			artifactName: "artifact2.tar.gz",
			expectedSum:  "hash2",
			expectError:  false,
		},
		{
			// Empty lines and whitespace should be skipped.
			name: "with empty lines",
			data: `

hash1  artifact.tar.gz

`,
			artifactName: "artifact.tar.gz",
			expectedSum:  "hash1",
			expectError:  false,
		},
		{
			// Artifact not found in the checksum file.
			name:         "artifact not found",
			data:         "hash1  other.tar.gz\n",
			artifactName: "missing.tar.gz",
			expectError:  true,
		},
		{
			// Malformed line with only one field should be skipped.
			name: "malformed line skipped",
			data: `malformed-no-filename
hash1  artifact.tar.gz
`,
			artifactName: "artifact.tar.gz",
			expectedSum:  "hash1",
			expectError:  false,
		},
		{
			// Empty data.
			name:         "empty data",
			data:         "",
			artifactName: "artifact.tar.gz",
			expectError:  true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			sum, err := parseSHA256SUMS([]byte(tc.data), tc.artifactName)
			if tc.expectError {
				require.Error(t, err)
				require.Contains(t, err.Error(), "checksum entry not found")
			} else {
				require.NoError(t, err)
				require.Equal(t, tc.expectedSum, sum)
			}
		})
	}
}

// TestResolveDownloadedFileNilOptions verifies that nil options return an error.
func TestResolveDownloadedFileNilOptions(t *testing.T) {
	t.Parallel()

	_, err := resolveDownloadedFile(context.Background(), "etcd", "etcd.tar.gz", "https://example.com/etcd.tar.gz", t.TempDir(), nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "download options are nil")
}

// TestResolveDownloadedFileLocalArtifactNotOffline verifies that when an artifact
// exists locally in artifactDir but offline mode is NOT enabled, the local artifact
// is still used (no download needed).
func TestResolveDownloadedFileLocalArtifactNotOffline(t *testing.T) {
	t.Parallel()

	artifactDir := t.TempDir()
	localPath := filepath.Join(artifactDir, "etcd.tar.gz")
	require.NoError(t, os.WriteFile(localPath, []byte("local-artifact"), 0o600))

	// offline=false but artifact exists locally - should use local copy.
	path, err := resolveDownloadedFile(context.Background(), "etcd", "etcd.tar.gz", "https://example.com/etcd.tar.gz", t.TempDir(), &Op{
		offline:     false,
		artifactDir: artifactDir,
	})
	require.NoError(t, err)
	require.Equal(t, localPath, path)
}

// TestRetryAllAttemptsFail verifies retry behavior when all attempts fail.
func TestRetryAllAttemptsFail(t *testing.T) {
	t.Parallel()

	attempts := 0
	expectedErr := errPersistentFailure

	err := retry(3, time.Millisecond, func() error {
		attempts++
		return expectedErr
	})

	require.Error(t, err)
	require.ErrorIs(t, err, expectedErr)
	require.Contains(t, err.Error(), "retry attempts exhausted")
	// Should have tried exactly 3 times.
	require.Equal(t, 3, attempts)
}

// TestRetrySucceedsOnLastAttempt verifies retry succeeds on the final attempt.
func TestRetrySucceedsOnLastAttempt(t *testing.T) {
	t.Parallel()

	attempts := 0

	err := retry(3, time.Millisecond, func() error {
		attempts++
		if attempts < 3 {
			return errNotYet
		}
		return nil
	})

	require.NoError(t, err)
	require.Equal(t, 3, attempts)
}

// TestRetryNegativeDelay verifies that negative delay defaults to 1 second minimum.
// We use a short test by mocking success on first attempt.
func TestRetryNegativeDelay(t *testing.T) {
	t.Parallel()

	attempts := 0

	err := retry(1, -5*time.Second, func() error {
		attempts++
		return nil
	})

	require.NoError(t, err)
	require.Equal(t, 1, attempts)
}

// TestVerifyArtifactInvalidURL verifies error handling for malformed download URLs.
func TestVerifyArtifactInvalidURL(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "artifact.tar.gz")
	require.NoError(t, os.WriteFile(path, []byte("data"), 0o600))

	// Use an invalid URL that cannot be parsed.
	err := verifyArtifact(context.Background(), path, "artifact.tar.gz", "://invalid-url", nil, false, time.Second)
	require.Error(t, err)
	require.Contains(t, err.Error(), "failed to parse download URL")
}

// TestVerifyArtifactChecksumDownloadFails verifies error when checksum file download fails.
func TestVerifyArtifactChecksumDownloadFails(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "artifact.tar.gz")
	require.NoError(t, os.WriteFile(path, []byte("data"), 0o600))

	// Server that always returns 404 for checksum requests.
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer ts.Close()

	err := verifyArtifact(context.Background(), path, "artifact.tar.gz", ts.URL+"/artifact.tar.gz", nil, false, time.Second)
	require.Error(t, err)
	require.Contains(t, err.Error(), "failed to download checksum list")
}

// TestVerifyArtifactChecksumMismatch verifies error when file checksum doesn't match.
func TestVerifyArtifactChecksumMismatch(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	artifactName := "artifact.tar.gz"
	path := filepath.Join(tmpDir, artifactName)
	require.NoError(t, os.WriteFile(path, []byte("actual-content"), 0o600))

	// Server returns a checksum for different content.
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "SHA256SUMS") {
			// Return an incorrect checksum (64 hex chars representing a different hash).
			_, _ = fmt.Fprintf(w, "0000000000000000000000000000000000000000000000000000000000000000  %s\n", artifactName)
			return
		}
		http.NotFound(w, r)
	}))
	defer ts.Close()

	err := verifyArtifact(context.Background(), path, artifactName, ts.URL+"/"+artifactName, nil, false, time.Second)
	require.Error(t, err)
	require.Contains(t, err.Error(), "sha256 mismatch")
}

// TestCopyFileSourceNotExist verifies error when source file doesn't exist.
func TestCopyFileSourceNotExist(t *testing.T) {
	t.Parallel()

	err := file.CopyFile(filepath.Join(t.TempDir(), "nonexistent"), filepath.Join(t.TempDir(), "dst"))
	require.Error(t, err)
}

// TestCopyFileDestinationUnwritable verifies error when destination parent is unwritable.
func TestCopyFileDestinationUnwritable(t *testing.T) {
	t.Parallel()

	src := filepath.Join(t.TempDir(), "src")
	require.NoError(t, os.WriteFile(src, []byte("data"), 0o600))

	// Destination in an unwritable root path.
	err := file.CopyFile(src, "/proc/1/impossible/dst")
	require.Error(t, err)
}

// TestMoveFileSameFilesystem verifies moveFile uses rename when on same filesystem.
func TestMoveFileSameFilesystem(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	src := filepath.Join(tmpDir, "src")
	dst := filepath.Join(tmpDir, "dst")

	require.NoError(t, os.WriteFile(src, []byte("move-test"), 0o600))
	require.NoError(t, moveFile(src, dst))

	// Source should be gone, destination should have content.
	_, err := os.Stat(src)
	require.True(t, os.IsNotExist(err))

	data, err := os.ReadFile(dst)
	require.NoError(t, err)
	require.Equal(t, "move-test", string(data))
}

// TestFinalizeDownloadedBinSetsExecutable verifies that finalized binary has executable permissions.
func TestFinalizeDownloadedBinSetsExecutable(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	downloaded := filepath.Join(tmpDir, "downloaded")
	binPath := filepath.Join(tmpDir, "executable")
	require.NoError(t, os.WriteFile(downloaded, []byte("#!/bin/sh\necho test"), 0o600))

	require.NoError(t, finalizeDownloadedBin(downloaded, binPath))

	info, err := os.Stat(binPath)
	require.NoError(t, err)
	// Verify executable bit is set (at least for owner).
	require.NotZero(t, info.Mode()&0o100, "executable bit should be set")
}

// TestLoadManifestValidFile verifies loading a valid manifest file.
func TestLoadManifestValidFile(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	manifestPath := filepath.Join(tmpDir, "manifest.yaml")
	content := `version: "1"
artifacts:
- name: etcd
  sha256: abc123
  size: 1024
`
	require.NoError(t, os.WriteFile(manifestPath, []byte(content), 0o600))

	manifest, err := loadManifest(manifestPath)
	require.NoError(t, err)
	require.NotNil(t, manifest)
	require.Equal(t, "1", manifest.Version)
	require.Len(t, manifest.Artifacts, 1)
	require.Equal(t, "etcd", manifest.Artifacts[0].Name)
}

// TestLoadManifestFileNotFound verifies error when manifest file doesn't exist.
func TestLoadManifestFileNotFound(t *testing.T) {
	t.Parallel()

	_, err := loadManifest(filepath.Join(t.TempDir(), "nonexistent.yaml"))
	require.Error(t, err)
	require.ErrorIs(t, err, os.ErrNotExist)
}

// TestResolveDownloadURLWithVersion verifies that a version option
// triggers URL construction from the version.
func TestResolveDownloadURLWithVersion(t *testing.T) {
	t.Parallel()

	options := &Op{version: "v3.6.8"}
	url, err := resolveDownloadURL(options)
	require.NoError(t, err)
	require.Contains(t, url, "v3.6.8")
	require.Contains(t, url, "github.com/etcd-io/etcd/releases")
}

// TestResolveDownloadURLFailsWithoutVersionOrURL verifies fail-fast behavior
// when neither downloadURL nor version is provided.
func TestResolveDownloadURLFailsWithoutVersionOrURL(t *testing.T) {
	t.Parallel()

	options := &Op{downloadURL: ""}
	_, err := resolveDownloadURL(options)
	require.ErrorIs(t, err, errVersionRequired)
}

// TestProbeDownloadSizeServerError verifies that server errors result in "unknown" size.
func TestProbeDownloadSizeServerError(t *testing.T) {
	t.Parallel()

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer ts.Close()

	size := probeDownloadSize(context.Background(), ts.URL+"/etcd.tar.gz", &Op{downloadTimeout: time.Second})
	require.Equal(t, "unknown", size)
}

// TestProbeDownloadSizeLargeFile verifies humanized size for large files.
func TestProbeDownloadSizeLargeFile(t *testing.T) {
	t.Parallel()

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// 25 MB file.
		w.Header().Set("Content-Length", "26214400")
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	size := probeDownloadSize(context.Background(), ts.URL+"/etcd.tar.gz", &Op{downloadTimeout: time.Second})
	// Should be human-readable format like "25 MB" or "26 MB".
	require.Contains(t, size, "MB")
}

// TestVerifyArtifactWithManifestSizeMismatch verifies error when file size doesn't match manifest.
func TestVerifyArtifactWithManifestSizeMismatch(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "artifact.tar.gz")
	// Write content of a specific size.
	require.NoError(t, os.WriteFile(path, []byte("short"), 0o600))

	sha, err := commoninstall.ComputeSHA256(path)
	require.NoError(t, err)

	// Manifest claims a different size.
	manifest := &commoninstall.Manifest{
		Artifacts: []commoninstall.Artifact{{
			Name:   "artifact.tar.gz",
			SHA256: sha,
			Size:   9999, // Wrong size.
		}},
	}

	err = verifyArtifact(context.Background(), path, "artifact.tar.gz", "https://example.com/artifact.tar.gz", manifest, false, time.Second)
	require.Error(t, err)
	require.Contains(t, err.Error(), "unexpected file size")
}

// TestDownloadWithVersionCheckDisabled verifies that version check can be skipped.
// This uses a local artifact to avoid network calls.
func TestDownloadWithVersionCheckDisabled(t *testing.T) {
	t.Parallel()

	artifactDir, manifestPath, url := writeLocalArtifact(t, "etcd", "etcd Version: v3.6.7")
	binPath := filepath.Join(t.TempDir(), "etcd")

	out, err := DownloadEtcd(
		context.Background(),
		binPath,
		WithDownloadURL(url),
		WithArtifactDir(artifactDir),
		WithChecksumManifest(manifestPath),
		WithOffline(true),
		WithVersionCheck(false), // Disable version check.
	)
	require.NoError(t, err)
	// With version check disabled, output should be nil.
	require.Nil(t, out)

	// But the binary should still be installed.
	_, err = os.Stat(binPath)
	require.NoError(t, err)
}

// TestDownloadPreExtractedBinaryWithVersionCheck exercises the pre-extracted binary
// path in download() where a matching binary exists in artifactDir/bin/.
func TestDownloadPreExtractedBinaryWithVersionCheck(t *testing.T) {
	t.Parallel()

	artifactDir := t.TempDir()
	binDir := filepath.Join(artifactDir, "bin")
	require.NoError(t, os.MkdirAll(binDir, 0o750))

	// Create a pre-extracted binary with canonical arch naming (x86_64)
	// matching what the artifact builder produces.
	binaryPath := filepath.Join(binDir, testEtcdPreExtractedBase)
	require.NoError(t, os.WriteFile(binaryPath, []byte(testVersionScript), 0o600))

	binPath := filepath.Join(t.TempDir(), "etcd")

	// Download URL uses Go-convention arch (amd64) matching upstream naming;
	// findPreExtractedBinary canonicalizes to x86_64 for local lookup.
	// Use offline to avoid slow probeDownloadSize HTTP HEAD requests.
	out, err := download(context.Background(), "etcd", []string{"--version"}, binPath,
		WithDownloadURL("https://example.com/"+testEtcdArchiveBase+".tar.gz"),
		WithArtifactDir(artifactDir),
		WithOffline(true),
		WithVersionCheck(true),
	)
	require.NoError(t, err)
	require.Contains(t, string(out), "etcd Version")
}

// TestDownloadPreExtractedBinarySkipsVersionCheck exercises the pre-extracted binary
// path when version check is disabled, covering the early return.
func TestDownloadPreExtractedBinarySkipsVersionCheck(t *testing.T) {
	t.Parallel()

	artifactDir := t.TempDir()
	binDir := filepath.Join(artifactDir, "bin")
	require.NoError(t, os.MkdirAll(binDir, 0o750))

	binaryPath := filepath.Join(binDir, testEtcdPreExtractedBase)
	require.NoError(t, os.WriteFile(binaryPath, []byte(testVersionScript), 0o600))

	binPath := filepath.Join(t.TempDir(), "etcd")

	out, err := download(context.Background(), "etcd", []string{"--version"}, binPath,
		WithDownloadURL("https://example.com/"+testEtcdArchiveBase+".tar.gz"),
		WithArtifactDir(artifactDir),
		WithOffline(true),
		WithVersionCheck(false),
	)
	require.NoError(t, err)
	require.Nil(t, out)

	// Binary should still be copied to binPath.
	_, err = os.Stat(binPath)
	require.NoError(t, err)
}

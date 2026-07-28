//nolint:nlreturn,noinlineerr // Keep installer helpers compact.
package installer

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/dustin/go-humanize"

	"git.tbd/etcd-infra/pkg/archive"
	"git.tbd/etcd-infra/pkg/file"
	"git.tbd/etcd-infra/pkg/httputil"
	commoninstall "git.tbd/etcd-infra/pkg/install"
	logutil "git.tbd/etcd-infra/pkg/log"
)

var (
	errManifestPathEmpty        = errors.New("manifest path is empty")
	errDownloadOptsNil          = errors.New("download options are nil")
	errOfflineArtifactDirUnset  = errors.New("offline mode enabled but artifact directory not set")
	errOfflineArtifactNotFound  = errors.New("offline mode enabled but artifact not found")
	errUnsupportedOS            = errors.New("unsupported OS")
	errUnsupportedArchitecture  = errors.New("unsupported architecture")
	errChecksumManifestMissing  = errors.New("checksum manifest missing entry")
	errChecksumEntryNotFound    = errors.New("checksum entry not found")
	errFailedDownloadSizeProbe  = errors.New("failed to probe download size")
	errFailedParseDownloadURL   = errors.New("failed to parse download URL")
	errFailedDownloadChecksum   = errors.New("failed to download checksum list")
	errFailedVerifyArtifact     = errors.New("failed to verify artifact")
	errFailedMoveDownloadedBin  = errors.New("failed to move downloaded binary")
	errFailedSetBinaryMode      = errors.New("failed to set binary permissions")
	errFailedLoadManifest       = errors.New("failed to load manifest")
	errFailedCheckFileExistence = errors.New("failed to check file existence")
	errFailedMoveFile           = errors.New("failed to move file")
	errFailedCheckVersion       = errors.New("failed to check version")
	errVersionRequired          = errors.New("etcd version is required: set WithVersion or WithDownloadURL (version comes from spec YAML)")
)

const (
	checksumHint          = "provide WithChecksumManifest or WithArtifactDir for offline use"
	downloadRetryAttempts = 3
	downloadRetryDelay    = 2 * time.Second
	binaryPerm            = 0o755
	minChecksumFields     = 2
)

func download(ctx context.Context, binName string, checkVersionArgs []string, binPath string, opts ...OpOption) ([]byte, error) {
	options := &Op{}
	options.applyOpts(opts)

	u, err := resolveDownloadURL(options)
	if err != nil {
		return nil, err
	}

	logutil.S().Infow("downloading", "component", binName, "url", u, "size", probeDownloadSize(ctx, u, options))

	// Check for pre-extracted binary in artifact directory (new artifact layout).
	// The artifact builder stores extracted binaries as bin/{name} without archive
	// extensions (e.g., bin/etcd-v3.6.7-linux-x86_64 instead of etcd-v3.6.7-linux-x86_64.tar.gz).
	//nolint:nestif // Complex conditional logic - clarity prioritized over nesting depth
	if options.artifactDir != "" {
		if preExtracted := findPreExtractedBinary(options.artifactDir, path.Base(u), binName); preExtracted != "" {
			logutil.S().Infow("using pre-extracted binary from artifact directory",
				"component", binName, "path", preExtracted, "target", binPath)
			if err := file.CopyFile(preExtracted, binPath); err != nil { //nolint:govet // Intentional error shadow
				return nil, fmt.Errorf("copy pre-extracted binary: %w", err)
			}
			if err := os.Chmod(binPath, binaryPerm); err != nil { //nolint:govet // Intentional error shadow
				return nil, fmt.Errorf("set binary permissions: %w", err)
			}
			if !options.versionCheck {
				return nil, nil
			}
			return checkVersion(ctx, binPath, checkVersionArgs)
		}
	}

	tmpDir, err := os.MkdirTemp(os.TempDir(), "etcd-install")
	if err != nil {
		return nil, fmt.Errorf("create temp dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	artifactName := path.Base(u)
	manifest, err := loadManifest(options.manifestPath)
	if err != nil && !errors.Is(err, errManifestPathEmpty) {
		return nil, err
	}

	downloadedFile, err := resolveDownloadedFile(ctx, binName, artifactName, u, tmpDir, options)
	if err != nil {
		return nil, err
	}

	if verifyErr := verifyArtifact(
		ctx,
		downloadedFile,
		artifactName,
		u,
		manifest,
		options.offline,
		options.downloadTimeout,
	); verifyErr != nil {
		return nil, verifyErr
	}

	downloadedBin, err := extractDownloadedBin(binName, downloadedFile, u, tmpDir)
	if err != nil {
		return nil, err
	}

	if err := finalizeDownloadedBin(downloadedBin, binPath); err != nil {
		return nil, err
	}

	if !options.versionCheck {
		return nil, nil
	}

	return checkVersion(ctx, binPath, checkVersionArgs)
}

// findPreExtractedBinary looks for a pre-extracted binary in the artifact directory.
// The artifact builder stores extracted binaries in bin/ without archive extensions.
//
// binName is the specific binary to find (e.g., "etcd" or "etcdctl").
// artifactName is the archive filename from the URL (e.g., "etcd-v3.6.7-linux-amd64.tar.gz").
//
// For etcd downloads, the archive contains both etcd and etcdctl. The artifact builder
// extracts them as separate files: bin/etcd-v3.6.7-linux-x86_64, bin/etcdctl-v3.6.7-linux-x86_64.
// This function finds the correct binary by matching the binName prefix with the version suffix.
func findPreExtractedBinary(artifactDir, artifactName, binName string) string {
	binBase := stripArchiveExtension(artifactName)
	if binBase == artifactName {
		return "" // No archive extension to strip; not applicable.
	}

	// Derive the expected binary name for this specific binary.
	// For etcd: artifactName="etcd-v3.6.7-linux-amd64.tar.gz" → look for "etcd-v3.6.7-linux-x86_64"
	// For etcdctl: same archive but binName="etcdctl" → look for "etcdctl-v3.6.7-linux-x86_64"
	expectedName := binBase
	if !strings.HasPrefix(binBase, binName+"-") && binBase != binName {
		// Different binary than the archive name; derive by replacing the prefix.
		// Find the version suffix (e.g., "-v3.6.7-linux-amd64" from "etcd-v3.6.7-linux-amd64").
		if idx := findVersionSuffixStart(binBase); idx >= 0 {
			expectedName = binName + binBase[idx:]
		}
	}

	// Normalize Go-convention architecture suffix (amd64/arm64) to canonical
	// uname -m convention (x86_64/aarch64) used in etcd-infra artifact filenames.
	expectedName = canonicalizeArtifactName(expectedName)

	// Check bin/ subdirectory first (primary artifact layout).
	if p := filepath.Join(artifactDir, "bin", expectedName); fileExistsQuiet(p) {
		return p
	}
	// Try .tar.gz variant and extract in-place (new uniform archive format).
	if p := filepath.Join(artifactDir, "bin", expectedName+".tar.gz"); fileExistsQuiet(p) {
		if err := archive.UntarGz(p, filepath.Dir(p)); err == nil {
			if fileExistsQuiet(filepath.Join(artifactDir, "bin", expectedName)) {
				return filepath.Join(artifactDir, "bin", expectedName)
			}
		}
	}
	// Check artifact root (fallback).
	if p := filepath.Join(artifactDir, expectedName); fileExistsQuiet(p) {
		return p
	}
	if p := filepath.Join(artifactDir, expectedName+".tar.gz"); fileExistsQuiet(p) {
		if err := archive.UntarGz(p, filepath.Dir(p)); err == nil {
			if fileExistsQuiet(filepath.Join(artifactDir, expectedName)) {
				return filepath.Join(artifactDir, expectedName)
			}
		}
	}
	return ""
}

// findVersionSuffixStart returns the index of the version suffix in a binary name.
// It looks for "-v" followed by a digit (e.g., "-v3.6.7-linux-amd64" in "etcd-v3.6.7-linux-amd64").
// Returns -1 if no version suffix is found.
func findVersionSuffixStart(name string) int {
	for i := range len(name) - 2 {
		if name[i] == '-' && name[i+1] == 'v' && name[i+2] >= '0' && name[i+2] <= '9' {
			return i
		}
	}
	return -1
}

// canonicalizeArtifactName normalizes Go-convention architecture suffixes in
// artifact filenames to canonical uname -m convention used by etcd-infra:
// "-linux-amd64" → "-linux-x86_64", "-linux-arm64" → "-linux-aarch64".
func canonicalizeArtifactName(name string) string {
	name = strings.Replace(name, "-linux-amd64", "-linux-x86_64", 1)
	name = strings.Replace(name, "-linux-arm64", "-linux-aarch64", 1)
	return name
}

// stripArchiveExtension removes known archive extensions (.tar.gz, .tgz, .zip).
func stripArchiveExtension(name string) string {
	for _, ext := range []string{".tar.gz", ".tgz", ".zip"} {
		if before, ok := strings.CutSuffix(name, ext); ok {
			return before
		}
	}
	return name
}

// fileExistsQuiet returns true if the path exists, false otherwise (errors treated as false).
func fileExistsQuiet(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func resolveDownloadURL(options *Op) (string, error) {
	if options == nil {
		return "", errDownloadOptsNil
	}
	if options.downloadURL != "" {
		return options.downloadURL, nil
	}
	return buildDownloadURL(options.version)
}

func probeDownloadSize(ctx context.Context, downloadURL string, options *Op) string {
	sizeText := "unknown"
	if options == nil || options.offline {
		return sizeText
	}

	size := int64(-1)
	if err := retry(downloadRetryAttempts, downloadRetryDelay, func() error {
		s, err := httputil.HeadURLForContentLength(
			ctx,
			downloadURL,
			httputil.WithTimeout(options.downloadTimeout),
		)
		if err != nil {
			return fmt.Errorf("%w: %w", errFailedDownloadSizeProbe, err)
		}
		size = s
		return nil
	}); err != nil {
		logutil.S().Warnw("failed to probe download size", "url", downloadURL, "error", err)
	}
	if size >= 0 {
		sizeText = humanize.Bytes(uint64(size))
	}
	return sizeText
}

func resolveDownloadedFile(ctx context.Context, binName, artifactName, downloadURL, tmpDir string, options *Op) (string, error) {
	if options == nil {
		return "", errDownloadOptsNil
	}
	if options.offline && options.artifactDir == "" {
		return "", errOfflineArtifactDirUnset
	}

	if options.artifactDir != "" {
		if cached, err := resolveFromArtifactDir(options.artifactDir, artifactName, options.offline); cached != "" || err != nil {
			return cached, err
		}
	}

	downloadedFile := ""
	if err := retry(downloadRetryAttempts, downloadRetryDelay, func() error {
		var err error
		downloadedFile, err = httputil.DownloadURLToFile(
			ctx,
			downloadURL,
			httputil.WithTimeout(options.downloadTimeout),
			httputil.WithDownloadFilePath(filepath.Join(tmpDir, binName)),
		)
		if err != nil {
			return fmt.Errorf("download artifact: %w", err)
		}

		return nil
	}); err != nil {
		return "", fmt.Errorf("download retries exhausted: %w", err)
	}
	return downloadedFile, nil
}

// resolveFromArtifactDir checks for a cached artifact in artifactDir, trying
// both the original name and the canonical arch variant (x86_64/aarch64).
// Returns ("", nil) when the artifact is not cached and downloading is allowed.
func resolveFromArtifactDir(artifactDir, artifactName string, offline bool) (string, error) {
	localPath := filepath.Join(artifactDir, artifactName)
	exists, err := fileExists(localPath)
	if err != nil {
		return "", fmt.Errorf("check artifact path: %w", err)
	}
	if exists {
		return localPath, nil
	}
	// Also check canonical arch naming (x86_64/aarch64) since etcd-infra artifacts
	// use uname -m convention, not Go's amd64/arm64.
	if canonical := canonicalizeArtifactName(artifactName); canonical != artifactName {
		canonicalPath := filepath.Join(artifactDir, canonical)
		cExists, cErr := fileExists(canonicalPath)
		if cErr != nil {
			return "", fmt.Errorf("check canonical artifact path: %w", cErr)
		}
		if cExists {
			return canonicalPath, nil
		}
	}
	if offline {
		return "", fmt.Errorf("%w: %s", errOfflineArtifactNotFound, localPath)
	}
	return "", nil
}

func extractDownloadedBin(binName, downloadedFile, downloadURL, tmpDir string) (string, error) {
	switch {
	case strings.HasSuffix(downloadURL, ".tar.gz"):
		if err := archive.UntarGz(downloadedFile, tmpDir); err != nil {
			return "", fmt.Errorf("extract tar.gz: %w", err)
		}
		base := strings.TrimSuffix(path.Base(downloadURL), ".tar.gz")
		return filepath.Join(tmpDir, base, binName), nil
	case strings.HasSuffix(downloadURL, ".tgz"):
		if err := archive.UntarGz(downloadedFile, tmpDir); err != nil {
			return "", fmt.Errorf("extract tgz: %w", err)
		}
		base := strings.TrimSuffix(path.Base(downloadURL), ".tgz")
		return filepath.Join(tmpDir, base, binName), nil
	case path.Ext(downloadURL) == ".zip":
		if err := archive.Unzip(downloadedFile, tmpDir); err != nil {
			return "", fmt.Errorf("extract zip: %w", err)
		}
		base := strings.TrimSuffix(path.Base(downloadURL), ".zip")
		return filepath.Join(tmpDir, base, binName), nil
	default:
		return downloadedFile, nil
	}
}

func finalizeDownloadedBin(downloadedBin, binPath string) error {
	logutil.S().Infow("renaming", "downloadedBin", downloadedBin, "binPath", binPath)
	if err := moveFile(downloadedBin, binPath); err != nil {
		return fmt.Errorf("%w: %w", errFailedMoveDownloadedBin, err)
	}
	if err := os.Chmod(binPath, binaryPerm); err != nil {
		return fmt.Errorf("%w: %w", errFailedSetBinaryMode, err)
	}
	logutil.S().Infow("successfully downloaded", "binPath", binPath)
	return nil
}

func checkVersion(ctx context.Context, binPath string, checkVersionArgs []string) ([]byte, error) {
	logutil.S().Infow("checking version", "binPath", binPath)
	cmd := exec.CommandContext(ctx, binPath, checkVersionArgs...)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errFailedCheckVersion, err)
	}

	return out, nil
}

func retry(attempts int, delay time.Duration, fn func() error) error {
	attempts = max(attempts, 1)
	if delay <= 0 {
		delay = time.Second
	}
	var err error
	for i := range attempts {
		err = fn()
		if err == nil {
			return nil
		}
		if i == attempts-1 {
			break
		}
		time.Sleep(delay)
		delay *= 2
	}
	return fmt.Errorf("retry attempts exhausted: %w", err)
}

// DownloadEtcd downloads the etcd binary to the given path.
func DownloadEtcd(ctx context.Context, binPath string, opts ...OpOption) ([]byte, error) {
	return download(ctx, "etcd", []string{"--version"}, binPath, opts...)
}

// DownloadEtcdctl downloads the etcdctl binary to the given path.
func DownloadEtcdctl(ctx context.Context, binPath string, opts ...OpOption) ([]byte, error) {
	return download(ctx, "etcdctl", []string{"version"}, binPath, opts...)
}

// DownloadEtcdutl downloads the etcdutl binary to the given path.
// etcdutl is required for snapshot restore in etcd 3.6.x (moved from etcdctl).
func DownloadEtcdutl(ctx context.Context, binPath string, opts ...OpOption) ([]byte, error) {
	return download(ctx, "etcdutl", []string{"version"}, binPath, opts...)
}

// buildDownloadURL constructs the etcd release download URL for the given version.
// version may or may not include the 'v' prefix (e.g., "3.6.8" or "v3.6.8").
// The spec YAML stores versions without the prefix ("3.6.8"), but etcd release
// URLs use the prefix ("v3.6.8"). This function normalizes automatically.
// ref. https://github.com/etcd-io/etcd/releases
func buildDownloadURL(version string) (string, error) {
	if version == "" {
		return "", errVersionRequired
	}
	// Normalize: etcd release URLs always use "v" prefix (e.g., v3.6.8).
	// The spec YAML stores "3.6.8" (binary convention), so add prefix if missing.
	if !strings.HasPrefix(version, "v") {
		version = "v" + version
	}
	osType, err := getOSType()
	if err != nil {
		return "", err
	}
	archType, err := getArchType()
	if err != nil {
		return "", err
	}
	archiveType, err := getArchiveType()
	if err != nil {
		return "", err
	}

	// e.g., https://github.com/etcd-io/etcd/releases/download/v3.6.8/etcd-v3.6.8-linux-amd64.tar.gz
	// e.g., https://github.com/etcd-io/etcd/releases/download/v3.6.8/etcd-v3.6.8-linux-arm64.tar.gz
	return fmt.Sprintf("https://github.com/etcd-io/etcd/releases/download/%s/etcd-%s-%s-%s.%s",
		version,
		version,
		osType,
		archType,
		archiveType,
	), nil
}

const (
	osTypeLinux  = "linux"
	osTypeDarwin = "darwin"
	archAMD64    = "amd64"
	archARM64    = "arm64"

	archiveTypeTarGz = "tar.gz"
	archiveTypeZip   = "zip"
)

func getOSType() (string, error) {
	switch runtime.GOOS {
	case osTypeLinux, osTypeDarwin:
		return runtime.GOOS, nil
	default:
		return "", fmt.Errorf("%w: %s", errUnsupportedOS, runtime.GOOS)
	}
}

func getArchType() (string, error) {
	switch runtime.GOARCH {
	case archAMD64:
		return archAMD64, nil
	case archARM64:
		return archARM64, nil
	default:
		return "", fmt.Errorf("%w: %s", errUnsupportedArchitecture, runtime.GOARCH)
	}
}

func moveFile(src, dst string) error {
	if err := os.Rename(src, dst); err != nil {
		if !errors.Is(err, syscall.EXDEV) {
			return fmt.Errorf("%w: rename: %w", errFailedMoveFile, err)
		}
		if err := file.CopyFile(src, dst); err != nil {
			return fmt.Errorf("%w: %w", errFailedMoveFile, err)
		}
		if err := os.Remove(src); err != nil {
			logutil.S().Warnw("failed to remove temp download after copy", "path", src, "error", err)
		}
	}
	return nil
}

func loadManifest(path string) (*commoninstall.Manifest, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errManifestPathEmpty
	}
	manifest, err := commoninstall.LoadManifest(path)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errFailedLoadManifest, err)
	}

	return manifest, nil
}

func fileExists(path string) (bool, error) {
	_, err := os.Stat(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("%w: %w", errFailedCheckFileExistence, err)
	}
	return true, nil
}

func verifyArtifact(
	ctx context.Context,
	filePath string,
	artifactName string,
	rawURL string,
	manifest *commoninstall.Manifest,
	offline bool,
	timeout time.Duration,
) error {
	if manifest != nil {
		entry := manifest.Find(artifactName)
		if entry == nil {
			return fmt.Errorf("%w: %q", errChecksumManifestMissing, artifactName)
		}
		if err := commoninstall.VerifyFileSHA256(filePath, entry.SHA256, entry.Size); err != nil {
			return fmt.Errorf("%w: %w", errFailedVerifyArtifact, err)
		}

		return nil
	}
	if offline {
		// In offline mode without a manifest, trust the local artifact.
		// This is safe because artifacts are pre-cached by the builder VM
		// which downloads from official sources and verifies during build.
		logutil.S().Infow("skipping checksum verification for trusted offline artifact",
			"artifact", artifactName, "path", filePath)
		return nil
	}

	// Construct checksum URL by replacing the filename with SHA256SUMS.
	// Use net/url to properly handle URL parsing (path.Join mangles the scheme).
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("%w: %w", errFailedParseDownloadURL, err)
	}
	parsed.Path = path.Dir(parsed.Path) + "/SHA256SUMS"
	sumURL := parsed.String()

	body, err := httputil.DownloadURL(
		ctx,
		sumURL,
		httputil.WithTimeout(timeout),
	)
	if err != nil {
		return fmt.Errorf("%w for %q: %w (%s)", errFailedDownloadChecksum, artifactName, err, checksumHint)
	}
	expected, err := parseSHA256SUMS(body, artifactName)
	if err != nil {
		return fmt.Errorf("parse checksum entry: %w", err)
	}
	verifyErr := commoninstall.VerifyFileSHA256(filePath, expected, 0)
	if verifyErr != nil {
		return fmt.Errorf("%w: %w", errFailedVerifyArtifact, verifyErr)
	}

	return nil
}

func parseSHA256SUMS(data []byte, artifactName string) (string, error) {
	for line := range strings.SplitSeq(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < minChecksumFields {
			continue
		}
		if fields[1] == artifactName {
			return fields[0], nil
		}
	}
	return "", fmt.Errorf("%w: %q", errChecksumEntryNotFound, artifactName)
}

// getArchiveType returns the archive format for etcd release artifacts.
func getArchiveType() (string, error) {
	switch runtime.GOOS {
	case osTypeLinux:
		return archiveTypeTarGz, nil
	case osTypeDarwin:
		return archiveTypeZip, nil
	default:
		return "", fmt.Errorf("%w: %s", errUnsupportedOS, runtime.GOOS)
	}
}

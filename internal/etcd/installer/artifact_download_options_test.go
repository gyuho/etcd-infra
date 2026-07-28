//nolint:testpackage // Tests use package internals and shared resources.
package installer

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestOpDefaultValues verifies that Op starts with zero values before applyOpts is called.
// This ensures options are properly initialized and no unexpected defaults exist in the struct itself.
func TestOpDefaultValues(t *testing.T) {
	t.Parallel()

	op := &Op{}

	// Verify all fields start as zero values before applyOpts is called.
	assert.Empty(t, op.downloadURL, "downloadURL should be empty before applyOpts")
	assert.Zero(t, op.downloadTimeout, "downloadTimeout should be zero before applyOpts")
	assert.False(t, op.versionCheck, "versionCheck should be false before applyOpts")
	assert.False(t, op.offline, "offline should be false before applyOpts")
	assert.Empty(t, op.artifactDir, "artifactDir should be empty before applyOpts")
	assert.Empty(t, op.manifestPath, "manifestPath should be empty before applyOpts")
}

// TestApplyOptsDefaultTimeout verifies that applyOpts sets a 1-minute default timeout
// when no timeout is explicitly specified. This prevents indefinite hangs during downloads.
func TestApplyOptsDefaultTimeout(t *testing.T) {
	t.Parallel()

	op := &Op{}
	op.applyOpts(nil)

	// Default timeout should be 1 minute when not explicitly set.
	assert.Equal(t, time.Minute, op.downloadTimeout, "default timeout should be 1 minute")
}

// TestApplyOptsPreservesExplicitTimeout verifies that an explicitly set timeout
// is not overridden by the default value in applyOpts.
func TestApplyOptsPreservesExplicitTimeout(t *testing.T) {
	t.Parallel()

	op := &Op{}
	customTimeout := 5 * time.Minute
	op.applyOpts([]OpOption{WithDownloadTimeout(customTimeout)})

	// Explicitly set timeout should not be overridden.
	assert.Equal(t, customTimeout, op.downloadTimeout, "explicit timeout should be preserved")
}

// TestApplyOptsEmptySlice verifies that passing an empty slice to applyOpts
// still applies the default timeout (same behavior as nil).
func TestApplyOptsEmptySlice(t *testing.T) {
	t.Parallel()

	op := &Op{}
	op.applyOpts([]OpOption{})

	// Empty slice should behave like nil - apply specs.
	assert.Equal(t, time.Minute, op.downloadTimeout, "empty slice should apply default timeout")
}

// TestWithDownloadURL verifies the WithDownloadURL option sets the download URL correctly.
func TestWithDownloadURL(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name     string
		url      string
		expected string
	}{
		{
			name:     "standard GitHub URL",
			url:      "https://github.com/etcd-io/etcd/releases/download/v3.6.7/etcd-v3.6.7-linux-amd64.tar.gz",
			expected: "https://github.com/etcd-io/etcd/releases/download/v3.6.7/etcd-v3.6.7-linux-amd64.tar.gz",
		},
		{
			name:     "custom mirror URL",
			url:      "https://internal-mirror.example.com/etcd.tar.gz",
			expected: "https://internal-mirror.example.com/etcd.tar.gz",
		},
		{
			name:     "empty URL",
			url:      "",
			expected: "",
		},
		{
			name:     "URL with special characters",
			url:      "https://example.com/path%20with%20spaces/etcd.tar.gz",
			expected: "https://example.com/path%20with%20spaces/etcd.tar.gz",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			op := &Op{}
			op.applyOpts([]OpOption{WithDownloadURL(tc.url)})
			assert.Equal(t, tc.expected, op.downloadURL)
		})
	}
}

// TestWithDownloadTimeout verifies the WithDownloadTimeout option sets timeouts correctly.
func TestWithDownloadTimeout(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name     string
		timeout  time.Duration
		expected time.Duration
	}{
		{
			name:     "short timeout",
			timeout:  5 * time.Second,
			expected: 5 * time.Second,
		},
		{
			name:     "long timeout",
			timeout:  30 * time.Minute,
			expected: 30 * time.Minute,
		},
		{
			name:     "zero timeout triggers default",
			timeout:  0,
			expected: time.Minute, // Default is applied when timeout is 0.
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			op := &Op{}
			op.applyOpts([]OpOption{WithDownloadTimeout(tc.timeout)})
			assert.Equal(t, tc.expected, op.downloadTimeout)
		})
	}
}

// TestWithVersionCheck verifies the WithVersionCheck option sets the flag correctly.
func TestWithVersionCheck(t *testing.T) {
	t.Parallel()

	// Test enabling version check.
	op := &Op{}
	op.applyOpts([]OpOption{WithVersionCheck(true)})
	assert.True(t, op.versionCheck, "versionCheck should be true when enabled")

	// Test disabling version check (explicit false).
	op2 := &Op{}
	op2.applyOpts([]OpOption{WithVersionCheck(false)})
	assert.False(t, op2.versionCheck, "versionCheck should be false when disabled")
}

// TestWithOffline verifies the WithOffline option sets offline mode correctly.
func TestWithOffline(t *testing.T) {
	t.Parallel()

	// Test enabling offline mode.
	op := &Op{}
	op.applyOpts([]OpOption{WithOffline(true)})
	assert.True(t, op.offline, "offline should be true when enabled")

	// Test disabling offline mode (explicit false).
	op2 := &Op{}
	op2.applyOpts([]OpOption{WithOffline(false)})
	assert.False(t, op2.offline, "offline should be false when disabled")
}

// TestWithArtifactDir verifies the WithArtifactDir option sets the directory correctly.
func TestWithArtifactDir(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name     string
		dir      string
		expected string
	}{
		{
			name:     "absolute path",
			dir:      "/opt/etcd-infra/artifacts",
			expected: "/opt/etcd-infra/artifacts",
		},
		{
			name:     "relative path",
			dir:      "./artifacts",
			expected: "./artifacts",
		},
		{
			name:     "empty directory",
			dir:      "",
			expected: "",
		},
		{
			name:     "path with spaces",
			dir:      "/opt/etcd-infra/my artifacts",
			expected: "/opt/etcd-infra/my artifacts",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			op := &Op{}
			op.applyOpts([]OpOption{WithArtifactDir(tc.dir)})
			assert.Equal(t, tc.expected, op.artifactDir)
		})
	}
}

// TestWithChecksumManifest verifies the WithChecksumManifest option sets the path correctly.
func TestWithChecksumManifest(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name     string
		path     string
		expected string
	}{
		{
			name:     "yaml manifest",
			path:     "/etc/etcd-infra/manifest.yaml",
			expected: "/etc/etcd-infra/manifest.yaml",
		},
		{
			name:     "json manifest",
			path:     "/etc/etcd-infra/manifest.json",
			expected: "/etc/etcd-infra/manifest.json",
		},
		{
			name:     "empty path",
			path:     "",
			expected: "",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			op := &Op{}
			op.applyOpts([]OpOption{WithChecksumManifest(tc.path)})
			assert.Equal(t, tc.expected, op.manifestPath)
		})
	}
}

// TestApplyOptsMultipleOptions verifies that multiple options can be applied in sequence
// and that later options override earlier ones for the same field.
func TestApplyOptsMultipleOptions(t *testing.T) {
	t.Parallel()

	op := &Op{}
	op.applyOpts([]OpOption{
		WithDownloadURL("https://first-url.com/etcd.tar.gz"),
		WithDownloadTimeout(30 * time.Second),
		WithVersionCheck(true),
		WithOffline(false),
		WithArtifactDir("/first/dir"),
		WithChecksumManifest("/first/manifest.yaml"),
		// Override some options with new values.
		WithDownloadURL("https://second-url.com/etcd.tar.gz"),
		WithArtifactDir("/second/dir"),
	})

	// Verify overridden values.
	assert.Equal(t, "https://second-url.com/etcd.tar.gz", op.downloadURL, "later URL should override earlier")
	assert.Equal(t, "/second/dir", op.artifactDir, "later artifactDir should override earlier")

	// Verify non-overridden values remain.
	assert.Equal(t, 30*time.Second, op.downloadTimeout, "timeout should remain unchanged")
	assert.True(t, op.versionCheck, "versionCheck should remain true")
	assert.False(t, op.offline, "offline should remain false")
	assert.Equal(t, "/first/manifest.yaml", op.manifestPath, "manifestPath should remain unchanged")
}

// TestApplyOptsIdempotent verifies that calling applyOpts multiple times
// with the same options produces consistent results.
func TestApplyOptsIdempotent(t *testing.T) {
	t.Parallel()

	opts := []OpOption{
		WithDownloadURL("https://example.com/etcd.tar.gz"),
		WithDownloadTimeout(2 * time.Minute),
		WithVersionCheck(true),
	}

	op1 := &Op{}
	op1.applyOpts(opts)

	op2 := &Op{}
	op2.applyOpts(opts)
	op2.applyOpts(opts) // Apply again.

	// Both should have identical values.
	assert.Equal(t, op1.downloadURL, op2.downloadURL)
	assert.Equal(t, op1.downloadTimeout, op2.downloadTimeout)
	assert.Equal(t, op1.versionCheck, op2.versionCheck)
}

// TestOptionFunctionType verifies that OpOption is a proper function type
// that can be stored and called later. This tests the functional options pattern.
func TestOptionFunctionType(t *testing.T) {
	t.Parallel()

	// Create a slice of options to apply later.
	options := []OpOption{
		WithDownloadURL("https://example.com/etcd.tar.gz"),
		WithOffline(true),
	}

	// Apply the collected options.
	op := &Op{}
	op.applyOpts(options)

	assert.Equal(t, "https://example.com/etcd.tar.gz", op.downloadURL)
	assert.True(t, op.offline)
}

// TestOfflineModeConfiguration verifies common offline mode configurations
// where multiple related options are set together.
func TestOfflineModeConfiguration(t *testing.T) {
	t.Parallel()

	// Typical offline configuration with artifact directory and manifest.
	op := &Op{}
	op.applyOpts([]OpOption{
		WithOffline(true),
		WithArtifactDir("/opt/etcd-infra/artifacts"),
		WithChecksumManifest("/opt/etcd-infra/manifest.yaml"),
	})

	require.True(t, op.offline, "offline mode should be enabled")
	require.Equal(t, "/opt/etcd-infra/artifacts", op.artifactDir, "artifact directory should be set")
	require.Equal(t, "/opt/etcd-infra/manifest.yaml", op.manifestPath, "manifest path should be set")
	// Timeout default should still be applied.
	require.Equal(t, time.Minute, op.downloadTimeout, "default timeout should be applied")
}

const etcdDownloadURL = "https://github.com/etcd-io/etcd/releases/download/v3.5.16/etcd-v3.5.16-linux-amd64.tar.gz"

// TestOnlineModeConfiguration verifies common online mode configurations
// where only URL and timeout are typically set.
func TestOnlineModeConfiguration(t *testing.T) {
	t.Parallel()

	// Typical online configuration with custom URL and version check.
	op := &Op{}
	op.applyOpts([]OpOption{
		WithDownloadURL(etcdDownloadURL),
		WithDownloadTimeout(5 * time.Minute),
		WithVersionCheck(true),
	})

	require.Equal(t, etcdDownloadURL, op.downloadURL)
	require.Equal(t, 5*time.Minute, op.downloadTimeout)
	require.True(t, op.versionCheck)
	require.False(t, op.offline, "offline should be false by default")
	require.Empty(t, op.artifactDir, "artifact directory should be empty for online mode")
}

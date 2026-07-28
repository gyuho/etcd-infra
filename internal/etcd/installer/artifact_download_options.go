package installer

import (
	"time"
)

// Op holds download options for etcd artifacts.
type Op struct {
	version         string
	downloadURL     string
	downloadTimeout time.Duration
	versionCheck    bool
	offline         bool
	artifactDir     string
	manifestPath    string
}

// OpOption mutates download options.
type OpOption func(*Op)

func (op *Op) applyOpts(opts []OpOption) {
	for _, opt := range opts {
		opt(op)
	}
	if op.downloadTimeout == 0 {
		op.downloadTimeout = time.Minute
	}
}

// WithVersion sets the etcd version for constructing the download URL.
// The version may or may not include the 'v' prefix (e.g., "3.6.8" or "v3.6.8");
// buildDownloadURL normalizes it automatically.
// This is required when no explicit download URL is provided.
func WithVersion(v string) OpOption {
	return func(op *Op) {
		op.version = v
	}
}

// WithDownloadURL sets the download URL for the artifact.
func WithDownloadURL(u string) OpOption {
	return func(op *Op) {
		op.downloadURL = u
	}
}

// WithDownloadTimeout sets the HTTP timeout for downloads.
func WithDownloadTimeout(d time.Duration) OpOption {
	return func(op *Op) {
		op.downloadTimeout = d
	}
}

// WithVersionCheck toggles post-download version checks.
func WithVersionCheck(b bool) OpOption {
	return func(op *Op) {
		op.versionCheck = b
	}
}

// WithOffline forces use of locally bundled artifacts; no network downloads.
func WithOffline(b bool) OpOption {
	return func(op *Op) {
		op.offline = b
	}
}

// WithArtifactDir sets the directory containing pre-bundled artifacts.
func WithArtifactDir(dir string) OpOption {
	return func(op *Op) {
		op.artifactDir = dir
	}
}

// WithChecksumManifest sets the checksum manifest path used for integrity verification.
func WithChecksumManifest(path string) OpOption {
	return func(op *Op) {
		op.manifestPath = path
	}
}

package install

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

var (
	errManifestPathEmpty    = errors.New("manifest path is empty")
	errUnexpectedFileSize   = errors.New("unexpected file size")
	errNegativeExpectedSize = errors.New("negative expected size")
	errSHA256Mismatch       = errors.New("sha256 mismatch")
)

// Manifest describes a set of artifacts and their integrity metadata.
// YAML is recommended, but JSON is also supported (YAML is a superset of JSON).
type Manifest struct {
	Version   string     `json:"version"   yaml:"version"`
	Artifacts []Artifact `json:"artifacts" yaml:"artifacts"`
}

// Artifact represents a single downloadable artifact.
// Name should match the artifact filename (for example, "etcd-v3.6.7-linux-x86_64.tar.gz").
type Artifact struct {
	Name   string `json:"name"           yaml:"name"`
	SHA256 string `json:"sha256"         yaml:"sha256"`
	Size   int64  `json:"size,omitempty" yaml:"size,omitempty"`
}

// LoadManifest reads and parses a checksum manifest file.
func LoadManifest(path string) (*Manifest, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errManifestPathEmpty
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read manifest file: %w", err)
	}
	m := new(Manifest)
	err = yaml.Unmarshal(b, m)
	if err != nil {
		return nil, fmt.Errorf("unmarshal manifest: %w", err)
	}

	return m, nil
}

// Find returns the artifact entry matching the given name.
func (m *Manifest) Find(name string) *Artifact {
	if m == nil {
		return nil
	}
	for i := range m.Artifacts {
		if m.Artifacts[i].Name == name {
			return &m.Artifacts[i]
		}
	}

	return nil
}

// VerifyFileSHA256 verifies a file against the expected SHA-256 hex string.
// If expectedSize > 0, it also verifies the file size.
// A negative expectedSize is an error.
func VerifyFileSHA256(path, expectedSHA256 string, expectedSize int64) error {
	expectedSHA256 = strings.TrimSpace(expectedSHA256)
	if expectedSize < 0 {
		return fmt.Errorf("%w: %d", errNegativeExpectedSize, expectedSize)
	}
	if expectedSize > 0 {
		info, err := os.Stat(path)
		if err != nil {
			return fmt.Errorf("stat file: %w", err)
		}
		if info.Size() != expectedSize {
			return fmt.Errorf("%w: for %s: got %d, want %d", errUnexpectedFileSize, path, info.Size(), expectedSize)
		}
	}
	actual, err := ComputeSHA256(path)
	if err != nil {
		return err
	}
	if !strings.EqualFold(actual, expectedSHA256) {
		return fmt.Errorf("%w: for %s: got %s, want %s", errSHA256Mismatch, path, actual, expectedSHA256)
	}

	return nil
}

// ComputeSHA256 returns the SHA-256 checksum hex string for a file.
func ComputeSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open file: %w", err)
	}
	defer func() { _ = f.Close() }()

	h := sha256.New()
	_, err = io.Copy(h, f)
	if err != nil {
		return "", fmt.Errorf("compute sha256: %w", err)
	}

	return hex.EncodeToString(h.Sum(nil)), nil
}

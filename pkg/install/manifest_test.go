package install

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLoadManifest(t *testing.T) {
	t.Parallel()
	t.Run("loads valid YAML manifest", func(t *testing.T) {
		t.Parallel()
		tmpDir := t.TempDir()
		manifestPath := filepath.Join(tmpDir, "manifest.yaml")

		content := `version: "1.0"
artifacts:
  - name: "test-binary"
    sha256: "abc123"
    size: 1024
  - name: "another-binary"
    sha256: "def456"
`
		err := os.WriteFile(manifestPath, []byte(content), 0o600)
		require.NoError(t, err)

		m, err := LoadManifest(manifestPath)
		require.NoError(t, err)
		require.NotNil(t, m)
		require.Equal(t, "1.0", m.Version)
		require.Len(t, m.Artifacts, 2)
		require.Equal(t, "test-binary", m.Artifacts[0].Name)
		require.Equal(t, "abc123", m.Artifacts[0].SHA256)
		require.Equal(t, int64(1024), m.Artifacts[0].Size)
		require.Equal(t, "another-binary", m.Artifacts[1].Name)
	})

	t.Run("loads valid JSON manifest", func(t *testing.T) {
		t.Parallel()
		tmpDir := t.TempDir()
		manifestPath := filepath.Join(tmpDir, "manifest.json")

		content := `{
  "version": "2.0",
  "artifacts": [
    {"name": "binary", "sha256": "deadbeef", "size": 2048}
  ]
}`
		err := os.WriteFile(manifestPath, []byte(content), 0o600)
		require.NoError(t, err)

		m, err := LoadManifest(manifestPath)
		require.NoError(t, err)
		require.NotNil(t, m)
		require.Equal(t, "2.0", m.Version)
		require.Len(t, m.Artifacts, 1)
	})

	t.Run("returns error for empty path", func(t *testing.T) {
		t.Parallel()
		_, err := LoadManifest("")
		require.Error(t, err)
		require.Contains(t, err.Error(), "manifest path is empty")
	})

	t.Run("returns error for whitespace-only path", func(t *testing.T) {
		t.Parallel()
		_, err := LoadManifest("   ")
		require.Error(t, err)
		require.Contains(t, err.Error(), "manifest path is empty")
	})

	t.Run("returns error for non-existent file", func(t *testing.T) {
		t.Parallel()
		_, err := LoadManifest("/nonexistent/manifest.yaml")
		require.Error(t, err)
	})

	t.Run("returns error for invalid YAML", func(t *testing.T) {
		t.Parallel()
		tmpDir := t.TempDir()
		manifestPath := filepath.Join(tmpDir, "invalid.yaml")

		err := os.WriteFile(manifestPath, []byte("invalid: yaml: content: ["), 0o600)
		require.NoError(t, err)

		_, err = LoadManifest(manifestPath)
		require.Error(t, err)
	})
}

func TestManifest_Find(t *testing.T) {
	t.Parallel()
	m := &Manifest{
		Version: "1.0",
		Artifacts: []Artifact{
			{Name: "etcd", SHA256: "aaa", Size: 100},
			{Name: "etcdctl", SHA256: "bbb", Size: 200},
			{Name: "etcdutl", SHA256: "ccc", Size: 300},
		},
	}

	t.Run("finds existing artifact", func(t *testing.T) {
		t.Parallel()
		a := m.Find("etcdctl")
		require.NotNil(t, a)
		require.Equal(t, "etcdctl", a.Name)
		require.Equal(t, "bbb", a.SHA256)
		require.Equal(t, int64(200), a.Size)
	})

	t.Run("returns nil for non-existent artifact", func(t *testing.T) {
		t.Parallel()
		a := m.Find("nonexistent")
		require.Nil(t, a)
	})

	t.Run("returns nil when manifest is nil", func(t *testing.T) {
		t.Parallel()
		var nilManifest *Manifest
		a := nilManifest.Find("test")
		require.Nil(t, a)
	})

	t.Run("finds first artifact", func(t *testing.T) {
		t.Parallel()
		a := m.Find("etcd")
		require.NotNil(t, a)
		require.Equal(t, "etcd", a.Name)
	})

	t.Run("finds last artifact", func(t *testing.T) {
		t.Parallel()
		a := m.Find("etcdutl")
		require.NotNil(t, a)
		require.Equal(t, "etcdutl", a.Name)
	})
}

func TestComputeSHA256(t *testing.T) {
	t.Parallel()
	t.Run("computes correct SHA256 hash", func(t *testing.T) {
		t.Parallel()
		tmpDir := t.TempDir()
		testFile := filepath.Join(tmpDir, "testfile")
		content := []byte("hello world")
		err := os.WriteFile(testFile, content, 0o600)
		require.NoError(t, err)

		// Compute expected hash
		h := sha256.Sum256(content)
		expected := hex.EncodeToString(h[:])

		actual, err := ComputeSHA256(testFile)
		require.NoError(t, err)
		require.Equal(t, expected, actual)
	})

	t.Run("returns error for non-existent file", func(t *testing.T) {
		t.Parallel()
		_, err := ComputeSHA256("/nonexistent/file")
		require.Error(t, err)
	})

	t.Run("handles empty file", func(t *testing.T) {
		t.Parallel()
		tmpDir := t.TempDir()
		testFile := filepath.Join(tmpDir, "empty")
		err := os.WriteFile(testFile, []byte{}, 0o600)
		require.NoError(t, err)

		hash, err := ComputeSHA256(testFile)
		require.NoError(t, err)
		// SHA256 of empty string
		require.Equal(t, "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855", hash)
	})
}

func TestVerifyFileSHA256(t *testing.T) {
	t.Parallel()
	t.Run("verifies correct hash and size", func(t *testing.T) {
		t.Parallel()
		tmpDir := t.TempDir()
		testFile := filepath.Join(tmpDir, "testfile")
		content := []byte("test content for verification")
		err := os.WriteFile(testFile, content, 0o600)
		require.NoError(t, err)

		h := sha256.Sum256(content)
		expectedHash := hex.EncodeToString(h[:])

		err = VerifyFileSHA256(testFile, expectedHash, int64(len(content)))
		require.NoError(t, err)
	})

	t.Run("verifies correct hash without size check", func(t *testing.T) {
		t.Parallel()
		tmpDir := t.TempDir()
		testFile := filepath.Join(tmpDir, "testfile")
		content := []byte("test content")
		err := os.WriteFile(testFile, content, 0o600)
		require.NoError(t, err)

		h := sha256.Sum256(content)
		expectedHash := hex.EncodeToString(h[:])

		// Size 0 means no size check
		err = VerifyFileSHA256(testFile, expectedHash, 0)
		require.NoError(t, err)
	})

	t.Run("handles case-insensitive hash comparison", func(t *testing.T) {
		t.Parallel()
		tmpDir := t.TempDir()
		testFile := filepath.Join(tmpDir, "testfile")
		content := []byte("test")
		err := os.WriteFile(testFile, content, 0o600)
		require.NoError(t, err)

		// SHA256 of "test" in uppercase
		err = VerifyFileSHA256(testFile, "9F86D081884C7D659A2FEAA0C55AD015A3BF4F1B2B0B822CD15D6C15B0F00A08", 0)
		require.NoError(t, err)
	})

	t.Run("returns error for wrong hash", func(t *testing.T) {
		t.Parallel()
		tmpDir := t.TempDir()
		testFile := filepath.Join(tmpDir, "testfile")
		err := os.WriteFile(testFile, []byte("content"), 0o600)
		require.NoError(t, err)

		err = VerifyFileSHA256(testFile, "wronghash", 0)
		require.Error(t, err)
		require.Contains(t, err.Error(), "sha256 mismatch")
	})

	t.Run("returns error for wrong size", func(t *testing.T) {
		t.Parallel()
		tmpDir := t.TempDir()
		testFile := filepath.Join(tmpDir, "testfile")
		content := []byte("content")
		err := os.WriteFile(testFile, content, 0o600)
		require.NoError(t, err)

		h := sha256.Sum256(content)
		expectedHash := hex.EncodeToString(h[:])

		err = VerifyFileSHA256(testFile, expectedHash, 9999)
		require.Error(t, err)
		require.Contains(t, err.Error(), "unexpected file size")
	})

	t.Run("returns error for non-existent file with size check", func(t *testing.T) {
		t.Parallel()
		err := VerifyFileSHA256("/nonexistent/file", "hash", 100)
		require.Error(t, err)
	})

	t.Run("returns error for non-existent file without size check", func(t *testing.T) {
		t.Parallel()
		err := VerifyFileSHA256("/nonexistent/file", "hash", 0)
		require.Error(t, err)
	})

	t.Run("returns error for negative expected size", func(t *testing.T) {
		t.Parallel()
		tmpDir := t.TempDir()
		testFile := filepath.Join(tmpDir, "testfile")
		err := os.WriteFile(testFile, []byte("content"), 0o600)
		require.NoError(t, err)

		err = VerifyFileSHA256(testFile, "deadbeef", -1)
		require.Error(t, err)
		require.ErrorIs(t, err, errNegativeExpectedSize)
	})

	t.Run("trims whitespace from expected hash", func(t *testing.T) {
		t.Parallel()
		tmpDir := t.TempDir()
		testFile := filepath.Join(tmpDir, "testfile")
		content := []byte("test")
		err := os.WriteFile(testFile, content, 0o600)
		require.NoError(t, err)

		h := sha256.Sum256(content)
		hashWithWhitespace := "  " + hex.EncodeToString(h[:]) + "  "

		err = VerifyFileSHA256(testFile, hashWithWhitespace, 0)
		require.NoError(t, err)
	})

	t.Run("mismatch error uses trimmed expected hash", func(t *testing.T) {
		t.Parallel()
		tmpDir := t.TempDir()
		testFile := filepath.Join(tmpDir, "testfile")
		err := os.WriteFile(testFile, []byte("content"), 0o600)
		require.NoError(t, err)

		err = VerifyFileSHA256(testFile, "  wronghash  ", 0)
		require.Error(t, err)
		require.ErrorIs(t, err, errSHA256Mismatch)
		require.Contains(t, err.Error(), "want wronghash")
		require.NotContains(t, err.Error(), "want  wronghash")
	})
}

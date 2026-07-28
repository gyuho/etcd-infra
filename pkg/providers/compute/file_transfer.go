package compute

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	// DefaultFileTransferTimeout is the default timeout for single-file transfer
	// fallback operations (write, copy, download). Callers can override this by
	// setting a deadline on the context passed to the package-level helpers.
	DefaultFileTransferTimeout = 30 * time.Second

	// DefaultDirectoryTransferTimeout is the default timeout for directory transfer
	// fallback operations (tar + pipe). Callers can override this by setting a
	// deadline on the context.
	DefaultDirectoryTransferTimeout = 5 * time.Minute
)

// effectiveTimeout returns the context deadline's remaining duration if set,
// otherwise returns the provided fallback timeout. This ensures that callers
// who set explicit context deadlines are respected by fallback transfer paths.
func effectiveTimeout(ctx context.Context, fallback time.Duration) time.Duration {
	if deadline, ok := ctx.Deadline(); ok {
		if remaining := time.Until(deadline); remaining > 0 {
			return remaining
		}
	}
	return fallback
}

// FileTransferInstance is an optional capability for providers that support
// direct remote file transfer operations.
//
// Callers should prefer the package-level helpers (WriteFile, CopyFile,
// CopyDirectory, DownloadFile). These helpers use this capability when present
// and fall back to command-based transfer when it is absent.
type FileTransferInstance interface {
	Instance
	WriteFile(ctx context.Context, path, content string) error
	CopyFile(ctx context.Context, localPath, remotePath string) error
	CopyDirectory(ctx context.Context, localPath, remotePath string) error
	DownloadFile(ctx context.Context, remotePath, localPath string) error
}

// WriteFile writes content to a file on the target instance.
func WriteFile(ctx context.Context, inst Instance, path, content string) error {
	if inst == nil {
		return errors.New("instance is required")
	}
	if strings.TrimSpace(path) == "" {
		return errors.New("path is required")
	}

	if ft, ok := inst.(FileTransferInstance); ok {
		if err := ft.WriteFile(ctx, path, content); err == nil {
			return nil
		} else if !IsNotSupported(err) {
			return fmt.Errorf("provider write file %q: %w", path, err)
		}
	}

	// Fallback path for providers that only implement command execution.
	result, err := inst.RunCommandWithOptions(
		ctx,
		[]string{"sh", "-c", `mkdir -p "$(dirname "$1")" && cat > "$1"`, "etcd-infra-write-file", path},
		&RunCommandOptions{
			Timeout: effectiveTimeout(ctx, DefaultFileTransferTimeout),
			Stdin:   strings.NewReader(content),
		},
	)
	if err != nil {
		return fmt.Errorf("write file %q: %w", path, err)
	}
	if result == nil {
		return fmt.Errorf("write file %q: nil result", path)
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("write file %q failed with exit code %d: %s", path, result.ExitCode, strings.TrimSpace(result.Stderr))
	}
	return nil
}

// CopyFile copies a local file to the target instance.
func CopyFile(ctx context.Context, inst Instance, localPath, remotePath string) error {
	if inst == nil {
		return errors.New("instance is required")
	}
	if strings.TrimSpace(localPath) == "" {
		return errors.New("local path is required")
	}
	if strings.TrimSpace(remotePath) == "" {
		return errors.New("remote path is required")
	}
	if ft, ok := inst.(FileTransferInstance); ok {
		if err := ft.CopyFile(ctx, localPath, remotePath); err == nil {
			return nil
		} else if !IsNotSupported(err) {
			return fmt.Errorf("provider copy file %q -> %q: %w", localPath, remotePath, err)
		}
	}

	data, err := os.ReadFile(localPath)
	if err != nil {
		return fmt.Errorf("read local file %q: %w", localPath, err)
	}
	return WriteFile(ctx, inst, remotePath, string(data))
}

// CopyDirectory copies a local directory to the target instance.
func CopyDirectory(ctx context.Context, inst Instance, localPath, remotePath string) error {
	if inst == nil {
		return errors.New("instance is required")
	}
	if strings.TrimSpace(localPath) == "" {
		return errors.New("local path is required")
	}
	if strings.TrimSpace(remotePath) == "" {
		return errors.New("remote path is required")
	}
	if ft, ok := inst.(FileTransferInstance); ok {
		if err := ft.CopyDirectory(ctx, localPath, remotePath); err == nil {
			return nil
		} else if !IsNotSupported(err) {
			return fmt.Errorf("provider copy directory %q -> %q: %w", localPath, remotePath, err)
		}
	}

	info, err := os.Stat(localPath)
	if err != nil {
		return fmt.Errorf("stat local directory %q: %w", localPath, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("local path %q is not a directory", localPath)
	}

	var tarBuf bytes.Buffer
	if tarErr := CreateTarArchive(localPath, &tarBuf); tarErr != nil {
		return fmt.Errorf("create tar archive for %q: %w", localPath, tarErr)
	}

	result, err := inst.RunCommandWithOptions(
		ctx,
		[]string{"sh", "-c", `mkdir -p "$1" && tar -xf - -C "$1"`, "etcd-infra-copy-dir", remotePath},
		&RunCommandOptions{
			Timeout: effectiveTimeout(ctx, DefaultDirectoryTransferTimeout),
			Stdin:   &tarBuf,
		},
	)
	if err != nil {
		return fmt.Errorf("copy directory %q -> %q: %w", localPath, remotePath, err)
	}
	if result == nil {
		return fmt.Errorf("copy directory %q -> %q: nil result", localPath, remotePath)
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("copy directory %q -> %q failed with exit code %d: %s", localPath, remotePath, result.ExitCode, strings.TrimSpace(result.Stderr))
	}
	return nil
}

// DownloadFile downloads a remote file from the target instance.
func DownloadFile(ctx context.Context, inst Instance, remotePath, localPath string) error {
	if inst == nil {
		return errors.New("instance is required")
	}
	if strings.TrimSpace(remotePath) == "" {
		return errors.New("remote path is required")
	}
	if strings.TrimSpace(localPath) == "" {
		return errors.New("local path is required")
	}
	if ft, ok := inst.(FileTransferInstance); ok {
		if err := ft.DownloadFile(ctx, remotePath, localPath); err == nil {
			return nil
		} else if !IsNotSupported(err) {
			return fmt.Errorf("provider download file %q -> %q: %w", remotePath, localPath, err)
		}
	}

	result, err := inst.RunCommandWithOptions(ctx, []string{"cat", remotePath}, &RunCommandOptions{
		Timeout: effectiveTimeout(ctx, DefaultFileTransferTimeout),
	})
	if err != nil {
		return fmt.Errorf("download file %q: %w", remotePath, err)
	}
	if result == nil {
		return fmt.Errorf("download file %q: nil result", remotePath)
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("download file %q failed with exit code %d: %s", remotePath, result.ExitCode, strings.TrimSpace(result.Stderr))
	}

	if err := os.MkdirAll(filepath.Dir(localPath), 0o750); err != nil {
		return fmt.Errorf("create local directory for %q: %w", localPath, err)
	}
	if err := os.WriteFile(localPath, []byte(result.Stdout), 0o600); err != nil {
		return fmt.Errorf("write local file %q: %w", localPath, err)
	}
	return nil
}

// CreateTarArchive creates a tar archive of the directory at root and writes
// it to out. Used by CopyDirectory and available for provider packages that
// need tar-based file transfer without duplicating the implementation.
func CreateTarArchive(root string, out io.Writer) error {
	tw := tar.NewWriter(out)

	if walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return fmt.Errorf("walk path %q: %w", path, walkErr)
		}

		if path == root {
			return nil
		}

		info, err := d.Info()
		if err != nil {
			return fmt.Errorf("read file info for %q: %w", path, err)
		}

		relPath, err := filepath.Rel(root, path)
		if err != nil {
			return fmt.Errorf("compute relative path for %q: %w", path, err)
		}

		// Resolve symlink targets so tar records the link correctly.
		var linkTarget string
		if info.Mode()&fs.ModeSymlink != 0 {
			linkTarget, err = os.Readlink(path)
			if err != nil {
				return fmt.Errorf("read symlink target for %q: %w", path, err)
			}
		}

		header, err := tar.FileInfoHeader(info, linkTarget)
		if err != nil {
			return fmt.Errorf("build tar header for %q: %w", path, err)
		}
		header.Name = filepath.ToSlash(relPath)

		if writeHeaderErr := tw.WriteHeader(header); writeHeaderErr != nil {
			return fmt.Errorf("write tar header for %q: %w", path, writeHeaderErr)
		}
		if !info.Mode().IsRegular() {
			return nil
		}

		file, err := os.Open(path)
		if err != nil {
			return fmt.Errorf("open file %q: %w", path, err)
		}
		defer file.Close() //nolint:errcheck

		if _, err := io.Copy(tw, file); err != nil {
			return fmt.Errorf("copy file %q into tar: %w", path, err)
		}
		return nil
	}); walkErr != nil {
		return fmt.Errorf("walk directory %q: %w", root, walkErr)
	}

	// Close explicitly to flush the tar end-of-archive marker and surface
	// any write errors instead of silently discarding them via defer.
	if err := tw.Close(); err != nil {
		return fmt.Errorf("close tar writer: %w", err)
	}
	return nil
}

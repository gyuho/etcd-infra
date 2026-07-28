package archive

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"go.uber.org/zap"

	"git.tbd/etcd-infra/pkg/log"
)

const defaultDirPerm = 0o750

// TarGzSingleFile wraps a single file in a tar+gzip archive.
// The tar entry uses filepath.Base(srcFile) as its name and preserves the
// source file's permissions. This produces a standard .tar.gz that UntarGz
// can extract.
func TarGzSingleFile(srcFile, dstArchive string) error {
	info, err := os.Stat(srcFile)
	if err != nil {
		return fmt.Errorf("stat source %s: %w", srcFile, err)
	}
	if info.IsDir() {
		return fmt.Errorf("source %s is a directory, not a file", srcFile)
	}

	src, err := os.Open(srcFile)
	if err != nil {
		return fmt.Errorf("open source %s: %w", srcFile, err)
	}
	defer func() {
		if closeErr := src.Close(); closeErr != nil {
			log.L().Warn("failed to close source file", zap.Error(closeErr))
		}
	}()

	dst, err := os.Create(dstArchive)
	if err != nil {
		return fmt.Errorf("create archive %s: %w", dstArchive, err)
	}
	defer func() {
		if closeErr := dst.Close(); closeErr != nil {
			log.L().Warn("failed to close archive file", zap.Error(closeErr))
		}
	}()

	gzWriter := gzip.NewWriter(dst)
	defer func() {
		if closeErr := gzWriter.Close(); closeErr != nil {
			log.L().Warn("failed to close gzip writer", zap.Error(closeErr))
		}
	}()

	tarWriter := tar.NewWriter(gzWriter)
	defer func() {
		if closeErr := tarWriter.Close(); closeErr != nil {
			log.L().Warn("failed to close tar writer", zap.Error(closeErr))
		}
	}()

	header := &tar.Header{
		Name: filepath.Base(srcFile),
		Size: info.Size(),
		Mode: int64(info.Mode().Perm()),
	}
	if err := tarWriter.WriteHeader(header); err != nil {
		return fmt.Errorf("write tar header for %s: %w", srcFile, err)
	}
	if _, err := io.Copy(tarWriter, src); err != nil {
		return fmt.Errorf("write tar body for %s: %w", srcFile, err)
	}
	return nil
}

// UntarGz extracts a gzip-compressed tar archive into dest.
//
//nolint:gocognit,gocyclo,cyclop // Archive extraction includes multiple IO paths and checks.
func UntarGz(tarGzFile, dest string) error {
	file, err := os.Open(tarGzFile)
	if err != nil {
		return fmt.Errorf("open tar.gz %s: %w", tarGzFile, err)
	}
	defer func() {
		closeErr := file.Close()
		if closeErr != nil {
			log.L().Warn("failed to close file", zap.Error(closeErr))
		}
	}()

	gzipReader, err := gzip.NewReader(file)
	if err != nil {
		return fmt.Errorf("create gzip reader for %s: %w", tarGzFile, err)
	}
	defer func() {
		closeErr := gzipReader.Close()
		if closeErr != nil {
			log.L().Warn("failed to close gzip reader", zap.Error(closeErr))
		}
	}()

	tarReader := tar.NewReader(gzipReader)

	for {
		header, err := tarReader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("read tar entry: %w", err)
		}

		// SECURITY: Resolve every archive entry through secureExtractPath to prevent
		// Zip Slip style traversal. Without this check, a crafted header.Name such as
		// "../../etc/cron.d/pwn" would write outside dest and could overwrite
		// privileged files during bootstrap/restore.
		target, pathErr := secureExtractPath(dest, header.Name)
		if pathErr != nil {
			return fmt.Errorf("resolve target path for %q: %w", header.Name, pathErr)
		}

		switch header.Typeflag {
		case tar.TypeDir:
			dirMode := header.FileInfo().Mode().Perm()
			if dirMode == 0 {
				// SECURITY: Default to a restrictive directory mode when archive metadata
				// is missing. If we allow permissive defaults, extracted content can become
				// group/world writable and enable local privilege abuse.
				dirMode = defaultDirPerm
			}
			err = os.MkdirAll(target, dirMode)
			if err != nil {
				return fmt.Errorf("create directory %s: %w", target, err)
			}
		case tar.TypeReg:
			err = os.MkdirAll(filepath.Dir(target), defaultDirPerm)
			if err != nil {
				return fmt.Errorf("create parent directory for %s: %w", target, err)
			}
			// Remove existing regular file first to avoid ETXTBSY ("text file
			// busy") when overwriting a running executable (e.g., containerd).
			// unlink(2) frees the directory entry while the running process
			// keeps its file descriptor to the old inode.
			if fi, statErr := os.Lstat(target); statErr == nil && fi.Mode().IsRegular() {
				_ = os.Remove(target)
			}
			// SECURITY: Create files with a restrictive mode first (0600), then apply
			// the sanitized archive mode below. If we create directly with untrusted
			// metadata, a malicious archive could momentarily expose sensitive content
			// with overly broad permissions.
			outFile, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
			if err != nil {
				return fmt.Errorf("create file %s: %w", target, err)
			}
			//nolint:gosec // G110: Decompression bomb risk is acceptable for trusted archives
			_, copyErr := io.Copy(outFile, tarReader)
			if copyErr != nil {
				closeErr := outFile.Close()
				if closeErr != nil {
					log.L().Warn("failed to close output file", zap.Error(closeErr))
				}

				return fmt.Errorf("copy file %s: %w", target, copyErr)
			}
			closeErr := outFile.Close()
			if closeErr != nil {
				log.L().Warn("failed to close output file", zap.Error(closeErr))
			}

			// SECURITY: Preserve file permissions from the tar header after extraction.
			// os.Create uses 0666 (umask-applied) which drops execute bits;
			// binaries like containerd require the original permissions.
			// If we skip this, executables may become non-executable and break node bootstrap.
			if perm := header.FileInfo().Mode().Perm(); perm != 0 {
				if chmodErr := os.Chmod(target, perm); chmodErr != nil {
					return fmt.Errorf("chmod %s: %w", target, chmodErr)
				}
			}
		case tar.TypeSymlink, tar.TypeLink, tar.TypeChar, tar.TypeBlock, tar.TypeFifo:
			// SECURITY: Explicitly reject link/device entries. If we silently accept
			// these from untrusted archives, attackers can redirect writes or create
			// unsafe filesystem objects that bypass destination confinement.
			return fmt.Errorf("%w: %q (type=%d)", errArchiveUnsupportedEntry, header.Name, header.Typeflag)
		default:
			// SECURITY: Reject unknown tar entry types by default.
			// Failing open here can reintroduce path or device handling bugs.
			return fmt.Errorf("%w: %q (type=%d)", errArchiveUnsupportedEntry, header.Name, header.Typeflag)
		}
	}

	return nil
}

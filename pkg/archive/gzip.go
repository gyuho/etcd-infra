package archive

import (
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"go.uber.org/zap"

	"git.tbd/etcd-infra/pkg/log"
)

// GzipFile compresses src into dst using gzip.
// This is a single-file gzip operation (not tar+gzip).
// Example: GzipFile("/opt/etcd", "/opt/etcd.gz").
func GzipFile(src, dst string) error {
	inFile, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open source %s: %w", src, err)
	}
	defer func() {
		if closeErr := inFile.Close(); closeErr != nil {
			log.L().Warn("failed to close source file", zap.Error(closeErr))
		}
	}()

	outFile, err := os.Create(dst)
	if err != nil {
		return fmt.Errorf("create destination %s: %w", dst, err)
	}
	defer func() {
		if closeErr := outFile.Close(); closeErr != nil {
			log.L().Warn("failed to close destination file", zap.Error(closeErr))
		}
	}()

	gzWriter := gzip.NewWriter(outFile)
	defer func() {
		if closeErr := gzWriter.Close(); closeErr != nil {
			log.L().Warn("failed to close gzip writer", zap.Error(closeErr))
		}
	}()

	// Set the original filename in the gzip header.
	gzWriter.Name = filepath.Base(src)

	if _, err := io.Copy(gzWriter, inFile); err != nil {
		return fmt.Errorf("compress %s: %w", src, err)
	}
	return nil
}

// GunzipFile decompresses a gzip file from src into dst.
// This is a single-file gunzip operation (not tar+gzip).
// Example: GunzipFile("/opt/etcd.gz", "/opt/etcd").
func GunzipFile(src, dst string) error {
	inFile, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open source %s: %w", src, err)
	}
	defer func() {
		if closeErr := inFile.Close(); closeErr != nil {
			log.L().Warn("failed to close source file", zap.Error(closeErr))
		}
	}()

	gzReader, err := gzip.NewReader(inFile)
	if err != nil {
		return fmt.Errorf("create gzip reader for %s: %w", src, err)
	}
	defer func() {
		if closeErr := gzReader.Close(); closeErr != nil {
			log.L().Warn("failed to close gzip reader", zap.Error(closeErr))
		}
	}()

	outFile, err := os.Create(dst)
	if err != nil {
		return fmt.Errorf("create destination %s: %w", dst, err)
	}
	defer func() {
		if closeErr := outFile.Close(); closeErr != nil {
			log.L().Warn("failed to close destination file", zap.Error(closeErr))
		}
	}()

	//nolint:gosec // G110: Decompression bomb risk is acceptable for trusted archives
	if _, err := io.Copy(outFile, gzReader); err != nil {
		return fmt.Errorf("decompress %s: %w", src, err)
	}
	return nil
}

// GzipReader returns an io.ReadCloser that yields gzip-compressed data from the
// given file on-the-fly. No temporary file is created. The caller MUST close the
// returned reader to avoid goroutine leaks.
//
// This is used for streaming upload to S3; the S3 client reads compressed bytes
// from the reader while a background goroutine feeds uncompressed file data in.
func GzipReader(path string) (io.ReadCloser, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}

	pr, pw := io.Pipe()

	go func() {
		gzw := gzip.NewWriter(pw)
		gzw.Name = filepath.Base(path)

		_, copyErr := io.Copy(gzw, f)
		closeErr := gzw.Close()
		_ = f.Close()

		//nolint:gocritic // Multiple error conditions, not suitable for switch
		if copyErr != nil {
			_ = pw.CloseWithError(fmt.Errorf("compress %s: %w", path, copyErr))
		} else if closeErr != nil {
			_ = pw.CloseWithError(fmt.Errorf("flush gzip for %s: %w", path, closeErr))
		} else {
			_ = pw.Close()
		}
	}()

	return pr, nil
}

// IsAlreadyCompressed returns true if the file extension indicates it is already
// a compressed or binary archive. These files should not be double-compressed.
// Container image .tar files are included: they are uploaded as-is to S3 so
// that `ctr images import` can consume them directly without decompression.
func IsAlreadyCompressed(path string) bool {
	lower := strings.ToLower(path)
	return strings.HasSuffix(lower, ".gz") ||
		strings.HasSuffix(lower, ".tgz") ||
		strings.HasSuffix(lower, ".zip") ||
		strings.HasSuffix(lower, ".bz2") ||
		strings.HasSuffix(lower, ".xz") ||
		strings.HasSuffix(lower, ".zst") ||
		strings.HasSuffix(lower, ".tar")
}

// IsGzipCompressed reports whether the file at path begins with the gzip magic
// bytes (0x1f 0x8b). Returns false for files shorter than 2 bytes.
func IsGzipCompressed(path string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, fmt.Errorf("open %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	var header [2]byte
	n, _ := io.ReadFull(f, header[:])
	if n < 2 {
		return false, nil //nolint:nilerr // short file is simply not gzip
	}
	return header[0] == 0x1f && header[1] == 0x8b, nil
}

// DecompressDirectory walks dir and decompresses any file that was transparently
// gzip-compressed during upload (S3 keys preserve original names, content is gzip).
// Files with natively-compressed extensions (.gz, .tgz, .zip, etc.) are skipped
// because they were uploaded as-is and are handled by their own extraction tools
// (e.g. tar -xzf).
//
// Decompression is in-place: the compressed file is replaced by its uncompressed
// content, preserving the original file permissions.
func DecompressDirectory(dir string) error {
	return filepath.WalkDir(dir, func(path string, d os.DirEntry, walkErr error) error { //nolint:wrapcheck // External package error
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		if IsAlreadyCompressed(path) {
			return nil
		}
		compressed, err := IsGzipCompressed(path)
		if err != nil {
			return fmt.Errorf("check gzip header for %s: %w", path, err)
		}
		if !compressed {
			return nil
		}

		info, err := d.Info()
		if err != nil {
			return fmt.Errorf("stat %s: %w", path, err)
		}
		mode := info.Mode()

		tmpPath := path + ".gunzip-tmp"
		if err := GunzipFile(path, tmpPath); err != nil {
			_ = os.Remove(tmpPath)
			return fmt.Errorf("decompress %s: %w", path, err)
		}
		if err := os.Chmod(tmpPath, mode); err != nil {
			_ = os.Remove(tmpPath)
			return fmt.Errorf("chmod %s: %w", tmpPath, err)
		}
		if err := os.Rename(tmpPath, path); err != nil {
			_ = os.Remove(tmpPath)
			return fmt.Errorf("rename %s to %s: %w", tmpPath, path, err)
		}
		return nil
	})
}

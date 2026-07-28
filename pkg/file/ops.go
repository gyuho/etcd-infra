// Copyright (c) Tailscale Inc & AUTHORS
// SPDX-License-Identifier: BSD-3-Clause

package file

import (
	"fmt"
	"io"
	"math/rand"
	"os"
	"path/filepath"
	"runtime"

	"go.uber.org/zap"

	"git.tbd/etcd-infra/pkg/log"
)

// RemoveIfExists removes the file at path.
// It returns nil when the file does not exist.
func RemoveIfExists(path string) error {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove file %s: %w", path, err)
	}
	return nil
}

// Exists checks if a file exists.
func Exists(p string) (bool, error) {
	_, err := os.Stat(p)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("failed to check file existence: %w", err)
	}

	return true, nil
}

// DirExists checks if a directory exists.
func DirExists(p string) (bool, error) {
	stat, err := os.Stat(p)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("failed to check directory existence: %w", err)
	}
	if !stat.IsDir() {
		return false, nil
	}

	return true, nil
}

// CopyFile copies a file from src to dst, creating parent directories as needed.
// It removes any existing file at dst first to avoid ETXTBSY ("text file busy")
// errors when overwriting a running executable (e.g., etcd, containerd).
// The destination file is synced to disk before returning.
func CopyFile(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil { //nolint:gosec // Intentional permission/operation
		return fmt.Errorf("create parent directory for %s: %w", dst, err)
	}

	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open source file: %w", err)
	}
	defer func() { _ = in.Close() }()

	// Remove existing file to avoid ETXTBSY when dst is a running binary.
	_ = os.Remove(dst)

	out, err := os.Create(dst)
	if err != nil {
		return fmt.Errorf("create destination file: %w", err)
	}

	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return fmt.Errorf("copy file contents: %w", err)
	}

	if err := out.Sync(); err != nil {
		_ = out.Close()
		return fmt.Errorf("sync destination file: %w", err)
	}

	if err := out.Close(); err != nil {
		return fmt.Errorf("close destination file: %w", err)
	}

	return nil
}

type tempFile interface {
	Write(p []byte) (int, error)
	Sync() error
	Close() error
	Name() string
}

type fileOps struct {
	stat       func(string) (os.FileInfo, error)
	createTemp func(string, string) (tempFile, error)
	chmod      func(string, os.FileMode) error
	rename     func(string, string) error
	remove     func(string) error
}

//nolint:gochecknoglobals // Mutable for testing
var defaultFileOps = fileOps{
	stat: os.Stat,
	createTemp: func(dir, pattern string) (tempFile, error) {
		return os.CreateTemp(dir, pattern)
	},
	chmod:  os.Chmod,
	rename: os.Rename,
	remove: os.Remove,
}

// WriteAtomic writes data to a file atomically with the default permission (0600).
// Copied from https://pkg.go.dev/tailscale.com/atomicfile#WriteFile.
func WriteAtomic(file string, d []byte) error {
	return writeFileAtomicPermWith(file, d, permFile, defaultFileOps)
}

// WriteAtomicPerm writes data to a file atomically with explicit permissions.
func WriteAtomicPerm(file string, d []byte, perm os.FileMode) error {
	return writeFileAtomicPermWith(file, d, perm, defaultFileOps)
}

//nolint:nonamedreturns // err is named for defer access
func writeFileAtomicPermWith(file string, d []byte, perm os.FileMode, ops fileOps) (err error) {
	fi, err := ops.stat(file)
	if err == nil && !fi.Mode().IsRegular() {
		return fmt.Errorf("%s %w", file, errNotRegularFile)
	}
	//nolint:gosec // G404: math/rand is sufficient for temp file naming (not security-critical)
	f, err := ops.createTemp(filepath.Dir(file), fmt.Sprintf("%x", rand.Int63()))
	if err != nil {
		return err
	}
	tmpName := f.Name()
	defer func() {
		if err != nil {
			closeErr := f.Close()
			if closeErr != nil {
				log.L().Warn("failed to close temp file", zap.Error(closeErr))
			}
			removeErr := ops.remove(tmpName)
			if removeErr != nil {
				log.L().Warn("failed to remove temp file", zap.Error(removeErr), zap.String("path", tmpName))
			}
		}
	}()
	_, err = f.Write(d)
	if err != nil {
		return fmt.Errorf("failed to write temp file: %w", err)
	}
	if runtime.GOOS != "windows" {
		err = ops.chmod(tmpName, perm)
		if err != nil {
			return err
		}
	}
	err = f.Sync()
	if err != nil {
		return fmt.Errorf("failed to sync temp file: %w", err)
	}
	err = f.Close()
	if err != nil {
		return fmt.Errorf("failed to close temp file: %w", err)
	}

	return ops.rename(tmpName, file)
}

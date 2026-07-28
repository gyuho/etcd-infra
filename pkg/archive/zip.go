package archive

import (
	"archive/zip"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"go.uber.org/zap"

	"git.tbd/etcd-infra/pkg/log"
)

// Unzip extracts the given zip file to the given destination.
//
//nolint:gocognit // Zip extraction handles multiple IO paths and error cases.
func Unzip(zipFile, dest string) error {
	r, err := zip.OpenReader(zipFile)
	if err != nil {
		return fmt.Errorf("open zip %s: %w", zipFile, err)
	}
	defer func() {
		closeErr := r.Close()
		if closeErr != nil {
			log.L().Warn("failed to close zip reader", zap.Error(closeErr))
		}
	}()

	for _, f := range r.File {
		// SECURITY: Resolve entry path via secureExtractPath to block traversal
		// payloads (e.g. "../.."). Without this, a malicious ZIP can overwrite
		// arbitrary host files outside the extraction directory.
		fpath, pathErr := secureExtractPath(dest, f.Name)
		if pathErr != nil {
			return fmt.Errorf("resolve target path for %q: %w", f.Name, pathErr)
		}

		if f.FileInfo().IsDir() {
			mode := f.Mode().Perm()
			if mode == 0 {
				// SECURITY: Use restrictive default directory permissions when zip
				// metadata is empty. Permissive defaults can expose extracted data to
				// unintended local users.
				mode = defaultDirPerm
			}
			err = os.MkdirAll(fpath, mode)
			if err != nil {
				return fmt.Errorf("create directory %s: %w", fpath, err)
			}

			continue
		}

		err = os.MkdirAll(filepath.Dir(fpath), defaultDirPerm)
		if err != nil {
			return fmt.Errorf("create parent directory for %s: %w", fpath, err)
		}

		mode := f.Mode().Perm()
		if mode == 0 {
			// SECURITY: Create files with least-privilege defaults when archive
			// metadata omits permissions. This prevents world-readable secrets by
			// default and narrows blast radius if extraction directory is shared.
			mode = 0o600
		}

		outFile, err := os.OpenFile(fpath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
		if err != nil {
			return fmt.Errorf("open file %s: %w", fpath, err)
		}

		rc, err := f.Open()
		if err != nil {
			_ = outFile.Close()
			return fmt.Errorf("open zip entry %s: %w", f.Name, err)
		}

		//nolint:gosec // G110: Decompression bomb risk is acceptable for trusted archives
		_, err = io.Copy(outFile, rc)

		closeErr := outFile.Close()
		if closeErr != nil {
			log.L().Warn("failed to close output file", zap.Error(closeErr))
		}
		closeErr = rc.Close()
		if closeErr != nil {
			log.L().Warn("failed to close zip file reader", zap.Error(closeErr))
		}

		if err != nil {
			return fmt.Errorf("copy zip entry %s: %w", f.Name, err)
		}
	}

	return nil
}

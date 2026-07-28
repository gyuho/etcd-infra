package archive

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

var (
	errArchiveEntryEmptyName      = errors.New("archive entry name is empty")
	errArchiveEntryAbsolutePath   = errors.New("archive entry path must be relative")
	errArchiveEntryPathTraversal  = errors.New("archive entry escapes destination directory")
	errArchiveEntrySymlinkInPath  = errors.New("archive extraction path traverses a symlink")
	errArchiveUnsupportedEntry    = errors.New("archive entry type is not supported")
	errArchivePathValidationError = errors.New("archive path validation failed")
)

// secureExtractPath resolves an archive entry path relative to dest and ensures
// the result is confined to dest.
func secureExtractPath(dest, entryName string) (string, error) {
	// SECURITY: Empty or dot-only names are ambiguous and can bypass expected
	// extraction semantics. Rejecting them keeps archive processing deterministic.
	// If omitted, attackers can exploit edge-case path normalization behavior.
	trimmed := strings.TrimSpace(entryName)
	if trimmed == "" || trimmed == "." {
		return "", errArchiveEntryEmptyName
	}
	// SECURITY: Null bytes in filenames can truncate paths at the C/OS level
	// while Go strings see the full value, creating a mismatch between
	// validation and actual filesystem writes (CWE-158). Linux rejects them
	// at the syscall level, but defense-in-depth demands explicit rejection.
	if strings.ContainsRune(trimmed, 0) {
		return "", fmt.Errorf("%w: contains null byte", errArchiveEntryPathTraversal)
	}

	cleanName := filepath.Clean(trimmed)
	// SECURITY: Absolute paths must be rejected because they ignore dest and write
	// directly to host paths. If omitted, archives can overwrite system files.
	if filepath.IsAbs(trimmed) || filepath.IsAbs(cleanName) {
		return "", fmt.Errorf("%w: %q", errArchiveEntryAbsolutePath, entryName)
	}
	// SECURITY: Explicitly block parent directory traversal before join.
	// If omitted, "../" segments can escape dest (Zip Slip).
	if cleanName == ".." || strings.HasPrefix(cleanName, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("%w: %q", errArchiveEntryPathTraversal, entryName)
	}

	destClean := filepath.Clean(dest)
	// SECURITY: Destination root itself must not be a symlink. If we only check
	// subcomponents, a symlinked extraction root can redirect all writes outside
	// the intended tree.
	if err := ensureDestinationRootSafe(destClean); err != nil {
		return "", err
	}

	targetClean := filepath.Clean(filepath.Join(destClean, cleanName))
	// SECURITY: Prefix check after full normalization is the core confinement
	// guardrail. If omitted, crafted names can still resolve outside dest.
	if targetClean != destClean &&
		!strings.HasPrefix(targetClean, destClean+string(os.PathSeparator)) {
		return "", fmt.Errorf("%w: %q", errArchiveEntryPathTraversal, entryName)
	}

	// SECURITY: Refuse paths that traverse an existing symlink component. Without
	// this, an attacker can pre-place symlinks and redirect extraction writes to
	// arbitrary host locations even if lexical path checks pass.
	if err := ensureNoSymlinkComponent(destClean, targetClean); err != nil {
		return "", err
	}

	return targetClean, nil
}

func ensureDestinationRootSafe(dest string) error {
	info, err := os.Lstat(dest)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			// Missing destination roots are allowed; callers may create them during
			// extraction. Symlink safety is enforced for any existing roots.
			return nil
		}
		return fmt.Errorf("%w: lstat destination %q: %w", errArchivePathValidationError, dest, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%w: destination root %q", errArchiveEntrySymlinkInPath, dest)
	}
	return nil
}

func ensureNoSymlinkComponent(dest, target string) error {
	rel, err := filepath.Rel(dest, target)
	if err != nil {
		return fmt.Errorf("%w: relative path: %w", errArchivePathValidationError, err)
	}
	if rel == "." {
		return nil
	}

	current := dest
	for part := range strings.SplitSeq(rel, string(os.PathSeparator)) {
		if part == "" || part == "." {
			continue
		}
		current = filepath.Join(current, part)

		info, err := os.Lstat(current)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				// SECURITY: Non-existent components are safe to create later.
				// We only need to block already-existing symlink pivots.
				continue
			}
			return fmt.Errorf("%w: lstat %q: %w", errArchivePathValidationError, current, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: %q", errArchiveEntrySymlinkInPath, current)
		}
	}

	return nil
}

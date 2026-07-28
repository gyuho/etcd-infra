package file

import (
	"errors"
	"io/fs"
)

// File permissions.
const (
	// permFile is the default permission for files written atomically (0600).
	// Restrictive by default to avoid exposing sensitive data (PKI, credentials)
	// through a TOCTOU race window between file creation and chmod.
	permFile fs.FileMode = 0o600
)

// Common errors.
var (
	errNotRegularFile = errors.New("already exists and is not a regular file")
)

// Package file provides filesystem helpers used across etcd-infra.
//
// WHY NAME: "file" describes the domain: filesystem read/write/check utilities.
// WHY PATH: Under pkg/ so both internal/ lifecycle code and provider packages can reuse these helpers.
// OWNS: File existence checks, atomic writes, path helpers, and permission utilities.
// DOES NOT OWN: OS-level file locking (pkg/filelock) or archive extraction (pkg/archive).
package file

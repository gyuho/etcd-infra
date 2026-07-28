// Package shell provides shell-safe quoting utilities for constructing
// command strings from argument slices.
//
// WHY NAME:
// - "shell" describes the domain: POSIX shell argument quoting.
// - Not "quote", quoting is one operation; the package may grow.
//
// WHY PATH:
//   - Under pkg/ because compute providers (Hetzner SSH, AWS SSM, Docker exec)
//     and audit tooling all need shell-safe argument construction.
//
// OWNS:
// - Shell argument quoting (single-quote style, POSIX-compatible).
//
// DOES NOT OWN:
// - Command execution (pkg/providers/compute), SSH connections (pkg/ssh).
package shell

package compute

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

// Error variables for VM configuration operations.
var (
	ErrVMConfigUnsupportedOS   = errors.New("unsupported OS")
	ErrVMConfigUnsupportedArch = errors.New("unsupported architecture")
)

// Supported OS versions for compute instances.
const (
	OSUbuntu2404 = "ubuntu24.04"
	OSUbuntu2204 = "ubuntu22.04"
)

// SupportedOSVersions returns the list of valid OS version strings.
func SupportedOSVersions() []string {
	return []string{OSUbuntu2404, OSUbuntu2204}
}

// ValidateOSVersion validates that the given OS version is supported.
// Returns nil if valid, or an error describing valid options.
func ValidateOSVersion(os string) error {
	if slices.Contains(SupportedOSVersions(), os) {
		return nil
	}
	return fmt.Errorf("%w: %q; valid options: %s", ErrVMConfigUnsupportedOS, os, strings.Join(SupportedOSVersions(), ", "))
}

// Supported architectures for compute instances.
const (
	ArchX86_64  = "x86_64"
	ArchAarch64 = "aarch64"
	// ArchAMD64 is an alias for x86_64.
	ArchAMD64 = "amd64"
	// ArchARM64 is an alias for aarch64.
	ArchARM64 = "arm64"
)

// SupportedArchitectures returns the list of valid architecture strings
// (canonical forms only, not aliases).
func SupportedArchitectures() []string {
	return []string{ArchX86_64, ArchAarch64}
}

// SupportedArchitecturesWithAliases returns all valid architecture strings
// including aliases (amd64, arm64).
func SupportedArchitecturesWithAliases() []string {
	return []string{ArchX86_64, ArchAarch64, ArchAMD64, ArchARM64}
}

// NormalizeArch converts architecture aliases to their canonical form.
// For example, "amd64" becomes "x86_64" and "arm64" becomes "aarch64".
// Returns the input unchanged if it's already canonical or unrecognized.
func NormalizeArch(arch string) string {
	switch strings.ToLower(arch) {
	//nolint:goconst // Aliases are clear here.
	case "amd64", "x86_64":
		return ArchX86_64
	//nolint:goconst // Aliases are clear here.
	case "arm64", "aarch64":
		return ArchAarch64
	default:
		return arch
	}
}

// ValidateArch validates that the given architecture is supported.
// Accepts both canonical names (x86_64, aarch64) and aliases (amd64, arm64).
// Returns nil if valid, or an error describing valid options.
func ValidateArch(arch string) error {
	if slices.Contains(SupportedArchitectures(), NormalizeArch(arch)) {
		return nil
	}
	return fmt.Errorf("%w: %q; valid options: %s (aliases: %s, %s)",
		ErrVMConfigUnsupportedArch,
		arch,
		strings.Join(SupportedArchitectures(), ", "),
		ArchAMD64, ArchARM64)
}

// ValidateArchSupported validates that the given architecture is currently
// supported for cluster deployment. Only x86_64 is supported today; ARM
// (aarch64) support is planned but not yet available.
func ValidateArchSupported(arch string) error {
	normalized := NormalizeArch(arch)
	if normalized != ArchX86_64 {
		return fmt.Errorf("%w: %q; only %q is currently supported (ARM support is planned)",
			ErrVMConfigUnsupportedArch, arch, ArchX86_64)
	}
	return nil
}

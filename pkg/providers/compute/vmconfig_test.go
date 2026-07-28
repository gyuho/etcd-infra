package compute

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSupportedOSVersions(t *testing.T) {
	t.Parallel()

	versions := SupportedOSVersions()
	assert.Len(t, versions, 2)
	assert.Contains(t, versions, OSUbuntu2404)
	assert.Contains(t, versions, OSUbuntu2204)
}

func TestValidateOSVersion(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		os        string
		wantError bool
	}{
		{"valid ubuntu 24.04", OSUbuntu2404, false},
		{"valid ubuntu 22.04", OSUbuntu2204, false},
		{"invalid OS version", "ubuntu20.04", true},
		{"empty string", "", true},
		{"completely invalid", "windows11", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := ValidateOSVersion(tt.os)
			if tt.wantError {
				require.Error(t, err)
				require.ErrorIs(t, err, ErrVMConfigUnsupportedOS)
				assert.Contains(t, err.Error(), "valid options:")
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestSupportedArchitectures(t *testing.T) {
	t.Parallel()

	archs := SupportedArchitectures()
	assert.Len(t, archs, 2)
	assert.Contains(t, archs, ArchX86_64)
	assert.Contains(t, archs, ArchAarch64)
	// Should not contain aliases.
	assert.NotContains(t, archs, ArchAMD64)
	assert.NotContains(t, archs, ArchARM64)
}

func TestSupportedArchitecturesWithAliases(t *testing.T) {
	t.Parallel()

	archs := SupportedArchitecturesWithAliases()
	assert.Len(t, archs, 4)
	assert.Contains(t, archs, ArchX86_64)
	assert.Contains(t, archs, ArchAarch64)
	assert.Contains(t, archs, ArchAMD64)
	assert.Contains(t, archs, ArchARM64)
}

func TestNormalizeArch(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"amd64 to x86_64", "amd64", ArchX86_64},
		{"x86_64 unchanged", "x86_64", ArchX86_64},
		{"arm64 to aarch64", "arm64", ArchAarch64},
		{"aarch64 unchanged", "aarch64", ArchAarch64},
		{"uppercase AMD64", "AMD64", ArchX86_64},
		{"uppercase ARM64", "ARM64", ArchAarch64},
		{"mixed case X86_64", "X86_64", ArchX86_64},
		{"mixed case AArch64", "AArch64", ArchAarch64},
		{"unrecognized arch unchanged", "riscv64", "riscv64"},
		{"empty string unchanged", "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.expected, NormalizeArch(tt.input))
		})
	}
}

func TestValidateArch(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		arch      string
		wantError bool
	}{
		{"valid x86_64", ArchX86_64, false},
		{"valid aarch64", ArchAarch64, false},
		{"valid amd64 alias", ArchAMD64, false},
		{"valid arm64 alias", ArchARM64, false},
		{"uppercase AMD64", "AMD64", false},
		{"uppercase ARM64", "ARM64", false},
		{"mixed case X86_64", "X86_64", false},
		{"invalid architecture", "riscv64", true},
		{"empty string", "", true},
		{"completely invalid", "invalid", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := ValidateArch(tt.arch)
			if tt.wantError {
				require.Error(t, err)
				require.ErrorIs(t, err, ErrVMConfigUnsupportedArch)
				assert.Contains(t, err.Error(), "valid options:")
				assert.Contains(t, err.Error(), "aliases:")
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestValidateArchSupported(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		arch      string
		wantError bool
	}{
		{"x86_64 supported", ArchX86_64, false},
		{"amd64 alias supported", ArchAMD64, false},
		{"uppercase AMD64 supported", "AMD64", false},
		{"aarch64 not yet supported", ArchAarch64, true},
		{"arm64 not yet supported", ArchARM64, true},
		{"riscv64 not supported", "riscv64", true},
		{"empty string not supported", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := ValidateArchSupported(tt.arch)
			if tt.wantError {
				require.Error(t, err)
				require.ErrorIs(t, err, ErrVMConfigUnsupportedArch)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

package shell

import "strings"

// JoinArgs joins command arguments with proper shell quoting.
// Arguments containing spaces, newlines, or special characters are single-quoted.
func JoinArgs(args []string) string {
	if len(args) == 0 {
		return ""
	}

	quoted := make([]string, 0, len(args))
	for _, arg := range args {
		quoted = append(quoted, Quote(arg))
	}
	return strings.Join(quoted, " ")
}

// Quote returns a shell-safe quoted version of the string.
// Simple strings (alphanumeric, dash, `_`, dot, slash, equals, colon)
// are returned as-is. All others are wrapped in single quotes with embedded
// single quotes escaped in shell syntax.
func Quote(s string) string {
	if IsSimpleArg(s) {
		return s
	}

	// Use single quotes. Single quotes preserve everything literally,
	// except single quotes themselves which need to be escaped.
	// To include a single quote: end quote, add escaped quote, start quote again.
	// 'foo'\''bar' produces foo'bar
	escaped := strings.ReplaceAll(s, "'", `'\''`)
	return "'" + escaped + "'"
}

// IsValidPOSIXEnvName validates that a name conforms to the POSIX environment variable
// naming convention: [A-Za-z_][A-Za-z0-9_]*. This allowlist-based approach prevents
// shell metacharacters (;, $, backtick, |, etc.) from being injected into shell commands
// that interpolate the variable name.
func IsValidPOSIXEnvName(name string) bool {
	if name == "" {
		return false
	}
	// First character must be a letter or `_`.
	first := name[0]
	if (first < 'A' || first > 'Z') && (first < 'a' || first > 'z') && first != '_' {
		return false
	}
	// Remaining characters: letters, digits, or `_`.
	for i := 1; i < len(name); i++ {
		c := name[i]
		if (c < 'A' || c > 'Z') && (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '_' {
			return false
		}
	}
	return true
}

// IsSimpleArg returns true if the string doesn't need quoting.
// Allows alphanumeric, dash, `_`, dot, slash, equals, colon.
func IsSimpleArg(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		isAlpha := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
		isDigit := r >= '0' && r <= '9'
		isAllowed := r == '-' || r == '_' || r == '.' || r == '/' || r == '=' || r == ':'
		if !isAlpha && !isDigit && !isAllowed {
			return false
		}
	}
	return true
}

package shell

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestIsSimpleArg(t *testing.T) {
	t.Parallel()

	assert.True(t, IsSimpleArg("abc-_.=/"))
	assert.True(t, IsSimpleArg("abc"))
	assert.True(t, IsSimpleArg("/var/log/audit.log"))
	assert.True(t, IsSimpleArg("key=value"))
	assert.True(t, IsSimpleArg("host:port"))
	assert.False(t, IsSimpleArg("space here"))
	assert.False(t, IsSimpleArg(""))
	assert.False(t, IsSimpleArg("tab\there"))
	assert.False(t, IsSimpleArg("newline\nhere"))
	assert.False(t, IsSimpleArg("quote'here"))
}

func TestQuote(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "simple", input: "simple", want: "simple"},
		{name: "path", input: "/var/log/audit.log", want: "/var/log/audit.log"},
		{name: "space", input: "a b", want: "'a b'"},
		{name: "single_quote", input: "a'b", want: `'a'\''b'`},
		{name: "empty", input: "", want: "''"},
		{name: "newline", input: "a\nb", want: "'a\nb'"},
		{name: "tab", input: "a\tb", want: "'a\tb'"},
		{name: "special_chars", input: "hello world!", want: "'hello world!'"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, Quote(tt.input))
		})
	}
}

func TestIsValidPOSIXEnvName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  bool
	}{
		{name: "simple", input: "HOME", want: true},
		{name: "with_underscore", input: "MY_VAR", want: true},
		{name: "leading_underscore", input: "_PRIVATE", want: true},
		{name: "lowercase", input: "path", want: true},
		{name: "mixed_case_digits", input: "Var_123_abc", want: true},
		{name: "single_char", input: "X", want: true},
		{name: "empty", input: "", want: false},
		{name: "starts_with_digit", input: "1VAR", want: false},
		{name: "contains_dash", input: "MY-VAR", want: false},
		{name: "contains_dot", input: "MY.VAR", want: false},
		{name: "contains_space", input: "MY VAR", want: false},
		{name: "contains_equals", input: "MY=VAR", want: false},
		{name: "contains_semicolon", input: "MY;VAR", want: false},
		{name: "contains_dollar", input: "MY$VAR", want: false},
		{name: "contains_backtick", input: "MY`VAR", want: false},
		{name: "shell_injection_attempt", input: "x}; rm -rf /; ${y", want: false},
		{name: "newline", input: "MY\nVAR", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, IsValidPOSIXEnvName(tt.input))
		})
	}
}

func TestJoinArgs(t *testing.T) {
	t.Parallel()

	assert.Empty(t, JoinArgs(nil))
	assert.Empty(t, JoinArgs([]string{}))
	assert.Equal(t, "echo 'hello world'", JoinArgs([]string{"echo", "hello world"}))
	assert.Equal(t, "ls -la /tmp", JoinArgs([]string{"ls", "-la", "/tmp"}))
	assert.Equal(t, "bash -c 'echo hi'", JoinArgs([]string{"bash", "-c", "echo hi"}))
}

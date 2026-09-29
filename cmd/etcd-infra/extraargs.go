package main

import (
	"fmt"
	"os"
	"strings"
)

// extraArgsFileEnv names the environment variable that supplies the default
// --extra-args-file path, so scripts can point at a file without the path or
// the flags appearing on the command line.
const extraArgsFileEnv = "ETCD_INFRA_EXTRA_ARGS_FILE"

// parseExtraArgsFile reads extra etcd server arguments from a file, one
// argument per line. Blank lines and lines starting with '#' are ignored;
// every other line is passed verbatim (surrounding whitespace trimmed), so
// values containing spaces need no shell quoting. Keeping flags in a file
// outside the repository keeps experimental or private-branch flags out of
// shell history, scripts, and git.
func parseExtraArgsFile(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read extra args file %s: %w", path, err)
	}
	var args []string
	for lineNo, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if !strings.HasPrefix(line, "-") {
			return nil, fmt.Errorf("extra args file %s line %d: %q must be a single flag argument starting with '-' (one argument per line)", path, lineNo+1, line)
		}
		args = append(args, line)
	}
	return args, nil
}

// resolveExtraArgs merges --extra-args-file and --extra-args. File arguments
// come first so that a repeated inline flag overrides the file entry (etcd
// applies the last occurrence of a repeated flag).
func resolveExtraArgs(inline, file string) ([]string, error) {
	var args []string
	if strings.TrimSpace(file) != "" {
		fileArgs, err := parseExtraArgsFile(file)
		if err != nil {
			return nil, err
		}
		args = append(args, fileArgs...)
	}
	return append(args, strings.Fields(inline)...), nil
}

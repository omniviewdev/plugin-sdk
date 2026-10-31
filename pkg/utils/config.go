// Package utils provides SDK state-store access, RPC middleware composition, and legacy encoding helpers.
package utils

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ExpandTilde expands paths beginning with "~/" using the current home directory.
// Other inputs, including empty strings and bare "~", remain unchanged. HOME lookup errors are returned.
func ExpandTilde(path string) (string, error) {
	if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("failed to get home directory: %w", err)
		}
		path = filepath.Join(home, path[2:])
	}
	return path, nil
}

// DeexpandTilde replaces HOME or its lexical path-component prefix with "~".
// Other paths and failed HOME lookups remain unchanged; it does not resolve symlinks.
func DeexpandTilde(path string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		// fallback to the original path
		return path
	}
	if home == "" {
		return path
	}
	suffix, ok := strings.CutPrefix(path, home)
	if !ok || suffix != "" && !os.IsPathSeparator(suffix[0]) && !os.IsPathSeparator(home[len(home)-1]) {
		return path
	}
	return filepath.Join("~", suffix)
}

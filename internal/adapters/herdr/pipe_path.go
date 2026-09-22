package herdr

import (
	"strings"
)

// herdrPipePath converts a Herdr socket marker path into a valid Win32 named
// pipe path. On Windows, Herdr injects a filesystem-style path in
// HERDR_SOCKET_PATH (e.g. `C:\Users\<user>\AppData\Roaming\herdr\herdr.sock`),
// while the actual named pipe is `\\.\pipe\C:\Users\<user>\AppData\Roaming\herdr\herdr.sock`.
//
// It normalizes forward slashes to backslashes, preserves paths that already
// begin with the local named-pipe prefix, and strips extraneous leading
// backslashes to anchor the pipe strictly within the local machine namespace.
func herdrPipePath(path string) string {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return ""
	}
	// Normalize forward slashes to backslashes for Windows named pipe syntax.
	normalized := strings.ReplaceAll(trimmed, "/", `\`)
	// If it already starts with the local named pipe prefix, keep it as is.
	if strings.HasPrefix(strings.ToLower(normalized), `\\.\pipe\`) {
		return normalized
	}
	// Strip leading backslashes so we never produce invalid `\\.\pipe\\...` or
	// allow unanchored remote UNC paths to bypass local pipe binding.
	clean := strings.TrimLeft(normalized, `\`)
	return `\\.\pipe\` + clean
}

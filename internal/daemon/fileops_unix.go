//go:build !windows

package daemon

import "path/filepath"

// resolveExisting canonicalizes an existing path (symlinks, case,
// mount points) via the standard library.
func resolveExisting(path string) (string, error) {
	return filepath.EvalSymlinks(path)
}

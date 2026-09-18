//go:build !windows

package daemon

import (
	"os"
	"path/filepath"
	"syscall"
)

// resolveExisting canonicalizes an existing path (symlinks, case,
// mount points) via the standard library.
func resolveExisting(path string) (string, error) {
	return filepath.EvalSymlinks(path)
}

// openNoFollowPlatform opens path with O_NOFOLLOW so a trailing symlink is
// refused (ELOOP) instead of followed (issue #92).
func openNoFollowPlatform(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
}

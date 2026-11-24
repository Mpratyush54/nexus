//go:build windows

package platform

// RaiseFileLimit is a no-op on Windows, which has no per-process
// descriptor limit of this kind. It returns 0 (unknown).
func RaiseFileLimit() uint64 { return 0 }

// FileLimit returns 0 (unknown) on Windows.
func FileLimit() uint64 { return 0 }

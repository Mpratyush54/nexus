//go:build !windows

package platform

import "golang.org/x/sys/unix"

// RaiseFileLimit lifts the soft open-file limit toward the hard limit and
// returns the soft limit in force afterwards (0 when unknown).
//
// A Go executable does this by itself at startup, but a Go library loaded
// into another process (libnexuscore.dylib inside the desktop app) does not:
// macOS CI measured the soft limit unchanged at launchd's default of 256
// after loading the library. 256 descriptors are shared by the whole app,
// and kqueue file watching alone can use thousands.
func RaiseFileLimit() uint64 {
	var lim unix.Rlimit
	if err := unix.Getrlimit(unix.RLIMIT_NOFILE, &lim); err != nil {
		return 0
	}
	want := lim.Max
	// setrlimit refuses values above kern.maxfilesperproc on macOS, and
	// the hard limit there is usually "unlimited".
	if n := perProcessFileCap(); n > 0 && n < want {
		want = n
	}
	if want > lim.Cur {
		next := lim
		next.Cur = want
		if unix.Setrlimit(unix.RLIMIT_NOFILE, &next) == nil {
			lim.Cur = want
		}
	}
	return lim.Cur
}

// FileLimit returns the current soft open-file limit (0 when unknown).
func FileLimit() uint64 {
	var lim unix.Rlimit
	if err := unix.Getrlimit(unix.RLIMIT_NOFILE, &lim); err != nil {
		return 0
	}
	return lim.Cur
}

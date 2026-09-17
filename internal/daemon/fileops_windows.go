//go:build windows

package daemon

import (
	"fmt"
	"strings"
	"syscall"
	"unsafe"
)

var (
	modKernel32                   = syscall.NewLazyDLL("kernel32.dll")
	procGetFinalPathNameByHandleW = modKernel32.NewProc("GetFinalPathNameByHandleW")
)

// resolveExisting returns the canonical on-disk path for an existing file
// or directory: 8.3 short names expanded, symlinks followed, and —
// critically — junctions and mount points resolved.
//
// filepath.EvalSymlinks does not follow Windows junctions (a junction has
// no symlink bit, so it is invisible to Lstat-based detection too), which
// would let a `<root>/evil-dir -> C:\outside` junction escape the sandbox.
// GetFinalPathNameByHandleW asks the kernel for the true path instead.
// Stdlib-only: syscall ships with Go, no go.mod changes.
func resolveExisting(path string) (string, error) {
	p16, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return "", err
	}
	h, err := syscall.CreateFile(p16,
		syscall.GENERIC_READ,
		syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE|syscall.FILE_SHARE_DELETE,
		nil, syscall.OPEN_EXISTING, syscall.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return "", err
	}
	defer syscall.CloseHandle(h)

	buf := make([]uint16, 32768)
	ret, _, callErr := procGetFinalPathNameByHandleW.Call(
		uintptr(h),
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(len(buf)),
		uintptr(0), // VOLUME_NAME_DOS
	)
	n := int(ret)
	if n == 0 {
		return "", fmt.Errorf("daemon: final path: %v", callErr)
	}
	if n > len(buf) {
		return "", fmt.Errorf("daemon: final path too long")
	}
	s := syscall.UTF16ToString(buf[:n])
	// `\\?\C:\x` -> `C:\x`; `\\?\UNC\server\share` -> `\\server\share`.
	s = strings.TrimPrefix(s, `\\?\`)
	if rest, ok := strings.CutPrefix(s, `UNC\`); ok {
		s = `\\` + rest
	}
	return s, nil
}

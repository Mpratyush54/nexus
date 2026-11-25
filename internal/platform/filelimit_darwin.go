//go:build darwin

package platform

import "golang.org/x/sys/unix"

func perProcessFileCap() uint64 {
	n, err := unix.SysctlUint32("kern.maxfilesperproc")
	if err != nil {
		return 0
	}
	return uint64(n)
}

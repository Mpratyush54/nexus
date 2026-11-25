//go:build memharness && windows

package daemon

import "syscall"

// processCPUSeconds is the process's user+kernel CPU time.
func processCPUSeconds() float64 {
	h, err := syscall.GetCurrentProcess()
	if err != nil {
		return 0
	}
	var c, e, k, u syscall.Filetime
	if err := syscall.GetProcessTimes(h, &c, &e, &k, &u); err != nil {
		return 0
	}
	ticks := func(f syscall.Filetime) float64 { return float64(uint64(f.HighDateTime)<<32 | uint64(f.LowDateTime)) }
	return (ticks(k) + ticks(u)) / 1e7
}

//go:build !windows

package platform

import (
	"testing"

	"golang.org/x/sys/unix"
)

func TestRaiseFileLimitLiftsLowSoftLimit(t *testing.T) {
	var orig unix.Rlimit
	if err := unix.Getrlimit(unix.RLIMIT_NOFILE, &orig); err != nil {
		t.Skip(err)
	}
	t.Cleanup(func() { _ = unix.Setrlimit(unix.RLIMIT_NOFILE, &orig) })
	low := orig
	low.Cur = 256
	if low.Cur > orig.Max {
		t.Skip("hard limit below 256")
	}
	if err := unix.Setrlimit(unix.RLIMIT_NOFILE, &low); err != nil {
		t.Skip(err)
	}
	got := RaiseFileLimit()
	t.Logf("soft limit 256 -> %d (hard %d)", got, orig.Max)
	if orig.Max > 256 && got <= 256 {
		t.Fatalf("RaiseFileLimit left the soft limit at %d", got)
	}
	if FileLimit() != got {
		t.Fatalf("FileLimit() = %d, want %d", FileLimit(), got)
	}
}

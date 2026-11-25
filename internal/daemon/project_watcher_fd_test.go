//go:build !windows

package daemon

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/fsnotify/fsnotify"
	"golang.org/x/sys/unix"
)

func openFDs() int {
	// Probe each descriptor number: listing /dev/fd is unreliable on macOS.
	n := 0
	for fd := 0; fd < 65536; fd++ {
		if _, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0); err == nil {
			n++
		}
	}
	return n
}

// makeTree builds dirs folders of files files each under root.
func makeTree(t *testing.T, root string, dirs, files int) {
	t.Helper()
	for d := 0; d < dirs; d++ {
		dir := filepath.Join(root, "pkg", fmt.Sprintf("d%03d", d))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		for f := 0; f < files; f++ {
			if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("f%03d.go", f)), nil, 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func watchTree(t *testing.T, root string, budget int) (pw *projectWatcher, held, leftAfterClose int) {
	t.Helper()
	before := openFDs()
	w, err := fsnotify.NewWatcher()
	if err != nil {
		t.Fatal(err)
	}
	pw = &projectWatcher{root: root, w: w, watched: map[string]bool{}, budget: budget}
	pw.addTree(root)
	held = openFDs() - before
	_ = w.Close()
	leftAfterClose = openFDs() - before
	return pw, held, leftAfterClose
}

// On kqueue (macOS) fsnotify holds one descriptor per watched entry. This
// measures that cost and proves the watcher stays inside its budget. It
// opens thousands of descriptors, so it runs only when asked for
// (NEXUS_FD_PROBE=1), in its own test process, and never beside other tests.
func TestProjectWatcherDescriptorBudgetKqueue(t *testing.T) {
	if !watchCostsPerEntry {
		t.Skip("fsnotify does not spend a descriptor per entry on this OS")
	}
	if os.Getenv("NEXUS_FD_PROBE") != "1" {
		t.Skip("set NEXUS_FD_PROBE=1 to run (opens thousands of descriptors)")
	}
	var lim syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &lim); err == nil {
		t.Logf("RLIMIT_NOFILE soft=%d hard=%d", lim.Cur, lim.Max)
	}
	root := t.TempDir()
	const dirs, files = 60, 100 // 6,000 files + 62 folders
	makeTree(t, root, dirs, files)

	pw, held, left := watchTree(t, root, kqueueWatchBudget)
	t.Logf("budgeted: held %d descriptors (budget %d), capped=%v, %d still open after Close", held, kqueueWatchBudget, pw.capped, left)
	if left > 64 {
		t.Errorf("fsnotify left %d descriptors open after Close", left)
	}
	if held > kqueueWatchBudget+16 {
		t.Errorf("budgeted watcher held %d descriptors, budget is %d", held, kqueueWatchBudget)
	}
	if !pw.capped {
		t.Error("a tree over budget must be reported as capped (disables git caching)")
	}

	raw, held, left := watchTree(t, root, 0)
	t.Logf("unbudgeted (previous behaviour): %d folders / %d files held %d descriptors, capped=%v, %d still open after Close",
		dirs+2, dirs*files, held, raw.capped, left)
	if left > 64 {
		t.Errorf("fsnotify left %d descriptors open after Close (fsnotify < 1.10 kqueue leak)", left)
	}
}
